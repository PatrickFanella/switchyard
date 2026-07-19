#!/usr/bin/env bash
set -euo pipefail

image='postgres:17@sha256:0af65001d05296a2ead57ac4a6412433d8913d1bb5d0c88435a7d1e1ee5cb04b'
name="switchyard-postgres-test-$$"
password='switchyard-test-only-password'

cleanup() {
  docker rm -f "$name" >/dev/null 2>&1 || true
}
trap cleanup EXIT

docker run --detach --rm --name "$name" \
  --env POSTGRES_DB=switchyard_test \
  --env POSTGRES_USER=switchyard_test \
  --env POSTGRES_PASSWORD="$password" \
  --publish 127.0.0.1::5432 \
  "$image" >/dev/null

port="$(docker port "$name" 5432/tcp | sed 's/.*://')"
for _ in $(seq 1 120); do
	if docker logs "$name" 2>&1 | grep -q 'PostgreSQL init process complete'; then
		break
	fi
	sleep 0.25
done
docker logs "$name" 2>&1 | grep -q 'PostgreSQL init process complete'
docker exec "$name" pg_isready --username switchyard_test --dbname switchyard_test >/dev/null

export SWITCHYARD_TEST_DATABASE_URL="host=127.0.0.1 port=$port user=switchyard_test password=$password dbname=switchyard_test sslmode=disable"
export SWITCHYARD_TEST_POSTGRES_ISOLATED=1
cd backend
go test ./internal/controlplane -v -run 'TestPostgresStore.*Integration|TestStoreLifecycleContract/postgres' -count=1
