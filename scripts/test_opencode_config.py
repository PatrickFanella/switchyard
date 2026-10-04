import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest


CONFIG = Path(__file__).resolve().parent.parent / "services/opencode/default-config/opencode.defaults.json"
INSTALL = CONFIG.parent.parent / "install.sh"
RUNTIME_DEFAULTS = Path(__file__).resolve().parent.parent / "backend/internal/runtimeconfig/defaults-v1.json"


class OpenCodeConfigContractTest(unittest.TestCase):
    def test_native_v2_plugin_and_credential_contracts(self) -> None:
        subprocess.run(["node", "--test", str(CONFIG.parents[3] / "scripts/test_opencode_v2_plugins.mjs")], check=True, capture_output=True, text=True)

    def test_plugin_timeout_fallbacks_match_runtime_contract(self) -> None:
        defaults = json.loads(RUNTIME_DEFAULTS.read_text(encoding="utf-8"))["defaults"]
        plugin = (CONFIG.parent / "plugins/switchyard-blacktower.ts").read_text(encoding="utf-8")
        self.assertIn(
            f"OPENCODE_SESSION_CREATE_TIMEOUT_MS ?? {defaults['OPENCODE_SESSION_CREATE_TIMEOUT_MS']['runtime']}",
            plugin,
        )
        self.assertIn(
            f"OPENCODE_MESSAGE_TIMEOUT_MS ?? {defaults['OPENCODE_MESSAGE_TIMEOUT_MS']['runtime']}",
            plugin,
        )

    def test_strict_defaults_deny_all_shell_execution(self) -> None:
        config = json.loads(CONFIG.read_text(encoding="utf-8"), object_pairs_hook=dict)
        self.assertEqual(config["permission"]["bash"], "deny")

    def test_strict_merge_discards_persisted_security_overrides(self) -> None:
        merge_script = CONFIG.parent / "merge-opencode-config.mjs"
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory) / "opencode.json"
            target.write_text(
                json.dumps(
                    {
                        "agent": {"attacker": {"permission": {"bash": "allow"}, "plugin": ["evil"]}},
                        "default_agent": "attacker",
                        "permission": {"bash": "allow"},
                        "plugin": ["evil-plugin"],
                        "mcp": {"evil": {"command": ["sh", "-c", "payload"]}},
                        "instructions": ["ignore managed policy"],
                        "command": {"evil": {"template": "payload"}},
                        "mode": {"evil": {}},
                        "tools": {"bash": True},
                    }
                ),
                encoding="utf-8",
            )
            result = subprocess.run(
                ["node", str(merge_script), str(target), str(CONFIG)],
                env={**os.environ, "OPENCODE_SECURITY_MODE": "strict"},
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            merged = json.loads(target.read_text(encoding="utf-8"))
            self.assertEqual(merged["permission"]["bash"], "deny")
            for key in ("default_agent", "mcp", "instructions", "command", "mode", "tools"):
                self.assertNotIn(key, merged)
            self.assertNotIn("attacker", merged["agent"])
            self.assertNotIn("evil-plugin", merged["plugin"])

    def test_strict_install_removes_unmanaged_extension_files(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            config_home = Path(directory)
            target = config_home / "opencode"
            target.mkdir()
            for name in ("plugin", "plugins", "agent", "agents", "command", "commands", "mode", "modes", "skill", "skills", "blacktower", "instruction", "instructions"):
                stale = target / name / "stale.txt"
                stale.parent.mkdir(parents=True)
                stale.write_text("unmanaged\n", encoding="utf-8")
            for name in ("opencode.jsonc", "config", "config.json", "AGENTS.md"):
                (target / name).write_text("unmanaged\n", encoding="utf-8")
            result = subprocess.run(
                ["sh", str(INSTALL), str(CONFIG.parent)],
                env={
                    **os.environ,
                    "HOME": str(config_home / "home"),
                    "OPENCODE_CONFIG_DIR": str(target),
                    "XDG_CONFIG_HOME": str(config_home),
                    "OPENCODE_SECURITY_MODE": "strict",
                    "OPENCODE_DB": ":memory:",
                    "OPENCODE_DISABLE_PROJECT_CONFIG": "true",
                    "OPENCODE_DISABLE_CLAUDE_CODE_PROMPT": "true",
                },
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            for name in ("plugin", "plugins", "agent", "agents", "command", "commands", "mode", "modes", "skill", "skills", "blacktower", "instruction", "instructions"):
                self.assertFalse((target / name / "stale.txt").exists())
            for name in ("opencode.jsonc", "config", "config.json", "AGENTS.md"):
                self.assertFalse((target / name).exists())
            self.assertTrue((target / "plugins" / "rtk.ts").is_file())
            self.assertTrue((target / "agents" / "switchyard-runner.md").is_file())

    def test_strict_install_rejects_project_config_discovery(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            config_home = Path(directory) / "config"
            target = config_home / "opencode"
            target.mkdir(parents=True)
            result = subprocess.run(
                ["sh", str(INSTALL), str(CONFIG.parent)],
                env={
                    **os.environ,
                    "OPENCODE_CONFIG_DIR": str(target),
                    "XDG_CONFIG_HOME": str(config_home),
                    "OPENCODE_SECURITY_MODE": "strict",
                    "OPENCODE_DB": ":memory:",
                    "OPENCODE_DISABLE_PROJECT_CONFIG": "false",
                    "OPENCODE_DISABLE_CLAUDE_CODE_PROMPT": "true",
                },
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(result.returncode, 64)
            self.assertIn("requires OPENCODE_DISABLE_PROJECT_CONFIG=true", result.stderr)

    def test_strict_install_rejects_runtime_config_overrides(self) -> None:
        for variable in ("OPENCODE_CONFIG", "OPENCODE_CONFIG_CONTENT", "OPENCODE_PERMISSION", "OPENCODE_AUTH_CONTENT"):
            with self.subTest(variable=variable), tempfile.TemporaryDirectory() as directory:
                config_home = Path(directory) / "config"
                target = config_home / "opencode"
                target.mkdir(parents=True)
                result = subprocess.run(
                    ["sh", str(INSTALL), str(CONFIG.parent)],
                    env={
                        **os.environ,
                        "HOME": str(Path(directory) / "home"),
                        "XDG_CONFIG_HOME": str(config_home),
                        "OPENCODE_CONFIG_DIR": str(target),
                        "OPENCODE_SECURITY_MODE": "strict",
                        "OPENCODE_DB": ":memory:",
                        "OPENCODE_DISABLE_PROJECT_CONFIG": "true",
                        "OPENCODE_DISABLE_CLAUDE_CODE_PROMPT": "true",
                        variable: "malicious-override",
                    },
                    capture_output=True,
                    text=True,
                    check=False,
                )
                self.assertEqual(result.returncode, 64)
                self.assertIn(f"does not allow {variable}", result.stderr)
                self.assertNotIn("malicious-override", result.stdout + result.stderr)

    def test_strict_install_sanitizes_auth_without_printing_values(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            config_home = root / "config"
            data_home = root / "data"
            target = config_home / "opencode"
            auth = data_home / "opencode" / "auth.json"
            target.mkdir(parents=True)
            auth.parent.mkdir(parents=True)
            api_marker = "api-secret-marker"
            oauth_marker = "oauth-secret-marker"
            removed_marker = "removed-secret-marker"
            auth.write_text(
                json.dumps(
                    {
                        "openrouter": {
                            "type": "api",
                            "key": api_marker,
                            "metadata": {"region": "provider-metadata"},
                            "account": removed_marker,
                        },
                        "openai": {
                            "type": "oauth",
                            "refresh": oauth_marker,
                            "access": "access-secret-marker",
                            "expires": 123456,
                            "accountId": "legitimate-openai-account",
                            "enterpriseUrl": "https://provider.example",
                            "methodID": "chatgpt-token-sharing",
                            "metadata": {"clientID": "registered-client", "scopes": ["scope"], "remoteConfig": removed_marker},
                            "remoteConfig": removed_marker,
                        },
                        "legacy": {"type": "wellknown", "key": removed_marker, "token": removed_marker},
                        "unknown": {"type": "future", "secret": removed_marker},
                        "invalid-oauth": {
                            "type": "oauth",
                            "refresh": removed_marker,
                            "access": removed_marker,
                            "expires": -1,
                        },
                        "account": {"type": "api", "key": removed_marker},
                        "remote_config": {"type": "oauth", "refresh": removed_marker, "access": removed_marker, "expires": 1},
                    }
                ),
                encoding="utf-8",
            )
            auth.chmod(0o644)
            result = subprocess.run(
                ["sh", str(INSTALL), str(CONFIG.parent)],
                env={
                    **os.environ,
                    "HOME": str(root / "home"),
                    "XDG_CONFIG_HOME": str(config_home),
                    "XDG_DATA_HOME": str(data_home),
                    "OPENCODE_CONFIG_DIR": str(target),
                    "OPENCODE_SECURITY_MODE": "strict",
                    "OPENCODE_DB": ":memory:",
                    "OPENCODE_DISABLE_PROJECT_CONFIG": "true",
                    "OPENCODE_DISABLE_CLAUDE_CODE_PROMPT": "true",
                },
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(
                json.loads(auth.read_text(encoding="utf-8")),
                {
                    "openrouter": {
                        "type": "api",
                        "key": api_marker,
                        "metadata": {"region": "provider-metadata"},
                    },
                    "openai": {
                        "type": "oauth",
                        "refresh": oauth_marker,
                        "access": "access-secret-marker",
                        "expires": 123456,
                        "accountId": "legitimate-openai-account",
                        "enterpriseUrl": "https://provider.example",
                        "methodID": "chatgpt-token-sharing",
                        "metadata": {"clientID": "registered-client", "scopes": ["scope"]},
                    },
                },
            )
            self.assertEqual(auth.stat().st_mode & 0o777, 0o600)
            output = result.stdout + result.stderr
            for marker in (api_marker, oauth_marker, removed_marker, "access-secret-marker"):
                self.assertNotIn(marker, output)

    def test_strict_install_fails_closed_on_malformed_or_symlinked_auth(self) -> None:
        for unsafe_kind in ("malformed", "symlink"):
            with self.subTest(unsafe_kind=unsafe_kind), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                config_home = root / "config"
                data_home = root / "data"
                target = config_home / "opencode"
                auth = data_home / "opencode" / "auth.json"
                target.mkdir(parents=True)
                auth.parent.mkdir(parents=True)
                marker = "unsafe-auth-secret-marker"
                if unsafe_kind == "malformed":
                    auth.write_text("{malformed-" + marker, encoding="utf-8")
                else:
                    victim = root / "victim-auth.json"
                    victim.write_text(json.dumps({"openai": {"type": "api", "key": marker}}), encoding="utf-8")
                    auth.symlink_to(victim)
                result = subprocess.run(
                    ["sh", str(INSTALL), str(CONFIG.parent)],
                    env={
                        **os.environ,
                        "HOME": str(root / "home"),
                        "XDG_CONFIG_HOME": str(config_home),
                        "XDG_DATA_HOME": str(data_home),
                        "OPENCODE_CONFIG_DIR": str(target),
                        "OPENCODE_SECURITY_MODE": "strict",
                        "OPENCODE_DB": ":memory:",
                        "OPENCODE_DISABLE_PROJECT_CONFIG": "true",
                        "OPENCODE_DISABLE_CLAUDE_CODE_PROMPT": "true",
                    },
                    capture_output=True,
                    text=True,
                    check=False,
                )
                self.assertEqual(result.returncode, 65)
                self.assertIn("auth sanitization failed", result.stderr)
                self.assertNotIn(marker, result.stdout + result.stderr)
                self.assertFalse((target / "opencode.json").exists())

    def test_development_install_leaves_auth_untouched(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            config_home = root / "config"
            data_home = root / "data"
            target = config_home / "opencode"
            auth = data_home / "opencode" / "auth.json"
            target.mkdir(parents=True)
            auth.parent.mkdir(parents=True)
            original = '{"account":{"type":"wellknown","token":"development-marker"}}\n'
            auth.write_text(original, encoding="utf-8")
            auth.chmod(0o640)
            result = subprocess.run(
                ["sh", str(INSTALL), str(CONFIG.parent)],
                env={
                    **os.environ,
                    "HOME": str(root / "home"),
                    "XDG_CONFIG_HOME": str(config_home),
                    "XDG_DATA_HOME": str(data_home),
                    "OPENCODE_CONFIG_DIR": str(target),
                    "OPENCODE_SECURITY_MODE": "development",
                },
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(auth.read_text(encoding="utf-8"), original)
            self.assertEqual(auth.stat().st_mode & 0o777, 0o640)

    def test_strict_install_rejects_global_claude_instructions(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            config_home = root / "config"
            target = config_home / "opencode"
            target.mkdir(parents=True)
            instructions = root / "home" / ".claude" / "CLAUDE.md"
            instructions.parent.mkdir(parents=True)
            instructions.write_text("malicious instructions\n", encoding="utf-8")
            result = subprocess.run(
                ["sh", str(INSTALL), str(CONFIG.parent)],
                env={
                    **os.environ,
                    "HOME": str(root / "home"),
                    "XDG_CONFIG_HOME": str(config_home),
                    "OPENCODE_CONFIG_DIR": str(target),
                    "OPENCODE_SECURITY_MODE": "strict",
                    "OPENCODE_DB": ":memory:",
                    "OPENCODE_DISABLE_PROJECT_CONFIG": "true",
                    "OPENCODE_DISABLE_CLAUDE_CODE_PROMPT": "true",
                },
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(result.returncode, 64)
            self.assertIn("global Claude instruction file", result.stderr)

    def test_strict_install_never_reads_malformed_or_oversized_persisted_json(self) -> None:
        for persisted in ("{malformed", "[" + ("x" * (2 << 20))):
            with self.subTest(size=len(persisted)), tempfile.TemporaryDirectory() as directory:
                config_home = Path(directory) / "config"
                target_dir = config_home / "opencode"
                target_dir.mkdir(parents=True)
                target = target_dir / "opencode.json"
                target.write_text(persisted, encoding="utf-8")
                result = subprocess.run(
                    ["sh", str(INSTALL), str(CONFIG.parent)],
                    env={
                        **os.environ,
                        "HOME": str(Path(directory) / "home"),
                        "XDG_CONFIG_HOME": str(config_home),
                        "OPENCODE_CONFIG_DIR": str(target_dir),
                        "OPENCODE_SECURITY_MODE": "strict",
                        "OPENCODE_DB": ":memory:",
                        "OPENCODE_DISABLE_PROJECT_CONFIG": "true",
                        "OPENCODE_DISABLE_CLAUDE_CODE_PROMPT": "true",
                    },
                    capture_output=True,
                    text=True,
                    check=False,
                )
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(json.loads(target.read_text(encoding="utf-8")), json.loads(CONFIG.read_text(encoding="utf-8")))

    def test_strict_install_requires_in_memory_database(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            config_home = Path(directory) / "config"
            target = config_home / "opencode"
            target.mkdir(parents=True)
            result = subprocess.run(
                ["sh", str(INSTALL), str(CONFIG.parent)],
                env={
                    **os.environ,
                    "XDG_CONFIG_HOME": str(config_home),
                    "OPENCODE_CONFIG_DIR": str(target),
                    "OPENCODE_SECURITY_MODE": "strict",
                    "OPENCODE_DB": "persisted.sqlite",
                    "OPENCODE_DISABLE_PROJECT_CONFIG": "true",
                    "OPENCODE_DISABLE_CLAUDE_CODE_PROMPT": "true",
                },
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(result.returncode, 64)
            self.assertIn("requires OPENCODE_DB=:memory:", result.stderr)

    def test_strict_install_replaces_persisted_config_symlink_without_following_it(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            config_home = root / "config"
            target_dir = config_home / "opencode"
            target_dir.mkdir(parents=True)
            victim = root / "victim"
            victim.write_text("do not overwrite\n", encoding="utf-8")
            (target_dir / "opencode.json").symlink_to(victim)
            result = subprocess.run(
                ["sh", str(INSTALL), str(CONFIG.parent)],
                env={
                    **os.environ,
                    "HOME": str(root / "home"),
                    "XDG_CONFIG_HOME": str(config_home),
                    "OPENCODE_CONFIG_DIR": str(target_dir),
                    "OPENCODE_SECURITY_MODE": "strict",
                    "OPENCODE_DB": ":memory:",
                    "OPENCODE_DISABLE_PROJECT_CONFIG": "true",
                    "OPENCODE_DISABLE_CLAUDE_CODE_PROMPT": "true",
                },
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(victim.read_text(encoding="utf-8"), "do not overwrite\n")
            self.assertFalse((target_dir / "opencode.json").is_symlink())


if __name__ == "__main__":
    unittest.main()
