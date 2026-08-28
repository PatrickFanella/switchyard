#!/usr/bin/env python3
"""Prepare every registered Open Pilot workspace after a complete preflight."""

from __future__ import annotations

import argparse
import os
import sys
from pathlib import Path

from open_pilot_registry import load_operator_registry, prepare_registry_filesystem


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--registry", type=Path, required=True)
    parser.add_argument("--runtime-uid", type=int, default=int(os.environ.get("OPENCODE_RUNTIME_UID", "1000")))
    parser.add_argument("--runtime-gid", type=int, default=int(os.environ.get("OPENCODE_RUNTIME_GID", "1000")))
    parser.add_argument("--gitea-base-url", default=os.environ.get("GITEA_BASE_URL", ""))
    args = parser.parse_args()
    try:
        registry = load_operator_registry(args.registry)
        prepared = prepare_registry_filesystem(registry, args.runtime_uid, args.runtime_gid, args.gitea_base_url)
    except (OSError, ValueError) as error:
        print(f"open pilot registry preparation: {error}", file=sys.stderr)
        return 1
    print(f"Open Pilot registry prepared: generation={registry['generation']} repositories={len(prepared)}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
