import json
import os
import pathlib
import subprocess
import tempfile
import textwrap
import time
import unittest


ROOT = pathlib.Path(__file__).resolve().parent.parent


class RestoreShellSafetyTest(unittest.TestCase):
    def test_shell_syntax(self):
        subprocess.run(
            ["bash", "-n", str(ROOT / "scripts/backup-postgres.sh"), str(ROOT / "scripts/restore-postgres-drill.sh"), str(ROOT / "scripts/test-restore-postgres-integration.sh")],
            check=True,
        )

    def test_adapter_has_isolated_randomized_exact_owned_resources(self):
        text = (ROOT / "scripts/restore-postgres-drill.sh").read_text(encoding="utf-8")
        self.assertIn("timeout --signal=TERM --kill-after=15", text)
        for label in ("owner", "type", "attempt", "created", "reclaim_after"):
            self.assertIn(f"switchyard.restore.{label}", text)
        self.assertIn("trap cleanup EXIT", text)
        self.assertIn("lock_name=switchyard-postgres-restore-lock", text)
        self.assertIn('docker create --name "$lock_name"', text)
        self.assertNotIn('docker rm -f "$lock_name"', text)
        self.assertNotIn('docker rm -f "$api_name"', text)
        self.assertNotIn('docker rm -f "$name"', text)
        self.assertIn("created_epoch - owner_created > reclaim_seconds", text)
        self.assertNotIn("State.Status", text)
        self.assertIn("docker network create --internal", text)
        self.assertIn('--network "$network_name"', text)
        self.assertNotIn("--network host", text)
        self.assertNotIn("--publish", text)
        self.assertIn("secrets.token_urlsafe(32)", text)
        self.assertIn('DATABASE_URL=host=$name port=5432', text)
        self.assertIn('docker exec "$api_id" wget', text)
        self.assertNotIn("restore-drill-token", text)
        self.assertNotIn("switchyard-restore-disposable", text)
        self.assertNotIn("POSTGRES_DB=${", text)
        self.assertNotIn("DATABASE_URL=${", text)

        integration = (ROOT / "scripts/test-restore-postgres-integration.sh").read_text(encoding="utf-8")
        self.assertIn("docker network create --internal", integration)
        self.assertIn("secrets.token_urlsafe(32)", integration)
        self.assertNotIn("--publish", integration)
        self.assertNotIn("--network host", integration)
        self.assertNotIn("fixture-token", integration)
        self.assertNotIn("fixture-disposable", integration)

    def test_backup_publishes_manifest_before_success_metric(self):
        text = (ROOT / "scripts/backup-postgres.sh").read_text(encoding="utf-8")
        self.assertLess(text.index("publish-bundle"), text.index("switchyard_postgres_backup_last_success"))
        self.assertIn("SWITCHYARD_BACKUP_POLICY", text)

    def fake_docker(self, root):
        fake_bin = root / "bin"
        fake_bin.mkdir()
        state = root / "state"
        state.mkdir()
        docker = fake_bin / "docker"
        docker.write_text(textwrap.dedent(r'''#!/usr/bin/env python3
import json, os, pathlib, shutil, sys, time
state = pathlib.Path(os.environ["FAKE_DOCKER_STATE"])
command, args = sys.argv[1], sys.argv[2:]
lock = state / "lock"
details_file = lock / "details.json"

def labels():
    result = {}
    for index, arg in enumerate(args):
        if arg == "--label" and index + 1 < len(args):
            key, value = args[index + 1].split("=", 1)
            result[key] = value
    return result

def current():
    return json.loads(details_file.read_text())

if command == "create":
    try:
        lock.mkdir()
    except FileExistsError:
        raise SystemExit(1)
    value = labels()
    value["id"] = "lock-" + value["switchyard.restore.attempt"]
    details_file.write_text(json.dumps(value))
    print(value["id"])
elif command == "start":
    pass
elif command == "inspect":
    target = args[-1]
    if target == "foreign-id":
        print("foreign api foreign-attempt 1 2")
    elif details_file.exists() and target in ("switchyard-postgres-restore-lock", current()["id"]):
        value = current()
        fields = [value["switchyard.restore.owner"], value["switchyard.restore.type"],
                  value["switchyard.restore.attempt"], value["switchyard.restore.created"],
                  value["switchyard.restore.reclaim_after"]]
        if any(".Id" in arg for arg in args): fields.insert(0, value["id"])
        print(" ".join(fields))
    else:
        raise SystemExit(1)
elif command == "rm":
    target = args[-1]
    with (state / "removals").open("a") as handle: handle.write(target + "\n")
    if details_file.exists() and target == current()["id"]: shutil.rmtree(lock)
elif command == "ps":
    time.sleep(float(os.environ.get("FAKE_DOCKER_PS_SLEEP", "0")))
    if os.environ.get("FAKE_DOCKER_INCLUDE_FOREIGN") == "1": print("foreign-id")
elif command == "network":
    if args[0] == "ls": pass
    elif args[0] == "create": raise SystemExit(1)
    else: raise SystemExit(1)
elif command == "image":
    if args[0] == "ls": pass
    else: raise SystemExit(1)
elif command == "info":
    pass
else:
    raise SystemExit(1)
'''), encoding="utf-8")
        docker.chmod(0o700)
        return fake_bin, state

    def adapter_inputs(self, root):
        files = []
        for name in ("archive", "metadata", "contract"):
            path = root / name
            path.write_text("{}", encoding="utf-8")
            files.append(path)
        return files

    def test_fresh_running_lock_blocks_without_cross_delete(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            fake_bin, state = self.fake_docker(root)
            files = self.adapter_inputs(root)
            first_payload = root / "first.json"
            second_payload = root / "second.json"
            env = os.environ.copy()
            env.update(PATH=f"{fake_bin}:{env['PATH']}", FAKE_DOCKER_STATE=str(state),
                       SWITCHYARD_RESTORE_ADAPTER_INNER="1", FAKE_DOCKER_PS_SLEEP="3")
            adapter = str(ROOT / "scripts/restore-postgres-drill.sh")
            first = subprocess.Popen([adapter, *(str(path) for path in files), str(first_payload), "first", "100"], env=env)
            for _ in range(100):
                if (state / "lock/details.json").exists():
                    break
                time.sleep(0.01)
            try:
                second = subprocess.run([adapter, *(str(path) for path in files), str(second_payload), "second", "100"], env=env, check=False)
                self.assertNotEqual(second.returncode, 0)
                self.assertEqual(json.loads(second_payload.read_text())["error_code"], "restore_already_running")
                self.assertFalse((state / "removals").exists(), "contender deleted the active lock")
            finally:
                first.wait(timeout=10)

    def test_sigkill_and_timeout_leave_next_attempt_recoverable(self):
        for mode in ("sigkill", "timeout"):
            with self.subTest(mode=mode), tempfile.TemporaryDirectory() as temporary:
                root = pathlib.Path(temporary)
                fake_bin, state = self.fake_docker(root)
                files = self.adapter_inputs(root)
                env = os.environ.copy()
                env.update(PATH=f"{fake_bin}:{env['PATH']}", FAKE_DOCKER_STATE=str(state), FAKE_DOCKER_PS_SLEEP="30")
                adapter = str(ROOT / "scripts/restore-postgres-drill.sh")
                first_payload = root / "first.json"
                if mode == "sigkill":
                    inner_env = dict(env, SWITCHYARD_RESTORE_ADAPTER_INNER="1")
                    first = subprocess.Popen([adapter, *(str(path) for path in files), str(first_payload), "first", "1"], env=inner_env)
                    for _ in range(100):
                        if (state / "lock/details.json").exists(): break
                        time.sleep(0.01)
                    first.kill()
                    first.wait(timeout=5)
                    time.sleep(2)
                else:
                    first = subprocess.run([adapter, *(str(path) for path in files), str(first_payload), "first", "1", "100"], env=env, timeout=5, check=False)
                    self.assertNotEqual(first.returncode, 0)
                env.pop("FAKE_DOCKER_PS_SLEEP")
                next_payload = root / "next.json"
                inner_env = dict(env, SWITCHYARD_RESTORE_ADAPTER_INNER="1")
                next_run = subprocess.run([adapter, *(str(path) for path in files), str(next_payload), "next", "1"], env=inner_env, check=False)
                self.assertNotEqual(next_run.returncode, 0)
                self.assertNotEqual(json.loads(next_payload.read_text())["error_code"], "restore_already_running")

    def test_partial_foreign_labels_are_never_deleted(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            fake_bin, state = self.fake_docker(root)
            files = self.adapter_inputs(root)
            payload = root / "payload.json"
            env = os.environ.copy()
            env.update(PATH=f"{fake_bin}:{env['PATH']}", FAKE_DOCKER_STATE=str(state),
                       SWITCHYARD_RESTORE_ADAPTER_INNER="1", FAKE_DOCKER_INCLUDE_FOREIGN="1")
            result = subprocess.run([str(ROOT / "scripts/restore-postgres-drill.sh"), *(str(path) for path in files),
                                     str(payload), "owner", "100"], env=env, check=False)
            self.assertNotEqual(result.returncode, 0)
            removals = (state / "removals").read_text().splitlines()
            self.assertNotIn("foreign-id", removals)


if __name__ == "__main__":
    unittest.main()
