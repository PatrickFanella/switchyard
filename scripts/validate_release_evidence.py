#!/usr/bin/env python3
"""Validate release evidence against a candidate and generated manifest."""

import argparse
import json
import re
import sys
from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import Any
from urllib.parse import urlsplit


GIT_SHA = re.compile(r"^[0-9a-f]{40}$")
SHA256 = re.compile(r"^[0-9a-f]{64}$")
SEMVER_TAG = re.compile(
    r"^v?(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)"
    r"(?:-(?:[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?"
    r"(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$"
)
PULL_REQUEST_PATH = re.compile(r"^/(?:[^/?#]+/)+pulls/[1-9]\d*$")
RFC3339_UTC = re.compile(r"^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z$")
CLOCK_TOLERANCE = timedelta(minutes=5)
PLACEHOLDERS = {"", "todo", "tbd", "unknown", "changeme", "replace_me", "placeholder"}
PLACEHOLDER_TOKEN = re.compile(
    r"(?:^|[^a-z0-9])(?:todo|tbd|unknown|changeme|replace[_ -]?me|placeholder)(?:$|[^a-z0-9])",
    re.IGNORECASE,
)


class EvidenceError(ValueError):
    pass


def require_object(value: Any, location: str) -> dict[str, Any]:
    if not isinstance(value, dict):
        raise EvidenceError(f"{location} must be an object")
    return value


def require_exact_keys(
    value: dict[str, Any], location: str, required: set[str], optional: set[str] | frozenset[str] = frozenset()
) -> None:
    missing = sorted(required - value.keys())
    if missing:
        raise EvidenceError(f"{location} missing required field(s): {', '.join(missing)}")
    extra = sorted(value.keys() - required - optional)
    if extra:
        raise EvidenceError(f"{location} has unknown field(s): {', '.join(extra)}")


def require_text(value: Any, location: str) -> str:
    if not isinstance(value, str):
        raise EvidenceError(f"{location} must be a string")
    text = value.strip()
    lowered = text.lower()
    if (
        lowered in PLACEHOLDERS
        or PLACEHOLDER_TOKEN.search(text)
        or "<" in text
        or ">" in text
        or "${" in text
        or "example.com" in lowered
    ):
        raise EvidenceError(f"{location} contains a placeholder")
    return text


def require_timestamp(value: Any, location: str) -> datetime:
    text = require_text(value, location)
    if not RFC3339_UTC.fullmatch(text):
        raise EvidenceError(f"{location} must be a strict RFC 3339 UTC timestamp")
    try:
        parsed = datetime.fromisoformat(text[:-1] + "+00:00")
    except ValueError as exc:
        raise EvidenceError(f"{location} must be an RFC 3339 timestamp") from exc
    if parsed.tzinfo != timezone.utc:
        raise EvidenceError(f"{location} must be UTC")
    return parsed


def require_pull_request(value: Any) -> str:
    text = require_text(value, "evidence.pull_request")
    try:
        parsed = urlsplit(text)
        hostname = parsed.hostname
    except ValueError as exc:
        raise EvidenceError("evidence.pull_request must be a credential-free HTTPS pull request URL") from exc
    if (
        parsed.scheme != "https"
        or not hostname
        or parsed.username is not None
        or parsed.password is not None
        or parsed.query
        or parsed.fragment
        or not PULL_REQUEST_PATH.fullmatch(parsed.path)
    ):
        raise EvidenceError(
            "evidence.pull_request must be a credential-free HTTPS URL ending in /pulls/<positive integer>"
        )
    return text


def require_rollback_target(value: Any, candidate_sha: str, release_tag: str) -> str:
    target = require_text(value, "evidence.rollback_target")
    if not GIT_SHA.fullmatch(target) and not SEMVER_TAG.fullmatch(target):
        raise EvidenceError(
            "evidence.rollback_target must be a full lowercase Git SHA or semver-like release tag"
        )
    if target in {candidate_sha, release_tag}:
        raise EvidenceError("evidence.rollback_target must differ from candidate_sha and release_tag")
    return target


def validate_gate(value: Any, location: str, ci_run: str | None = None) -> datetime:
    gate = require_object(value, location)
    require_exact_keys(gate, location, {"status", "completed_at", "proof"})
    if gate["status"] != "passed":
        raise EvidenceError(f"{location}.status must be passed")
    completed_at = require_timestamp(gate["completed_at"], f"{location}.completed_at")
    proof = require_text(gate["proof"], f"{location}.proof")
    if ci_run is not None and proof != ci_run:
        raise EvidenceError(f"{location}.proof must match evidence.ci_run")
    return completed_at


def artifact_checksums(
    items: Any, location: str, checksum_key: str, *, allow_oci_digest: bool = False
) -> dict[str, str]:
    if not isinstance(items, list) or not items:
        raise EvidenceError(f"{location} must be a non-empty array")
    checksums: dict[str, str] = {}
    for index, item_value in enumerate(items):
        item_location = f"{location}[{index}]"
        item = require_object(item_value, item_location)
        required = {"archive", checksum_key}
        optional = {"oci_index_digest"} if allow_oci_digest else set()
        require_exact_keys(item, item_location, required, optional)
        archive = require_text(item["archive"], f"{item_location}.archive")
        checksum = require_text(item[checksum_key], f"{item_location}.{checksum_key}")
        if not SHA256.fullmatch(checksum):
            raise EvidenceError(f"{item_location}.{checksum_key} must be a lowercase SHA-256")
        if archive in checksums:
            raise EvidenceError(f"{location} contains duplicate archive {archive}")
        checksums[archive] = checksum
    return checksums


def validate(candidate_sha: str, evidence: Any, manifest: Any, now: datetime) -> None:
    if not GIT_SHA.fullmatch(candidate_sha):
        raise EvidenceError("candidate SHA must be a full lowercase 40-character Git SHA")
    if now.tzinfo is None:
        raise EvidenceError("validation time must include a timezone")
    now = now.astimezone(timezone.utc)

    manifest_object = require_object(manifest, "manifest")
    manifest_sha = manifest_object.get("source_revision")
    if manifest_sha != candidate_sha:
        raise EvidenceError("manifest source_revision does not match candidate SHA")
    manifest_release_tag = require_text(manifest_object.get("release_tag"), "manifest.release_tag")
    manifest_ci_run = require_text(manifest_object.get("ci_run_url"), "manifest.ci_run_url")
    manifest_migration_head = require_text(
        manifest_object.get("migration_head"), "manifest.migration_head"
    )
    manifest_checksums = artifact_checksums(
        manifest_object.get("images"),
        "manifest.images",
        "archive_sha256",
        allow_oci_digest=True,
    )

    record = require_object(evidence, "evidence")
    require_exact_keys(
        record,
        "evidence",
        {
            "schema_version",
            "phase",
            "candidate_sha",
            "release_tag",
            "pull_request",
            "ci_run",
            "migration_head",
            "rollback_target",
            "exceptions",
            "recorded_at",
            "gates",
            "artifacts",
        },
        {"deployment"},
    )
    if type(record["schema_version"]) is not int or record["schema_version"] != 1:
        raise EvidenceError("evidence.schema_version must be integer 1")
    phase = record["phase"]
    if phase not in {"candidate", "deployed"}:
        raise EvidenceError("evidence.phase must be candidate or deployed")
    if record["candidate_sha"] != candidate_sha:
        raise EvidenceError("evidence candidate_sha does not match candidate SHA")

    release_tag = require_text(record["release_tag"], "evidence.release_tag")
    if release_tag != manifest_release_tag:
        raise EvidenceError("evidence release_tag does not match manifest release_tag")
    require_pull_request(record["pull_request"])
    ci_run = require_text(record["ci_run"], "evidence.ci_run")
    if ci_run != manifest_ci_run:
        raise EvidenceError("evidence ci_run does not match manifest ci_run_url")
    migration_head = require_text(record["migration_head"], "evidence.migration_head")
    if migration_head != manifest_migration_head:
        raise EvidenceError("evidence migration_head does not match manifest migration_head")
    require_rollback_target(record["rollback_target"], candidate_sha, release_tag)
    exceptions = record["exceptions"]
    if not isinstance(exceptions, list):
        raise EvidenceError("evidence.exceptions must be an array")
    for index, exception in enumerate(exceptions):
        require_text(exception, f"evidence.exceptions[{index}]")

    recorded_at = require_timestamp(record["recorded_at"], "evidence.recorded_at")
    if recorded_at > now + CLOCK_TOLERANCE:
        raise EvidenceError("evidence.recorded_at is in the future")

    gates = require_object(record["gates"], "evidence.gates")
    required_gates = {"verify_production", "extended_soak"}
    if phase == "deployed":
        required_gates |= {"backup_restore", "production_smoke"}
    require_exact_keys(gates, "evidence.gates", required_gates)
    gate_completed_at = {}
    for name in sorted(required_gates):
        gate_ci_run = ci_run if name in {"verify_production", "extended_soak"} else None
        completed_at = validate_gate(gates[name], f"evidence.gates.{name}", gate_ci_run)
        if completed_at > recorded_at + CLOCK_TOLERANCE:
            raise EvidenceError(f"evidence.gates.{name} completed after evidence.recorded_at")
        gate_completed_at[name] = completed_at

    evidence_checksums = artifact_checksums(
        record["artifacts"], "evidence.artifacts", "archive_sha256"
    )
    if evidence_checksums != manifest_checksums:
        raise EvidenceError("evidence artifact checksums do not match manifest images")

    if phase == "candidate":
        if "deployment" in record:
            raise EvidenceError("candidate evidence must omit deployment")
        return

    deployment = record.get("deployment")
    deployment_object = require_object(deployment, "evidence.deployment")
    require_exact_keys(deployment_object, "evidence.deployment", {"deployed_sha", "deployed_at", "host"})
    if deployment_object["deployed_sha"] != candidate_sha:
        raise EvidenceError("evidence deployed_sha does not match candidate SHA")
    deployed_at = require_timestamp(deployment_object["deployed_at"], "evidence.deployment.deployed_at")
    if deployed_at > recorded_at + CLOCK_TOLERANCE:
        raise EvidenceError("evidence deployed after evidence.recorded_at")
    for name in ("verify_production", "extended_soak", "backup_restore"):
        if deployed_at + CLOCK_TOLERANCE < gate_completed_at[name]:
            raise EvidenceError(f"evidence deployment predates qualification gate {name}")
    if gate_completed_at["production_smoke"] + CLOCK_TOLERANCE < deployed_at:
        raise EvidenceError("evidence production_smoke predates deployment")
    require_text(deployment_object["host"], "evidence.deployment.host")


def load_json(path: Path, label: str) -> Any:
    def reject_duplicate_keys(pairs: list[tuple[str, Any]]) -> dict[str, Any]:
        result = {}
        for key, value in pairs:
            if key in result:
                raise EvidenceError(f"{label} contains duplicate JSON key {key!r}")
            result[key] = value
        return result

    try:
        with path.open(encoding="utf-8") as source:
            return json.load(source, object_pairs_hook=reject_duplicate_keys)
    except (OSError, json.JSONDecodeError) as exc:
        raise EvidenceError(f"cannot read {label} {path}: {exc}") from exc


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--candidate-sha", required=True)
    parser.add_argument("--evidence", required=True, type=Path)
    parser.add_argument("--manifest", required=True, type=Path)
    args = parser.parse_args()
    try:
        validate(
            args.candidate_sha,
            load_json(args.evidence, "evidence"),
            load_json(args.manifest, "manifest"),
            datetime.now(timezone.utc),
        )
    except EvidenceError as exc:
        print(f"release evidence invalid: {exc}", file=sys.stderr)
        return 1
    print(f"release evidence valid for {args.candidate_sha}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
