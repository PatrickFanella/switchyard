#!/usr/bin/env python3
"""Refresh generated web identity assets from the canonical product design."""

import hashlib
import json
from pathlib import Path


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def main():
    source = Path(__file__).resolve().parent
    root = source.parents[2]
    destination = root / "web/public/brand"
    destination.mkdir(parents=True, exist_ok=True)
    tokens = source.parent / "tokens.json"
    generated_tokens = root / "web/brand-tokens.json"
    generated_tokens.write_bytes(tokens.read_bytes())
    mark = source / "mark.svg"
    (destination / "mark.svg").write_bytes(mark.read_bytes())
    accent = json.loads(tokens.read_text())["color"]["dark"]["action"]
    (destination / "favicon.svg").write_text(mark.read_text().replace('fill="currentColor"', f'fill="{accent}"'))
    manifest_path = destination / "manifest.json"
    manifest = json.loads(manifest_path.read_text())
    generated = {"mark.svg", "favicon.svg"}
    manifest["assets"] = [entry for entry in manifest["assets"] if entry["file"] not in generated]
    for name in sorted(generated):
        manifest["assets"].append({"file": name, "source": "docs/design/identity/mark.svg", "sourceSha256": digest(mark), "sha256": digest(destination / name)})
    manifest["tokens"] = {"source": "docs/design/tokens.json", "sourceSha256": digest(tokens), "generated": "web/brand-tokens.json", "sha256": digest(generated_tokens)}
    manifest_path.write_text(json.dumps(manifest, indent=2) + "\n")


if __name__ == "__main__":
    main()
