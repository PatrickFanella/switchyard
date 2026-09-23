#!/usr/bin/env python3
"""Build an offline verifier image from reviewed, committed Go/Node dependency inputs."""
import argparse
import hashlib
import json
import re
import subprocess
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
INPUTS = ("backend/go.mod", "backend/go.sum", "web/package.json", "web/pnpm-lock.yaml", "package.json", "pnpm-lock.yaml", "pnpm-workspace.yaml", ".npmrc", "web/.npmrc", "go.work", "go.work.sum")

def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--source", type=Path, required=True)
    p.add_argument("--revision", required=True)
    p.add_argument("--verifier-image", required=True)
    p.add_argument("--tag", required=True)
    p.add_argument("--record", type=Path, required=True)
    args = p.parse_args()
    docker = str(ROOT / "deploy/docker-local")
    revision = subprocess.check_output(["git", "-C", str(args.source), "rev-parse", "--verify", args.revision + "^{commit}"], text=True).strip()
    if not re.fullmatch(r"[0-9a-f]{40}", revision):
        p.error("invalid source revision")
    base = json.loads(subprocess.check_output([docker, "image", "inspect", args.verifier_image]))[0]["Id"]
    # Pin the local build input by content and use an unambiguous local tag for FROM.
    base_tag = "switchyard-verifier-base:" + base.split(":")[1]
    subprocess.run([docker, "tag", base, base_tag], check=True)
    with tempfile.TemporaryDirectory(prefix="switchyard-verifier-build-") as directory:
        root = Path(directory)
        (root / "inputs").mkdir()
        files = {}
        for name in INPUTS:
            result = subprocess.run(["git", "-C", str(args.source), "show", revision + ":" + name], capture_output=True)
            if result.returncode:
                files[name] = None
                continue
            if name not in INPUTS[:4]:
                p.error("unsupported dependency configuration: " + name)
            files[name] = hashlib.sha256(result.stdout).hexdigest()
            target = root / "inputs" / name
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(result.stdout)
        package = root / "inputs/web/package.json"
        if package.exists():
            manager = json.loads(package.read_text()).get("packageManager", "pnpm@10.33.0")
            if manager.split("+")[0] != "pnpm@10.33.0":
                p.error("this preparation profile requires pnpm@10.33.0")
        manifest = {"version": 2, "base_sha": revision, "files": files, "commands": [["make", "verify"]], "verifier_base": base, "profile": "go1.26.6-node24-pnpm10.33.0-v2"}
        (root / "manifest.json").write_text(json.dumps(manifest, sort_keys=True))
        (root / "Dockerfile").write_bytes((ROOT / "services/verifier/Dockerfile").read_bytes())
        subprocess.run([docker, "build", "--build-arg", "VERIFIER_BASE=" + base_tag, "-t", args.tag, str(root)], check=True)
        image = json.loads(subprocess.check_output([docker, "image", "inspect", args.tag]))[0]["Id"]
        args.record.write_text(json.dumps({"image": image, "tag": args.tag, "manifest": manifest}, indent=2) + "\n")
        print("Prepared immutable verifier image: " + image)

if __name__ == "__main__":
    main()
