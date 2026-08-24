#!/usr/bin/env bash
set -euo pipefail

image='postgres:17@sha256:0af65001d05296a2ead57ac4a6412433d8913d1bb5d0c88435a7d1e1ee5cb04b'
name="switchyard-postgres-test-$$"
password='switchyard-test-only-password'
network="${SWITCHYARD_TEST_DOCKER_NETWORK:-}"

cleanup() {
  docker rm -f "$name" >/dev/null 2>&1 || true
}
trap cleanup EXIT

if [[ -n "$network" ]] && ! docker network inspect "$network" >/dev/null 2>&1; then
  printf 'SWITCHYARD_TEST_DOCKER_NETWORK %q does not name an existing Docker network\n' "$network" >&2
  exit 1
fi

docker_args=(--detach --rm --name "$name")
if [[ -n "$network" ]]; then
  docker_args+=(--network "$network")
else
  docker_args+=(--publish 127.0.0.1::5432)
fi

docker run "${docker_args[@]}" \
  --env POSTGRES_DB=switchyard_test \
  --env POSTGRES_USER=switchyard_test \
  --env POSTGRES_PASSWORD="$password" \
  "$image" >/dev/null

if [[ -n "$network" ]]; then
  database_host="$name"
  database_port=5432
else
  database_host=127.0.0.1
  database_port="$(docker port "$name" 5432/tcp | sed 's/.*://')"
fi
for _ in $(seq 1 120); do
	if docker logs "$name" 2>&1 | grep -q 'PostgreSQL init process complete'; then
		break
	fi
	sleep 0.25
done
docker logs "$name" 2>&1 | grep -q 'PostgreSQL init process complete'
docker exec "$name" pg_isready --username switchyard_test --dbname switchyard_test >/dev/null

export SWITCHYARD_TEST_DATABASE_URL="host=$database_host port=$database_port user=switchyard_test password=$password dbname=switchyard_test sslmode=disable"
export SWITCHYARD_TEST_POSTGRES_ISOLATED=1
cd backend
go test ./internal/controlplane -v -run 'TestPostgresStore.*Integration|TestArtifactAndCompatibilityMigrationsPreserveFloorsAndOldReadersIntegration|TestStoreLifecycleContract/postgres' -count=1
