#!/usr/bin/env python3
"""Produce one durable four-file retirement qualification bundle."""

from __future__ import annotations

import argparse
import hashlib
import io
import json
import os
import re
import sys
import tempfile
import urllib.parse
import urllib.request
import zipfile
from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import Any

import download_retirement_qualifications as gitea
import restore_evidence
import validate_release_evidence as release_validator


ROOT = Path(__file__).resolve().parents[1]
QUALIFICATION_NAME = "retirement-release-qualification.json"
EVIDENCE_NAME = "deployed-evidence.json"
MANIFEST_NAME = "manifest.json"
REPORT_NAME = "compatibility-retirement-report.json"
CANDIDATE_MEMBERS = {"candidate-evidence.json", MANIFEST_NAME}
MAX_CANDIDATE_ARCHIVE_BYTES = 2 * 1024 * 1024
MAX_REPORT_BYTES = 4 * 1024 * 1024
MAX_RESTORE_PROOF_AGE = timedelta(days=8)
SAFE_BASENAME = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]{0,127}\.json$")
PRODUCTION_DEPLOYMENT_WORKFLOW = "prove-production-deployment.yml"
PRODUCTION_SMOKE_JOB = "production-smoke"
PRODUCTION_RUNNER_LABEL = "switchyard-production"
VERIFY_PRODUCTION_WORKFLOW = "verify-production.yml"
VERIFY_PRODUCTION_JOB = "verify-production"


class QualificationError(ValueError):
    pass


def parse_timestamp(value: str, label: str) -> datetime:
    try:
        parsed = datetime.fromisoformat(value.removesuffix("Z") + "+00:00")
    except ValueError as error:
        raise QualificationError(f"{label} must be a strict RFC 3339 UTC timestamp") from error
    if not value.endswith("Z") or parsed.tzinfo != timezone.utc:
        raise QualificationError(f"{label} must be a strict RFC 3339 UTC timestamp")
    return parsed


def timestamp(value: datetime) -> str:
    return value.astimezone(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def load_json_bytes(payload: bytes, label: str) -> Any:
    try:
        return json.loads(payload.decode("utf-8"), object_pairs_hook=_reject_duplicates)
    except (UnicodeDecodeError, json.JSONDecodeError) as error:
        raise QualificationError(f"cannot parse {label}: {error}") from error


def load_json(path: Path, label: str) -> Any:
    try:
        return load_json_bytes(path.read_bytes(), label)
    except OSError as error:
        raise QualificationError(f"cannot read {label}") from error


def write_json(path: Path, value: Any) -> None:
    path.write_text(json.dumps(value, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")


def fetch_candidate_record(
    api_url: str, repository: str, token: str, run_id: int, artifact_id: int, expected_sha: str
) -> tuple[dict[str, Any], dict[str, Any]]:
    base_url, encoded_repo = gitea.validate_origin(api_url, repository)
    if not token:
        raise QualificationError("Gitea token is required")
    try:
        run = gitea._verify_run_provenance(
            base_url,
            encoded_repo,
            token,
            run_id,
            VERIFY_PRODUCTION_WORKFLOW,
            VERIFY_PRODUCTION_JOB,
            expected_sha,
        )
    except gitea.DownloadError as error:
        raise QualificationError(str(error)) from error
    list_url = f"{base_url}/repos/{encoded_repo}/actions/runs/{run_id}/artifacts?limit=100&page=1"
    listing = gitea._request_json(list_url, token, gitea.MAX_LIST_BYTES, f"artifacts for run {run_id}")
    artifacts = listing.get("artifacts") if isinstance(listing, dict) else None
    if not isinstance(artifacts, list) or listing.get("total_count", len(artifacts)) > 100:
        raise QualificationError("Gitea returned an unsafe candidate artifact list")
    matches = [item for item in artifacts if isinstance(item, dict) and item.get("id") == artifact_id]
    if len(matches) != 1:
        raise QualificationError("candidate artifact must occur exactly once in the named run")
    artifact = matches[0]
    if (
        artifact.get("name") != f"candidate-release-record-{expected_sha}"
        or artifact.get("expired") is not False
        or artifact.get("workflow_run", {}).get("id") != run_id
        or artifact.get("workflow_run", {}).get("head_sha") != expected_sha
    ):
        raise QualificationError("candidate artifact is expired or belongs to another run")
    download_url = f"{base_url}/repos/{encoded_repo}/actions/artifacts/{artifact_id}/zip"
    archive = gitea._request_bytes(
        download_url,
        token,
        MAX_CANDIDATE_ARCHIVE_BYTES,
        f"candidate artifact {artifact_id}",
        allowed_redirect_path=urllib.parse.urlsplit(download_url).path + "/raw",
    )
    try:
        with zipfile.ZipFile(io.BytesIO(archive)) as bundle:
            members = bundle.infolist()
            if {member.filename for member in members} != CANDIDATE_MEMBERS or any(
                member.is_dir() or member.file_size > MAX_CANDIDATE_ARCHIVE_BYTES for member in members
            ):
                raise QualificationError("candidate artifact must contain exactly candidate-evidence.json and manifest.json")
            values = {
                member.filename: load_json_bytes(bundle.read(member), member.filename)
                for member in members
            }
    except zipfile.BadZipFile as error:
        raise QualificationError("candidate artifact is not a valid ZIP") from error
    candidate, manifest = values["candidate-evidence.json"], values[MANIFEST_NAME]
    if not isinstance(candidate, dict) or not isinstance(manifest, dict):
        raise QualificationError("candidate artifact members must contain JSON objects")
    if candidate.get("ci_run") != run.get("url") or manifest.get("ci_run_url") != run.get("url"):
        raise QualificationError("candidate evidence does not reference its verified Actions run URL")
    return candidate, manifest


def fetch_report(control_url: str, token: str, now: datetime) -> dict[str, Any]:
    parsed = urllib.parse.urlsplit(control_url)
    if parsed.scheme not in {"http", "https"} or not parsed.netloc or parsed.username or parsed.password or parsed.query or parsed.fragment:
        raise QualificationError("control URL must be a credential-free absolute HTTP(S) URL")
    end = now.astimezone(timezone.utc).date() - timedelta(days=1)
    start = end - timedelta(days=89)
    query = urllib.parse.urlencode({"start": start.isoformat(), "end": end.isoformat()})
    url = control_url.rstrip("/") + "/api/compatibility/report?" + query
    request = urllib.request.Request(url, headers={"Accept": "application/json", "Authorization": f"Bearer {token}"})
    if not token:
        raise QualificationError("control API token is required")
    try:
        with urllib.request.build_opener(gitea._SameOriginRedirects(url, None)).open(request, timeout=30) as response:
            payload = response.read(MAX_REPORT_BYTES + 1)
    except OSError as error:
        raise QualificationError("authenticated compatibility report request failed") from error
    if len(payload) > MAX_REPORT_BYTES:
        raise QualificationError("compatibility report exceeds the byte limit")
    wrapper = load_json_bytes(payload, "compatibility report response")
    if not isinstance(wrapper, dict) or set(wrapper) != {"compatibility_report"}:
        raise QualificationError("control API returned an invalid compatibility report envelope")
    report = wrapper["compatibility_report"]
    if not isinstance(report, dict):
        raise QualificationError("control API returned an invalid compatibility report")
    if report.get("window_start") != start.isoformat() or report.get("window_end") != end.isoformat():
        raise QualificationError("control API report window does not match the requested 90 elapsed days")
    return report


def verify_smoke_run(proof: str, api_url: str, repository: str, token: str, sha: str, deployed_at: datetime, completed_at: datetime) -> None:
    run_id = validate_smoke_proof(proof, api_url, repository)
    base_url, encoded_repo = gitea.validate_origin(api_url, repository)
    if not token:
        raise QualificationError("Gitea token is required")
    try:
        run = gitea._verify_run_provenance(
            base_url,
            encoded_repo,
            token,
            run_id,
            PRODUCTION_DEPLOYMENT_WORKFLOW,
            PRODUCTION_SMOKE_JOB,
            sha,
        )
    except gitea.DownloadError as error:
        raise QualificationError(str(error)) from error
    if run.get("url") != proof:
        raise QualificationError("production smoke proof URL does not match its Actions run")
    run_completed = parse_timestamp(run.get("updated_at", ""), "production smoke run updated_at")
    if abs(run_completed - completed_at) > release_validator.CLOCK_TOLERANCE:
        raise QualificationError("production smoke completion time does not match its Actions run")
    run_started = parse_timestamp(run.get("run_started_at", ""), "production smoke run run_started_at")
    if run_started > completed_at or abs(run_started - deployed_at) > release_validator.CLOCK_TOLERANCE:
        raise QualificationError("production smoke Actions run chronology is invalid")


def produce(args: argparse.Namespace, now: datetime) -> None:
    if args.output.exists():
        raise QualificationError("output path already exists")
    if not SAFE_BASENAME.fullmatch(args.restore_evidence_basename):
        raise QualificationError("restore evidence must be a safe JSON basename")
    deployed_at = parse_timestamp(args.deployed_at, "deployed_at")
    smoke_at = parse_timestamp(args.production_smoke_completed_at, "production_smoke_completed_at")
    if args.candidate_run_id <= 0 or args.candidate_run_id > gitea.MAX_ID or args.candidate_artifact_id <= 0 or args.candidate_artifact_id > gitea.MAX_ID:
        raise QualificationError("candidate run and artifact IDs must be positive signed 64-bit integers")
    sha = args.checkout_sha
    candidate, manifest = fetch_candidate_record(
        args.gitea_api_url, args.repository, os.environ.get("GITEA_TOKEN", ""), args.candidate_run_id, args.candidate_artifact_id, sha
    )
    verify_smoke_run(
        args.production_smoke_proof,
        args.gitea_api_url,
        args.repository,
        os.environ.get("GITEA_TOKEN", ""),
        sha,
        deployed_at,
        smoke_at,
    )
    if manifest.get("source_revision") != sha or candidate.get("candidate_sha") != sha:
        raise QualificationError("checkout, candidate evidence, and manifest SHA do not match")
    if manifest.get("release_tag") != args.release_tag or candidate.get("release_tag") != args.release_tag:
        raise QualificationError("checkout tag, candidate evidence, and manifest release tag do not match")
    if "retirement_proposal" in candidate:
        raise QualificationError("prior release qualification requires candidate evidence without a retirement proposal")
    migrations = sorted((ROOT / "backend/internal/controlplane/migrations").glob("*.sql"))
    if not migrations or manifest.get("migration_head") != migrations[-1].name:
        raise QualificationError("manifest migration head is not the current repository migration")
    release_validator.validate(sha, candidate, manifest, now)

    restore_path = args.restore_evidence_dir / args.restore_evidence_basename
    if not args.restore_evidence_dir.is_absolute() or args.restore_evidence_dir.is_symlink():
        raise QualificationError("restore evidence directory must be an absolute non-symlink production path")
    if restore_path.is_symlink() or not restore_path.is_file():
        raise QualificationError("scheduled restore evidence must be a regular non-symlink file")
    directory_stat = args.restore_evidence_dir.stat()
    evidence_stat = restore_path.stat()
    if directory_stat.st_uid != os.getuid() or evidence_stat.st_uid != os.getuid() or directory_stat.st_mode & 0o077 or evidence_stat.st_mode & 0o077:
        raise QualificationError("scheduled restore evidence path must be owned by the runner and private")
    restore = load_json(restore_path, "scheduled restore evidence")
    try:
        restore_evidence.validate_evidence(restore)
    except (restore_evidence.ContractError, ValueError) as error:
        raise QualificationError(f"scheduled restore evidence is invalid: {error}") from error
    qualification = restore.get("qualification") or {}
    if (
        restore.get("result") != "success"
        or qualification.get("current_policy_qualified") is not True
        or not str(restore.get("attempt_id", "")).startswith("scheduled-")
    ):
        raise QualificationError("scheduled restore and backup proof must qualify the current production policy")
    restore_finished = parse_timestamp(restore["finished_at"], "restore evidence finished_at")
    if restore_finished > deployed_at + release_validator.CLOCK_TOLERANCE:
        raise QualificationError("scheduled backup/restore proof must precede deployment")
    if deployed_at - restore_finished > MAX_RESTORE_PROOF_AGE:
        raise QualificationError("scheduled backup/restore proof is stale for this deployment")
    provider_payload = restore.get("provider_payload") or {}
    migration_after = provider_payload.get("migration_after") or []
    if not migration_after or migration_after[-1] != manifest["migration_head"]:
        raise QualificationError("scheduled restore did not prove the deployed release migration head")

    report = fetch_report(args.control_url, os.environ.get("SWITCHYARD_CONTROL_READ_TOKEN", ""), now)
    if report.get("source_revision") != sha:
        raise QualificationError("compatibility report source revision does not match deployed SHA")
    if report.get("source_identity") != args.deployment_host:
        raise QualificationError("compatibility report source identity does not match deployment identity")
    report_start = datetime.strptime(report.get("window_start", ""), "%Y-%m-%d").replace(tzinfo=timezone.utc)
    if deployed_at > report_start or smoke_at > report_start:
        raise QualificationError("deployment and production smoke must precede the 90-day report window")
    if smoke_at + release_validator.CLOCK_TOLERANCE < deployed_at:
        raise QualificationError("production smoke predates deployment")

    evidence = dict(candidate)
    evidence["phase"] = "deployed"
    evidence["recorded_at"] = timestamp(now)
    evidence["gates"] = dict(candidate["gates"])
    evidence["gates"].update({
        "backup_restore": {"status": "passed", "completed_at": timestamp(restore_finished), "proof": f"scheduled-restore:{restore['attempt_id']}"},
        "production_smoke": {"status": "passed", "completed_at": timestamp(smoke_at), "proof": args.production_smoke_proof},
    })
    evidence["deployment"] = {"deployed_sha": sha, "deployed_at": timestamp(deployed_at), "host": args.deployment_host}
    release_validator.validate(sha, evidence, manifest, now)

    args.output.mkdir(parents=True)
    write_json(args.output / EVIDENCE_NAME, evidence)
    write_json(args.output / MANIFEST_NAME, manifest)
    write_json(args.output / REPORT_NAME, report)
    report_link = {
        "artifact": REPORT_NAME,
        "raw_sha256": digest(args.output / REPORT_NAME),
        "canonical_sha256": release_validator.canonical_sha256(report),
        "source_revision": report.get("source_revision"),
        "source_identity": report.get("source_identity"),
        "source_segment": report.get("source_segment"),
    }
    release_validator.validate_retirement_report(report_link, report, now)
    qualification_record = {
        "schema_version": "switchyard.compatibility-qualification/v1",
        "release_tag": args.release_tag,
        "release_sha": sha,
        "qualified_at": timestamp(now),
        "evidence_artifact": EVIDENCE_NAME,
        "evidence_raw_sha256": digest(args.output / EVIDENCE_NAME),
        "manifest_artifact": MANIFEST_NAME,
        "manifest_raw_sha256": digest(args.output / MANIFEST_NAME),
        "report_artifact": REPORT_NAME,
        "report_raw_sha256": digest(args.output / REPORT_NAME),
        "report_canonical_sha256": release_validator.canonical_sha256(report),
    }
    write_json(args.output / QUALIFICATION_NAME, qualification_record)
    validate_bundle(args.output, now)


def validate_bundle(root: Path, now: datetime) -> None:
    expected = {QUALIFICATION_NAME, EVIDENCE_NAME, MANIFEST_NAME, REPORT_NAME}
    if {path.name for path in root.iterdir() if path.is_file()} != expected or any(path.is_dir() for path in root.iterdir()):
        raise QualificationError("retirement qualification output must contain exactly four files")
    qualification = load_json(root / QUALIFICATION_NAME, "qualification")
    evidence = load_json(root / EVIDENCE_NAME, "deployed evidence")
    manifest = load_json(root / MANIFEST_NAME, "manifest")
    report = load_json(root / REPORT_NAME, "compatibility report")
    required = {
        "schema_version", "release_tag", "release_sha", "qualified_at",
        "evidence_artifact", "evidence_raw_sha256", "manifest_artifact",
        "manifest_raw_sha256", "report_artifact", "report_raw_sha256",
        "report_canonical_sha256",
    }
    if not isinstance(qualification, dict) or set(qualification) != required:
        raise QualificationError("qualification must contain the exact v1 fields")
    if qualification["schema_version"] != "switchyard.compatibility-qualification/v1":
        raise QualificationError("qualification schema version is invalid")
    for artifact_field, hash_field in (("evidence_artifact", "evidence_raw_sha256"), ("manifest_artifact", "manifest_raw_sha256"), ("report_artifact", "report_raw_sha256")):
        if qualification.get(artifact_field) not in expected or digest(root / qualification[artifact_field]) != qualification.get(hash_field):
            raise QualificationError(f"qualification {artifact_field} hash binding is invalid")
    expected_artifacts = {
        "evidence_artifact": EVIDENCE_NAME,
        "manifest_artifact": MANIFEST_NAME,
        "report_artifact": REPORT_NAME,
    }
    for field, name in expected_artifacts.items():
        if qualification[field] != name:
            raise QualificationError(f"qualification {field} must name {name}")
    if release_validator.canonical_sha256(report) != qualification.get("report_canonical_sha256"):
        raise QualificationError("qualification report canonical hash binding is invalid")
    release_validator.validate(qualification["release_sha"], evidence, manifest, now)
    if qualification["release_sha"] != evidence.get("candidate_sha") or qualification["release_tag"] != evidence.get("release_tag"):
        raise QualificationError("qualification release identity does not match deployed evidence")
    qualified_at = parse_timestamp(qualification["qualified_at"], "qualification qualified_at")
    if qualified_at > now + release_validator.CLOCK_TOLERANCE:
        raise QualificationError("qualification time is in the future")
    report_link = {
        "artifact": REPORT_NAME,
        "raw_sha256": qualification["report_raw_sha256"],
        "canonical_sha256": qualification["report_canonical_sha256"],
        "source_revision": report.get("source_revision"),
        "source_identity": report.get("source_identity"),
        "source_segment": report.get("source_segment"),
    }
    generated_at = release_validator.validate_retirement_report(report_link, report, qualified_at)
    if qualified_at < generated_at or qualified_at < parse_timestamp(evidence["recorded_at"], "evidence recorded_at"):
        raise QualificationError("qualification time predates its evidence or report")


def validate_smoke_proof(proof: str, api_url: str, repository: str) -> int:
    api = urllib.parse.urlsplit(api_url)
    value = urllib.parse.urlsplit(proof)
    expected_path = f"/{repository}/actions/runs/"
    if (
        value.scheme != "https"
        or value.netloc != api.netloc
        or value.username
        or value.password
        or value.query
        or value.fragment
        or not value.path.startswith(expected_path)
        or not value.path.removeprefix(expected_path).isdigit()
        or int(value.path.removeprefix(expected_path)) <= 0
    ):
        raise QualificationError("production smoke proof must be a credential-free same-repository HTTPS Actions run URL")
    return int(value.path.removeprefix(expected_path))


def digest(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def _reject_duplicates(pairs: list[tuple[str, Any]]) -> dict[str, Any]:
    result: dict[str, Any] = {}
    for key, value in pairs:
        if key in result:
            raise QualificationError(f"JSON contains duplicate key {key!r}")
        result[key] = value
    return result


def parser() -> argparse.ArgumentParser:
    result = argparse.ArgumentParser(description=__doc__)
    result.add_argument("--gitea-api-url", required=True)
    result.add_argument("--repository", required=True)
    result.add_argument("--candidate-run-id", required=True, type=int)
    result.add_argument("--candidate-artifact-id", required=True, type=int)
    result.add_argument("--checkout-sha", required=True)
    result.add_argument("--release-tag", required=True)
    result.add_argument("--deployed-at", required=True)
    result.add_argument("--deployment-host", required=True)
    result.add_argument("--production-smoke-completed-at", required=True)
    result.add_argument("--production-smoke-proof", required=True)
    result.add_argument("--restore-evidence-dir", required=True, type=Path)
    result.add_argument("--restore-evidence-basename", required=True)
    result.add_argument("--control-url", required=True)
    result.add_argument("--output", required=True, type=Path)
    return result


def main(argv: list[str] | None = None) -> int:
    try:
        produce(parser().parse_args(argv), datetime.now(timezone.utc))
    except (QualificationError, release_validator.EvidenceError, gitea.DownloadError, KeyError, TypeError, ValueError) as error:
        print(f"retirement qualification failed: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
