#!/usr/bin/env python3

import argparse
import copy
import io
import json
import tempfile
import unittest
from datetime import datetime, timedelta, timezone
from pathlib import Path
from unittest import mock
import zipfile

import produce_retirement_qualification as producer
import validate_release_evidence as validator


SHA = "1" * 40
NOW = datetime(2026, 11, 23, 12, 0, tzinfo=timezone.utc)


def gate(at: str = "2026-08-20T10:00:00Z") -> dict:
    return {"status": "passed", "completed_at": at, "proof": "https://gitea.internal/owner/switchyard/actions/runs/100"}


def candidate_record() -> tuple[dict, dict]:
    migration = sorted((producer.ROOT / "backend/internal/controlplane/migrations").glob("*.sql"))[-1].name
    evidence = {
        "schema_version": 1,
        "phase": "candidate",
        "candidate_sha": SHA,
        "release_tag": "v1.2.3",
        "pull_request": "https://gitea.internal/switchyard/pulls/42",
        "ci_run": "https://gitea.internal/owner/switchyard/actions/runs/100",
        "migration_head": migration,
        "rollback_target": "v1.2.2",
        "exceptions": [],
        "recorded_at": "2026-08-20T11:00:00Z",
        "gates": {"verify_production": gate(), "extended_soak": gate()},
        "artifacts": [{"archive": "switchyard-api.oci.tar", "archive_sha256": "a" * 64}],
    }
    manifest = {
        "source_revision": SHA,
        "release_tag": "v1.2.3",
        "ci_run_url": evidence["ci_run"],
        "migration_head": migration,
        "images": [{"archive": "switchyard-api.oci.tar", "archive_sha256": "a" * 64, "oci_index_digest": "sha256:" + "b" * 64}],
    }
    return evidence, manifest


def report() -> dict:
    start = datetime(2026, 8, 24, tzinfo=timezone.utc)
    observations = []
    for offset in range(90):
        day = start + timedelta(days=offset)
        for backend in ("llama-line", "ocq"):
            observations.append({
                "date": day.strftime("%Y-%m-%d"),
                "backend": backend,
                "attempts": 0,
                "source_revision": SHA,
                "deployment_id": "production-kvant",
                "observation_started_at": day.strftime("%Y-%m-%dT00:00:00Z"),
                "observation_completed_at": (day + timedelta(days=1)).strftime("%Y-%m-%dT00:00:00Z"),
                "observation_segment": "segment-a",
            })
    value = {
        "version": "switchyard.compatibility-observation/v1",
        "source_revision": SHA,
        "source_identity": "production-kvant",
        "source_segment": "segment-a",
        "window_start": "2026-08-24",
        "window_end": "2026-11-21",
        "generated_at": "2026-11-22T00:30:00Z",
        "observations": observations,
        "qualification_eligible": True,
    }
    value["canonical_sha256"] = validator.raw_sha256(validator.compatibility_report_payload(value))
    return value


def candidate_archive() -> bytes:
    evidence, manifest = candidate_record()
    stream = io.BytesIO()
    with zipfile.ZipFile(stream, "w", zipfile.ZIP_DEFLATED) as bundle:
        bundle.writestr("candidate-evidence.json", json.dumps(evidence))
        bundle.writestr("manifest.json", json.dumps(manifest))
    return stream.getvalue()


class ProduceRetirementQualificationTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.restore_dir = self.root / "restore"
        self.restore_dir.mkdir()
        self.restore_dir.chmod(0o700)
        self.restore = self.restore_dir / "scheduled-proof.json"
        self.restore.write_text(json.dumps({
            "attempt_id": "scheduled-20260820",
            "finished_at": "2026-08-20T12:00:00Z",
            "result": "success",
            "qualification": {"current_policy_qualified": True},
            "provider_payload": {"migration_after": [sorted((producer.ROOT / "backend/internal/controlplane/migrations").glob("*.sql"))[-1].name]},
        }))
        self.restore.chmod(0o600)
        self.args = argparse.Namespace(
            output=self.root / "bundle",
            restore_evidence_dir=self.restore_dir,
            restore_evidence_basename=self.restore.name,
            deployed_at="2026-08-22T10:00:00Z",
            production_smoke_completed_at="2026-08-22T10:30:00Z",
            production_smoke_proof="https://gitea.internal/owner/switchyard/actions/runs/300",
            deployment_host="production-kvant",
            checkout_sha=SHA,
            release_tag="v1.2.3",
            gitea_api_url="https://gitea.internal/api/v1",
            repository="owner/switchyard",
            candidate_run_id=100,
            candidate_artifact_id=200,
            control_url="https://control.internal",
        )

    def produce(self, report_value=None) -> None:
        with mock.patch.object(producer, "fetch_candidate_record", return_value=candidate_record()), \
             mock.patch.object(producer, "fetch_report", return_value=report_value or report()), \
             mock.patch.object(producer, "verify_smoke_run"), \
             mock.patch.object(producer.restore_evidence, "validate_evidence"):
            producer.produce(self.args, NOW)

    def test_produces_exact_valid_four_file_bundle_with_raw_and_canonical_hashes(self) -> None:
        with mock.patch.dict(producer.os.environ, {"GITEA_TOKEN": "secret", "SWITCHYARD_CONTROL_READ_TOKEN": "secret"}):
            self.produce()
        self.assertEqual({path.name for path in self.args.output.iterdir()}, {
            producer.QUALIFICATION_NAME, producer.EVIDENCE_NAME, producer.MANIFEST_NAME, producer.REPORT_NAME,
        })
        qualification = json.loads((self.args.output / producer.QUALIFICATION_NAME).read_text())
        self.assertEqual(qualification["report_raw_sha256"], producer.digest(self.args.output / producer.REPORT_NAME))
        self.assertEqual(qualification["report_canonical_sha256"], validator.canonical_sha256(report()))
        self.assertNotIn("secret", "".join(path.read_text() for path in self.args.output.iterdir()))
        producer.validate_bundle(self.args.output, NOW)

    def test_rejects_report_for_another_deployed_revision(self) -> None:
        wrong = report()
        wrong["source_revision"] = "2" * 40
        with self.assertRaisesRegex(producer.QualificationError, "source revision"):
            self.produce(wrong)
        self.assertFalse(self.args.output.exists())

    def test_rejects_candidate_that_already_proposes_retirement(self) -> None:
        evidence, manifest = candidate_record()
        evidence["retirement_proposal"] = {"report": {}, "prior_qualifying_releases": []}
        with mock.patch.object(producer, "fetch_candidate_record", return_value=(evidence, manifest)), \
             mock.patch.object(producer, "verify_smoke_run"):
            with self.assertRaisesRegex(producer.QualificationError, "without a retirement proposal"):
                producer.produce(self.args, NOW)
        self.assertFalse(self.args.output.exists())

    def test_rejects_report_for_another_deployment_identity(self) -> None:
        wrong = report()
        wrong["source_identity"] = "other-production"
        with self.assertRaisesRegex(producer.QualificationError, "source identity"):
            self.produce(wrong)
        self.assertFalse(self.args.output.exists())

    def test_rejects_restore_not_created_by_scheduler(self) -> None:
        value = json.loads(self.restore.read_text())
        value["attempt_id"] = "manual-20260820"
        self.restore.write_text(json.dumps(value))
        with self.assertRaisesRegex(producer.QualificationError, "scheduled restore"):
            self.produce()

    def test_rejects_restore_without_current_migration(self) -> None:
        value = json.loads(self.restore.read_text())
        value["provider_payload"]["migration_after"] = ["001_initial.sql"]
        self.restore.write_text(json.dumps(value))
        self.restore.chmod(0o600)
        with self.assertRaisesRegex(producer.QualificationError, "migration head"):
            self.produce()

    def test_rejects_unbound_smoke_proof(self) -> None:
        self.args.production_smoke_proof = "https://other.invalid/owner/switchyard/actions/runs/300"
        with self.assertRaisesRegex(producer.QualificationError, "same-repository"):
            producer.validate_smoke_proof(self.args.production_smoke_proof, self.args.gitea_api_url, self.args.repository)

    def test_fetch_report_requires_the_requested_window(self) -> None:
        response = json.dumps({"compatibility_report": report()}).encode()
        fake = mock.MagicMock()
        fake.__enter__.return_value.read.return_value = response
        with mock.patch.object(producer.urllib.request.OpenerDirector, "open", return_value=fake), \
             self.assertRaisesRegex(producer.QualificationError, "requested 90"):
            producer.fetch_report("https://control.internal", "secret", NOW)

    def test_smoke_proof_requires_deployment_workflow_and_production_runner(self) -> None:
        run = {
            "id": 300,
            "status": "success",
            "head_sha": SHA,
            "url": self.args.production_smoke_proof,
            "workflow_id": producer.PRODUCTION_DEPLOYMENT_WORKFLOW,
            "run_started_at": self.args.deployed_at,
            "updated_at": self.args.production_smoke_completed_at,
        }
        jobs = {"jobs": [{
            "run_id": 300,
            "name": producer.PRODUCTION_SMOKE_JOB,
            "head_sha": SHA,
            "status": "completed",
            "conclusion": "success",
            "labels": [producer.PRODUCTION_RUNNER_LABEL],
        }]}
        with mock.patch.object(producer.gitea, "_request_json", side_effect=[run, jobs]):
            producer.verify_smoke_run(
                self.args.production_smoke_proof,
                self.args.gitea_api_url,
                self.args.repository,
                "secret",
                SHA,
                producer.parse_timestamp(self.args.deployed_at, "deployed_at"),
                producer.parse_timestamp(self.args.production_smoke_completed_at, "smoke_at"),
            )
        run["workflow_id"] = ".gitea/workflows/verify-production.yml"
        with mock.patch.object(producer.gitea, "_request_json", return_value=run), \
             self.assertRaisesRegex(producer.QualificationError, "prove-production-deployment"):
            producer.verify_smoke_run(
                self.args.production_smoke_proof,
                self.args.gitea_api_url,
                self.args.repository,
                "secret",
                SHA,
                producer.parse_timestamp(self.args.deployed_at, "deployed_at"),
                producer.parse_timestamp(self.args.production_smoke_completed_at, "smoke_at"),
            )

    def test_candidate_record_requires_verified_production_run_job_and_artifact(self) -> None:
        run_template = {
            "id": 100,
            "workflow_id": producer.VERIFY_PRODUCTION_WORKFLOW,
            "head_sha": SHA,
            "status": "success",
            "url": "https://gitea.internal/owner/switchyard/actions/runs/100",
        }
        job_template = {
            "run_id": 100,
            "name": producer.VERIFY_PRODUCTION_JOB,
            "head_sha": SHA,
            "status": "completed",
            "conclusion": "success",
            "labels": [producer.PRODUCTION_RUNNER_LABEL],
        }
        artifact_template = {
            "id": 200,
            "name": f"candidate-release-record-{SHA}",
            "expired": False,
            "workflow_run": {"id": 100, "repository_id": 7, "head_sha": SHA},
        }

        def fetch(run, job, artifact):
            with mock.patch.object(producer.gitea, "_request_json", side_effect=[run, {"total_count": 1, "jobs": [job]}, {"total_count": 1, "artifacts": [artifact]}]), \
                 mock.patch.object(producer.gitea, "_request_bytes", return_value=candidate_archive()):
                return producer.fetch_candidate_record(
                    self.args.gitea_api_url,
                    self.args.repository,
                    "secret",
                    100,
                    200,
                    SHA,
                )

        candidate, manifest = fetch(copy.deepcopy(run_template), copy.deepcopy(job_template), copy.deepcopy(artifact_template))
        self.assertEqual(candidate["candidate_sha"], SHA)
        self.assertEqual(manifest["source_revision"], SHA)
        cases = {
            "workflow": ("run", "workflow_id", ".gitea/workflows/other.yml"),
            "status": ("run", "status", "failure"),
            "head": ("run", "head_sha", "2" * 40),
            "job": ("job", "name", "other"),
            "runner": ("job", "labels", ["ubuntu-latest"]),
            "artifact": ("artifact", "name", "other"),
        }
        for name, (target, field, value) in cases.items():
            with self.subTest(name=name):
                run = copy.deepcopy(run_template)
                job = copy.deepcopy(job_template)
                artifact = copy.deepcopy(artifact_template)
                {"run": run, "job": job, "artifact": artifact}[target][field] = value
                with self.assertRaises((producer.QualificationError, producer.gitea.DownloadError)):
                    fetch(run, job, artifact)
    def test_bundle_validation_rechecks_report_semantics(self) -> None:
        self.produce()
        report_path = self.args.output / producer.REPORT_NAME
        qualification_path = self.args.output / producer.QUALIFICATION_NAME
        value = json.loads(report_path.read_text())
        value["qualification_eligible"] = False
        report_path.write_text(json.dumps(value))
        qualification = json.loads(qualification_path.read_text())
        qualification["report_raw_sha256"] = producer.digest(report_path)
        qualification["report_canonical_sha256"] = validator.canonical_sha256(value)
        qualification_path.write_text(json.dumps(qualification))
        with self.assertRaises(validator.EvidenceError):
            producer.validate_bundle(self.args.output, NOW)

    def test_rejects_unqualified_scheduled_restore(self) -> None:
        value = json.loads(self.restore.read_text())
        value["qualification"]["current_policy_qualified"] = False
        self.restore.write_text(json.dumps(value))
        with self.assertRaisesRegex(producer.QualificationError, "qualify the current production policy"):
            self.produce()

    def test_workflow_is_opt_in_and_uploads_only_downloader_contract_for_400_days(self) -> None:
        workflow = (producer.ROOT / ".gitea/workflows/qualify-retirement-release.yml").read_text()
        deployment_workflow = (producer.ROOT / ".gitea/workflows/prove-production-deployment.yml").read_text()
        self.assertIn("workflow_dispatch:", workflow)
        self.assertNotIn("schedule:", workflow)
        self.assertIn("make production-smoke", workflow)
        self.assertIn("retention-days: 400", workflow)
        for name in (producer.QUALIFICATION_NAME, producer.EVIDENCE_NAME, producer.MANIFEST_NAME, producer.REPORT_NAME):
            self.assertIn(f"retirement-qualification/{name}", workflow)
        self.assertIn("name: compatibility-retirement-report", workflow)
        self.assertNotIn("retirement-qualification/**", workflow)
        self.assertIn("${{ secrets.SWITCHYARD_CONTROL_READ_TOKEN }}", workflow)
        self.assertNotIn('"${{ inputs.', workflow)
        self.assertIn('git cat-file -t "refs/tags/$RELEASE_TAG"', workflow)
        self.assertIn("runs-on: switchyard-production", deployment_workflow)
        self.assertIn("name: production-smoke", deployment_workflow)
        self.assertIn("make production-smoke", deployment_workflow)

    def test_verify_workflow_uses_only_small_report_reference_inputs(self) -> None:
        workflow = (producer.ROOT / ".gitea/workflows/verify-production.yml").read_text()
        self.assertIn("retirement_report_run_id:", workflow)
        self.assertIn("retirement_report_artifact_id:", workflow)
        self.assertIn("scripts/download_retirement_report.py", workflow)
        self.assertNotIn("retirement_report:\n", workflow)
        self.assertNotIn("RETIREMENT_REPORT_JSON", workflow)


if __name__ == "__main__":
    unittest.main()
