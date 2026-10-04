#!/usr/bin/env python3
"""Discover Gitea repositories and prepare eligible work in a bounded host pool."""

from __future__ import annotations

import argparse
import copy
import fcntl
import json
import os
import re
import secrets
import shutil
import subprocess
import sys
import tempfile
import time
from datetime import datetime, timedelta, timezone
from pathlib import Path
from urllib.parse import urlencode, urlsplit
from urllib.request import Request, build_opener, HTTPRedirectHandler

from open_pilot_registry import (
    SLUG, canonical_json, load_operator_registry, prepare_registry_filesystem,
    registry_fingerprint, render_compose_file_set, render_compose_override,
    render_runtime_manifest, write_atomic,
)
from validate_production_config import parse_dotenv, sanitized_git_env


class NoRedirect(HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):
        raise ValueError("authenticated requests must not redirect")


class ReconciliationDeferred(ValueError):
    """Retain the catalog and retry at the next scheduled reconciliation."""


class Client:
    def __init__(self, base: str, token: str, scheme: str):
        parsed = urlsplit(base)
        if parsed.username or parsed.password or parsed.query or parsed.fragment or not token:
            raise ValueError("invalid API target or missing credential")
        if parsed.scheme != "https" and not (parsed.scheme == "http" and parsed.hostname == "127.0.0.1"):
            raise ValueError("API target must use HTTPS or host loopback")
        self.base, self.token, self.scheme = base.rstrip("/"), token, scheme
        self.opener = build_opener(NoRedirect())

    def request(self, path: str, body=None, method="GET"):
        request = Request(self.base + path, data=None if body is None else json.dumps(body).encode(),
                          headers={"Authorization": self.scheme + " " + self.token,
                                   "User-Agent": "Switchyard-Repository-Reconciler/1.0",
                                   "Content-Type": "application/json"}, method=method)
        with self.opener.open(request, timeout=30) as response:
            return json.load(response)

    def pages(self, path: str, parameters: dict, key=None):
        result = []
        for page in range(1, 201):
            payload = self.request(path + "?" + urlencode({**parameters, "limit": 50, "page": page}))
            batch = payload[key] if key else payload
            if not isinstance(batch, list):
                raise ValueError("upstream returned an invalid collection")
            result.extend(batch)
            if len(batch) < 50:
                return result
        raise ValueError("upstream collection exceeds the discovery bound")


def discover(client: Client, labels: list[str], assignee: str):
    repositories = client.pages("/api/v1/repos/search", {}, "data")
    by_slug = {}
    ids = set()
    for repository in repositories:
        slug, identifier = repository.get("full_name", ""), repository.get("id")
        if not SLUG.fullmatch(slug) or not isinstance(identifier, int) or identifier < 1:
            raise ValueError("upstream returned an invalid repository identity")
        if slug.casefold() in by_slug or identifier in ids:
            raise ValueError("upstream repository inventory is duplicated or unstable")
        by_slug[slug.casefold()] = repository
        ids.add(identifier)
    candidates = []
    if labels:
        candidates += client.pages("/api/v1/repos/issues/search", {"state": "open", "type": "issues", "labels": ",".join(labels)})
    if assignee:
        identity = client.request("/api/v1/user")
        if identity["login"].casefold() != assignee.casefold():
            raise ValueError("assignment discovery requires the configured runtime identity")
        candidates += client.pages("/api/v1/repos/issues/search", {"state": "open", "type": "issues", "assigned": "true"})
    eligible = {}
    seen = set()
    for issue in candidates:
        slug = issue.get("repository", {}).get("full_name", "").casefold()
        repository = by_slug.get(slug)
        key = (slug, issue.get("number"))
        names = {x.get("name", "").casefold() for x in issue.get("labels", [])}
        assignees = {x.get("login", "").casefold() for x in issue.get("assignees") or []}
        if key in seen or not repository or issue.get("state") != "open" or issue.get("pull_request"):
            continue
        seen.add(key)
        if (labels and all(label.casefold() in names for label in labels)) or (assignee and assignee.casefold() in assignees):
            eligible.setdefault(slug, []).append(issue["number"])
    catalog = []
    for slug, repository in sorted(by_slug.items()):
        blocker = ""
        if repository.get("archived"):
            blocker = "archived"
        elif not repository.get("has_issues"):
            blocker = "issues_disabled"
        elif repository.get("mirror"):
            blocker = "read_only_mirror"
        elif not repository.get("permissions", {}).get("push", False):
            blocker = "write_access_missing"
        elif repository.get("empty") or not repository.get("default_branch"):
            blocker = "empty_repository"
        catalog.append({"gitea_id": repository["id"], "slug": repository["full_name"],
                        "default_branch": repository.get("default_branch"),
                        "issues": sorted(set(eligible.get(slug, []))), "blocker": blocker})
    return catalog


def select_pool(registry: dict, catalog: list[dict], states: list[dict], pinned: set[str], capacity: int):
    if not 1 <= capacity <= 32 or len(pinned) > capacity:
        raise ValueError("executor capacity must contain pinned repositories and be at most 32")
    needed = {x["slug"].casefold() for x in catalog if x["issues"] and not x["blocker"]}
    protected = {x.get("repository_id") for x in states if x.get("phase") != "issue_closed"}
    keep, retired = [], []
    for repository in registry["repositories"]:
        if repository["id"] in pinned | protected or repository["slug"].casefold() in needed:
            keep.append(repository)
        else:
            retired.append(repository)
    existing = {x["slug"].casefold() for x in keep}
    pending = [x for x in catalog if x["slug"].casefold() in needed - existing]
    # Numeric Gitea IDs survive renames; derived filesystem paths never use slugs.
    pending.sort(key=lambda x: x["gitea_id"])
    available = max(0, capacity - len(keep))
    return keep, pending[:available], pending[available:], retired


def private_directory(path: Path):
    if path.is_symlink() or (path.exists() and (not path.is_dir() or path.resolve() != path)):
        raise ValueError("managed root must be a non-symlink directory")
    if not path.exists():
        path.mkdir(mode=0o700, parents=True)
    info = path.stat()
    if info.st_uid != os.getuid() or info.st_gid != os.getgid() or info.st_mode & 0o077:
        raise ValueError("managed root must be owned by the runtime user with mode 0700")


def prepare_repository(item: dict, root: Path, env: dict, generation: str, config_source: Path):
    identifier = "gitea-" + str(item["gitea_id"])
    home = root / identifier
    private_directory(home)
    if shutil.disk_usage(home).free < (4 << 30):
        raise ValueError("automatic preparation requires at least 4 GiB free")
    source = home / "source"
    origin = env["GITEA_BASE_URL"].rstrip("/") + "/" + item["slug"] + ".git"
    git_env = sanitized_git_env()
    if not source.exists():
        # The token is passed in a child environment, never argv, a Git URL, or
        # persistent local config. Do not inherit the host credential helper.
        with tempfile.TemporaryDirectory(prefix="open-pilot-askpass-") as directory:
            askpass = Path(directory) / "askpass"
            askpass.write_text('#!/bin/sh\ncase "$1" in\n*Username*) printf "%s\\n" "$OPEN_PILOT_GIT_USERNAME" ;;\n*Password*) printf "%s\\n" "$GITEA_TOKEN" ;;\nesac\n')
            askpass.chmod(0o700)
            git_env.update({"GIT_ASKPASS": str(askpass), "GITEA_TOKEN": env["GITEA_TOKEN"],
                            "OPEN_PILOT_GIT_USERNAME": env.get("OPEN_PILOT_ASSIGNEE", "open-pilot")})
            temporary = home / "source-preparing"
            if temporary.exists():
                raise ValueError("incomplete clone retained for recovery: " + identifier)
            subprocess.run(["git", "-c", "credential.helper=", "-c", "http.followRedirects=false",
                            "clone", "--no-checkout", "--origin", "origin", "--", origin, str(temporary)],
                           env=git_env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=True, timeout=600)
            # Preparation owns this dedicated checkout. Check out the remote's
            # default branch without executing hooks from host configuration.
            subprocess.run(["git", "-C", str(temporary), "checkout", "--force", item["default_branch"]],
                           env=sanitized_git_env(), stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                           check=True, timeout=60)
            temporary.rename(source)
    entry = {"id": identifier, "slug": item["slug"], "aliases": [], "executor_generation": generation,
             "source_path": str(source), "worktree_root": str(home / "worktrees"),
             "config_path": str(home / "config"), "data_path": str(home / "data"),
             "password_file": str(home / "password"), "base_branch": item["default_branch"],
             "required_checks": ["*"], "opencode_image": env["OPEN_PILOT_PREPARATION_OPENCODE_IMAGE"],
             "verifier_image": env["OPEN_PILOT_PREPARATION_VERIFIER_IMAGE"]}
    for name in ("config", "data"):
        private_directory(home / name)
    # Only reviewed first-party configuration is copied. Repository-provided
    # plugins, MCP, permissions, and host-global OpenCode config are excluded.
    for name in ("agents", "plugins", "skills"):
        destination = home / "config" / name
        if not destination.exists():
            shutil.copytree(config_source / name, destination, symlinks=False)
    if not (home / "password").exists():
        descriptor = os.open(home / "password", os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
        with os.fdopen(descriptor, "w") as output:
            output.write(secrets.token_urlsafe(48) + "\n")
    # Existing roots are validated rather than overwritten or reset.
    prepare_registry_filesystem({"repositories": [entry]}, os.getuid(), os.getgid(), env["GITEA_BASE_URL"])
    return entry


def pause(api: Client, key: str, value: bool):
    config = api.request("/api/runtime-config")
    api.request("/api/runtime-config/" + key,
                {"value": value, "expected_revision": config["revision"],
                 "reason": "automatic repository onboarding"}, "PUT")


def activate(api: Client, env: dict, registry: dict, paths: dict[str, Path], compose: Path, env_file: Path):
    config = api.request("/api/runtime-config")
    settings = {x["key"]: x for x in config["settings"]}
    if any(settings[key].get("effective") != "false" for key in ("scheduler.dispatch_paused", "workers.claim_paused")):
        raise ReconciliationDeferred("operator runtime pause is active; activation deferred")
    gate = api.request("/api/open-pilot/rollout")["gate"]
    if gate["mode"] != "allow" or len(gate["proof_targets"]) != 2:
        raise ValueError("automatic activation requires the existing proven allow gate")
    fingerprint = registry_fingerprint(registry)
    payloads = {"registry": canonical_json(registry),
                "manifest": canonical_json(render_runtime_manifest(registry, fingerprint)),
                "override": canonical_json(render_compose_override(registry, paths["manifest"], fingerprint)),
                "file_set": canonical_json(render_compose_file_set(compose.parent.parent / "docker-compose.yml",
                             paths["override"], paths["manifest"], fingerprint))}
    backup = paths["registry"].parent / ("recovery-" + datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%S%fZ"))
    private_directory(backup)
    previous = {key: path.read_bytes() for key, path in paths.items()}
    for key, payload in previous.items():
        write_atomic(backup / (key + ".json"), payload)
    paused, published = [], False
    try:
        for key in ("scheduler.dispatch_paused", "workers.claim_paused"):
            pause(api, key, True)
            paused.append(key)
        cursor = ""
        for page in range(1000):
            result = api.request("/api/runs?" + urlencode({"limit": 100, "cursor": cursor}))
            if any(x["status"] in ("running", "queued") for x in result["runs"]):
                raise ReconciliationDeferred("runtime has unfinished runs; activation deferred")
            cursor = result.get("next_cursor", "")
            if not cursor:
                break
        else:
            raise ValueError("run history exceeds the activation inspection bound")
        published = True
        for key, payload in payloads.items():
            write_atomic(paths[key], payload)
        command = [str(compose), "--env-file", str(env_file), "--file-set", str(paths["file_set"]), "--"]
        subprocess.run(command + ["config", "--quiet"], check=True, timeout=60,
                       stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        # Existing healthy sidecars are retained; retired sidecars are not
        # removed automatically. No image build or volume deletion occurs.
        subprocess.run(command + ["up", "-d", "--no-build", "api"], check=True, timeout=300,
                       stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        for attempt in range(60):
            try:
                current = api.request("/api/open-pilot/readiness")
                if current["registry_fingerprint"] == fingerprint:
                    break
            except (OSError, ValueError):
                pass
            time.sleep(1)
        else:
            raise ValueError("candidate API did not load the intended manifest")
        expires = (datetime.now(timezone.utc) + timedelta(minutes=10)).isoformat().replace("+00:00", "Z")
        api.request("/api/open-pilot/rollout", {"mode": "proof", "registry_fingerprint": fingerprint,
                    "proof_targets": gate["proof_targets"], "expires_at": expires,
                    "reason": "automatic onboarding preserves completed proof identities"}, "POST")
        api.request("/api/open-pilot/rollout", {"mode": "allow", "registry_fingerprint": fingerprint,
                    "reason": "automatic onboarding after durable completed-proof validation"}, "POST")
        if not api.request("/api/open-pilot/readiness")["ready"]:
            raise ValueError("candidate readiness failed")
        retired_ids = {x["id"] for x in json.loads(previous["registry"])["repositories"]} - {x["id"] for x in registry["repositories"]}
        if retired_ids:
            # Stop only exact retired pool owners; retain their containers,
            # volumes, private roots, worktrees, and identity for recovery.
            names = ["switchyard-open-pilot-" + kind + "-" + identifier + "-1"
                     for identifier in sorted(retired_ids) for kind in ("opencode", "verifier")]
            subprocess.run([str(compose.parent / "docker-local"), "stop", *names], check=True, timeout=120,
                           stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    except Exception:
        if published:
            for key, payload in previous.items():
                write_atomic(paths[key], payload)
            subprocess.run([str(compose), "--env-file", str(env_file), "--file-set", str(paths["file_set"]),
                            "--", "up", "-d", "--no-build", "api"], check=True, timeout=300,
                           stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            for attempt in range(60):
                try:
                    current = api.request("/api/open-pilot/readiness")
                    if current["registry_fingerprint"] == gate["registry_fingerprint"]:
                        break
                except (OSError, ValueError):
                    pass
                time.sleep(1)
            else:
                raise ValueError("rollback did not load its original manifest")
            if api.request("/api/open-pilot/rollout")["gate"]["registry_fingerprint"] != gate["registry_fingerprint"]:
                expires = (datetime.now(timezone.utc) + timedelta(minutes=10)).isoformat().replace("+00:00", "Z")
                api.request("/api/open-pilot/rollout", {"mode": "proof", "registry_fingerprint": gate["registry_fingerprint"],
                            "proof_targets": gate["proof_targets"], "expires_at": expires, "reason": "automatic onboarding rollback"}, "POST")
                api.request("/api/open-pilot/rollout", {"mode": "allow", "registry_fingerprint": gate["registry_fingerprint"],
                            "reason": "automatic onboarding rollback after durable proof validation"}, "POST")
            if not api.request("/api/open-pilot/readiness")["ready"]:
                raise ValueError("rollback readiness failed; manual recovery required")
        raise
    finally:
        for key in reversed(paused):
            pause(api, key, False)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--env-file", type=Path, required=True)
    parser.add_argument("--registry", type=Path, required=True)
    parser.add_argument("--workspace-root", type=Path, required=True)
    parser.add_argument("--catalog", type=Path, required=True)
    parser.add_argument("--control-url", default="http://127.0.0.1:28080")
    parser.add_argument("--deployment-root", type=Path, default=Path(__file__).resolve().parents[1])
    parser.add_argument("--opencode-image")
    parser.add_argument("--verifier-image")
    parser.add_argument("--capacity", type=int, default=4)
    parser.add_argument("--pinned", default="switchyard,actions-smoke-test")
    parser.add_argument("--apply", action="store_true")
    parser.add_argument("--activate", action="store_true")
    args = parser.parse_args()
    env = parse_dotenv(args.env_file)
    root = args.deployment_root.resolve(strict=True)
    if args.activate and not args.apply:
        raise ValueError("activation requires --apply")
    if args.apply:
        for name, value in (("OPENCODE", args.opencode_image), ("VERIFIER", args.verifier_image)):
            if not value or not re.fullmatch(r"sha256:[0-9a-f]{64}", value):
                raise ValueError("preparation requires immutable local executor image IDs")
            env["OPEN_PILOT_PREPARATION_" + name + "_IMAGE"] = value
    private_directory(args.catalog.parent)
    with (args.catalog.parent / "reconcile.lock").open("a") as lock:
        fcntl.flock(lock.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
        registry = load_operator_registry(args.registry)
        gitea = Client(env["GITEA_BASE_URL"], env["GITEA_TOKEN"], "token")
        api = Client(args.control_url, env["SWITCHYARD_CONTROL_API_TOKEN"], "Bearer")
        catalog = discover(gitea, [x.strip() for x in env.get("OPEN_PILOT_REQUIRED_LABELS", "ready-for-agent,agent:queued").split(",") if x.strip()], env.get("OPEN_PILOT_ASSIGNEE", ""))
        states = api.request("/api/open-pilot/states")["states"]
        keep, selected, pending, retired = select_pool(registry, catalog, states, set(args.pinned.split(",")), args.capacity)
        report = {"version": 1, "checked_at": datetime.now(timezone.utc).isoformat(), "repositories": catalog,
                  "selected": [x["slug"] for x in selected], "pending": [x["slug"] for x in pending],
                  "retired": [x["slug"] for x in retired], "applied": False}
        write_atomic(args.catalog, canonical_json(report))
        if args.apply:
            private_directory(args.workspace_root)
            candidate = copy.deepcopy(registry)
            candidate["repositories"] = keep + [prepare_repository(x, args.workspace_root, env, registry["generation"], root / "services/opencode/default-config") for x in selected]
            prepare_registry_filesystem(candidate, os.getuid(), os.getgid(), env["GITEA_BASE_URL"])
            if registry_fingerprint(candidate) != registry_fingerprint(registry):
                candidate_path = args.catalog.parent / "candidate-registry.json"
                write_atomic(candidate_path, canonical_json(candidate))
                load_operator_registry(candidate_path)
                if args.activate:
                    paths = {"registry": args.registry, "manifest": args.registry.parent / "runtime-manifest.json",
                             "override": args.registry.parent / "docker-compose.open-pilot.json",
                             "file_set": args.registry.parent / "compose-file-set.json"}
                    try:
                        activate(api, env, candidate, paths, root / "deploy/switchyard-compose", args.env_file)
                        report["applied"] = True
                    except ReconciliationDeferred as error:
                        report["deferred"] = str(error)
                    write_atomic(args.catalog, canonical_json(report))
        print(json.dumps({"repositories": len(catalog), "selected": report["selected"], "pending": len(pending), "applied": report["applied"], "deferred": report.get("deferred")}))


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, subprocess.SubprocessError, KeyError) as error:
        # Never print an upstream body, child environment, or command arguments.
        print("Open Pilot reconciliation failed: " + (str(error) if isinstance(error, ValueError) else type(error).__name__), file=sys.stderr)
        sys.exit(1)
