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
        self.runtime_gid = os.getgid() or 1000
        self.user = pwd.getpwuid(uid).pw_name
        self.group = grp.getgrgid(os.getgid()).gr_name
        tools = {
            "id": "#!/bin/sh\nif [ \"$1\" = -u ] && [ $# -eq 1 ]; then echo 0; elif [ \"$1\" = -u ]; then echo \"$RUNTIME_UID\"; else exec /usr/bin/id \"$@\"; fi\n",
            "docker": "#!/bin/sh\ncase \"$*\" in\n  info) [ \"${FAIL_DOCKER_INFO:-0}\" = 0 ] || exit 1;;\n  'compose version') [ \"${FAIL_COMPOSE_VERSION:-0}\" = 0 ] || exit 1;;\n  *'config --services') [ \"${FAIL_COMPOSE_CONFIG:-0}\" = 0 ] || exit 1; printf 'api\\nweb\\n';;\n  *'ps --status running --services') [ \"${PRIOR_WEB_RUNNING:-0}\" = 1 ] && printf 'web\\n';;\n  *' up -d '*|*' stop '*) printf 'docker %s\\n' \"$*\" >>\"$MUTATION_LOG\";;\nesac\nexit 0\n",
            "getent": "#!/bin/sh\n[ \"$1\" = group ] && [ -n \"$2\" ] && [ \"$2\" != \"${MISSING_GROUP:-}\" ]\n",
            "nft": "#!/bin/sh\ncase \"$*\" in '--check --file '*|'list table inet switchyard_web_gate') exit 0;; esac\nexit 1\n",
            "ufw": "#!/bin/sh\n[ \"$*\" = status ] && { printf 'Status: active\\n'; exit; }\nexit 1\n",
            "iptables": "#!/bin/sh\ncase \"$*\" in '-C ufw-user-forward '*|'-C ufw-user-input '*) exit 0;; esac\nexit 1\n",
            "stat": "#!/bin/sh\ncase \"$3\" in *manage_tailscale_web_forward.py) [ \"${UNSAFE_GATE_HELPER:-0}\" = 0 ] || { printf '1000 1000 777\\n'; exit; };; esac\nprintf '0 0 %s\\n' \"$(case \"$3\" in *web-gate.nft|*switchyard-web-gate.service) printf 644;; *) printf 755;; esac)\"\n",
            "systemd-analyze": "#!/bin/sh\nexit 0\n",
            "systemctl": "#!/bin/sh\ncase \"$*\" in\n  'is-active --quiet switchyard-web-gate.service') [ \"${GATE_ACTIVE:-1}\" = 1 ]; exit;;\n  'is-enabled --quiet switchyard.service') [ \"${PRIOR_MAIN_ENABLED:-0}\" = 1 ]; exit;;\n  'is-active --quiet switchyard.service') { [ -e \"$MAIN_STATE\" ] || [ \"${PRIOR_MAIN_ACTIVE:-0}\" = 1 ]; }; exit;;\n  'show docker.service --property=Requires --value'|'show docker.service --property=After --value') printf 'switchyard-web-gate.service\\n'; exit;;\n  'is-enabled --quiet switchyard-restore-drill.service') [ \"${PRIOR_SERVICE_ENABLED:-0}\" = 1 ]; exit;;\n  'is-active --quiet switchyard-restore-drill.service') [ \"${PRIOR_SERVICE_ACTIVE:-0}\" = 1 ]; exit;;\n  'is-enabled --quiet switchyard-restore-drill.timer') [ \"${PRIOR_TIMER_ENABLED:-0}\" = 1 ]; exit;;\n  'is-active --quiet switchyard-restore-drill.timer') [ \"${PRIOR_TIMER_ACTIVE:-0}\" = 1 ]; exit;;\n  list-unit-files*) exit 1;;\nesac\nprintf 'systemctl %s\\n' \"$*\" >>\"$MUTATION_LOG\"\nif [ \"$*\" = 'restart switchyard.service' ]; then [ \"${FAIL_MAIN_RESTART:-0}\" = 0 ] || exit 1; : >\"$MAIN_STATE\"; fi\nif [ \"${FAIL_RESTORE_ENABLE:-0}\" = 1 ] && [ \"$*\" = 'enable --now switchyard-restore-drill.timer' ]; then exit 1; fi\nexit 0\n",
            "install": "#!/bin/sh\n/usr/bin/install \"$@\" || exit\nprintf 'install %s\\n' \"$*\" >>\"$MUTATION_LOG\"\n",
            "runuser": "#!/bin/sh\noriginal=\"$*\"\nprintf '%s\\n' \"$original\" >>\"$RUNUSER_LOG\"\nuser= primary= supplementary=\nwhile [ $# -gt 0 ]; do\n  case \"$1\" in\n    -u) user=$2; shift 2;;\n    -g) primary=$2; shift 2;;\n    -G) supplementary=$2; shift 2;;\n    --) shift; break;;\n    *) exit 2;;\n  esac\ndone\npython3 - \"$RUNTIME_UID\" \"$@\" <<'PY'\nimport os, sys\nos.setuid(int(sys.argv[1]))\nos.execvp(sys.argv[2], sys.argv[2:])\nPY\nstatus=$?\n[ $status -eq 0 ] || exit $status\ncase \"$*\" in *initialize-deadline*) printf 'initialize-deadline\\n' >>\"$MUTATION_LOG\";; esac\n",
            "switchyard-compose": "#!/bin/sh\nwhile [ $# -gt 0 ]; do [ \"$1\" = -- ] && { shift; break; }; shift; done\nexec \"$FAKE_DOCKER\" compose \"$@\"\n",
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
        installer = (ROOT / "deploy/install-systemd.sh").read_text()
        installer = installer.replace(
            'repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"',
            f'repo_root="{ROOT}"',
        ).replace(
            'docker_local="$repo_root/deploy/docker-local"',
            f'docker_local="{self.bin / "docker"}"',
        ).replace(
            'compose_wrapper="$repo_root/deploy/switchyard-compose"',
            f'compose_wrapper="{self.bin / "switchyard-compose"}"',
        )
        self.installer = self.root / "install-systemd.sh"
        self.installer.write_text(installer)
        self.installer.chmod(0o700)
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
        self.registry = self.root / "open-pilot-registry.json"
        self.runtime_manifest = self.root / "open-pilot-runtime.json"
        self.compose_override = self.root / "open-pilot-compose.json"
        self.compose_file_set = self.root / "open-pilot-file-set.json"
        source = self.root / "open-pilot-source"
        source.mkdir(mode=0o700)
        subprocess.run(["git", "init", "--initial-branch=main", str(source)], check=True, capture_output=True)
        subprocess.run(["git", "-C", str(source), "remote", "add", "origin", "https://git.example.test/owner/repo.git"], check=True)
        worktrees = source / ".open-pilot-worktrees"
        worktrees.mkdir(mode=0o700)
        marker = worktrees / ".switchyard-managed"
        marker.write_text("switchyard-managed-worktree-root\n")
        marker.chmod(0o600)
        config = self.private("open-pilot-config")
        data = self.private("open-pilot-data")
        password = self.root / "open-pilot-password"
        password.write_text("p" * 48 + "\n")
        password.chmod(0o600)
        self.registry.write_text(json.dumps({"version": 1, "generation": "test-1", "repositories": [{
            "id": "repo", "slug": "owner/repo", "aliases": [], "executor_generation": "test-1",
            "source_path": str(source), "worktree_root": str(worktrees), "config_path": str(config),
            "data_path": str(data), "password_file": str(password),
        }]}))
        self.registry.chmod(0o600)
        if uid == 0:
            for path in (self.root, self.bin, self.backup, self.evidence, self.metrics, source, source / ".git", worktrees, marker, config, data):
                os.chown(path, self.runtime_uid, self.runtime_gid)

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
            "OPENCODE_RUNTIME_GID": str(self.runtime_gid),
            "GITEA_BASE_URL": "https://git.example.test",
            "SWITCHYARD_OPEN_PILOT_OPERATOR_REGISTRY": self.registry,
            "SWITCHYARD_OPEN_PILOT_RUNTIME_MANIFEST": self.runtime_manifest,
            "SWITCHYARD_OPEN_PILOT_COMPOSE_OVERRIDE": self.compose_override,
            "SWITCHYARD_OPEN_PILOT_COMPOSE_FILE_SET": self.compose_file_set,
            "SWITCHYARD_WEB_BIND_HOST": "100.65.164.66",
            "SWITCHYARD_TAILSCALE_PROXY_IPV4": "100.77.128.80",
            "WEB_PORT": "8081",
            "SWITCHYARD_DOCKER_BRIDGE_SUBNET": "172.31.250.0/24",
            "SWITCHYARD_DOCKER_BRIDGE_GATEWAY": "172.31.250.1",
            "SWITCHYARD_DOCKER_BRIDGE_NAME": "switchyard0",
            "SWITCHYARD_API_CONTAINER_IPV4": "172.31.250.4",
            "SWITCHYARD_WEB_CONTAINER_IPV4": "172.31.250.10",
            "OLLAMA_BASE_URL": "http://172.31.250.1:11435",
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
            "RUNUSER_LOG": str(self.root / "runuser.log"),
            "EXPECTED_RUN_USER": self.user,
            "EXPECTED_RUN_GROUP": self.group,
            "FAKE_DOCKER": str(self.bin / "docker"),
        })
        env.update(extra_env or {})
        env["EXPECTED_DOCKER_GROUP"] = env.get("SWITCHYARD_DOCKER_GROUP", "docker")
        return subprocess.run([str(self.installer)], env=env, text=True, capture_output=True)

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
        backup_service = (self.units / "switchyard-backup.service").read_text()
        timer = (self.units / "switchyard-restore-drill.timer").read_text()
        self.assertIn(f"User={self.user}", service)
        self.assertIn("TimeoutStartSec=210s", service)
        self.assertNotIn("infinity", service)
        self.assertIn(f"EnvironmentFile={self.env_file}", service)
        self.assertIn(f"EnvironmentFile=-{self.env_file}", backup_service)
        self.assertIn(f"Environment=SWITCHYARD_ENV_FILE={self.env_file}", backup_service)
        main = (self.units / "switchyard.service").read_text()
        self.assertIn(f"--file-set {self.compose_file_set}", main)
        manifest = json.loads(self.runtime_manifest.read_text())
        override = json.loads(self.compose_override.read_text())
        file_set = json.loads(self.compose_file_set.read_text())
        self.assertEqual(manifest["registry_fingerprint"], file_set["registry_fingerprint"])
        self.assertEqual(override["services"]["api"]["environment"]["SWITCHYARD_OPEN_PILOT_REGISTRY_FINGERPRINT"], file_set["registry_fingerprint"])
        for name in ("switchyard.service", "switchyard-backup.service", "switchyard-restore-drill.service"):
            self.assertIn("SupplementaryGroups=docker", (self.units / name).read_text())
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

    def test_distinct_docker_group_override_changes_every_docker_unit(self):
        self.write_env()
        docker_group = "switchyarddocker"
        self.assertNotEqual(docker_group, self.group)
        result = self.run_installer({"SWITCHYARD_DOCKER_GROUP": docker_group})
        self.assertEqual(result.returncode, 0, result.stderr)
        for name in ("switchyard.service", "switchyard-backup.service", "switchyard-restore-drill.service"):
            unit = (self.units / name).read_text()
            self.assertIn(f"Group={self.group}\n", unit)
            self.assertIn(f"SupplementaryGroups={docker_group}\n", unit)

    def test_docker_probe_uses_exact_identity_binary_and_clean_environment(self):
        self.write_env()
        docker_group = "switchyarddocker"
        result = self.run_installer({
            "SWITCHYARD_DOCKER_GROUP": docker_group,
            "DOCKER_HOST": "tcp://hostile.invalid:2375",
            "DOCKER_CONTEXT": "hostile",
        })
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(
            (self.root / "runuser.log").read_text().splitlines()[0],
            f"-u {self.user} -g {self.group} -G {docker_group} -- {self.bin / 'docker'} info",
        )
        self.assertEqual(
            (self.root / "runuser.log").read_text().splitlines()[1],
            f"-u {self.user} -g {self.group} -G {docker_group} -- {self.bin / 'docker'} compose version",
        )

    def test_topology_rendering_uses_runtime_identity(self):
        self.write_env()
        result = self.run_installer()
        self.assertEqual(result.returncode, 0, result.stderr)
        calls = (self.root / "runuser.log").read_text().splitlines()
        prefix = f"-u {self.user} -g {self.group} -- python3 "
        self.assertTrue(any(call.startswith(prefix) and "prepare_open_pilot_registry.py" in call for call in calls))
        self.assertTrue(any(call.startswith(prefix) and "render_open_pilot_topology.py" in call for call in calls))
        self.assertTrue(any(call.startswith(prefix) and "resolve_open_pilot_compose_file_set.py" in call for call in calls))

    def test_missing_system_compose_plugin_causes_no_mutation(self):
        self.write_env()
        result = self.run_installer({"FAIL_COMPOSE_VERSION": "1"})
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("system-wide Docker Compose plugin", result.stderr)
        self.assertFalse(self.log.exists())

    def test_hostile_docker_environment_and_path_cannot_mask_probe_failure(self):
        self.write_env()
        result = self.run_installer({
            "FAIL_DOCKER_INFO": "1",
            "DOCKER_HOST": "tcp://hostile.invalid:2375",
            "DOCKER_CONTEXT": "hostile",
        })
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("runtime identity cannot reach Docker", result.stderr)
        self.assertFalse(self.log.exists())

    def test_unreachable_docker_runtime_identity_causes_no_mutation(self):
        self.write_env()
        result = self.run_installer({"FAIL_DOCKER_INFO": "1"})
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("runtime identity cannot reach Docker", result.stderr)
        self.assertFalse(self.log.exists())
        self.assertEqual([path for path in self.units.iterdir() if path.name != "switchyard-web-gate.service"], [])

    def test_unsafe_docker_group_is_rejected_before_mutation(self):
        self.write_env()
        result = self.run_installer({"SWITCHYARD_DOCKER_GROUP": "docker\nSupplementaryGroups=root"})
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("must be a safe group name", result.stderr)
        self.assertFalse(self.log.exists())

    def test_nonexistent_docker_group_is_rejected_before_mutation(self):
        self.write_env()
        result = self.run_installer({
            "SWITCHYARD_DOCKER_GROUP": "missingdocker",
            "MISSING_GROUP": "missingdocker",
        })
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("Docker group 'missingdocker' does not exist", result.stderr)
        self.assertFalse(self.log.exists())

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

    def test_failure_restores_exact_prior_generated_topology(self):
        self.write_env()
        originals = {
            self.runtime_manifest: (b"old runtime\n", 0o640),
            self.compose_override: (b"old override\n", 0o600),
            self.compose_file_set: (b"old file set\n", 0o604),
        }
        for path, (content, mode) in originals.items():
            path.write_bytes(content)
            path.chmod(mode)
        registry_before = self.registry.read_bytes()
        result = self.run_installer({"FAIL_MAIN_RESTART": "1"})
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(self.registry.read_bytes(), registry_before)
        for path, (content, mode) in originals.items():
            self.assertEqual(path.read_bytes(), content)
            self.assertEqual(path.stat().st_mode & 0o777, mode)

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
