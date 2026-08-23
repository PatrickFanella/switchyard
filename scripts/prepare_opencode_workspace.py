#!/usr/bin/env python3
"""Create the local OpenCode workspace without changing host ownership."""

from __future__ import annotations

import argparse
import os
import re
import stat
import subprocess
import sys
from pathlib import Path

from validate_production_config import (
    MANAGED_MARKER,
    MANAGED_MARKER_CONTENT,
    MAX_RUNTIME_ID,
    WORKTREE_DIRECTORY,
    SYSTEM_DIRECTORY_ROOTS,
    parse_dotenv,
    sanitized_git_env,
    unsafe_local_git_config,
)


REPO_ROOT = Path(__file__).resolve().parent.parent
NUMERIC_ID = re.compile(r"[0-9]+\Z")


def runtime_id(env: dict[str, str], name: str, default: str) -> int:
    raw = env.get(name, "").strip() or default
    if not NUMERIC_ID.fullmatch(raw):
        raise ValueError(f"{name} must be a numeric non-root ID")
    value = int(raw)
    if not 1 <= value <= MAX_RUNTIME_ID:
        raise ValueError(f"{name} must be between 1 and {MAX_RUNTIME_ID}")
    return value


def prepare_bind_directory(env: dict[str, str], name: str, default: str, uid: int, gid: int) -> Path:
    configured = env.get(name, "").strip() or default
    directory = Path(configured)
    if not directory.is_absolute():
        lexical = Path(os.path.abspath(REPO_ROOT / directory))
        repo_root = REPO_ROOT.resolve()
        if not lexical.is_relative_to(repo_root):
            raise ValueError(f"relative {name} must remain inside this repository")
        current = repo_root
        for component in lexical.relative_to(repo_root).parts:
            current /= component
            if current.is_symlink():
                raise ValueError(f"{name} must be canonical and contain no symlink components")
        lexical.mkdir(parents=True, mode=0o700, exist_ok=True)
    elif not directory.exists():
        raise ValueError(f"absolute {name} must already exist")
    else:
        lexical = Path(os.path.abspath(directory))
    if lexical.is_symlink() or not lexical.is_dir():
        raise ValueError(f"{name} must be a non-symlink directory")
    canonical = lexical.resolve(strict=True)
    if canonical != lexical:
        raise ValueError(f"{name} must be canonical and contain no symlink components")
    repo_root = REPO_ROOT.resolve()
    if canonical in SYSTEM_DIRECTORY_ROOTS or repo_root.is_relative_to(canonical):
        raise ValueError(f"{name} must not be a root, system, or repository-containing directory")
    require_runtime_directory(canonical, name, uid, gid)
    return canonical


def require_runtime_directory(path: Path, label: str, uid: int, gid: int) -> os.stat_result:
    info = path.lstat()
    if info.st_uid != uid or info.st_gid != gid:
        raise ValueError(f"{label} ownership does not match configured runtime IDs")
    mode = stat.S_IMODE(info.st_mode)
    if mode & stat.S_IRWXU != stat.S_IRWXU:
        raise ValueError(f"{label} owner must have read, write, and traverse access")
    if mode & (stat.S_IWGRP | stat.S_IWOTH):
        raise ValueError(f"{label} must not be group- or world-writable")
    return info


def prepare(env: dict[str, str]) -> Path:
    uid = runtime_id(env, "OPENCODE_RUNTIME_UID", "1000")
    gid = runtime_id(env, "OPENCODE_RUNTIME_GID", "1000")
    configured = env.get("OPENCODE_WORKSPACE", "").strip() or "./tmp/opencode-workspace"
    workspace = Path(configured)
    default_development_workspace = not workspace.is_absolute() and workspace.as_posix() in {
        "tmp/opencode-workspace",
        "./tmp/opencode-workspace",
    }
    if not workspace.is_absolute():
        workspace = (REPO_ROOT / workspace).resolve(strict=False)
        if not workspace.is_relative_to(REPO_ROOT.resolve()):
            raise ValueError("relative OPENCODE_WORKSPACE must remain inside this repository")
        workspace.mkdir(parents=True, mode=0o750, exist_ok=True)
    elif not workspace.exists():
        raise ValueError("absolute OPENCODE_WORKSPACE must already exist")
    if workspace.is_symlink():
        raise ValueError("OPENCODE_WORKSPACE must not be a symlink")
    canonical_workspace = workspace.resolve(strict=True)
    if workspace.is_absolute() and canonical_workspace != workspace:
        raise ValueError("absolute OPENCODE_WORKSPACE must be canonical and contain no symlink components")
    workspace = canonical_workspace
    if not workspace.is_dir():
        raise ValueError("OPENCODE_WORKSPACE must be a directory")

    repo_root = REPO_ROOT.resolve()
    if workspace == repo_root or repo_root.is_relative_to(workspace):
        raise ValueError("OPENCODE_WORKSPACE must not contain this repository")

    owner = workspace.stat()
    if owner.st_uid != uid or owner.st_gid != gid:
        raise ValueError(
            "OPENCODE_WORKSPACE ownership does not match OPENCODE_RUNTIME_UID/OPENCODE_RUNTIME_GID; refusing to change ownership"
        )
    if stat.S_IMODE(owner.st_mode) & (stat.S_IWGRP | stat.S_IWOTH):
        raise ValueError("OPENCODE_WORKSPACE must not be group- or world-writable")
    if stat.S_IMODE(owner.st_mode) & stat.S_IRWXU != stat.S_IRWXU:
        raise ValueError("OPENCODE_WORKSPACE owner must have read, write, and traverse access")

    git_metadata = workspace / ".git"
    if not git_metadata.exists() and not git_metadata.is_symlink():
        if not default_development_workspace:
            raise ValueError("OPENCODE_WORKSPACE must already be a valid Git top-level")
        if any(workspace.iterdir()):
            raise ValueError("default development OPENCODE_WORKSPACE must be empty before git init")
        result = subprocess.run(
            ["git", "-c", f"safe.directory={workspace}", "init", "--initial-branch=main", str(workspace)],
            env=sanitized_git_env(),
            capture_output=True,
            text=True,
            check=False,
        )
        if result.returncode != 0:
            raise ValueError("unable to initialize default development Git workspace")
    metadata_stat = git_metadata.lstat()
    if stat.S_ISLNK(metadata_stat.st_mode) or not stat.S_ISDIR(metadata_stat.st_mode):
        raise ValueError("OPENCODE_WORKSPACE .git must be a non-symlink directory")
    require_runtime_directory(git_metadata, "OPENCODE_WORKSPACE .git", uid, gid)
    top_level = subprocess.run(
        ["git", "-c", f"safe.directory={workspace}", "-C", str(workspace), "rev-parse", "--show-toplevel"],
        env=sanitized_git_env(),
        capture_output=True,
        text=True,
        check=False,
    )
    if top_level.returncode != 0 or Path(top_level.stdout.strip()).resolve() != workspace:
        raise ValueError("OPENCODE_WORKSPACE must already be a valid Git top-level")
    if unsafe_local_git_config(workspace):
        raise ValueError("OPENCODE_WORKSPACE local Git redirect or credential configuration is not allowed")

    worktree_root = workspace / WORKTREE_DIRECTORY
    worktree_root.mkdir(mode=0o700, exist_ok=True)
    root_stat = worktree_root.lstat()
    if stat.S_ISLNK(root_stat.st_mode) or not stat.S_ISDIR(root_stat.st_mode):
        raise ValueError("managed worktree root must be a non-symlink directory")
    require_runtime_directory(worktree_root, "managed worktree root", uid, gid)

    marker = worktree_root / MANAGED_MARKER
    try:
        descriptor = os.open(
            marker,
            os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW,
            0o600,
        )
    except FileExistsError:
        descriptor = None
    if descriptor is not None:
        with os.fdopen(descriptor, "w", encoding="utf-8") as marker_file:
            marker_file.write(MANAGED_MARKER_CONTENT)
    marker_stat = marker.lstat()
    if (
        stat.S_ISLNK(marker_stat.st_mode)
        or not stat.S_ISREG(marker_stat.st_mode)
        or marker.read_text(encoding="utf-8") != MANAGED_MARKER_CONTENT
    ):
        raise ValueError("managed worktree root marker is invalid")
    if marker_stat.st_uid != uid or marker_stat.st_gid != gid:
        raise ValueError("managed marker ownership does not match configured runtime IDs")
    if stat.S_IMODE(marker_stat.st_mode) & 0o077:
        raise ValueError("managed marker must be private")
    config_dir = prepare_bind_directory(env, "OPENCODE_CONFIG_DIR", "./.opencode", uid, gid)
    data_dir = prepare_bind_directory(env, "OPENCODE_DATA_DIR", "./opencode-data", uid, gid)
    for name, directory in (("OPENCODE_CONFIG_DIR", config_dir), ("OPENCODE_DATA_DIR", data_dir)):
        if directory.is_relative_to(workspace) or workspace.is_relative_to(directory):
            raise ValueError(f"{name} must be dedicated and separate from OPENCODE_WORKSPACE")
    if config_dir.is_relative_to(data_dir) or data_dir.is_relative_to(config_dir):
        raise ValueError("OPENCODE_CONFIG_DIR and OPENCODE_DATA_DIR must be separate dedicated directories")
    return workspace


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--env-file", type=Path, default=REPO_ROOT / ".env")
    args = parser.parse_args()

    env: dict[str, str] = {}
    if args.env_file.exists():
        try:
            env.update(parse_dotenv(args.env_file))
        except (OSError, UnicodeError, ValueError):
            print("workspace init: unable to read env file safely", file=sys.stderr)
            return 1
    env.update(os.environ)
    try:
        workspace = prepare(env)
    except (OSError, RuntimeError, ValueError) as error:
        print(f"workspace init: {error}", file=sys.stderr)
        return 1
    print(f"OpenCode workspace ready: {workspace}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
