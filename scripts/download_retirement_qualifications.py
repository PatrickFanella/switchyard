#!/usr/bin/env python3
"""Download two prior retirement qualification bundles from the same Gitea repo."""

from __future__ import annotations

import argparse
import io
import json
import os
import shutil
import stat
import sys
import tempfile
import urllib.error
import urllib.parse
import urllib.request
import zipfile
from pathlib import Path
from typing import Any


QUALIFICATION_NAME = "retirement-release-qualification.json"
EVIDENCE_NAME = "deployed-evidence.json"
MANIFEST_NAME = "manifest.json"
REPORT_NAME = "compatibility-retirement-report.json"
MAX_LIST_BYTES = 1024 * 1024
MAX_ARCHIVE_BYTES = 16 * 1024 * 1024
MAX_MEMBER_BYTES = 16 * 1024 * 1024
MAX_TOTAL_BYTES = 32 * 1024 * 1024
MAX_MEMBERS = 8
MAX_ARTIFACTS_PER_RUN = 100
MAX_ID = (1 << 63) - 1
RETIREMENT_WORKFLOW_ID = "qualify-retirement-release.yml"
RETIREMENT_JOB_NAME = "qualify"
PRODUCTION_RUNNER_LABEL = "switchyard-production"


class DownloadError(Exception):
    pass


def parse_references(payload: str) -> list[dict[str, int]]:
    try:
        value = json.loads(payload, object_pairs_hook=_reject_duplicate_keys)
    except json.JSONDecodeError as error:
        raise DownloadError(f"retirement prior release references are invalid JSON: {error.msg}") from None
    if not isinstance(value, list) or len(value) != 2:
        raise DownloadError("retirement prior release references must contain exactly two records")
    references: list[dict[str, int]] = []
    for index, record in enumerate(value):
        if not isinstance(record, dict) or set(record) != {"run_id", "artifact_id"}:
            raise DownloadError(f"reference {index} must contain only run_id and artifact_id")
        for field in ("run_id", "artifact_id"):
            item = record[field]
            if isinstance(item, bool) or not isinstance(item, int) or item <= 0 or item > MAX_ID:
                raise DownloadError(f"reference {index} {field} must be a positive signed 64-bit integer")
        references.append({"run_id": record["run_id"], "artifact_id": record["artifact_id"]})
    if len({item["artifact_id"] for item in references}) != 2:
        raise DownloadError("artifact_id values must be distinct")
    return references


def validate_origin(api_url: str, repository: str) -> tuple[str, str]:
    parsed = urllib.parse.urlsplit(api_url)
    if parsed.scheme not in {"http", "https"} or not parsed.netloc:
        raise DownloadError("Gitea API URL must be an absolute HTTP(S) URL")
    if parsed.username or parsed.password or parsed.query or parsed.fragment:
        raise DownloadError("Gitea API URL must not contain credentials, a query, or a fragment")
    if not parsed.path.rstrip("/").endswith("/api/v1"):
        raise DownloadError("Gitea API URL must end in /api/v1")
    segments = repository.split("/")
    if len(segments) != 2 or any(not _safe_repo_segment(segment) for segment in segments):
        raise DownloadError("Gitea repository must be an owner/name pair")
    return api_url.rstrip("/"), "/".join(urllib.parse.quote(segment, safe="._-") for segment in segments)


def download_bundles(
    references_payload: str,
    api_url: str,
    repository: str,
    token: str,
    output: Path,
) -> None:
    references = parse_references(references_payload)
    base_url, encoded_repo = validate_origin(api_url, repository)
    if not token:
        raise DownloadError("Gitea token is required")
    if output.exists():
        raise DownloadError("output path already exists")

    output.parent.mkdir(parents=True, exist_ok=True)
    staging = Path(tempfile.mkdtemp(prefix=f".{output.name}.", dir=output.parent))
    try:
        for index, reference in enumerate(references):
            archive, artifact = _download_one(base_url, encoded_repo, token, reference)
            qualification = _extract_bundle(archive, staging / str(index))
            release_sha = qualification.get("release_sha")
            release_tag = qualification.get("release_tag")
            if not isinstance(release_sha, str) or not isinstance(release_tag, str):
                raise DownloadError(f"{QUALIFICATION_NAME} has an invalid release identity")
            expected_name = f"retirement-release-qualification-{release_tag}"
            if artifact.get("name") != expected_name:
                raise DownloadError(f"artifact {reference['artifact_id']} is not named {expected_name}")
            if artifact.get("workflow_run", {}).get("head_sha") != release_sha:
                raise DownloadError(f"artifact {reference['artifact_id']} does not match release SHA {release_sha}")
            _verify_run_provenance(
                base_url,
                encoded_repo,
                token,
                reference["run_id"],
                RETIREMENT_WORKFLOW_ID,
                RETIREMENT_JOB_NAME,
                release_sha,
            )
        os.replace(staging, output)
    except Exception:
        shutil.rmtree(staging, ignore_errors=True)
        raise


def _download_one(base_url: str, repository: str, token: str, reference: dict[str, int]) -> tuple[bytes, dict[str, Any]]:
    run_id = reference["run_id"]
    artifact_id = reference["artifact_id"]
    list_url = f"{base_url}/repos/{repository}/actions/runs/{run_id}/artifacts?limit={MAX_ARTIFACTS_PER_RUN}&page=1"
    listing = _request_json(list_url, token, MAX_LIST_BYTES, f"artifacts for run {run_id}")
    if not isinstance(listing, dict) or not isinstance(listing.get("artifacts"), list):
        raise DownloadError(f"Gitea returned an invalid artifact list for run {run_id}")
    artifacts = listing["artifacts"]
    total_count = listing.get("total_count", len(artifacts))
    if isinstance(total_count, bool) or not isinstance(total_count, int) or total_count < len(artifacts):
        raise DownloadError(f"Gitea returned an invalid artifact count for run {run_id}")
    if total_count > MAX_ARTIFACTS_PER_RUN:
        raise DownloadError(f"run {run_id} has too many artifacts to verify safely")
    matches = [artifact for artifact in artifacts if isinstance(artifact, dict) and artifact.get("id") == artifact_id]
    if len(matches) != 1:
        raise DownloadError(f"artifact {artifact_id} must occur exactly once in run {run_id}")
    artifact = matches[0]
    workflow_run = artifact.get("workflow_run")
    if not isinstance(workflow_run, dict) or workflow_run.get("id") != run_id:
        raise DownloadError(f"artifact {artifact_id} does not belong to run {run_id}")
    if artifact.get("expired") is not False:
        raise DownloadError(f"artifact {artifact_id} is expired")
    download_url = f"{base_url}/repos/{repository}/actions/artifacts/{artifact_id}/zip"
    archive = _request_bytes(
        download_url,
        token,
        MAX_ARCHIVE_BYTES,
        f"artifact {artifact_id}",
        allowed_redirect_path=urllib.parse.urlsplit(download_url).path + "/raw",
    )
    return archive, artifact


def _verify_run_provenance(
    base_url: str,
    repository: str,
    token: str,
    run_id: int,
    workflow_id: str,
    job_name: str,
    head_sha: str,
) -> dict[str, Any]:
    run = _request_json(
        f"{base_url}/repos/{repository}/actions/runs/{run_id}",
        token,
        MAX_LIST_BYTES,
        f"Actions run {run_id}",
    )
    if (
        not isinstance(run, dict)
        or run.get("id") != run_id
        or run.get("workflow_id") != workflow_id
        or run.get("head_sha") != head_sha
        or run.get("status") != "success"
        or not _is_run_url(run.get("url"), base_url, repository, run_id)
    ):
        raise DownloadError(f"Actions run {run_id} does not prove successful {workflow_id} execution for {head_sha}")
    jobs = _request_json(
        f"{base_url}/repos/{repository}/actions/runs/{run_id}/jobs?limit=100&page=1",
        token,
        MAX_LIST_BYTES,
        f"jobs for Actions run {run_id}",
    )
    rows = jobs.get("jobs") if isinstance(jobs, dict) else None
    total = jobs.get("total_count", len(rows) if isinstance(rows, list) else -1) if isinstance(jobs, dict) else -1
    if (
        not isinstance(rows, list)
        or isinstance(total, bool)
        or not isinstance(total, int)
        or total != len(rows)
        or total > 100
    ):
        raise DownloadError(f"Gitea returned an invalid job list for run {run_id}")
    matching = [
        job
        for job in rows
        if isinstance(job, dict)
        and job.get("run_id") == run_id
        and job.get("name") == job_name
        and job.get("head_sha") == head_sha
        and job.get("status") == "completed"
        and job.get("conclusion") == "success"
        and isinstance(job.get("labels"), list)
        and PRODUCTION_RUNNER_LABEL in job["labels"]
    ]
    if len(matching) != 1:
        raise DownloadError(f"Actions run {run_id} requires one successful {job_name} job on {PRODUCTION_RUNNER_LABEL}")
    return run


def _is_run_url(value: Any, base_url: str, repository: str, run_id: int) -> bool:
    if not isinstance(value, str):
        return False
    api = urllib.parse.urlsplit(base_url)
    run = urllib.parse.urlsplit(value)
    web_root = api.path.removesuffix("/api/v1").rstrip("/")
    run_prefix = f"{web_root}/{repository}/actions/runs/"
    run_number = run.path.removeprefix(run_prefix)
    return (
        run.scheme == api.scheme
        and run.netloc == api.netloc
        and not run.username
        and not run.password
        and not run.query
        and not run.fragment
        and run.path.startswith(run_prefix)
        and run_number.isdigit()
        and int(run_number) > 0
    )


def _request_json(url: str, token: str, limit: int, label: str) -> Any:
    payload = _request_bytes(url, token, limit, label)
    try:
        return json.loads(payload.decode("utf-8"), object_pairs_hook=_reject_duplicate_keys)
    except (UnicodeDecodeError, json.JSONDecodeError) as error:
        raise DownloadError(f"Gitea returned invalid JSON for {label}") from None


def _request_bytes(
    url: str,
    token: str,
    limit: int,
    label: str,
    allowed_redirect_path: str | None = None,
) -> bytes:
    request = urllib.request.Request(
        url,
        headers={"Accept": "application/json", "Authorization": f"token {token}"},
    )
    opener = urllib.request.build_opener(_SameOriginRedirects(url, allowed_redirect_path))
    try:
        with opener.open(request, timeout=30) as response:
            length = response.headers.get("Content-Length")
            if length is not None:
                try:
                    declared = int(length)
                except ValueError:
                    raise DownloadError(f"Gitea returned an invalid Content-Length for {label}") from None
                if declared < 0 or declared > limit:
                    raise DownloadError(f"Gitea response for {label} exceeds the byte limit")
            payload = response.read(limit + 1)
    except DownloadError:
        raise
    except urllib.error.HTTPError as error:
        status = error.code
        error.close()
        raise DownloadError(f"Gitea request for {label} failed with HTTP {status}") from None
    except (urllib.error.URLError, TimeoutError, OSError):
        raise DownloadError(f"Gitea request for {label} failed") from None
    if len(payload) > limit:
        raise DownloadError(f"Gitea response for {label} exceeds the byte limit")
    return payload


class _SameOriginRedirects(urllib.request.HTTPRedirectHandler):
    def __init__(self, original_url: str, allowed_path: str | None) -> None:
        self.original = urllib.parse.urlsplit(original_url)
        self.allowed_path = allowed_path

    def redirect_request(self, req: Any, fp: Any, code: int, msg: str, headers: Any, newurl: str) -> Any:
        target = urllib.parse.urlsplit(newurl)
        try:
            query = urllib.parse.parse_qs(target.query, strict_parsing=True)
        except ValueError:
            return None
        if (
            self.allowed_path is None
            or urllib.parse.urlsplit(req.full_url).path != self.original.path
            or target.scheme != self.original.scheme
            or target.netloc != self.original.netloc
            or target.path != self.allowed_path
            or target.fragment
            or set(query) != {"sig", "expires"}
            or any(len(values) != 1 or not values[0] for values in query.values())
        ):
            return None
        return super().redirect_request(req, fp, code, msg, headers, newurl)


def _extract_bundle(archive: bytes, destination: Path) -> dict[str, Any]:
    try:
        bundle = zipfile.ZipFile(io.BytesIO(archive))
    except zipfile.BadZipFile:
        raise DownloadError("downloaded artifact is not a valid ZIP archive") from None
    with bundle:
        members = bundle.infolist()
        if len(members) > MAX_MEMBERS:
            raise DownloadError("retirement qualification archive has too many members")
        names: set[str] = set()
        total = 0
        for member in members:
            name = member.filename
            if not _safe_member_name(name) or member.is_dir():
                raise DownloadError(f"unsafe ZIP member {name!r}")
            if name in names:
                raise DownloadError(f"duplicate ZIP member {name!r}")
            names.add(name)
            file_type = stat.S_IFMT(member.external_attr >> 16)
            if file_type not in (0, stat.S_IFREG):
                raise DownloadError(f"ZIP member {name!r} is not a regular file")
            if member.file_size > MAX_MEMBER_BYTES:
                raise DownloadError(f"ZIP member {name!r} exceeds the byte limit")
            total += member.file_size
            if total > MAX_TOTAL_BYTES:
                raise DownloadError("retirement qualification archive exceeds the total byte limit")
        if QUALIFICATION_NAME not in names:
            raise DownloadError(f"retirement qualification archive is missing {QUALIFICATION_NAME}")

        qualification_payload = _read_member(bundle, bundle.getinfo(QUALIFICATION_NAME))
        qualification = _parse_qualification(qualification_payload)
        expected = {QUALIFICATION_NAME, EVIDENCE_NAME, MANIFEST_NAME, REPORT_NAME}
        if names != expected:
            raise DownloadError("retirement qualification archive does not contain the exact expected bundle files")

        destination.mkdir(parents=True)
        for member in members:
            (destination / member.filename).write_bytes(_read_member(bundle, member))
        return qualification


def _read_member(bundle: zipfile.ZipFile, member: zipfile.ZipInfo) -> bytes:
    try:
        with bundle.open(member) as source:
            payload = source.read(MAX_MEMBER_BYTES + 1)
    except (zipfile.BadZipFile, RuntimeError, NotImplementedError):
        raise DownloadError(f"ZIP member {member.filename!r} is corrupt") from None
    if len(payload) > MAX_MEMBER_BYTES or len(payload) != member.file_size:
        raise DownloadError(f"ZIP member {member.filename!r} exceeds its declared size or byte limit")
    return payload


def _parse_qualification(payload: bytes) -> dict[str, Any]:
    try:
        value = json.loads(payload.decode("utf-8"), object_pairs_hook=_reject_duplicate_keys)
    except (UnicodeDecodeError, json.JSONDecodeError):
        raise DownloadError(f"{QUALIFICATION_NAME} is not valid JSON") from None
    if not isinstance(value, dict):
        raise DownloadError(f"{QUALIFICATION_NAME} must contain a JSON object")
    expected = {
        "evidence_artifact": EVIDENCE_NAME,
        "manifest_artifact": MANIFEST_NAME,
        "report_artifact": REPORT_NAME,
    }
    for field, name in expected.items():
        if value.get(field) != name:
            raise DownloadError(f"{QUALIFICATION_NAME} must name {name} as {field}")
    return value


def _safe_member_name(value: Any) -> bool:
    return (
        isinstance(value, str)
        and value.endswith(".json")
        and not value.startswith(".")
        and "/" not in value
        and "\\" not in value
        and Path(value).name == value
    )


def _safe_repo_segment(value: str) -> bool:
    return bool(value) and value not in {".", ".."} and all(character.isalnum() or character in "._-" for character in value)


def _reject_duplicate_keys(pairs: list[tuple[str, Any]]) -> dict[str, Any]:
    result: dict[str, Any] = {}
    for key, value in pairs:
        if key in result:
            raise DownloadError(f"JSON contains duplicate key {key!r}")
        result[key] = value
    return result


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--references", required=True)
    parser.add_argument("--api-url", required=True)
    parser.add_argument("--repository", required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args(argv)
    try:
        download_bundles(
            args.references,
            args.api_url,
            args.repository,
            os.environ.get("GITEA_TOKEN", ""),
            args.output,
        )
    except DownloadError as error:
        print(f"retirement qualification download failed: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
