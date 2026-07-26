#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
run_user="${SWITCHYARD_RUN_USER:-${SUDO_USER:-$(id -un)}}"
run_group="${SWITCHYARD_RUN_GROUP:-$(id -gn "$run_user")}"
unit_src="$repo_root/deploy/switchyard.service"
unit_dst="/etc/systemd/system/switchyard.service"
backup_unit_src="$repo_root/deploy/switchyard-backup.service"
backup_unit_dst="/etc/systemd/system/switchyard-backup.service"
backup_timer_src="$repo_root/deploy/switchyard-backup.timer"
backup_timer_dst="/etc/systemd/system/switchyard-backup.timer"

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

tmp_unit="$(mktemp)"
tmp_backup_unit="$(mktemp)"
trap 'rm -f "$tmp_unit" "$tmp_backup_unit"' EXIT

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

if systemctl list-unit-files llama-line.service >/dev/null 2>&1; then
  systemctl disable --now llama-line.service || true
  rm -f /etc/systemd/system/llama-line.service
fi

systemctl daemon-reload
systemctl enable --now switchyard.service
if [[ "${SWITCHYARD_INSTALL_BACKUP_TIMER:-0}" == "1" ]]; then
  if [[ ! -f "$repo_root/.env" ]] || ! grep -Eq '^SWITCHYARD_BACKUP_DIR=.+$' "$repo_root/.env"; then
    echo "refusing to enable backup timer without SWITCHYARD_BACKUP_DIR in $repo_root/.env" >&2
    exit 1
  fi
  systemctl enable --now switchyard-backup.timer
fi
systemctl --no-pager status switchyard.service
