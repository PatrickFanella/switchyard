#!/usr/bin/env python3
"""Validate and deterministically render the Open Pilot repository registry."""

from __future__ import annotations

import hashlib
import json
import os
import re
import stat
import subprocess
from pathlib import Path
from typing import Any, Mapping

from validate_production_config import (
    MANAGED_MARKER,
    MANAGED_MARKER_CONTENT,
    sanitized_git_env,
    unsafe_local_git_config,
)


MAX_DOCUMENT_BYTES = 256 << 10
MAX_REPOSITORIES = 32
ID = re.compile(r"[a-z0-9][a-z0-9-]{0,31}\Z")
SLUG = re.compile(r"[A-Za-z0-9][A-Za-z0-9._-]{0,99}/[A-Za-z0-9][A-Za-z0-9._-]{0,99}\Z")
OPERATOR_KEYS = {"version", "generation", "repositories"}
REPOSITORY_KEYS = {
    "id", "slug", "aliases", "executor_generation", "source_path",
    "worktree_root", "config_path", "data_path", "password_file",
}


def _read_json(path: Path) -> Any:
    info = path.lstat()
    if stat.S_ISLNK(info.st_mode) or not stat.S_ISREG(info.st_mode):
        raise ValueError("operator registry must be a regular non-symlink file")
    if info.st_size > MAX_DOCUMENT_BYTES:
        raise ValueError("operator registry exceeds the size limit")
    try:
        return json.loads(path.read_text(encoding="utf-8"), object_pairs_hook=_unique_object)
    except (OSError, UnicodeError, json.JSONDecodeError) as error:
        raise ValueError("operator registry is not strict UTF-8 JSON") from error


def _unique_object(pairs: list[tuple[str, Any]]) -> dict[str, Any]:
    value: dict[str, Any] = {}
    for key, item in pairs:
        if key in value:
            raise ValueError("operator registry contains a duplicate JSON key")
        value[key] = item
    return value


def _absolute_path(value: Any, label: str) -> Path:
    if not isinstance(value, str) or not value or len(value) > 4096 or any(ord(character) < 32 for character in value):
        raise ValueError(f"{label} must be a bounded absolute path")
    path = Path(value)
    if not path.is_absolute():
        raise ValueError(f"{label} must be a bounded absolute path")
    if path != Path(os.path.abspath(path)):
        raise ValueError(f"{label} must be lexically canonical")
    return path


def load_operator_registry(path: Path) -> dict[str, Any]:
    document = _read_json(path)
    if not isinstance(document, dict) or set(document) != OPERATOR_KEYS:
        raise ValueError("operator registry has unknown or missing fields")
    if document["version"] != 1 or not isinstance(document["generation"], str) or not ID.fullmatch(document["generation"]):
        raise ValueError("operator registry version or generation is invalid")
    repositories = document["repositories"]
    if not isinstance(repositories, list) or not 1 <= len(repositories) <= MAX_REPOSITORIES:
        raise ValueError("operator registry repository count is invalid")
    ids: set[str] = set()
    names: set[str] = set()
    for index, repository in enumerate(repositories):
        if not isinstance(repository, dict) or set(repository) != REPOSITORY_KEYS:
            raise ValueError(f"operator repository {index} has unknown or missing fields")
        identifier = repository["id"]
        generation = repository["executor_generation"]
        slug = repository["slug"]
        aliases = repository["aliases"]
        if not isinstance(identifier, str) or not ID.fullmatch(identifier) or identifier in ids:
            raise ValueError(f"operator repository {index} ID is invalid or duplicated")
        if not isinstance(generation, str) or not ID.fullmatch(generation):
            raise ValueError(f"operator repository {index} executor generation is invalid")
        if not isinstance(slug, str) or not SLUG.fullmatch(slug):
            raise ValueError(f"operator repository {index} slug is invalid")
        if not isinstance(aliases, list) or len(aliases) > 32 or any(not isinstance(alias, str) or not SLUG.fullmatch(alias) for alias in aliases):
            raise ValueError(f"operator repository {index} aliases are invalid")
        folded = [slug.casefold(), *(alias.casefold() for alias in aliases)]
        if len(folded) != len(set(folded)) or any(name in names for name in folded):
            raise ValueError(f"operator repository {index} slug or alias is ambiguous")
        ids.add(identifier)
        names.update(folded)
        for key in ("source_path", "worktree_root", "config_path", "data_path", "password_file"):
            _absolute_path(repository[key], f"operator repository {index} {key}")
    return document


def _require_owned_directory(path: Path, label: str, uid: int, gid: int) -> os.stat_result:
    info = path.lstat()
    if stat.S_ISLNK(info.st_mode) or not stat.S_ISDIR(info.st_mode):
        raise ValueError(f"{label} must be a non-symlink directory")
    if path.resolve(strict=True) != path:
        raise ValueError(f"{label} must contain no symlink components")
    if info.st_uid != uid or info.st_gid != gid:
        raise ValueError(f"{label} ownership does not match configured runtime IDs")
    mode = stat.S_IMODE(info.st_mode)
    if mode & stat.S_IRWXU != stat.S_IRWXU or mode & (stat.S_IWGRP | stat.S_IWOTH):
        raise ValueError(f"{label} permissions are unsafe")
    return info


def validate_registry_filesystem(
    registry: Mapping[str, Any], uid: int, gid: int, gitea_base_url: str, *, allow_unprepared_worktrees: bool = False
) -> list[dict[str, Any]]:
    """Validate every entry without mutation and return prepared path metadata."""
    prepared: list[dict[str, Any]] = []
    claimed_paths: list[tuple[str, Path]] = []
    expected_host = gitea_base_url.rstrip("/")
    for repository in registry["repositories"]:
        identifier = repository["id"]
        paths = {key: _absolute_path(repository[key], f"repository {identifier} {key}") for key in (
            "source_path", "worktree_root", "config_path", "data_path", "password_file"
        )}
        source = paths["source_path"]
        _require_owned_directory(source, f"repository {identifier} source", uid, gid)
        git_dir = source / ".git"
        _require_owned_directory(git_dir, f"repository {identifier} .git", uid, gid)
        top = subprocess.run(
            ["git", "-c", f"safe.directory={source}", "-C", str(source), "rev-parse", "--show-toplevel"],
            env=sanitized_git_env(), capture_output=True, text=True, check=False,
        )
        if top.returncode != 0 or Path(top.stdout.strip()).resolve() != source:
            raise ValueError(f"repository {identifier} source is not an exact Git top-level")
        if unsafe_local_git_config(source):
            raise ValueError(f"repository {identifier} source has unsafe local Git configuration")
        origin = subprocess.run(
            ["git", "-c", f"safe.directory={source}", "-C", str(source), "remote", "get-url", "origin"],
            env=sanitized_git_env(), capture_output=True, text=True, check=False,
        )
        accepted = f"{expected_host}/{repository['slug']}.git"
        accepted_without_suffix = f"{expected_host}/{repository['slug']}"
        if origin.returncode != 0 or origin.stdout.strip() not in {accepted, accepted_without_suffix}:
            raise ValueError(f"repository {identifier} origin does not exactly match its registered slug")
        for key in ("config_path", "data_path"):
            _require_owned_directory(paths[key], f"repository {identifier} {key}", uid, gid)
        worktree_prepared = paths["worktree_root"].exists() or paths["worktree_root"].is_symlink()
        if worktree_prepared:
            _require_owned_directory(paths["worktree_root"], f"repository {identifier} worktree_root", uid, gid)
        elif allow_unprepared_worktrees:
            parent = paths["worktree_root"].parent
            _require_owned_directory(parent, f"repository {identifier} worktree_root parent", uid, gid)
        else:
            raise ValueError(f"repository {identifier} worktree_root must be a non-symlink directory")
        marker = paths["worktree_root"] / MANAGED_MARKER
        marker_prepared = marker.exists() or marker.is_symlink()
        if marker_prepared:
            marker_info = marker.lstat()
            if (stat.S_ISLNK(marker_info.st_mode) or not stat.S_ISREG(marker_info.st_mode)
                    or marker.read_text(encoding="utf-8") != MANAGED_MARKER_CONTENT
                    or marker_info.st_uid != uid or marker_info.st_gid != gid
                    or stat.S_IMODE(marker_info.st_mode) & 0o077):
                raise ValueError(f"repository {identifier} managed worktree marker is invalid")
        elif not allow_unprepared_worktrees:
            raise ValueError(f"repository {identifier} managed worktree marker is invalid")
        elif worktree_prepared and any(paths["worktree_root"].iterdir()):
            raise ValueError(f"repository {identifier} unprepared worktree root must be empty")
        password_info = paths["password_file"].lstat()
        if (stat.S_ISLNK(password_info.st_mode) or not stat.S_ISREG(password_info.st_mode)
                or paths["password_file"].resolve(strict=True) != paths["password_file"]
                or password_info.st_uid not in {0, uid} or password_info.st_gid not in {0, gid}
                or stat.S_IMODE(password_info.st_mode) & 0o077
                or password_info.st_size < 32 or password_info.st_size > 4096):
            raise ValueError(f"repository {identifier} password file is unsafe")
        entry_claims: list[tuple[str, Path]] = []
        for key, path in paths.items():
            for prior_label, prior in claimed_paths:
                if path == prior or path.is_relative_to(prior) or prior.is_relative_to(path):
                    raise ValueError(f"repository {identifier} {key} overlaps {prior_label}")
            for prior_key, prior in entry_claims:
                allowed_worktree_nesting = {key, prior_key} == {"source_path", "worktree_root"} and (
                    path.is_relative_to(prior) or prior.is_relative_to(path)
                )
                if not allowed_worktree_nesting and (path == prior or path.is_relative_to(prior) or prior.is_relative_to(path)):
                    raise ValueError(f"repository {identifier} {key} overlaps its {prior_key}")
            entry_claims.append((key, path))
        claimed_paths.extend((f"repository {identifier} {key}", path) for key, path in entry_claims)
        prepared.append({"repository": repository, "paths": paths, "worktree_prepared": worktree_prepared, "marker_prepared": marker_prepared})
    return prepared


def prepare_registry_filesystem(registry: Mapping[str, Any], uid: int, gid: int, gitea_base_url: str) -> list[dict[str, Any]]:
    """Validate the complete registry, then create only missing managed roots/markers."""
    prepared = validate_registry_filesystem(
        registry, uid, gid, gitea_base_url, allow_unprepared_worktrees=True
    )
    created: list[Path] = []
    try:
        for item in prepared:
            root = item["paths"]["worktree_root"]
            if not item["worktree_prepared"]:
                os.mkdir(root, mode=0o700)
                created.append(root)
            marker = root / MANAGED_MARKER
            if not item["marker_prepared"]:
                descriptor = os.open(marker, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
                with os.fdopen(descriptor, "w", encoding="utf-8") as output:
                    output.write(MANAGED_MARKER_CONTENT)
                    output.flush()
                    os.fsync(output.fileno())
                created.append(marker)
        return validate_registry_filesystem(registry, uid, gid, gitea_base_url)
    except (OSError, ValueError):
        for path in reversed(created):
            try:
                if path.is_dir():
                    path.rmdir()
                else:
                    path.unlink()
            except OSError:
                pass
        raise


def registry_fingerprint(registry: Mapping[str, Any]) -> str:
    encoded = json.dumps(registry, sort_keys=True, separators=(",", ":"), ensure_ascii=True).encode("ascii")
    return hashlib.sha256(encoded).hexdigest()


def render_runtime_manifest(registry: Mapping[str, Any], fingerprint: str) -> dict[str, Any]:
    repositories: list[dict[str, Any]] = []
    for repository in sorted(registry["repositories"], key=lambda item: item["id"]):
        identifier = repository["id"]
        root = f"/open-pilot/repositories/{identifier}"
        repositories.append({
            "id": identifier,
            "slug": repository["slug"],
            "aliases": sorted(repository["aliases"], key=str.casefold),
            "executor_generation": repository["executor_generation"],
            "source_path": f"{root}/source",
            "worktree_root": f"{root}/worktrees",
            "opencode_endpoint": f"http://open-pilot-opencode-{identifier}:4096",
            "verifier_endpoint": f"http://open-pilot-verifier-{identifier}:8090",
            "password_file": f"/run/secrets/open-pilot-{identifier}-password",
        })
    return {
        "version": 1,
        "registry_generation": registry["generation"],
        "registry_fingerprint": fingerprint,
        "repositories": repositories,
    }


def _opencode_service(repository: Mapping[str, Any]) -> dict[str, Any]:
    identifier = repository["id"]
    root = f"/open-pilot/repositories/{identifier}"
    return {
        "build": {"context": "./services/opencode", "args": {"SWITCHYARD_SOURCE_REVISION": "${SWITCHYARD_SOURCE_REVISION:-unknown}"}},
        "restart": "unless-stopped", "user": "0:0", "read_only": True,
        "security_opt": ["no-new-privileges:true"], "cap_drop": ["ALL"],
        "cap_add": ["CHOWN", "SETGID", "SETUID"],
        "tmpfs": ["/tmp:rw,noexec,nosuid,size=256m", "/run:rw,noexec,nosuid,size=16m"],
        "environment": {
            "HOME": "/home/opencode", "XDG_CONFIG_HOME": "/home/opencode/.config",
            "XDG_DATA_HOME": "/home/opencode/.local/share", "XDG_CACHE_HOME": "/home/opencode/.cache",
            "XDG_STATE_HOME": "/home/opencode/.local/state", "OPENCODE_SERVER_HOSTNAME": "0.0.0.0",
            "OPENCODE_SERVER_PORT": "4096", "OPENCODE_SERVER_USERNAME": "${OPENCODE_SERVER_USERNAME:-switchyard}",
            "OPENCODE_SERVER_PASSWORD_FILE": f"/run/secrets/open-pilot-{identifier}-password",
            "OPENAI_BASE_URL": "${OPEN_PILOT_OPENAI_BASE_URL:-http://api:11434/v1}",
            "OPENAI_API_KEY": "${OPEN_PILOT_MODEL_API_KEY:-switchyard-internal}",
            "OPENCODE_SECURITY_MODE": "${OPENCODE_SECURITY_MODE:-strict}", "OPENCODE_DB": ":memory:",
            "OPENCODE_DISABLE_PROJECT_CONFIG": "true", "OPENCODE_DISABLE_CLAUDE_CODE_PROMPT": "true",
            "OPENCODE_RUNTIME_UID": "${OPENCODE_RUNTIME_UID:-1000}", "OPENCODE_RUNTIME_GID": "${OPENCODE_RUNTIME_GID:-1000}",
            "OPEN_PILOT_WORKTREE_ROOT": f"{root}/worktrees", "GIT_OPTIONAL_LOCKS": "0", "GIT_TERMINAL_PROMPT": "0",
            "GIT_CONFIG_COUNT": "2", "GIT_CONFIG_KEY_0": "safe.directory", "GIT_CONFIG_VALUE_0": f"{root}/worktrees",
            "GIT_CONFIG_KEY_1": "safe.directory", "GIT_CONFIG_VALUE_1": f"{root}/worktrees/*",
        },
        "volumes": [
            {"type": "bind", "source": repository["source_path"] + "/.git", "target": f"{root}/source/.git", "read_only": True, "bind": {"create_host_path": False}},
            {"type": "bind", "source": repository["worktree_root"], "target": f"{root}/worktrees", "bind": {"create_host_path": False}},
            {"type": "bind", "source": repository["config_path"], "target": "/home/opencode/.config/opencode", "bind": {"create_host_path": False}},
            {"type": "bind", "source": repository["data_path"], "target": "/home/opencode/.local/share/opencode", "bind": {"create_host_path": False}},
            {"type": "bind", "source": repository["password_file"], "target": f"/run/secrets/open-pilot-{identifier}-password", "read_only": True, "bind": {"create_host_path": False}},
            f"open-pilot-state-{identifier}:/home/opencode/.local/state",
            f"open-pilot-cache-{identifier}:/home/opencode/.cache",
        ],
        "networks": [f"open-pilot-api-{identifier}"],
        "healthcheck": {"test": ["CMD", "/usr/local/bin/switchyard-opencode-health"], "interval": "10s", "timeout": "5s", "retries": 12},
    }


def _verifier_service(repository: Mapping[str, Any]) -> dict[str, Any]:
    identifier = repository["id"]
    root = f"/open-pilot/repositories/{identifier}"
    return {
        "build": {"context": "./backend", "target": "verifier"}, "restart": "unless-stopped",
        "user": "${OPENCODE_RUNTIME_UID:-1000}:${OPENCODE_RUNTIME_GID:-1000}", "command": ["/app/verifier"],
        "environment": {"OPEN_PILOT_WORKTREE_ROOT": f"{root}/worktrees", "OPEN_PILOT_GIT_COMMON_DIR": f"{root}/source/.git", "GIT_OPTIONAL_LOCKS": "0", "GIT_TERMINAL_PROMPT": "0", "TMPDIR": "/cache", "SWITCHYARD_VERIFIER_CONCURRENCY": "1"},
        "pids_limit": 128, "mem_limit": "2g", "cpus": 2, "read_only": True,
        "security_opt": ["no-new-privileges:true"], "cap_drop": ["ALL"],
        "tmpfs": ["/tmp:rw,exec,nosuid,size=1g,mode=1777", "/cache:rw,exec,nosuid,size=512m,mode=1777"],
        "volumes": [
            {"type": "bind", "source": repository["source_path"] + "/.git", "target": f"{root}/source/.git", "read_only": True, "bind": {"create_host_path": False}},
            {"type": "bind", "source": repository["worktree_root"], "target": f"{root}/worktrees", "read_only": True, "bind": {"create_host_path": False}},
        ],
        "networks": [f"open-pilot-verification-{identifier}"],
        "healthcheck": {"test": ["CMD", "wget", "-qO-", "http://127.0.0.1:8090/healthz"], "interval": "10s", "timeout": "5s", "retries": 5},
    }


def render_compose_override(registry: Mapping[str, Any], manifest_path: Path, fingerprint: str) -> dict[str, Any]:
    services: dict[str, Any] = {}
    networks: dict[str, Any] = {}
    volumes: dict[str, Any] = {}
    api_networks: dict[str, Any] = {"default": None, "verification": None}
    api_volumes: list[dict[str, Any]] = [{"type": "bind", "source": str(manifest_path), "target": "/etc/switchyard/open-pilot-runtime-manifest.json", "read_only": True, "bind": {"create_host_path": False}}]
    depends: dict[str, Any] = {"postgres": {"condition": "service_healthy"}}
    for repository in sorted(registry["repositories"], key=lambda item: item["id"]):
        identifier = repository["id"]
        opencode = f"open-pilot-opencode-{identifier}"
        verifier = f"open-pilot-verifier-{identifier}"
        services[opencode] = _opencode_service(repository)
        services[verifier] = _verifier_service(repository)
        volumes[f"open-pilot-state-{identifier}"] = {}
        volumes[f"open-pilot-cache-{identifier}"] = {}
        depends[opencode] = {"condition": "service_healthy"}
        depends[verifier] = {"condition": "service_healthy"}
        for suffix in ("api", "verification"):
            name = f"open-pilot-{suffix}-{identifier}"
            networks[name] = {"internal": True}
            api_networks[name] = None
        root = f"/open-pilot/repositories/{identifier}"
        api_volumes.extend([
            {"type": "bind", "source": repository["source_path"], "target": f"{root}/source", "bind": {"create_host_path": False}},
            {"type": "bind", "source": repository["worktree_root"], "target": f"{root}/worktrees", "bind": {"create_host_path": False}},
            {"type": "bind", "source": repository["password_file"], "target": f"/run/secrets/open-pilot-{identifier}-password", "read_only": True, "bind": {"create_host_path": False}},
        ])
    services["api"] = {
        "environment": {
            "SWITCHYARD_OPEN_PILOT_RUNTIME_MANIFEST": "/etc/switchyard/open-pilot-runtime-manifest.json",
            "SWITCHYARD_OPEN_PILOT_REGISTRY_FINGERPRINT": fingerprint,
            "SWITCHYARD_OPEN_PILOT_ACTIVATION_MODE": "${SWITCHYARD_OPEN_PILOT_ACTIVATION_MODE:-disabled}",
        },
        "depends_on": depends, "volumes": api_volumes, "networks": api_networks,
    }
    return {"name": "switchyard", "services": services, "networks": networks, "volumes": volumes}


def render_compose_file_set(base_compose: Path, compose_override: Path, runtime_manifest: Path, fingerprint: str) -> dict[str, Any]:
    return {
        "version": 1,
        "registry_fingerprint": fingerprint,
        "base_compose": str(base_compose.resolve()),
        "compose_override": str(compose_override.resolve()),
        "runtime_manifest": str(runtime_manifest.resolve()),
    }


def canonical_json(document: Mapping[str, Any]) -> bytes:
    return (json.dumps(document, sort_keys=True, indent=2, ensure_ascii=True) + "\n").encode("ascii")


def write_atomic(path: Path, payload: bytes, mode: int = 0o600) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_name(f".{path.name}.tmp-{os.getpid()}")
    descriptor = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, mode)
    try:
        with os.fdopen(descriptor, "wb") as output:
            output.write(payload)
            output.flush()
            os.fsync(output.fileno())
        os.replace(temporary, path)
    finally:
        try:
            temporary.unlink()
        except FileNotFoundError:
            pass
