import json
import os
import pathlib
import pwd
import grp
import subprocess
import sys
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
        self.nft_dir = self.root / "nft"
        self.nft_dir.mkdir(mode=0o700)
        self.lib_dir = self.root / "lib"
        self.lib_dir.mkdir(mode=0o700)
        self.bin = self.root / "bin"
        self.bin.mkdir(mode=0o700)
        self.log = self.root / "mutations.log"
        uid = os.getuid()
        self.runtime_uid = uid or 1000
        self.user = pwd.getpwuid(uid).pw_name
        self.group = grp.getgrgid(os.getgid()).gr_name
        tools = {
            "id": "#!/bin/sh\nif [ \"$1\" = -u ] && [ $# -eq 1 ]; then echo 0; elif [ \"$1\" = -u ]; then echo \"$RUNTIME_UID\"; else exec /usr/bin/id \"$@\"; fi\n",
            "docker": "#!/bin/sh\ncase \"$*\" in\n  *'config --services') [ \"${FAIL_COMPOSE_CONFIG:-0}\" = 0 ] || exit 1; printf 'api\\nweb\\n';;\n  *'ps --status running --services') [ \"${PRIOR_WEB_RUNNING:-0}\" = 1 ] && printf 'web\\n';;\n  *' up -d '*|*' stop '*) printf 'docker %s\\n' \"$*\" >>\"$MUTATION_LOG\";;\nesac\nexit 0\n",
            "nft": "#!/bin/sh\ncase \"$*\" in '--check --file '*|'list table inet switchyard_web_gate') exit 0;; esac\nexit 1\n",
            "ufw": "#!/bin/sh\n[ \"$*\" = status ] && { printf 'Status: active\\n'; exit; }\nexit 1\n",
            "iptables": "#!/bin/sh\ncase \"$*\" in '-C ufw-user-forward '*) exit 0;; esac\nexit 1\n",
            "stat": "#!/bin/sh\ncase \"$3\" in *manage_tailscale_web_forward.py) [ \"${UNSAFE_GATE_HELPER:-0}\" = 0 ] || { printf '1000 1000 777\\n'; exit; };; esac\nprintf '0 0 %s\\n' \"$(case \"$3\" in *web-gate.nft|*switchyard-web-gate.service) printf 644;; *) printf 755;; esac)\"\n",
            "systemd-analyze": "#!/bin/sh\nexit 0\n",
            "systemctl": "#!/bin/sh\ncase \"$*\" in\n  'is-active --quiet switchyard-web-gate.service') [ \"${GATE_ACTIVE:-1}\" = 1 ]; exit;;\n  'is-enabled --quiet switchyard.service') [ \"${PRIOR_MAIN_ENABLED:-0}\" = 1 ]; exit;;\n  'is-active --quiet switchyard.service') { [ -e \"$MAIN_STATE\" ] || [ \"${PRIOR_MAIN_ACTIVE:-0}\" = 1 ]; }; exit;;\n  'show docker.service --property=Requires --value'|'show docker.service --property=After --value') printf 'switchyard-web-gate.service\\n'; exit;;\n  'is-enabled --quiet switchyard-restore-drill.service') [ \"${PRIOR_SERVICE_ENABLED:-0}\" = 1 ]; exit;;\n  'is-active --quiet switchyard-restore-drill.service') [ \"${PRIOR_SERVICE_ACTIVE:-0}\" = 1 ]; exit;;\n  'is-enabled --quiet switchyard-restore-drill.timer') [ \"${PRIOR_TIMER_ENABLED:-0}\" = 1 ]; exit;;\n  'is-active --quiet switchyard-restore-drill.timer') [ \"${PRIOR_TIMER_ACTIVE:-0}\" = 1 ]; exit;;\n  list-unit-files*) exit 1;;\nesac\nprintf 'systemctl %s\\n' \"$*\" >>\"$MUTATION_LOG\"\nif [ \"$*\" = 'restart switchyard.service' ]; then [ \"${FAIL_MAIN_RESTART:-0}\" = 0 ] || exit 1; : >\"$MAIN_STATE\"; fi\nif [ \"${FAIL_RESTORE_ENABLE:-0}\" = 1 ] && [ \"$*\" = 'enable --now switchyard-restore-drill.timer' ]; then exit 1; fi\nexit 0\n",
            "install": "#!/bin/sh\n/usr/bin/install \"$@\" || exit\nprintf 'install %s\\n' \"$*\" >>\"$MUTATION_LOG\"\n",
            "runuser": "#!/bin/sh\nshift 3\npython3 - \"$RUNTIME_UID\" \"$@\" <<'PY'\nimport os, sys\nos.setuid(int(sys.argv[1]))\nos.execvp(sys.argv[2], sys.argv[2:])\nPY\nstatus=$?\n[ $status -eq 0 ] || exit $status\ncase \"$*\" in *initialize-deadline*) printf 'initialize-deadline\\n' >>\"$MUTATION_LOG\";; esac\n",
        }
        backup_state_probes = (
            "  'is-enabled --quiet switchyard-backup.service') [ \"${PRIOR_BACKUP_SERVICE_ENABLED:-0}\" = 1 ]; exit;;\n"
            "  'is-active --quiet switchyard-backup.service') [ \"${PRIOR_BACKUP_SERVICE_ACTIVE:-0}\" = 1 ]; exit;;\n"
            "  'is-enabled --quiet switchyard-backup.timer') [ \"${PRIOR_BACKUP_TIMER_ENABLED:-0}\" = 1 ]; exit;;\n"
            "  'is-active --quiet switchyard-backup.timer') [ \"${PRIOR_BACKUP_TIMER_ACTIVE:-0}\" = 1 ]; exit;;\n"
        )
        tools["systemctl"] = tools["systemctl"].replace(
            "  'is-enabled --quiet switchyard-restore-drill.service')",
            backup_state_probes + "  'is-enabled --quiet switchyard-restore-drill.service')",
        )
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
            "SWITCHYARD_WEB_BIND_HOST": "100.65.164.66",
            "SWITCHYARD_TAILSCALE_PROXY_IPV4": "100.77.128.80",
            "WEB_PORT": "8081",
            "SWITCHYARD_DOCKER_BRIDGE_SUBNET": "172.31.250.0/24",
            "SWITCHYARD_DOCKER_BRIDGE_GATEWAY": "172.31.250.1",
            "SWITCHYARD_DOCKER_BRIDGE_NAME": "switchyard0",
            "SWITCHYARD_WEB_CONTAINER_IPV4": "172.31.250.10",
        }
        if not complete:
            values.pop("SWITCHYARD_RESTORE_POLICY")
        self.env_file.write_text("".join(f"{key}={value}\n" for key, value in values.items()))
        subprocess.run(
            [sys.executable, str(ROOT / "scripts/render_tailscale_web_gate.py"), "--env-file", str(self.env_file), "--output", str(self.nft_dir / "web-gate.nft")],
            check=True,
        )
        renderer = self.lib_dir / "render_tailscale_web_gate.py"
        renderer.write_bytes((ROOT / "scripts/render_tailscale_web_gate.py").read_bytes())
        renderer.chmod(0o755)
        helper = self.lib_dir / "manage_tailscale_web_forward.py"
        helper.write_bytes((ROOT / "scripts/manage_tailscale_web_forward.py").read_bytes())
        helper.chmod(0o755)
        gate = (ROOT / "deploy/switchyard-web-gate.service").read_text()
        gate = gate.replace("__RENDERER__", str(renderer))
        gate = gate.replace("__ENV_FILE__", str(self.env_file))
        gate = gate.replace("__NFT_RULES__", str(self.nft_dir / "web-gate.nft"))
        gate = gate.replace("__UFW_HELPER__", str(helper))
        (self.units / "switchyard-web-gate.service").write_text(gate)

    def run_installer(self, extra_env=None):
        metrics_file = self.metrics / "restore-drill.prom"
        if os.getuid() == 0 and metrics_file.exists():
            os.chown(metrics_file, self.runtime_uid, -1)
        env = os.environ.copy()
        env.update({
            "PATH": f"{self.bin}:{env['PATH']}", "MUTATION_LOG": str(self.log),
            "SWITCHYARD_INSTALL_RESTORE_DRILL_TIMER": "1", "SWITCHYARD_ENV_FILE": str(self.env_file),
            "SWITCHYARD_SYSTEMD_UNIT_DIR": str(self.units), "SWITCHYARD_RUN_USER": self.user,
            "SWITCHYARD_NFT_CONFIG_DIR": str(self.nft_dir),
            "SWITCHYARD_LIB_DIR": str(self.lib_dir),
            "SWITCHYARD_RUN_GROUP": self.group,
            "RUNTIME_UID": str(self.runtime_uid),
            "MAIN_STATE": str(self.root / "main-active"),
        })
        env.update(extra_env or {})
        return subprocess.run([str(ROOT / "deploy/install-systemd.sh")], env=env, text=True, capture_output=True)

    def test_missing_restore_setting_causes_no_install_or_systemctl_mutation(self):
        self.write_env(complete=False)
        result = self.run_installer()
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(self.log.exists())
        self.assertEqual([path for path in self.units.iterdir() if path.name != "switchyard-web-gate.service"], [])

    def test_inactive_web_gate_causes_no_install_or_systemctl_mutation(self):
        self.write_env()
        result = self.run_installer({"GATE_ACTIVE": "0"})
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("install-web-gate.sh first", result.stderr)
        self.assertFalse(self.log.exists())
        self.assertEqual([path for path in self.units.iterdir() if path.name != "switchyard-web-gate.service"], [])

    def test_unsafe_installed_gate_helper_causes_no_mutation(self):
        self.write_env()
        result = self.run_installer({"UNSAFE_GATE_HELPER": "1"})
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("root-owned", result.stderr)
        self.assertFalse(self.log.exists())

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
        main = (self.units / "switchyard.service").read_text()
        self.assertEqual(main.count(f"--env-file {self.env_file}"), 11)
        self.assertNotIn(f"--env-file {ROOT / '.env'}", main)
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
        self.assertEqual([path for path in self.units.iterdir() if path.name != "switchyard-web-gate.service"], [])

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

    def test_main_restart_failure_restores_unit_state_and_prior_workload(self):
        self.write_env()
        main = self.units / "switchyard.service"
        original = b"existing main unit\n"
        main.write_bytes(original)
        main.chmod(0o640)
        result = self.run_installer({
            "FAIL_MAIN_RESTART": "1", "PRIOR_MAIN_ENABLED": "1",
            "PRIOR_WEB_RUNNING": "1",
        })
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(main.read_bytes(), original)
        self.assertEqual(main.stat().st_mode & 0o777, 0o640)
        mutations = self.log.read_text().splitlines()
        self.assertIn("systemctl disable --now switchyard.service", mutations)
        self.assertIn("systemctl enable switchyard.service", mutations)
        restored = next(line for line in mutations if line.startswith("docker ") and " up -d --no-deps web" in line)
        self.assertGreater(mutations.index(restored), mutations.index("systemctl daemon-reload"))

    def test_compose_preflight_failure_does_not_mutate_units(self):
        self.write_env()
        result = self.run_installer({"FAIL_COMPOSE_CONFIG": "1"})
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(self.log.exists())
        self.assertEqual([path for path in self.units.iterdir() if path.name != "switchyard-web-gate.service"], [])

    def test_main_installer_rejects_relative_and_symlink_env(self):
        self.write_env()
        relative = self.run_installer({"SWITCHYARD_ENV_FILE": "switchyard.env"})
        self.assertNotEqual(relative.returncode, 0)
        link = self.root / "env-link"
        link.symlink_to(self.env_file)
        symlink = self.run_installer({"SWITCHYARD_ENV_FILE": str(link)})
        self.assertNotEqual(symlink.returncode, 0)
        unsafe = self.root / "unsafe env"
        unsafe.write_bytes(self.env_file.read_bytes())
        unsafe_result = self.run_installer({"SWITCHYARD_ENV_FILE": str(unsafe)})
        self.assertNotEqual(unsafe_result.returncode, 0)
        self.assertFalse(self.log.exists())

    def test_active_main_rollback_restores_partial_workload(self):
        self.write_env()
        result = self.run_installer({
            "FAIL_MAIN_RESTART": "1", "PRIOR_MAIN_ACTIVE": "1",
            "PRIOR_MAIN_ENABLED": "1", "PRIOR_WEB_RUNNING": "1",
        })
        self.assertNotEqual(result.returncode, 0)
        mutations = self.log.read_text().splitlines()
        start = mutations.index("systemctl start switchyard.service")
        stop_api = next(i for i, line in enumerate(mutations) if line.startswith("docker ") and line.endswith(" stop api"))
        self.assertLess(start, stop_api)

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

    def test_failure_restores_every_unit_and_exact_prior_daemon_state(self):
        self.write_env()
        originals = {
            "switchyard.service": (b"old main\n", 0o640),
            "switchyard-backup.service": (b"old backup service\n", 0o600),
            "switchyard-backup.timer": (b"old backup timer\n", 0o644),
            "switchyard-restore-drill.service": (b"old restore service\n", 0o620),
            "switchyard-restore-drill.timer": (b"old restore timer\n", 0o604),
        }
        for name, (content, mode) in originals.items():
            path = self.units / name
            path.write_bytes(content)
            path.chmod(mode)

        result = self.run_installer({
            "FAIL_RESTORE_ENABLE": "1",
            "PRIOR_MAIN_ENABLED": "1", "PRIOR_MAIN_ACTIVE": "1",
            "PRIOR_WEB_RUNNING": "1",
            "PRIOR_BACKUP_SERVICE_ENABLED": "1", "PRIOR_BACKUP_SERVICE_ACTIVE": "1",
            "PRIOR_BACKUP_TIMER_ENABLED": "1", "PRIOR_BACKUP_TIMER_ACTIVE": "0",
            "PRIOR_SERVICE_ENABLED": "0", "PRIOR_SERVICE_ACTIVE": "1",
            "PRIOR_TIMER_ENABLED": "1", "PRIOR_TIMER_ACTIVE": "1",
        })
        self.assertNotEqual(result.returncode, 0)
        for name, (content, mode) in originals.items():
            path = self.units / name
            self.assertEqual(path.read_bytes(), content, name)
            self.assertEqual(path.stat().st_mode & 0o777, mode, name)

        mutations = self.log.read_text().splitlines()
        for unit in originals:
            self.assertIn(f"systemctl disable --now {unit}", mutations)
        for unit in (
            "switchyard.service", "switchyard-backup.service",
            "switchyard-backup.timer", "switchyard-restore-drill.timer",
        ):
            self.assertIn(f"systemctl enable {unit}", mutations)
        self.assertNotIn("systemctl enable switchyard-restore-drill.service", mutations)
        for unit in (
            "switchyard.service", "switchyard-backup.service",
            "switchyard-restore-drill.service", "switchyard-restore-drill.timer",
        ):
            self.assertIn(f"systemctl start {unit}", mutations)
        self.assertNotIn("systemctl start switchyard-backup.timer", mutations)
        stop_api = next(line for line in mutations if line.startswith("docker ") and line.endswith(" stop api"))
        self.assertGreater(mutations.index(stop_api), mutations.index("systemctl start switchyard.service"))

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
