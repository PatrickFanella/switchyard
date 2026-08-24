import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


ROOT = Path(__file__).resolve().parent.parent
SCRIPT = ROOT / "scripts/manage_tailscale_web_forward.py"


class UfwForwardTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.bin = self.root / "bin"
        self.bin.mkdir()
        self.log = self.root / "commands"
        self.web_state = self.root / "web-rule"
        self.model_state = self.root / "model-rule"
        self.env_file = self.root / "gate.env"
        self.env_file.write_text(
            "SWITCHYARD_WEB_BIND_HOST=100.65.164.66\n"
            "SWITCHYARD_TAILSCALE_PROXY_IPV4=100.77.128.80\n"
            "WEB_PORT=8081\n"
            "SWITCHYARD_DOCKER_BRIDGE_NAME=switchyard0\n"
            "SWITCHYARD_DOCKER_BRIDGE_SUBNET=172.31.250.0/24\n"
            "SWITCHYARD_DOCKER_BRIDGE_GATEWAY=172.31.250.1\n"
            "SWITCHYARD_API_CONTAINER_IPV4=172.31.250.4\n"
            "SWITCHYARD_WEB_CONTAINER_IPV4=172.31.250.10\n"
        )
        self.write_tool("ufw", """#!/bin/sh
printf 'ufw %s\n' "$*" >>"$COMMAND_LOG"
if [ "$*" = status ]; then
  [ "${UFW_ACTIVE:-1}" = 1 ] && { printf 'Status: active\n'; exit 0; }
  printf 'Status: inactive\n'; exit 0
fi
case "$*" in
  'route allow '*) [ "${FAIL_WEB_ADD:-0}" = 0 ] || exit 1; : >"$WEB_RULE_STATE"; printf 'Rule added\n';;
  'allow in '*) [ "${FAIL_MODEL_ADD:-0}" = 0 ] || exit 1; : >"$MODEL_RULE_STATE"; printf 'Rule added\n';;
  '--force route delete allow '*) rm -f "$WEB_RULE_STATE";;
  '--force delete allow in '*) rm -f "$MODEL_RULE_STATE";;
  *) exit 2;;
esac
""")
        self.write_tool("iptables", """#!/bin/sh
printf 'iptables %s\n' "$*" >>"$COMMAND_LOG"
case "$*" in
  '-C ufw-user-forward '*) [ -e "$WEB_RULE_STATE" ];;
  '-C ufw-user-input '*) [ -e "$MODEL_RULE_STATE" ];;
  *) exit 2;;
esac
""")
        self.write_tool("nft", """#!/bin/sh
printf 'table inet switchyard_web_gate {\n'
printf 'chain prerouting {\n'
printf 'type filter hook prerouting priority dstnat - 1; policy accept;\n'
if [ "${EXTRA_NFT_RULE:-0}" = 1 ]; then printf 'ip saddr 100.64.0.0/10 accept\n'; fi
printf 'iifname "tailscale0" ip saddr 100.77.128.80 ip daddr 100.65.164.66 tcp dport 8081 counter packets 1 bytes 64 accept comment "allow Tailscale proxy to web"\n'
printf 'ip daddr 100.65.164.66 tcp dport 8081 counter packets 0 bytes 0 drop comment "drop other ingress to web"\n}\n'
printf 'chain output {\n'
printf 'type filter hook output priority dstnat - 1; policy accept;\n'
printf 'ip daddr 100.65.164.66 tcp dport 8081 counter packets 0 bytes 0 drop comment "drop local access to web"\n}\n}\n'
""")

    def write_tool(self, name, content):
        path = self.bin / name
        path.write_text(content)
        path.chmod(0o755)

    def execute(self, action, extra=None):
        env = os.environ.copy()
        env.update({
            "PATH": f"{self.bin}:{env['PATH']}",
            "COMMAND_LOG": str(self.log),
            "WEB_RULE_STATE": str(self.web_state),
            "MODEL_RULE_STATE": str(self.model_state),
        })
        env.update(extra or {})
        return subprocess.run(
            [sys.executable, str(SCRIPT), action, "--env-file", str(self.env_file)],
            env=env, capture_output=True, text=True, check=False,
        )

    def test_ensures_exact_rule_before_docker_user_deny(self):
        result = self.execute("ensure")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, "web=created\nmodel=created\n")
        commands = self.log.read_text().splitlines()
        exact = (
            "-i tailscale0 -o switchyard0 -p tcp -s 100.77.128.80/32 "
            "-d 172.31.250.10/32 --dport 80 -j ACCEPT"
        )
        self.assertIn(f"iptables -C ufw-user-forward {exact}", commands)
        self.assertIn(
            "ufw route allow in on tailscale0 out on switchyard0 proto tcp from "
            "100.77.128.80 to 172.31.250.10 port 80 comment switchyard-web-gate",
            commands,
        )
        self.assertIn(
            "iptables -C ufw-user-input -i switchyard0 -p tcp -s 172.31.250.4/32 "
            "-d 172.31.250.1/32 --dport 11435 -j ACCEPT",
            commands,
        )
        self.assertIn(
            "ufw allow in on switchyard0 proto tcp from 172.31.250.4 to "
            "172.31.250.1 port 11435 comment switchyard-model-gate",
            commands,
        )
        self.assertLess(commands.index(next(line for line in commands if line.startswith("ufw route allow"))), commands.index(next(line for line in commands if line.startswith("ufw allow in"))))
        joined = "\n".join(commands)
        self.assertNotIn("100.64.0.0/10", joined)
        self.assertNotIn("172.31.250.0/24", joined)
        self.assertNotIn("172.16.0.0/12", joined)

    def test_ensure_is_idempotent(self):
        self.web_state.touch()
        self.model_state.touch()
        result = self.execute("ensure")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, "web=existing\nmodel=existing\n")
        self.assertFalse(any("route allow" in line for line in self.log.read_text().splitlines()))

    def test_delete_removes_only_exact_rule(self):
        self.web_state.touch()
        self.model_state.touch()
        result = self.execute("delete")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn(
            "ufw --force route delete allow in on tailscale0 out on switchyard0 proto tcp "
            "from 100.77.128.80 to 172.31.250.10 port 80 comment switchyard-web-gate",
            self.log.read_text().splitlines(),
        )
        self.assertIn(
            "ufw --force delete allow in on switchyard0 proto tcp from 172.31.250.4 "
            "to 172.31.250.1 port 11435 comment switchyard-model-gate",
            self.log.read_text().splitlines(),
        )

    def test_ensure_preserves_preexisting_web_rule(self):
        self.web_state.touch()
        result = self.execute("ensure")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, "web=existing\nmodel=created\n")

    def test_failed_model_add_rolls_back_only_new_web_rule(self):
        result = self.execute("ensure", {"FAIL_MODEL_ADD": "1"})
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(self.web_state.exists())
        self.assertFalse(self.model_state.exists())

        self.log.unlink()
        self.web_state.touch()
        result = self.execute("ensure", {"FAIL_MODEL_ADD": "1"})
        self.assertNotEqual(result.returncode, 0)
        self.assertTrue(self.web_state.exists())
        self.assertFalse(any("route delete" in line for line in self.log.read_text().splitlines()))

    def test_delete_actions_are_independent(self):
        self.web_state.touch()
        self.model_state.touch()
        self.assertEqual(self.execute("delete-model").returncode, 0)
        self.assertTrue(self.web_state.exists())
        self.assertFalse(self.model_state.exists())

    def test_refuses_inactive_ufw_without_mutation(self):
        result = self.execute("ensure", {"UFW_ACTIVE": "0"})
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(self.web_state.exists())
        self.assertFalse(self.model_state.exists())
        self.assertFalse(any("route allow" in line for line in self.log.read_text().splitlines()))

    def test_gate_verification_requires_ufw_and_live_exact_nft(self):
        self.web_state.touch()
        self.model_state.touch()
        result = self.execute("verify-gate")
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_gate_verification_rejects_extra_nft_rule(self):
        self.web_state.touch()
        self.model_state.touch()
        result = self.execute("verify-gate", {"EXTRA_NFT_RULE": "1"})
        self.assertNotEqual(result.returncode, 0)


if __name__ == "__main__":
    unittest.main()
