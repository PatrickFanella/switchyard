#!/usr/bin/env python3
"""Render fingerprint-matched Open Pilot runtime and Compose documents."""

from __future__ import annotations

import argparse
import os
import sys
from pathlib import Path

from open_pilot_registry import (
    canonical_json,
    load_operator_registry,
    registry_fingerprint,
    render_compose_override,
    render_compose_file_set,
    render_runtime_manifest,
    validate_registry_filesystem,
    write_atomic,
)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--registry", type=Path, required=True)
    parser.add_argument("--runtime-manifest", type=Path, required=True)
    parser.add_argument("--compose-override", type=Path, required=True)
    parser.add_argument("--compose-file-set", type=Path, required=True)
    parser.add_argument("--base-compose", type=Path, default=Path(__file__).resolve().parents[1] / "docker-compose.yml")
    parser.add_argument("--runtime-uid", type=int, default=int(os.environ.get("OPENCODE_RUNTIME_UID", "1000")))
    parser.add_argument("--runtime-gid", type=int, default=int(os.environ.get("OPENCODE_RUNTIME_GID", "1000")))
    parser.add_argument("--gitea-base-url", default=os.environ.get("GITEA_BASE_URL", ""))
    parser.add_argument("--check", action="store_true")
    args = parser.parse_args()
    try:
        registry = load_operator_registry(args.registry)
        validate_registry_filesystem(registry, args.runtime_uid, args.runtime_gid, args.gitea_base_url)
        fingerprint = registry_fingerprint(registry)
        manifest = canonical_json(render_runtime_manifest(registry, fingerprint))
        override = canonical_json(render_compose_override(registry, args.runtime_manifest.resolve(), fingerprint))
        file_set = canonical_json(render_compose_file_set(args.base_compose, args.compose_override, args.runtime_manifest, fingerprint))
        if args.check:
            if (args.runtime_manifest.read_bytes() != manifest or args.compose_override.read_bytes() != override
                    or args.compose_file_set.read_bytes() != file_set):
                raise ValueError("generated Open Pilot topology does not match the operator registry")
        else:
            write_atomic(args.runtime_manifest, manifest)
            write_atomic(args.compose_override, override)
            write_atomic(args.compose_file_set, file_set)
    except (OSError, ValueError) as error:
        print(f"open pilot topology: {error}", file=sys.stderr)
        return 1
    print(f"Open Pilot topology ready: generation={registry['generation']} repositories={len(registry['repositories'])} fingerprint={fingerprint}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
