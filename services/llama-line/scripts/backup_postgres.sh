#!/usr/bin/env bash
set -Eeuo pipefail

backup_dir="${BACKUP_DIR:-/home/onnwee/Projects/tools/llama-line/backups/postgres}"
ts="$(date -u +%Y%m%dT%H%M%SZ)"
out="$backup_dir/llama_line-$ts.sql.gz"

mkdir -p "$backup_dir"
chmod 700 "$backup_dir"

docker exec llama-line-postgres pg_dump -U llama -d llama_line --no-owner --no-privileges \
  | gzip -9 > "$out"
chmod 600 "$out"
printf '%s\n' "$out"
