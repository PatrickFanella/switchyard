#!/usr/bin/env python3

import io
import json
import tempfile
import unittest
import zipfile
from pathlib import Path
from unittest import mock

import download_retirement_report as report_downloader


SHA = "a" * 40


def archive(payload: bytes | None = None) -> bytes:
    stream = io.BytesIO()
    with zipfile.ZipFile(stream, "w", zipfile.ZIP_DEFLATED) as bundle:
        bundle.writestr(report_downloader.REPORT_NAME, payload or json.dumps({"source_revision": SHA}).encode())
    return stream.getvalue()


class DownloadRetirementReportTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.output = Path(self.temp.name) / report_downloader.REPORT_NAME
        self.run_fixture = {
            "id": 10,
            "workflow_id": report_downloader.gitea.RETIREMENT_WORKFLOW_ID,
            "head_sha": SHA,
            "status": "success",
            "url": "https://gitea.internal/owner/repo/actions/runs/10",
        }
        self.artifact = {
            "id": 20,
            "name": report_downloader.ARTIFACT_NAME,
            "expired": False,
            "workflow_run": {"id": 10, "repository_id": 7, "head_sha": SHA},
        }
        self.job = {
            "run_id": 10,
            "name": report_downloader.gitea.RETIREMENT_JOB_NAME,
            "head_sha": SHA,
            "status": "completed",
            "conclusion": "success",
            "labels": [report_downloader.gitea.PRODUCTION_RUNNER_LABEL],
        }

    def download(self) -> None:
        with mock.patch.object(report_downloader.gitea, "_request_json", side_effect=[self.run_fixture, self.run_fixture, {"total_count": 1, "jobs": [self.job]}, {"total_count": 1, "artifacts": [self.artifact]}]), \
             mock.patch.object(report_downloader.gitea, "_request_bytes", return_value=archive()):
            report_downloader.download_report("https://gitea.internal/api/v1", "owner/repo", "secret", 10, 20, self.output)

    def test_downloads_exact_report_from_real_gitea_124_shape(self) -> None:
        self.download()
        self.assertEqual(json.loads(self.output.read_text())["source_revision"], SHA)

    def test_rejects_wrong_status_head_artifact_expiry_or_membership(self) -> None:
        cases = {
            "status": lambda: self.run_fixture.update(status="failure"),
            "head": lambda: self.run_fixture.update(head_sha="b" * 40),
            "workflow": lambda: self.run_fixture.update(workflow_id="other.yml"),
            "runner": lambda: self.job.update(labels=["ubuntu-latest"]),
            "name": lambda: self.artifact.update(name="other"),
            "expired": lambda: self.artifact.update(expired=True),
            "membership": lambda: self.artifact["workflow_run"].update(id=11),
        }
        for name, mutate in cases.items():
            with self.subTest(name=name):
                self.setUp()
                mutate()
                with self.assertRaises(report_downloader.ReportDownloadError):
                    self.download()
                self.assertFalse(self.output.exists())

    def test_rejects_report_over_size_limit(self) -> None:
        with mock.patch.object(report_downloader, "MAX_REPORT_BYTES", 8), self.assertRaisesRegex(report_downloader.ReportDownloadError, "byte limit"):
            report_downloader._extract_report(archive(b"{" + b" " * 20 + b"}"))


if __name__ == "__main__":
    unittest.main()
