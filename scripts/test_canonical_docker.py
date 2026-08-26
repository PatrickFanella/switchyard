import json
import os
import pathlib
import re
import subprocess
import tempfile
import textwrap
import unittest


ROOT = pathlib.Path(__file__).resolve().parent.parent


class CanonicalDockerTest(unittest.TestCase):
    def test_hostile_environment_cannot_redirect_docker(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            recorder = root / "canonical-docker"
            record = root / "record.json"
            recorder.write_text(
                "#!/usr/bin/python3\n"
                "import json, os, pathlib, sys\n"
                f"pathlib.Path({str(record)!r}).write_text(json.dumps({{"
                "'argv': sys.argv[1:], 'env': dict(os.environ)}))\n"
            )
            recorder.chmod(0o700)
            wrapper = root / "docker-local"
            wrapper.write_text(
                (ROOT / "deploy/docker-local").read_text().replace(
                    "/usr/bin/docker", str(recorder)
                )
            )
            wrapper.chmod(0o700)
            hostile_bin = root / "hostile-bin"
            hostile_bin.mkdir()
            hostile_docker = hostile_bin / "docker"
            hostile_docker.write_text("#!/bin/sh\nexit 99\n")
            hostile_docker.chmod(0o700)
            hostile_config = root / "hostile-config"
            hostile_config.mkdir()
            (hostile_config / "config.json").write_text(
                '{"currentContext":"hostile","cliPluginsExtraDirs":["/hostile"]}'
            )
            startup_marker = root / "startup-marker"
            startup = root / "startup.sh"
            startup.write_text(f"touch {startup_marker}\n")
            env = os.environ.copy()
            env.update({
                "PATH": str(hostile_bin),
                "DOCKER_HOST": "tcp://hostile.invalid:2375",
                "DOCKER_CONTEXT": "hostile",
                "DOCKER_CONFIG": str(hostile_config),
                "TMPDIR": str(hostile_config),
                "BASH_ENV": str(startup),
                "ENV": str(startup),
            })

            result = subprocess.run(
                [str(wrapper), "compose", "up", "-d"], env=env, capture_output=True
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            invocation = json.loads(record.read_text())
            self.assertEqual(
                invocation["argv"][2:], ["--host", "unix:///var/run/docker.sock", "compose", "up", "-d"]
            )
            self.assertEqual(invocation["argv"][0], "--config")
            self.assertNotEqual(invocation["argv"][1], str(hostile_config))
            self.assertEqual(invocation["env"]["HOME"], "/nonexistent")
            self.assertEqual(
                invocation["env"]["PATH"],
                "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
            )
            self.assertFalse(any(name.startswith("DOCKER_") for name in invocation["env"]))
            self.assertNotIn("COMPOSE_FILE", invocation["env"])
            self.assertFalse(startup_marker.exists())

    def test_all_production_paths_use_the_boundary(self):
        boundary = "deploy/docker-local"
        main = (ROOT / "deploy/switchyard.service").read_text()
        self.assertEqual(main.count(boundary), 5)
        for path in ("scripts/backup-postgres.sh", "scripts/restore-postgres-drill.sh"):
            text = (ROOT / path).read_text()
            self.assertIn('docker() { "$root/deploy/docker-local" "$@"; }', text)
        for path in ("deploy/install-systemd.sh", "deploy/install-web-gate.sh"):
            text = (ROOT / path).read_text()
            self.assertIn('docker_local="$repo_root/deploy/docker-local"', text)
            self.assertNotRegex(text, r"(?m)(^|[^_])docker compose")
            compose_calls = [line for line in text.splitlines() if '"$docker_local" compose' in line]
            self.assertTrue(compose_calls)
            for call in compose_calls:
                if "compose version" not in call:
                    self.assertIn('--env-file "$env_file"', call, path)
                    self.assertIn('-f "$repo_root/docker-compose.yml"', call, path)
        production_paths = (
            "deploy/install-systemd.sh",
            "deploy/install-web-gate.sh",
            "deploy/switchyard.service",
            "deploy/switchyard-backup.service",
            "deploy/switchyard-restore-drill.service",
        )
        direct = re.compile(r"(?m)^\s*(?:exec\s+)?(?:/usr/bin/)?docker(?:\s|$)")
        for path in production_paths:
            self.assertIsNone(direct.search((ROOT / path).read_text()), path)

    def test_service_compose_calls_pin_file_and_env(self):
        unit = (ROOT / "deploy/switchyard.service").read_text()
        calls = [line for line in unit.splitlines() if "docker-local compose" in line]
        self.assertEqual(len(calls), 5)
        for call in calls:
            self.assertIn("--env-file __ENV_FILE__", call)
            self.assertIn("-f __REPO_ROOT__/docker-compose.yml", call)

    def test_backup_uses_external_env_and_canonical_file(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            (root / "scripts").mkdir()
            (root / "deploy").mkdir()
            (root / "docker-compose.yml").write_text("services: {}\n")
            backup = root / "backup"
            backup.mkdir()
            env_file = root / "external.env"
            env_file.write_text("POSTGRES_PASSWORD=external\n")
            policy = root / "policy.json"
            policy.write_text("{}")
            record = root / "docker-record.jsonl"
            docker = root / "deploy/docker-local"
            docker.write_text(textwrap.dedent(f'''\
                #!/usr/bin/env python3
                import json, pathlib, sys
                record = pathlib.Path({str(record)!r})
                with record.open("a") as handle:
                    handle.write(json.dumps(sys.argv[1:]) + "\\n")
                command = " ".join(sys.argv)
                if "pg_dump" in command:
                    print("archive")
                elif "show server_version_num" in command:
                    print("170000")
                elif "schema_migrations" in command:
                    print("1")
                '''))
            docker.chmod(0o700)
            evidence = root / "scripts/restore_evidence.py"
            evidence.write_text(textwrap.dedent('''\
                import pathlib, sys
                if sys.argv[1] == "publish-bundle":
                    print("published")
                elif sys.argv[1] == "prepare-directory":
                    pathlib.Path(sys.argv[3]).mkdir(parents=True, exist_ok=True)
                '''))
            script = root / "scripts/backup-postgres.sh"
            script.write_bytes((ROOT / "scripts/backup-postgres.sh").read_bytes())
            script.chmod(0o700)
            fake_bin = root / "bin"
            fake_bin.mkdir()
            git = fake_bin / "git"
            git.write_text("#!/bin/sh\nprintf '%040d\\n' 1\n")
            git.chmod(0o700)
            env = os.environ.copy()
            env.update({
                "PATH": f"{fake_bin}:{env['PATH']}",
                "SWITCHYARD_BACKUP_DIR": str(backup),
                "SWITCHYARD_BACKUP_POLICY": str(policy),
                "SWITCHYARD_ENV_FILE": str(env_file),
                "COMPOSE_FILE": str(root / "hostile.yml"),
            })
            result = subprocess.run([str(script)], env=env, text=True, capture_output=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            calls = [json.loads(line) for line in record.read_text().splitlines()]
            self.assertEqual(len(calls), 5)
            for call in calls:
                self.assertEqual(call[:2], ["compose", "--env-file"])
                self.assertEqual(call[2], str(env_file))
                self.assertEqual(call[3:5], ["-f", str(root / "docker-compose.yml")])

    def test_production_images_need_no_registry_auth(self):
        compose = (ROOT / "docker-compose.yml").read_text()
        self.assertRegex(compose, r"(?m)^    image: postgres:17@sha256:")
        self.assertEqual(len(re.findall(r"(?m)^    image:", compose)), 1)
        for service in ("api", "opencode", "web", "verifier"):
            self.assertRegex(compose, rf"(?ms)^  {service}:\n.*?^    build:")
        allowed_registries = {"docker.gitea.com"}
        dockerfiles = (
            ROOT / "backend/Dockerfile",
            ROOT / "web/Dockerfile",
            ROOT / "services/opencode/Dockerfile",
            ROOT / "deploy/runner/Dockerfile",
        )
        for dockerfile in dockerfiles:
            stages = set()
            for line in dockerfile.read_text().splitlines():
                match = re.match(r"FROM\s+(\S+)(?:\s+AS\s+(\S+))?$", line, re.IGNORECASE)
                if not match:
                    continue
                image, stage = match.groups()
                if image not in stages:
                    self.assertIn("@sha256:", image, dockerfile)
                    if "/" in image:
                        self.assertIn(image.split("/", 1)[0], allowed_registries, dockerfile)
                if stage:
                    stages.add(stage)
        readme = (ROOT / "README.md").read_text()
        self.assertIn("anonymously pullable public images", readme)
        self.assertIn("/usr/libexec/docker/cli-plugins/docker-compose", readme)


if __name__ == "__main__":
    unittest.main()
