#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
run_user="${SWITCHYARD_RUN_USER:-${SUDO_USER:-$(id -un)}}"
run_group="${SWITCHYARD_RUN_GROUP:-$(id -gn "$run_user")}"
env_file="${SWITCHYARD_ENV_FILE:-$repo_root/.env}"
unit_src="$repo_root/deploy/switchyard.service"
backup_unit_src="$repo_root/deploy/switchyard-backup.service"
backup_timer_src="$repo_root/deploy/switchyard-backup.timer"
restore_unit_src="$repo_root/deploy/switchyard-restore-drill.service"
restore_timer_src="$repo_root/deploy/switchyard-restore-drill.timer"
unit_dir="${SWITCHYARD_SYSTEMD_UNIT_DIR:-/etc/systemd/system}"
unit_dst="$unit_dir/switchyard.service"
backup_unit_dst="$unit_dir/switchyard-backup.service"
backup_timer_dst="$unit_dir/switchyard-backup.timer"
restore_unit_dst="$unit_dir/switchyard-restore-drill.service"
restore_timer_dst="$unit_dir/switchyard-restore-drill.timer"

if [[ "$(id -u)" -ne 0 ]]; then
  echo "install-systemd.sh must be run with sudo/root" >&2
  exit 1
fi

if ! command -v docker >/dev/null 2>&1; then
  echo "docker is required" >&2
  exit 1
fi

if [[ ! -f "$repo_root/docker-compose.yml" ]]; then
  echo "missing docker-compose.yml under $repo_root" >&2
  exit 1
fi

if [[ "${SWITCHYARD_INSTALL_BACKUP_TIMER:-0}" == "1" ]]; then
  if [[ ! -f "$repo_root/.env" ]] || ! grep -Eq '^SWITCHYARD_BACKUP_DIR=.+$' "$repo_root/.env" || ! grep -Eq '^SWITCHYARD_BACKUP_POLICY=.+$' "$repo_root/.env"; then
    echo "refusing to enable backup timer without SWITCHYARD_BACKUP_DIR and SWITCHYARD_BACKUP_POLICY in .env" >&2
    exit 1
  fi
  policy_path=$(python3 - "$repo_root/.env" <<'PY'
import pathlib, sys
for line in pathlib.Path(sys.argv[1]).read_text(encoding="utf-8").splitlines():
    if line.startswith("SWITCHYARD_BACKUP_POLICY="):
        print(line.split("=", 1)[1])
        break
PY
  )
  python3 "$repo_root/scripts/restore_evidence.py" validate-policy --policy "$policy_path" >/dev/null
fi

drill_calendar=""
drill_timeout=""
restore_metrics_file=""
if [[ "${SWITCHYARD_INSTALL_RESTORE_DRILL_TIMER:-0}" == "1" ]]; then
  if [[ ! -f "$env_file" ]]; then
    echo "refusing to enable restore drill timer without .env" >&2
    exit 1
  fi
  run_uid="$(id -u "$run_user")"
  if [[ "$run_uid" -ne 1000 ]]; then
    echo "restore drill run user must map to the API metrics UID 1000" >&2
    exit 1
  fi
  if ! command -v runuser >/dev/null 2>&1; then
    echo "runuser is required for restore drill metric initialization" >&2
    exit 1
  fi
  if ! runuser -u "$run_user" -- docker info >/dev/null 2>&1; then
    echo "restore drill timer requires a reachable Docker daemon" >&2
    exit 1
  fi
  preflight_output="$(runuser -u "$run_user" -- python3 "$repo_root/scripts/restore_drill_scheduler.py" preflight --env-file "$env_file" --owner-uid "$run_uid")"
  mapfile -t preflight_lines <<<"$preflight_output"
  if [[ "${#preflight_lines[@]}" -ne 2 || ! "${preflight_lines[1]}" =~ ^[1-9][0-9]*$ ]]; then
    echo "restore drill preflight returned unsafe output" >&2
    exit 1
  fi
  drill_calendar="${preflight_lines[0]}"
  drill_timeout="${preflight_lines[1]}"
  if [[ -z "$drill_calendar" || "$drill_calendar" == *$'\n'* || "$drill_calendar" == *$'\r'* || "$drill_calendar" == *%* ]]; then
    echo "restore drill calendar is unsafe" >&2
    exit 1
  fi
  systemd-analyze calendar "$drill_calendar" >/dev/null
  restore_metrics_file="$(python3 - "$env_file" <<'PY'
import pathlib, sys
for line in pathlib.Path(sys.argv[1]).read_text(encoding="utf-8").splitlines():
    if line.startswith("SWITCHYARD_RESTORE_DRILL_METRICS_FILE="):
        print(line.split("=", 1)[1])
        break
PY
  )"
fi

tmp_unit="$(mktemp)"
tmp_backup_unit="$(mktemp)"
tmp_restore_unit="$(mktemp)"
tmp_restore_timer="$(mktemp)"
metrics_snapshot="$(mktemp)"
restore_unit_snapshot="$(mktemp)"
restore_timer_snapshot="$(mktemp)"
restore_mutation_started=0
restore_unit_existed=0
restore_timer_existed=0
restore_service_enabled=0
restore_service_active=0
restore_timer_enabled=0
restore_timer_active=0
deadline_initialized=0
metrics_existed=0
cleanup() {
  status=$?
  if [[ $status -ne 0 && $restore_mutation_started -eq 1 ]]; then
    systemctl disable --now switchyard-restore-drill.timer >/dev/null 2>&1 || true
    systemctl disable --now switchyard-restore-drill.service >/dev/null 2>&1 || true
    if [[ $deadline_initialized -eq 1 ]]; then
      if [[ $metrics_existed -eq 1 ]]; then
        python3 - "$metrics_snapshot" "$restore_metrics_file" <<'PY' || true
import os, pathlib, sys
source, target = map(pathlib.Path, sys.argv[1:])
info = source.stat()
temporary = target.parent / f".{target.name}.rollback-{os.getpid()}"
temporary.write_bytes(source.read_bytes())
os.chmod(temporary, info.st_mode & 0o777)
os.chown(temporary, info.st_uid, info.st_gid)
with temporary.open("rb") as handle:
    os.fsync(handle.fileno())
os.replace(temporary, target)
directory = os.open(target.parent, os.O_RDONLY | os.O_DIRECTORY)
try:
    os.fsync(directory)
finally:
    os.close(directory)
PY
      else
        rm -f "$restore_metrics_file"
      fi
    fi
    python3 - "$restore_unit_snapshot" "$restore_unit_dst" "$restore_unit_existed" "$restore_timer_snapshot" "$restore_timer_dst" "$restore_timer_existed" <<'PY' || true
import os, pathlib, shutil, sys
for offset in (1, 4):
    source = pathlib.Path(sys.argv[offset])
    target = pathlib.Path(sys.argv[offset + 1])
    existed = sys.argv[offset + 2] == "1"
    if not existed:
        target.unlink(missing_ok=True)
        continue
    temporary = target.parent / f".{target.name}.rollback-{os.getpid()}"
    shutil.copy2(source, temporary, follow_symlinks=False)
    os.replace(temporary, target)
    directory = os.open(target.parent, os.O_RDONLY | os.O_DIRECTORY)
    try:
        os.fsync(directory)
    finally:
        os.close(directory)
PY
    systemctl daemon-reload >/dev/null 2>&1 || true
    [[ $restore_service_enabled -eq 0 ]] || systemctl enable switchyard-restore-drill.service >/dev/null 2>&1 || true
    [[ $restore_service_active -eq 0 ]] || systemctl start switchyard-restore-drill.service >/dev/null 2>&1 || true
    [[ $restore_timer_enabled -eq 0 ]] || systemctl enable switchyard-restore-drill.timer >/dev/null 2>&1 || true
    [[ $restore_timer_active -eq 0 ]] || systemctl start switchyard-restore-drill.timer >/dev/null 2>&1 || true
  fi
  rm -f "$tmp_unit" "$tmp_backup_unit" "$tmp_restore_unit" "$tmp_restore_timer" "$metrics_snapshot" "$restore_unit_snapshot" "$restore_timer_snapshot"
  return "$status"
}
trap cleanup EXIT

if [[ -e "$restore_unit_dst" ]]; then
  restore_unit_existed=1
  cp -p -- "$restore_unit_dst" "$restore_unit_snapshot"
fi
if [[ -e "$restore_timer_dst" ]]; then
  restore_timer_existed=1
  cp -p -- "$restore_timer_dst" "$restore_timer_snapshot"
fi
if systemctl is-enabled --quiet switchyard-restore-drill.service; then restore_service_enabled=1; fi
if systemctl is-active --quiet switchyard-restore-drill.service; then restore_service_active=1; fi
if systemctl is-enabled --quiet switchyard-restore-drill.timer; then restore_timer_enabled=1; fi
if systemctl is-active --quiet switchyard-restore-drill.timer; then restore_timer_active=1; fi

sed \
  -e "s#__REPO_ROOT__#$repo_root#g" \
  -e "s#__RUN_USER__#$run_user#g" \
  -e "s#__RUN_GROUP__#$run_group#g" \
  "$unit_src" > "$tmp_unit"

install -m 0644 "$tmp_unit" "$unit_dst"

sed \
  -e "s#__REPO_ROOT__#$repo_root#g" \
  -e "s#__RUN_USER__#$run_user#g" \
  -e "s#__RUN_GROUP__#$run_group#g" \
  "$backup_unit_src" > "$tmp_backup_unit"
install -m 0644 "$tmp_backup_unit" "$backup_unit_dst"
install -m 0644 "$backup_timer_src" "$backup_timer_dst"

python3 - "$restore_unit_src" "$tmp_restore_unit" "$repo_root" "$run_user" "$run_group" "$env_file" <<'PY'
import pathlib, sys
source, target, root, user, group, env_file = sys.argv[1:]
value = pathlib.Path(source).read_text(encoding="utf-8")
value = value.replace("__REPO_ROOT__", root).replace("__RUN_USER__", user).replace("__RUN_GROUP__", group).replace("__ENV_FILE__", env_file)
pathlib.Path(target).write_text(value, encoding="utf-8")
PY
sed -i "s/__TIMEOUT_START_SECONDS__/${drill_timeout:-86430}/g" "$tmp_restore_unit"
restore_mutation_started=1
install -m 0644 "$tmp_restore_unit" "$restore_unit_dst"
if [[ -n "$drill_calendar" ]]; then
  python3 - "$restore_timer_src" "$tmp_restore_timer" "$drill_calendar" <<'PY'
import pathlib, sys
value = pathlib.Path(sys.argv[1]).read_text(encoding="utf-8").replace("__DRILL_CALENDAR__", sys.argv[3])
pathlib.Path(sys.argv[2]).write_text(value, encoding="utf-8")
PY
  install -m 0644 "$tmp_restore_timer" "$restore_timer_dst"
fi

if systemctl list-unit-files llama-line.service >/dev/null 2>&1; then
  systemctl disable --now llama-line.service || true
  rm -f /etc/systemd/system/llama-line.service
fi

systemctl daemon-reload
systemctl enable --now switchyard.service
if [[ "${SWITCHYARD_INSTALL_BACKUP_TIMER:-0}" == "1" ]]; then
  systemctl enable --now switchyard-backup.timer
fi
if [[ "${SWITCHYARD_INSTALL_RESTORE_DRILL_TIMER:-0}" == "1" ]]; then
  if [[ -e "$restore_metrics_file" ]]; then
    metrics_existed=1
    python3 - "$restore_metrics_file" "$metrics_snapshot" <<'PY'
import os, pathlib, shutil, sys
source, target = map(pathlib.Path, sys.argv[1:])
shutil.copyfile(source, target)
info = source.stat(follow_symlinks=False)
os.chmod(target, info.st_mode & 0o777)
os.chown(target, info.st_uid, info.st_gid)
PY
  fi
  deadline_initialized=1
  runuser -u "$run_user" -- python3 "$repo_root/scripts/restore_drill_scheduler.py" initialize-deadline --env-file "$env_file" --owner-uid "$run_uid"
  systemctl enable --now switchyard-restore-drill.timer
fi
systemctl --no-pager status switchyard.service
