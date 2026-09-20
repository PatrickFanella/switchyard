#!/usr/bin/env bash
set -euo pipefail

suffix="$$"
network="${SWITCHYARD_TEST_DOCKER_NETWORK:-switchyard-test-$suffix}"
network_created=false
postgres="switchyard-test-postgres-$suffix"
api="switchyard-test-api-$suffix"
password='switchyard-test-only-password'
token='switchyard-test-control-token'

assert_equal() {
  local actual="$1" expected="$2" message="$3"
  if [[ "$actual" != "$expected" ]]; then
    printf 'Docker API isolation assertion failed: %s\n' "$message" >&2
    exit 1
  fi
}

cleanup() {
  docker rm -f "$api" "$postgres" >/dev/null 2>&1 || true
  if [[ "$network_created" == true ]]; then
    docker network rm "$network" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

if [[ -n "${SWITCHYARD_TEST_DOCKER_NETWORK:-}" ]]; then
  if ! docker network inspect "$network" >/dev/null 2>&1; then
    printf 'SWITCHYARD_TEST_DOCKER_NETWORK %q does not name an existing Docker network\n' "$network" >&2
    exit 1
  fi
else
  docker network create "$network" >/dev/null
  network_created=true
fi
docker run --detach --rm --name "$postgres" --network "$network" \
  --env POSTGRES_DB=switchyard_test --env POSTGRES_USER=switchyard_test --env POSTGRES_PASSWORD="$password" \
  postgres:17@sha256:0af65001d05296a2ead57ac4a6412433d8913d1bb5d0c88435a7d1e1ee5cb04b >/dev/null
postgres_initialized=false
for _ in $(seq 1 240); do
	if docker logs "$postgres" 2>&1 | grep -F 'PostgreSQL init process complete' >/dev/null; then
		postgres_initialized=true
		break
	fi
	sleep 0.25
done
if [[ "$postgres_initialized" != true ]]; then
	docker logs "$postgres" >&2
	echo "isolated PostgreSQL fixture did not initialize within 60 seconds" >&2
	exit 1
fi
postgres_accepting=false
for _ in $(seq 1 240); do
	if docker exec "$postgres" pg_isready --username switchyard_test --dbname switchyard_test >/dev/null 2>&1; then
		postgres_accepting=true
		break
	fi
	sleep 0.25
done
if [[ "$postgres_accepting" != true ]]; then
	docker logs "$postgres" >&2
	echo "isolated PostgreSQL fixture did not accept connections within 60 seconds after initialization" >&2
	exit 1
fi

api_args=(--detach --name "$api" --network "$network" --user 1000:1000 --read-only --cap-drop ALL --security-opt no-new-privileges --tmpfs /tmp:rw,exec,nosuid,size=256m --tmpfs /run:rw,noexec,nosuid,size=16m)
if [[ -n "${SWITCHYARD_TEST_DOCKER_NETWORK:-}" ]]; then
  control_url="http://$api:8080"
  model_url="http://$api:11434"
else
  api_args+=(--publish 127.0.0.1::8080 --publish 127.0.0.1::11434)
fi
docker run "${api_args[@]}" \
	--env "DATABASE_URL=host=$postgres port=5432 user=switchyard_test password=$password dbname=switchyard_test sslmode=disable" \
	--env "SWITCHYARD_CONTROL_API_TOKEN=$token" --env LLAMA_LINE_LISTEN_ADDR=:11434 \
	--env SWITCHYARD_SCHEDULER_ENABLED=false --env SWITCHYARD_WORKER_ENABLED=false \
  --env SWITCHYARD_COMPATIBILITY_OBSERVER_ENABLED=false \
  switchyard-api:latest >/dev/null

if [[ -z "${SWITCHYARD_TEST_DOCKER_NETWORK:-}" ]]; then
  control_port="$(docker port "$api" 8080/tcp | sed 's/.*://')"
  model_port="$(docker port "$api" 11434/tcp | sed 's/.*://')"
  control_url="http://127.0.0.1:$control_port"
  model_url="http://127.0.0.1:$model_port"
fi
api_ready=false
for _ in $(seq 1 240); do
	if curl --fail --silent "$control_url/readyz" >/dev/null 2>&1; then
		api_ready=true
		break
	fi
	if ! docker inspect --format '{{.State.Running}}' "$api" 2>/dev/null | grep -Fx true >/dev/null; then
		break
	fi
	sleep 0.25
done
if [[ "$api_ready" != true ]]; then
	docker logs "$postgres" >&2
	docker logs "$api" >&2
	echo "isolated API fixture did not become ready within 60 seconds" >&2
	exit 1
fi

assert_equal "$(docker inspect --format '{{.Config.User}}' "$api")" "1000:1000" "configured user must be non-root"
assert_equal "$(docker inspect --format '{{.HostConfig.ReadonlyRootfs}}' "$api")" "true" "root filesystem must be read-only"
assert_equal "$(docker inspect --format '{{json .HostConfig.CapDrop}}' "$api")" '["ALL"]' "all capabilities must be dropped"
assert_equal "$(docker inspect --format '{{json .HostConfig.SecurityOpt}}' "$api")" '["no-new-privileges"]' "no-new-privileges must be enabled"
assert_equal "$(docker inspect --format '{{index .HostConfig.Tmpfs "/tmp"}}' "$api")" "rw,exec,nosuid,size=256m" "/tmp must be writable and executable"
docker exec "$api" sh -eu -c 'test "$(id -u)" != 0; printf "#!/bin/sh\nexit 0\n" > /tmp/switchyard-exec-check; chmod 700 /tmp/switchyard-exec-check; /tmp/switchyard-exec-check; rm /tmp/switchyard-exec-check'

test "$(curl --silent --output /dev/null --write-out '%{http_code}' "$control_url/api/runs")" = 401
test "$(curl --silent --output /dev/null --write-out '%{http_code}' -H "Authorization: Bearer $token" "$control_url/api/runs")" = 200
test "$(curl --silent --output /dev/null --write-out '%{http_code}' -H "Authorization: Bearer $token" "$control_url/v1/models")" = 404
test "$(curl --silent --output /dev/null --write-out '%{http_code}' "$model_url/api/runs")" = 404
test "$(curl --silent --output /dev/null --write-out '%{http_code}' "$model_url/v1/models")" = 200

docker stop --time 10 "$api" >/dev/null
test "$(docker inspect --format '{{.State.ExitCode}}' "$api")" = 0
