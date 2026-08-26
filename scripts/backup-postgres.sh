#!/usr/bin/env bash
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
docker() { "$root/deploy/docker-local" "$@"; }
env_file=${SWITCHYARD_ENV_FILE:-$root/.env}
backup_dir=${SWITCHYARD_BACKUP_DIR:-}
policy=${SWITCHYARD_BACKUP_POLICY:-}
compose_service=${SWITCHYARD_POSTGRES_SERVICE:-postgres}
metrics_file=${SWITCHYARD_BACKUP_METRICS_FILE:-}
trap 'echo "backup failed" >&2' ERR

if [[ -z "$backup_dir" ]]; then
  echo "SWITCHYARD_BACKUP_DIR is required" >&2
  exit 2
fi
if [[ -z "$policy" ]]; then
  echo "SWITCHYARD_BACKUP_POLICY is required" >&2
  exit 2
fi
python3 "$root/scripts/restore_evidence.py" validate-policy --policy "$policy" >/dev/null

python3 "$root/scripts/restore_evidence.py" prepare-directory --directory "$backup_dir"

created_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
stamp=$(date -u +%Y%m%dT%H%M%SZ)
revision=$(git -C "$root" rev-parse HEAD 2>/dev/null)
bundle_id="switchyard-${stamp}-${revision:0:12}"
archive_name="${bundle_id}.dump"
archive_tmp=$(mktemp "$backup_dir/.${archive_name}.XXXXXX" 2>/dev/null)
ledger_tmp=$(mktemp 2>/dev/null)
ledger_after_tmp=$(mktemp 2>/dev/null)

cleanup() {
  rm -f "$archive_tmp" "$ledger_tmp" "$ledger_after_tmp" >/dev/null 2>&1 || true
}
trap cleanup EXIT
umask 077

docker compose --env-file "$env_file" -f "$root/docker-compose.yml" exec -T "$compose_service" \
  sh -eu -c 'psql --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" --tuples-only --no-align --command "select version from schema_migrations order by version"' \
  >"$ledger_tmp" 2>/dev/null

docker compose --env-file "$env_file" -f "$root/docker-compose.yml" exec -T "$compose_service" \
  sh -eu -c 'exec pg_dump --format=custom --no-owner --no-privileges --username "$POSTGRES_USER" --dbname "$POSTGRES_DB"' \
  >"$archive_tmp" 2>/dev/null
test -s "$archive_tmp"
docker compose --env-file "$env_file" -f "$root/docker-compose.yml" exec -T "$compose_service" \
  pg_restore --list <"$archive_tmp" >/dev/null 2>&1

docker compose --env-file "$env_file" -f "$root/docker-compose.yml" exec -T "$compose_service" \
  sh -eu -c 'psql --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" --tuples-only --no-align --command "select version from schema_migrations order by version"' \
  >"$ledger_after_tmp" 2>/dev/null
if ! cmp -s "$ledger_tmp" "$ledger_after_tmp"; then
  echo "database migrations changed during backup" >&2
  exit 1
fi

database_version=$(docker compose --env-file "$env_file" -f "$root/docker-compose.yml" exec -T "$compose_service" \
  sh -eu -c 'psql --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" --tuples-only --no-align --command "show server_version_num"' 2>/dev/null |
  tr -d '[:space:]' 2>/dev/null)

publication=$(python3 "$root/scripts/restore_evidence.py" publish-bundle \
  --policy "$policy" \
  --archive-temp "$archive_tmp" \
  --output-dir "$backup_dir" \
  --bundle-id "$bundle_id" \
  --archive-basename "$archive_name" \
  --source-revision "$revision" \
  --database-version "$database_version" \
  --migration-ledger "$ledger_tmp" \
  --created-at "$created_at")

if [[ -n "$metrics_file" ]]; then
  metrics_dir=$(dirname "$metrics_file")
  if [[ -L "$metrics_dir" ]]; then
    echo "metrics directory must not be a symlink" >&2
    exit 2
  fi
  mkdir -p "$metrics_dir" 2>/dev/null
  metrics_tmp=$(mktemp "$metrics_dir/.switchyard-backup.XXXXXX" 2>/dev/null)
  printf 'switchyard_postgres_backup_last_success_timestamp_seconds %s\n' "$(date -u +%s)" >"$metrics_tmp"
  chmod 0644 "$metrics_tmp" 2>/dev/null
  mv "$metrics_tmp" "$metrics_file" 2>/dev/null
fi

printf '%s\n' "$publication"
