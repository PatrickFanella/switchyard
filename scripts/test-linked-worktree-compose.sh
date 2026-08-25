#!/usr/bin/env bash
set -euo pipefail

project="switchyard-linked-worktree-$$"
network_octet=$((($$ % 50) + 200))
network_probe="${project}-network-probe"
network_reserved=false
for _ in $(seq 1 50); do
  test_subnet="10.${network_octet}.0.0/24"
  if docker network create --subnet "$test_subnet" "$network_probe" >/dev/null 2>&1; then
    docker network rm "$network_probe" >/dev/null
    network_reserved=true
    break
  fi
  network_octet=$((network_octet + 1))
  if [ "$network_octet" -gt 249 ]; then network_octet=200; fi
done
if docker network inspect "$network_probe" >/dev/null 2>&1; then
  docker network rm "$network_probe" >/dev/null
fi
if [ "$network_reserved" != true ]; then
  printf 'Unable to reserve a non-overlapping linked-worktree test subnet\n' >&2
  exit 1
fi
uid="$(id -u)"
gid="$(id -g)"
if [ "$uid" -eq 0 ] || [ "$gid" -eq 0 ]; then
  uid=1000
  gid=1000
fi
daemon_name="${project}-${RANDOM}-$(date +%s)"
daemon_root="/tmp/${daemon_name}"
workspace="${daemon_root}/workspace"
state_root="${daemon_root}/opencode-state"
client_tmp="$(mktemp -d)"
helper_image="alpine:3.22@sha256:14358309a308569c32bdc37e2e0e9694be33a9d99e68afb0f5ff33cc1f695dce"

daemon_tmp() {
  docker run --rm --user 0:0 \
    --mount type=bind,source=/tmp,target=/daemon-tmp \
    "$helper_image" "$@"
}

cleanup() {
  COMPOSE_PROJECT_NAME="$project" docker compose down --volumes --remove-orphans >/dev/null 2>&1 || true
  daemon_tmp sh -eu -c 'rm -rf "/daemon-tmp/$1"' sh "$daemon_name" >/dev/null 2>&1 || true
  rm -rf "$client_tmp"
}
trap cleanup EXIT

daemon_tmp sh -eu -c '
  root="/daemon-tmp/$1"
  uid="$2"
  gid="$3"
  mkdir -p "$root/workspace/metrics" "$root/workspace/.open-pilot-worktrees" \
    "$root/opencode-state/config" "$root/opencode-state/data"
  chown -R "$uid:$gid" "$root"
  chmod 700 "$root" "$root/workspace" "$root/workspace/.open-pilot-worktrees" \
    "$root/opencode-state/config" "$root/opencode-state/data"
  printf "switchyard-managed-worktree-root\n" >"$root/workspace/.open-pilot-worktrees/.switchyard-managed"
  chown "$uid:$gid" "$root/workspace/.open-pilot-worktrees/.switchyard-managed"
  chmod 600 "$root/workspace/.open-pilot-worktrees/.switchyard-managed"
' sh "$daemon_name" "$uid" "$gid"

export COMPOSE_PROJECT_NAME="$project"
export OPENCODE_WORKSPACE="$workspace"
export OPENCODE_CONFIG_DIR="$state_root/config"
export OPENCODE_DATA_DIR="$state_root/data"
export SWITCHYARD_BACKUP_METRICS_DIR="$workspace/metrics"
export OPENCODE_RUNTIME_UID="$uid"
export OPENCODE_RUNTIME_GID="$gid"
export OPENCODE_SECURITY_MODE=strict
export OPENCODE_SERVER_PASSWORD=linked-smoke-password
export SWITCHYARD_DOCKER_BRIDGE_SUBNET="$test_subnet"
export SWITCHYARD_DOCKER_BRIDGE_GATEWAY="10.${network_octet}.0.1"
export SWITCHYARD_API_CONTAINER_IPV4="10.${network_octet}.0.4"
export SWITCHYARD_WEB_CONTAINER_IPV4="10.${network_octet}.0.10"
export SWITCHYARD_DOCKER_BRIDGE_NAME="sywt${network_octet}"

docker compose run --rm --no-deps --user "$uid:$gid" --entrypoint sh api -eu -c '
  git init --initial-branch=main /workspace >/dev/null
  git -C /workspace config user.email switchyard@example.test
  git -C /workspace config user.name "Switchyard Test"
  git -C /workspace remote add origin https://git.example.test/owner/repo.git
  printf "linked worktree smoke\n" >/workspace/README.md
  git -C /workspace add README.md
  git -C /workspace commit -m initial >/dev/null
'

docker compose run --rm --no-deps --user "$uid:$gid" --entrypoint git api \
  -C /workspace worktree add -b linked-smoke /workspace/.open-pilot-worktrees/live main >/dev/null

docker compose run --rm --no-deps --user "$uid:$gid" --entrypoint sh api -eu -c '
  cat >/workspace/.open-pilot-worktrees/live/go.mod <<"EOF"
module example.test/verifier-smoke

go 1.26
EOF
  cat >/workspace/.open-pilot-worktrees/live/tiny_test.go <<"EOF"
package smoke

import "testing"

func TestTiny(t *testing.T) {}
EOF
'

docker compose up -d --no-deps opencode verifier >/dev/null
opencode_container="$(docker compose ps -q opencode)"
verifier_container="$(docker compose ps -q verifier)"
for container in "$opencode_container" "$verifier_container"; do
  for _ in $(seq 1 120); do
    health="$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{end}}' "$container")"
    if [ "$health" = healthy ]; then
      break
    fi
    if [ "$health" = unhealthy ]; then
      printf 'Container became unhealthy: %s\n' "$container" >&2
      exit 1
    fi
    sleep 1
  done
  test "$(docker inspect --format '{{.State.Health.Status}}' "$container")" = healthy
done

docker compose exec -T --user 0:0 opencode sh -eu -c '
  actual_uid="$(sed -n "s/^Uid:[[:space:]]*\([0-9][0-9]*\).*/\1/p" /proc/1/status)"
  test "$actual_uid" = "$1"
' sh "$uid"

merged_config="$client_tmp/merged-opencode.json"
docker cp "$opencode_container:/home/opencode/.config/opencode/opencode.json" "$merged_config"
python3 - "$merged_config" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as config_file:
    config = json.load(config_file)
if config.get("permission", {}).get("bash") != "deny":
    raise SystemExit("merged strict config does not deny permission.bash")
PY

docker compose exec -T --user "$uid:$gid" opencode sh -eu -c \
  'test "$(git -C /workspace/.open-pilot-worktrees/live rev-parse --show-toplevel)" = /workspace/.open-pilot-worktrees/live; git -C /workspace/.open-pilot-worktrees/live diff --stat >/dev/null'

docker compose run --rm --no-deps --entrypoint wget verifier -qO- \
  --header='Content-Type: application/json' \
  --post-data='{"command":["git","diff","--stat"],"workdir":"/workspace/.open-pilot-worktrees/live","timeout":"5s"}' \
  http://verifier:8090/verify | python3 -c 'import json,sys; assert "output" in json.load(sys.stdin)'

verify() {
  payload="$1"
  docker compose run --rm --no-deps --entrypoint node verifier -e \
    'fetch("http://verifier:8090/verify", {method:"POST", headers:{"content-type":"application/json"}, body:process.argv[1]}).then(async response => { process.stdout.write(await response.text()); if (!response.ok) process.exitCode = 1 })' \
    "$payload"
}

for payload in \
  '{"command":["git","--version"],"workdir":"/workspace/.open-pilot-worktrees/live","timeout":"30s"}' \
  '{"command":["go","version"],"workdir":"/workspace/.open-pilot-worktrees/live","timeout":"30s"}' \
  '{"command":["make","--version"],"workdir":"/workspace/.open-pilot-worktrees/live","timeout":"30s"}' \
  '{"command":["npm","--version"],"workdir":"/workspace/.open-pilot-worktrees/live","timeout":"30s"}' \
  '{"command":["cargo","--version"],"workdir":"/workspace/.open-pilot-worktrees/live","timeout":"30s"}'; do
  verify "$payload" | python3 -c 'import json,sys; data=json.load(sys.stdin); assert "error" not in data and data.get("output")'
done
verify '{"command":["pnpm","--version"],"workdir":"/workspace/.open-pilot-worktrees/live","timeout":"30s"}' \
  | python3 -c 'import json,sys; data=json.load(sys.stdin); assert data.get("output") == "10.33.0" and "error" not in data'
verify '{"command":["go","test","./..."],"workdir":"/workspace/.open-pilot-worktrees/live","timeout":"2m"}' \
  | python3 -c 'import json,sys; data=json.load(sys.stdin); assert "error" not in data, data; assert "example.test/verifier-smoke" in data.get("output", ""), data'

verify '{"command":["sh","-c","sleep 30 >/dev/null 2>&1 &"],"workdir":"/workspace/.open-pilot-worktrees/live","timeout":"30s"}' \
  | python3 -c 'import json,sys; data=json.load(sys.stdin); assert "error" not in data, data'
verify '{"command":["sh","-c","setsid sleep 30 >/dev/null 2>&1 &"],"workdir":"/workspace/.open-pilot-worktrees/live","timeout":"30s"}' \
  | python3 -c 'import json,sys; data=json.load(sys.stdin); assert "error" not in data, data'
docker compose exec -T verifier sh -eu -c \
  'for command in /proc/[0-9]*/cmdline; do [ -r "$command" ] || continue; case "$(tr "\000" " " <"$command")" in "sleep 30 "*) exit 1;; esac; done'

printf 'Linked-worktree Compose smoke passed\n'
