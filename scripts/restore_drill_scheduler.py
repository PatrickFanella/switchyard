#!/usr/bin/env python3
"""Select and run the newest completed restore bundle on an operator schedule."""

from __future__ import annotations

import argparse
import ctypes
import datetime as dt
import fcntl
import hashlib
import math
import os
import signal
import stat
import subprocess
import sys
import tempfile
import time
from pathlib import Path

import restore_evidence

ROOT = Path(__file__).resolve().parent.parent
METRIC_NAMES = (
    "switchyard_restore_drill_last_attempt_timestamp_seconds",
    "switchyard_restore_drill_last_result",
    "switchyard_restore_drill_last_failure_timestamp_seconds",
    "switchyard_restore_drill_last_success_timestamp_seconds",
    "switchyard_restore_drill_next_deadline_timestamp_seconds",
)
MAX_METRICS_BYTES = 4096
ATTEMPT_OVERHEAD_SECONDS = 120
FINALIZATION_RESERVE_SECONDS = 30
SYSTEMD_BACKSTOP_SECONDS = 30
MAX_ATTEMPT_SECONDS = 24 * 60 * 60
MAX_RUNTIME_UID = 2147483647
PROCESS_GROUP_GRACE_SECONDS = 1
CLOCK_SKEW_ALLOWANCE_SECONDS = 300
PR_SET_CHILD_SUBREAPER = 36
LOCK_NAME = ".restore-drill-scheduler.lock"


class SchedulerError(Exception):
    pass


class AttemptTimeout(Exception):
    pass


class Sink:
    def __init__(self, directory: Path, metrics_file: Path | None = None):
        self.directory = directory
        self.metrics_file = metrics_file


def dotenv(path: Path) -> dict[str, str]:
    values: dict[str, str] = {}
    try:
        lines = path.read_text(encoding="utf-8").splitlines()
    except (OSError, UnicodeError) as exc:
        raise SchedulerError("invalid_environment") from exc
    for line in lines:
        if not line or line.startswith("#"):
            continue
        if "=" not in line:
            raise SchedulerError("invalid_environment")
        key, value = line.split("=", 1)
        if not key or not key.replace("_", "A").isalnum() or key in values or "\x00" in value or "\n" in value:
            raise SchedulerError("invalid_environment")
        values[key] = value
    return values


def settings(env_file: str | None) -> dict[str, str]:
    values = dict(os.environ)
    if env_file:
        values.update(dotenv(Path(env_file)))
    return values


def runtime_uid(values: dict[str, str]) -> int:
    raw = values.get("OPENCODE_RUNTIME_UID") or "1000"
    if not raw.isascii() or not raw.isdecimal():
        raise SchedulerError("invalid_runtime_uid")
    value = int(raw)
    if value < 1 or value > MAX_RUNTIME_UID:
        raise SchedulerError("invalid_runtime_uid")
    return value


def canonical_directory(raw: str, owner_uid: int) -> Path:
    path = Path(raw)
    if not path.is_absolute() or path != path.absolute():
        raise SchedulerError("unsafe_directory")
    fd = restore_evidence.open_directory(path, "selection")
    try:
        info = os.fstat(fd)
        resolved = Path(os.readlink(f"/proc/self/fd/{fd}"))
        if resolved != path or info.st_uid != owner_uid or info.st_mode & 0o077 or info.st_mode & 0o700 != 0o700:
            raise SchedulerError("unsafe_directory")
        return resolved
    finally:
        os.close(fd)


def probe_directory(directory: Path, publication: str) -> None:
    fd = restore_evidence.open_directory(directory, "selection")
    temporary = f".restore-drill-probe-{os.getpid()}-{os.urandom(6).hex()}.tmp"
    published = f".restore-drill-probe-{os.getpid()}-{os.urandom(6).hex()}.published"
    out = -1
    try:
        out = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600, dir_fd=fd)
        restore_evidence.write_all(out, b"probe\n")
        os.fsync(out)
        os.close(out)
        out = -1
        if publication == "link":
            os.link(temporary, published, src_dir_fd=fd, dst_dir_fd=fd, follow_symlinks=False)
        else:
            os.rename(temporary, published, src_dir_fd=fd, dst_dir_fd=fd)
        os.fsync(fd)
        os.unlink(published, dir_fd=fd)
        if publication == "link":
            os.unlink(temporary, dir_fd=fd)
        os.fsync(fd)
    finally:
        if out >= 0:
            os.close(out)
        for name in (temporary, published):
            try:
                os.unlink(name, dir_fd=fd)
            except FileNotFoundError:
                pass
        os.close(fd)


def evidence_sink(values: dict[str, str], owner_uid: int, probe: bool = True) -> Sink:
    raw = values.get("SWITCHYARD_RESTORE_DRILL_EVIDENCE_DIR")
    if not raw:
        raise SchedulerError("missing_setting")
    directory = canonical_directory(raw, owner_uid)
    if directory == ROOT or ROOT in directory.parents or directory in ROOT.parents:
        raise SchedulerError("directory_in_repository")
    if probe:
        probe_directory(directory, "link")
    return Sink(directory)


def metrics_sink(values: dict[str, str], owner_uid: int, probe: bool = True) -> Sink:
    raw_dir = values.get("SWITCHYARD_BACKUP_METRICS_DIR")
    raw_file = values.get("SWITCHYARD_RESTORE_DRILL_METRICS_FILE")
    if not raw_dir or not raw_file:
        raise SchedulerError("missing_setting")
    directory = canonical_directory(raw_dir, owner_uid)
    path = Path(raw_file)
    if not path.is_absolute() or path.parent != directory or path.name != "restore-drill.prom":
        raise SchedulerError("unsafe_metrics_file")
    if directory == ROOT or ROOT in directory.parents or directory in ROOT.parents:
        raise SchedulerError("directory_in_repository")
    try:
        info = path.lstat()
        if stat.S_ISLNK(info.st_mode) or not stat.S_ISREG(info.st_mode) or info.st_uid != owner_uid or info.st_mode & 0o077 or info.st_mode & 0o600 != 0o600:
            raise SchedulerError("unsafe_metrics_file")
    except FileNotFoundError:
        pass
    if probe:
        probe_directory(directory, "rename")
    return Sink(directory, path)


def backup_timeout(values: dict[str, str], owner_uid: int) -> tuple[Path, int]:
    required = ("SWITCHYARD_RESTORE_POLICY", "SWITCHYARD_BACKUP_DIR", "SWITCHYARD_RESTORE_TIMEOUT_SECONDS")
    if any(not values.get(name) for name in required):
        raise SchedulerError("missing_setting")
    backup = canonical_directory(values["SWITCHYARD_BACKUP_DIR"], owner_uid)
    if backup == ROOT or ROOT in backup.parents or backup in ROOT.parents:
        raise SchedulerError("directory_in_repository")
    try:
        timeout = int(values["SWITCHYARD_RESTORE_TIMEOUT_SECONDS"])
    except ValueError as exc:
        raise SchedulerError("invalid_timeout") from exc
    if timeout < 1 or timeout > restore_evidence.MAX_POLICY_SECONDS:
        raise SchedulerError("invalid_timeout")
    return backup, timeout


def policy_backup(values: dict[str, str], owner_uid: int) -> tuple[dict, Path, int]:
    backup, timeout = backup_timeout(values, owner_uid)
    try:
        policy, _ = restore_evidence.validate_policy_path(Path(values["SWITCHYARD_RESTORE_POLICY"]))
    except restore_evidence.ContractError as exc:
        raise SchedulerError("invalid_policy") from exc
    return policy, backup, timeout


def separate(paths: tuple[Path, ...]) -> bool:
    return all(left != right and left not in right.parents and right not in left.parents
               for index, left in enumerate(paths) for right in paths[index + 1:])


def attempt_timeout_seconds(policy: dict, restore_timeout: int) -> int:
    return min(MAX_ATTEMPT_SECONDS, max(policy["declared_rto_seconds"], restore_timeout) + ATTEMPT_OVERHEAD_SECONDS)


def preflight(values: dict[str, str], owner_uid: int) -> tuple[dict, Path, Path, Path, Path]:
    evidence = evidence_sink(values, owner_uid)
    metrics = metrics_sink(values, owner_uid)
    policy, backup, _ = policy_backup(values, owner_uid)
    if not separate((backup, evidence.directory, metrics.directory)):
        raise SchedulerError("directories_not_separate")
    return policy, backup, evidence.directory, metrics.directory, metrics.metrics_file  # type: ignore[return-value]


def parse_metrics(path: Path, now: int | None = None) -> dict[str, float]:
    try:
        info = path.lstat()
        if stat.S_ISLNK(info.st_mode) or not stat.S_ISREG(info.st_mode) or info.st_size > MAX_METRICS_BYTES:
            return {}
        fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
        try:
            data = os.read(fd, MAX_METRICS_BYTES + 1).decode("ascii")
        finally:
            os.close(fd)
    except (OSError, UnicodeError):
        return {}
    result: dict[str, float] = {}
    for line in data.splitlines():
        fields = line.split()
        if len(fields) != 2 or fields[0] not in METRIC_NAMES or fields[0] in result:
            return {}
        try:
            value = float(fields[1])
        except ValueError:
            return {}
        if not math.isfinite(value) or value < 0 or fields[0].endswith("last_result") and value not in (0, 1):
            return {}
        if now is not None and fields[0].endswith("timestamp_seconds"):
            if value > now + CLOCK_SKEW_ALLOWANCE_SECONDS:
                continue
            value = min(value, now)
        result[fields[0]] = value
    return result


def write_metrics(directory: Path, path: Path, values: dict[str, float]) -> None:
    fd = restore_evidence.open_directory(directory, "selection")
    temporary = f".{path.name}.{os.getpid()}.{os.urandom(6).hex()}.tmp"
    out = -1
    try:
        out = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600, dir_fd=fd)
        content = "".join(f"{name} {values[name]:.0f}\n" for name in METRIC_NAMES if name in values).encode("ascii")
        restore_evidence.write_all(out, content)
        os.fsync(out)
        os.close(out)
        out = -1
        os.rename(temporary, path.name, src_dir_fd=fd, dst_dir_fd=fd)
        os.fsync(fd)
    finally:
        if out >= 0:
            os.close(out)
        try:
            os.unlink(temporary, dir_fd=fd)
        except FileNotFoundError:
            pass
        os.close(fd)


def acquire_lock(sink: Sink) -> int:
    directory_fd = restore_evidence.open_directory(sink.directory, "selection")
    try:
        lock_fd = os.open(LOCK_NAME, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600, dir_fd=directory_fd)
        info = os.fstat(lock_fd)
        if (info.st_uid != os.getuid() or not stat.S_ISREG(info.st_mode) or info.st_mode & 0o077 or
                info.st_mode & 0o600 != 0o600):
            os.close(lock_fd)
            raise SchedulerError("unsafe_lock_file")
        fcntl.flock(lock_fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        return lock_fd
    finally:
        os.close(directory_fd)


def initialize(values: dict[str, str], owner_uid: int, now: int | None = None) -> None:
    policy, _, _, metrics_dir, metrics_file = preflight(values, owner_uid)
    lock_fd = acquire_lock(Sink(metrics_dir, metrics_file))
    try:
        current_time = now if now is not None else int(time.time())
        previous = parse_metrics(metrics_file, current_time)
        current = {}
        success_name = "switchyard_restore_drill_last_success_timestamp_seconds"
        if success_name in previous:
            current[success_name] = previous[success_name]
        current["switchyard_restore_drill_next_deadline_timestamp_seconds"] = current_time + policy["max_drill_interval_seconds"] + policy["alert_grace_seconds"]
        write_metrics(metrics_dir, metrics_file, current)
    finally:
        os.close(lock_fd)


def select_newest_info(backup: Path) -> tuple[Path, object, str, str]:
    directory_fd = restore_evidence.open_directory(backup, "selection")
    valid: list[tuple[object, str, str, str]] = []
    try:
        for name in os.listdir(directory_fd):
            if not name.endswith(".complete.json") or not restore_evidence.SAFE_ID.fullmatch(name.removesuffix(".complete.json")):
                continue
            try:
                bundle = restore_evidence.validate_bundle(backup / name, source_directory_fd=directory_fd)
            except restore_evidence.ContractError:
                continue
            valid.append((bundle["created_at"], bundle["manifest"]["bundle_id"], name, bundle["manifest_sha256"]))
    finally:
        os.close(directory_fd)
    if not valid:
        raise SchedulerError("no_complete_bundle")
    newest = max(valid)
    return backup / newest[2], newest[0], newest[1], newest[3]


def select_newest(backup: Path) -> Path:
    return select_newest_info(backup)[0]


def exact_attempt_evidence(directory: Path, attempt_id: str, expected_result: str, bundle_id: str | None,
                           policy_digest: str | None = None) -> bool:
    try:
        value = restore_evidence.load_json(directory / f"restore-evidence-{attempt_id}.json", "cleanup")
        restore_evidence.validate_evidence(value)
    except restore_evidence.ContractError:
        return False
    if value["attempt_id"] != attempt_id or value["result"] != expected_result:
        return False
    return bundle_id is None or value["bundle"] is not None and value["bundle"]["bundle_id"] == bundle_id and (
        policy_digest is None or value["bundle"]["active_policy_sha256"] == policy_digest)


def write_scheduler_failure(sink: Sink, policy: dict | None, attempt_id: str, started: dt.datetime,
                            stage: str, code: str) -> None:
    value = {
        "contract_version": "switchyard.restore-evidence.v1",
        "attempt_id": attempt_id,
        "started_at": restore_evidence.timestamp(started),
        "finished_at": restore_evidence.timestamp(restore_evidence.utc_now()),
        "provider": policy.get("provider") if policy else None,
        "result": "failure",
        "stage": stage if stage in restore_evidence.STAGES and stage != "complete" else "restore",
        "error_code": code if restore_evidence.ERROR_CODE.fullmatch(code) else "internal_error",
        "bundle": None,
        "qualification": None,
        "provider_payload": None,
    }
    restore_evidence.validate_evidence(value)
    fd = restore_evidence.open_directory(sink.directory, "selection")
    try:
        restore_evidence.write_json_at(fd, f"restore-evidence-{attempt_id}.json", value)
    finally:
        os.close(fd)


def arm_deadline(seconds: float) -> None:
    signal.setitimer(signal.ITIMER_REAL, max(0.001, seconds))


def process_group_exists(group: int) -> bool:
    try:
        os.killpg(group, 0)
        return True
    except ProcessLookupError:
        return False


def become_child_subreaper() -> None:
    libc = ctypes.CDLL(None, use_errno=True)
    if libc.prctl(PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0) != 0:
        error = ctypes.get_errno()
        raise OSError(error, os.strerror(error))


def reap_process_group(group: int) -> None:
    while True:
        try:
            os.waitpid(-group, 0)
        except ChildProcessError:
            return


def terminate_process_group(process: subprocess.Popen, grace: float = PROCESS_GROUP_GRACE_SECONDS) -> None:
    group = process.pid
    watched = (signal.SIGALRM, signal.SIGTERM, signal.SIGINT)
    handlers = {item: signal.getsignal(item) for item in watched}
    for item in watched:
        signal.signal(item, signal.SIG_IGN)
    try:
        try:
            os.killpg(group, signal.SIGTERM)
        except ProcessLookupError:
            pass
        deadline = time.monotonic() + grace
        while process_group_exists(group) and time.monotonic() < deadline:
            process.poll()
            time.sleep(min(0.02, max(0, deadline - time.monotonic())))
        if process_group_exists(group):
            try:
                os.killpg(group, signal.SIGKILL)
            except ProcessLookupError:
                pass
        process.wait()
        reap_process_group(group)
    finally:
        for item, handler in handlers.items():
            signal.signal(item, handler)


def run_command(command: list[str], timeout: float) -> int:
    become_child_subreaper()
    process = subprocess.Popen(command, stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                               start_new_session=True)
    try:
        return process.wait(timeout=timeout)
    except BaseException:
        terminate_process_group(process)
        raise


def stage_policy(path: Path, staging_root: Path) -> tuple[dict, str, Path]:
    restore_evidence.safe_input_file(path, "policy")
    descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    try:
        if not stat.S_ISREG(os.fstat(descriptor).st_mode):
            raise restore_evidence.ContractError("policy", "unsafe_file")
        policy, data = restore_evidence.read_fd_json(descriptor, "policy")
        policy = restore_evidence.validate_policy(policy)
        digest = hashlib.sha256(data).hexdigest()
        staged_path = staging_root / "active-policy.json"
        out = os.open(staged_path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o400)
        try:
            restore_evidence.write_all(out, data)
            os.fsync(out)
        finally:
            os.close(out)
    finally:
        os.close(descriptor)
    staged_policy, staged_digest = restore_evidence.validate_policy_path(staged_path)
    if staged_policy != policy or staged_digest != digest:
        raise SchedulerError("staged_policy_mismatch")
    return policy, digest, staged_path


def run(values: dict[str, str], owner_uid: int, restore_command: str | None = None, now: int | None = None) -> int:
    started = restore_evidence.utc_now()
    started_epoch = now if now is not None else int(started.timestamp())
    started_monotonic = time.monotonic()
    attempt_id = "scheduled-" + started.strftime("%Y%m%dt%H%M%Sz").lower() + "-" + os.urandom(6).hex()
    old_handler = signal.getsignal(signal.SIGALRM)
    old_term_handler = signal.getsignal(signal.SIGTERM)
    old_int_handler = signal.getsignal(signal.SIGINT)
    timeout_handler = lambda *_: (_ for _ in ()).throw(AttemptTimeout())
    signal.signal(signal.SIGALRM, timeout_handler)
    signal.signal(signal.SIGTERM, timeout_handler)
    signal.signal(signal.SIGINT, timeout_handler)
    arm_deadline(MAX_ATTEMPT_SECONDS)
    evidence: Sink | None = None
    metrics: Sink | None = None
    lock_fd: int | None = None
    guard_lock_fd: int | None = None
    lock_sink: Sink | None = None
    policy: dict | None = None
    policy_digest: str | None = None
    previous: dict[str, float] = {}
    result = 1
    stage, error = "selection", "internal_error"
    evidence_written = False
    staging_root: Path | None = None
    try:
        try:
            evidence = evidence_sink(values, owner_uid, probe=False)
        except (SchedulerError, restore_evidence.ContractError, OSError):
            pass
        try:
            metrics = metrics_sink(values, owner_uid, probe=False)
        except (SchedulerError, restore_evidence.ContractError, OSError):
            pass
        if metrics is None and evidence is None:
            return 1
        lock_sink = metrics or evidence
        assert lock_sink is not None
        try:
            lock_fd = acquire_lock(lock_sink)  # type: ignore[arg-type]
        except BlockingIOError:
            alternate = evidence if lock_sink is metrics else metrics
            if alternate is not None:
                try:
                    probe_directory(alternate.directory, "link" if alternate is evidence else "rename")
                    write_scheduler_failure(alternate, None, attempt_id, started, "selection", "concurrent_attempt")
                except (OSError, restore_evidence.ContractError):
                    pass
            return 1

        try:
            probe_directory(lock_sink.directory, "rename" if lock_sink is metrics else "link")
        except (OSError, restore_evidence.ContractError):
            if lock_sink is metrics and evidence is not None:
                guard_lock_fd = lock_fd
                lock_fd = None
                metrics = None
                lock_sink = evidence
                try:
                    lock_fd = acquire_lock(evidence)
                    probe_directory(evidence.directory, "link")
                except (BlockingIOError, OSError, restore_evidence.ContractError):
                    evidence = None
                    return 1
            else:
                if lock_sink is evidence:
                    evidence = None
                else:
                    metrics = None
        if evidence is not None and lock_sink is not evidence:
            try:
                probe_directory(evidence.directory, "link")
            except (OSError, restore_evidence.ContractError):
                evidence = None

        if metrics is not None:
            previous = parse_metrics(metrics.metrics_file, started_epoch)  # type: ignore[arg-type]
        backup, restore_timeout = backup_timeout(values, owner_uid)
        staging_root = Path(tempfile.mkdtemp(prefix="switchyard-scheduled-restore-"))
        os.chmod(staging_root, 0o700)
        policy, policy_digest, staged_policy = stage_policy(Path(values["SWITCHYARD_RESTORE_POLICY"]), staging_root)
        usable_paths = [backup]
        if evidence is not None:
            usable_paths.append(evidence.directory)
        if metrics is not None:
            usable_paths.append(metrics.directory)
        if not separate(tuple(usable_paths)):
            raise SchedulerError("directories_not_separate")
        if evidence is None or metrics is None:
            raise SchedulerError("unusable_sink")
        budget = attempt_timeout_seconds(policy, restore_timeout)
        arm_deadline(budget - (time.monotonic() - started_monotonic))
        testing_override = restore_command is not None and os.environ.get("SWITCHYARD_RESTORE_SCHEDULER_TESTING") == "1"
        if restore_command is not None and not testing_override:
            raise SchedulerError("test_restore_command_forbidden")
        manifest, selected_at, selected_id, selected_digest = select_newest_info(backup)
        backup_fd = restore_evidence.open_directory(backup, "selection")
        try:
            staged = restore_evidence.validate_bundle(manifest, staging_root / "bundle", backup_fd)
        finally:
            os.close(backup_fd)
        if (staged["created_at"] != selected_at or staged["manifest"]["bundle_id"] != selected_id or
                staged["manifest_sha256"] != selected_digest):
            raise SchedulerError("selected_bundle_replaced")
        os.chmod(staging_root, 0o500)
        command = [restore_command or str(ROOT / "scripts/restore_evidence.py"), "restore", "--bundle", str(staged["paths"]["manifest"]),
                   "--policy", str(staged_policy), "--evidence-dir", str(evidence.directory),
                   "--timeout-seconds", str(restore_timeout), "--attempt-id", attempt_id,
                   "--started-at", restore_evidence.timestamp(started), "--disposable"]
        remaining = max(0.001, budget - FINALIZATION_RESERVE_SECONDS - (time.monotonic() - started_monotonic))
        result = run_command(command, remaining)
        expected = "success" if result == 0 else "failure"
        if not exact_attempt_evidence(evidence.directory, attempt_id, expected, selected_id, policy_digest):
            result, stage, error = 1, "restore", "missing_restore_evidence"
        else:
            evidence_written = True
    except AttemptTimeout:
        result, stage, error = 1, "restore", "scheduler_timeout"
    except subprocess.TimeoutExpired:
        result, stage, error = 1, "restore", "scheduler_timeout"
    except SchedulerError as exc:
        result, stage, error = 1, "selection", str(exc)
    except restore_evidence.ContractError as exc:
        result, stage, error = 1, exc.stage, exc.code
    except (OSError, subprocess.SubprocessError, ValueError):
        result, stage, error = 1, "restore", "scheduler_command_failed"
    finally:
        if staging_root is not None:
            try:
                restore_evidence.remove_staging(staging_root)
            except AttemptTimeout:
                result, stage, error = 1, "restore", "scheduler_timeout"
            except OSError:
                result, stage, error = 1, "cleanup", "scheduler_cleanup_failed"
        if result != 0 and evidence is not None and not evidence_written:
            try:
                if not exact_attempt_evidence(evidence.directory, attempt_id, "failure", None):
                    write_scheduler_failure(evidence, policy, attempt_id, started, stage, error)
            except AttemptTimeout:
                result, stage, error = 1, "restore", "scheduler_timeout"
            except (OSError, restore_evidence.ContractError):
                pass
        if metrics is not None and lock_sink is metrics and lock_fd is not None:
            try:
                attempted = started_epoch
                success_name = "switchyard_restore_drill_last_success_timestamp_seconds"
                updated = {success_name: previous[success_name]} if success_name in previous else {}
                updated["switchyard_restore_drill_last_attempt_timestamp_seconds"] = attempted
                updated["switchyard_restore_drill_last_result"] = 1 if result == 0 else 0
                if policy is not None:
                    updated["switchyard_restore_drill_next_deadline_timestamp_seconds"] = attempted + policy["max_drill_interval_seconds"] + policy["alert_grace_seconds"]
                updated["switchyard_restore_drill_last_success_timestamp_seconds" if result == 0 else "switchyard_restore_drill_last_failure_timestamp_seconds"] = attempted
                write_metrics(metrics.directory, metrics.metrics_file, updated)  # type: ignore[arg-type]
            except (AttemptTimeout, OSError, restore_evidence.ContractError):
                result = 1
        if lock_fd is not None:
            os.close(lock_fd)
        if guard_lock_fd is not None:
            os.close(guard_lock_fd)
        signal.setitimer(signal.ITIMER_REAL, 0)
        signal.signal(signal.SIGALRM, old_handler)
        signal.signal(signal.SIGTERM, old_term_handler)
        signal.signal(signal.SIGINT, old_int_handler)
    return 0 if result == 0 else 1


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("command", choices=("preflight", "initialize-deadline", "run"))
    parser.add_argument("--env-file")
    parser.add_argument("--owner-uid", type=int, default=os.getuid())
    parser.add_argument("--restore-command", help=argparse.SUPPRESS)
    args = parser.parse_args()
    try:
        values = settings(args.env_file)
        if args.command == "preflight":
            policy, _, _, _, _ = preflight(values, args.owner_uid)
            _, _, timeout = policy_backup(values, args.owner_uid)
            expected_uid = runtime_uid(values)
            print(policy["drill_calendar"])
            print(attempt_timeout_seconds(policy, timeout) + SYSTEMD_BACKSTOP_SECONDS)
            print(expected_uid)
            return 0
        if args.command == "initialize-deadline":
            initialize(values, args.owner_uid)
            return 0
        return run(values, args.owner_uid, args.restore_command)
    except (SchedulerError, restore_evidence.ContractError, OSError):
        print("restore drill scheduler rejected configuration", file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
