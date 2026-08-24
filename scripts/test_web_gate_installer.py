import os
import pathlib
import subprocess
import tempfile
import unittest


ROOT = pathlib.Path(__file__).resolve().parent.parent


class WebGateInstallerTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = pathlib.Path(self.temp.name)
        self.units = self.root / "units"
        self.nft_dir = self.root / "nft"
        self.lib_dir = self.root / "lib"
        self.bin = self.root / "bin"
        for path in (self.units, self.nft_dir, self.lib_dir, self.bin):
            path.mkdir()
        self.log = self.root / "mutations.log"
        self.env_file = self.root / ".env"
        self.env_file.write_text(
            "SWITCHYARD_WEB_BIND_HOST=100.65.164.66\n"
            "SWITCHYARD_TAILSCALE_PROXY_IPV4=100.77.128.80\n"
            "WEB_PORT=8081\n"
            "SWITCHYARD_DOCKER_BRIDGE_SUBNET=172.31.250.0/24\n"
            "SWITCHYARD_DOCKER_BRIDGE_GATEWAY=172.31.250.1\n"
        )
        tools = {
            "id": "#!/bin/sh\n[ \"$1\" = -u ] && { echo 0; exit; }\nexec /usr/bin/id \"$@\"\n",
            "install": """#!/bin/bash
original="$*"
args=()
while (( $# )); do
  case "$1" in -o|-g) shift 2;; *) args+=("$1"); shift;; esac
done
/usr/bin/install "${args[@]}" || exit
printf 'install %s\n' "$original" >>"$MUTATION_LOG"
""",
            "docker": """#!/bin/sh
case "$*" in
  *'config --services') printf 'api\nweb\n';;
  *'ps --status running --services') [ "${WEB_RUNNING:-0}" = 1 ] && printf 'web\n';;
  *'stop web') printf 'docker %s\n' "$*" >>"$MUTATION_LOG"; [ "${FAIL_WEB_STOP:-0}" = 0 ];;
esac
""",
            "stat": """#!/bin/sh
case "$3" in
  *web-gate.nft) printf '0 0 %s\n' "${RULES_MODE:-644}";;
  *render_tailscale_web_gate.py) printf '0 0 %s\n' "${RENDERER_MODE:-755}";;
  *nft) [ "${NON_TRAVERSABLE_NFT:-0}" = 0 ] && printf '0 755\n' || printf '0 700\n';;
  *) [ "${UNSAFE_LIB:-0}" = 0 ] && printf '0 755\n' || printf '1000 777\n';;
esac
""",
            "nft": """#!/bin/sh
case "$*" in
  '--check --file '*) [ "${FAIL_NFT_CHECK:-0}" = 0 ]; exit;;
  'list table inet switchyard_web_gate')
    if [ -e "$TABLE_STATE" ]; then
      if [ "${BAD_LIVE_RULES:-0}" = 1 ]; then
        printf 'table inet switchyard_web_gate { chain wrong { counter packets 0 bytes 0 } }\n'
        exit
      fi
      printf 'table inet switchyard_web_gate {\n'
      printf 'chain prerouting {\n'
      printf 'type filter hook prerouting priority dstnat - 1; policy accept;\n'
      printf 'iifname "tailscale0" ip saddr 100.77.128.80 ip daddr 100.65.164.66 tcp dport 8081 counter packets 1 bytes 64 accept comment "allow Tailscale proxy to web"\n'
      printf 'ip daddr 100.65.164.66 tcp dport 8081 counter packets 0 bytes 0 drop comment "drop other ingress to web"\n'
      printf '}\nchain output {\n'
      printf 'type filter hook output priority dstnat - 1; policy accept;\n'
      printf 'ip daddr 100.65.164.66 tcp dport 8081 counter packets 0 bytes 0 drop comment "drop local access to web"\n}\n}\n'
      exit
    fi
    [ "${PRIOR_TABLE:-0}" = 1 ] || exit 1
    printf 'table inet switchyard_web_gate { chain old { counter packets 0 bytes 0 } }\n'
    exit;;
esac
printf 'nft %s\n' "$*" >>"$MUTATION_LOG"
exit 0
""",
            "systemctl": """#!/bin/sh
case "$*" in
  'is-enabled --quiet switchyard-web-gate.service') [ "${PRIOR_ENABLED:-0}" = 1 ]; exit;;
  'is-active --quiet switchyard-web-gate.service')
    if [ -e "$STATE" ]; then [ "${FAIL_ACTIVE:-0}" = 0 ]; else [ "${PRIOR_ACTIVE:-0}" = 1 ]; fi
    exit;;
  'show docker.service --property=Requires --value'|'show docker.service --property=After --value')
    [ "${FAIL_DOCKER_DEPENDENCY:-0}" = 0 ] && printf 'network.target switchyard-web-gate.service\n'
    [ "${FAIL_DOCKER_DEPENDENCY:-0}" = 0 ]
    exit;;
esac
printf 'systemctl %s\n' "$*" >>"$MUTATION_LOG"
if [ "$*" = 'restart switchyard-web-gate.service' ]; then
  [ "${FAIL_START:-0}" = 0 ] || exit 1
  : >"$STATE"
  : >"$TABLE_STATE"
fi
exit 0
""",
        }
        for name, content in tools.items():
            path = self.bin / name
            path.write_text(content)
            path.chmod(0o700)

    def tearDown(self):
        self.temp.cleanup()

    def run_installer(self, extra=None):
        env = os.environ.copy()
        env.update(
            {
                "PATH": f"{self.bin}:{env['PATH']}",
                "MUTATION_LOG": str(self.log),
                "STATE": str(self.root / "active"),
                "TABLE_STATE": str(self.root / "table-active"),
                "SWITCHYARD_ENV_FILE": str(self.env_file),
                "SWITCHYARD_SYSTEMD_UNIT_DIR": str(self.units),
                "SWITCHYARD_NFT_CONFIG_DIR": str(self.nft_dir),
                "SWITCHYARD_LIB_DIR": str(self.lib_dir),
            }
        )
        env.update(extra or {})
        return subprocess.run(
            [str(ROOT / "deploy/install-web-gate.sh")], env=env, text=True, capture_output=True
        )

    def test_installs_rules_and_starts_gate_after_preflight(self):
        result = self.run_installer()
        self.assertEqual(result.returncode, 0, result.stderr)
        rules = (self.nft_dir / "web-gate.nft").read_text()
        self.assertIn("ip saddr 100.77.128.80 ip daddr 100.65.164.66", rules)
        self.assertTrue((self.units / "switchyard-web-gate.service").exists())
        renderer = self.lib_dir / "render_tailscale_web_gate.py"
        rules_path = self.nft_dir / "web-gate.nft"
        self.assertEqual(rules_path.stat().st_mode & 0o777, 0o644)
        self.assertEqual(renderer.stat().st_mode & 0o777, 0o755)
        self.assertEqual(renderer.read_bytes(), (ROOT / "scripts/render_tailscale_web_gate.py").read_bytes())
        self.assertIn("Requires=switchyard-web-gate.service", (self.units / "docker.service.d/50-switchyard-web-gate.conf").read_text())
        self.assertIn(str(self.nft_dir / "web-gate.nft"), (self.units / "switchyard-web-gate.service").read_text())
        self.assertIn(str(renderer), (self.units / "switchyard-web-gate.service").read_text())
        self.assertIn("--require-matching-output", (self.units / "switchyard-web-gate.service").read_text())
        self.assertNotIn("--output", (self.units / "switchyard-web-gate.service").read_text())
        self.assertNotIn(str(ROOT / "scripts/render_tailscale_web_gate.py"), (self.units / "switchyard-web-gate.service").read_text())
        mutations = self.log.read_text().splitlines()
        renderer_install = next(line for line in mutations if "-m 0755" in line and "render_tailscale_web_gate.py" in line)
        self.assertIn("-o root -g root", renderer_install)
        rules_install = next(line for line in mutations if "-m 0644" in line and "web-gate.nft" in line)
        self.assertIn("-o root -g root", rules_install)
        self.assertLess(mutations.index(rules_install),
                        mutations.index("systemctl restart switchyard-web-gate.service"))

    def test_nft_preflight_failure_makes_no_mutation(self):
        result = self.run_installer({"FAIL_NFT_CHECK": "1"})
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(self.log.exists())
        self.assertEqual(list(self.units.iterdir()), [])
        self.assertEqual(list(self.nft_dir.iterdir()), [])

    def test_creates_missing_nft_directory(self):
        self.nft_dir.rmdir()
        result = self.run_installer()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue((self.nft_dir / "web-gate.nft").is_file())

    def test_rejects_symlink_destination_before_mutation(self):
        target = self.root / "target"
        target.write_text("keep\n")
        (self.units / "switchyard-web-gate.service").symlink_to(target)
        result = self.run_installer()
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(self.log.exists())
        self.assertEqual(target.read_text(), "keep\n")

    def test_start_failure_restores_files_rules_and_unit_state(self):
        rules = self.nft_dir / "web-gate.nft"
        unit = self.units / "switchyard-web-gate.service"
        renderer = self.lib_dir / "render_tailscale_web_gate.py"
        rules.write_text("old rules\n")
        unit.write_text("old unit\n")
        renderer.write_text("old renderer\n")
        rules.chmod(0o640)
        unit.chmod(0o600)
        renderer.chmod(0o700)
        result = self.run_installer(
            {"FAIL_START": "1", "PRIOR_TABLE": "1", "PRIOR_ENABLED": "1", "PRIOR_ACTIVE": "1"}
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(rules.read_text(), "old rules\n")
        self.assertEqual(unit.read_text(), "old unit\n")
        self.assertEqual(renderer.read_text(), "old renderer\n")
        self.assertEqual(rules.stat().st_mode & 0o777, 0o640)
        self.assertEqual(unit.stat().st_mode & 0o777, 0o600)
        self.assertEqual(renderer.stat().st_mode & 0o777, 0o700)
        mutations = self.log.read_text()
        self.assertIn("nft delete table inet switchyard_web_gate", mutations)
        self.assertIn("systemctl enable switchyard-web-gate.service", mutations)
        self.assertIn("systemctl start switchyard-web-gate.service", mutations)
        lines = mutations.splitlines()
        self.assertLess(
            lines.index("systemctl start switchyard-web-gate.service"),
            max(i for i, line in enumerate(lines) if line == "nft delete table inet switchyard_web_gate"),
        )

    def test_dependency_verification_failure_restores_prior_dropin(self):
        dropin = self.units / "docker.service.d"
        dropin.mkdir()
        prior = dropin / "50-switchyard-web-gate.conf"
        prior.write_text("old dependency\n")
        result = self.run_installer({"FAIL_DOCKER_DEPENDENCY": "1"})
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(prior.read_text(), "old dependency\n")
        self.assertGreaterEqual(self.log.read_text().count("systemctl daemon-reload"), 2)

    def test_changed_gate_stops_running_web_before_install_and_leaves_it_stopped_on_failure(self):
        result = self.run_installer({"WEB_RUNNING": "1", "FAIL_START": "1"})
        self.assertNotEqual(result.returncode, 0)
        mutations = self.log.read_text().splitlines()
        stop = next(i for i, line in enumerate(mutations) if line.startswith("docker ") and line.endswith(" stop web"))
        install = next(i for i, line in enumerate(mutations) if "install -o root -g root -m 0755" in line)
        self.assertLess(stop, install)
        self.assertFalse(any(" up " in line for line in mutations))

    def test_unchanged_active_gate_does_not_stop_web_or_reload_gate(self):
        first = self.run_installer()
        self.assertEqual(first.returncode, 0, first.stderr)
        self.log.unlink()
        result = self.run_installer({"WEB_RUNNING": "1", "PRIOR_ACTIVE": "1", "PRIOR_ENABLED": "1"})
        self.assertEqual(result.returncode, 0, result.stderr)
        mutations = self.log.read_text().splitlines() if self.log.exists() else []
        self.assertFalse(any(line.startswith("docker ") for line in mutations))
        self.assertNotIn("systemctl restart switchyard-web-gate.service", mutations)

    def test_reinstalls_unchanged_rules_and_renderer_with_safe_metadata(self):
        first = self.run_installer()
        self.assertEqual(first.returncode, 0, first.stderr)
        rules = self.nft_dir / "web-gate.nft"
        renderer = self.lib_dir / "render_tailscale_web_gate.py"
        rules.chmod(0o600)
        renderer.chmod(0o700)
        self.log.unlink()
        result = self.run_installer({
            "PRIOR_ACTIVE": "1", "PRIOR_ENABLED": "1",
            "RULES_MODE": "600", "RENDERER_MODE": "700",
        })
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(rules.stat().st_mode & 0o777, 0o644)
        self.assertEqual(renderer.stat().st_mode & 0o777, 0o755)
        self.assertIn("systemctl restart switchyard-web-gate.service", self.log.read_text().splitlines())

    def test_unchanged_missing_live_table_stops_web_and_reapplies_gate(self):
        first = self.run_installer()
        self.assertEqual(first.returncode, 0, first.stderr)
        self.log.unlink()
        (self.root / "table-active").unlink()
        result = self.run_installer({"WEB_RUNNING": "1", "PRIOR_ACTIVE": "1", "PRIOR_ENABLED": "1"})
        self.assertEqual(result.returncode, 0, result.stderr)
        mutations = self.log.read_text().splitlines()
        stop = next(i for i, line in enumerate(mutations) if line.startswith("docker ") and line.endswith(" stop web"))
        restart = mutations.index("systemctl restart switchyard-web-gate.service")
        self.assertLess(stop, restart)

    def test_unchanged_inactive_gate_failure_leaves_web_stopped(self):
        first = self.run_installer()
        self.assertEqual(first.returncode, 0, first.stderr)
        self.log.unlink()
        (self.root / "active").unlink()
        result = self.run_installer({"WEB_RUNNING": "1", "FAIL_START": "1", "PRIOR_ENABLED": "1"})
        self.assertNotEqual(result.returncode, 0)
        mutations = self.log.read_text().splitlines()
        self.assertTrue(any(line.startswith("docker ") and line.endswith(" stop web") for line in mutations))
        self.assertFalse(any(" up " in line for line in mutations))

    def test_live_rule_verification_failure_does_not_report_success(self):
        first = self.run_installer()
        self.assertEqual(first.returncode, 0, first.stderr)
        self.log.unlink()
        result = self.run_installer({"WEB_RUNNING": "1", "PRIOR_ACTIVE": "1", "BAD_LIVE_RULES": "1"})
        self.assertNotEqual(result.returncode, 0)
        mutations = self.log.read_text().splitlines()
        self.assertTrue(any(line.startswith("docker ") and line.endswith(" stop web") for line in mutations))
        self.assertIn("systemctl restart switchyard-web-gate.service", mutations)

    def test_rejects_relative_and_symlink_env_paths_before_mutation(self):
        relative = self.run_installer({"SWITCHYARD_ENV_FILE": ".env"})
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

    def test_rejects_deployment_user_writable_renderer_path(self):
        result = self.run_installer({"UNSAFE_LIB": "1"})
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("root-owned", result.stderr)
        self.assertFalse(self.log.exists())

    def test_rejects_non_traversable_rules_path(self):
        result = self.run_installer({"NON_TRAVERSABLE_NFT": "1"})
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("traversable", result.stderr)
        self.assertFalse(self.log.exists())


if __name__ == "__main__":
    unittest.main()
