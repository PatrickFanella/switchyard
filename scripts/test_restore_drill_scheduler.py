import datetime as dt
import hashlib
import importlib.util
import json
import os
import pathlib
import subprocess
import tempfile
import threading
import time
import unittest
from unittest import mock


ROOT = pathlib.Path(__file__).resolve().parent.parent


def load(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    assert spec is not None
    module = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    spec.loader.exec_module(module)
    return module


restore_evidence = load("restore_evidence", ROOT / "scripts/restore_evidence.py")
import sys
sys.modules["restore_evidence"] = restore_evidence
scheduler = load("restore_drill_scheduler", ROOT / "scripts/restore_drill_scheduler.py")


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


class RestoreDrillSchedulerTest(unittest.TestCase):
    def setUp(self):
        self.testing = mock.patch.dict(os.environ, {"SWITCHYARD_RESTORE_SCHEDULER_TESTING": "1"})
        self.testing.start()
        self.temp = tempfile.TemporaryDirectory()
        self.root = pathlib.Path(self.temp.name)
        self.root.chmod(0o700)
        self.backup = self.root / "backup"
        self.evidence = self.root / "evidence"
        self.metrics = self.root / "metrics"
        for path in (self.backup, self.evidence, self.metrics):
            path.mkdir(mode=0o700)
        self.policy = self.root / "policy.json"
        self.policy.write_text(json.dumps({
            "contract_version": "switchyard.operator-policy.v1", "policy_id": "ops", "policy_version": 1,
            "provider": "postgres", "storage_owner": "operations", "encryption_owner": "security",
            "encryption_expectation": "host encrypted", "retention_duration": "P35D",
            "declared_rpo_seconds": 86400, "declared_rto_seconds": 60,
            "drill_calendar": "Sun *-*-01..07 03:00:00 UTC", "max_drill_interval_seconds": 100,
            "alert_grace_seconds": 5,
        }))
        self.values = {
            "SWITCHYARD_RESTORE_POLICY": str(self.policy),
            "SWITCHYARD_RESTORE_DRILL_EVIDENCE_DIR": str(self.evidence),
            "SWITCHYARD_BACKUP_DIR": str(self.backup),
            "SWITCHYARD_BACKUP_METRICS_DIR": str(self.metrics),
            "SWITCHYARD_RESTORE_DRILL_METRICS_FILE": str(self.metrics / "restore-drill.prom"),
            "SWITCHYARD_RESTORE_TIMEOUT_SECONDS": "10",
        }

    def tearDown(self):
        self.testing.stop()
        self.temp.cleanup()

    def bundle(self, name, created_at):
        archive = self.backup / f"{name}.dump"
        archived_policy = self.backup / f"{name}.policy.json"
        contract_path = self.backup / f"{name}.object-contract.json"
        metadata_path = self.backup / f"{name}.metadata.json"
        archive.write_bytes(name.encode())
        archived_policy.write_bytes(self.policy.read_bytes())
        contract_path.write_bytes(restore_evidence.OBJECT_CONTRACT.read_bytes())
        policy = json.loads(self.policy.read_text())
        contract = json.loads(contract_path.read_text())
        metadata = {
            "contract_version": "switchyard.postgres-backup-metadata.v1", "bundle_id": name,
            "provider": "postgres", "created_at": created_at, "source_revision": "a" * 40,
            "database_version": "170006", "archive_basename": archive.name, "archive_format": "postgres-custom",
            "archive_sha256": digest(archive), "policy_id": policy["policy_id"], "policy_version": 1,
            "policy_sha256": digest(archived_policy), "object_contract_version": contract["contract_version"],
            "object_contract_sha256": digest(contract_path), "migration_head": contract["migration_head"],
            "migration_ledger": contract["migration_ledger"],
        }
        metadata_path.write_text(json.dumps(metadata))
        manifest = self.backup / f"{name}.complete.json"
        manifest.write_text(json.dumps({
            "contract_version": "switchyard.completed-backup-bundle.v1", "bundle_id": name,
            "completed_at": created_at, "archive": {"basename": archive.name, "sha256": digest(archive)},
            "metadata": {"basename": metadata_path.name, "sha256": digest(metadata_path)},
            "policy": {"basename": archived_policy.name, "sha256": digest(archived_policy)},
            "object_contract": {"basename": contract_path.name, "sha256": digest(contract_path)},
        }))
        return manifest

    def test_selects_newest_valid_complete_metadata_and_ignores_partial_tampered(self):
        older = self.bundle("older", "2026-08-20T00:00:00Z")
        newest = self.bundle("newest", "2026-08-22T00:00:00Z")
        tampered = self.bundle("tampered", "2026-08-23T00:00:00Z")
        (self.backup / "tampered.dump").write_bytes(b"changed")
        (self.backup / "partial.dump").write_bytes(b"newer but incomplete")
        os.utime(older, (2_000_000_000, 2_000_000_000))
        self.assertEqual(scheduler.select_newest(self.backup), newest)

    def test_rejected_candidates_do_not_leak_descriptors(self):
        manifest = self.bundle("tampered", "2026-08-23T00:00:00Z")
        (self.backup / "tampered.dump").write_bytes(b"changed")
        before = len(list(pathlib.Path("/proc/self/fd").iterdir()))
        for _ in range(100):
            with self.assertRaises(restore_evidence.ContractError):
                restore_evidence.validate_bundle(manifest)
        after = len(list(pathlib.Path("/proc/self/fd").iterdir()))
        self.assertEqual(after, before)

    def test_concurrent_selection_is_stable(self):
        newest = self.bundle("newest", "2026-08-22T00:00:00Z")
        self.bundle("older", "2026-08-20T00:00:00Z")
        selected = []
        threads = [threading.Thread(target=lambda: selected.append(scheduler.select_newest(self.backup))) for _ in range(8)]
        for thread in threads:
            thread.start()
        for thread in threads:
            thread.join()
        self.assertEqual(selected, [newest] * 8)

    def test_selection_accepts_maximum_contract_bundle_id(self):
        manifest = self.bundle("current", "2026-08-22T00:00:00Z")
        renamed = self.backup / ("b" * 128 + ".complete.json")
        manifest.rename(renamed)
        self.assertEqual(scheduler.select_newest(self.backup), renamed)

    def test_replacement_between_selection_and_staging_records_attempt(self):
        manifest = self.bundle("current", "2026-08-22T00:00:00Z")
        selected = scheduler.select_newest_info(self.backup)

        def replace_after_selection(_):
            manifest.write_text("{}")
            return selected

        with mock.patch.object(scheduler, "select_newest_info", side_effect=replace_after_selection):
            self.assertEqual(scheduler.run(self.values, os.getuid(), "/bin/true", now=1000), 1)
        evidence = [json.loads(path.read_text()) for path in self.evidence.glob("restore-evidence-*.json")]
        self.assertEqual(len(evidence), 1)
        self.assertEqual(evidence[0]["result"], "failure")
        metrics = scheduler.parse_metrics(self.metrics / "restore-drill.prom")
        self.assertEqual(metrics["switchyard_restore_drill_last_attempt_timestamp_seconds"], 1000)
        self.assertEqual(metrics["switchyard_restore_drill_last_result"], 0)

    def test_metrics_initialize_then_preserve_success_across_failure(self):
        self.bundle("current", dt.datetime.now(dt.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"))
        scheduler.initialize(self.values, os.getuid(), now=1000)
        initial = scheduler.parse_metrics(self.metrics / "restore-drill.prom")
        self.assertEqual(initial, {"switchyard_restore_drill_next_deadline_timestamp_seconds": 1105})
        with mock.patch.object(scheduler, "exact_attempt_evidence", return_value=True):
            self.assertEqual(scheduler.run(self.values, os.getuid(), "/bin/true", now=1100), 0)
        self.assertEqual(scheduler.run(self.values, os.getuid(), "/bin/false", now=1200), 1)
        values = scheduler.parse_metrics(self.metrics / "restore-drill.prom")
        self.assertEqual(values["switchyard_restore_drill_last_success_timestamp_seconds"], 1100)
        self.assertEqual(values["switchyard_restore_drill_last_failure_timestamp_seconds"], 1200)
        self.assertEqual(values["switchyard_restore_drill_last_attempt_timestamp_seconds"], 1200)
        self.assertEqual(values["switchyard_restore_drill_last_result"], 0)
        self.assertEqual(values["switchyard_restore_drill_next_deadline_timestamp_seconds"], 1305)

    def test_future_metrics_do_not_move_current_attempt_or_deadline(self):
        self.bundle("current", "2026-08-22T00:00:00Z")
        metrics = self.metrics / "restore-drill.prom"
        metrics.write_text(
            "switchyard_restore_drill_last_attempt_timestamp_seconds 9999999999\n"
            "switchyard_restore_drill_last_success_timestamp_seconds 900\n"
            "switchyard_restore_drill_next_deadline_timestamp_seconds 9999999999\n")
        metrics.chmod(0o600)
        with mock.patch.object(scheduler, "exact_attempt_evidence", return_value=True):
            self.assertEqual(scheduler.run(self.values, os.getuid(), "/bin/true", now=1000), 0)
        values = scheduler.parse_metrics(self.metrics / "restore-drill.prom")
        self.assertEqual(values["switchyard_restore_drill_last_attempt_timestamp_seconds"], 1000)
        self.assertEqual(values["switchyard_restore_drill_next_deadline_timestamp_seconds"], 1105)
        self.assertEqual(values["switchyard_restore_drill_last_success_timestamp_seconds"], 1000)

    def test_initialize_clamps_small_skew_and_drops_untrusted_history(self):
        metrics = self.metrics / "restore-drill.prom"
        metrics.write_text(
            "switchyard_restore_drill_last_success_timestamp_seconds 1001\n"
            "switchyard_restore_drill_last_failure_timestamp_seconds 9999999999\n"
            "switchyard_restore_drill_last_attempt_timestamp_seconds 9999999999\n")
        metrics.chmod(0o600)
        scheduler.initialize(self.values, os.getuid(), now=1000)
        self.assertEqual(scheduler.parse_metrics(metrics), {
            "switchyard_restore_drill_last_success_timestamp_seconds": 1000,
            "switchyard_restore_drill_next_deadline_timestamp_seconds": 1105,
        })

    def test_policy_mutation_after_staging_cannot_change_child_or_deadline(self):
        self.bundle("current", "2026-08-22T00:00:00Z")
        original = self.policy.read_bytes()
        original_digest = hashlib.sha256(original).hexdigest()
        captured = self.root / "captured-policy.json"
        command = self.root / "capture-policy"
        command.write_text(
            "#!/usr/bin/env python3\n"
            "import json, pathlib, stat, sys\n"
            "p = pathlib.Path(sys.argv[sys.argv.index('--policy') + 1])\n"
            f"pathlib.Path({str(captured)!r}).write_text(json.dumps({{'bytes': p.read_text(), 'path': str(p), 'mode': stat.S_IMODE(p.stat().st_mode)}}))\n")
        command.chmod(0o700)
        selected = scheduler.select_newest_info(self.backup)

        def mutate_policy(_):
            value = json.loads(self.policy.read_text())
            value["max_drill_interval_seconds"] = 999
            self.policy.write_text(json.dumps(value))
            return selected

        with mock.patch.object(scheduler, "select_newest_info", side_effect=mutate_policy), mock.patch.object(
                scheduler, "exact_attempt_evidence", return_value=True) as exact:
            self.assertEqual(scheduler.run(self.values, os.getuid(), str(command), now=1000), 0)
        child = json.loads(captured.read_text())
        self.assertEqual(child["bytes"].encode(), original)
        self.assertEqual(child["mode"], 0o400)
        self.assertNotEqual(child["path"], str(self.policy))
        self.assertFalse(pathlib.Path(child["path"]).exists())
        self.assertEqual(exact.call_args.args[-1], original_digest)
        self.assertEqual(scheduler.parse_metrics(self.metrics / "restore-drill.prom")[
            "switchyard_restore_drill_next_deadline_timestamp_seconds"], 1105)

    def test_timeout_terminates_entire_new_process_group(self):
        state = self.root / "processes.json"
        command = self.root / "process-tree"
        command.write_text(
            "#!/usr/bin/env python3\n"
            "import json, os, pathlib, signal, subprocess, sys, time\n"
            "signal.signal(signal.SIGTERM, signal.SIG_IGN)\n"
            "child = subprocess.Popen([sys.executable, '-c', 'import signal,time; signal.signal(signal.SIGTERM, signal.SIG_IGN); time.sleep(60)'])\n"
            f"pathlib.Path({str(state)!r}).write_text(json.dumps({{'parent': os.getpid(), 'sid': os.getsid(0), 'child': child.pid, 'child_pgid': os.getpgid(child.pid)}}))\n"
            "time.sleep(60)\n")
        command.chmod(0o700)
        with self.assertRaises(subprocess.TimeoutExpired):
            scheduler.run_command([str(command)], 0.2)
        processes = json.loads(state.read_text())
        self.assertEqual(processes["parent"], processes["sid"])
        self.assertEqual(processes["parent"], processes["child_pgid"])
        for pid in (processes["parent"], processes["child"]):
            with self.assertRaises(ProcessLookupError):
                os.kill(pid, 0)

    def test_selection_failure_keeps_unique_failure_evidence_and_no_clobber(self):
        for attempted in (1000, 1001):
            self.assertEqual(scheduler.run(self.values, os.getuid(), "/bin/true", now=attempted), 1)
        files = list(self.evidence.glob("restore-evidence-*.json"))
        self.assertEqual(len(files), 2)
        for path in files:
            value = json.loads(path.read_text())
            self.assertEqual((value["stage"], value["error_code"]), ("selection", "no_complete_bundle"))
            self.assertNotIn(str(self.root), path.read_text())

    def test_unrelated_new_file_does_not_suppress_failure_evidence(self):
        self.bundle("current", "2026-08-22T00:00:00Z")
        command = self.root / "fake-restore"
        command.write_text("#!/usr/bin/env python3\nimport pathlib, sys\np = pathlib.Path(sys.argv[sys.argv.index('--evidence-dir') + 1])\n(p / 'unrelated-entry').touch()\nraise SystemExit(1)\n")
        command.chmod(0o700)
        self.assertEqual(scheduler.run(self.values, os.getuid(), str(command), now=1000), 1)
        self.assertEqual(len(list(self.evidence.glob("restore-evidence-*.json"))), 1)

    def test_attempt_id_is_passed_and_peer_evidence_cannot_satisfy_attempt(self):
        self.bundle("current", "2026-08-22T00:00:00Z")
        peer = {
            "contract_version": "switchyard.restore-evidence.v1", "attempt_id": "peer-attempt",
            "started_at": "2026-08-23T00:00:00Z", "finished_at": "2026-08-23T00:00:01Z",
            "provider": "postgres", "result": "failure", "stage": "restore", "error_code": "adapter_failed",
            "bundle": None, "qualification": None, "provider_payload": None,
        }
        (self.evidence / "restore-evidence-peer-attempt.json").write_text(json.dumps(peer))
        captured = self.root / "arguments.json"
        command = self.root / "fake-restore"
        command.write_text(f"#!/usr/bin/env python3\nimport json, pathlib, sys\npathlib.Path({str(captured)!r}).write_text(json.dumps(sys.argv))\nraise SystemExit(1)\n")
        command.chmod(0o700)
        self.assertEqual(scheduler.run(self.values, os.getuid(), str(command), now=1000), 1)
        arguments = json.loads(captured.read_text())
        attempt_id = arguments[arguments.index("--attempt-id") + 1]
        self.assertTrue(attempt_id.startswith("scheduled-"))
        own = self.evidence / f"restore-evidence-{attempt_id}.json"
        self.assertTrue(own.exists())
        self.assertEqual(json.loads(own.read_text())["attempt_id"], attempt_id)
        self.assertIn("--started-at", arguments)

    def test_concurrent_attempt_records_only_alternate_evidence(self):
        metrics_path = self.metrics / "restore-drill.prom"
        metrics_path.write_text("switchyard_restore_drill_last_attempt_timestamp_seconds 777\n")
        metrics_path.chmod(0o600)
        lock = scheduler.acquire_lock(scheduler.Sink(self.metrics, metrics_path))
        try:
            self.assertEqual(scheduler.run(self.values, os.getuid(), "/bin/true", now=1000), 1)
        finally:
            os.close(lock)
        self.assertEqual(metrics_path.read_text(), "switchyard_restore_drill_last_attempt_timestamp_seconds 777\n")
        evidence = [json.loads(path.read_text()) for path in self.evidence.glob("restore-evidence-*.json")]
        self.assertEqual(len(evidence), 1)
        self.assertEqual(evidence[0]["error_code"], "concurrent_attempt")

    def test_evidence_write_error_does_not_suppress_metrics(self):
        with mock.patch.object(scheduler, "write_scheduler_failure", side_effect=OSError):
            self.assertEqual(scheduler.run(self.values, os.getuid(), "/bin/true", now=1000), 1)
        metrics = scheduler.parse_metrics(self.metrics / "restore-drill.prom")
        self.assertEqual(metrics["switchyard_restore_drill_last_attempt_timestamp_seconds"], 1000)
        self.assertEqual(metrics["switchyard_restore_drill_last_result"], 0)

    def test_metrics_write_error_does_not_suppress_evidence(self):
        with mock.patch.object(scheduler, "write_metrics", side_effect=OSError):
            self.assertEqual(scheduler.run(self.values, os.getuid(), "/bin/true", now=1000), 1)
        evidence = [json.loads(path.read_text()) for path in self.evidence.glob("restore-evidence-*.json")]
        self.assertEqual(len(evidence), 1)
        self.assertEqual(evidence[0]["error_code"], "no_complete_bundle")

    def test_deadline_covers_selection_and_finalizes_failure(self):
        def slow_selection(_):
            time.sleep(2)

        with mock.patch.object(scheduler, "attempt_timeout_seconds", return_value=1), mock.patch.object(
                scheduler, "select_newest_info", side_effect=slow_selection):
            self.assertEqual(scheduler.run(self.values, os.getuid(), "/bin/true", now=1000), 1)
        evidence = [json.loads(path.read_text()) for path in self.evidence.glob("restore-evidence-*.json")]
        self.assertEqual(evidence[0]["error_code"], "scheduler_timeout")
        metrics = scheduler.parse_metrics(self.metrics / "restore-drill.prom")
        self.assertEqual(metrics["switchyard_restore_drill_last_result"], 0)

    def test_preflight_rejects_overlap_symlink_and_wrong_metrics_name(self):
        bad = dict(self.values, SWITCHYARD_RESTORE_DRILL_EVIDENCE_DIR=str(self.backup))
        with self.assertRaises(scheduler.SchedulerError):
            scheduler.preflight(bad, os.getuid())
        linked = self.root / "linked"
        linked.symlink_to(self.evidence, target_is_directory=True)
        bad = dict(self.values, SWITCHYARD_RESTORE_DRILL_EVIDENCE_DIR=str(linked))
        with self.assertRaises(Exception):
            scheduler.preflight(bad, os.getuid())
        bad = dict(self.values, SWITCHYARD_RESTORE_DRILL_METRICS_FILE=str(self.metrics / "other.prom"))
        with self.assertRaises(scheduler.SchedulerError):
            scheduler.preflight(bad, os.getuid())

    def test_runtime_uid_defaults_and_rejects_invalid_values(self):
        self.assertEqual(scheduler.runtime_uid({}), 1000)
        for raw in ("0", "-1", "user", "2147483648", "１２３"):
            with self.subTest(raw=raw), self.assertRaises(scheduler.SchedulerError):
                scheduler.runtime_uid({"OPENCODE_RUNTIME_UID": raw})

    def test_preflight_cli_returns_only_bounded_render_values(self):
        env_file = self.root / "restore.env"
        values = dict(self.values, OPENCODE_RUNTIME_UID="12345")
        env_file.write_text("".join(f"{key}={value}\n" for key, value in values.items()))
        result = subprocess.run([
            "python3", str(ROOT / "scripts/restore_drill_scheduler.py"), "preflight",
            "--env-file", str(env_file), "--owner-uid", str(os.getuid()),
        ], text=True, capture_output=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.splitlines(), ["Sun *-*-01..07 03:00:00 UTC", "210", "12345"])
        self.assertEqual(result.stderr, "")

    def test_command_output_contains_no_paths_or_credentials(self):
        env = os.environ.copy()
        env.update(self.values)
        env["DATABASE_URL"] = "postgres://operator:private-value@production/db"
        result = subprocess.run(["python3", str(ROOT / "scripts/restore_drill_scheduler.py"), "run"],
                                env=env, text=True, capture_output=True)
        self.assertEqual(result.returncode, 1)
        output = result.stdout + result.stderr
        self.assertNotIn(str(self.root), output)
        self.assertNotIn("private-value", output)
        self.assertNotIn("production", output)


if __name__ == "__main__":
    unittest.main()
