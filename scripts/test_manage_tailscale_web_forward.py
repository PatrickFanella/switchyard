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
        self.state = self.root / "rule"
        self.env_file = self.root / "gate.env"
        self.env_file.write_text(
            "SWITCHYARD_WEB_BIND_HOST=100.65.164.66\n"
            "SWITCHYARD_TAILSCALE_PROXY_IPV4=100.77.128.80\n"
            "WEB_PORT=8081\n"
            "SWITCHYARD_DOCKER_BRIDGE_NAME=switchyard0\n"
            "SWITCHYARD_DOCKER_BRIDGE_SUBNET=172.31.250.0/24\n"
            "SWITCHYARD_DOCKER_BRIDGE_GATEWAY=172.31.250.1\n"
            "SWITCHYARD_WEB_CONTAINER_IPV4=172.31.250.10\n"
        )
        self.write_tool("ufw", """#!/bin/sh
printf 'ufw %s\n' "$*" >>"$COMMAND_LOG"
if [ "$*" = status ]; then
  [ "${UFW_ACTIVE:-1}" = 1 ] && { printf 'Status: active\n'; exit 0; }
  printf 'Status: inactive\n'; exit 0
fi
case "$*" in
  'route allow '*) [ "${FAIL_ADD:-0}" = 0 ] || exit 1; : >"$RULE_STATE";;
  '--force route delete allow '*) rm -f "$RULE_STATE";;
  *) exit 2;;
esac
""")
        self.write_tool("iptables", """#!/bin/sh
printf 'iptables %s\n' "$*" >>"$COMMAND_LOG"
[ -e "$RULE_STATE" ]
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
            "RULE_STATE": str(self.state),
        })
        env.update(extra or {})
        return subprocess.run(
            [sys.executable, str(SCRIPT), action, "--env-file", str(self.env_file)],
            env=env, capture_output=True, text=True, check=False,
        )

    def test_ensures_exact_rule_before_docker_user_deny(self):
        result = self.execute("ensure")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, "created\n")
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
        joined = "\n".join(commands)
        self.assertNotIn("100.64.0.0/10", joined)
        self.assertNotIn("172.31.250.0/24", joined)
        self.assertNotIn("172.16.0.0/12", joined)

    def test_ensure_is_idempotent(self):
        self.state.touch()
        result = self.execute("ensure")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, "existing\n")
        self.assertFalse(any("route allow" in line for line in self.log.read_text().splitlines()))

    def test_delete_removes_only_exact_rule(self):
        self.state.touch()
        result = self.execute("delete")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn(
            "ufw --force route delete allow in on tailscale0 out on switchyard0 proto tcp "
            "from 100.77.128.80 to 172.31.250.10 port 80 comment switchyard-web-gate",
            self.log.read_text().splitlines(),
        )

    def test_refuses_inactive_ufw_without_mutation(self):
        result = self.execute("ensure", {"UFW_ACTIVE": "0"})
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(self.state.exists())
        self.assertFalse(any("route allow" in line for line in self.log.read_text().splitlines()))

    def test_gate_verification_requires_ufw_and_live_exact_nft(self):
        self.state.touch()
        result = self.execute("verify-gate")
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_gate_verification_rejects_extra_nft_rule(self):
        self.state.touch()
        result = self.execute("verify-gate", {"EXTRA_NFT_RULE": "1"})
        self.assertNotEqual(result.returncode, 0)


if __name__ == "__main__":
    unittest.main()
