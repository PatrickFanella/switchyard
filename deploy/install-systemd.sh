#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
run_user="${SWITCHYARD_RUN_USER:-${SUDO_USER:-$(id -un)}}"
run_group="${SWITCHYARD_RUN_GROUP:-$(id -gn "$run_user")}"
unit_src="$repo_root/deploy/switchyard.service"
unit_dst="/etc/systemd/system/switchyard.service"

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
trap 'rm -f "$tmp_unit"' EXIT

sed \
  -e "s#__REPO_ROOT__#$repo_root#g" \
  -e "s#__RUN_USER__#$run_user#g" \
  -e "s#__RUN_GROUP__#$run_group#g" \
  "$unit_src" > "$tmp_unit"

install -m 0644 "$tmp_unit" "$unit_dst"

if systemctl list-unit-files llama-line.service >/dev/null 2>&1; then
  systemctl disable --now llama-line.service || true
  rm -f /etc/systemd/system/llama-line.service
fi

systemctl daemon-reload
systemctl enable --now switchyard.service
systemctl --no-pager status switchyard.service
