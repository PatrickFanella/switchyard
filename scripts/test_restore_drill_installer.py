import json
import os
import pathlib
import pwd
import grp
import subprocess
import tempfile
import unittest


ROOT = pathlib.Path(__file__).resolve().parent.parent


class RestoreDrillInstallerTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = pathlib.Path(self.temp.name)
        self.root.chmod(0o700)
        self.units = self.root / "units"
        self.units.mkdir(mode=0o700)
        self.bin = self.root / "bin"
        self.bin.mkdir(mode=0o700)
        self.log = self.root / "mutations.log"
        uid = os.getuid()
        self.runtime_uid = uid or 1000
        self.user = pwd.getpwuid(uid).pw_name
        self.group = grp.getgrgid(os.getgid()).gr_name
        tools = {
            "id": "#!/bin/sh\nif [ \"$1\" = -u ] && [ $# -eq 1 ]; then echo 0; elif [ \"$1\" = -u ]; then echo \"$RUNTIME_UID\"; else exec /usr/bin/id \"$@\"; fi\n",
            "docker": "#!/bin/sh\nexit 0\n",
            "systemd-analyze": "#!/bin/sh\nexit 0\n",
            "systemctl": "#!/bin/sh\ncase \"$*\" in\n  'is-enabled --quiet switchyard-restore-drill.service') [ \"${PRIOR_SERVICE_ENABLED:-0}\" = 1 ]; exit;;\n  'is-active --quiet switchyard-restore-drill.service') [ \"${PRIOR_SERVICE_ACTIVE:-0}\" = 1 ]; exit;;\n  'is-enabled --quiet switchyard-restore-drill.timer') [ \"${PRIOR_TIMER_ENABLED:-0}\" = 1 ]; exit;;\n  'is-active --quiet switchyard-restore-drill.timer') [ \"${PRIOR_TIMER_ACTIVE:-0}\" = 1 ]; exit;;\n  list-unit-files*) exit 1;;\nesac\nprintf 'systemctl %s\\n' \"$*\" >>\"$MUTATION_LOG\"\nif [ \"${FAIL_RESTORE_ENABLE:-0}\" = 1 ] && [ \"$*\" = 'enable --now switchyard-restore-drill.timer' ]; then exit 1; fi\nexit 0\n",
            "install": "#!/bin/sh\n/usr/bin/install \"$@\" || exit\nprintf 'install %s\\n' \"$*\" >>\"$MUTATION_LOG\"\n",
            "runuser": "#!/bin/sh\nshift 3\npython3 - \"$RUNTIME_UID\" \"$@\" <<'PY'\nimport os, sys\nos.setuid(int(sys.argv[1]))\nos.execvp(sys.argv[2], sys.argv[2:])\nPY\nstatus=$?\n[ $status -eq 0 ] || exit $status\ncase \"$*\" in *initialize-deadline*) printf 'initialize-deadline\\n' >>\"$MUTATION_LOG\";; esac\n",
        }
        for name, content in tools.items():
            path = self.bin / name
            path.write_text(content)
            path.chmod(0o700)
            if uid == 0:
                os.chown(path, self.runtime_uid, -1)
        self.backup = self.private("backup")
        self.evidence = self.private("evidence")
        self.metrics = self.private("metrics")
        self.policy = self.root / "policy.json"
        self.policy.write_text(json.dumps({
            "contract_version": "switchyard.operator-policy.v1", "policy_id": "ops", "policy_version": 1,
            "provider": "postgres", "storage_owner": "operations", "encryption_owner": "security",
            "encryption_expectation": "host encrypted", "retention_duration": "P35D",
            "declared_rpo_seconds": 86400, "declared_rto_seconds": 60,
            "drill_calendar": "Sun *-*-01..07 03:00:00 UTC", "max_drill_interval_seconds": 100,
            "alert_grace_seconds": 5,
        }))
        self.env_file = self.root / "switchyard.env"
        if uid == 0:
            for path in (self.root, self.bin, self.backup, self.evidence, self.metrics):
                os.chown(path, self.runtime_uid, -1)

    def private(self, name):
        path = self.root / name
        path.mkdir(mode=0o700)
        return path

    def tearDown(self):
        self.temp.cleanup()

    def write_env(self, complete=True):
        values = {
            "SWITCHYARD_RESTORE_POLICY": self.policy,
            "SWITCHYARD_RESTORE_DRILL_EVIDENCE_DIR": self.evidence,
            "SWITCHYARD_BACKUP_DIR": self.backup,
            "SWITCHYARD_BACKUP_METRICS_DIR": self.metrics,
            "SWITCHYARD_RESTORE_DRILL_METRICS_FILE": self.metrics / "restore-drill.prom",
            "SWITCHYARD_RESTORE_TIMEOUT_SECONDS": "10",
            "OPENCODE_RUNTIME_UID": str(self.runtime_uid),
        }
        if not complete:
            values.pop("SWITCHYARD_RESTORE_POLICY")
        self.env_file.write_text("".join(f"{key}={value}\n" for key, value in values.items()))

    def run_installer(self, extra_env=None):
        metrics_file = self.metrics / "restore-drill.prom"
        if os.getuid() == 0 and metrics_file.exists():
            os.chown(metrics_file, self.runtime_uid, -1)
        env = os.environ.copy()
        env.update({
            "PATH": f"{self.bin}:{env['PATH']}", "MUTATION_LOG": str(self.log),
            "SWITCHYARD_INSTALL_RESTORE_DRILL_TIMER": "1", "SWITCHYARD_ENV_FILE": str(self.env_file),
            "SWITCHYARD_SYSTEMD_UNIT_DIR": str(self.units), "SWITCHYARD_RUN_USER": self.user,
            "SWITCHYARD_RUN_GROUP": self.group,
            "RUNTIME_UID": str(self.runtime_uid),
        })
        env.update(extra_env or {})
        return subprocess.run([str(ROOT / "deploy/install-systemd.sh")], env=env, text=True, capture_output=True)

    def test_missing_restore_setting_causes_no_install_or_systemctl_mutation(self):
        self.write_env(complete=False)
        result = self.run_installer()
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(self.log.exists())
        self.assertEqual(list(self.units.iterdir()), [])

    def test_renders_service_timer_and_initializes_deadline(self):
        self.write_env()
        result = self.run_installer()
        self.assertEqual(result.returncode, 0, result.stderr)
        service = (self.units / "switchyard-restore-drill.service").read_text()
        timer = (self.units / "switchyard-restore-drill.timer").read_text()
        self.assertIn(f"User={self.user}", service)
        self.assertIn("TimeoutStartSec=210s", service)
        self.assertNotIn("infinity", service)
        self.assertIn(f"EnvironmentFile={self.env_file}", service)
        self.assertIn("OnCalendar=Sun *-*-01..07 03:00:00 UTC", timer)
        self.assertIn("Persistent=true", timer)
        metrics = (self.metrics / "restore-drill.prom").read_text()
        self.assertEqual(len(metrics.splitlines()), 1)
        self.assertTrue(metrics.startswith("switchyard_restore_drill_next_deadline_timestamp_seconds "))
        mutations = self.log.read_text().splitlines()
        self.assertLess(mutations.index("systemctl daemon-reload"), mutations.index("initialize-deadline"))
        self.assertEqual(mutations[mutations.index("initialize-deadline") + 1],
                         "systemctl enable --now switchyard-restore-drill.timer")

    def test_calendar_injection_is_rejected_before_mutation(self):
        value = json.loads(self.policy.read_text())
        value["drill_calendar"] = "daily\nOnUnitActiveSec=1s"
        self.policy.write_text(json.dumps(value))
        self.write_env()
        result = self.run_installer()
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(self.log.exists())

    def test_runtime_uid_mismatch_is_rejected_before_mutation(self):
        self.write_env()
        configured = self.runtime_uid + 1
        self.env_file.write_text(self.env_file.read_text().replace(
            f"OPENCODE_RUNTIME_UID={self.runtime_uid}\n", f"OPENCODE_RUNTIME_UID={configured}\n"))
        result = self.run_installer()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("restore drill run user must match OPENCODE_RUNTIME_UID", result.stderr)
        self.assertFalse(self.log.exists())
        self.assertEqual(list(self.units.iterdir()), [])

    def test_timer_enable_failure_removes_initialized_deadline_and_restore_units(self):
        self.write_env()
        result = self.run_installer({"FAIL_RESTORE_ENABLE": "1"})
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse((self.metrics / "restore-drill.prom").exists())
        self.assertFalse((self.units / "switchyard-restore-drill.service").exists())
        self.assertFalse((self.units / "switchyard-restore-drill.timer").exists())

    def test_timer_enable_failure_restores_previous_metrics_exactly(self):
        self.write_env()
        metrics = self.metrics / "restore-drill.prom"
        original = b"switchyard_restore_drill_last_success_timestamp_seconds 55\n"
        metrics.write_bytes(original)
        metrics.chmod(0o600)
        result = self.run_installer({"FAIL_RESTORE_ENABLE": "1"})
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(metrics.read_bytes(), original)
        self.assertEqual(metrics.stat().st_mode & 0o777, 0o600)
        self.assertIn("initialize-deadline", self.log.read_text().splitlines())

    def test_reinstall_failure_restores_existing_units_and_runtime_state(self):
        self.write_env()
        service = self.units / "switchyard-restore-drill.service"
        timer = self.units / "switchyard-restore-drill.timer"
        service_bytes = b"existing working restore service\n"
        timer_bytes = b"existing working restore timer\n"
        service.write_bytes(service_bytes)
        timer.write_bytes(timer_bytes)
        service.chmod(0o640)
        timer.chmod(0o644)
        result = self.run_installer({
            "FAIL_RESTORE_ENABLE": "1", "PRIOR_SERVICE_ENABLED": "1",
            "PRIOR_TIMER_ENABLED": "1", "PRIOR_TIMER_ACTIVE": "1",
        })
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(service.read_bytes(), service_bytes)
        self.assertEqual(timer.read_bytes(), timer_bytes)
        self.assertEqual(service.stat().st_mode & 0o777, 0o640)
        self.assertEqual(timer.stat().st_mode & 0o777, 0o644)
        mutations = self.log.read_text().splitlines()
        self.assertGreaterEqual(mutations.count("systemctl daemon-reload"), 2)
        self.assertIn("systemctl enable switchyard-restore-drill.service", mutations)
        self.assertIn("systemctl enable switchyard-restore-drill.timer", mutations)
        self.assertIn("systemctl start switchyard-restore-drill.timer", mutations)
        self.assertNotIn("systemctl start switchyard-restore-drill.service", mutations)

    def test_compose_path_and_external_alerts_are_static(self):
        compose = (ROOT / "docker-compose.yml").read_text()
        alerts = (ROOT / "deploy/monitoring/switchyard-alerts.yml").read_text()
        self.assertIn("SWITCHYARD_RESTORE_DRILL_METRICS_FILE: /var/lib/switchyard-metrics/restore-drill.prom", compose)
        self.assertIn('switchyard_restore_drill_last_result{job="switchyard-model"} == 0', alerts)
        self.assertIn('time() > switchyard_restore_drill_next_deadline_timestamp_seconds{job="switchyard-model"}', alerts)
        self.assertNotIn("max_drill_interval_seconds", alerts)
        self.assertNotIn("alert_grace_seconds", alerts)


if __name__ == "__main__":
    unittest.main()
