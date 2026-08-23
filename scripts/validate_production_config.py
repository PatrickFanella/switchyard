#!/usr/bin/env python3
"""Validate the host-facing production transport contract from the environment."""

from __future__ import annotations

import argparse
import ipaddress
import os
import re
import stat
import subprocess
import sys
from collections.abc import Mapping
from pathlib import Path
from urllib.parse import urlsplit


TAILSCALE_CGNAT = ipaddress.IPv4Network("100.64.0.0/10")
DOTENV_NAME = re.compile(r"[A-Za-z_][A-Za-z0-9_]*\Z")
NUMERIC_ID = re.compile(r"[0-9]+\Z")
MAX_RUNTIME_ID = 2_147_483_647
REPO_ROOT = Path(__file__).resolve().parent.parent
WORKTREE_DIRECTORY = ".open-pilot-worktrees"
MANAGED_MARKER = ".switchyard-managed"
MANAGED_MARKER_CONTENT = "switchyard-managed-worktree-root\n"
SYSTEM_DIRECTORY_ROOTS = {
    Path("/"), Path("/dev"), Path("/etc"), Path("/home"), Path("/proc"),
    Path("/root"), Path("/run"), Path("/sys"), Path("/tmp"), Path("/usr"), Path("/var"),
}


def sanitized_git_env() -> dict[str, str]:
    env = {name: value for name, value in os.environ.items() if not name.startswith("GIT_")}
    env.update(
        {
            "GIT_CONFIG_NOSYSTEM": "1",
            "GIT_CONFIG_GLOBAL": "/dev/null",
            "GIT_OPTIONAL_LOCKS": "0",
            "GIT_TERMINAL_PROMPT": "0",
            "GIT_ASKPASS": "",
            "GIT_PAGER": "cat",
        }
    )
    return env


def unsafe_local_git_config_key(key: str) -> bool:
    key = key.strip().lower()
    if key in {
        "core.sshcommand", "core.askpass", "core.hookspath", "core.fsmonitor",
        "core.gitproxy", "core.worktree", "core.attributesfile", "core.pager", "diff.external",
        "interactive.difffilter", "http.extraheader", "http.proxy",
        "http.curloptresolve", "http.followredirects", "http.sslverify",
        "http.sslcert", "http.sslkey", "http.sslcainfo", "http.sslcapath",
        "http.sslbackend",
    }:
        return True
    if key.startswith(("credential.", "url.", "include.", "includeif.", "protocol.", "gpg.")):
        return True
    if key.startswith("http.") and key.endswith(
        (".extraheader", ".proxy", ".curloptresolve", ".sslverify", ".sslcert",
         ".sslkey", ".sslcainfo", ".sslcapath", ".sslbackend", ".cookiefile")
    ):
        return True
    if key.startswith("remote.") and key.endswith(
        (".pushurl", ".proxy", ".receivepack", ".uploadpack", ".vcs")
    ):
        return True
    if key.startswith("filter."):
        return True
    if key.startswith("submodule.") and key.endswith(".update"):
        return True
    if key.startswith("merge.") and key.endswith(".driver"):
        return True
    return key.startswith("diff.") and key.endswith((".command", ".textconv"))


def unsafe_local_git_config(workspace: Path) -> bool:
    result = subprocess.run(
        [
            "git",
            "-c",
            f"safe.directory={workspace}",
            "-C",
            str(workspace),
            "config",
            "--local",
            "--name-only",
            "--get-regexp",
            ".*",
        ],
        env=sanitized_git_env(),
        capture_output=True,
        text=True,
        check=False,
    )
    if result.returncode not in {0, 1}:
        return True
    for raw_key in result.stdout.splitlines():
        if unsafe_local_git_config_key(raw_key):
            return True
    return False


def validate_runtime_directory(
    path: Path,
    label: str,
    runtime_uid: int | None,
    runtime_gid: int | None,
    errors: list[str],
) -> os.stat_result | None:
    try:
        info = path.lstat()
    except OSError:
        return None
    if runtime_uid is not None and info.st_uid != runtime_uid:
        errors.append(f"{label} owner must match OPENCODE_RUNTIME_UID")
    if runtime_gid is not None and info.st_gid != runtime_gid:
        errors.append(f"{label} group must match OPENCODE_RUNTIME_GID")
    mode = stat.S_IMODE(info.st_mode)
    if mode & stat.S_IRWXU != stat.S_IRWXU:
        errors.append(f"{label} owner must have read, write, and traverse access")
    if mode & (stat.S_IWGRP | stat.S_IWOTH):
        errors.append(f"{label} must not be group- or world-writable")
    return info


def validate_dedicated_host_directory(
    env: Mapping[str, str],
    name: str,
    runtime_uid: int | None,
    runtime_gid: int | None,
    errors: list[str],
) -> Path | None:
    raw = _value(env, name)
    configured = Path(raw)
    if not raw or not configured.is_absolute():
        errors.append(f"{name} must be an absolute existing dedicated directory")
        return None
    try:
        if configured.is_symlink():
            raise OSError
        canonical = configured.resolve(strict=True)
        if canonical != configured or not canonical.is_dir():
            raise OSError
    except (OSError, RuntimeError):
        errors.append(f"{name} must be an absolute existing dedicated directory")
        return None
    repo_root = REPO_ROOT.resolve()
    if canonical in SYSTEM_DIRECTORY_ROOTS or repo_root.is_relative_to(canonical) or canonical.is_relative_to(repo_root):
        errors.append(f"{name} must not be a root, system, or repository directory")
    validate_runtime_directory(canonical, name, runtime_uid, runtime_gid, errors)
    return canonical


def _value(env: Mapping[str, str], name: str, default: str = "") -> str:
    return env.get(name, default).strip()


def _port(env: Mapping[str, str], name: str, default: str, errors: list[str]) -> int | None:
    raw = _value(env, name, default)
    try:
        port = int(raw)
    except ValueError:
        errors.append(f"{name} must be an integer port")
        return None
    if not 1 <= port <= 65535:
        errors.append(f"{name} must be between 1 and 65535")
        return None
    return port


def _runtime_id(env: Mapping[str, str], name: str, errors: list[str]) -> int | None:
    raw = _value(env, name)
    if not NUMERIC_ID.fullmatch(raw):
        errors.append(f"{name} must be a numeric non-root ID")
        return None
    value = int(raw)
    if not 1 <= value <= MAX_RUNTIME_ID:
        errors.append(f"{name} must be between 1 and {MAX_RUNTIME_ID}")
        return None
    return value


def parse_dotenv(path: Path) -> dict[str, str]:
    env: dict[str, str] = {}
    for line_number, raw_line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
        line = raw_line.strip()
        if not line or line.startswith("#"):
            continue
        if line.startswith("export "):
            line = line[7:].lstrip()
        if "=" not in line:
            raise ValueError(f"invalid env file syntax at line {line_number}")
        name, raw_value = line.split("=", 1)
        name = name.strip()
        if not DOTENV_NAME.fullmatch(name):
            raise ValueError(f"invalid env file name at line {line_number}")

        value = raw_value.strip()
        if value.startswith(("'", '"')):
            quote = value[0]
            closing = value.find(quote, 1)
            trailing = value[closing + 1 :].strip()
            if closing < 0 or (trailing and not trailing.startswith("#")):
                raise ValueError(f"invalid quoted env value at line {line_number}")
            value = value[1:closing]
        else:
            value = re.split(r"\s+#", value, maxsplit=1)[0].rstrip()
        env[name] = value
    return env


def validate(env: Mapping[str, str]) -> list[str]:
    errors: list[str] = []

    if _value(env, "OPENCODE_SECURITY_MODE").lower() != "strict":
        errors.append("OPENCODE_SECURITY_MODE must be strict")
    if _value(env, "OPENCODE_DISABLE_PROJECT_CONFIG").lower() != "true":
        errors.append("OPENCODE_DISABLE_PROJECT_CONFIG must be true")
    if _value(env, "OPENCODE_DB") != ":memory:":
        errors.append("OPENCODE_DB must be :memory:")
    if _value(env, "OPENCODE_DISABLE_CLAUDE_CODE_PROMPT").lower() != "true":
        errors.append("OPENCODE_DISABLE_CLAUDE_CODE_PROMPT must be true")
    password = _value(env, "OPENCODE_SERVER_PASSWORD")
    if not password or password in {
        "opencode-change-me",
        "switchyard-local-opencode-password",
    }:
        errors.append("OPENCODE_SERVER_PASSWORD must be set to a non-default value")

    runtime_uid = _runtime_id(env, "OPENCODE_RUNTIME_UID", errors)
    runtime_gid = _runtime_id(env, "OPENCODE_RUNTIME_GID", errors)

    config_dir = validate_dedicated_host_directory(env, "OPENCODE_CONFIG_DIR", runtime_uid, runtime_gid, errors)
    data_dir = validate_dedicated_host_directory(env, "OPENCODE_DATA_DIR", runtime_uid, runtime_gid, errors)
    workspace_raw = _value(env, "OPENCODE_WORKSPACE")
    workspace: Path | None = None
    if not workspace_raw or not Path(workspace_raw).is_absolute():
        errors.append("OPENCODE_WORKSPACE must be an absolute existing directory")
    else:
        try:
            configured_workspace = Path(workspace_raw)
            if configured_workspace.is_symlink():
                raise OSError
            workspace = configured_workspace.resolve(strict=True)
            if workspace != configured_workspace:
                raise OSError
            if not workspace.is_dir():
                raise NotADirectoryError
        except (OSError, RuntimeError):
            workspace = None
            errors.append("OPENCODE_WORKSPACE must be an absolute existing directory")

    if workspace is not None:
        repo_root = REPO_ROOT.resolve()
        if (
            workspace == repo_root
            or workspace.is_relative_to(repo_root)
            or repo_root.is_relative_to(workspace)
        ):
            errors.append("OPENCODE_WORKSPACE must be dedicated and outside this repository")
        try:
            owner = workspace.stat()
        except OSError:
            errors.append("OPENCODE_WORKSPACE ownership could not be verified")
        else:
            if runtime_uid is not None and owner.st_uid != runtime_uid:
                errors.append("OPENCODE_WORKSPACE owner must match OPENCODE_RUNTIME_UID")
            if runtime_gid is not None and owner.st_gid != runtime_gid:
                errors.append("OPENCODE_WORKSPACE group must match OPENCODE_RUNTIME_GID")
            if stat.S_IMODE(owner.st_mode) & (stat.S_IWGRP | stat.S_IWOTH):
                errors.append("OPENCODE_WORKSPACE must not be group- or world-writable")
            if stat.S_IMODE(owner.st_mode) & stat.S_IRWXU != stat.S_IRWXU:
                errors.append("OPENCODE_WORKSPACE owner must have read, write, and traverse access")

        git_metadata = workspace / ".git"
        try:
            metadata_stat = git_metadata.lstat()
            if stat.S_ISLNK(metadata_stat.st_mode) or not stat.S_ISDIR(metadata_stat.st_mode):
                raise OSError
        except OSError:
            errors.append("OPENCODE_WORKSPACE .git must be a non-symlink directory")
        else:
            metadata_info = validate_runtime_directory(
                git_metadata,
                "OPENCODE_WORKSPACE .git",
                runtime_uid,
                runtime_gid,
                errors,
            )
            if metadata_info is not None and not stat.S_ISDIR(metadata_info.st_mode):
                errors.append("OPENCODE_WORKSPACE .git must be a non-symlink directory")
            try:
                top_level = subprocess.run(
                    ["git", "-c", f"safe.directory={workspace}", "-C", str(workspace), "rev-parse", "--show-toplevel"],
                    env=sanitized_git_env(),
                    capture_output=True,
                    text=True,
                    check=False,
                )
                valid_top_level = (
                    top_level.returncode == 0
                    and Path(top_level.stdout.strip()).resolve() == workspace
                )
            except (OSError, RuntimeError):
                valid_top_level = False
            if not valid_top_level:
                errors.append("OPENCODE_WORKSPACE must be a valid Git top-level")
            else:
                try:
                    unsafe_config = unsafe_local_git_config(workspace)
                except (OSError, RuntimeError):
                    unsafe_config = True
                if unsafe_config:
                    errors.append("OPENCODE_WORKSPACE local Git redirect or credential configuration is not allowed")

        for name, directory in (("OPENCODE_CONFIG_DIR", config_dir), ("OPENCODE_DATA_DIR", data_dir)):
            if directory is not None and (directory.is_relative_to(workspace) or workspace.is_relative_to(directory)):
                errors.append(f"{name} must be dedicated and separate from OPENCODE_WORKSPACE")

        worktree_root = workspace / WORKTREE_DIRECTORY
        marker = worktree_root / MANAGED_MARKER
        try:
            root_stat = worktree_root.lstat()
            if stat.S_ISLNK(root_stat.st_mode) or not stat.S_ISDIR(root_stat.st_mode):
                raise OSError
        except OSError:
            errors.append("OPENCODE_WORKSPACE must contain a managed worktree root")
        else:
            validate_runtime_directory(
                worktree_root,
                "managed worktree root",
                runtime_uid,
                runtime_gid,
                errors,
            )
            try:
                marker_stat = marker.lstat()
                valid_marker = (
                    stat.S_ISREG(marker_stat.st_mode)
                    and not stat.S_ISLNK(marker_stat.st_mode)
                    and marker.read_text(encoding="utf-8") == MANAGED_MARKER_CONTENT
                )
            except (OSError, UnicodeError):
                valid_marker = False
                marker_stat = None
            if not valid_marker:
                errors.append("managed worktree root marker is missing or invalid")
            elif marker_stat is not None:
                if runtime_uid is not None and marker_stat.st_uid != runtime_uid:
                    errors.append("managed marker owner must match OPENCODE_RUNTIME_UID")
                if runtime_gid is not None and marker_stat.st_gid != runtime_gid:
                    errors.append("managed marker group must match OPENCODE_RUNTIME_GID")
                if stat.S_IMODE(marker_stat.st_mode) & 0o077:
                    errors.append("managed marker must be private")

    if config_dir is not None and data_dir is not None and (
        config_dir.is_relative_to(data_dir) or data_dir.is_relative_to(config_dir)
    ):
        errors.append("OPENCODE_CONFIG_DIR and OPENCODE_DATA_DIR must be separate dedicated directories")

    if _value(env, "OPEN_PILOT_REPO_PATH") != "/workspace":
        errors.append("OPEN_PILOT_REPO_PATH must be /workspace")
    if _value(env, "OPEN_PILOT_WORKTREE_ROOT") != "/workspace/.open-pilot-worktrees":
        errors.append(
            "OPEN_PILOT_WORKTREE_ROOT must be /workspace/.open-pilot-worktrees"
        )

    if _value(env, "OPEN_PILOT_ALLOW_LOCAL_GIT_REMOTE").lower() == "true":
        errors.append("OPEN_PILOT_ALLOW_LOCAL_GIT_REMOTE must not be enabled in production")
    push_allowed = _value(env, "OPEN_PILOT_GIT_PUSH_MODE").lower() == "allow" or (
        _value(env, "OPEN_PILOT_AUTONOMY_MODE").lower() == "full"
        and not _value(env, "OPEN_PILOT_GIT_PUSH_MODE")
    )
    if push_allowed:
        gitea_base_url = _value(env, "GITEA_BASE_URL")
        if not gitea_base_url:
            errors.append("GITEA_BASE_URL is required when Open Pilot Git push is allowed")
        else:
            try:
                gitea_url = urlsplit(gitea_base_url)
                gitea_url.port
                valid_gitea_url = (
                    gitea_url.scheme.lower() == "https"
                    and bool(gitea_url.hostname)
                    and gitea_url.username is None
                    and gitea_url.password is None
                    and not gitea_url.query
                    and not gitea_url.fragment
                )
            except ValueError:
                valid_gitea_url = False
            if not valid_gitea_url:
                errors.append("GITEA_BASE_URL must be an absolute sanitized HTTPS URL when Open Pilot Git push is allowed")
        if not _value(env, "GITEA_TOKEN"):
            errors.append("GITEA_TOKEN is required when Open Pilot Git push is allowed")

    transport = _value(env, "SWITCHYARD_WEB_TRANSPORT", "http").lower()
    web_bind: ipaddress.IPv4Address | None = None
    if transport not in {"http", "tailscale"}:
        errors.append("SWITCHYARD_WEB_TRANSPORT must be http or tailscale")
    elif transport != "tailscale":
        errors.append("production requires SWITCHYARD_WEB_TRANSPORT=tailscale")

    if transport == "tailscale":
        web_bind_raw = _value(env, "SWITCHYARD_WEB_BIND_HOST", "127.0.0.1")
        try:
            web_bind = ipaddress.IPv4Address(web_bind_raw)
            if web_bind not in TAILSCALE_CGNAT:
                errors.append(
                    "SWITCHYARD_WEB_BIND_HOST must be an IPv4 address within 100.64.0.0/10"
                )
        except ValueError:
            errors.append("SWITCHYARD_WEB_BIND_HOST must be an IPv4 address")

        proxy_raw = _value(env, "SWITCHYARD_WEB_TRUSTED_PROXY_CIDR")
        proxy: ipaddress.IPv4Network | None = None
        if not proxy_raw.endswith("/32"):
            errors.append("SWITCHYARD_WEB_TRUSTED_PROXY_CIDR must be an explicit /32")
        else:
            try:
                proxy = ipaddress.IPv4Network(proxy_raw, strict=True)
                if not proxy.subnet_of(TAILSCALE_CGNAT):
                    errors.append(
                        "SWITCHYARD_WEB_TRUSTED_PROXY_CIDR must be within 100.64.0.0/10"
                    )
            except ValueError:
                errors.append(
                    "SWITCHYARD_WEB_TRUSTED_PROXY_CIDR must be a valid IPv4 /32"
                )

        if web_bind is not None and proxy is not None and web_bind == proxy.network_address:
            errors.append(
                "SWITCHYARD_WEB_BIND_HOST and SWITCHYARD_WEB_TRUSTED_PROXY_CIDR must identify distinct Tailscale peers"
            )

    dashboard_raw = _value(env, "SWITCHYARD_DASHBOARD_CIDR", "127.0.0.1/32")
    try:
        dashboard = ipaddress.IPv4Network(dashboard_raw, strict=True)
        if (
            dashboard.prefixlen != 32
            or not dashboard_raw.endswith("/32")
            or not dashboard.network_address.is_loopback
        ):
            raise ValueError
    except ValueError:
        errors.append("SWITCHYARD_DASHBOARD_CIDR must be a literal loopback IPv4 /32")

    public_url = _value(env, "SWITCHYARD_PUBLIC_URL")
    try:
        parsed_url = urlsplit(public_url)
        parsed_url.port
        valid_public_url = (
            parsed_url.scheme.lower() == "https"
            and bool(parsed_url.hostname)
            and parsed_url.username is None
            and parsed_url.password is None
            and parsed_url.path in {"", "/"}
            and not parsed_url.query
            and not parsed_url.fragment
        )
    except ValueError:
        valid_public_url = False
    if not valid_public_url:
        errors.append("SWITCHYARD_PUBLIC_URL must be an absolute HTTPS URL")

    if _value(env, "SWITCHYARD_REVERSE_PROXY_AUTH", "false").lower() != "true":
        errors.append("SWITCHYARD_REVERSE_PROXY_AUTH must be true")

    control_bind = _value(env, "SWITCHYARD_BIND_HOST", "127.0.0.1")
    control_ip: ipaddress.IPv4Address | None = None
    try:
        control_ip = ipaddress.IPv4Address(control_bind)
        if not control_ip.is_loopback:
            raise ValueError
    except ValueError:
        control_ip = None
        errors.append("SWITCHYARD_BIND_HOST must be a literal loopback IPv4 address")

    model_bind = _value(env, "SWITCHYARD_MODEL_BIND_HOST", control_bind)
    model_ip: ipaddress.IPv4Address | None = None
    try:
        model_ip = ipaddress.IPv4Address(model_bind)
        if model_ip.is_unspecified:
            raise ValueError
    except ValueError:
        model_ip = None
        errors.append(
            "SWITCHYARD_MODEL_BIND_HOST must be a literal non-unspecified IPv4 address"
        )

    postgres_port = _port(env, "POSTGRES_PORT", "5432", errors)
    control_port = _port(env, "API_PORT", "18080", errors)
    model_port = _port(env, "LLAMA_LINE_PORT", "11434", errors)
    web_port = _port(env, "WEB_PORT", "8081", errors)
    listeners = (
        ("postgres", control_ip, postgres_port),
        ("control", control_ip, control_port),
        ("model", model_ip, model_port),
        ("web", web_bind, web_port),
    )
    for index, (first_name, first_bind, first_port) in enumerate(listeners):
        for second_name, second_bind, second_port in listeners[index + 1 :]:
            if (
                first_bind is not None
                and second_bind is not None
                and first_port is not None
                and first_port == second_port
                and first_bind == second_bind
            ):
                errors.append(f"{first_name} and {second_name} listeners must not collide")

    return errors


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--env-file", type=Path)
    args = parser.parse_args()

    env: dict[str, str] = {}
    if args.env_file is not None:
        try:
            env.update(parse_dotenv(args.env_file))
        except OSError:
            print("production config: unable to read env file", file=sys.stderr)
            return 1
        except UnicodeError:
            print("production config: env file must be UTF-8", file=sys.stderr)
            return 1
        except ValueError as error:
            print(f"production config: {error}", file=sys.stderr)
            return 1
    env.update(os.environ)

    errors = validate(env)
    if errors:
        for error in errors:
            print(f"production config: {error}", file=sys.stderr)
        return 1
    print("Production configuration contract passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
