import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

from validate_production_config import (
    MANAGED_MARKER,
    MANAGED_MARKER_CONTENT,
    WORKTREE_DIRECTORY,
    parse_dotenv,
    unsafe_local_git_config_key,
    validate,
)


REPO_ROOT = Path(__file__).resolve().parent.parent


def make_owned_workspace(parent: str) -> tuple[Path, int, int]:
    workspace = Path(parent) / "opencode-workspace"
    workspace.mkdir(mode=0o750)
    subprocess.run(
        ["git", "init", "--initial-branch=main", str(workspace)],
        check=True,
        capture_output=True,
        text=True,
    )
    worktree_root = workspace / WORKTREE_DIRECTORY
    worktree_root.mkdir(mode=0o700)
    marker = worktree_root / MANAGED_MARKER
    marker.write_text(MANAGED_MARKER_CONTENT, encoding="utf-8")
    marker.chmod(0o600)
    config_dir = Path(parent) / "opencode-config"
    data_dir = Path(parent) / "opencode-data"
    config_dir.mkdir(mode=0o700)
    data_dir.mkdir(mode=0o700)
    uid = os.getuid()
    gid = os.getgid()
    if uid == 0 or gid == 0:
        uid = 65534
        gid = 65534
        os.chown(workspace, uid, gid)
        os.chown(worktree_root, uid, gid)
        os.chown(marker, uid, gid)
        os.chown(config_dir, uid, gid)
        os.chown(data_dir, uid, gid)
    return workspace, uid, gid


class ProductionConfigTest(unittest.TestCase):
    def setUp(self) -> None:
        temporary_directory = tempfile.TemporaryDirectory()
        self.addCleanup(temporary_directory.cleanup)
        workspace, uid, gid = make_owned_workspace(temporary_directory.name)
        self.env = {
            "OPENCODE_SECURITY_MODE": "strict",
            "OPENCODE_DB": ":memory:",
            "OPENCODE_DISABLE_PROJECT_CONFIG": "true",
            "OPENCODE_DISABLE_CLAUDE_CODE_PROMPT": "true",
            "OPENCODE_SERVER_PASSWORD": "production-test-password",
            "OPENCODE_WORKSPACE": str(workspace),
            "OPENCODE_RUNTIME_UID": str(uid),
            "OPENCODE_RUNTIME_GID": str(gid),
            "OPENCODE_CONFIG_DIR": str(workspace.parent / "opencode-config"),
            "OPENCODE_DATA_DIR": str(workspace.parent / "opencode-data"),
            "OPEN_PILOT_REPO_PATH": "/workspace",
            "OPEN_PILOT_WORKTREE_ROOT": "/workspace/.open-pilot-worktrees",
            "SWITCHYARD_WEB_TRANSPORT": "tailscale",
            "SWITCHYARD_WEB_BIND_HOST": "100.65.164.66",
            "SWITCHYARD_WEB_TRUSTED_PROXY_CIDR": "100.77.128.80/32",
            "SWITCHYARD_DASHBOARD_CIDR": "127.0.0.1/32",
            "SWITCHYARD_PUBLIC_URL": "https://switchyard.example.test",
            "SWITCHYARD_REVERSE_PROXY_AUTH": "true",
            "SWITCHYARD_BIND_HOST": "127.0.0.1",
            "SWITCHYARD_MODEL_BIND_HOST": "100.65.164.66",
            "POSTGRES_PORT": "5432",
            "API_PORT": "18080",
            "LLAMA_LINE_PORT": "11434",
            "WEB_PORT": "8081",
        }

    def assert_rejected(self, name: str, value: str, message: str) -> None:
        self.env[name] = value
        self.assertIn(message, validate(self.env))

    def test_accepts_approved_tailscale_contract(self) -> None:
        self.assertEqual(validate(self.env), [])

    def test_rejects_non_strict_opencode(self) -> None:
        self.assert_rejected(
            "OPENCODE_SECURITY_MODE",
            "development",
            "OPENCODE_SECURITY_MODE must be strict",
        )

    def test_rejects_project_config_discovery(self) -> None:
        self.assert_rejected(
            "OPENCODE_DISABLE_PROJECT_CONFIG",
            "false",
            "OPENCODE_DISABLE_PROJECT_CONFIG must be true",
        )

    def test_rejects_persisted_opencode_database(self) -> None:
        self.assert_rejected("OPENCODE_DB", "opencode.db", "OPENCODE_DB must be :memory:")

    def test_rejects_claude_prompt_discovery(self) -> None:
        self.assert_rejected(
            "OPENCODE_DISABLE_CLAUDE_CODE_PROMPT",
            "false",
            "OPENCODE_DISABLE_CLAUDE_CODE_PROMPT must be true",
        )

    def test_rejects_missing_blank_and_default_opencode_password(self) -> None:
        for value in (
            None,
            "",
            "   ",
            "opencode-change-me",
            "switchyard-local-opencode-password",
        ):
            with self.subTest(value=value):
                env = dict(self.env)
                if value is None:
                    env.pop("OPENCODE_SERVER_PASSWORD")
                else:
                    env["OPENCODE_SERVER_PASSWORD"] = value
                self.assertIn(
                    "OPENCODE_SERVER_PASSWORD must be set to a non-default value",
                    validate(env),
                )

    def test_rejects_relative_workspace(self) -> None:
        self.assert_rejected(
            "OPENCODE_WORKSPACE",
            "tmp/opencode-workspace",
            "OPENCODE_WORKSPACE must be an absolute existing directory",
        )

    def test_rejects_absolute_nonexistent_workspace(self) -> None:
        current = Path(self.env["OPENCODE_WORKSPACE"])
        self.assert_rejected(
            "OPENCODE_WORKSPACE",
            str(current.parent / "missing-workspace"),
            "OPENCODE_WORKSPACE must be an absolute existing directory",
        )

    def test_rejects_workspace_with_symlinked_parent(self) -> None:
        workspace = Path(self.env["OPENCODE_WORKSPACE"])
        linked_parent = workspace.parent / "linked-workspace-parent"
        linked_parent.symlink_to(workspace.parent, target_is_directory=True)
        self.addCleanup(linked_parent.unlink)
        self.assert_rejected(
            "OPENCODE_WORKSPACE",
            str(linked_parent / workspace.name),
            "OPENCODE_WORKSPACE must be an absolute existing directory",
        )

    def test_rejects_workspace_inside_repository(self) -> None:
        with tempfile.TemporaryDirectory(dir=REPO_ROOT) as directory:
            self.env["OPENCODE_WORKSPACE"] = directory
            self.assertIn(
                "OPENCODE_WORKSPACE must be dedicated and outside this repository",
                validate(self.env),
            )

    def test_rejects_workspace_containing_repository(self) -> None:
        self.assert_rejected(
            "OPENCODE_WORKSPACE",
            str(REPO_ROOT.parent),
            "OPENCODE_WORKSPACE must be dedicated and outside this repository",
        )

    def test_rejects_zero_and_non_numeric_runtime_ids(self) -> None:
        self.assert_rejected(
            "OPENCODE_RUNTIME_UID",
            "0",
            "OPENCODE_RUNTIME_UID must be between 1 and 2147483647",
        )
        self.assert_rejected(
            "OPENCODE_RUNTIME_GID",
            "operator",
            "OPENCODE_RUNTIME_GID must be a numeric non-root ID",
        )

    def test_rejects_workspace_owner_mismatch(self) -> None:
        self.env["OPENCODE_RUNTIME_UID"] = str(int(self.env["OPENCODE_RUNTIME_UID"]) + 1)
        self.assertIn(
            "OPENCODE_WORKSPACE owner must match OPENCODE_RUNTIME_UID",
            validate(self.env),
        )

    def test_rejects_workspace_group_mismatch(self) -> None:
        self.env["OPENCODE_RUNTIME_GID"] = str(int(self.env["OPENCODE_RUNTIME_GID"]) + 1)
        self.assertIn(
            "OPENCODE_WORKSPACE group must match OPENCODE_RUNTIME_GID",
            validate(self.env),
        )

    def test_rejects_missing_or_unsafe_managed_layout(self) -> None:
        workspace = Path(self.env["OPENCODE_WORKSPACE"])
        root = workspace / WORKTREE_DIRECTORY
        marker = root / MANAGED_MARKER

        marker.unlink()
        self.assertIn("managed worktree root marker is missing or invalid", validate(self.env))
        marker.write_text(MANAGED_MARKER_CONTENT, encoding="utf-8")
        marker.chmod(0o666)
        self.assertIn("managed marker must be private", validate(self.env))
        marker.chmod(0o600)
        root.chmod(0o707)
        self.assertIn("managed worktree root must not be group- or world-writable", validate(self.env))

    def test_rejects_group_or_world_writable_trust_boundary(self) -> None:
        workspace = Path(self.env["OPENCODE_WORKSPACE"])
        paths = {
            "workspace": workspace,
            "config": Path(self.env["OPENCODE_CONFIG_DIR"]),
            "data": Path(self.env["OPENCODE_DATA_DIR"]),
            "git": workspace / ".git",
            "worktree root": workspace / WORKTREE_DIRECTORY,
            "marker": workspace / WORKTREE_DIRECTORY / MANAGED_MARKER,
        }
        for name, path in paths.items():
            original = path.stat().st_mode & 0o777
            for write_bit in (0o020, 0o002):
                with self.subTest(path=name, write_bit=oct(write_bit)):
                    path.chmod(original | write_bit)
                    self.assertTrue(
                        any("writable" in error or "private" in error for error in validate(self.env)),
                        f"{name} mode {oct(original | write_bit)} was accepted",
                    )
                    path.chmod(original)

    def test_rejects_non_git_workspace_and_git_file(self) -> None:
        workspace = Path(self.env["OPENCODE_WORKSPACE"])
        git_metadata = workspace / ".git"
        import shutil

        shutil.rmtree(git_metadata)
        self.assertIn("OPENCODE_WORKSPACE .git must be a non-symlink directory", validate(self.env))
        git_metadata.write_text("gitdir: /tmp/fake\n", encoding="utf-8")
        self.assertIn("OPENCODE_WORKSPACE .git must be a non-symlink directory", validate(self.env))

    def test_rejects_unsafe_git_config_without_returning_values(self) -> None:
        workspace = Path(self.env["OPENCODE_WORKSPACE"])
        marker = "credential-marker-must-not-print"
        subprocess.run(
            ["git", "-C", str(workspace), "config", "--local", "credential.helper", marker],
            check=True,
            capture_output=True,
            text=True,
        )
        errors = validate(self.env)
        self.assertIn(
            "OPENCODE_WORKSPACE local Git redirect or credential configuration is not allowed",
            errors,
        )
        self.assertNotIn(marker, "\n".join(errors))

    def test_rejects_command_and_transport_git_config_keys(self) -> None:
        for key in (
            "gpg.program",
            "gpg.ssh.program",
            "http.sslVerify",
            "http.sslCert",
            "http.sslKey",
            "protocol.ext.allow",
            "core.gitProxy",
            "core.worktree",
            "remote.origin.uploadpack",
            "diff.secret.textconv",
            "submodule.secret.update",
        ):
            with self.subTest(key=key):
                self.assertTrue(unsafe_local_git_config_key(key))

    def test_rejects_unsafe_config_and_data_directories(self) -> None:
        workspace = Path(self.env["OPENCODE_WORKSPACE"])
        cases = (
            ("OPENCODE_CONFIG_DIR", "/"),
            ("OPENCODE_DATA_DIR", str(REPO_ROOT)),
            ("OPENCODE_CONFIG_DIR", str(workspace)),
        )
        for name, value in cases:
            with self.subTest(name=name, value=value):
                env = dict(self.env)
                env[name] = value
                self.assertTrue(any(name in error for error in validate(env)))

    def test_rejects_symlinked_config_directory(self) -> None:
        config = Path(self.env["OPENCODE_CONFIG_DIR"])
        link = config.parent / "config-link"
        link.symlink_to(config, target_is_directory=True)
        self.env["OPENCODE_CONFIG_DIR"] = str(link)
        self.assertIn(
            "OPENCODE_CONFIG_DIR must be an absolute existing dedicated directory",
            validate(self.env),
        )

    def test_rejects_local_remote_override_in_production(self) -> None:
        self.assert_rejected(
            "OPEN_PILOT_ALLOW_LOCAL_GIT_REMOTE",
            "true",
            "OPEN_PILOT_ALLOW_LOCAL_GIT_REMOTE must not be enabled in production",
        )

    def test_push_allow_requires_gitea_url_and_token(self) -> None:
        self.env["OPEN_PILOT_GIT_PUSH_MODE"] = "allow"
        errors = validate(self.env)
        self.assertIn("GITEA_BASE_URL is required when Open Pilot Git push is allowed", errors)
        self.assertIn("GITEA_TOKEN is required when Open Pilot Git push is allowed", errors)
        self.env["GITEA_BASE_URL"] = "https://git.example.test"
        self.env["GITEA_TOKEN"] = "test-token"
        self.assertEqual(validate(self.env), [])

    def test_push_allow_rejects_unsafe_gitea_url(self) -> None:
        self.env.update(
            {
                "OPEN_PILOT_GIT_PUSH_MODE": "allow",
                "GITEA_BASE_URL": "http://user:secret@git.example.test/?token=secret",
                "GITEA_TOKEN": "test-token",
            }
        )
        self.assertIn(
            "GITEA_BASE_URL must be an absolute sanitized HTTPS URL when Open Pilot Git push is allowed",
            validate(self.env),
        )

    def test_rejects_runtime_inaccessible_git_metadata(self) -> None:
        workspace = Path(self.env["OPENCODE_WORKSPACE"])
        git_metadata = workspace / ".git"
        git_metadata.chmod(0o500)
        self.addCleanup(git_metadata.chmod, 0o755)
        self.assertIn(
            "OPENCODE_WORKSPACE .git owner must have read, write, and traverse access",
            validate(self.env),
        )

    def test_rejects_symlinked_managed_marker(self) -> None:
        workspace = Path(self.env["OPENCODE_WORKSPACE"])
        marker = workspace / WORKTREE_DIRECTORY / MANAGED_MARKER
        target = workspace / "marker-target"
        target.write_text(MANAGED_MARKER_CONTENT, encoding="utf-8")
        marker.unlink()
        marker.symlink_to(target)
        self.assertIn("managed worktree root marker is missing or invalid", validate(self.env))

    def test_rejects_noncanonical_container_paths(self) -> None:
        self.assert_rejected(
            "OPEN_PILOT_REPO_PATH",
            "/repo",
            "OPEN_PILOT_REPO_PATH must be /workspace",
        )
        self.assert_rejected(
            "OPEN_PILOT_WORKTREE_ROOT",
            "/tmp/worktrees",
            "OPEN_PILOT_WORKTREE_ROOT must be /workspace/.open-pilot-worktrees",
        )

    def test_rejects_http_transport(self) -> None:
        self.assert_rejected(
            "SWITCHYARD_WEB_TRANSPORT",
            "http",
            "production requires SWITCHYARD_WEB_TRANSPORT=tailscale",
        )

    def test_rejects_non_cgnat_web_bind(self) -> None:
        self.assert_rejected(
            "SWITCHYARD_WEB_BIND_HOST",
            "10.0.0.50",
            "SWITCHYARD_WEB_BIND_HOST must be an IPv4 address within 100.64.0.0/10",
        )

    def test_rejects_non_cgnat_proxy(self) -> None:
        self.assert_rejected(
            "SWITCHYARD_WEB_TRUSTED_PROXY_CIDR",
            "10.0.0.200/32",
            "SWITCHYARD_WEB_TRUSTED_PROXY_CIDR must be within 100.64.0.0/10",
        )

    def test_rejects_broad_proxy_cidr(self) -> None:
        self.assert_rejected(
            "SWITCHYARD_WEB_TRUSTED_PROXY_CIDR",
            "100.77.128.0/24",
            "SWITCHYARD_WEB_TRUSTED_PROXY_CIDR must be an explicit /32",
        )

    def test_rejects_proxy_without_explicit_prefix(self) -> None:
        self.assert_rejected(
            "SWITCHYARD_WEB_TRUSTED_PROXY_CIDR",
            "100.77.128.80",
            "SWITCHYARD_WEB_TRUSTED_PROXY_CIDR must be an explicit /32",
        )

    def test_rejects_same_tailscale_peer(self) -> None:
        self.assert_rejected(
            "SWITCHYARD_WEB_TRUSTED_PROXY_CIDR",
            "100.65.164.66/32",
            "SWITCHYARD_WEB_BIND_HOST and SWITCHYARD_WEB_TRUSTED_PROXY_CIDR must identify distinct Tailscale peers",
        )

    def test_rejects_broad_dashboard_cidr(self) -> None:
        self.assert_rejected(
            "SWITCHYARD_DASHBOARD_CIDR",
            "0.0.0.0/0",
            "SWITCHYARD_DASHBOARD_CIDR must be a literal loopback IPv4 /32",
        )

    def test_rejects_remote_dashboard_peer(self) -> None:
        self.assert_rejected(
            "SWITCHYARD_DASHBOARD_CIDR",
            "100.77.128.80/32",
            "SWITCHYARD_DASHBOARD_CIDR must be a literal loopback IPv4 /32",
        )

    def test_rejects_non_https_public_url(self) -> None:
        self.assert_rejected(
            "SWITCHYARD_PUBLIC_URL",
            "http://switchyard.example.test",
            "SWITCHYARD_PUBLIC_URL must be an absolute HTTPS URL",
        )

    def test_rejects_malformed_public_url(self) -> None:
        self.assert_rejected(
            "SWITCHYARD_PUBLIC_URL",
            "https://[",
            "SWITCHYARD_PUBLIC_URL must be an absolute HTTPS URL",
        )

    def test_rejects_public_url_credentials(self) -> None:
        self.assert_rejected(
            "SWITCHYARD_PUBLIC_URL",
            "https://user:secret@switchyard.example.test",
            "SWITCHYARD_PUBLIC_URL must be an absolute HTTPS URL",
        )

    def test_rejects_public_url_query(self) -> None:
        self.assert_rejected(
            "SWITCHYARD_PUBLIC_URL",
            "https://switchyard.example.test/?token=secret",
            "SWITCHYARD_PUBLIC_URL must be an absolute HTTPS URL",
        )

    def test_rejects_disabled_reverse_proxy_auth(self) -> None:
        self.assert_rejected(
            "SWITCHYARD_REVERSE_PROXY_AUTH",
            "false",
            "SWITCHYARD_REVERSE_PROXY_AUTH must be true",
        )

    def test_rejects_non_loopback_control_bind(self) -> None:
        self.assert_rejected(
            "SWITCHYARD_BIND_HOST",
            "100.65.164.66",
            "SWITCHYARD_BIND_HOST must be a literal loopback IPv4 address",
        )

    def test_rejects_localhost_control_bind(self) -> None:
        self.assert_rejected(
            "SWITCHYARD_BIND_HOST",
            "localhost",
            "SWITCHYARD_BIND_HOST must be a literal loopback IPv4 address",
        )

    def test_rejects_ipv6_control_bind_not_supported_by_compose_publish_syntax(self) -> None:
        self.assert_rejected(
            "SWITCHYARD_BIND_HOST",
            "::1",
            "SWITCHYARD_BIND_HOST must be a literal loopback IPv4 address",
        )

    def test_rejects_control_model_listener_collision(self) -> None:
        self.env["SWITCHYARD_MODEL_BIND_HOST"] = "127.0.0.1"
        self.assert_rejected(
            "LLAMA_LINE_PORT",
            "18080",
            "control and model listeners must not collide",
        )

    def test_accepts_non_tailscale_model_bind(self) -> None:
        self.env["SWITCHYARD_MODEL_BIND_HOST"] = "192.168.1.20"
        self.assertEqual(validate(self.env), [])

    def test_rejects_wildcard_model_bind(self) -> None:
        self.assert_rejected(
            "SWITCHYARD_MODEL_BIND_HOST",
            "0.0.0.0",
            "SWITCHYARD_MODEL_BIND_HOST must be a literal non-unspecified IPv4 address",
        )

    def test_rejects_malformed_model_bind(self) -> None:
        self.assert_rejected(
            "SWITCHYARD_MODEL_BIND_HOST",
            "model.internal",
            "SWITCHYARD_MODEL_BIND_HOST must be a literal non-unspecified IPv4 address",
        )

    def test_rejects_invalid_listener_ports(self) -> None:
        for name in ("POSTGRES_PORT", "API_PORT", "LLAMA_LINE_PORT", "WEB_PORT"):
            with self.subTest(name=name):
                env = dict(self.env)
                env[name] = "not-a-port"
                self.assertIn(f"{name} must be an integer port", validate(env))

    def test_rejects_all_host_listener_collisions(self) -> None:
        cases = (
            ("postgres", "control", {"POSTGRES_PORT": "18080"}),
            (
                "postgres",
                "model",
                {"SWITCHYARD_MODEL_BIND_HOST": "127.0.0.1", "LLAMA_LINE_PORT": "5432"},
            ),
            (
                "postgres",
                "web",
                {"SWITCHYARD_WEB_BIND_HOST": "127.0.0.1", "WEB_PORT": "5432"},
            ),
            (
                "control",
                "model",
                {"SWITCHYARD_MODEL_BIND_HOST": "127.0.0.1", "LLAMA_LINE_PORT": "18080"},
            ),
            (
                "control",
                "web",
                {"SWITCHYARD_WEB_BIND_HOST": "127.0.0.1", "WEB_PORT": "18080"},
            ),
            (
                "model",
                "web",
                {"SWITCHYARD_MODEL_BIND_HOST": "100.65.164.66", "LLAMA_LINE_PORT": "8081"},
            ),
        )
        for first, second, values in cases:
            with self.subTest(first=first, second=second):
                env = dict(self.env)
                env.update(values)
                self.assertIn(f"{first} and {second} listeners must not collide", validate(env))


class ProductionConfigCLITest(unittest.TestCase):
    def test_parses_dotenv_without_expansion(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / ".env"
            path.write_text(
                "export PLAIN=$DO_NOT_EXPAND\nSINGLE='$ALSO_LITERAL'\nDOUBLE=\"$STILL_LITERAL\" # comment\n",
                encoding="utf-8",
            )

            self.assertEqual(
                parse_dotenv(path),
                {
                    "PLAIN": "$DO_NOT_EXPAND",
                    "SINGLE": "$ALSO_LITERAL",
                    "DOUBLE": "$STILL_LITERAL",
                },
            )

    def test_validates_env_file_without_outputting_secrets(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            workspace, uid, gid = make_owned_workspace(directory)
            values = {
                "OPENCODE_SECURITY_MODE": "strict",
                "OPENCODE_DB": ":memory:",
                "OPENCODE_DISABLE_PROJECT_CONFIG": "true",
                "OPENCODE_DISABLE_CLAUDE_CODE_PROMPT": "true",
                "OPENCODE_SERVER_PASSWORD": "production-test-password",
                "OPENCODE_WORKSPACE": str(workspace),
                "OPENCODE_RUNTIME_UID": str(uid),
                "OPENCODE_RUNTIME_GID": str(gid),
                "OPENCODE_CONFIG_DIR": str(workspace.parent / "opencode-config"),
                "OPENCODE_DATA_DIR": str(workspace.parent / "opencode-data"),
                "OPEN_PILOT_REPO_PATH": "/workspace",
                "OPEN_PILOT_WORKTREE_ROOT": "/workspace/.open-pilot-worktrees",
                "SWITCHYARD_WEB_TRANSPORT": "tailscale",
                "SWITCHYARD_WEB_BIND_HOST": "100.65.164.66",
                "SWITCHYARD_WEB_TRUSTED_PROXY_CIDR": "100.77.128.80/32",
                "SWITCHYARD_DASHBOARD_CIDR": "127.0.0.1/32",
                "SWITCHYARD_PUBLIC_URL": "https://switchyard.example.test",
                "SWITCHYARD_REVERSE_PROXY_AUTH": "true",
                "SWITCHYARD_BIND_HOST": "127.0.0.1",
                "SWITCHYARD_MODEL_BIND_HOST": "100.65.164.66",
                "POSTGRES_PORT": "5432",
                "API_PORT": "18080",
                "LLAMA_LINE_PORT": "11434",
                "WEB_PORT": "8081",
                "SWITCHYARD_CONTROL_API_TOKEN": "never-print-this-secret",
            }
            path = Path(directory) / ".env"
            path.write_text(
                "\n".join(f"{name}={value}" for name, value in values.items()),
                encoding="utf-8",
            )
            result = subprocess.run(
                [sys.executable, str(REPO_ROOT / "scripts/validate_production_config.py"), "--env-file", str(path)],
                capture_output=True,
                text=True,
                env={},
                check=False,
            )

        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertNotIn("never-print-this-secret", result.stdout + result.stderr)

    def test_env_file_errors_do_not_output_values(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / ".env"
            path.write_text('SECRET="never-print-this-secret" trailing\n', encoding="utf-8")
            result = subprocess.run(
                [sys.executable, str(REPO_ROOT / "scripts/validate_production_config.py"), "--env-file", str(path)],
                capture_output=True,
                text=True,
                env={},
                check=False,
            )

        self.assertEqual(result.returncode, 1)
        self.assertNotIn("never-print-this-secret", result.stdout + result.stderr)

    def test_git_config_rejection_does_not_output_credential_value(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            workspace, uid, gid = make_owned_workspace(directory)
            marker = "credential-marker-must-not-print"
            subprocess.run(
                ["git", "-C", str(workspace), "config", "--local", "http.extraheader", marker],
                check=True,
                capture_output=True,
                text=True,
            )
            values = {
                "OPENCODE_SECURITY_MODE": "strict",
                "OPENCODE_DB": ":memory:",
                "OPENCODE_DISABLE_PROJECT_CONFIG": "true",
                "OPENCODE_DISABLE_CLAUDE_CODE_PROMPT": "true",
                "OPENCODE_SERVER_PASSWORD": "production-test-password",
                "OPENCODE_WORKSPACE": str(workspace),
                "OPENCODE_RUNTIME_UID": str(uid),
                "OPENCODE_RUNTIME_GID": str(gid),
                "OPENCODE_CONFIG_DIR": str(workspace.parent / "opencode-config"),
                "OPENCODE_DATA_DIR": str(workspace.parent / "opencode-data"),
                "OPEN_PILOT_REPO_PATH": "/workspace",
                "OPEN_PILOT_WORKTREE_ROOT": "/workspace/.open-pilot-worktrees",
            }
            path = Path(directory) / ".env"
            path.write_text("\n".join(f"{name}={value}" for name, value in values.items()), encoding="utf-8")
            result = subprocess.run(
                [sys.executable, str(REPO_ROOT / "scripts/validate_production_config.py"), "--env-file", str(path)],
                capture_output=True,
                text=True,
                env={},
                check=False,
            )
        self.assertEqual(result.returncode, 1)
        self.assertNotIn(marker, result.stdout + result.stderr)

    def test_service_validates_env_file_before_start_and_reload(self) -> None:
        unit = (REPO_ROOT / "deploy/switchyard.service").read_text(encoding="utf-8")
        validator = "scripts/validate_production_config.py --env-file __REPO_ROOT__/.env"
        self.assertIn(f"ExecStartPre=/usr/bin/python3 __REPO_ROOT__/{validator}", unit)
        self.assertLess(unit.index("ExecStartPre="), unit.index("ExecStart=/usr/bin/docker"))
        self.assertLess(unit.index("ExecReload=/usr/bin/python3"), unit.index("ExecReload=/usr/bin/docker"))
        self.assertEqual(unit.count(validator), 2)

    def test_smoke_rejects_different_public_url(self) -> None:
        result = subprocess.run(
            ["bash", str(REPO_ROOT / "scripts/smoke-production-contract.sh")],
            capture_output=True,
            text=True,
            env={
                "PATH": os.environ["PATH"],
                "SWITCHYARD_PUBLIC_URL": "https://switchyard.example.test",
                "SWITCHYARD_PRODUCTION_PUBLIC_URL": "https://other.example.test",
            },
            check=False,
        )

        self.assertEqual(result.returncode, 2)
        self.assertIn("must match SWITCHYARD_PUBLIC_URL", result.stderr)

    def test_smoke_requires_validated_public_url(self) -> None:
        result = subprocess.run(
            ["bash", str(REPO_ROOT / "scripts/smoke-production-contract.sh")],
            capture_output=True,
            text=True,
            env={"PATH": os.environ["PATH"]},
            check=False,
        )

        self.assertEqual(result.returncode, 2)
        self.assertIn("Set validated SWITCHYARD_PUBLIC_URL", result.stderr)

    def test_smoke_derives_control_and_model_urls_from_listener_environment(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            directory_path = Path(directory)
            curl_path = directory_path / "curl"
            log_path = directory_path / "urls.log"
            curl_path.write_text(
                """#!/usr/bin/env python3
import os
from pathlib import Path
import sys

args = sys.argv[1:]
url = [arg for arg in args if arg.startswith(("http://", "https://"))][-1]
with Path(os.environ["CURL_LOG"]).open("a", encoding="utf-8") as log:
    log.write(url + "\\n")
if "--dump-header" in args:
    header_path = Path(args[args.index("--dump-header") + 1])
    header_path.write_text("HTTP/1.1 302 Found\\r\\nLocation: https://auth.example.test/\\r\\n\\r\\n", encoding="utf-8")
elif "--write-out" in args:
    print("404", end="")
elif url.endswith("/readyz"):
    print('{"ready":true}')
elif url.endswith("/api/operator-safety"):
    print('{"warnings":[]}')
elif url.endswith("/api/operator-summary"):
    print('{"schema_version":"1.0"}')
elif url.endswith("/health"):
    print('{"ok":true}')
elif url.endswith("/api/tags"):
    print('{"models":[{"name":"test"}]}')
elif url.endswith("/api/generate"):
    print('{"response":"ok"}')
elif url.endswith("/v1/chat/completions"):
    print('{"choices":[{"message":{"content":"ok"}}]}')
else:
    print('{}')
""",
                encoding="utf-8",
            )
            curl_path.chmod(0o755)
            result = subprocess.run(
                ["bash", str(REPO_ROOT / "scripts/smoke-production-contract.sh")],
                capture_output=True,
                text=True,
                env={
                    "PATH": f"{directory}{os.pathsep}{os.environ['PATH']}",
                    "CURL_LOG": str(log_path),
                    "SWITCHYARD_PUBLIC_URL": "https://switchyard.example.test",
                    "SWITCHYARD_CONTROL_API_TOKEN": "smoke-token",
                    "SWITCHYARD_BIND_HOST": "127.0.0.2",
                    "API_PORT": "28081",
                    "SWITCHYARD_MODEL_BIND_HOST": "192.0.2.10",
                    "LLAMA_LINE_PORT": "22434",
                    "SWITCHYARD_SMOKE_MODEL": "test",
                },
                check=False,
            )

            urls = log_path.read_text(encoding="utf-8").splitlines()

        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("http://127.0.0.2:28081/readyz", urls)
        self.assertIn("http://192.0.2.10:22434/health", urls)


if __name__ == "__main__":
    unittest.main()
