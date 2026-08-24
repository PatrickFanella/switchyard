#!/usr/bin/env python3
"""Validate release evidence against a candidate and generated manifest."""

import argparse
import hashlib
import json
import re
import sys
from datetime import datetime, timedelta, timezone
from pathlib import Path, PurePosixPath
from typing import Any
from urllib.parse import urlsplit


GIT_SHA = re.compile(r"^[0-9a-f]{40}$")
SHA256 = re.compile(r"^[0-9a-f]{64}$")
SAFE_ID = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$")
SEMVER_TAG = re.compile(
    r"^v?(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)"
    r"(?:-(?:[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?"
    r"(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$"
)
PULL_REQUEST_PATH = re.compile(r"^/(?:[^/?#]+/)+pulls/[1-9]\d*$")
RFC3339_UTC = re.compile(r"^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z$")
UTC_DATE = re.compile(r"^\d{4}-\d{2}-\d{2}$")
CLOCK_TOLERANCE = timedelta(minutes=5)
RETIREMENT_BACKENDS = {"llama-line", "ocq"}
MAX_ARTIFACT_BYTES = 16 * 1024 * 1024
DEFAULT_ARTIFACT_ROOT: Path | None = None
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


def require_safe_id(value: Any, location: str) -> str:
    text = require_text(value, location)
    if not SAFE_ID.fullmatch(text):
        raise EvidenceError(
            f"{location} must use 1-128 ASCII letters, digits, dot, underscore, or hyphen"
        )
    return text


def normalized_release_tag(tag: str) -> str:
    return tag[1:] if tag.startswith("v") else tag


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


def require_date(value: Any, location: str) -> datetime:
    text = require_text(value, location)
    if not UTC_DATE.fullmatch(text):
        raise EvidenceError(f"{location} must be a UTC date")
    try:
        return datetime.strptime(text, "%Y-%m-%d").replace(tzinfo=timezone.utc)
    except ValueError as exc:
        raise EvidenceError(f"{location} must be a valid UTC date") from exc


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


def canonical_sha256(value: Any) -> str:
    payload = json.dumps(
        value, ensure_ascii=False, separators=(",", ":"), sort_keys=True
    ).encode("utf-8")
    return hashlib.sha256(payload).hexdigest()


def raw_sha256(payload: bytes) -> str:
    return hashlib.sha256(payload).hexdigest()


def compatibility_report_payload(report: dict[str, Any]) -> bytes:
    observations = [
        {
            "date": observation["date"],
            "backend": observation["backend"],
            "attempts": observation["attempts"],
            "source_revision": observation["source_revision"],
            "deployment_id": observation["deployment_id"],
            "observation_started_at": observation["observation_started_at"],
            "observation_completed_at": observation["observation_completed_at"],
            "observation_segment": observation["observation_segment"],
        }
        for observation in report["observations"]
    ]
    canonical = {
        "version": report["version"],
        "source_revision": report["source_revision"],
        "source_identity": report["source_identity"],
        "source_segment": report["source_segment"],
        "window_start": report["window_start"],
        "window_end": report["window_end"],
        "generated_at": report["generated_at"],
        "observations": observations,
        "qualification_eligible": report["qualification_eligible"],
    }
    return json.dumps(canonical, ensure_ascii=False, separators=(",", ":")).encode("utf-8")


def validate_retirement_report(
    report_link_value: Any,
    report_value: Any,
    recorded_at: datetime,
) -> datetime:
    report_link = require_object(report_link_value, "evidence.retirement_proposal.report")
    require_exact_keys(
        report_link,
        "evidence.retirement_proposal.report",
        {"artifact", "raw_sha256", "canonical_sha256", "source_revision", "source_identity", "source_segment"},
    )
    artifact = require_text(
        report_link["artifact"], "evidence.retirement_proposal.report.artifact"
    )
    if Path(artifact).name != artifact or artifact != "compatibility-retirement-report.json":
        raise EvidenceError(
            "evidence.retirement_proposal.report.artifact must be compatibility-retirement-report.json"
        )
    canonical_digest = require_text(
        report_link["canonical_sha256"], "evidence.retirement_proposal.report.canonical_sha256"
    )
    if not SHA256.fullmatch(canonical_digest):
        raise EvidenceError("evidence.retirement_proposal.report.canonical_sha256 must be a lowercase SHA-256")
    if report_value is None:
        raise EvidenceError("retirement proposal requires a compatibility retirement report")
    report = require_object(report_value, "retirement report")
    require_exact_keys(
        report,
        "retirement report",
        {
            "version",
            "source_revision",
            "source_identity",
            "window_start",
            "window_end",
            "generated_at",
            "observations",
            "canonical_sha256", "source_segment", "qualification_eligible",
        },
    )
    if report["version"] != "switchyard.compatibility-observation/v1":
        raise EvidenceError("retirement report.version is unsupported")
    if report["qualification_eligible"] is not True:
        raise EvidenceError("retirement report is diagnostic and not qualification eligible")
    if canonical_sha256(report) != canonical_digest:
        raise EvidenceError("retirement report canonical hash does not match candidate evidence")
    internal_digest = require_text(report["canonical_sha256"], "retirement report.canonical_sha256")
    if not SHA256.fullmatch(internal_digest):
        raise EvidenceError("retirement report.canonical_sha256 must be a lowercase SHA-256")
    source_revision = require_text(report["source_revision"], "retirement report.source_revision")
    linked_revision = require_text(
        report_link["source_revision"],
        "evidence.retirement_proposal.report.source_revision",
    )
    if not GIT_SHA.fullmatch(source_revision) or linked_revision != source_revision:
        raise EvidenceError("retirement report source_revision does not match candidate evidence")
    source_identity = require_safe_id(report["source_identity"], "retirement report.source_identity")
    source_segment = require_safe_id(report["source_segment"], "retirement report.source_segment")
    linked_identity = require_text(
        report_link["source_identity"],
        "evidence.retirement_proposal.report.source_identity",
    )
    if source_identity != linked_identity:
        raise EvidenceError("retirement report source_identity does not match candidate evidence")
    linked_segment = require_safe_id(
        report_link["source_segment"],
        "evidence.retirement_proposal.report.source_segment",
    )
    if source_segment != linked_segment:
        raise EvidenceError("retirement report source_segment does not match candidate evidence")

    window_start = require_date(report["window_start"], "retirement report.window_start")
    window_end = require_date(report["window_end"], "retirement report.window_end")
    generated_at = require_timestamp(report["generated_at"], "retirement report.generated_at")
    elapsed_end = window_end + timedelta(days=1)
    if elapsed_end - window_start != timedelta(days=90):
        raise EvidenceError("retirement report must cover exactly 90 elapsed days")
    if generated_at < elapsed_end:
        raise EvidenceError("retirement report must be generated at or after its window end")
    if generated_at > recorded_at + CLOCK_TOLERANCE:
        raise EvidenceError("retirement report generated after evidence.recorded_at")
    observations = report["observations"]
    expected_days = (elapsed_end - window_start).days
    if not isinstance(observations, list) or len(observations) != expected_days * 2:
        raise EvidenceError("retirement report must have complete daily coverage for both backends")
    expected = [
        ((window_start + timedelta(days=offset)).strftime("%Y-%m-%d"), backend)
        for offset in range(expected_days)
        for backend in ("llama-line", "ocq")
    ]
    actual: list[tuple[str, str]] = []
    daily_identity: dict[str, tuple[str, str]] = {}
    for index, observation_value in enumerate(observations):
        location = f"retirement report.observations[{index}]"
        observation = require_object(observation_value, location)
        require_exact_keys(observation, location, {"date", "backend", "attempts", "source_revision", "deployment_id", "observation_started_at", "observation_completed_at", "observation_segment"})
        date = require_date(observation["date"], f"{location}.date")
        backend = require_text(observation["backend"], f"{location}.backend")
        actual.append((date.strftime("%Y-%m-%d"), backend))
        if type(observation["attempts"]) is not int or observation["attempts"] != 0:
            raise EvidenceError(f"{location}.attempts must be integer zero")
        revision = require_text(observation["source_revision"], f"{location}.source_revision")
        if not GIT_SHA.fullmatch(revision):
            raise EvidenceError(f"{location}.source_revision must be a full lowercase Git SHA")
        if revision != source_revision:
            raise EvidenceError(f"{location}.source_revision does not match the report source_revision")
        deployment = require_safe_id(observation["deployment_id"], f"{location}.deployment_id")
        segment = require_safe_id(observation["observation_segment"], f"{location}.observation_segment")
        if deployment != source_identity or segment != source_segment:
            raise EvidenceError(f"{location} does not match the report identity and segment")
        identity = (revision, deployment)
        date_text = date.strftime("%Y-%m-%d")
        if date_text in daily_identity and daily_identity[date_text] != identity:
            raise EvidenceError(f"{location} does not match the day's revision and deployment identity")
        daily_identity[date_text] = identity
        started = require_timestamp(observation["observation_started_at"], f"{location}.observation_started_at")
        completed = require_timestamp(observation["observation_completed_at"], f"{location}.observation_completed_at")
        if started > date or completed < date + timedelta(days=1):
            raise EvidenceError(f"{location} does not cover its complete UTC day")
    if actual != expected:
        raise EvidenceError("retirement report must have ordered complete daily coverage for both backends")
    if hashlib.sha256(compatibility_report_payload(report)).hexdigest() != internal_digest:
        raise EvidenceError("retirement report canonical_sha256 does not match its contents")

    return generated_at


def validate_retirement_proposal(
    value: Any,
    report_value: Any,
    candidate_sha: str,
    release_tag: str,
    recorded_at: datetime,
    artifact_root: Path | None = None,
    active: frozenset[Path] = frozenset(),
) -> None:
    proposal = require_object(value, "evidence.retirement_proposal")
    require_exact_keys(proposal, "evidence.retirement_proposal", {"report", "prior_qualifying_releases"})
    report_link = require_object(proposal["report"], "evidence.retirement_proposal.report")
    if artifact_root is None:
        raise EvidenceError("retirement proposal requires an artifact root")
    report_artifact, candidate_report_path = load_artifact_json(
        artifact_root,
        require_text(report_link.get("artifact"), "evidence.retirement_proposal.report.artifact"),
        require_text(report_link.get("raw_sha256"), "evidence.retirement_proposal.report.raw_sha256"),
        "candidate retirement report",
        active,
    )
    if report_value is None:
        raise EvidenceError("retirement proposal requires a compatibility retirement report")
    if canonical_sha256(report_value) != canonical_sha256(report_artifact):
        raise EvidenceError("supplied retirement report does not match its named artifact")
    generated_at = validate_retirement_report(report_link, report_artifact, recorded_at)
    if require_object(report_artifact, "retirement report").get("source_revision") != candidate_sha:
        raise EvidenceError("retirement report source_revision must match candidate SHA")

    releases = proposal["prior_qualifying_releases"]
    if not isinstance(releases, list) or len(releases) != 2:
        raise EvidenceError(
            "evidence.retirement_proposal.prior_qualifying_releases must contain exactly two releases"
        )
    seen_tags: set[str] = set()
    seen_shas: set[str] = set()
    seen_hashes: set[str] = set()
    previous_qualification_time: datetime | None = None
    for index, release_value in enumerate(releases):
        location = f"evidence.retirement_proposal.prior_qualifying_releases[{index}]"
        release = require_object(release_value, location)
        require_exact_keys(
            release,
            location,
            {"release_tag", "release_sha", "qualification_artifact", "qualification_raw_sha256"},
        )
        tag = require_text(release["release_tag"], f"{location}.release_tag")
        if not SEMVER_TAG.fullmatch(tag):
            raise EvidenceError(f"{location}.release_tag must be a semver-like release tag")
        sha = require_text(release["release_sha"], f"{location}.release_sha")
        if not GIT_SHA.fullmatch(sha):
            raise EvidenceError(f"{location}.release_sha must be a full lowercase Git SHA")
        qualification_hash = require_text(
            release["qualification_raw_sha256"], f"{location}.qualification_raw_sha256"
        )
        if not SHA256.fullmatch(qualification_hash):
            raise EvidenceError(f"{location}.qualification_raw_sha256 must be a lowercase SHA-256")
        normalized_tag = normalized_release_tag(tag)
        if normalized_tag == normalized_release_tag(release_tag) or sha == candidate_sha:
            raise EvidenceError("candidate release cannot count as prior qualifying release evidence")
        if normalized_tag in seen_tags or sha in seen_shas or qualification_hash in seen_hashes:
            raise EvidenceError("prior qualifying releases must be distinct immutable records")
        qualification, qualification_path = load_artifact_json(
            artifact_root,
            require_text(release["qualification_artifact"], f"{location}.qualification_artifact"),
            qualification_hash,
            f"{location} qualification",
            active | {candidate_report_path},
        )
        qualification = require_object(qualification, f"{location} qualification")
        require_exact_keys(
            qualification,
            f"{location} qualification",
            {
                "schema_version", "release_tag", "release_sha", "qualified_at",
                "evidence_artifact", "evidence_raw_sha256", "manifest_artifact",
                "manifest_raw_sha256", "report_artifact", "report_raw_sha256",
                "report_canonical_sha256",
            },
        )
        if qualification["schema_version"] != "switchyard.compatibility-qualification/v1":
            raise EvidenceError(f"{location} qualification schema_version is unsupported")
        if normalized_release_tag(require_text(qualification["release_tag"], f"{location} qualification.release_tag")) != normalized_tag or qualification["release_sha"] != sha:
            raise EvidenceError(f"{location} qualification release identity does not match its binding")
        qualified_at = require_timestamp(qualification["qualified_at"], f"{location} qualification.qualified_at")
        evidence_hash = require_text(qualification["evidence_raw_sha256"], f"{location} qualification.evidence_raw_sha256")
        manifest_hash = require_text(qualification["manifest_raw_sha256"], f"{location} qualification.manifest_raw_sha256")
        report_hash = require_text(qualification["report_raw_sha256"], f"{location} qualification.report_raw_sha256")
        report_canonical_hash = require_text(qualification["report_canonical_sha256"], f"{location} qualification.report_canonical_sha256")
        if not all(SHA256.fullmatch(value) for value in (evidence_hash, manifest_hash, report_hash, report_canonical_hash)):
            raise EvidenceError(f"{location} qualification artifact hashes must be lowercase SHA-256 values")
        component_root = qualification_path.parent
        prior_evidence, evidence_path = load_artifact_json(component_root, require_artifact_basename(qualification["evidence_artifact"], f"{location} qualification.evidence_artifact"), evidence_hash, f"{location} evidence", active | {candidate_report_path, qualification_path})
        prior_manifest, manifest_path = load_artifact_json(component_root, require_artifact_basename(qualification["manifest_artifact"], f"{location} qualification.manifest_artifact"), manifest_hash, f"{location} manifest", active | {candidate_report_path, qualification_path, evidence_path})
        prior_report, report_path = load_artifact_json(component_root, require_artifact_basename(qualification["report_artifact"], f"{location} qualification.report_artifact"), report_hash, f"{location} report", active | {candidate_report_path, qualification_path, evidence_path, manifest_path})
        if canonical_sha256(prior_report) != report_canonical_hash:
            raise EvidenceError(f"{location} report canonical hash does not match qualification")
        prior_record = require_object(prior_evidence, f"{location} evidence")
        if prior_record.get("candidate_sha") != sha or normalized_release_tag(require_text(prior_record.get("release_tag"), f"{location} evidence.release_tag")) != normalized_tag:
            raise EvidenceError(f"{location} evidence release identity does not match its binding")
        if prior_record.get("phase") != "deployed":
            raise EvidenceError(f"{location} evidence must be deployed phase")
        if "retirement_proposal" in prior_record:
            raise EvidenceError(f"{location} evidence must not contain retirement_proposal")
        prior_recorded_at = require_timestamp(prior_record.get("recorded_at"), f"{location} evidence.recorded_at")
        if qualified_at < prior_recorded_at or qualified_at > recorded_at:
            raise EvidenceError("prior qualification time must follow evidence finalization and predate candidate evidence")
        deployment = require_object(prior_record.get("deployment"), f"{location} evidence.deployment")
        deployed_at = require_timestamp(deployment.get("deployed_at"), f"{location} evidence.deployment.deployed_at")
        report_window_start = require_date(prior_report.get("window_start"), f"{location} report.window_start")
        if deployed_at > report_window_start:
            raise EvidenceError("prior qualifying release deployment must precede its zero-use observation window")
        smoke_at = require_timestamp(require_object(prior_record.get("gates"), f"{location} evidence.gates").get("production_smoke", {}).get("completed_at"), f"{location} evidence.gates.production_smoke.completed_at")
        if smoke_at > report_window_start:
            raise EvidenceError("prior qualifying release production smoke must precede its zero-use observation window")
        validate(
            sha,
            prior_record,
            prior_manifest,
            qualified_at,
            artifact_root=artifact_root,
        )
        prior_link = {"artifact": "compatibility-retirement-report.json", "raw_sha256": report_hash, "canonical_sha256": report_canonical_hash, "source_revision": prior_report.get("source_revision"), "source_identity": prior_report.get("source_identity"), "source_segment": prior_report.get("source_segment")}
        prior_generated = validate_retirement_report(prior_link, prior_report, prior_recorded_at)
        if prior_report.get("source_revision") != sha:
            raise EvidenceError(f"{location} report source_revision does not match release_sha")
        if prior_generated >= generated_at:
            raise EvidenceError("prior qualifying release report must predate the candidate report")
        if previous_qualification_time is not None and qualified_at <= previous_qualification_time:
            raise EvidenceError("prior qualifying releases must be ordered by qualification time")
        if prior_generated > prior_recorded_at:
            raise EvidenceError("prior qualifying release report must exist before deployed evidence finalization")
        previous_qualification_time = qualified_at
        seen_tags.add(normalized_tag)
        seen_shas.add(sha)
        seen_hashes.add(qualification_hash)


def validate(
    candidate_sha: str,
    evidence: Any,
    manifest: Any,
    now: datetime,
    retirement_report: Any = None,
    artifact_root: Path | None = None,
    active: frozenset[Path] = frozenset(),
) -> None:
    artifact_root = artifact_root or DEFAULT_ARTIFACT_ROOT
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
        {"deployment", "retirement_proposal"},
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
    if "retirement_proposal" in record:
        validate_retirement_proposal(
            record["retirement_proposal"],
            retirement_report,
            candidate_sha,
            release_tag,
            recorded_at,
            artifact_root,
            active,
        )
    elif retirement_report is not None:
        raise EvidenceError("retirement report supplied without a retirement proposal")

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


def parse_json_bytes(payload: bytes, label: str) -> Any:
    def reject_duplicate_keys(pairs: list[tuple[str, Any]]) -> dict[str, Any]:
        result = {}
        for key, value in pairs:
            if key in result:
                raise EvidenceError(f"{label} contains duplicate JSON key {key!r}")
            result[key] = value
        return result

    try:
        return json.loads(payload.decode("utf-8"), object_pairs_hook=reject_duplicate_keys)
    except (UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise EvidenceError(f"cannot parse {label}: {exc}") from exc


def load_json(path: Path, label: str) -> Any:
    try:
        return parse_json_bytes(path.read_bytes(), label)
    except OSError as exc:
        raise EvidenceError(f"cannot read {label} {path}: {exc}") from exc


def resolve_artifact(root: Path, artifact: str, location: str) -> Path:
    path = PurePosixPath(artifact)
    if not artifact or "\\" in artifact or path.is_absolute() or any(part in {"", ".", ".."} for part in path.parts):
        raise EvidenceError(f"{location} must be a safe relative POSIX path")
    root_path = root.resolve()
    try:
        resolved = (root_path / Path(*path.parts)).resolve(strict=True)
        resolved.relative_to(root_path)
    except (OSError, ValueError) as exc:
        raise EvidenceError(f"{location} escapes or is missing from artifact root") from exc
    if not resolved.is_file():
        raise EvidenceError(f"{location} must reference a regular file")
    return resolved


def require_artifact_basename(value: Any, location: str) -> str:
    artifact = require_text(value, location)
    path = PurePosixPath(artifact)
    if "\\" in artifact or len(path.parts) != 1 or path.name != artifact or artifact.startswith(".") or not artifact.endswith(".json"):
        raise EvidenceError(f"{location} must be a visible JSON artifact basename")
    return artifact


def artifact_json_bytes(root: Path, artifact: str, label: str, active: frozenset[Path]) -> tuple[bytes, Path]:
    path = resolve_artifact(root, artifact, f"{label} artifact")
    if path in active:
        raise EvidenceError(f"{label} creates a recursive artifact cycle")
    try:
        payload = path.read_bytes()
    except OSError as exc:
        raise EvidenceError(f"cannot read {label} {path}: {exc}") from exc
    if len(payload) > MAX_ARTIFACT_BYTES:
        raise EvidenceError(f"{label} exceeds {MAX_ARTIFACT_BYTES} bytes")
    return payload, path


def load_artifact_json(root: Path, artifact: str, expected_hash: str, label: str, active: frozenset[Path]) -> tuple[Any, Path]:
    if not SHA256.fullmatch(expected_hash):
        raise EvidenceError(f"{label} raw hash must be a lowercase SHA-256")
    payload, path = artifact_json_bytes(root, artifact, label, active)
    if hashlib.sha256(payload).hexdigest() != expected_hash:
        raise EvidenceError(f"{label} raw byte hash does not match evidence")
    return parse_json_bytes(payload, label), path


def load_canonical_artifact_json(root: Path, artifact: str, expected_hash: str, label: str, active: frozenset[Path]) -> tuple[Any, Path]:
    if not SHA256.fullmatch(expected_hash):
        raise EvidenceError(f"{label} canonical hash must be a lowercase SHA-256")
    payload, path = artifact_json_bytes(root, artifact, label, active)
    value = parse_json_bytes(payload, label)
    if canonical_sha256(value) != expected_hash:
        raise EvidenceError(f"{label} canonical hash does not match evidence")
    return value, path


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--candidate-sha", required=True)
    parser.add_argument("--evidence", required=True, type=Path)
    parser.add_argument("--manifest", required=True, type=Path)
    parser.add_argument("--retirement-report", type=Path)
    parser.add_argument("--artifact-root", type=Path, default=Path("."))
    args = parser.parse_args()
    try:
        validate(
            args.candidate_sha,
            load_json(args.evidence, "evidence"),
            load_json(args.manifest, "manifest"),
            datetime.now(timezone.utc),
            load_json(args.retirement_report, "retirement report") if args.retirement_report else None,
            args.artifact_root,
        )
    except EvidenceError as exc:
        print(f"release evidence invalid: {exc}", file=sys.stderr)
        return 1
    print(f"release evidence valid for {args.candidate_sha}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
