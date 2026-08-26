#!/usr/bin/env python3

import copy
import json
import re
import tempfile
import unittest
from datetime import datetime, timedelta, timezone
from pathlib import Path

import validate_release_evidence as validator


CANDIDATE = "1" * 40
OTHER_SHA = "2" * 40
PRIOR_SHA_ONE = "3" * 40
PRIOR_SHA_TWO = "4" * 40
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


def retirement_report(source_revision: str = CANDIDATE, start: datetime | None = None) -> dict:
    start = start or datetime(2026, 5, 24, tzinfo=timezone.utc)
    observations = []
    for offset in range(90):
        day = start + timedelta(days=offset)
        for backend in ("llama-line", "ocq"):
            observations.append({
                "date": day.strftime("%Y-%m-%d"),
                "backend": backend,
                "attempts": 0,
                "source_revision": source_revision,
                "deployment_id": "switchyard-production-kvant",
                "observation_segment": "segment-production-a",
                "observation_started_at": day.strftime("%Y-%m-%dT00:00:00Z"),
                "observation_completed_at": (day + timedelta(days=1)).strftime("%Y-%m-%dT00:00:00Z"),
            })
    report = {
        "version": "switchyard.compatibility-observation/v1",
        "source_revision": source_revision,
        "source_identity": "switchyard-production-kvant",
        "source_segment": "segment-production-a",
        "window_start": start.strftime("%Y-%m-%d"),
        "window_end": (start + timedelta(days=89)).strftime("%Y-%m-%d"),
        "generated_at": (start + timedelta(days=90, minutes=30)).strftime("%Y-%m-%dT%H:%M:%SZ"),
        "observations": observations,
        "qualification_eligible": True,
    }
    unsigned = validator.compatibility_report_payload(report)
    report["canonical_sha256"] = validator.hashlib.sha256(unsigned).hexdigest()
    return report


def retirement_evidence(artifact_root: Path | None = None) -> tuple[dict, dict]:
    artifact_root = artifact_root or validator.DEFAULT_ARTIFACT_ROOT
    assert artifact_root is not None
    evidence = candidate_evidence()
    report = retirement_report()
    report_path = artifact_root / "compatibility-retirement-report.json"
    report_path.write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
    evidence["retirement_proposal"] = {
        "report": {
            "artifact": "compatibility-retirement-report.json",
            "raw_sha256": validator.raw_sha256(report_path.read_bytes()),
            "canonical_sha256": validator.canonical_sha256(report),
            "source_revision": CANDIDATE,
            "source_identity": report["source_identity"],
            "source_segment": report["source_segment"],
        },
        "prior_qualifying_releases": [
            {
                "release_tag": "v1.2.1",
                "release_sha": PRIOR_SHA_ONE,
                "qualification_artifact": "prior-one-qualification.json",
                "qualification_raw_sha256": "b" * 64,
            },
            {
                "release_tag": "v1.2.2",
                "release_sha": PRIOR_SHA_TWO,
                "qualification_artifact": "prior-two-qualification.json",
                "qualification_raw_sha256": "c" * 64,
            },
        ],
    }
    for index, (sha, tag, start) in enumerate(((PRIOR_SHA_ONE, "v1.2.1", datetime(2025, 7, 1, tzinfo=timezone.utc)), (PRIOR_SHA_TWO, "v1.2.2", datetime(2025, 11, 1, tzinfo=timezone.utc)))):
        prior_evidence = candidate_evidence()
        prior_evidence["candidate_sha"] = sha
        prior_evidence["release_tag"] = tag
        prior_evidence["rollback_target"] = "v1.2.0"
        prior_evidence["phase"] = "deployed"
        deployed_at = start - timedelta(days=1)
        predeploy_gate = (start - timedelta(days=2)).strftime("%Y-%m-%dT%H:%M:%SZ")
        smoke_at = (deployed_at + timedelta(minutes=30)).strftime("%Y-%m-%dT%H:%M:%SZ")
        report_generated = start + timedelta(days=90, minutes=30)
        prior_evidence["gates"] = {
            "verify_production": gate(predeploy_gate),
            "extended_soak": gate(predeploy_gate),
            "backup_restore": gate(predeploy_gate),
            "production_smoke": gate(smoke_at),
        }
        prior_evidence["recorded_at"] = (report_generated + timedelta(minutes=10)).strftime("%Y-%m-%dT%H:%M:%SZ")
        prior_evidence["deployment"] = {
            "deployed_sha": sha,
            "deployed_at": deployed_at.strftime("%Y-%m-%dT%H:%M:%SZ"),
            "host": "kvant",
        }
        prior_manifest = manifest()
        prior_manifest["source_revision"] = sha
        prior_manifest["release_tag"] = tag
        prior_report = retirement_report(sha, start)
        release = evidence["retirement_proposal"]["prior_qualifying_releases"][index]
        evidence_bytes = json.dumps(prior_evidence, separators=(",", ":")).encode()
        manifest_bytes = json.dumps(prior_manifest, separators=(",", ":")).encode()
        report_bytes = json.dumps(prior_report, separators=(",", ":")).encode()
        prefix = "prior-one" if index == 0 else "prior-two"
        evidence_artifact = f"{prefix}-evidence.json"
        manifest_artifact = f"{prefix}-manifest.json"
        report_artifact = f"{prefix}-report.json"
        (artifact_root / evidence_artifact).write_bytes(evidence_bytes)
        (artifact_root / manifest_artifact).write_bytes(manifest_bytes)
        (artifact_root / report_artifact).write_bytes(report_bytes)
        qualification = {
            "schema_version": "switchyard.compatibility-qualification/v1",
            "release_tag": tag,
            "release_sha": sha,
            "qualified_at": (report_generated + timedelta(minutes=20)).strftime("%Y-%m-%dT%H:%M:%SZ"),
            "evidence_artifact": evidence_artifact,
            "evidence_raw_sha256": validator.raw_sha256(evidence_bytes),
            "manifest_artifact": manifest_artifact,
            "manifest_raw_sha256": validator.raw_sha256(manifest_bytes),
            "report_artifact": report_artifact,
            "report_raw_sha256": validator.raw_sha256(report_bytes),
            "report_canonical_sha256": validator.canonical_sha256(prior_report),
        }
        qualification_bytes = json.dumps(qualification, separators=(",", ":")).encode()
        (artifact_root / release["qualification_artifact"]).write_bytes(qualification_bytes)
        release["qualification_raw_sha256"] = validator.raw_sha256(qualification_bytes)
    return evidence, report


def refresh_report_hashes(evidence: dict, report: dict) -> None:
    report.pop("canonical_sha256", None)
    unsigned = validator.compatibility_report_payload(report)
    report["canonical_sha256"] = validator.hashlib.sha256(unsigned).hexdigest()
    evidence["retirement_proposal"]["report"]["canonical_sha256"] = validator.canonical_sha256(report)
    root = validator.DEFAULT_ARTIFACT_ROOT
    assert root is not None
    (root / evidence["retirement_proposal"]["report"]["artifact"]).write_text(
        json.dumps(report, indent=2) + "\n", encoding="utf-8"
    )
    evidence["retirement_proposal"]["report"]["raw_sha256"] = validator.raw_sha256(
        (root / evidence["retirement_proposal"]["report"]["artifact"]).read_bytes()
    )


def qualification_for(root: Path, release: dict) -> tuple[dict, Path]:
    path = root / release["qualification_artifact"]
    return json.loads(path.read_text()), path


def write_qualification(release: dict, qualification: dict, path: Path) -> None:
    payload = json.dumps(qualification, separators=(",", ":")).encode()
    path.write_bytes(payload)
    release["qualification_raw_sha256"] = validator.raw_sha256(payload)


class ReleaseEvidenceTest(unittest.TestCase):
    def setUp(self) -> None:
        self._temp = tempfile.TemporaryDirectory()
        self.addCleanup(self._temp.cleanup)
        self.artifact_root = Path(self._temp.name)
        validator.DEFAULT_ARTIFACT_ROOT = self.artifact_root

    def assert_invalid(self, evidence: dict, expected: str, *, release_manifest: dict | None = None) -> None:
        with self.assertRaisesRegex(validator.EvidenceError, expected):
            validator.validate(CANDIDATE, evidence, release_manifest or manifest(), NOW)

    def test_candidate_phase_accepts_no_deployment_fields(self) -> None:
        validator.validate(CANDIDATE, candidate_evidence(), manifest(), NOW)

    def test_retirement_proposal_accepts_complete_zero_use_report_and_two_prior_releases(self) -> None:
        evidence, report = retirement_evidence()
        validator.validate(CANDIDATE, evidence, manifest(), NOW, report)

    def test_prior_qualification_rejects_nested_component_paths(self) -> None:
        for field in ("evidence_artifact", "manifest_artifact", "report_artifact"):
            with self.subTest(field=field):
                evidence, report = retirement_evidence()
                release = evidence["retirement_proposal"]["prior_qualifying_releases"][0]
                qualification, path = qualification_for(self.artifact_root, release)
                qualification[field] = "nested/component.json"
                write_qualification(release, qualification, path)
                with self.assertRaisesRegex(validator.EvidenceError, "must be a visible JSON artifact basename"):
                    validator.validate(CANDIDATE, evidence, manifest(), NOW, report)

    def test_prior_qualification_rejects_component_names_omitted_by_upload_glob(self) -> None:
        for name in ("evidence.dat", ".evidence.json"):
            with self.subTest(name=name):
                evidence, report = retirement_evidence()
                release = evidence["retirement_proposal"]["prior_qualifying_releases"][0]
                qualification, path = qualification_for(self.artifact_root, release)
                qualification["evidence_artifact"] = name
                write_qualification(release, qualification, path)
                with self.assertRaisesRegex(validator.EvidenceError, "visible JSON artifact basename"):
                    validator.validate(CANDIDATE, evidence, manifest(), NOW, report)

    def test_retirement_report_is_rejected_without_proposal(self) -> None:
        with self.assertRaisesRegex(validator.EvidenceError, "without a retirement proposal"):
            validator.validate(CANDIDATE, candidate_evidence(), manifest(), NOW, retirement_report())

    def test_retirement_proposal_requires_report(self) -> None:
        evidence, _ = retirement_evidence()
        self.assert_invalid(evidence, "requires a compatibility retirement report")

    def test_retirement_report_requires_ninety_elapsed_days(self) -> None:
        evidence, report = retirement_evidence()
        report["window_start"] = "2026-05-25"
        refresh_report_hashes(evidence, report)
        with self.assertRaisesRegex(validator.EvidenceError, "exactly 90 elapsed days"):
            validator.validate(CANDIDATE, evidence, manifest(), NOW, report)

    def test_retirement_report_requires_complete_coverage_for_both_backends(self) -> None:
        mutations = (
            lambda report: report["observations"].pop(),
            lambda report: report["observations"][1].__setitem__("backend", "llama-line"),
            lambda report: report["observations"][1].__setitem__("date", "2026-05-25"),
        )
        for mutate in mutations:
            with self.subTest(mutation=mutate):
                evidence, report = retirement_evidence()
                mutate(report)
                refresh_report_hashes(evidence, report)
                with self.assertRaises(validator.EvidenceError):
                    validator.validate(CANDIDATE, evidence, manifest(), NOW, report)

    def test_retirement_report_rejects_backend_use(self) -> None:
        evidence, report = retirement_evidence()
        report["observations"][0]["attempts"] = 1
        refresh_report_hashes(evidence, report)
        with self.assertRaisesRegex(validator.EvidenceError, "attempts must be integer zero"):
            validator.validate(CANDIDATE, evidence, manifest(), NOW, report)

    def test_retirement_report_requires_shared_daily_identity(self) -> None:
        evidence, report = retirement_evidence()
        report["observations"][1]["deployment_id"] = "different-deployment"
        refresh_report_hashes(evidence, report)
        with self.assertRaisesRegex(validator.EvidenceError, "does not match the report identity"):
            validator.validate(CANDIDATE, evidence, manifest(), NOW, report)

    def test_retirement_report_hash_is_bound_to_candidate_evidence(self) -> None:
        evidence, report = retirement_evidence()
        evidence["retirement_proposal"]["report"]["canonical_sha256"] = "d" * 64
        with self.assertRaisesRegex(validator.EvidenceError, "hash does not match"):
            validator.validate(CANDIDATE, evidence, manifest(), NOW, report)

    def test_candidate_retirement_report_named_artifact_must_exist_and_match(self) -> None:
        for mutation, expected in (
            ("missing", "escapes or is missing"),
            ("reformatted", "raw byte hash does not match"),
            ("different", "does not match its named artifact"),
        ):
            with self.subTest(mutation=mutation):
                evidence, report = retirement_evidence()
                path = self.artifact_root / "compatibility-retirement-report.json"
                if mutation == "missing":
                    path.unlink()
                elif mutation == "reformatted":
                    path.write_text(json.dumps(json.loads(path.read_text()), separators=(",", ":")))
                else:
                    different = copy.deepcopy(report)
                    different["generated_at"] = "2026-08-22T10:59:59Z"
                    path.write_text(json.dumps(different))
                    evidence["retirement_proposal"]["report"]["raw_sha256"] = validator.raw_sha256(path.read_bytes())
                    evidence["retirement_proposal"]["report"]["canonical_sha256"] = validator.canonical_sha256(different)
                with self.assertRaisesRegex(validator.EvidenceError, expected):
                    validator.validate(CANDIDATE, evidence, manifest(), NOW, report)

    def test_retirement_report_revision_and_source_identity_are_bound(self) -> None:
        for field, value, expected in (
            ("source_revision", OTHER_SHA, "source_revision does not match"),
            ("source_identity", "another-deployment", "source_identity does not match"),
            ("source_segment", "another-segment", "source_segment does not match"),
        ):
            with self.subTest(field=field):
                evidence, report = retirement_evidence()
                evidence["retirement_proposal"]["report"][field] = value
                with self.assertRaisesRegex(validator.EvidenceError, expected):
                    validator.validate(CANDIDATE, evidence, manifest(), NOW, report)

    def test_retirement_report_source_revision_is_bound_to_candidate(self) -> None:
        evidence, report = retirement_evidence()
        report["source_revision"] = OTHER_SHA
        evidence["retirement_proposal"]["report"]["source_revision"] = OTHER_SHA
        for observation in report["observations"]:
            observation["source_revision"] = OTHER_SHA
        refresh_report_hashes(evidence, report)
        with self.assertRaisesRegex(validator.EvidenceError, "must match candidate SHA"):
            validator.validate(CANDIDATE, evidence, manifest(), NOW, report)

    def test_retirement_observations_match_report_revision(self) -> None:
        evidence, report = retirement_evidence()
        report["observations"][0]["source_revision"] = OTHER_SHA
        refresh_report_hashes(evidence, report)
        with self.assertRaisesRegex(validator.EvidenceError, "does not match the report source_revision"):
            validator.validate(CANDIDATE, evidence, manifest(), NOW, report)

    def test_retirement_observation_missing_hash_field_is_validation_error(self) -> None:
        evidence, report = retirement_evidence()
        del report["observations"][0]["backend"]
        evidence["retirement_proposal"]["report"]["canonical_sha256"] = validator.canonical_sha256(report)
        (self.artifact_root / "compatibility-retirement-report.json").write_text(json.dumps(report))
        evidence["retirement_proposal"]["report"]["raw_sha256"] = validator.raw_sha256((self.artifact_root / "compatibility-retirement-report.json").read_bytes())
        with self.assertRaisesRegex(validator.EvidenceError, "missing required field"):
            validator.validate(CANDIDATE, evidence, manifest(), NOW, report)

    def test_retirement_report_rejects_invalid_internal_digest(self) -> None:
        evidence, report = retirement_evidence()
        report["canonical_sha256"] = "f" * 64
        evidence["retirement_proposal"]["report"]["canonical_sha256"] = validator.canonical_sha256(report)
        (self.artifact_root / "compatibility-retirement-report.json").write_text(json.dumps(report))
        evidence["retirement_proposal"]["report"]["raw_sha256"] = validator.raw_sha256((self.artifact_root / "compatibility-retirement-report.json").read_bytes())
        with self.assertRaisesRegex(validator.EvidenceError, "canonical_sha256 does not match"):
            validator.validate(CANDIDATE, evidence, manifest(), NOW, report)

    def test_retirement_report_hash_ignores_input_object_key_order(self) -> None:
        evidence, report = retirement_evidence()
        original_digest = report["canonical_sha256"]
        report = dict(reversed(list(report.items())))
        report["observations"] = [
            dict(reversed(list(observation.items())))
            for observation in report["observations"]
        ]
        self.assertEqual(
            validator.hashlib.sha256(
                validator.compatibility_report_payload(report)
            ).hexdigest(),
            original_digest,
        )
        report_path = self.artifact_root / "compatibility-retirement-report.json"
        report_path.write_text(json.dumps(report))
        evidence["retirement_proposal"]["report"]["raw_sha256"] = validator.raw_sha256(report_path.read_bytes())
        evidence["retirement_proposal"]["report"]["canonical_sha256"] = validator.canonical_sha256(report)
        validator.validate(CANDIDATE, evidence, manifest(), NOW, report)

    def test_retirement_proposal_requires_exactly_two_prior_releases(self) -> None:
        for count in (1, 3):
            with self.subTest(count=count):
                evidence, report = retirement_evidence()
                releases = evidence["retirement_proposal"]["prior_qualifying_releases"]
                if count == 1:
                    releases.pop()
                else:
                    releases.append(copy.deepcopy(releases[-1]))
                    releases[-1]["release_tag"] = "v1.2.0"
                    releases[-1]["release_sha"] = "5" * 40
                    releases[-1]["qualification_raw_sha256"] = "e" * 64
                with self.assertRaisesRegex(validator.EvidenceError, "exactly two releases"):
                    validator.validate(CANDIDATE, evidence, manifest(), NOW, report)

    def test_retirement_prior_releases_are_distinct_and_ordered(self) -> None:
        for field in ("release_tag", "release_sha", "qualification_raw_sha256"):
            with self.subTest(field=field):
                evidence, report = retirement_evidence()
                releases = evidence["retirement_proposal"]["prior_qualifying_releases"]
                releases[1][field] = releases[0][field]
                with self.assertRaisesRegex(validator.EvidenceError, "distinct immutable records"):
                    validator.validate(CANDIDATE, evidence, manifest(), NOW, report)
        evidence, report = retirement_evidence()
        releases = evidence["retirement_proposal"]["prior_qualifying_releases"]
        releases.reverse()
        with self.assertRaisesRegex(validator.EvidenceError, "ordered by qualification time"):
            validator.validate(CANDIDATE, evidence, manifest(), NOW, report)

    def test_prior_release_requires_hashed_manifest_artifact(self) -> None:
        for mutation, expected in (
            ("missing", "escapes or is missing"),
            ("tampered", "raw byte hash"),
        ):
            with self.subTest(mutation=mutation):
                evidence, report = retirement_evidence()
                release = evidence["retirement_proposal"]["prior_qualifying_releases"][0]
                qualification, qualification_path = qualification_for(self.artifact_root, release)
                path = self.artifact_root / qualification["manifest_artifact"]
                if mutation == "missing":
                    path.unlink()
                else:
                    path.write_bytes(path.read_bytes() + b"\n")
                with self.assertRaisesRegex(validator.EvidenceError, expected):
                    validator.validate(CANDIDATE, evidence, manifest(), NOW, report)

    def test_prior_manifest_binds_release_revision_tag_ci_and_archives(self) -> None:
        mutations = (
            ("source_revision", OTHER_SHA, "manifest source_revision"),
            ("release_tag", "v9.9.9", "release_tag does not match manifest"),
            ("ci_run_url", "https://gitea.internal/actions/runs/999", "ci_run does not match"),
            ("images", [{"archive": "other.oci.tar", "archive_sha256": CHECKSUM}], "checksums do not match"),
        )
        for field, value, expected in mutations:
            with self.subTest(field=field):
                evidence, report = retirement_evidence()
                release = evidence["retirement_proposal"]["prior_qualifying_releases"][0]
                qualification, qualification_path = qualification_for(self.artifact_root, release)
                path = self.artifact_root / qualification["manifest_artifact"]
                prior_manifest = json.loads(path.read_text())
                prior_manifest[field] = value
                payload = json.dumps(prior_manifest, separators=(",", ":")).encode()
                path.write_bytes(payload)
                qualification["manifest_raw_sha256"] = validator.raw_sha256(payload)
                write_qualification(release, qualification, qualification_path)
                with self.assertRaisesRegex(validator.EvidenceError, expected):
                    validator.validate(CANDIDATE, evidence, manifest(), NOW, report)

    def test_prior_qualifying_evidence_must_be_deployed_with_deployment_gates(self) -> None:
        for mutation, expected in (
            ("candidate", "must be deployed phase"),
            ("missing-gate", "missing required field.*backup_restore"),
        ):
            with self.subTest(mutation=mutation):
                evidence, report = retirement_evidence()
                release = evidence["retirement_proposal"]["prior_qualifying_releases"][0]
                qualification, qualification_path = qualification_for(self.artifact_root, release)
                path = self.artifact_root / qualification["evidence_artifact"]
                prior = json.loads(path.read_text())
                if mutation == "candidate":
                    prior["phase"] = "candidate"
                    prior.pop("deployment")
                    prior["gates"].pop("backup_restore")
                    prior["gates"].pop("production_smoke")
                else:
                    prior["gates"].pop("backup_restore")
                payload = json.dumps(prior, separators=(",", ":")).encode()
                path.write_bytes(payload)
                qualification["evidence_raw_sha256"] = validator.raw_sha256(payload)
                write_qualification(release, qualification, qualification_path)
                with self.assertRaisesRegex(validator.EvidenceError, expected):
                    validator.validate(CANDIDATE, evidence, manifest(), NOW, report)

    def test_candidate_cannot_be_reused_as_prior_release(self) -> None:
        for field, value in (("release_tag", "v1.2.3"), ("release_sha", CANDIDATE)):
            with self.subTest(field=field):
                evidence, report = retirement_evidence()
                evidence["retirement_proposal"]["prior_qualifying_releases"][1][field] = value
                with self.assertRaisesRegex(validator.EvidenceError, "candidate release cannot count"):
                    validator.validate(CANDIDATE, evidence, manifest(), NOW, report)

    def test_candidate_tag_alias_cannot_be_reused_as_prior_release(self) -> None:
        evidence, report = retirement_evidence()
        evidence["retirement_proposal"]["prior_qualifying_releases"][1]["release_tag"] = "1.2.3"
        with self.assertRaisesRegex(validator.EvidenceError, "candidate release cannot count"):
            validator.validate(CANDIDATE, evidence, manifest(), NOW, report)

    def test_prior_tag_aliases_are_not_distinct(self) -> None:
        evidence, report = retirement_evidence()
        releases = evidence["retirement_proposal"]["prior_qualifying_releases"]
        releases[0]["release_tag"] = "v1.2.1"
        releases[1]["release_tag"] = "1.2.1"
        with self.assertRaisesRegex(validator.EvidenceError, "distinct immutable records"):
            validator.validate(CANDIDATE, evidence, manifest(), NOW, report)

    def test_prior_release_artifact_bytes_are_hashed(self) -> None:
        evidence, report = retirement_evidence()
        release = evidence["retirement_proposal"]["prior_qualifying_releases"][0]
        qualification, _ = qualification_for(self.artifact_root, release)
        path = self.artifact_root / qualification["evidence_artifact"]
        path.write_bytes(path.read_bytes() + b"\n")
        with self.assertRaisesRegex(validator.EvidenceError, "raw byte hash"):
            validator.validate(CANDIDATE, evidence, manifest(), NOW, report)

    def test_prior_report_reformat_is_raw_byte_tampering(self) -> None:
        evidence, report = retirement_evidence()
        release = evidence["retirement_proposal"]["prior_qualifying_releases"][0]
        qualification, _ = qualification_for(self.artifact_root, release)
        path = self.artifact_root / qualification["report_artifact"]
        path.write_text(json.dumps(json.loads(path.read_text()), indent=2) + "\n")
        with self.assertRaisesRegex(validator.EvidenceError, "raw byte hash"):
            validator.validate(CANDIDATE, evidence, manifest(), NOW, report)

    def test_prior_qualification_reformat_is_raw_byte_tampering(self) -> None:
        evidence, report = retirement_evidence()
        release = evidence["retirement_proposal"]["prior_qualifying_releases"][0]
        _, path = qualification_for(self.artifact_root, release)
        path.write_text(json.dumps(json.loads(path.read_text()), indent=2) + "\n")
        with self.assertRaisesRegex(validator.EvidenceError, "raw byte hash"):
            validator.validate(CANDIDATE, evidence, manifest(), NOW, report)

    def test_prior_evidence_must_not_embed_retirement_proposal(self) -> None:
        evidence, report = retirement_evidence()
        release = evidence["retirement_proposal"]["prior_qualifying_releases"][0]
        qualification, qualification_path = qualification_for(self.artifact_root, release)
        evidence_path = self.artifact_root / qualification["evidence_artifact"]
        prior = json.loads(evidence_path.read_text())
        prior["retirement_proposal"] = {"report": {}, "prior_qualifying_releases": []}
        payload = json.dumps(prior, separators=(",", ":")).encode()
        evidence_path.write_bytes(payload)
        qualification["evidence_raw_sha256"] = validator.raw_sha256(payload)
        write_qualification(release, qualification, qualification_path)
        with self.assertRaisesRegex(validator.EvidenceError, "must not contain retirement_proposal"):
            validator.validate(CANDIDATE, evidence, manifest(), NOW, report)

    def test_prior_qualification_enforces_chronology_and_deployment_boundary(self) -> None:
        for mutation, expected in (
            ("qualification-before-evidence", "qualification time must follow evidence finalization"),
            ("deployment-inside-window", "deployment must precede"),
        ):
            with self.subTest(mutation=mutation):
                evidence, report = retirement_evidence()
                release = evidence["retirement_proposal"]["prior_qualifying_releases"][0]
                qualification, qualification_path = qualification_for(self.artifact_root, release)
                if mutation == "qualification-before-evidence":
                    qualification["qualified_at"] = "2025-01-01T00:00:00Z"
                    write_qualification(release, qualification, qualification_path)
                else:
                    evidence_path = self.artifact_root / qualification["evidence_artifact"]
                    prior = json.loads(evidence_path.read_text())
                    prior["deployment"]["deployed_at"] = "2025-07-02T00:00:00Z"
                    payload = json.dumps(prior, separators=(",", ":")).encode()
                    evidence_path.write_bytes(payload)
                    qualification["evidence_raw_sha256"] = validator.raw_sha256(payload)
                    write_qualification(release, qualification, qualification_path)
                with self.assertRaisesRegex(validator.EvidenceError, expected):
                    validator.validate(CANDIDATE, evidence, manifest(), NOW, report)

    def test_prior_report_chronology_uses_prior_evidence_time(self) -> None:
        evidence, report = retirement_evidence()
        release = evidence["retirement_proposal"]["prior_qualifying_releases"][0]
        qualification, qualification_path = qualification_for(self.artifact_root, release)
        report_path = self.artifact_root / qualification["report_artifact"]
        prior_report = json.loads(report_path.read_text())
        prior_report["generated_at"] = "2026-08-22T10:30:00Z"
        prior_report.pop("canonical_sha256")
        prior_report["canonical_sha256"] = validator.hashlib.sha256(
            validator.compatibility_report_payload(prior_report)
        ).hexdigest()
        payload = json.dumps(prior_report, separators=(",", ":")).encode()
        report_path.write_bytes(payload)
        qualification["report_raw_sha256"] = validator.raw_sha256(payload)
        qualification["report_canonical_sha256"] = validator.canonical_sha256(prior_report)
        write_qualification(release, qualification, qualification_path)
        with self.assertRaisesRegex(validator.EvidenceError, "generated after evidence.recorded_at"):
            validator.validate(CANDIDATE, evidence, manifest(), NOW, report)

    def test_prior_release_order_uses_qualification_time(self) -> None:
        evidence, report = retirement_evidence()
        first = evidence["retirement_proposal"]["prior_qualifying_releases"][0]
        qualification, path = qualification_for(self.artifact_root, first)
        qualification["qualified_at"] = "2026-03-01T00:00:00Z"
        write_qualification(first, qualification, path)
        with self.assertRaisesRegex(validator.EvidenceError, "ordered by qualification time"):
            validator.validate(CANDIDATE, evidence, manifest(), NOW, report)

    def test_diagnostic_report_cannot_qualify(self) -> None:
        evidence, report = retirement_evidence()
        report["qualification_eligible"] = False
        refresh_report_hashes(evidence, report)
        with self.assertRaisesRegex(validator.EvidenceError, "diagnostic"):
            validator.validate(CANDIDATE, evidence, manifest(), NOW, report)

    def test_report_identity_uses_same_safe_charset_as_runtime(self) -> None:
        evidence, report = retirement_evidence()
        report["source_identity"] = "production&west"
        evidence["retirement_proposal"]["report"]["source_identity"] = "production&west"
        for observation in report["observations"]:
            observation["deployment_id"] = "production&west"
        refresh_report_hashes(evidence, report)
        with self.assertRaisesRegex(validator.EvidenceError, "ASCII letters"):
            validator.validate(CANDIDATE, evidence, manifest(), NOW, report)

    def test_report_identity_rejects_more_than_128_characters(self) -> None:
        evidence, report = retirement_evidence()
        report["source_identity"] = "a" * 129
        evidence["retirement_proposal"]["report"]["source_identity"] = "a" * 129
        for observation in report["observations"]:
            observation["deployment_id"] = "a" * 129
        refresh_report_hashes(evidence, report)
        with self.assertRaisesRegex(validator.EvidenceError, "1-128"):
            validator.validate(CANDIDATE, evidence, manifest(), NOW, report)

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
        qualifying = schema["$defs"]["qualifying_release"]
        self.assertEqual(
            set(qualifying["required"]),
            {"release_tag", "release_sha", "qualification_artifact", "qualification_raw_sha256"},
        )
        report_link = schema["properties"]["retirement_proposal"]["properties"]["report"]
        self.assertIn("raw_sha256", report_link["required"])
        self.assertIn("canonical_sha256", report_link["required"])
        self.assertIn("source_segment", report_link["required"])

        qualification_schema = json.loads(
            (schema_path.parent / "compatibility-qualification-v1.schema.json").read_text(encoding="utf-8")
        )
        self.assertEqual(
            qualification_schema["properties"]["schema_version"]["const"],
            "switchyard.compatibility-qualification/v1",
        )
        for field in ("evidence_raw_sha256", "manifest_raw_sha256", "report_raw_sha256", "report_canonical_sha256"):
            self.assertIn(field, qualification_schema["required"])
        basename_pattern = re.compile(qualification_schema["$defs"]["artifact_basename"]["pattern"])
        self.assertIsNotNone(basename_pattern.fullmatch("evidence.json"))
        self.assertIsNone(basename_pattern.fullmatch("nested/evidence.json"))
        self.assertIsNone(basename_pattern.fullmatch("evidence.dat"))
        self.assertIsNone(basename_pattern.fullmatch(".evidence.json"))

        reference_schema = json.loads(
            (schema_path.parent / "retirement-prior-references-v1.schema.json").read_text(encoding="utf-8")
        )
        self.assertEqual(reference_schema["minItems"], 2)
        self.assertEqual(reference_schema["maxItems"], 2)
        self.assertEqual(reference_schema["items"]["additionalProperties"], False)
        for field in ("run_id", "artifact_id"):
            self.assertEqual(reference_schema["items"]["properties"][field]["minimum"], 1)
            self.assertEqual(reference_schema["items"]["properties"][field]["maximum"], (1 << 63) - 1)

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
        for artifact in (
            "candidate-evidence.json",
            "compatibility-retirement-report.json",
            "manifest.json",
            "SHA256SUMS",
        ):
            with self.subTest(artifact=artifact):
                self.assertIn(f"release/{artifact}", workflow)
        self.assertIn("release/*.oci.tar", workflow)
        self.assertIn("retirement_proposal:", workflow)
        self.assertIn("RETIREMENT_PRIOR_RELEASES_JSON", workflow)
        self.assertIn("scripts/download_retirement_qualifications.py", workflow)
        self.assertIn("${{ gitea.token }}", workflow)
        self.assertIn("${{ gitea.api_url }}", workflow)
        self.assertIn("${{ gitea.repository }}", workflow)
        self.assertIn("actions: read", workflow)
        upload_action = "https://gitea.com/actions/gitea-upload-artifact@81f940d004763f986ba3582c007fd842dd5cb0d7"
        self.assertIn(upload_action, workflow)
        self.assertNotIn("actions/upload-artifact@", workflow)
        retirement_workflow = (root / ".gitea" / "workflows" / "qualify-retirement-release.yml").read_text()
        artifact_smoke = (root / ".gitea" / "workflows" / "artifact-smoke.yml").read_text()
        self.assertEqual(workflow.count(upload_action), 2)
        self.assertEqual(retirement_workflow.count(upload_action), 2)
        self.assertEqual(artifact_smoke.count(upload_action), 1)
        self.assertNotIn("uses: actions/gitea-upload-artifact@", workflow + retirement_workflow + artifact_smoke)
        self.assertIn("retention-days: 31", workflow)
        self.assertIn("retention-days: 401", workflow)
        self.assertEqual(retirement_workflow.count("retention-days: 401"), 2)
        self.assertIn("release_tag:", workflow)
        self.assertIn("ref: ${{ inputs.release_tag }}", workflow)
        self.assertIn('git cat-file -t "refs/tags/$RELEASE_TAG"', workflow)
        self.assertIn('git rev-parse "refs/tags/$RELEASE_TAG^{}"', workflow)
        self.assertIn('test "$GITHUB_REF" = "refs/tags/$RELEASE_TAG"', workflow)
        self.assertIn('test "$GITHUB_SHA" = "$TAG_SHA"', workflow)
        self.assertIn('manifest.get("release_tag") != os.environ["RELEASE_TAG"]', workflow)
        self.assertIn('tag=${RELEASE_TAG:-', generator)
        self.assertIn('"raw_sha256": hashlib.sha256(report_path.read_bytes()).hexdigest()', workflow)
        self.assertIn('"canonical_sha256": hashlib.sha256(canonical).hexdigest()', workflow)
        self.assertIn('"retirement-release-qualification.json"', workflow)
        self.assertIn('"qualification_raw_sha256"', workflow)
        self.assertNotIn('"qualified_at": supplied["qualified_at"]', workflow)
        self.assertNotIn("BASE64", workflow.upper())
        self.assertNotIn("b64decode", workflow)
        self.assertNotIn("write_bytes(payload)", workflow)
        self.assertNotIn('manifest_from_evidence', workflow)
        makefile = (root / "Makefile").read_text()
        self.assertIn("RETIREMENT_REPORT", makefile)
        self.assertIn('--retirement-report', makefile)

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
