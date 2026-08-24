#!/usr/bin/env python3
"""Download one bounded compatibility report from a verified Gitea Actions run."""

from __future__ import annotations

import argparse
import io
import json
import os
import re
import stat
import sys
import urllib.parse
import zipfile
from pathlib import Path
from typing import Any

import download_retirement_qualifications as gitea


ARTIFACT_NAME = "compatibility-retirement-report"
REPORT_NAME = "compatibility-retirement-report.json"
MAX_REPORT_ARCHIVE_BYTES = 5 * 1024 * 1024
MAX_REPORT_BYTES = 4 * 1024 * 1024
GIT_SHA = re.compile(r"^[0-9a-f]{40}$")


class ReportDownloadError(ValueError):
    pass


def download_report(api_url: str, repository: str, token: str, run_id: int, artifact_id: int, output: Path) -> None:
    if any(isinstance(value, bool) or not isinstance(value, int) or value <= 0 or value > gitea.MAX_ID for value in (run_id, artifact_id)):
        raise ReportDownloadError("run and artifact IDs must be positive signed 64-bit integers")
    if not token:
        raise ReportDownloadError("Gitea token is required")
    if output.exists():
        raise ReportDownloadError("output path already exists")
    base_url, encoded_repo = gitea.validate_origin(api_url, repository)
    run = gitea._request_json(
        f"{base_url}/repos/{encoded_repo}/actions/runs/{run_id}", token, gitea.MAX_LIST_BYTES, f"Actions run {run_id}"
    )
    head_sha = run.get("head_sha") if isinstance(run, dict) else None
    if not isinstance(run, dict) or run.get("id") != run_id or not isinstance(head_sha, str) or not GIT_SHA.fullmatch(head_sha):
        raise ReportDownloadError("report Actions run is not a successful same-repository run")
    try:
        run = gitea._verify_run_provenance(
            base_url,
            encoded_repo,
            token,
            run_id,
            gitea.RETIREMENT_WORKFLOW_ID,
            gitea.RETIREMENT_JOB_NAME,
            head_sha,
        )
    except gitea.DownloadError as error:
        raise ReportDownloadError(str(error)) from error
    list_url = f"{base_url}/repos/{encoded_repo}/actions/runs/{run_id}/artifacts?limit=100&page=1"
    listing = gitea._request_json(list_url, token, gitea.MAX_LIST_BYTES, f"artifacts for run {run_id}")
    rows = listing.get("artifacts") if isinstance(listing, dict) else None
    total = listing.get("total_count", len(rows) if isinstance(rows, list) else -1) if isinstance(listing, dict) else -1
    if not isinstance(rows, list) or isinstance(total, bool) or not isinstance(total, int) or total != len(rows) or total > 100:
        raise ReportDownloadError("Gitea returned an unsafe report artifact list")
    matches = [item for item in rows if isinstance(item, dict) and item.get("id") == artifact_id]
    if len(matches) != 1:
        raise ReportDownloadError("report artifact must occur exactly once in its run")
    artifact = matches[0]
    workflow_run = artifact.get("workflow_run")
    if (
        artifact.get("name") != ARTIFACT_NAME
        or artifact.get("expired") is not False
        or not isinstance(workflow_run, dict)
        or workflow_run.get("id") != run_id
        or workflow_run.get("head_sha") != run.get("head_sha")
    ):
        raise ReportDownloadError("report artifact name, expiry, run, or revision provenance is invalid")
    download_url = f"{base_url}/repos/{encoded_repo}/actions/artifacts/{artifact_id}/zip"
    archive = gitea._request_bytes(
        download_url,
        token,
        MAX_REPORT_ARCHIVE_BYTES,
        f"report artifact {artifact_id}",
        allowed_redirect_path=urllib.parse.urlsplit(download_url).path + "/raw",
    )
    payload = _extract_report(archive)
    try:
        report = json.loads(payload.decode("utf-8"), object_pairs_hook=gitea._reject_duplicate_keys)
    except (UnicodeDecodeError, json.JSONDecodeError, gitea.DownloadError) as error:
        raise ReportDownloadError("report artifact does not contain valid unique-key JSON") from error
    if not isinstance(report, dict) or report.get("source_revision") != run.get("head_sha"):
        raise ReportDownloadError("report source_revision does not match its verified Actions run")
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_bytes(payload)


def _extract_report(archive: bytes) -> bytes:
    try:
        with zipfile.ZipFile(io.BytesIO(archive)) as bundle:
            members = bundle.infolist()
            if len(members) != 1 or members[0].filename != REPORT_NAME or members[0].is_dir():
                raise ReportDownloadError(f"report artifact must contain only {REPORT_NAME}")
            member = members[0]
            file_type = stat.S_IFMT(member.external_attr >> 16)
            if file_type not in (0, stat.S_IFREG) or member.file_size > MAX_REPORT_BYTES:
                raise ReportDownloadError("report artifact member is unsafe or exceeds the byte limit")
            with bundle.open(member) as source:
                payload = source.read(MAX_REPORT_BYTES + 1)
    except zipfile.BadZipFile as error:
        raise ReportDownloadError("report artifact is not a valid ZIP") from error
    if len(payload) > MAX_REPORT_BYTES or len(payload) != member.file_size:
        raise ReportDownloadError("report artifact exceeds its declared size or byte limit")
    return payload


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--api-url", required=True)
    parser.add_argument("--repository", required=True)
    parser.add_argument("--run-id", required=True, type=int)
    parser.add_argument("--artifact-id", required=True, type=int)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    try:
        download_report(args.api_url, args.repository, os.environ.get("GITEA_TOKEN", ""), args.run_id, args.artifact_id, args.output)
    except (ReportDownloadError, gitea.DownloadError) as error:
        print(f"retirement report download failed: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
