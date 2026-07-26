#!/usr/bin/env bash
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
image='postgres:17@sha256:0af65001d05296a2ead57ac4a6412433d8913d1bb5d0c88435a7d1e1ee5cb04b'
backup=${1:-}
name="switchyard-restore-drill-$$"
api_name="${name}-api"
api_image="${api_name}:local"
password='switchyard-restore-drill-only'
tmp=$(mktemp -d)
started_at=$(date +%s)

cleanup() {
  docker rm -f "$api_name" >/dev/null 2>&1 || true
  docker rm -f "$name" >/dev/null 2>&1 || true
  docker image rm "$api_image" >/dev/null 2>&1 || true
  rm -rf "$tmp"
}
trap cleanup EXIT

if [[ -z "$backup" || ! -f "$backup" ]]; then
  echo "usage: $0 /path/to/switchyard.dump" >&2
  exit 2
fi

if [[ -f "$backup.metadata" ]]; then
  expected=$(awk -F= '$1 == "sha256" { print $2 }' "$backup.metadata")
  actual=$(sha256sum "$backup" | awk '{print $1}')
  if [[ -z "$expected" || "$actual" != "$expected" ]]; then
    echo "backup checksum does not match metadata" >&2
    exit 1
  fi
fi

docker run --detach --rm --name "$name" --publish 127.0.0.1::5432 \
  --env POSTGRES_DB=switchyard_restore \
  --env POSTGRES_USER=switchyard_restore \
  --env POSTGRES_PASSWORD="$password" \
  "$image" >/dev/null

for _ in $(seq 1 120); do
  if docker exec "$name" pg_isready --username switchyard_restore --dbname switchyard_restore >/dev/null 2>&1; then
    break
  fi
  sleep 0.25
done
docker exec "$name" pg_isready --username switchyard_restore --dbname switchyard_restore >/dev/null

docker cp "$backup" "$name:/tmp/switchyard.dump"
docker exec "$name" pg_restore --exit-on-error --no-owner --no-privileges \
  --username switchyard_restore --dbname switchyard_restore /tmp/switchyard.dump

for table in schema_migrations workflows runs run_steps run_events artifacts model_broker_requests; do
  docker exec "$name" psql --username switchyard_restore --dbname switchyard_restore \
    --tuples-only --no-align --command "select to_regclass('public.$table') is not null" |
    grep -qx t
done

port=$(docker port "$name" 5432/tcp | sed 's/.*://')
api_port=$(python3 - <<'PY'
import socket
s = socket.socket()
s.bind(("127.0.0.1", 0))
print(s.getsockname()[1])
s.close()
PY
)

docker build --quiet --tag "$api_image" "$root/backend" >/dev/null
docker run --detach --rm --name "$api_name" --network host \
  --env "DATABASE_URL=host=127.0.0.1 port=$port user=switchyard_restore password=$password dbname=switchyard_restore sslmode=disable" \
  --env "SWITCHYARD_LISTEN_ADDR=127.0.0.1:$api_port" \
  --env SWITCHYARD_CONTROL_API_TOKEN=restore-drill-token \
  --env SWITCHYARD_WORKER_ENABLED=false \
  --env SWITCHYARD_SCHEDULER_ENABLED=false \
  "$api_image" >/dev/null

for _ in $(seq 1 100); do
  if curl --fail --silent "http://127.0.0.1:$api_port/readyz" >/dev/null 2>&1; then
    break
  fi
  if ! docker inspect --format '{{.State.Running}}' "$api_name" 2>/dev/null | grep -qx true; then
    docker logs "$api_name" >&2 || true
    exit 1
  fi
  sleep 0.1
done

curl --fail --silent "http://127.0.0.1:$api_port/readyz" >/dev/null
curl --fail --silent -H 'Authorization: Bearer restore-drill-token' \
  "http://127.0.0.1:$api_port/api/workflows" >/dev/null

docker stop --time 10 "$api_name" >/dev/null

migrations=$(docker exec "$name" psql --username switchyard_restore --dbname switchyard_restore \
  --tuples-only --no-align --command 'select count(*) from schema_migrations')
runs=$(docker exec "$name" psql --username switchyard_restore --dbname switchyard_restore \
  --tuples-only --no-align --command 'select count(*) from runs')
artifacts=$(docker exec "$name" psql --username switchyard_restore --dbname switchyard_restore \
  --tuples-only --no-align --command 'select count(*) from artifacts')
duration_seconds=$(($(date +%s) - started_at))
printf 'PostgreSQL restore drill passed: backup=%s migrations=%s runs=%s artifacts=%s duration_seconds=%s\n' \
  "$backup" "$migrations" "$runs" "$artifacts" "$duration_seconds"
