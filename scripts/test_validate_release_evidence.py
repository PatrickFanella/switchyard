#!/usr/bin/env python3

import copy
import json
import re
import tempfile
import unittest
from datetime import datetime, timezone
from pathlib import Path

import validate_release_evidence as validator


CANDIDATE = "1" * 40
OTHER_SHA = "2" * 40
CHECKSUM = "a" * 64
NOW = datetime(2026, 8, 22, 12, 0, tzinfo=timezone.utc)


def gate(completed_at: str = "2026-08-22T10:00:00Z") -> dict[str, str]:
    return {
        "status": "passed",
        "completed_at": completed_at,
        "proof": "https://gitea.internal/actions/runs/100",
    }


def candidate_evidence() -> dict:
    return {
        "schema_version": 1,
        "phase": "candidate",
        "candidate_sha": CANDIDATE,
        "release_tag": "v1.2.3",
        "pull_request": "https://gitea.internal/switchyard/pulls/42",
        "ci_run": "https://gitea.internal/actions/runs/100",
        "migration_head": "020_allow_committed_open_pilot_phase.sql",
        "rollback_target": "v1.2.2",
        "exceptions": [],
        "recorded_at": "2026-08-22T11:00:00Z",
        "gates": {
            "verify_production": gate(),
            "extended_soak": gate(),
        },
        "artifacts": [
            {"archive": "switchyard-api.oci.tar", "archive_sha256": CHECKSUM}
        ],
    }


def manifest() -> dict:
    return {
        "source_revision": CANDIDATE,
        "release_tag": "v1.2.3",
        "ci_run_url": "https://gitea.internal/actions/runs/100",
        "migration_head": "020_allow_committed_open_pilot_phase.sql",
        "images": [
            {
                "archive": "switchyard-api.oci.tar",
                "archive_sha256": CHECKSUM,
                "oci_index_digest": "sha256:" + "b" * 64,
            }
        ],
    }


class ReleaseEvidenceTest(unittest.TestCase):
    def assert_invalid(self, evidence: dict, expected: str, *, release_manifest: dict | None = None) -> None:
        with self.assertRaisesRegex(validator.EvidenceError, expected):
            validator.validate(CANDIDATE, evidence, release_manifest or manifest(), NOW)

    def test_candidate_phase_accepts_no_deployment_fields(self) -> None:
        validator.validate(CANDIDATE, candidate_evidence(), manifest(), NOW)

    def test_checked_in_schema_is_version_one(self) -> None:
        schema_path = (
            Path(__file__).resolve().parents[1]
            / "docs"
            / "releases"
            / "release-evidence-v1.schema.json"
        )
        schema = json.loads(schema_path.read_text(encoding="utf-8"))
        self.assertEqual(schema["properties"]["schema_version"]["const"], 1)
        self.assertEqual(schema["properties"]["schema_version"]["type"], "integer")
        self.assertEqual(schema["$defs"]["git_sha"]["pattern"], r"^[0-9a-f]{40}$")
        for field in (
            "release_tag",
            "pull_request",
            "ci_run",
            "migration_head",
            "rollback_target",
            "exceptions",
        ):
            with self.subTest(field=field):
                self.assertIn(field, schema["required"])
        text_pattern = re.compile(schema["$defs"]["non_placeholder_text"]["pattern"])
        self.assertIsNone(text_pattern.fullmatch("TODO: supply proof"))
        self.assertIsNotNone(text_pattern.fullmatch("v1.2.3"))
        timestamp_pattern = re.compile(schema["$defs"]["utc_timestamp"]["pattern"])
        self.assertIsNotNone(timestamp_pattern.fullmatch("2026-08-22T10:00:00Z"))
        self.assertIsNone(timestamp_pattern.fullmatch("2026-08-22 10:00:00Z"))
        pull_request_pattern = re.compile(schema["$defs"]["pull_request_url"]["pattern"])
        self.assertIsNotNone(
            pull_request_pattern.fullmatch("https://gitea.internal/switchyard/pulls/42")
        )
        self.assertIsNone(
            pull_request_pattern.fullmatch("https://user:secret@gitea.internal/switchyard/pulls/42")
        )
        rollback_schema = schema["$defs"]["rollback_target"]
        semver_pattern = re.compile(rollback_schema["oneOf"][1]["pattern"])
        self.assertIsNotNone(semver_pattern.fullmatch("v1.2.2"))
        self.assertIsNone(semver_pattern.fullmatch("main"))

    def test_deployed_phase_accepts_complete_deployment_proof(self) -> None:
        evidence = candidate_evidence()
        evidence["phase"] = "deployed"
        evidence["gates"].update(
            {
                "backup_restore": gate(),
                "production_smoke": gate("2026-08-22T11:35:00Z"),
            }
        )
        evidence["recorded_at"] = "2026-08-22T11:40:00Z"
        evidence["deployment"] = {
            "deployed_sha": CANDIDATE,
            "deployed_at": "2026-08-22T11:30:00Z",
            "host": "kvant",
        }
        validator.validate(CANDIDATE, evidence, manifest(), NOW)

    def test_deployed_operational_gates_accept_external_proof(self) -> None:
        evidence = candidate_evidence()
        evidence["phase"] = "deployed"
        evidence["gates"].update(
            {
                "backup_restore": gate(),
                "production_smoke": gate("2026-08-22T11:35:00Z"),
            }
        )
        evidence["gates"]["backup_restore"]["proof"] = "artifact:restore-drill.json"
        evidence["gates"]["production_smoke"]["proof"] = "artifact:production-smoke.json"
        evidence["recorded_at"] = "2026-08-22T11:40:00Z"
        evidence["deployment"] = {
            "deployed_sha": CANDIDATE,
            "deployed_at": "2026-08-22T11:30:00Z",
            "host": "kvant",
        }
        validator.validate(CANDIDATE, evidence, manifest(), NOW)

    def test_rejects_missing_candidate_sha(self) -> None:
        evidence = candidate_evidence()
        del evidence["candidate_sha"]
        self.assert_invalid(evidence, "missing required field.*candidate_sha")

    def test_rejects_mismatched_candidate_sha(self) -> None:
        evidence = candidate_evidence()
        evidence["candidate_sha"] = OTHER_SHA
        self.assert_invalid(evidence, "candidate_sha does not match")

    def test_rejects_mismatched_manifest_sha(self) -> None:
        release_manifest = manifest()
        release_manifest["source_revision"] = OTHER_SHA
        self.assert_invalid(candidate_evidence(), "manifest source_revision", release_manifest=release_manifest)

    def test_rejects_evidence_recorded_before_its_gates(self) -> None:
        evidence = candidate_evidence()
        evidence["recorded_at"] = "2020-01-01T00:00:00Z"
        self.assert_invalid(evidence, "completed after evidence.recorded_at")

    def test_rejects_each_missing_release_identity_field(self) -> None:
        for field in (
            "release_tag",
            "pull_request",
            "ci_run",
            "migration_head",
            "rollback_target",
            "exceptions",
        ):
            with self.subTest(field=field):
                evidence = candidate_evidence()
                del evidence[field]
                self.assert_invalid(evidence, rf"missing required field.*{field}")

    def test_rejects_release_tag_not_matching_manifest(self) -> None:
        evidence = candidate_evidence()
        evidence["release_tag"] = "v1.2.4"
        self.assert_invalid(evidence, "release_tag does not match")

    def test_rejects_invalid_pull_request_urls(self) -> None:
        urls = (
            "http://gitea.internal/switchyard/pulls/42",
            "https://user:secret@gitea.internal/switchyard/pulls/42",
            "https://gitea.internal/pulls/42",
            "https://gitea.internal/switchyard/pulls/0",
            "https://gitea.internal/switchyard/pulls/42?token=secret",
            "https://gitea.internal/switchyard/pulls/42#proof",
        )
        for url in urls:
            with self.subTest(url=url):
                evidence = candidate_evidence()
                evidence["pull_request"] = url
                self.assert_invalid(evidence, "credential-free HTTPS")

    def test_accepts_immutable_sha_rollback_target(self) -> None:
        evidence = candidate_evidence()
        evidence["rollback_target"] = OTHER_SHA
        validator.validate(CANDIDATE, evidence, manifest(), NOW)

    def test_rejects_invalid_or_current_rollback_targets(self) -> None:
        targets = (
            "main",
            "V1.2.2",
            "A" * 40,
            CANDIDATE,
            candidate_evidence()["release_tag"],
        )
        for target in targets:
            with self.subTest(target=target):
                evidence = candidate_evidence()
                evidence["rollback_target"] = target
                expected = "must differ" if target in {CANDIDATE, evidence["release_tag"]} else "must be a full lowercase"
                self.assert_invalid(evidence, expected)

    def test_rejects_ci_run_not_matching_manifest(self) -> None:
        evidence = candidate_evidence()
        evidence["ci_run"] = "https://gitea.internal/actions/runs/999"
        self.assert_invalid(evidence, "ci_run does not match")

    def test_rejects_gate_proof_not_matching_ci_run(self) -> None:
        evidence = candidate_evidence()
        evidence["gates"]["extended_soak"]["proof"] = (
            "https://gitea.internal/actions/runs/101"
        )
        self.assert_invalid(evidence, "proof must match evidence.ci_run")

    def test_rejects_migration_head_not_matching_manifest(self) -> None:
        evidence = candidate_evidence()
        evidence["migration_head"] = "019_previous.sql"
        self.assert_invalid(evidence, "migration_head does not match")

    def test_rejects_boolean_schema_version(self) -> None:
        evidence = candidate_evidence()
        evidence["schema_version"] = True
        self.assert_invalid(evidence, "schema_version must be integer 1")

    def test_rejects_non_rfc3339_timestamp(self) -> None:
        evidence = candidate_evidence()
        evidence["recorded_at"] = "2026-08-22 11:00:00Z"
        self.assert_invalid(evidence, "must be a strict RFC 3339 UTC timestamp")

    def test_rejects_gate_completed_after_recording_tolerance(self) -> None:
        evidence = candidate_evidence()
        evidence["gates"]["extended_soak"]["completed_at"] = "2026-08-22T11:05:01Z"
        self.assert_invalid(evidence, "completed after evidence.recorded_at")

    def test_rejects_missing_deployed_sha(self) -> None:
        evidence = candidate_evidence()
        evidence["phase"] = "deployed"
        evidence["gates"].update(
            {"backup_restore": gate(), "production_smoke": gate()}
        )
        evidence["deployment"] = {
            "deployed_at": "2026-08-22T11:30:00Z",
            "host": "kvant",
        }
        self.assert_invalid(evidence, "deployment missing required field.*deployed_sha")

    def test_rejects_mismatched_deployed_sha(self) -> None:
        evidence = candidate_evidence()
        evidence["phase"] = "deployed"
        evidence["gates"].update(
            {"backup_restore": gate(), "production_smoke": gate()}
        )
        evidence["deployment"] = {
            "deployed_sha": OTHER_SHA,
            "deployed_at": "2026-08-22T11:30:00Z",
            "host": "kvant",
        }
        self.assert_invalid(evidence, "deployed_sha does not match")

    def test_rejects_missing_gate_proof_field(self) -> None:
        evidence = candidate_evidence()
        del evidence["gates"]["extended_soak"]["proof"]
        self.assert_invalid(evidence, "extended_soak missing required field.*proof")

    def test_rejects_placeholder(self) -> None:
        evidence = candidate_evidence()
        evidence["gates"]["verify_production"]["proof"] = "TODO: add CI run URL"
        self.assert_invalid(evidence, "contains a placeholder")

    def test_rejects_evidence_fields_not_allowed_by_schema(self) -> None:
        evidence = candidate_evidence()
        evidence["artifacts"][0]["oci_index_digest"] = "sha256:" + "b" * 64
        self.assert_invalid(evidence, "has unknown field.*oci_index_digest")

    def test_rejects_placeholder_exception(self) -> None:
        evidence = candidate_evidence()
        evidence["exceptions"] = ["TODO: assign an owner"]
        self.assert_invalid(evidence, r"exceptions\[0\] contains a placeholder")

    def test_rejects_checksum_inconsistency(self) -> None:
        evidence = candidate_evidence()
        evidence["artifacts"][0]["archive_sha256"] = "c" * 64
        self.assert_invalid(evidence, "checksums do not match")

    def test_rejects_candidate_deployment_fields(self) -> None:
        evidence = candidate_evidence()
        evidence["deployment"] = copy.deepcopy(
            {"deployed_sha": CANDIDATE, "deployed_at": "2026-08-22T11:30:00Z", "host": "kvant"}
        )
        self.assert_invalid(evidence, "candidate evidence must omit deployment")

    def test_rejects_candidate_null_deployment(self) -> None:
        evidence = candidate_evidence()
        evidence["deployment"] = None
        self.assert_invalid(evidence, "candidate evidence must omit deployment")

    def test_rejects_deployment_after_recording_tolerance(self) -> None:
        evidence = candidate_evidence()
        evidence["phase"] = "deployed"
        evidence["gates"].update(
            {"backup_restore": gate(), "production_smoke": gate("2026-08-22T11:00:00Z")}
        )
        evidence["deployment"] = {
            "deployed_sha": CANDIDATE,
            "deployed_at": "2026-08-22T11:05:01Z",
            "host": "kvant",
        }
        self.assert_invalid(evidence, "deployed after evidence.recorded_at")

    def test_rejects_deployment_before_qualification_gate(self) -> None:
        evidence = candidate_evidence()
        evidence["phase"] = "deployed"
        evidence["gates"].update(
            {"backup_restore": gate(), "production_smoke": gate()}
        )
        evidence["deployment"] = {
            "deployed_sha": CANDIDATE,
            "deployed_at": "2026-08-22T09:54:59Z",
            "host": "kvant",
        }
        self.assert_invalid(evidence, "deployment predates qualification gate")

    def test_rejects_production_smoke_before_deployment(self) -> None:
        evidence = candidate_evidence()
        evidence["phase"] = "deployed"
        evidence["recorded_at"] = "2026-08-22T11:40:00Z"
        evidence["gates"].update(
            {"backup_restore": gate(), "production_smoke": gate("2026-08-22T11:24:59Z")}
        )
        evidence["deployment"] = {
            "deployed_sha": CANDIDATE,
            "deployed_at": "2026-08-22T11:30:00Z",
            "host": "kvant",
        }
        self.assert_invalid(evidence, "production_smoke predates deployment")

    def test_release_files_bind_manifest_and_uploaded_evidence(self) -> None:
        root = Path(__file__).resolve().parents[1]
        generator = (root / "scripts" / "write-release-manifest.sh").read_text()
        workflow = (root / ".gitea" / "workflows" / "verify-production.yml").read_text()
        self.assertIn('"migration_head"', generator)
        for artifact in ("candidate-evidence.json", "manifest.json", "SHA256SUMS"):
            with self.subTest(artifact=artifact):
                self.assertIn(f"release/{artifact}", workflow)
        self.assertIn("release/*.oci.tar", workflow)

    def test_rejects_duplicate_json_keys_in_evidence_and_manifest(self) -> None:
        cases = (
            ("evidence", '{"phase":"candidate","phase":"deployed"}'),
            ("manifest", '{"source_revision":"one","source_revision":"two"}'),
        )
        with tempfile.TemporaryDirectory() as directory:
            for label, contents in cases:
                with self.subTest(label=label):
                    path = Path(directory) / f"{label}.json"
                    path.write_text(contents, encoding="utf-8")
                    with self.assertRaisesRegex(
                        validator.EvidenceError, rf"{label} contains duplicate JSON key"
                    ):
                        validator.load_json(path, label)


if __name__ == "__main__":
    unittest.main()
