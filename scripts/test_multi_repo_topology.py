#!/usr/bin/env python3
"""Live two-repository mount/network isolation smoke for generated topology."""

from __future__ import annotations

import json
import os
from pathlib import Path
import subprocess
import tempfile
import time

from open_pilot_registry import (
    canonical_json, registry_fingerprint, render_compose_file_set,
    render_compose_override, render_runtime_manifest, write_atomic,
)


ROOT = Path(__file__).resolve().parents[1]
DOCKER = ROOT / "deploy/docker-local"


def run(*arguments: str, env: dict[str, str] | None = None, check: bool = True) -> subprocess.CompletedProcess[str]:
    return subprocess.run(arguments, cwd=ROOT, env=env, text=True, capture_output=True, check=check)


def main() -> int:
    uid = int(os.environ.get("OPENCODE_RUNTIME_UID", "1000"))
    gid = int(os.environ.get("OPENCODE_RUNTIME_GID", "1000"))
    project = f"switchyard-multi-repo-smoke-{os.getpid()}"
    with tempfile.TemporaryDirectory(prefix="switchyard-multi-repo-") as temporary:
        root = Path(temporary)
        repositories = []
        for identifier in ("alpha", "beta"):
            base = root / identifier
            source = base / "source"
            source.mkdir(parents=True, mode=0o700)
            run("git", "init", "--initial-branch=main", str(source))
            run("git", "-C", str(source), "remote", "add", "origin", f"https://git.example.test/owner/{identifier}.git")
            run("git", "-C", str(source), "config", "user.name", "Switchyard Smoke")
            run("git", "-C", str(source), "config", "user.email", "switchyard-smoke@example.test")
            (source / "README.md").write_text(f"{identifier}\n", encoding="utf-8")
            run("git", "-C", str(source), "add", "README.md")
            run("git", "-C", str(source), "commit", "-m", "initial smoke commit")
            worktrees = source / ".open-pilot-worktrees"
            config = base / "config"
            data = base / "data"
            for directory in (worktrees, config, data):
                directory.mkdir(mode=0o700)
            marker = worktrees / ".switchyard-managed"
            marker.write_text("switchyard-managed-worktree-root\n", encoding="utf-8")
            marker.chmod(0o600)
            proof_worktree = worktrees / "proof"
            run("git", "-C", str(source), "worktree", "add", "-b", "open-pilot/smoke-proof", str(proof_worktree), "main")
            password = base / "password"
            password.write_text(identifier * 24 + "\n", encoding="utf-8")
            password.chmod(0o600)
            if os.geteuid() == 0:
                for path in (source, source / ".git", worktrees, marker, config, data):
                    os.chown(path, uid, gid)
                for directory, names, files in os.walk(proof_worktree):
                    os.chown(directory, uid, gid)
                    for name in names + files:
                        os.chown(Path(directory) / name, uid, gid)
            repositories.append({
                "id": identifier, "slug": f"owner/{identifier}", "aliases": [],
                "executor_generation": "smoke-1", "source_path": str(source),
                "worktree_root": str(worktrees), "config_path": str(config),
                "data_path": str(data), "password_file": str(password),
            })
        registry = {"version": 1, "generation": "smoke-1", "repositories": repositories}
        fingerprint = registry_fingerprint(registry)
        manifest = root / "runtime.json"
        override = root / "compose.json"
        file_set = root / "file-set.json"
        write_atomic(manifest, canonical_json(render_runtime_manifest(registry, fingerprint)))
        write_atomic(override, canonical_json(render_compose_override(registry, manifest, fingerprint)))
        write_atomic(file_set, canonical_json(render_compose_file_set(ROOT / "docker-compose.yml", override, manifest, fingerprint)))
        env_file = root / "compose.env"
        env_file.write_text(
            f"POSTGRES_PASSWORD=multi-repo-smoke-password\nOPENCODE_SERVER_PASSWORD=legacy-smoke-password\nOPENCODE_RUNTIME_UID={uid}\nOPENCODE_RUNTIME_GID={gid}\n",
            encoding="utf-8",
        )
        command = [str(DOCKER), "compose", "--env-file", str(env_file), "-p", project, "-f", str(ROOT / "docker-compose.yml"), "-f", str(override)]
        services = [f"open-pilot-{kind}-{identifier}" for identifier in ("alpha", "beta") for kind in ("opencode", "verifier")]
        try:
            run(*command, "up", "-d", "--build", *services)
            deadline = time.monotonic() + 180
            while time.monotonic() < deadline:
                states = []
                for service in services:
                    result = run(*command, "ps", "--format", "json", service, check=False)
                    states.append('"Health":"healthy"' in result.stdout or ('"State":"running"' in result.stdout and "verifier" in service))
                if all(states):
                    break
                time.sleep(2)
            else:
                raise RuntimeError("generated repository services did not become healthy")
            for identifier, sibling in (("alpha", "beta"), ("beta", "alpha")):
                for kind in ("opencode", "verifier"):
                    container = run(*command, "ps", "-q", f"open-pilot-{kind}-{identifier}").stdout.strip()
                    if not container:
                        raise RuntimeError("generated service has no container")
                    inspection = json.loads(run(str(DOCKER), "inspect", container).stdout)[0]
                    sources = {mount["Source"] for mount in inspection["Mounts"] if mount["Type"] == "bind"}
                    if any(f"/{sibling}/" in source for source in sources):
                        raise RuntimeError("generated service can see a sibling repository mount")
                    networks = set(inspection["NetworkSettings"]["Networks"])
                    if any(sibling in network for network in networks):
                        raise RuntimeError("generated service joined a sibling repository network")
                opencode_container = run(*command, "ps", "-q", f"open-pilot-opencode-{identifier}").stdout.strip()
                own = f"/open-pilot/repositories/{identifier}/worktrees/proof/{identifier}.txt"
                sibling_path = f"/open-pilot/repositories/{sibling}/worktrees/proof/{sibling}.txt"
                run(str(DOCKER), "exec", "--user", f"{uid}:{gid}", opencode_container, "sh", "-eu", "-c", f"printf '%s\\n' {identifier} > {own}; test ! -e {sibling_path}")
                verifier_container = run(*command, "ps", "-q", f"open-pilot-verifier-{identifier}").stdout.strip()
                denied = run(str(DOCKER), "exec", verifier_container, "sh", "-c", f"touch /open-pilot/repositories/{identifier}/worktrees/proof/verifier-must-not-write", check=False)
                if denied.returncode == 0:
                    raise RuntimeError(f"verifier {identifier} unexpectedly wrote to its read-only worktree")

            alpha = run(*command, "ps", "-q", "open-pilot-opencode-alpha").stdout.strip()
            beta = run(*command, "ps", "-q", "open-pilot-opencode-beta").stdout.strip()
            run(str(DOCKER), "stop", beta)
            run(str(DOCKER), "exec", "--user", f"{uid}:{gid}", alpha, "sh", "-eu", "-c", "printf 'sibling-down\\n' >> /open-pilot/repositories/alpha/worktrees/proof/alpha.txt")
            run(str(DOCKER), "start", beta)
            run(str(DOCKER), "restart", alpha)
            deadline = time.monotonic() + 60
            while time.monotonic() < deadline:
                alpha_state = run(*command, "ps", "--format", "json", "open-pilot-opencode-alpha", check=False)
                beta_state = run(*command, "ps", "--format", "json", "open-pilot-opencode-beta", check=False)
                if '"Health":"healthy"' in alpha_state.stdout and '"Health":"healthy"' in beta_state.stdout:
                    break
                time.sleep(2)
            else:
                raise RuntimeError("repository sidecars did not recover after stop/restart proof")
            alpha = run(*command, "ps", "-q", "open-pilot-opencode-alpha").stdout.strip()
            run(str(DOCKER), "exec", "--user", f"{uid}:{gid}", alpha, "sh", "-eu", "-c", "grep -q sibling-down /open-pilot/repositories/alpha/worktrees/proof/alpha.txt")
            print("Two-repository generated topology, write, outage, and restart isolation smoke passed")
            return 0
        finally:
            run(*command, "down", "-v", "--remove-orphans", check=False)


if __name__ == "__main__":
    raise SystemExit(main())
