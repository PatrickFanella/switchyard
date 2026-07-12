#!/usr/bin/env bash
set -euo pipefail

suffix="$$"
network="switchyard-test-$suffix"
postgres="switchyard-test-postgres-$suffix"
api="switchyard-test-api-$suffix"
password='switchyard-test-only-password'
token='switchyard-test-control-token'

cleanup() {
  docker rm -f "$api" "$postgres" >/dev/null 2>&1 || true
  docker network rm "$network" >/dev/null 2>&1 || true
}
trap cleanup EXIT

docker network create "$network" >/dev/null
docker run --detach --rm --name "$postgres" --network "$network" \
  --env POSTGRES_DB=switchyard_test --env POSTGRES_USER=switchyard_test --env POSTGRES_PASSWORD="$password" \
  postgres:17@sha256:0af65001d05296a2ead57ac4a6412433d8913d1bb5d0c88435a7d1e1ee5cb04b >/dev/null
for _ in $(seq 1 120); do
  if docker logs "$postgres" 2>&1 | grep -q 'PostgreSQL init process complete'; then break; fi
  sleep 0.25
done
docker logs "$postgres" 2>&1 | grep -q 'PostgreSQL init process complete'

docker run --detach --name "$api" --network "$network" \
  --publish 127.0.0.1::8080 --publish 127.0.0.1::11434 \
  --env "DATABASE_URL=host=$postgres port=5432 user=switchyard_test password=$password dbname=switchyard_test sslmode=disable" \
  --env "SWITCHYARD_CONTROL_API_TOKEN=$token" --env LLAMA_LINE_LISTEN_ADDR=:11434 \
  --env SWITCHYARD_SCHEDULER_ENABLED=false --env SWITCHYARD_WORKER_ENABLED=true \
  switchyard-api:latest >/dev/null

control_port="$(docker port "$api" 8080/tcp | sed 's/.*://')"
model_port="$(docker port "$api" 11434/tcp | sed 's/.*://')"
for _ in $(seq 1 120); do
  if curl --fail --silent "http://127.0.0.1:$control_port/readyz" >/dev/null 2>&1; then break; fi
  sleep 0.25
done
curl --fail --silent "http://127.0.0.1:$control_port/readyz" >/dev/null

test "$(curl --silent --output /dev/null --write-out '%{http_code}' "http://127.0.0.1:$control_port/api/runs")" = 401
test "$(curl --silent --output /dev/null --write-out '%{http_code}' -H "Authorization: Bearer $token" "http://127.0.0.1:$control_port/api/runs")" = 200
test "$(curl --silent --output /dev/null --write-out '%{http_code}' -H "Authorization: Bearer $token" "http://127.0.0.1:$control_port/v1/models")" = 404
test "$(curl --silent --output /dev/null --write-out '%{http_code}' "http://127.0.0.1:$model_port/api/runs")" = 404
test "$(curl --silent --output /dev/null --write-out '%{http_code}' "http://127.0.0.1:$model_port/v1/models")" = 200

docker stop --time 10 "$api" >/dev/null
test "$(docker inspect --format '{{.State.ExitCode}}' "$api")" = 0
