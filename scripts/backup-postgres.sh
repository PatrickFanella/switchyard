#!/usr/bin/env bash
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
backup_dir=${SWITCHYARD_BACKUP_DIR:-}
compose_service=${SWITCHYARD_POSTGRES_SERVICE:-postgres}

if [[ -z "$backup_dir" ]]; then
  echo "SWITCHYARD_BACKUP_DIR must name an encrypted or access-controlled backup directory" >&2
  exit 2
fi

mkdir -p "$backup_dir"
chmod 0700 "$backup_dir"

timestamp=$(date -u +%Y%m%dT%H%M%SZ)
revision=$(git -C "$root" rev-parse HEAD)
name="switchyard-${timestamp}-${revision:0:12}.dump"
tmp=$(mktemp "$backup_dir/.${name}.XXXXXX")
final="$backup_dir/$name"
metadata="$final.metadata"
metrics_file=${SWITCHYARD_BACKUP_METRICS_FILE:-}

cleanup() {
  rm -f "$tmp"
}
trap cleanup EXIT

umask 077
docker compose -f "$root/docker-compose.yml" exec -T "$compose_service" \
  sh -eu -c 'exec pg_dump --format=custom --no-owner --no-privileges --username "$POSTGRES_USER" --dbname "$POSTGRES_DB"' \
  >"$tmp"

test -s "$tmp"
docker compose -f "$root/docker-compose.yml" exec -T "$compose_service" \
  pg_restore --list <"$tmp" >/dev/null

checksum=$(sha256sum "$tmp" | awk '{print $1}')
database_version=$(docker compose -f "$root/docker-compose.yml" exec -T "$compose_service" \
  sh -eu -c 'psql --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" --tuples-only --no-align --command "show server_version"' |
  tr -d '[:space:]')
mv "$tmp" "$final"
trap - EXIT

{
  printf 'created_at=%s\n' "$timestamp"
  printf 'source_revision=%s\n' "$revision"
  printf 'database_version=%s\n' "$database_version"
  printf 'sha256=%s\n' "$checksum"
  printf 'format=postgres-custom\n'
} >"$metadata"
chmod 0600 "$final" "$metadata"

if [[ -n "$metrics_file" ]]; then
  metrics_dir=$(dirname "$metrics_file")
  mkdir -p "$metrics_dir"
  metrics_tmp=$(mktemp "$metrics_dir/.switchyard-backup.XXXXXX")
  printf 'switchyard_postgres_backup_last_success_timestamp_seconds %s\n' "$(date -u +%s)" >"$metrics_tmp"
  chmod 0644 "$metrics_tmp"
  mv "$metrics_tmp" "$metrics_file"
fi

printf 'backup=%s\nmetadata=%s\nsha256=%s\n' "$final" "$metadata" "$checksum"
