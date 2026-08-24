#!/usr/bin/env python3

import contextlib
import io
import json
import os
import stat
import tempfile
import threading
import unittest
import zipfile
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from unittest import mock
from urllib.parse import urlsplit

import download_retirement_qualifications as downloader


TOKEN = "secret-gitea-token"
REFERENCES = json.dumps([
    {"run_id": 101, "artifact_id": 201},
    {"run_id": 102, "artifact_id": 202},
])


def qualification() -> dict:
    return {
        "schema_version": "switchyard.compatibility-qualification/v1",
        "release_tag": "v1.2.3",
        "release_sha": "a" * 40,
        "qualified_at": "2026-08-22T12:00:00Z",
        "evidence_artifact": "deployed-evidence.json",
        "evidence_raw_sha256": "b" * 64,
        "manifest_artifact": "manifest.json",
        "manifest_raw_sha256": "c" * 64,
        "report_artifact": "compatibility-retirement-report.json",
        "report_raw_sha256": "d" * 64,
        "report_canonical_sha256": "e" * 64,
    }


def archive(mutations=None, extra=None) -> bytes:
    files = {
        downloader.QUALIFICATION_NAME: json.dumps(qualification()).encode(),
        "deployed-evidence.json": b"{}",
        "manifest.json": b"{}",
        "compatibility-retirement-report.json": b"{}",
    }
    if mutations:
        mutations(files)
    stream = io.BytesIO()
    with zipfile.ZipFile(stream, "w", zipfile.ZIP_DEFLATED) as bundle:
        for name, payload in files.items():
            bundle.writestr(name, payload)
        if extra:
            extra(bundle)
    return stream.getvalue()


class FakeGitea:
    def __init__(self) -> None:
        self.lists = {
            101: [self.artifact(201, 101)],
            102: [self.artifact(202, 102)],
        }
        self.runs = {101: self.run(101), 102: self.run(102)}
        self.jobs = {101: [self.job(101)], 102: [self.job(102)]}
        self.archives = {201: archive(), 202: archive()}
        self.redirects = {201}
        self.requests = []
        self.failure_status: int | None = None
        owner = self

        class Handler(BaseHTTPRequestHandler):
            def do_GET(self):
                owner.requests.append((self.path, self.headers.get("Authorization")))
                if owner.failure_status is not None:
                    self.send_error(owner.failure_status, TOKEN)
                    return
                path = urlsplit(self.path).path
                parts = path.split("/")
                if "/actions/runs/" in path and path.endswith("/artifacts"):
                    run_id = int(parts[-2])
                    artifacts = owner.lists.get(run_id, [])
                    self.send_json({"total_count": len(artifacts), "artifacts": artifacts})
                    return
                if "/actions/runs/" in path and path.endswith("/jobs"):
                    run_id = int(parts[-2])
                    jobs = owner.jobs.get(run_id, [])
                    self.send_json({"total_count": len(jobs), "jobs": jobs})
                    return
                if "/actions/runs/" in path:
                    run_id = int(parts[-1])
                    run = dict(owner.runs.get(run_id, {}))
                    if run:
                        run["url"] = f"{owner.api_url.removesuffix('/api/v1')}/owner/repo/actions/runs/{run_id}"
                        self.send_json(run)
                    else:
                        self.send_error(404)
                    return
                if "/actions/artifacts/" in path and path.endswith("/zip"):
                    artifact_id = int(parts[-2])
                    payload = owner.archives.get(artifact_id)
                    if payload is None:
                        self.send_error(404, TOKEN)
                        return
                    if artifact_id in owner.redirects:
                        self.send_response(302)
                        self.send_header("Location", f"{path}/raw?sig=fake&expires=9999999999")
                        self.end_headers()
                        return
                    self.send_archive(payload)
                    return
                if "/actions/artifacts/" in path and path.endswith("/zip/raw"):
                    artifact_id = int(parts[-3])
                    payload = owner.archives.get(artifact_id)
                    if payload is None:
                        self.send_error(404, TOKEN)
                        return
                    self.send_archive(payload)
                    return
                self.send_error(404, TOKEN)

            def send_archive(self, payload):
                self.send_response(200)
                self.send_header("Content-Length", str(len(payload)))
                self.end_headers()
                self.wfile.write(payload)

            def send_json(self, value):
                payload = json.dumps(value).encode()
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(payload)))
                self.end_headers()
                self.wfile.write(payload)

            def log_message(self, format, *args):
                pass

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)

    @staticmethod
    def artifact(artifact_id, run_id, expired=False):
        return {
            "id": artifact_id,
            "name": "retirement-release-qualification-v1.2.3",
            "expired": expired,
            "workflow_run": {"id": run_id, "repository_id": 9, "head_sha": "a" * 40},
        }

    @staticmethod
    def run(run_id):
        return {
            "id": run_id,
            "workflow_id": downloader.RETIREMENT_WORKFLOW_ID,
            "head_sha": "a" * 40,
            "status": "success",
        }

    @staticmethod
    def job(run_id):
        return {
            "id": run_id + 1000,
            "run_id": run_id,
            "name": downloader.RETIREMENT_JOB_NAME,
            "head_sha": "a" * 40,
            "status": "completed",
            "conclusion": "success",
            "labels": [downloader.PRODUCTION_RUNNER_LABEL],
            "runner_name": "production-1",
        }

    def __enter__(self):
        self.thread.start()
        return self

    def __exit__(self, *args):
        self.server.shutdown()
        self.thread.join()
        self.server.server_close()

    @property
    def api_url(self):
        return f"http://127.0.0.1:{self.server.server_port}/api/v1"


class DownloadRetirementQualificationsTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.output = Path(self.temporary.name) / "prior-qualifying-releases"

    def tearDown(self):
        self.temporary.cleanup()

    def download(self, server, references=REFERENCES):
        downloader.download_bundles(references, server.api_url, "owner/repo", TOKEN, self.output)

    def test_downloads_exact_bundles_with_authenticated_same_repo_requests(self):
        with FakeGitea() as server:
            self.download(server)
        for index in (0, 1):
            self.assertEqual(
                {path.name for path in (self.output / str(index)).iterdir()},
                {
                    downloader.QUALIFICATION_NAME,
                    "deployed-evidence.json",
                    "manifest.json",
                    "compatibility-retirement-report.json",
                },
            )
        self.assertEqual({auth for _, auth in server.requests}, {f"token {TOKEN}"})
        self.assertTrue(all(path.startswith("/api/v1/repos/owner/repo/actions/") for path, _ in server.requests))

    def test_rejects_renamed_bundle_component(self):
        def rename(files):
            record = qualification()
            record["evidence_artifact"] = "other-evidence.json"
            files[downloader.QUALIFICATION_NAME] = json.dumps(record).encode()
            files["other-evidence.json"] = files.pop("deployed-evidence.json")

        with FakeGitea() as server:
            server.archives[201] = archive(rename)
            with self.assertRaisesRegex(downloader.DownloadError, "must name deployed-evidence.json"):
                self.download(server)
        self.assertFalse(self.output.exists())

    def test_validates_all_references_before_network(self):
        invalid = (
            "[]",
            json.dumps([{"run_id": 1, "artifact_id": 2}, {"run_id": 3, "artifact_id": 2}]),
            json.dumps([{"run_id": 0, "artifact_id": 2}, {"run_id": 3, "artifact_id": 4}]),
            json.dumps([{"run_id": True, "artifact_id": 2}, {"run_id": 3, "artifact_id": 4}]),
            json.dumps([{"run_id": 1, "artifact_id": 2, "bytes": "x"}, {"run_id": 3, "artifact_id": 4}]),
        )
        with FakeGitea() as server:
            for payload in invalid:
                with self.subTest(payload=payload), self.assertRaises(downloader.DownloadError):
                    self.download(server, payload)
            self.assertEqual(server.requests, [])

    def test_rejects_zero_multiple_mismatched_and_expired_artifacts(self):
        cases = {
            "zero": [],
            "multiple": [FakeGitea.artifact(201, 101), FakeGitea.artifact(201, 101)],
            "mismatched": [FakeGitea.artifact(201, 999)],
            "expired": [FakeGitea.artifact(201, 101, expired=True)],
        }
        for name, artifacts in cases.items():
            with self.subTest(name=name), FakeGitea() as server:
                server.lists[101] = artifacts
                with self.assertRaises(downloader.DownloadError):
                    self.download(server)
                self.assertFalse(self.output.exists())

    def test_rejects_wrong_workflow_status_head_job_runner_and_artifact(self):
        cases = {
            "workflow": lambda server: server.runs[101].update(workflow_id=".gitea/workflows/verify-production.yml"),
            "status": lambda server: server.runs[101].update(status="failure"),
            "head": lambda server: server.runs[101].update(head_sha="b" * 40),
            "job": lambda server: server.jobs[101][0].update(name="other"),
            "runner": lambda server: server.jobs[101][0].update(labels=["ubuntu-latest"]),
            "artifact": lambda server: server.lists[101][0].update(name="other"),
        }
        for name, mutate in cases.items():
            with self.subTest(name=name), FakeGitea() as server:
                mutate(server)
                with self.assertRaises(downloader.DownloadError):
                    self.download(server)
                self.assertFalse(self.output.exists())

    def test_auth_failure_redacts_token(self):
        with FakeGitea() as server:
            server.failure_status = 401
            stderr = io.StringIO()
            with mock.patch.dict(os.environ, {"GITEA_TOKEN": TOKEN}), contextlib.redirect_stderr(stderr):
                status = downloader.main([
                    "--references", REFERENCES,
                    "--api-url", server.api_url,
                    "--repository", "owner/repo",
                    "--output", str(self.output),
                ])
        self.assertEqual(status, 1)
        self.assertNotIn(TOKEN, stderr.getvalue())

    def test_rejects_traversal_symlink_duplicate_and_missing_bundle(self):
        def symlink(bundle):
            item = zipfile.ZipInfo("link.json")
            item.create_system = 3
            item.external_attr = (stat.S_IFLNK | 0o777) << 16
            bundle.writestr(item, "manifest.json")

        def duplicate(bundle):
            with self.assertWarns(UserWarning):
                bundle.writestr("manifest.json", b"{}")

        cases = {
            "traversal": archive(extra=lambda bundle: bundle.writestr("../escape.json", b"{}")),
            "symlink": archive(extra=symlink),
            "duplicate": archive(extra=duplicate),
            "missing": archive(mutations=lambda files: files.pop(downloader.QUALIFICATION_NAME)),
        }
        for name, payload in cases.items():
            with self.subTest(name=name), FakeGitea() as server:
                server.archives[201] = payload
                with self.assertRaises(downloader.DownloadError):
                    self.download(server)
                self.assertFalse(self.output.exists())

    def test_rejects_zip_bomb_by_uncompressed_member_limit(self):
        with FakeGitea() as server, mock.patch.object(downloader, "MAX_MEMBER_BYTES", 8):
            with self.assertRaisesRegex(downloader.DownloadError, "byte limit"):
                self.download(server)
        self.assertFalse(self.output.exists())


if __name__ == "__main__":
    unittest.main()
