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
nft_dir="${SWITCHYARD_NFT_CONFIG_DIR:-/etc/switchyard}"
lib_dir="${SWITCHYARD_LIB_DIR:-/usr/lib/switchyard}"
gate_rules="$nft_dir/web-gate.nft"
gate_unit="$unit_dir/switchyard-web-gate.service"
gate_renderer="$lib_dir/render_tailscale_web_gate.py"
ufw_helper="$lib_dir/manage_tailscale_web_forward.py"
unit_dst="$unit_dir/switchyard.service"
backup_unit_dst="$unit_dir/switchyard-backup.service"
backup_timer_dst="$unit_dir/switchyard-backup.timer"
restore_unit_dst="$unit_dir/switchyard-restore-drill.service"
restore_timer_dst="$unit_dir/switchyard-restore-drill.timer"

if [[ "$(id -u)" -ne 0 ]]; then
  echo "install-systemd.sh must be run with sudo/root" >&2
  exit 1
fi
if [[ ! "$env_file" =~ ^/[A-Za-z0-9_./-]+$ ]]; then
  echo "SWITCHYARD_ENV_FILE contains characters unsafe for systemd" >&2
  exit 1
fi

if ! env_file="$(python3 - "$env_file" <<'PY'
import pathlib, stat, sys
path = pathlib.Path(sys.argv[1])
if not path.is_absolute():
    raise SystemExit(1)
try:
    info = path.lstat()
    resolved = path.resolve(strict=True)
except OSError:
    raise SystemExit(1)
if stat.S_ISLNK(info.st_mode) or not stat.S_ISREG(info.st_mode) or str(path) != str(resolved):
    raise SystemExit(1)
print(resolved)
PY
)"; then
  echo "SWITCHYARD_ENV_FILE must be an absolute regular non-symlink path" >&2
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

for command in python3 nft ufw iptables systemctl cmp stat; do
  if ! command -v "$command" >/dev/null 2>&1; then
    echo "$command is required" >&2
    exit 1
  fi
done
if [[ ! -f "$gate_rules" || -L "$gate_rules" || ! -f "$gate_unit" || -L "$gate_unit" || ! -f "$gate_renderer" || -L "$gate_renderer" || ! -f "$ufw_helper" || -L "$ufw_helper" ]]; then
  echo "switchyard web gate must be installed from the current environment" >&2
  exit 1
fi
for specification in "$gate_rules:0 0 644" "$gate_unit:0 0 644" "$gate_renderer:0 0 755" "$ufw_helper:0 0 755"; do
  path=${specification%%:*}
  expected=${specification#*:}
  if [[ "$(stat -Lc '%u %g %a' "$path")" != "$expected" ]]; then
    echo "switchyard web gate files must be root-owned with installed modes" >&2
    exit 1
  fi
done
gate_tmp="$(mktemp)"
if ! python3 "$gate_renderer" --env-file "$env_file" --check ||
   ! python3 "$gate_renderer" --env-file "$env_file" --output "$gate_tmp" ||
   ! nft --check --file "$gate_tmp" ||
   ! cmp --silent "$gate_tmp" "$gate_rules"; then
  rm -f "$gate_tmp"
  echo "switchyard web gate does not match the current environment" >&2
  exit 1
fi
rm -f "$gate_tmp"
if ! python3 - "$repo_root/deploy/switchyard-web-gate.service" "$gate_unit" "$gate_rules" "$gate_renderer" "$ufw_helper" "$env_file" <<'PY'
import pathlib, sys
source, installed, rules, renderer, ufw_helper, env_file = map(pathlib.Path, sys.argv[1:])
expected = source.read_text(encoding="utf-8")
for placeholder, replacement in (("__NFT_RULES__", rules), ("__RENDERER__", renderer), ("__UFW_HELPER__", ufw_helper), ("__ENV_FILE__", env_file)):
    expected = expected.replace(placeholder, str(replacement))
if any(placeholder in expected for placeholder in ("__NFT_RULES__", "__RENDERER__", "__UFW_HELPER__", "__ENV_FILE__")) or installed.read_text(encoding="utf-8") != expected:
    raise SystemExit(1)
PY
then
  echo "switchyard web gate unit does not match the current environment" >&2
  exit 1
fi
if ! nft list table inet switchyard_web_gate >/dev/null 2>&1 || ! python3 "$ufw_helper" verify --env-file "$env_file" || ! systemctl is-active --quiet switchyard-web-gate.service; then
  echo "switchyard-web-gate.service must be installed and active; run deploy/install-web-gate.sh first" >&2
  exit 1
fi
for property in Requires After; do
  if ! systemctl show docker.service --property="$property" --value | tr ' ' '\n' | grep -Fxq switchyard-web-gate.service; then
    echo "docker.service must require the active Switchyard web gate" >&2
    exit 1
  fi
done

if [[ "${SWITCHYARD_INSTALL_BACKUP_TIMER:-0}" == "1" ]]; then
  if ! grep -Eq '^SWITCHYARD_BACKUP_DIR=.+$' "$env_file" || ! grep -Eq '^SWITCHYARD_BACKUP_POLICY=.+$' "$env_file"; then
    echo "refusing to enable backup timer without SWITCHYARD_BACKUP_DIR and SWITCHYARD_BACKUP_POLICY in .env" >&2
    exit 1
  fi
  policy_path=$(python3 - "$env_file" <<'PY'
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
  if [[ "${#preflight_lines[@]}" -ne 3 || ${#preflight_lines[0]} -gt 256 ||
        ! "${preflight_lines[1]}" =~ ^[1-9][0-9]{0,8}$ ||
        ! "${preflight_lines[2]}" =~ ^[1-9][0-9]{0,9}$ ]]; then
    echo "restore drill preflight returned unsafe output" >&2
    exit 1
  fi
  drill_calendar="${preflight_lines[0]}"
  drill_timeout="${preflight_lines[1]}"
  expected_runtime_uid="${preflight_lines[2]}"
  if [[ -z "$drill_calendar" || "$drill_calendar" == *$'\n'* || "$drill_calendar" == *$'\r'* || "$drill_calendar" == *%* ]]; then
    echo "restore drill calendar is unsafe" >&2
    exit 1
  fi
  if (( drill_timeout > 86430 || expected_runtime_uid > 2147483647 )); then
    echo "restore drill preflight returned unsafe output" >&2
    exit 1
  fi
  if [[ "$run_uid" != "$expected_runtime_uid" ]]; then
    echo "restore drill run user must match OPENCODE_RUNTIME_UID" >&2
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
main_unit_snapshot="$(mktemp)"
backup_unit_snapshot="$(mktemp)"
backup_timer_snapshot="$(mktemp)"
unit_mutation_started=0
restore_unit_existed=0
restore_timer_existed=0
backup_unit_existed=0
backup_timer_existed=0
restore_service_enabled=0
restore_service_active=0
restore_timer_enabled=0
restore_timer_active=0
backup_service_enabled=0
backup_service_active=0
backup_timer_enabled=0
backup_timer_active=0
deadline_initialized=0
metrics_existed=0
main_unit_existed=0
main_was_enabled=0
main_was_active=0
running_services=()
compose_services=()
cleanup() {
  status=$?
  if [[ $status -ne 0 && $unit_mutation_started -eq 1 ]]; then
    # Stop every possibly changed definition before restoring files and state.
    for unit in switchyard.service switchyard-backup.service switchyard-backup.timer switchyard-restore-drill.service switchyard-restore-drill.timer; do
      systemctl disable --now "$unit" >/dev/null 2>&1 || true
    done
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
    python3 - \
      "$main_unit_snapshot" "$unit_dst" "$main_unit_existed" \
      "$backup_unit_snapshot" "$backup_unit_dst" "$backup_unit_existed" \
      "$backup_timer_snapshot" "$backup_timer_dst" "$backup_timer_existed" \
      "$restore_unit_snapshot" "$restore_unit_dst" "$restore_unit_existed" \
      "$restore_timer_snapshot" "$restore_timer_dst" "$restore_timer_existed" <<'PY' || true
import os, pathlib, shutil, sys
for offset in range(1, len(sys.argv), 3):
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

    [[ $backup_service_enabled -eq 0 ]] || systemctl enable switchyard-backup.service >/dev/null 2>&1 || true
    [[ $backup_timer_enabled -eq 0 ]] || systemctl enable switchyard-backup.timer >/dev/null 2>&1 || true
    [[ $restore_service_enabled -eq 0 ]] || systemctl enable switchyard-restore-drill.service >/dev/null 2>&1 || true
    [[ $restore_timer_enabled -eq 0 ]] || systemctl enable switchyard-restore-drill.timer >/dev/null 2>&1 || true
    [[ $main_was_enabled -eq 0 ]] || systemctl enable switchyard.service >/dev/null 2>&1 || true

    # Gate/config are now the pre-mutation versions (the main installer never
    # mutates them). Fail closed if that prerequisite cannot be re-proven.
    if nft list table inet switchyard_web_gate >/dev/null 2>&1 && python3 "$ufw_helper" verify --env-file "$env_file" >/dev/null 2>&1 && systemctl is-active --quiet switchyard-web-gate.service; then
      if [[ $main_was_active -eq 1 ]]; then
        systemctl start switchyard.service >/dev/null 2>&1 || true
        stopped_services=()
        for service in "${compose_services[@]}"; do
          found=0
          for running in "${running_services[@]}"; do [[ "$service" != "$running" ]] || found=1; done
          [[ $found -eq 1 ]] || stopped_services+=("$service")
        done
        [[ ${#stopped_services[@]} -eq 0 ]] || docker compose --env-file "$env_file" -f "$repo_root/docker-compose.yml" stop "${stopped_services[@]}" >/dev/null 2>&1 || true
      elif [[ ${#running_services[@]} -gt 0 ]]; then
        docker compose --env-file "$env_file" -f "$repo_root/docker-compose.yml" up -d --no-deps "${running_services[@]}" >/dev/null 2>&1 || true
      fi
    fi
    [[ $backup_service_active -eq 0 ]] || systemctl start switchyard-backup.service >/dev/null 2>&1 || true
    [[ $restore_service_active -eq 0 ]] || systemctl start switchyard-restore-drill.service >/dev/null 2>&1 || true
    [[ $backup_timer_active -eq 0 ]] || systemctl start switchyard-backup.timer >/dev/null 2>&1 || true
    [[ $restore_timer_active -eq 0 ]] || systemctl start switchyard-restore-drill.timer >/dev/null 2>&1 || true
  fi
  rm -f "$tmp_unit" "$tmp_backup_unit" "$tmp_restore_unit" "$tmp_restore_timer" "$metrics_snapshot" \
    "$restore_unit_snapshot" "$restore_timer_snapshot" "$main_unit_snapshot" "$backup_unit_snapshot" "$backup_timer_snapshot"
  return "$status"
}
trap cleanup EXIT

python3 - "$unit_src" "$tmp_unit" "$repo_root" "$run_user" "$run_group" "$env_file" "$gate_renderer" "$gate_rules" "$ufw_helper" <<'PY'
import pathlib, sys
source, target, root, user, group, env_file, renderer, rules, ufw_helper = sys.argv[1:]
value = pathlib.Path(source).read_text(encoding="utf-8")
for placeholder, replacement in (("__REPO_ROOT__", root), ("__RUN_USER__", user), ("__RUN_GROUP__", group), ("__ENV_FILE__", env_file), ("__RENDERER__", renderer), ("__NFT_RULES__", rules), ("__UFW_HELPER__", ufw_helper)):
    value = value.replace(placeholder, replacement)
if any(placeholder in value for placeholder in ("__REPO_ROOT__", "__RUN_USER__", "__RUN_GROUP__", "__ENV_FILE__", "__RENDERER__", "__NFT_RULES__", "__UFW_HELPER__")):
    raise SystemExit("unresolved switchyard unit placeholder")
pathlib.Path(target).write_text(value, encoding="utf-8")
PY

python3 - "$backup_unit_src" "$tmp_backup_unit" "$repo_root" "$run_user" "$run_group" <<'PY'
import pathlib, sys
source, target, root, user, group = sys.argv[1:]
value = pathlib.Path(source).read_text(encoding="utf-8").replace("__REPO_ROOT__", root).replace("__RUN_USER__", user).replace("__RUN_GROUP__", group)
pathlib.Path(target).write_text(value, encoding="utf-8")
PY

python3 - "$restore_unit_src" "$tmp_restore_unit" "$repo_root" "$run_user" "$run_group" "$env_file" <<'PY'
import pathlib, sys
source, target, root, user, group, env_file = sys.argv[1:]
value = pathlib.Path(source).read_text(encoding="utf-8")
value = value.replace("__REPO_ROOT__", root).replace("__RUN_USER__", user).replace("__RUN_GROUP__", group).replace("__ENV_FILE__", env_file)
pathlib.Path(target).write_text(value, encoding="utf-8")
PY
sed -i "s/__TIMEOUT_START_SECONDS__/${drill_timeout:-86430}/g" "$tmp_restore_unit"
if [[ -n "$drill_calendar" ]]; then
  python3 - "$restore_timer_src" "$tmp_restore_timer" "$drill_calendar" <<'PY'
import pathlib, sys
value = pathlib.Path(sys.argv[1]).read_text(encoding="utf-8").replace("__DRILL_CALENDAR__", sys.argv[3])
pathlib.Path(sys.argv[2]).write_text(value, encoding="utf-8")
PY
fi

# Last preflight: validate Compose and capture the exact workload state before
# the first filesystem or systemd mutation.
compose_services_output="$(docker compose --env-file "$env_file" -f "$repo_root/docker-compose.yml" config --services)"
running_services_output="$(docker compose --env-file "$env_file" -f "$repo_root/docker-compose.yml" ps --status running --services)"
mapfile -t compose_services <<<"$compose_services_output"
mapfile -t running_services <<<"$running_services_output"
[[ -n "$compose_services_output" ]] || { echo "Docker Compose returned no services" >&2; exit 1; }
[[ -n "$running_services_output" ]] || running_services=()
for running in "${running_services[@]}"; do
  [[ "$running" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]*$ ]] || { echo "Docker Compose returned an unsafe service name" >&2; exit 1; }
  found=0
  for service in "${compose_services[@]}"; do [[ "$running" != "$service" ]] || found=1; done
  [[ $found -eq 1 ]] || { echo "Docker Compose returned an unknown running service" >&2; exit 1; }
done
for destination in "$unit_dst" "$backup_unit_dst" "$backup_timer_dst" "$restore_unit_dst" "$restore_timer_dst"; do
  if [[ -L "$destination" || -e "$destination" && ! -f "$destination" ]]; then
    echo "systemd install destination must be a regular file" >&2
    exit 1
  fi
done
if [[ -e "$unit_dst" ]]; then main_unit_existed=1; cp -p -- "$unit_dst" "$main_unit_snapshot"; fi
if [[ -e "$backup_unit_dst" ]]; then backup_unit_existed=1; cp -p -- "$backup_unit_dst" "$backup_unit_snapshot"; fi
if [[ -e "$backup_timer_dst" ]]; then backup_timer_existed=1; cp -p -- "$backup_timer_dst" "$backup_timer_snapshot"; fi
if [[ -e "$restore_unit_dst" ]]; then restore_unit_existed=1; cp -p -- "$restore_unit_dst" "$restore_unit_snapshot"; fi
if [[ -e "$restore_timer_dst" ]]; then restore_timer_existed=1; cp -p -- "$restore_timer_dst" "$restore_timer_snapshot"; fi
if systemctl is-enabled --quiet switchyard.service; then main_was_enabled=1; fi
if systemctl is-active --quiet switchyard.service; then main_was_active=1; fi
if systemctl is-enabled --quiet switchyard-backup.service; then backup_service_enabled=1; fi
if systemctl is-active --quiet switchyard-backup.service; then backup_service_active=1; fi
if systemctl is-enabled --quiet switchyard-backup.timer; then backup_timer_enabled=1; fi
if systemctl is-active --quiet switchyard-backup.timer; then backup_timer_active=1; fi
if systemctl is-enabled --quiet switchyard-restore-drill.service; then restore_service_enabled=1; fi
if systemctl is-active --quiet switchyard-restore-drill.service; then restore_service_active=1; fi
if systemctl is-enabled --quiet switchyard-restore-drill.timer; then restore_timer_enabled=1; fi
if systemctl is-active --quiet switchyard-restore-drill.timer; then restore_timer_active=1; fi

unit_mutation_started=1
install -m 0644 "$tmp_unit" "$unit_dst"
install -m 0644 "$tmp_backup_unit" "$backup_unit_dst"
install -m 0644 "$backup_timer_src" "$backup_timer_dst"
install -m 0644 "$tmp_restore_unit" "$restore_unit_dst"
if [[ -n "$drill_calendar" ]]; then install -m 0644 "$tmp_restore_timer" "$restore_timer_dst"; fi

if systemctl list-unit-files llama-line.service >/dev/null 2>&1; then
  systemctl disable --now llama-line.service || true
  rm -f /etc/systemd/system/llama-line.service
fi

systemctl daemon-reload
systemctl enable switchyard.service
systemctl restart switchyard.service
systemctl is-active --quiet switchyard.service
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
