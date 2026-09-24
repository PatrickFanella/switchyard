#!/usr/bin/env python3
"""Build an offline verifier image from reviewed, committed Go/Node dependency inputs.

The supported toolchains, bound dependency inputs, and commands come from
backend/cmd/verifier/profiles.json, which the verifier binary embeds. Network access
happens only during this operator-owned image build, never inside verification.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import re
import subprocess
import sys
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
PROFILES_PATH = ROOT / "backend/cmd/verifier/profiles.json"
PROFILE = "go1.26.6-node24-pnpm10.33.0-v2"


class PreparationError(ValueError):
    pass


def load_profile(name: str = PROFILE, path: Path = PROFILES_PATH) -> dict:
    contract = json.loads(path.read_text())
    if contract.get("version") != 1:
        raise PreparationError("unsupported verification profile contract")
    profile = contract.get("profiles", {}).get(name)
    if profile is None:
        raise PreparationError("unknown verification profile: " + name)
    return profile


def committed_inputs(source: Path, revision: str, profile: dict) -> tuple[dict[str, str | None], dict[str, bytes]]:
    """Hash every bound input at the exact revision; absent inputs are recorded as None."""
    files: dict[str, str | None] = {}
    contents: dict[str, bytes] = {}
    for name in profile["inputs"]:
        result = subprocess.run(["git", "-C", str(source), "show", revision + ":" + name], capture_output=True)
        if result.returncode:
            files[name] = None
            continue
        if name not in profile["prepared_inputs"]:
            raise PreparationError("unsupported dependency configuration: " + name)
        files[name] = hashlib.sha256(result.stdout).hexdigest()
        contents[name] = result.stdout
    package = contents.get("web/package.json")
    if package is not None:
        pinned = "pnpm@" + profile["toolchains"]["pnpm"]
        manager = json.loads(package).get("packageManager", pinned)
        if manager.split("+")[0] != pinned:
            raise PreparationError("this preparation profile requires " + pinned)
    return files, contents


def build_manifest(revision: str, files: dict[str, str | None], verifier_base: str, profile_name: str = PROFILE, profile: dict | None = None) -> dict:
    profile = profile or load_profile(profile_name)
    return {
        "version": profile["manifest_version"],
        "base_sha": revision,
        "files": files,
        "commands": profile["commands"],
        "verifier_base": verifier_base,
        "profile": profile_name,
    }


def resolve_revision(source: Path, revision: str) -> str:
    resolved = subprocess.check_output(["git", "-C", str(source), "rev-parse", "--verify", revision + "^{commit}"], text=True).strip()
    if not re.fullmatch(r"[0-9a-f]{40}", resolved):
        raise PreparationError("invalid source revision")
    return resolved


def main() -> int:
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--source", type=Path, required=True)
    p.add_argument("--revision", required=True)
    p.add_argument("--verifier-image", required=True)
    p.add_argument("--tag", required=True)
    p.add_argument("--record", type=Path, required=True)
    args = p.parse_args()
    docker = str(ROOT / "deploy/docker-local")
    try:
        profile = load_profile()
        revision = resolve_revision(args.source, args.revision)
        files, contents = committed_inputs(args.source, revision, profile)
    except PreparationError as error:
        p.error(str(error))
    base = json.loads(subprocess.check_output([docker, "image", "inspect", args.verifier_image]))[0]["Id"]
    # Pin the local build input by content and use an unambiguous local tag for FROM.
    base_tag = "switchyard-verifier-base:" + base.split(":")[1]
    subprocess.run([docker, "tag", base, base_tag], check=True)
    with tempfile.TemporaryDirectory(prefix="switchyard-verifier-build-") as directory:
        root = Path(directory)
        (root / "inputs").mkdir()
        for name, data in contents.items():
            target = root / "inputs" / name
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(data)
        manifest = build_manifest(revision, files, base, PROFILE, profile)
        (root / "manifest.json").write_text(json.dumps(manifest, sort_keys=True))
        (root / "Dockerfile").write_bytes((ROOT / "services/verifier/Dockerfile").read_bytes())
        subprocess.run([docker, "build", "--build-arg", "VERIFIER_BASE=" + base_tag, "-t", args.tag, str(root)], check=True)
        image = json.loads(subprocess.check_output([docker, "image", "inspect", args.tag]))[0]["Id"]
        args.record.write_text(json.dumps({"image": image, "tag": args.tag, "manifest": manifest}, indent=2) + "\n")
        print("Prepared immutable verifier image: " + image)
    return 0


if __name__ == "__main__":
    sys.exit(main())
