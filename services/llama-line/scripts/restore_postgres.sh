#!/usr/bin/env bash
set -Eeuo pipefail

dump="${1:?usage: scripts/restore_postgres.sh backups/postgres/llama_line-YYYYMMDDTHHMMSSZ.sql.gz}"

test -f "$dump"
gzip -t "$dump"

docker exec llama-line-postgres psql -U llama -d llama_line -v ON_ERROR_STOP=1 -c 'drop schema if exists broker cascade;'
gzip -dc "$dump" | docker exec -i llama-line-postgres psql -U llama -d llama_line -v ON_ERROR_STOP=1
docker exec llama-line-postgres psql -U llama -d llama_line -c 'select version from broker.schema_migrations order by version;'
