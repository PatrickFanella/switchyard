#!/usr/bin/env python3
"""Validate backup policy/bundles and write provider-neutral restore evidence."""

from __future__ import annotations

import argparse
import datetime as dt
import hashlib
import json
import math
import os
import re
import secrets
import shutil
import stat
import subprocess
import sys
import tempfile
import time
from pathlib import Path
from typing import Any

ROOT = Path(__file__).resolve().parent.parent
OBJECT_CONTRACT = ROOT / "docs/persistence/postgres-object-set-v1.json"
SAFE_ID = re.compile(r"^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$")
SHA256 = re.compile(r"^[0-9a-f]{64}$")
REVISION = re.compile(r"^[0-9a-f]{40,64}$")
POSTGRES_VERSION = re.compile(r"^[0-9]{6}$")
ERROR_CODE = re.compile(r"^[a-z][a-z0-9_]{0,63}$")
PLACEHOLDER = re.compile(r"(?i)(replace[-_ ]?me|change[-_ ]?me|placeholder|example|todo|tbd|unknown)")
SECRET = re.compile(r"(?i)(password|passwd|secret|token|credential|authorization|database_url)\s*[:=]")
STAGES = {"selection", "policy", "bundle", "restore", "pre_api", "api", "cleanup", "complete"}
PROVIDER_ERROR_CODES = {
    "adapter_interrupted", "archive_copy_failed", "archive_restore_failed",
    "api_auth_failed", "api_start_failed", "orphan_cleanup_failed", "postgres_ready_failed",
    "postgres_start_failed", "pre_api_contract_failed", "resource_cleanup_failed",
    "restore_already_running", "unsafe_adapter_path",
}
PROVIDER_STAGE_ERRORS = {
    "restore": {"adapter_interrupted", "archive_copy_failed", "archive_restore_failed", "postgres_ready_failed", "postgres_start_failed", "restore_already_running", "unsafe_adapter_path"},
    "pre_api": {"pre_api_contract_failed"},
    "api": {"api_auth_failed", "api_start_failed"},
    "cleanup": {"orphan_cleanup_failed", "resource_cleanup_failed"},
}
MAX_JSON_BYTES = 256 * 1024
MAX_PROVIDER_ITEMS = 4096
MAX_POLICY_SECONDS = 315_576_000
MAX_COUNT = (1 << 63) - 1
CLEANUP_GRACE_SECONDS = 60
MIGRATION = re.compile(r"^[0-9]{3}_[a-z0-9_]+\.sql$")
OBJECT_NAME = re.compile(r"^[a-z][a-z0-9_]{0,62}$")


class ContractError(Exception):
    def __init__(self, stage: str, code: str):
        super().__init__(code)
        self.stage = stage
        self.code = code


def utc_now() -> dt.datetime:
    return dt.datetime.now(dt.timezone.utc).replace(microsecond=0)


def timestamp(value: dt.datetime) -> str:
    return value.astimezone(dt.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def parse_timestamp(value: Any, stage: str = "bundle") -> dt.datetime:
    if not isinstance(value, str) or not re.fullmatch(r"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z", value):
        raise ContractError(stage, "invalid_timestamp")
    try:
        return dt.datetime.strptime(value, "%Y-%m-%dT%H:%M:%SZ").replace(tzinfo=dt.timezone.utc)
    except ValueError as exc:
        raise ContractError(stage, "invalid_timestamp") from exc


def reject_duplicate(pairs: list[tuple[str, Any]]) -> dict[str, Any]:
    result: dict[str, Any] = {}
    for key, value in pairs:
        if key in result:
            raise ContractError("bundle", "duplicate_json_key")
        result[key] = value
    return result


def load_json(path: Path, stage: str) -> dict[str, Any]:
    safe_input_file(path, stage)
    try:
        data = path.read_bytes()
        if len(data) > MAX_JSON_BYTES:
            raise ContractError(stage, "json_too_large")
        value = json.loads(data.decode("utf-8"), object_pairs_hook=reject_duplicate)
    except ContractError as exc:
        raise ContractError(stage, exc.code) from exc
    except (OSError, UnicodeError, json.JSONDecodeError) as exc:
        raise ContractError(stage, "invalid_json") from exc
    if type(value) is not dict:
        raise ContractError(stage, "invalid_json_type")
    return value


def exact_object(value: dict[str, Any], keys: set[str], stage: str) -> None:
    if set(value) != keys:
        raise ContractError(stage, "invalid_fields")


def safe_input_file(path: Path, stage: str) -> None:
    try:
        info = path.lstat()
    except OSError as exc:
        raise ContractError(stage, "missing_file") from exc
    if stat.S_ISLNK(info.st_mode) or not stat.S_ISREG(info.st_mode) or path.parent.resolve() != path.parent.absolute():
        raise ContractError(stage, "unsafe_file")


def safe_directory(path: Path, stage: str, create: bool = False) -> None:
    if create:
        try:
            path.mkdir(mode=0o700, parents=False, exist_ok=True)
        except OSError as exc:
            raise ContractError(stage, "invalid_directory") from exc
    try:
        info = path.lstat()
    except OSError as exc:
        raise ContractError(stage, "invalid_directory") from exc
    if stat.S_ISLNK(info.st_mode) or not stat.S_ISDIR(info.st_mode) or info.st_mode & 0o077 or path.resolve() != path.absolute():
        raise ContractError(stage, "unsafe_directory")


def prepare_private_directory(path: Path) -> None:
    absolute = path.absolute()
    if absolute == ROOT or ROOT in absolute.parents:
        raise ContractError("bundle", "backup_directory_in_repository")
    current = Path(absolute.anchor)
    for part in absolute.parts[1:]:
        current = current / part
        try:
            info = current.lstat()
        except FileNotFoundError:
            try:
                current.mkdir(mode=0o700)
            except OSError as exc:
                raise ContractError("bundle", "invalid_directory") from exc
            continue
        except OSError as exc:
            raise ContractError("bundle", "invalid_directory") from exc
        if stat.S_ISLNK(info.st_mode) or not stat.S_ISDIR(info.st_mode):
            raise ContractError("bundle", "unsafe_directory")
    try:
        os.chmod(absolute, 0o700)
    except OSError as exc:
        raise ContractError("bundle", "invalid_directory") from exc
    safe_directory(path, "bundle")


def digest(path: Path) -> str:
    value = hashlib.sha256()
    with path.open("rb") as handle:
        for block in iter(lambda: handle.read(1024 * 1024), b""):
            value.update(block)
    return value.hexdigest()


def fsync_file(path: Path) -> None:
    descriptor = os.open(path, os.O_RDONLY)
    try:
        os.fsync(descriptor)
    finally:
        os.close(descriptor)


def fsync_directory(path: Path) -> None:
    descriptor = os.open(path, os.O_RDONLY | os.O_DIRECTORY)
    try:
        os.fsync(descriptor)
    finally:
        os.close(descriptor)


def open_directory(path: Path, stage: str) -> int:
    """Open an absolute directory without following a symlink in any component."""
    absolute = path.absolute()
    descriptor = os.open(absolute.anchor, os.O_RDONLY | os.O_DIRECTORY)
    try:
        for part in absolute.parts[1:]:
            next_descriptor = os.open(part, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=descriptor)
            os.close(descriptor)
            descriptor = next_descriptor
        info = os.fstat(descriptor)
        if info.st_mode & 0o077:
            raise ContractError(stage, "unsafe_directory")
        return descriptor
    except BaseException:
        os.close(descriptor)
        raise


def read_fd_json(descriptor: int, stage: str) -> tuple[dict[str, Any], bytes]:
    os.lseek(descriptor, 0, os.SEEK_SET)
    chunks: list[bytes] = []
    total = 0
    while True:
        block = os.read(descriptor, 64 * 1024)
        if not block:
            break
        total += len(block)
        if total > MAX_JSON_BYTES:
            raise ContractError(stage, "json_too_large")
        chunks.append(block)
    data = b"".join(chunks)
    try:
        value = json.loads(data.decode("utf-8"), object_pairs_hook=reject_duplicate)
    except ContractError as exc:
        raise ContractError(stage, exc.code) from exc
    except (UnicodeError, json.JSONDecodeError) as exc:
        raise ContractError(stage, "invalid_json") from exc
    if type(value) is not dict:
        raise ContractError(stage, "invalid_json_type")
    return value, data


def open_regular_at(directory_fd: int, name: str, stage: str) -> int:
    try:
        descriptor = os.open(name, os.O_RDONLY | os.O_NOFOLLOW, dir_fd=directory_fd)
    except OSError as exc:
        raise ContractError(stage, "missing_file") from exc
    if not stat.S_ISREG(os.fstat(descriptor).st_mode):
        os.close(descriptor)
        raise ContractError(stage, "unsafe_file")
    return descriptor


def fd_digest(descriptor: int) -> str:
    value = hashlib.sha256()
    os.lseek(descriptor, 0, os.SEEK_SET)
    for block in iter(lambda: os.read(descriptor, 1024 * 1024), b""):
        value.update(block)
    return value.hexdigest()


def safe_text(value: Any, field: str, *, identifier: bool = False) -> str:
    if not isinstance(value, str) or not value or len(value) > 256:
        raise ContractError("policy", "invalid_policy_field")
    if identifier and not SAFE_ID.fullmatch(value):
        raise ContractError("policy", "unsafe_identifier")
    if value.startswith("/") or "\x00" in value or "\n" in value or PLACEHOLDER.search(value) or SECRET.search(value):
        raise ContractError("policy", "unsafe_policy_value")
    return value


def positive_int(value: Any, *, zero: bool = False, maximum: int | None = None) -> int:
    if type(value) is not int or value < (0 if zero else 1) or maximum is not None and value > maximum:
        raise ContractError("policy", "invalid_policy_field")
    return value


def validate_policy(value: dict[str, Any]) -> dict[str, Any]:
    keys = {
        "contract_version", "policy_id", "policy_version", "provider", "storage_owner",
        "encryption_owner", "encryption_expectation", "retention_duration",
        "declared_rpo_seconds", "declared_rto_seconds", "drill_calendar",
        "max_drill_interval_seconds", "alert_grace_seconds",
    }
    exact_object(value, keys, "policy")
    if value["contract_version"] != "switchyard.operator-policy.v1":
        raise ContractError("policy", "unsupported_policy_contract")
    safe_text(value["policy_id"], "policy_id", identifier=True)
    positive_int(value["policy_version"])
    if value["provider"] != "postgres":
        raise ContractError("policy", "unsupported_provider")
    for field in ("storage_owner", "encryption_owner", "encryption_expectation", "drill_calendar"):
        safe_text(value[field], field)
    if not isinstance(value["retention_duration"], str) or not re.fullmatch(r"P[1-9][0-9]*D", value["retention_duration"]):
        raise ContractError("policy", "invalid_retention_duration")
    for field in ("declared_rpo_seconds", "declared_rto_seconds", "max_drill_interval_seconds"):
        positive_int(value[field], maximum=MAX_POLICY_SECONDS)
    positive_int(value["alert_grace_seconds"], zero=True, maximum=MAX_POLICY_SECONDS)
    return value


def validate_policy_path(path: Path) -> tuple[dict[str, Any], str]:
    safe_input_file(path, "policy")
    descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    try:
        value, _ = read_fd_json(descriptor, "policy")
        return validate_policy(value), fd_digest(descriptor)
    finally:
        os.close(descriptor)


def validate_contract(value: dict[str, Any]) -> dict[str, Any]:
    exact_object(value, {"contract_version", "provider", "migration_head", "migration_ledger", "required_objects"}, "bundle")
    if value["contract_version"] != "switchyard.postgres-object-set.v1" or value["provider"] != "postgres":
        raise ContractError("bundle", "unsupported_object_contract")
    ledger = value["migration_ledger"]
    objects = value["required_objects"]
    if not isinstance(ledger, list) or not ledger or len(ledger) > MAX_PROVIDER_ITEMS or any(type(item) is not str or len(item) > 128 or not MIGRATION.fullmatch(item) for item in ledger):
        raise ContractError("bundle", "invalid_migration_contract")
    if ledger != sorted(set(ledger)) or value["migration_head"] != ledger[-1]:
        raise ContractError("bundle", "invalid_migration_contract")
    if not isinstance(objects, list) or len(objects) > MAX_PROVIDER_ITEMS or objects != sorted(set(objects)) or any(type(item) is not str or not OBJECT_NAME.fullmatch(item) for item in objects):
        raise ContractError("bundle", "invalid_object_contract")
    return value


def write_new_json(path: Path, value: dict[str, Any], mode: int = 0o600) -> None:
    flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL
    descriptor = os.open(path, flags, mode)
    try:
        with os.fdopen(descriptor, "w", encoding="utf-8") as handle:
            json.dump(value, handle, sort_keys=True, separators=(",", ":"))
            handle.write("\n")
            handle.flush()
            os.fsync(handle.fileno())
    except BaseException:
        path.unlink(missing_ok=True)
        raise


def write_all(descriptor: int, data: bytes) -> None:
    view = memoryview(data)
    while view:
        written = os.write(descriptor, view)
        if written < 1:
            raise OSError("short write")
        view = view[written:]


def publish_fd_noclobber_fd(directory_fd: int, name: str, source_fd: int) -> str:
    temporary = f".{name}.{secrets.token_hex(8)}.tmp"
    descriptor = -1
    linked = False
    checksum = hashlib.sha256()
    try:
        descriptor = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600, dir_fd=directory_fd)
        os.lseek(source_fd, 0, os.SEEK_SET)
        for block in iter(lambda: os.read(source_fd, 1024 * 1024), b""):
            checksum.update(block)
            write_all(descriptor, block)
        os.fsync(descriptor)
        os.close(descriptor)
        descriptor = -1
        os.link(temporary, name, src_dir_fd=directory_fd, dst_dir_fd=directory_fd, follow_symlinks=False)
        linked = True
        failure = os.environ.get("SWITCHYARD_RESTORE_TEST_PUBLISH_FAILURE")
        if os.environ.get("SWITCHYARD_RESTORE_TESTING") == "1" and failure in {"after_link", f"after_{name}"}:
            raise OSError("injected publication failure")
        os.unlink(temporary, dir_fd=directory_fd)
        os.fsync(directory_fd)
    except BaseException:
        if descriptor >= 0:
            os.close(descriptor)
        try:
            os.unlink(temporary, dir_fd=directory_fd)
        except FileNotFoundError:
            pass
        if linked:
            try:
                os.unlink(name, dir_fd=directory_fd)
                os.fsync(directory_fd)
            except FileNotFoundError:
                pass
        raise
    return checksum.hexdigest()


def publish_bytes_noclobber_fd(directory_fd: int, name: str, data: bytes) -> str:
    source_fd, source_name = tempfile.mkstemp()
    try:
        write_all(source_fd, data)
        return publish_fd_noclobber_fd(directory_fd, name, source_fd)
    finally:
        os.close(source_fd)
        os.unlink(source_name)


def publish_json_noclobber_fd(directory_fd: int, name: str, value: dict[str, Any]) -> str:
    data = (json.dumps(value, sort_keys=True, separators=(",", ":")) + "\n").encode()
    return publish_bytes_noclobber_fd(directory_fd, name, data)
 

def publish_bundle(args: argparse.Namespace) -> int:
    policy_path = Path(args.policy)
    safe_input_file(policy_path, "policy")
    policy_fd = os.open(policy_path, os.O_RDONLY | os.O_NOFOLLOW)
    policy_value, _ = read_fd_json(policy_fd, "policy")
    policy = validate_policy(policy_value)
    policy_digest = fd_digest(policy_fd)
    contract_fd = os.open(OBJECT_CONTRACT, os.O_RDONLY | os.O_NOFOLLOW)
    contract_value, _ = read_fd_json(contract_fd, "bundle")
    contract = validate_contract(contract_value)
    contract_digest = fd_digest(contract_fd)
    archive_tmp = Path(args.archive_temp)
    output_dir = Path(args.output_dir)
    output_fd = open_directory(output_dir, "bundle")
    archive_parent_fd = open_directory(archive_tmp.parent, "bundle")
    if os.fstat(archive_parent_fd).st_ino != os.fstat(output_fd).st_ino or os.fstat(archive_parent_fd).st_dev != os.fstat(output_fd).st_dev:
        raise ContractError("bundle", "archive_not_in_backup_directory")
    archive_fd = open_regular_at(output_fd, archive_tmp.name, "bundle")
    if not SAFE_ID.fullmatch(args.bundle_id) or not SAFE_ID.fullmatch(args.archive_basename) or PLACEHOLDER.search(args.bundle_id) or PLACEHOLDER.search(args.archive_basename):
        raise ContractError("bundle", "unsafe_identifier")
    if not REVISION.fullmatch(args.source_revision):
        raise ContractError("bundle", "invalid_revision")
    if not POSTGRES_VERSION.fullmatch(args.database_version):
        raise ContractError("bundle", "invalid_database_version")
    created_at = timestamp(parse_timestamp(args.created_at))
    try:
        ledger = [line.rstrip("\n") for line in Path(args.migration_ledger).read_text(encoding="utf-8").splitlines()]
    except (OSError, UnicodeError) as exc:
        raise ContractError("bundle", "invalid_migration_ledger") from exc
    if ledger != contract["migration_ledger"]:
        raise ContractError("bundle", "migration_ledger_mismatch")
    os.fsync(archive_fd)
    archive_digest = fd_digest(archive_fd)
    archive_name = args.archive_basename
    policy_name = f"{args.bundle_id}.policy.json"
    contract_name = f"{args.bundle_id}.object-contract.json"
    metadata_name = f"{args.bundle_id}.metadata.json"
    manifest_name = f"{args.bundle_id}.complete.json"
    names = (archive_name, policy_name, contract_name, metadata_name, manifest_name)
    for name in names:
        try:
            os.stat(name, dir_fd=output_fd, follow_symlinks=False)
        except FileNotFoundError:
            continue
        raise ContractError("bundle", "bundle_exists")
    metadata = {
        "contract_version": "switchyard.postgres-backup-metadata.v1",
        "bundle_id": args.bundle_id,
        "provider": "postgres",
        "created_at": created_at,
        "source_revision": args.source_revision,
        "database_version": args.database_version,
        "archive_basename": archive_name,
        "archive_format": "postgres-custom",
        "archive_sha256": archive_digest,
        "policy_id": policy["policy_id"],
        "policy_version": policy["policy_version"],
        "policy_sha256": policy_digest,
        "object_contract_version": contract["contract_version"],
        "object_contract_sha256": contract_digest,
        "migration_head": contract["migration_head"],
        "migration_ledger": ledger,
    }
    written: list[str] = []
    try:
        if publish_fd_noclobber_fd(output_fd, archive_name, archive_fd) != archive_digest:
            raise ContractError("bundle", "archive_changed_during_publication")
        written.append(archive_name)
        if publish_fd_noclobber_fd(output_fd, policy_name, policy_fd) != policy_digest:
            raise ContractError("bundle", "policy_changed_during_publication")
        written.append(policy_name)
        if publish_fd_noclobber_fd(output_fd, contract_name, contract_fd) != contract_digest:
            raise ContractError("bundle", "contract_changed_during_publication")
        written.append(contract_name)
        metadata_bytes = (json.dumps(metadata, sort_keys=True, separators=(",", ":")) + "\n").encode()
        publish_bytes_noclobber_fd(output_fd, metadata_name, metadata_bytes)
        written.append(metadata_name)
        manifest = {
            "contract_version": "switchyard.completed-backup-bundle.v1",
            "bundle_id": args.bundle_id,
            "completed_at": created_at,
            "archive": {"basename": archive_name, "sha256": archive_digest},
            "metadata": {"basename": metadata_name, "sha256": hashlib.sha256(metadata_bytes).hexdigest()},
            "policy": {"basename": policy_name, "sha256": policy_digest},
            "object_contract": {"basename": contract_name, "sha256": contract_digest},
        }
        os.fsync(output_fd)
        publish_json_noclobber_fd(output_fd, manifest_name, manifest)
        written.append(manifest_name)
        os.unlink(archive_tmp.name, dir_fd=output_fd)
        os.fsync(output_fd)
    except BaseException:
        for name in reversed(written):
            try:
                os.unlink(name, dir_fd=output_fd)
            except FileNotFoundError:
                pass
        os.fsync(output_fd)
        raise
    finally:
        os.close(policy_fd)
        os.close(contract_fd)
        os.close(archive_fd)
        os.close(archive_parent_fd)
        os.close(output_fd)
    print(f"bundle_id={args.bundle_id}\narchive={archive_name}\nmanifest={manifest_name}\nsha256={archive_digest}")
    return 0


def file_entry(value: Any, stage: str) -> tuple[str, str]:
    if type(value) is not dict:
        raise ContractError(stage, "invalid_manifest")
    exact_object(value, {"basename", "sha256"}, stage)
    name, checksum = value["basename"], value["sha256"]
    if not isinstance(name, str) or not SAFE_ID.fullmatch(name) or Path(name).name != name or PLACEHOLDER.search(name):
        raise ContractError(stage, "unsafe_basename")
    if not isinstance(checksum, str) or not SHA256.fullmatch(checksum):
        raise ContractError(stage, "invalid_checksum")
    return name, checksum


def validate_bundle(manifest_path: Path, staging_dir: Path | None = None, source_directory_fd: int | None = None) -> dict[str, Any]:
    parent = manifest_path.parent
    directory_fd = os.dup(source_directory_fd) if source_directory_fd is not None else open_directory(parent, "bundle")
    descriptors: dict[str, int] = {}
    try:
        manifest_fd = open_regular_at(directory_fd, manifest_path.name, "bundle")
        descriptors["manifest"] = manifest_fd
        manifest, _ = read_fd_json(manifest_fd, "bundle")
        exact_object(manifest, {"contract_version", "bundle_id", "completed_at", "archive", "metadata", "policy", "object_contract"}, "bundle")
        if manifest["contract_version"] != "switchyard.completed-backup-bundle.v1" or not isinstance(manifest["bundle_id"], str) or not SAFE_ID.fullmatch(manifest["bundle_id"]) or PLACEHOLDER.search(manifest["bundle_id"]):
            raise ContractError("bundle", "invalid_manifest")
        completed_at = parse_timestamp(manifest["completed_at"])
        entries: dict[str, tuple[str, str]] = {}
        for key in ("archive", "metadata", "policy", "object_contract"):
            name, expected = file_entry(manifest[key], "bundle")
            descriptor = open_regular_at(directory_fd, name, "bundle")
            descriptors[key] = descriptor
            if fd_digest(descriptor) != expected:
                raise ContractError("bundle", f"{key}_checksum_mismatch")
            entries[key] = (name, expected)

        policy_value, _ = read_fd_json(descriptors["policy"], "policy")
        archived_policy = validate_policy(policy_value)
        archived_digest = fd_digest(descriptors["policy"])
        metadata, _ = read_fd_json(descriptors["metadata"], "bundle")
        contract_value, _ = read_fd_json(descriptors["object_contract"], "bundle")
        contract = validate_contract(contract_value)
        contract_digest = fd_digest(descriptors["object_contract"])
    finally:
        pass

    metadata_keys = {
        "contract_version", "bundle_id", "provider", "created_at", "source_revision", "database_version",
        "archive_basename", "archive_format", "archive_sha256", "policy_id", "policy_version", "policy_sha256",
        "object_contract_version", "object_contract_sha256", "migration_head", "migration_ledger",
    }
    exact_object(metadata, metadata_keys, "bundle")
    checks = (
        metadata["contract_version"] == "switchyard.postgres-backup-metadata.v1",
        metadata["bundle_id"] == manifest["bundle_id"],
        metadata["provider"] == archived_policy["provider"] == "postgres",
        metadata["created_at"] == manifest["completed_at"],
        metadata["archive_basename"] == manifest["archive"]["basename"],
        metadata["archive_sha256"] == manifest["archive"]["sha256"],
        metadata["policy_id"] == archived_policy["policy_id"],
        metadata["policy_version"] == archived_policy["policy_version"],
        metadata["policy_sha256"] == archived_digest,
        metadata["object_contract_version"] == contract["contract_version"],
        metadata["object_contract_sha256"] == contract_digest,
        metadata["migration_head"] == contract["migration_head"],
        metadata["migration_ledger"] == contract["migration_ledger"],
        metadata["archive_format"] == "postgres-custom",
    )
    if not all(checks) or not isinstance(metadata["source_revision"], str) or not REVISION.fullmatch(metadata["source_revision"]):
        raise ContractError("bundle", "bundle_binding_mismatch")
    if not isinstance(metadata["database_version"], str) or not POSTGRES_VERSION.fullmatch(metadata["database_version"]):
        raise ContractError("bundle", "invalid_database_version")
    paths = {key: parent / entries[key][0] for key in entries}
    try:
        if staging_dir is not None:
            staging_dir.mkdir(mode=0o700)
            staging_fd = open_directory(staging_dir, "bundle")
            try:
                staged: dict[str, Path] = {}
                for key in ("archive", "metadata", "object_contract"):
                    name, expected = entries[key]
                    current = os.stat(name, dir_fd=directory_fd, follow_symlinks=False)
                    opened = os.fstat(descriptors[key])
                    if (current.st_dev, current.st_ino) != (opened.st_dev, opened.st_ino):
                        raise ContractError("bundle", f"{key}_replaced_during_validation")
                    target_name = {"archive": "archive.dump", "metadata": "metadata.json", "object_contract": "object-contract.json"}[key]
                    target_fd = os.open(target_name, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o400, dir_fd=staging_fd)
                    checksum = hashlib.sha256()
                    try:
                        os.lseek(descriptors[key], 0, os.SEEK_SET)
                        while True:
                            block = os.read(descriptors[key], 1024 * 1024)
                            if not block:
                                break
                            checksum.update(block)
                            write_all(target_fd, block)
                        os.fsync(target_fd)
                    finally:
                        os.close(target_fd)
                    if checksum.hexdigest() != expected:
                        raise ContractError("bundle", f"{key}_checksum_mismatch")
                    staged[key] = staging_dir / target_name
                os.fsync(staging_fd)
                os.chmod(staging_dir, 0o500)
                paths.update(staged)
            finally:
                os.close(staging_fd)
        return {"manifest": manifest, "metadata": metadata, "policy": archived_policy, "paths": paths, "created_at": completed_at, "contract": contract}
    finally:
        for descriptor in descriptors.values():
            os.close(descriptor)
        os.close(directory_fd)


def bounded_names(value: Any, pattern: re.Pattern[str], *, nullable: bool = False) -> list[str] | None:
    if value is None and nullable:
        return None
    if type(value) is not list or len(value) > MAX_PROVIDER_ITEMS:
        raise ContractError("restore", "invalid_provider_payload")
    if any(type(item) is not str or len(item) > 128 or not pattern.fullmatch(item) for item in value):
        raise ContractError("restore", "invalid_provider_payload")
    if value != sorted(set(value)):
        raise ContractError("restore", "invalid_provider_payload")
    return value


def bounded_counts(value: Any, *, nullable: bool = False) -> dict[str, int] | None:
    if value is None and nullable:
        return None
    if type(value) is not dict or len(value) > MAX_PROVIDER_ITEMS:
        raise ContractError("restore", "invalid_row_counts")
    for name, count in value.items():
        if type(name) is not str or not OBJECT_NAME.fullmatch(name) or type(count) is not int or not 0 <= count <= MAX_COUNT:
            raise ContractError("restore", "invalid_row_counts")
    return value


def validate_provider_payload(payload: dict[str, Any], contract: dict[str, Any]) -> tuple[str, str | None]:
    keys = {"contract_version", "provider", "result", "stage", "error_code", "database_version", "migration_before", "required_objects", "row_counts", "migration_after", "api_ready", "api_auth_workflows"}
    exact_object(payload, keys, "restore")
    if payload["contract_version"] != "switchyard.postgres-restore-payload.v1" or payload["provider"] != "postgres":
        raise ContractError("restore", "invalid_provider_payload")
    if payload["database_version"] is not None and (not isinstance(payload["database_version"], str) or not POSTGRES_VERSION.fullmatch(payload["database_version"])):
        raise ContractError("restore", "invalid_database_version")
    provider_stages = {"restore", "pre_api", "api", "cleanup", "complete"}
    if type(payload["result"]) is not str or type(payload["stage"]) is not str or payload["result"] not in ("success", "failure") or payload["stage"] not in provider_stages:
        raise ContractError("restore", "invalid_provider_payload")
    error = payload["error_code"]
    if error is not None and (type(error) is not str or error not in PROVIDER_ERROR_CODES):
        raise ContractError("restore", "invalid_provider_error")
    if type(payload["api_ready"]) is not bool or type(payload["api_auth_workflows"]) is not bool:
        raise ContractError("restore", "invalid_provider_payload")
    before = bounded_names(payload["migration_before"], MIGRATION, nullable=True)
    objects = bounded_names(payload["required_objects"], OBJECT_NAME, nullable=True)
    counts = bounded_counts(payload["row_counts"], nullable=True)
    after = bounded_names(payload["migration_after"], MIGRATION, nullable=True)
    if payload["result"] == "success":
        if payload["stage"] != "complete" or error is not None:
            raise ContractError("restore", "invalid_provider_result")
        if payload["database_version"] is None or before is None or objects is None or counts is None or after is None:
            raise ContractError("restore", "invalid_provider_result")
        active_contract = validate_contract(load_json(OBJECT_CONTRACT, "bundle"))
        if before != contract["migration_ledger"] or after != active_contract["migration_ledger"]:
            raise ContractError("pre_api", "migration_ledger_mismatch")
        if objects != contract["required_objects"]:
            raise ContractError("pre_api", "required_object_mismatch")
        if set(counts) != set(contract["required_objects"]):
            raise ContractError("pre_api", "invalid_row_counts")
        if payload["api_ready"] is not True or payload["api_auth_workflows"] is not True:
            raise ContractError("api", "api_check_failed")
        return "complete", None
    if payload["stage"] == "complete" or error is None:
        raise ContractError("restore", "invalid_provider_result")
    stage = payload["stage"]
    if error not in PROVIDER_STAGE_ERRORS[stage]:
        raise ContractError("restore", "invalid_provider_error")
    if stage == "restore" and any(item is not None for item in (payload["database_version"], before, objects, counts, after)):
        raise ContractError("restore", "invalid_provider_result")
    if stage in {"restore", "pre_api"} and (payload["api_ready"] or payload["api_auth_workflows"] or after is not None):
        raise ContractError("restore", "invalid_provider_result")
    if stage == "api" and (payload["database_version"] is None or before != contract["migration_ledger"] or objects != contract["required_objects"] or counts is None or set(counts) != set(contract["required_objects"]) or after is not None or payload["api_auth_workflows"]):
        raise ContractError("restore", "invalid_provider_result")
    return stage, error


def evidence_path(directory: Path, attempt_id: str) -> Path:
    return directory / f"restore-evidence-{attempt_id}.json"


def validate_evidence_bundle(bundle: Any) -> None:
    if type(bundle) is not dict:
        raise ContractError("cleanup", "invalid_evidence")
    exact_object(bundle, {"bundle_id", "backup_basename", "backup_sha256", "backup_created_at", "backup_age_seconds", "source_revision", "archived_policy_id", "archived_policy_version", "archived_policy_sha256", "active_policy_id", "active_policy_version", "active_policy_sha256", "object_contract_sha256", "bundle_valid_against_archived_policy"}, "cleanup")
    if (type(bundle["bundle_id"]) is not str or not SAFE_ID.fullmatch(bundle["bundle_id"]) or
            type(bundle["backup_basename"]) is not str or not SAFE_ID.fullmatch(bundle["backup_basename"]) or
            type(bundle["backup_sha256"]) is not str or not SHA256.fullmatch(bundle["backup_sha256"]) or
            type(bundle["source_revision"]) is not str or not REVISION.fullmatch(bundle["source_revision"]) or
            type(bundle["archived_policy_id"]) is not str or not SAFE_ID.fullmatch(bundle["archived_policy_id"]) or
            type(bundle["active_policy_id"]) is not str or not SAFE_ID.fullmatch(bundle["active_policy_id"]) or
            type(bundle["archived_policy_version"]) is not int or bundle["archived_policy_version"] < 1 or
            type(bundle["active_policy_version"]) is not int or bundle["active_policy_version"] < 1 or
            any(type(bundle[field]) is not str or not SHA256.fullmatch(bundle[field]) for field in ("archived_policy_sha256", "active_policy_sha256", "object_contract_sha256")) or
            type(bundle["backup_age_seconds"]) is not int or bundle["backup_age_seconds"] < 0 or bundle["bundle_valid_against_archived_policy"] is not True):
        raise ContractError("cleanup", "invalid_evidence")
    parse_timestamp(bundle["backup_created_at"], "cleanup")


def validate_evidence(value: dict[str, Any]) -> None:
    exact_object(value, {"contract_version", "attempt_id", "started_at", "finished_at", "provider", "result", "stage", "error_code", "bundle", "qualification", "provider_payload"}, "cleanup")
    if value["contract_version"] != "switchyard.restore-evidence.v1" or type(value["attempt_id"]) is not str or not SAFE_ID.fullmatch(value["attempt_id"]):
        raise ContractError("cleanup", "invalid_evidence")
    parse_timestamp(value["started_at"], "cleanup")
    parse_timestamp(value["finished_at"], "cleanup")
    if value["provider"] not in (None, "postgres") or value["result"] not in ("success", "failure") or value["stage"] not in STAGES:
        raise ContractError("cleanup", "invalid_evidence")
    error = value["error_code"]
    if error is not None and (type(error) is not str or not ERROR_CODE.fullmatch(error)):
        raise ContractError("cleanup", "invalid_evidence")
    if value["bundle"] is not None:
        validate_evidence_bundle(value["bundle"])
    if value["result"] == "success":
        if value["provider"] != "postgres" or value["stage"] != "complete" or error is not None or type(value["bundle"]) is not dict or type(value["qualification"]) is not dict or type(value["provider_payload"]) is not dict:
            raise ContractError("cleanup", "invalid_evidence_result")
        bundle = value["bundle"]
        exact_object(bundle, {"bundle_id", "backup_basename", "backup_sha256", "backup_created_at", "backup_age_seconds", "source_revision", "archived_policy_id", "archived_policy_version", "archived_policy_sha256", "active_policy_id", "active_policy_version", "active_policy_sha256", "object_contract_sha256", "bundle_valid_against_archived_policy"}, "cleanup")
        if (type(bundle["bundle_id"]) is not str or not SAFE_ID.fullmatch(bundle["bundle_id"]) or
                type(bundle["backup_basename"]) is not str or not SAFE_ID.fullmatch(bundle["backup_basename"]) or
                type(bundle["backup_sha256"]) is not str or not SHA256.fullmatch(bundle["backup_sha256"]) or
                type(bundle["source_revision"]) is not str or not REVISION.fullmatch(bundle["source_revision"]) or
                type(bundle["archived_policy_id"]) is not str or not SAFE_ID.fullmatch(bundle["archived_policy_id"]) or
                type(bundle["active_policy_id"]) is not str or not SAFE_ID.fullmatch(bundle["active_policy_id"]) or
                type(bundle["archived_policy_version"]) is not int or bundle["archived_policy_version"] < 1 or
                type(bundle["active_policy_version"]) is not int or bundle["active_policy_version"] < 1 or
                any(type(bundle[field]) is not str or not SHA256.fullmatch(bundle[field]) for field in ("archived_policy_sha256", "active_policy_sha256", "object_contract_sha256")) or
                type(bundle["backup_age_seconds"]) is not int or bundle["backup_age_seconds"] < 0 or bundle["bundle_valid_against_archived_policy"] is not True):
            raise ContractError("cleanup", "invalid_evidence")
        parse_timestamp(bundle["backup_created_at"], "cleanup")
        qualification = value["qualification"]
        exact_object(qualification, {"active_policy_matches_archived_policy", "backup_age_within_active_declared_rpo", "isolated_drill_duration_seconds", "isolated_drill_within_active_declared_rto", "current_policy_qualified", "measurement_scope"}, "cleanup")
        if (any(type(qualification[field]) is not bool for field in ("active_policy_matches_archived_policy", "backup_age_within_active_declared_rpo", "isolated_drill_within_active_declared_rto", "current_policy_qualified")) or
                type(qualification["isolated_drill_duration_seconds"]) is not int or qualification["isolated_drill_duration_seconds"] < 0 or
                qualification["measurement_scope"] != "isolated_restore_drill_not_production_recovery_proof"):
            raise ContractError("cleanup", "invalid_evidence")
        expected_qualified = (qualification["active_policy_matches_archived_policy"] and
                              qualification["backup_age_within_active_declared_rpo"] and
                              qualification["isolated_drill_within_active_declared_rto"])
        if qualification["current_policy_qualified"] is not expected_qualified:
            raise ContractError("cleanup", "invalid_evidence")
        payload = value["provider_payload"]
        exact_object(payload, {"contract_version", "provider", "result", "stage", "error_code", "database_version", "migration_before", "required_objects", "row_counts", "migration_after", "api_ready", "api_auth_workflows"}, "cleanup")
        if (payload["contract_version"] != "switchyard.postgres-restore-payload.v1" or payload["provider"] != "postgres" or
                payload["result"] != "success" or payload["stage"] != "complete" or payload["error_code"] is not None or
                type(payload["database_version"]) is not str or not POSTGRES_VERSION.fullmatch(payload["database_version"]) or
                payload["api_ready"] is not True or payload["api_auth_workflows"] is not True):
            raise ContractError("cleanup", "invalid_evidence")
        before = bounded_names(payload["migration_before"], MIGRATION)
        objects = bounded_names(payload["required_objects"], OBJECT_NAME)
        counts = bounded_counts(payload["row_counts"])
        after = bounded_names(payload["migration_after"], MIGRATION)
        if not before or not objects or not counts or not after or set(objects) != set(counts):
            raise ContractError("cleanup", "invalid_evidence")
    elif value["stage"] == "complete" or error is None or value["qualification"] is not None or value["provider_payload"] is not None:
        raise ContractError("cleanup", "invalid_evidence_result")


def write_json_at(directory_fd: int, name: str, value: dict[str, Any]) -> None:
    descriptor = os.open(name, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600, dir_fd=directory_fd)
    try:
        with os.fdopen(descriptor, "w", encoding="utf-8") as handle:
            json.dump(value, handle, sort_keys=True, separators=(",", ":"))
            handle.write("\n")
            handle.flush()
            os.fsync(handle.fileno())
        descriptor = -1
        os.fsync(directory_fd)
    finally:
        if descriptor >= 0:
            os.close(descriptor)


def remove_staging(path: Path) -> None:
    parent_fd = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        for current, directories, files in os.walk(path, topdown=False, followlinks=False):
            current_path = Path(current)
            for name in files:
                os.chmod(current_path / name, 0o600, follow_symlinks=False)
            for name in directories:
                os.chmod(current_path / name, 0o700, follow_symlinks=False)
            os.chmod(current_path, 0o700, follow_symlinks=False)
        shutil.rmtree(path)
        os.fsync(parent_fd)
        if os.environ.get("SWITCHYARD_RESTORE_TESTING") == "1" and os.environ.get("SWITCHYARD_RESTORE_TEST_CLEANUP_FAILURE") == "1":
            raise OSError("injected staging cleanup failure")
    finally:
        os.close(parent_fd)


def restore(args: argparse.Namespace) -> int:
    started = utc_now()
    attempt_id = started.strftime("%Y%m%dt%H%M%Sz").lower() + "-" + secrets.token_hex(6)
    evidence_dir = Path(args.evidence_dir)
    evidence_fd = open_directory(evidence_dir, "selection")
    provider: str | None = None
    bundle_evidence: dict[str, Any] | None = None
    qualification: dict[str, Any] | None = None
    provider_payload: dict[str, Any] | None = None
    candidate_payload: dict[str, Any] | None = None
    safe_stage, safe_error = "restore", None
    result, stage, error_code = "failure", "selection", "internal_error"
    started_monotonic = time.monotonic()
    staging_root: Path | None = None
    try:
        if not args.disposable:
            raise ContractError("selection", "disposable_mode_required")
        resolved_evidence = Path(os.readlink(f"/proc/self/fd/{evidence_fd}"))
        bundle_parent_path = Path(args.bundle).parent
        bundle_fd = open_directory(bundle_parent_path, "bundle")
        bundle_parent = Path(os.readlink(f"/proc/self/fd/{bundle_fd}"))
        if (resolved_evidence == ROOT or ROOT in resolved_evidence.parents or
                resolved_evidence == bundle_parent or resolved_evidence in bundle_parent.parents or bundle_parent in resolved_evidence.parents):
            raise ContractError("selection", "evidence_directory_not_dedicated")
        active_policy, active_digest = validate_policy_path(Path(args.policy))
        provider = active_policy["provider"]
        staging_root = Path(tempfile.mkdtemp(prefix="switchyard-restore-stage-"))
        os.chmod(staging_root, 0o700)
        try:
            bundle = validate_bundle(Path(args.bundle), staging_root / "bundle", bundle_fd)
        finally:
            os.close(bundle_fd)
        metadata = bundle["metadata"]
        if bundle["created_at"] > started:
            raise ContractError("bundle", "future_backup_timestamp")
        age = max(0, math.ceil((started - bundle["created_at"]).total_seconds()))
        bundle_evidence = {
            "bundle_id": bundle["manifest"]["bundle_id"],
            "backup_basename": metadata["archive_basename"],
            "backup_sha256": metadata["archive_sha256"],
            "backup_created_at": metadata["created_at"],
            "backup_age_seconds": age,
            "source_revision": metadata["source_revision"],
            "archived_policy_id": bundle["policy"]["policy_id"],
            "archived_policy_version": bundle["policy"]["policy_version"],
            "archived_policy_sha256": metadata["policy_sha256"],
            "active_policy_id": active_policy["policy_id"],
            "active_policy_version": active_policy["policy_version"],
            "active_policy_sha256": active_digest,
            "object_contract_sha256": metadata["object_contract_sha256"],
            "bundle_valid_against_archived_policy": True,
        }
        payload_path = staging_root / "provider.json"
        adapter = ROOT / "scripts/restore-postgres-drill.sh"
        if args.test_adapter:
            if os.environ.get("SWITCHYARD_RESTORE_TESTING") != "1":
                raise ContractError("selection", "test_adapter_forbidden")
            adapter = Path(args.test_adapter)
        safe_input_file(adapter, "restore")
        reclaim_seconds = active_policy["declared_rto_seconds"] + CLEANUP_GRACE_SECONDS
        command = [str(adapter), str(bundle["paths"]["archive"]), str(bundle["paths"]["metadata"]), str(bundle["paths"]["object_contract"]), str(payload_path), attempt_id, str(args.timeout_seconds), str(reclaim_seconds)]
        try:
            completed = subprocess.run(command, stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=args.timeout_seconds + 20, check=False)
        except subprocess.TimeoutExpired as exc:
            raise ContractError("restore", "adapter_timeout") from exc
        if payload_path.exists():
            candidate_payload = load_json(payload_path, "restore")
            payload_path.unlink()
            try:
                safe_stage, safe_error = validate_provider_payload(candidate_payload, bundle["contract"])
            except ContractError:
                provider_payload = None
                raise
            if candidate_payload["result"] == "success":
                provider_payload = candidate_payload
            else:
                provider_payload = None
        if completed.returncode != 0:
            if candidate_payload is not None and candidate_payload["result"] == "failure":
                raise ContractError(safe_stage, safe_error or "adapter_failed")
            provider_payload = None
            raise ContractError("restore", "adapter_failed")
        if provider_payload is None or provider_payload["result"] != "success":
            raise ContractError("restore", "missing_provider_payload")
        duration = max(0, math.ceil(time.monotonic() - started_monotonic))
        policy_match = metadata["policy_sha256"] == active_digest
        age_ok = age <= active_policy["declared_rpo_seconds"]
        duration_ok = duration <= active_policy["declared_rto_seconds"]
        qualification = {
            "active_policy_matches_archived_policy": policy_match,
            "backup_age_within_active_declared_rpo": age_ok,
            "isolated_drill_duration_seconds": duration,
            "isolated_drill_within_active_declared_rto": duration_ok,
            "current_policy_qualified": policy_match and age_ok and duration_ok,
            "measurement_scope": "isolated_restore_drill_not_production_recovery_proof",
        }
        result, stage, error_code = "success", "complete", None
    except ContractError as exc:
        stage, error_code = exc.stage, exc.code
    except (OSError, ValueError, subprocess.SubprocessError):
        stage, error_code = "restore", "internal_error"
    finally:
        if staging_root is not None:
            try:
                remove_staging(staging_root)
            except OSError:
                result, stage, error_code = "failure", "cleanup", "staging_cleanup_failed"
                qualification = None
                provider_payload = None
        finished = utc_now()
        evidence = {
            "contract_version": "switchyard.restore-evidence.v1",
            "attempt_id": attempt_id,
            "started_at": timestamp(started),
            "finished_at": timestamp(finished),
            "provider": provider,
            "result": result,
            "stage": stage,
            "error_code": error_code,
            "bundle": bundle_evidence,
            "qualification": qualification,
            "provider_payload": provider_payload,
        }
        validate_evidence(evidence)
        output_name = evidence_path(evidence_dir, attempt_id).name
        write_json_at(evidence_fd, output_name, evidence)
        os.close(evidence_fd)
        print(f"attempt_id={attempt_id}\nevidence={output_name}\nresult={result}\nstage={stage}")
    return 0 if result == "success" else 1


def parser() -> argparse.ArgumentParser:
    result = argparse.ArgumentParser()
    commands = result.add_subparsers(dest="command", required=True)
    policy = commands.add_parser("validate-policy")
    policy.add_argument("--policy", required=True)
    directory = commands.add_parser("prepare-directory")
    directory.add_argument("--directory", required=True)
    publish = commands.add_parser("publish-bundle")
    for option in ("policy", "archive-temp", "output-dir", "bundle-id", "archive-basename", "source-revision", "database-version", "migration-ledger", "created-at"):
        publish.add_argument(f"--{option}", required=True)
    drill = commands.add_parser("restore")
    drill.add_argument("--bundle", required=True)
    drill.add_argument("--policy", required=True)
    drill.add_argument("--evidence-dir", required=True)
    drill.add_argument("--test-adapter", help=argparse.SUPPRESS)
    drill.add_argument("--timeout-seconds", type=int, required=True)
    drill.add_argument("--disposable", action="store_true")
    return result


def main() -> int:
    args = parser().parse_args()
    try:
        if args.command == "validate-policy":
            _, value = validate_policy_path(Path(args.policy))
            print(value)
            return 0
        if args.command == "prepare-directory":
            prepare_private_directory(Path(args.directory))
            return 0
        if args.command == "publish-bundle":
            return publish_bundle(args)
        if args.timeout_seconds < 1:
            raise ContractError("selection", "invalid_timeout")
        return restore(args)
    except ContractError as exc:
        print(f"restore contract rejected: stage={exc.stage} code={exc.code}", file=sys.stderr)
        return 2
    except OSError:
        print("restore contract rejected: stage=bundle code=filesystem_error", file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
