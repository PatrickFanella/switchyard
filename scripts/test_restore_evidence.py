import datetime as dt
import hashlib
import importlib.util
import json
import os
import pathlib
import subprocess
import tempfile
import time
import unittest
from unittest import mock


ROOT = pathlib.Path(__file__).resolve().parent.parent
MODULE_PATH = ROOT / "scripts/restore_evidence.py"
SPEC = importlib.util.spec_from_file_location("restore_evidence", MODULE_PATH)
assert SPEC is not None
assert SPEC.loader is not None
restore_evidence = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(restore_evidence)


def sha(path: pathlib.Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


class RestoreEvidenceTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = pathlib.Path(self.temp.name)
        self.root.chmod(0o700)
        self.bundle_dir = self.root / "bundles"
        self.bundle_dir.mkdir(mode=0o700)
        self.evidence_dir = self.root / "evidence"
        self.evidence_dir.mkdir(mode=0o700)
        self.policy = self.root / "policy.json"
        self.write_policy(self.policy)
        self.manifest = self.write_bundle()
        self.adapter = self.root / "fake-adapter.py"
        self.adapter.write_text(
            """#!/usr/bin/env python3
import json, os, pathlib, sys, time
contract = json.loads(pathlib.Path(sys.argv[3]).read_text())
active_contract = json.loads(pathlib.Path(os.environ['ACTIVE_OBJECT_CONTRACT']).read_text())
stage = os.environ.get('FAKE_STAGE', 'complete')
stage_errors = {'restore': 'archive_restore_failed', 'pre_api': 'pre_api_contract_failed', 'api': 'api_start_failed', 'cleanup': 'resource_cleanup_failed'}
if os.environ.get('FAKE_SIGNAL'): pathlib.Path(os.environ['FAKE_SIGNAL']).touch()
if os.environ.get('FAKE_STAGING_PATH'): pathlib.Path(os.environ['FAKE_STAGING_PATH']).write_text(sys.argv[1])
if os.environ.get('FAKE_SLEEP'):
    time.sleep(float(os.environ['FAKE_SLEEP']))
objects = contract['required_objects']
ledger = contract['migration_ledger']
failure = stage != 'complete'
if os.environ.get('FAKE_OBJECT_MISMATCH'):
    objects = objects[:-1]
if os.environ.get('FAKE_MIGRATION_MISMATCH'):
    ledger = ledger[:-1]
payload = {
  'contract_version': 'switchyard.postgres-restore-payload.v1', 'provider': 'postgres',
  'result': 'success' if stage == 'complete' else 'failure', 'stage': stage,
  'error_code': None if stage == 'complete' else stage_errors[stage], 'database_version': '170006',
  'migration_before': ledger, 'required_objects': objects,
  'row_counts': {name: 0 for name in contract['required_objects']}, 'migration_after': active_contract['migration_ledger'],
  'api_ready': stage == 'complete', 'api_auth_workflows': stage == 'complete'
}
if stage in ('restore', 'pre_api'):
    payload.update(database_version=None, migration_before=None, required_objects=None, row_counts=None,
                   migration_after=None, api_ready=False, api_auth_workflows=False)
elif stage == 'api':
    payload.update(migration_after=None, api_ready=False, api_auth_workflows=False)
elif stage == 'cleanup':
    payload.update(api_ready=True, api_auth_workflows=True)
if os.environ.get('FAKE_ORIGINAL_ARCHIVE'):
    pathlib.Path(os.environ['FAKE_ORIGINAL_ARCHIVE']).write_bytes(b'mutated after staging')
    if pathlib.Path(sys.argv[1]).read_bytes() != b'archive fixture': raise SystemExit(3)
if os.environ.get('FAKE_UNSAFE'):
    payload['database_version'] = '/private/cluster'
if os.environ.get('FAKE_SECRET'):
    payload['database_version'] = 'token=private-value'
if os.environ.get('FAKE_ERROR_SECRET'):
    payload['error_code'] = 'secret'
if os.environ.get('FAKE_ERROR_STAGE_MISMATCH'):
    payload['error_code'] = 'api_auth_failed'
fd = os.open(sys.argv[4], os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
with os.fdopen(fd, 'w') as handle: json.dump(payload, handle)
raise SystemExit(1 if stage != 'complete' or os.environ.get('FAKE_EXIT_NONZERO') else 0)
""",
            encoding="utf-8",
        )
        self.adapter.chmod(0o700)

    def tearDown(self):
        self.temp.cleanup()

    def policy_value(self, **changes):
        value = {
            "contract_version": "switchyard.operator-policy.v1",
            "policy_id": "operations-primary",
            "policy_version": 1,
            "provider": "postgres",
            "storage_owner": "infrastructure-team",
            "encryption_owner": "security-team",
            "encryption_expectation": "host-managed encrypted storage",
            "retention_duration": "P35D",
            "declared_rpo_seconds": 86400,
            "declared_rto_seconds": 600,
            "drill_calendar": "Sun *-*-01..07 03:00:00 UTC",
            "max_drill_interval_seconds": 2678400,
            "alert_grace_seconds": 3600,
        }
        value.update(changes)
        return value

    def write_policy(self, path, **changes):
        path.write_text(json.dumps(self.policy_value(**changes)), encoding="utf-8")

    def write_bundle(self, *, created_at=None):
        created_at = created_at or dt.datetime.now(dt.timezone.utc).replace(microsecond=0).strftime("%Y-%m-%dT%H:%M:%SZ")
        archive = self.bundle_dir / "switchyard-unit.dump"
        archive.write_bytes(b"archive fixture")
        archived_policy = self.bundle_dir / "switchyard-unit.policy.json"
        archived_policy.write_bytes(self.policy.read_bytes())
        archived_contract = self.bundle_dir / "switchyard-unit.object-contract.json"
        archived_contract.write_bytes(restore_evidence.OBJECT_CONTRACT.read_bytes())
        policy_value = json.loads(archived_policy.read_text())
        contract = json.loads(restore_evidence.OBJECT_CONTRACT.read_text())
        metadata = self.bundle_dir / "switchyard-unit.metadata.json"
        metadata.write_text(json.dumps({
            "contract_version": "switchyard.postgres-backup-metadata.v1", "bundle_id": "switchyard-unit",
            "provider": "postgres", "created_at": created_at, "source_revision": "a" * 40,
            "database_version": "170006", "archive_basename": archive.name, "archive_format": "postgres-custom",
            "archive_sha256": sha(archive), "policy_id": policy_value["policy_id"],
            "policy_version": policy_value["policy_version"], "policy_sha256": sha(archived_policy),
            "object_contract_version": contract["contract_version"],
            "object_contract_sha256": sha(archived_contract),
            "migration_head": contract["migration_head"], "migration_ledger": contract["migration_ledger"],
        }), encoding="utf-8")
        manifest = self.bundle_dir / "switchyard-unit.complete.json"
        manifest.write_text(json.dumps({
            "contract_version": "switchyard.completed-backup-bundle.v1", "bundle_id": "switchyard-unit",
            "completed_at": created_at,
            "archive": {"basename": archive.name, "sha256": sha(archive)},
            "metadata": {"basename": metadata.name, "sha256": sha(metadata)},
            "policy": {"basename": archived_policy.name, "sha256": sha(archived_policy)},
            "object_contract": {"basename": archived_contract.name, "sha256": sha(archived_contract)},
        }), encoding="utf-8")
        return manifest

    def run_restore(self, env=None, policy=None, disposable=True, manifest=None):
        before = set(self.evidence_dir.glob("restore-evidence-*.json"))
        command = ["python3", str(MODULE_PATH), "restore", "--bundle", str(manifest or self.manifest),
                   "--policy", str(policy or self.policy), "--evidence-dir", str(self.evidence_dir),
                   "--test-adapter", str(self.adapter), "--timeout-seconds", "10"]
        if disposable:
            command.append("--disposable")
        process_env = os.environ.copy()
        process_env["SWITCHYARD_RESTORE_TESTING"] = "1"
        process_env["ACTIVE_OBJECT_CONTRACT"] = str(restore_evidence.OBJECT_CONTRACT)
        process_env.update(env or {})
        result = subprocess.run(command, text=True, capture_output=True, env=process_env, check=False)
        files = set(self.evidence_dir.glob("restore-evidence-*.json")) - before
        self.assertEqual(len(files), 1)
        evidence = json.loads(files.pop().read_text())
        return result, evidence

    def test_checked_contracts_are_strict_json(self):
        for path in (ROOT / "docs/persistence").glob("*.json"):
            restore_evidence.load_json(path, "bundle")
        contract = json.loads(restore_evidence.OBJECT_CONTRACT.read_text())
        migrations = sorted(path.name for path in (ROOT / "backend/internal/controlplane/migrations").glob("*.sql"))
        self.assertEqual(contract["migration_ledger"], migrations)
        self.assertEqual(contract["migration_head"], migrations[-1])
        malformed = dict(contract, migration_ledger=["not-a-migration"], migration_head="not-a-migration")
        with self.assertRaises(restore_evidence.ContractError):
            restore_evidence.validate_contract(malformed)

    def test_publish_bundle_writes_bound_manifest_last_set_and_refuses_clobber(self):
        publish_dir = self.root / "publish"
        publish_dir.mkdir(mode=0o700)
        archive = publish_dir / ".archive.tmp"
        archive.write_bytes(b"custom archive")
        ledger = self.root / "ledger"
        contract = json.loads(restore_evidence.OBJECT_CONTRACT.read_text())
        ledger.write_text("\n".join(contract["migration_ledger"]) + "\n", encoding="utf-8")
        command = ["python3", str(MODULE_PATH), "publish-bundle", "--policy", str(self.policy),
                   "--archive-temp", str(archive), "--output-dir", str(publish_dir),
                   "--bundle-id", "switchyard-publish", "--archive-basename", "switchyard-publish.dump",
                   "--source-revision", "b" * 40, "--database-version", "170006",
                   "--migration-ledger", str(ledger), "--created-at", "2026-08-23T12:00:00Z"]
        result = subprocess.run(command, text=True, capture_output=True, check=False)
        self.assertEqual(result.returncode, 0, result.stderr)
        manifest = publish_dir / "switchyard-publish.complete.json"
        bundle = restore_evidence.validate_bundle(manifest)
        self.assertTrue(bundle["manifest"])
        replacement = publish_dir / ".archive.next"
        replacement.write_bytes(b"custom archive")
        command[command.index(str(archive))] = str(replacement)
        duplicate = subprocess.run(command, text=True, capture_output=True, check=False)
        self.assertNotEqual(duplicate.returncode, 0)
        self.assertNotIn(str(self.root), duplicate.stderr)

    def test_publish_interruption_rolls_back_every_partial_and_keeps_source(self):
        publish_dir = self.root / "publish-failure"
        publish_dir.mkdir(mode=0o700)
        archive = publish_dir / ".archive.tmp"
        archive.write_bytes(b"custom archive")
        ledger = self.root / "ledger-failure"
        contract = json.loads(restore_evidence.OBJECT_CONTRACT.read_text())
        ledger.write_text("\n".join(contract["migration_ledger"]) + "\n", encoding="utf-8")
        command = ["python3", str(MODULE_PATH), "publish-bundle", "--policy", str(self.policy),
                   "--archive-temp", str(archive), "--output-dir", str(publish_dir),
                   "--bundle-id", "switchyard-failure", "--archive-basename", "switchyard-failure.dump",
                   "--source-revision", "b" * 40, "--database-version", "170006",
                   "--migration-ledger", str(ledger), "--created-at", "2026-08-23T12:00:00Z"]
        targets = (
            "switchyard-failure.dump", "switchyard-failure.policy.json",
            "switchyard-failure.object-contract.json", "switchyard-failure.metadata.json",
            "switchyard-failure.complete.json",
        )
        for target in targets:
            env = os.environ.copy()
            env.update(SWITCHYARD_RESTORE_TESTING="1", SWITCHYARD_RESTORE_TEST_PUBLISH_FAILURE=f"after_{target}")
            result = subprocess.run(command, text=True, capture_output=True, env=env, check=False)
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual([archive], list(publish_dir.iterdir()))

    def test_policy_requires_all_fields_and_postgres(self):
        value = self.policy_value()
        del value["declared_rpo_seconds"]
        with self.assertRaises(restore_evidence.ContractError):
            restore_evidence.validate_policy(value)
        with self.assertRaisesRegex(restore_evidence.ContractError, "unsupported_provider"):
            restore_evidence.validate_policy(self.policy_value(provider="sqlite"))

    def test_policy_rejects_duplicate_keys_placeholders_and_wrong_types(self):
        self.policy.write_text('{"policy_id":"one","policy_id":"two"}', encoding="utf-8")
        with self.assertRaisesRegex(restore_evidence.ContractError, "duplicate_json_key"):
            restore_evidence.validate_policy_path(self.policy)
        with self.assertRaisesRegex(restore_evidence.ContractError, "unsafe_policy_value"):
            restore_evidence.validate_policy(self.policy_value(storage_owner="replace-me"))
        with self.assertRaises(restore_evidence.ContractError):
            restore_evidence.validate_policy(self.policy_value(declared_rto_seconds=True))
        with self.assertRaisesRegex(restore_evidence.ContractError, "invalid_policy_field"):
            restore_evidence.validate_policy(self.policy_value(declared_rto_seconds=2**63 - 1))

    def test_missing_incomplete_and_tampered_bundles_write_failure_evidence(self):
        missing, evidence = self.run_restore(manifest=self.bundle_dir / "missing.complete.json")
        self.assertNotEqual(missing.returncode, 0)
        self.assertEqual((evidence["stage"], evidence["error_code"]), ("bundle", "missing_file"))
        self.manifest.unlink()
        incomplete, evidence = self.run_restore()
        self.assertNotEqual(incomplete.returncode, 0)
        self.assertEqual(evidence["stage"], "bundle")
        self.manifest = self.write_bundle()
        (self.bundle_dir / "switchyard-unit.dump").write_bytes(b"tampered")
        tampered, evidence = self.run_restore()
        self.assertNotEqual(tampered.returncode, 0)
        self.assertEqual(evidence["error_code"], "archive_checksum_mismatch")

    def test_policy_change_allows_restore_but_disqualifies_current_policy(self):
        active = self.root / "active.json"
        self.write_policy(active, policy_version=2)
        result, evidence = self.run_restore(policy=active)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(evidence["qualification"]["active_policy_matches_archived_policy"])
        self.assertFalse(evidence["qualification"]["current_policy_qualified"])

    def test_retained_bundle_uses_archived_object_contract(self):
        archived_contract = self.bundle_dir / "switchyard-unit.object-contract.json"
        contract = json.loads(archived_contract.read_text())
        contract["migration_ledger"] = contract["migration_ledger"][:-1]
        contract["migration_head"] = contract["migration_ledger"][-1]
        archived_contract.write_text(json.dumps(contract), encoding="utf-8")
        metadata_path = self.bundle_dir / "switchyard-unit.metadata.json"
        metadata = json.loads(metadata_path.read_text())
        metadata["object_contract_sha256"] = sha(archived_contract)
        metadata["migration_ledger"] = contract["migration_ledger"]
        metadata["migration_head"] = contract["migration_head"]
        metadata_path.write_text(json.dumps(metadata), encoding="utf-8")
        manifest = json.loads(self.manifest.read_text())
        manifest["object_contract"]["sha256"] = sha(archived_contract)
        manifest["metadata"]["sha256"] = sha(metadata_path)
        self.manifest.write_text(json.dumps(manifest), encoding="utf-8")
        result, evidence = self.run_restore()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(evidence["provider_payload"]["migration_before"], contract["migration_ledger"])
        self.assertNotEqual(evidence["provider_payload"]["migration_after"], contract["migration_ledger"])

    def test_stale_backup_and_rto_are_drill_qualification_not_restore_failure(self):
        old = "2020-01-01T00:00:00Z"
        self.manifest = self.write_bundle(created_at=old)
        active = self.root / "active.json"
        self.write_policy(active, declared_rpo_seconds=1, declared_rto_seconds=1)
        result, evidence = self.run_restore(policy=active, env={"FAKE_SLEEP": "2.1"})
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(evidence["qualification"]["backup_age_within_active_declared_rpo"])
        self.assertFalse(evidence["qualification"]["isolated_drill_within_active_declared_rto"])
        self.assertEqual(evidence["qualification"]["measurement_scope"], "isolated_restore_drill_not_production_recovery_proof")

    def test_future_backup_timestamp_is_rejected(self):
        self.manifest = self.write_bundle(created_at="2999-01-01T00:00:00Z")
        result, evidence = self.run_restore()
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(evidence["error_code"], "future_backup_timestamp")

    def test_explicit_disposable_mode_is_required(self):
        result, evidence = self.run_restore(disposable=False)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(evidence["error_code"], "disposable_mode_required")

    def test_adapter_failure_writes_each_bounded_stage(self):
        expected = {"restore": "archive_restore_failed", "pre_api": "pre_api_contract_failed", "api": "api_start_failed", "cleanup": "resource_cleanup_failed"}
        for stage in ("restore", "pre_api", "api", "cleanup"):
            result, evidence = self.run_restore(env={"FAKE_STAGE": stage})
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(evidence["stage"], stage)
            self.assertEqual(evidence["error_code"], expected[stage])

    def test_nonzero_adapter_exit_omits_even_valid_success_payload(self):
        result, evidence = self.run_restore(env={"FAKE_EXIT_NONZERO": "1"})
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(evidence["error_code"], "adapter_failed")
        self.assertIsNone(evidence["provider_payload"])

    def test_object_and_migration_mismatches_fail_pre_api(self):
        for variable in ("FAKE_OBJECT_MISMATCH", "FAKE_MIGRATION_MISMATCH"):
            result, evidence = self.run_restore(env={variable: "1"})
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(evidence["stage"], "pre_api")

    def test_absolute_path_payload_is_sanitized_to_error_code(self):
        for variable in ("FAKE_UNSAFE", "FAKE_SECRET"):
            result, evidence = self.run_restore(env={variable: "1"})
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(evidence["error_code"], "invalid_database_version")
            self.assertNotIn(str(self.root), json.dumps(evidence))
            self.assertNotIn("private-value", json.dumps(evidence))
        result, evidence = self.run_restore(env={"FAKE_STAGE": "restore", "FAKE_ERROR_SECRET": "1"})
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(evidence["error_code"], "invalid_provider_error")
        self.assertNotIn('"secret"', json.dumps(evidence))
        result, evidence = self.run_restore(env={"FAKE_STAGE": "restore", "FAKE_ERROR_STAGE_MISMATCH": "1"})
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(evidence["error_code"], "invalid_provider_error")

    def test_adapter_receives_staged_copy_not_mutable_bundle_path(self):
        archive = self.bundle_dir / "switchyard-unit.dump"
        result, evidence = self.run_restore(env={"FAKE_ORIGINAL_ARCHIVE": str(archive)})
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(evidence["result"], "success")
        self.assertEqual(archive.read_bytes(), b"mutated after staging")

    def test_staging_is_strictly_removed_and_cleanup_failure_is_reported(self):
        recorded = self.root / "staging-path"
        result, evidence = self.run_restore(env={"FAKE_STAGING_PATH": str(recorded)})
        self.assertEqual(result.returncode, 0, result.stderr)
        staged_archive = pathlib.Path(recorded.read_text())
        self.assertFalse(staged_archive.exists())
        self.assertFalse(staged_archive.parent.parent.exists())

        result, evidence = self.run_restore(env={"SWITCHYARD_RESTORE_TEST_CLEANUP_FAILURE": "1"})
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual((evidence["stage"], evidence["error_code"]), ("cleanup", "staging_cleanup_failed"))
        self.assertIsNone(evidence["qualification"])
        self.assertIsNone(evidence["provider_payload"])

    def test_bundle_mutation_and_replacement_races_are_rejected(self):
        archive = self.bundle_dir / "switchyard-unit.dump"
        original_digest = restore_evidence.fd_digest

        mutated = [False]
        def mutate_after_digest(descriptor):
            value = original_digest(descriptor)
            if not mutated[0]:
                mutated[0] = True
                archive.write_bytes(b"mutated during validation")
            return value

        with mock.patch.object(restore_evidence, "fd_digest", side_effect=mutate_after_digest):
            with self.assertRaisesRegex(restore_evidence.ContractError, "archive_checksum_mismatch"):
                restore_evidence.validate_bundle(self.manifest, self.root / "stage-mutation")

        self.manifest = self.write_bundle()

        replaced = [False]
        def replace_after_digest(descriptor):
            value = original_digest(descriptor)
            if not replaced[0]:
                replaced[0] = True
                replacement = self.root / "replacement"
                replacement.write_bytes(b"archive fixture")
                os.replace(replacement, archive)
            return value

        with mock.patch.object(restore_evidence, "fd_digest", side_effect=replace_after_digest):
            with self.assertRaisesRegex(restore_evidence.ContractError, "archive_replaced_during_validation"):
                restore_evidence.validate_bundle(self.manifest, self.root / "stage-replacement")

    def test_evidence_rejects_both_bundle_ancestry_directions(self):
        original = self.evidence_dir
        self.evidence_dir = self.root
        result, evidence = self.run_restore()
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(evidence["error_code"], "evidence_directory_not_dedicated")
        nested = self.bundle_dir / "evidence"
        nested.mkdir(mode=0o700)
        self.evidence_dir = nested
        result, evidence = self.run_restore()
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(evidence["error_code"], "evidence_directory_not_dedicated")
        self.evidence_dir = original

    def test_evidence_symlink_swap_writes_through_held_directory_fd(self):
        signal = self.root / "adapter-started"
        command = ["python3", str(MODULE_PATH), "restore", "--bundle", str(self.manifest),
                   "--policy", str(self.policy), "--evidence-dir", str(self.evidence_dir),
                   "--test-adapter", str(self.adapter), "--timeout-seconds", "10", "--disposable"]
        env = os.environ.copy()
        env.update(SWITCHYARD_RESTORE_TESTING="1", ACTIVE_OBJECT_CONTRACT=str(restore_evidence.OBJECT_CONTRACT),
                   FAKE_SLEEP="1", FAKE_SIGNAL=str(signal))
        process = subprocess.Popen(command, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, env=env)
        for _ in range(100):
            if signal.exists():
                break
            time.sleep(0.01)
        real = self.root / "evidence-held"
        attacker = self.root / "attacker"
        attacker.mkdir(mode=0o700)
        self.evidence_dir.rename(real)
        self.evidence_dir.symlink_to(attacker, target_is_directory=True)
        stdout, stderr = process.communicate(timeout=15)
        self.assertEqual(process.returncode, 0, stderr)
        self.assertEqual(list(attacker.iterdir()), [])
        self.assertEqual(len(list(real.glob("restore-evidence-*.json"))), 1, stdout)

    def test_evidence_validator_rejects_null_and_contradictory_results(self):
        _, evidence = self.run_restore()
        for field, replacement in (("bundle", None), ("qualification", None), ("provider_payload", None), ("error_code", "failed"), ("stage", "api")):
            changed = dict(evidence)
            changed[field] = replacement
            with self.assertRaises(restore_evidence.ContractError):
                restore_evidence.validate_evidence(changed)
        failed = dict(evidence, result="failure", stage="api", error_code="adapter_failed", qualification=None, provider_payload=None)
        restore_evidence.validate_evidence(failed)
        failed["qualification"] = evidence["qualification"]
        with self.assertRaises(restore_evidence.ContractError):
            restore_evidence.validate_evidence(failed)
        failed["qualification"] = None
        failed["bundle"] = {"arbitrary": "value"}
        with self.assertRaises(restore_evidence.ContractError):
            restore_evidence.validate_evidence(failed)
        contradictory = json.loads(json.dumps(evidence))
        contradictory["qualification"]["backup_age_within_active_declared_rpo"] = False
        with self.assertRaises(restore_evidence.ContractError):
            restore_evidence.validate_evidence(contradictory)
        nested_failure = json.loads(json.dumps(evidence))
        nested_failure["provider_payload"].update(
            result="failure", stage="restore", error_code="archive_restore_failed", database_version=None,
            migration_before=None, required_objects=None, row_counts=None, migration_after=None,
            api_ready=False, api_auth_workflows=False,
        )
        with self.assertRaises(restore_evidence.ContractError):
            restore_evidence.validate_evidence(nested_failure)
        inconsistent = json.loads(json.dumps(evidence))
        inconsistent["provider_payload"]["row_counts"].pop(next(iter(inconsistent["provider_payload"]["row_counts"])))
        with self.assertRaises(restore_evidence.ContractError):
            restore_evidence.validate_evidence(inconsistent)

    def test_symlink_inputs_rejected_and_no_clobber_preserved(self):
        link = self.root / "policy-link.json"
        link.symlink_to(self.policy)
        result, evidence = self.run_restore(policy=link)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(evidence["error_code"], "unsafe_file")
        target = self.root / "once.json"
        restore_evidence.write_new_json(target, {"one": 1})
        with self.assertRaises(FileExistsError):
            restore_evidence.write_new_json(target, {"two": 2})
        self.assertEqual(json.loads(target.read_text()), {"one": 1})
        real_parent = self.root / "real-parent"
        real_parent.mkdir()
        linked_parent = self.root / "linked-parent"
        linked_parent.symlink_to(real_parent, target_is_directory=True)
        with self.assertRaisesRegex(restore_evidence.ContractError, "unsafe_directory"):
            restore_evidence.prepare_private_directory(linked_parent / "backups")


if __name__ == "__main__":
    unittest.main()
