#!/usr/bin/env python3

import copy
import json
import re
import unittest
from datetime import datetime, timezone
from pathlib import Path

import validate_release_evidence as validator


CANDIDATE = "1" * 40
OTHER_SHA = "2" * 40
CHECKSUM = "a" * 64
NOW = datetime(2026, 8, 22, 12, 0, tzinfo=timezone.utc)


def gate(name: str) -> dict[str, str]:
    return {
        "status": "passed",
        "completed_at": "2026-08-22T10:00:00Z",
        "proof": f"https://gitea.internal/actions/runs/{name}",
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
            "verify_production": gate("100"),
            "extended_soak": gate("101"),
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

    def test_deployed_phase_accepts_complete_deployment_proof(self) -> None:
        evidence = candidate_evidence()
        evidence["phase"] = "deployed"
        evidence["gates"].update(
            {"backup_restore": gate("102"), "production_smoke": gate("103")}
        )
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

    def test_historical_matching_evidence_remains_valid(self) -> None:
        evidence = candidate_evidence()
        evidence["recorded_at"] = "2020-01-01T00:00:00Z"
        validator.validate(CANDIDATE, evidence, manifest(), NOW)

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

    def test_rejects_ci_run_not_matching_manifest(self) -> None:
        evidence = candidate_evidence()
        evidence["ci_run"] = "https://gitea.internal/actions/runs/999"
        self.assert_invalid(evidence, "ci_run does not match")

    def test_rejects_missing_deployed_sha(self) -> None:
        evidence = candidate_evidence()
        evidence["phase"] = "deployed"
        evidence["gates"].update(
            {"backup_restore": gate("102"), "production_smoke": gate("103")}
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
            {"backup_restore": gate("102"), "production_smoke": gate("103")}
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
        self.assert_invalid(evidence, "candidate evidence must not contain deployment")


if __name__ == "__main__":
    unittest.main()
