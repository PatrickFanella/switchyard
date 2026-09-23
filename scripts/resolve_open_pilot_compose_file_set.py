#!/usr/bin/env python3
"""Validate and print the exact generated Compose file set for the host wrapper."""

from __future__ import annotations

import argparse
import json
import os
import stat
import sys
from pathlib import Path


KEYS = {"version", "registry_fingerprint", "base_compose", "compose_override", "runtime_manifest"}


def strict_object(pairs: list[tuple[str, object]]) -> dict[str, object]:
    value: dict[str, object] = {}
    for key, item in pairs:
        if key in value:
            raise ValueError("duplicate JSON key")
        value[key] = item
    return value


def regular(path: Path, label: str) -> Path:
    if not path.is_absolute() or path != Path(os.path.abspath(path)):
        raise ValueError(f"{label} must be an absolute canonical path")
    info = path.lstat()
    if stat.S_ISLNK(info.st_mode) or not stat.S_ISREG(info.st_mode) or path.resolve(strict=True) != path:
        raise ValueError(f"{label} must be a regular non-symlink file")
    return path


def load(path: Path) -> dict[str, object]:
    info = path.lstat()
    if stat.S_ISLNK(info.st_mode) or not stat.S_ISREG(info.st_mode) or info.st_size > 1 << 20:
        raise ValueError("Compose file-set metadata is unsafe")
    try:
        document = json.loads(path.read_text(encoding="utf-8"), object_pairs_hook=strict_object)
    except (OSError, UnicodeError, json.JSONDecodeError) as error:
        raise ValueError("Compose file-set metadata is invalid") from error
    if not isinstance(document, dict) or set(document) != KEYS or document["version"] != 1:
        raise ValueError("Compose file-set metadata contract is invalid")
    fingerprint = document["registry_fingerprint"]
    if not isinstance(fingerprint, str) or len(fingerprint) != 64 or any(character not in "0123456789abcdef" for character in fingerprint):
        raise ValueError("Compose file-set fingerprint is invalid")
    return document


def resolve(path: Path, expected_base: Path) -> tuple[Path, Path]:
    document = load(path)
    base = regular(Path(str(document["base_compose"])), "base Compose file")
    override = regular(Path(str(document["compose_override"])), "generated Compose override")
    manifest_path = regular(Path(str(document["runtime_manifest"])), "runtime manifest")
    if base != expected_base.resolve(strict=True):
        raise ValueError("Compose file set does not use the expected repository base")
    try:
        manifest = json.loads(manifest_path.read_text(encoding="utf-8"), object_pairs_hook=strict_object)
        compose = json.loads(override.read_text(encoding="utf-8"), object_pairs_hook=strict_object)
    except (OSError, UnicodeError, json.JSONDecodeError) as error:
        raise ValueError("generated topology document is invalid") from error
    fingerprint = document["registry_fingerprint"]
    if manifest.get("registry_fingerprint") != fingerprint:
        raise ValueError("runtime manifest fingerprint does not match the Compose file set")
    api = compose.get("services", {}).get("api", {}) if isinstance(compose, dict) else {}
    environment = api.get("environment", {}) if isinstance(api, dict) else {}
    if environment.get("SWITCHYARD_OPEN_PILOT_REGISTRY_FINGERPRINT") != fingerprint:
        raise ValueError("Compose override fingerprint does not match the file set")
    manifest_mounts = [item for item in api.get("volumes", []) if isinstance(item, dict) and item.get("target") == "/etc/switchyard/open-pilot-runtime-manifest.json"]
    if len(manifest_mounts) != 1 or Path(str(manifest_mounts[0].get("source"))) != manifest_path:
        raise ValueError("Compose override runtime-manifest mount does not match the file set")
    return base, override


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--file-set", type=Path, required=True)
    parser.add_argument("--expected-base", type=Path, required=True)
    args = parser.parse_args()
    try:
        base, override = resolve(args.file_set, args.expected_base)
    except (OSError, ValueError) as error:
        print(f"open pilot Compose file set: {error}", file=sys.stderr)
        return 1
    print(base)
    print(override)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
