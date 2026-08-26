#!/usr/bin/env bash
set -euo pipefail

if [[ ${SWITCHYARD_RESTORE_ADAPTER_INNER:-0} != 1 ]]; then
  if [[ $# -ne 7 || ! ${6} =~ ^[1-9][0-9]*$ || ! ${7} =~ ^[1-9][0-9]*$ ]]; then
    echo "postgres restore adapter requires archive, metadata, object contract, payload path, attempt ID, timeout, and reclaim threshold" >&2
    exit 2
  fi
  exec timeout --signal=TERM --kill-after=15 "${6}s" env SWITCHYARD_RESTORE_ADAPTER_INNER=1 "$0" "${1}" "${2}" "${3}" "${4}" "${5}" "${7}"
fi

if [[ $# -ne 6 ]]; then
  exit 2
fi

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
docker() { "$root/deploy/docker-local" "$@"; }
image='postgres:17@sha256:0af65001d05296a2ead57ac4a6412433d8913d1bb5d0c88435a7d1e1ee5cb04b'
archive=$1
metadata=$2
object_contract=$3
payload_path=$4
attempt_id=$5
reclaim_seconds=$6
name="switchyard-restore-${attempt_id}"
api_name="${name}-api"
network_name="${name}-network"
api_image="${api_name}:local"
tmp=$(mktemp -d)
stage=restore
error_code=adapter_interrupted
completed=0
database_version=
api_ready=false
api_auth=false
lock_id=
postgres_id=
api_id=
network_id=
api_image_id=
created_epoch=$(date +%s)
reclaim_epoch=$((created_epoch + reclaim_seconds))
lock_name=switchyard-postgres-restore-lock
success_payload_tmp=
failure_payload_tmp=

write_payload() {
  local result=$1
  local target=${2:-$payload_path}
  PAYLOAD_RESULT=$result PAYLOAD_STAGE=$stage PAYLOAD_ERROR=$error_code \
    PAYLOAD_DATABASE_VERSION=$database_version PAYLOAD_API_READY=$api_ready PAYLOAD_API_AUTH=$api_auth \
    python3 - "$target" "$tmp/migration-before" "$tmp/objects" "$tmp/counts" "$tmp/migration-after" <<'PY'
import json
import os
import pathlib
import sys

def lines(path):
    value = pathlib.Path(path)
    try:
        return value.read_text(encoding="utf-8").splitlines() if value.exists() else None
    except OSError:
        return None

counts = None
count_lines = lines(sys.argv[4])
if count_lines is not None:
    counts = {}
    for line in count_lines:
        name, value = line.split("=", 1)
        counts[name] = int(value)

value = {
    "contract_version": "switchyard.postgres-restore-payload.v1",
    "provider": "postgres",
    "result": os.environ["PAYLOAD_RESULT"],
    "stage": os.environ["PAYLOAD_STAGE"],
    "error_code": os.environ["PAYLOAD_ERROR"] or None,
    "database_version": os.environ["PAYLOAD_DATABASE_VERSION"] or None,
    "migration_before": lines(sys.argv[2]),
    "required_objects": lines(sys.argv[3]),
    "row_counts": counts,
    "migration_after": lines(sys.argv[5]),
    "api_ready": os.environ["PAYLOAD_API_READY"] == "true",
    "api_auth_workflows": os.environ["PAYLOAD_API_AUTH"] == "true",
}
descriptor = os.open(sys.argv[1], os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
with os.fdopen(descriptor, "w", encoding="utf-8") as handle:
    json.dump(value, handle, sort_keys=True, separators=(",", ":"))
    handle.write("\n")
    handle.flush()
    os.fsync(handle.fileno())
PY
}

container_details() {
  docker inspect --format '{{index .Config.Labels "switchyard.restore.owner"}} {{index .Config.Labels "switchyard.restore.type"}} {{index .Config.Labels "switchyard.restore.attempt"}} {{index .Config.Labels "switchyard.restore.created"}} {{index .Config.Labels "switchyard.restore.reclaim_after"}}' "$1"
}

image_details() {
  docker image inspect --format '{{index .Config.Labels "switchyard.restore.owner"}} {{index .Config.Labels "switchyard.restore.type"}} {{index .Config.Labels "switchyard.restore.attempt"}} {{index .Config.Labels "switchyard.restore.created"}} {{index .Config.Labels "switchyard.restore.reclaim_after"}}' "$1"
}

network_details() {
  docker network inspect --format '{{index .Labels "switchyard.restore.owner"}} {{index .Labels "switchyard.restore.type"}} {{index .Labels "switchyard.restore.attempt"}} {{index .Labels "switchyard.restore.created"}} {{index .Labels "switchyard.restore.reclaim_after"}}' "$1"
}

verify_details() {
  local details=$1 expected_type=$2 expected_attempt=$3 expected_created=$4 expected_reclaim=$5
  local owner resource_type owner_attempt owner_created owner_reclaim
  read -r owner resource_type owner_attempt owner_created owner_reclaim <<<"$details"
  [[ $owner == switchyard && $resource_type == "$expected_type" && $owner_attempt == "$expected_attempt" && \
     $owner_created == "$expected_created" && $owner_reclaim == "$expected_reclaim" ]]
}

remove_owned_container() {
  local id=$1 resource_type=$2
  local details
  details=$(container_details "$id") || return 1
  verify_details "$details" "$resource_type" "$attempt_id" "$created_epoch" "$reclaim_epoch" || return 1
  docker rm -f "$id" >/dev/null || return 1
  ! docker inspect "$id" >/dev/null 2>&1
}

remove_owned_image() {
  local details
  details=$(image_details "$api_image_id") || return 1
  verify_details "$details" api-image "$attempt_id" "$created_epoch" "$reclaim_epoch" || return 1
  docker image rm "$api_image_id" >/dev/null || return 1
  ! docker image inspect "$api_image_id" >/dev/null 2>&1
}

remove_owned_network() {
  local details
  details=$(network_details "$network_id") || return 1
  verify_details "$details" network "$attempt_id" "$created_epoch" "$reclaim_epoch" || return 1
  docker network rm "$network_id" >/dev/null || return 1
  ! docker network inspect "$network_id" >/dev/null 2>&1
}

cleanup_resources() {
  local failed=0
  if [[ -n $api_id ]]; then if remove_owned_container "$api_id" api; then api_id=; else failed=1; fi; fi
  if [[ -n $postgres_id ]]; then if remove_owned_container "$postgres_id" postgres; then postgres_id=; else failed=1; fi; fi
  if [[ -n $network_id ]]; then if remove_owned_network; then network_id=; else failed=1; fi; fi
  if [[ -n $api_image_id ]]; then if remove_owned_image; then api_image_id=; else failed=1; fi; fi
  return "$failed"
}

cleanup_tmp() {
  [[ -e $tmp ]] || return 0
  chmod -R u+rwX "$tmp" >/dev/null 2>&1 || return 1
  rm -rf -- "$tmp" || return 1
  [[ ! -e $tmp ]]
}

release_lock() {
  local details
  [[ -n $lock_id ]] || return 0
  details=$(container_details "$lock_id") || return 1
  verify_details "$details" lock "$attempt_id" "$created_epoch" "$reclaim_epoch" || return 1
  docker rm -f "$lock_id" >/dev/null || return 1
  ! docker inspect "$lock_id" >/dev/null 2>&1 || return 1
  lock_id=
}

cleanup() {
  local failed=0
  cleanup_resources || failed=1
  release_lock || failed=1
  if [[ $failed -ne 0 ]]; then
    stage=cleanup
    error_code=resource_cleanup_failed
  fi
  if [[ $completed -eq 0 && ! -e $payload_path ]]; then
    failure_payload_tmp="${payload_path}.${attempt_id}.failure.private"
    write_payload failure "$failure_payload_tmp" || failed=1
  fi
  if ! cleanup_tmp; then
    failed=1
    stage=cleanup
    error_code=resource_cleanup_failed
    rm -f -- "$failure_payload_tmp" >/dev/null 2>&1 || true
    if [[ $completed -eq 0 && ! -e $payload_path ]]; then
      write_payload failure "$failure_payload_tmp" || failed=1
    fi
  fi
  if [[ -n $success_payload_tmp && -e $success_payload_tmp ]]; then
    rm -f -- "$success_payload_tmp" || failed=1
  fi
  if [[ $completed -eq 0 && ! -e $payload_path && -n $failure_payload_tmp && -e $failure_payload_tmp ]]; then
    if ! ln -- "$failure_payload_tmp" "$payload_path" || ! rm -- "$failure_payload_tmp"; then
      rm -f -- "$payload_path" "$failure_payload_tmp" >/dev/null 2>&1 || true
      failed=1
    fi
  fi
}
trap cleanup EXIT
trap 'exit 143' TERM INT

if [[ -L $archive || -L $metadata || -L $object_contract || -e $payload_path || -L $payload_path ]]; then
  error_code=unsafe_adapter_path
  exit 1
fi
umask 077

labels() {
  local resource_type=$1
  printf '%s\n' --label switchyard.restore.owner=switchyard \
    --label "switchyard.restore.type=${resource_type}" \
    --label "switchyard.restore.attempt=${attempt_id}" \
    --label "switchyard.restore.created=${created_epoch}" \
    --label "switchyard.restore.reclaim_after=${reclaim_epoch}"
}

create_lock() {
  local args=()
  mapfile -t args < <(labels lock)
  lock_id=$(docker create --name "$lock_name" "${args[@]}" --entrypoint /bin/sh "$image" -c 'while :; do sleep 3600; done' 2>/dev/null) || return 1
  docker start "$lock_id" >/dev/null || { docker rm -f "$lock_id" >/dev/null 2>&1 || true; lock_id=; return 1; }
}

acquire_lock() {
  create_lock && return 0
  local details existing_id owner resource_type existing_attempt existing_created existing_reclaim
  details=$(docker inspect --format '{{.Id}} {{index .Config.Labels "switchyard.restore.owner"}} {{index .Config.Labels "switchyard.restore.type"}} {{index .Config.Labels "switchyard.restore.attempt"}} {{index .Config.Labels "switchyard.restore.created"}} {{index .Config.Labels "switchyard.restore.reclaim_after"}}' "$lock_name" 2>/dev/null) || return 1
  read -r existing_id owner resource_type existing_attempt existing_created existing_reclaim <<<"$details"
  [[ $owner == switchyard && $resource_type == lock && $existing_attempt =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$ && \
     $existing_created =~ ^[0-9]+$ && $existing_reclaim =~ ^[0-9]+$ && $existing_reclaim -ge $existing_created && \
     $((created_epoch - existing_created)) -gt $reclaim_seconds ]] || return 1
  docker rm -f "$existing_id" >/dev/null || return 1
  create_lock
}

if ! acquire_lock; then
  lock_id=
  error_code=restore_already_running
  exit 1
fi

remove_or_block_containers() {
  local id details owner resource_type owner_attempt owner_created owner_reclaim ids
  ids=$(docker ps -aq --filter label=switchyard.restore.owner=switchyard) || return 1
  while read -r id; do
    [[ -z $id ]] && continue
    details=$(container_details "$id") || return 1
    read -r owner resource_type owner_attempt owner_created owner_reclaim <<<"$details"
    if [[ $resource_type == lock ]]; then
      verify_details "$details" lock "$attempt_id" "$created_epoch" "$reclaim_epoch" || return 1
      continue
    fi
    [[ $owner == switchyard && $resource_type =~ ^(api|postgres)$ && $owner_attempt =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$ && \
       $owner_created =~ ^[0-9]+$ && $owner_reclaim =~ ^[0-9]+$ && $owner_reclaim -ge $owner_created ]] || return 1
    [[ $owner_attempt != "$attempt_id" ]] || continue
    (( created_epoch - owner_created > reclaim_seconds )) || return 1
    docker rm -f "$id" >/dev/null || return 1
  done <<<"$ids"
}

remove_or_block_networks() {
  local id details owner resource_type owner_attempt owner_created owner_reclaim ids
  ids=$(docker network ls -q --filter label=switchyard.restore.owner=switchyard) || return 1
  while read -r id; do
    [[ -z $id ]] && continue
    details=$(network_details "$id") || return 1
    read -r owner resource_type owner_attempt owner_created owner_reclaim <<<"$details"
    [[ $owner == switchyard && $resource_type == network && $owner_attempt =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$ && \
       $owner_created =~ ^[0-9]+$ && $owner_reclaim =~ ^[0-9]+$ && $owner_reclaim -ge $owner_created ]] || return 1
    [[ $owner_attempt != "$attempt_id" ]] || continue
    (( created_epoch - owner_created > reclaim_seconds )) || return 1
    docker network rm "$id" >/dev/null || return 1
  done <<<"$ids"
}

remove_or_block_images() {
  local id details owner resource_type owner_attempt owner_created owner_reclaim ids
  ids=$(docker image ls -q --filter label=switchyard.restore.owner=switchyard) || return 1
  while read -r id; do
    [[ -z $id ]] && continue
    details=$(image_details "$id") || return 1
    read -r owner resource_type owner_attempt owner_created owner_reclaim <<<"$details"
    [[ $owner == switchyard && $resource_type == api-image && $owner_attempt =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$ && \
       $owner_created =~ ^[0-9]+$ && $owner_reclaim =~ ^[0-9]+$ && $owner_reclaim -ge $owner_created ]] || return 1
    [[ $owner_attempt != "$attempt_id" ]] || continue
    (( created_epoch - owner_created > reclaim_seconds )) || return 1
    docker image rm "$id" >/dev/null || return 1
  done <<<"$ids"
}

stage=cleanup
error_code=orphan_cleanup_failed
remove_or_block_containers
remove_or_block_networks
remove_or_block_images

password=$(python3 -c 'import secrets; print(secrets.token_urlsafe(32))')
api_token=$(python3 -c 'import secrets; print(secrets.token_urlsafe(32))')

network_args=()
mapfile -t network_args < <(labels network)
stage=restore
error_code=postgres_start_failed
network_id=$(docker network create --internal "${network_args[@]}" "$network_name")

postgres_args=()
mapfile -t postgres_args < <(labels postgres)
postgres_id=$(docker run --detach --name "$name" "${postgres_args[@]}" --network "$network_name" \
  --env POSTGRES_DB=switchyard_restore --env POSTGRES_USER=switchyard_restore --env POSTGRES_PASSWORD="$password" "$image")

error_code=postgres_ready_failed
for _ in $(seq 1 240); do
  docker exec "$postgres_id" psql --username switchyard_restore --dbname switchyard_restore --command 'select 1' >/dev/null 2>&1 && break
  sleep 0.25
done
docker exec "$postgres_id" psql --username switchyard_restore --dbname switchyard_restore --command 'select 1' >/dev/null
error_code=archive_copy_failed
docker cp "$archive" "$postgres_id:/tmp/switchyard.dump"
error_code=archive_restore_failed
docker exec "$postgres_id" pg_restore --exit-on-error --no-owner --no-privileges \
  --username switchyard_restore --dbname switchyard_restore /tmp/switchyard.dump >/dev/null

stage=pre_api
error_code=pre_api_contract_failed
docker exec "$postgres_id" psql --username switchyard_restore --dbname switchyard_restore --tuples-only --no-align \
  --command 'select version from schema_migrations order by version' >"$tmp/migration-before"
python3 - "$metadata" "$tmp/migration-before" <<'PY'
import json, pathlib, sys
metadata = json.loads(pathlib.Path(sys.argv[1]).read_text(encoding="utf-8"))
actual = pathlib.Path(sys.argv[2]).read_text(encoding="utf-8").splitlines()
if actual != metadata["migration_ledger"] or not actual or actual[-1] != metadata["migration_head"]:
    raise SystemExit(1)
PY

docker exec "$postgres_id" psql --username switchyard_restore --dbname switchyard_restore --tuples-only --no-align \
  --command "select tablename from pg_tables where schemaname='public' order by tablename" >"$tmp/objects"
python3 - "$object_contract" "$tmp/objects" <<'PY'
import json, pathlib, sys
contract = json.loads(pathlib.Path(sys.argv[1]).read_text(encoding="utf-8"))
actual = pathlib.Path(sys.argv[2]).read_text(encoding="utf-8").splitlines()
if actual != contract["required_objects"]:
    raise SystemExit(1)
PY

while read -r table; do
  count=$(docker exec "$postgres_id" psql --username switchyard_restore --dbname switchyard_restore --tuples-only --no-align \
    --command "select count(*) from public.\"${table}\"")
  [[ $count =~ ^[0-9]+$ ]]
  printf '%s=%s\n' "$table" "$count" >>"$tmp/counts"
done <"$tmp/objects"
database_version=$(docker exec "$postgres_id" psql --username switchyard_restore --dbname switchyard_restore --tuples-only --no-align --command 'show server_version_num' | tr -d '[:space:]')

stage=api
error_code=api_start_failed
image_args=()
mapfile -t image_args < <(labels api-image)
api_image_id=$(docker build --quiet "${image_args[@]}" --tag "$api_image" "$root/backend")
api_args=()
mapfile -t api_args < <(labels api)
api_id=$(docker run --detach --name "$api_name" "${api_args[@]}" --network "$network_name" \
  --user 1000:1000 --read-only --cap-drop ALL --security-opt no-new-privileges \
  --tmpfs /tmp:rw,exec,nosuid,size=256m --tmpfs /run:rw,noexec,nosuid,size=16m \
  --env "DATABASE_URL=host=$name port=5432 user=switchyard_restore password=$password dbname=switchyard_restore sslmode=disable" \
  --env SWITCHYARD_LISTEN_ADDR=0.0.0.0:8080 --env SWITCHYARD_CONTROL_API_TOKEN="$api_token" \
  --env SWITCHYARD_WORKER_ENABLED=false --env SWITCHYARD_SCHEDULER_ENABLED=false "$api_image")

for _ in $(seq 1 100); do
  if docker exec "$api_id" wget --quiet --output-document=/dev/null http://127.0.0.1:8080/readyz >/dev/null 2>&1; then
    api_ready=true
    break
  fi
  docker inspect --format '{{.State.Running}}' "$api_id" 2>/dev/null | grep -qx true || exit 1
  sleep 0.1
done
[[ $api_ready == true ]]
error_code=api_auth_failed
docker exec "$api_id" wget --quiet --output-document=/dev/null --header="Authorization: Bearer $api_token" \
  http://127.0.0.1:8080/api/workflows
api_auth=true
docker exec "$postgres_id" psql --username switchyard_restore --dbname switchyard_restore --tuples-only --no-align \
  --command 'select version from schema_migrations order by version' >"$tmp/migration-after"

stage=cleanup
error_code=resource_cleanup_failed
cleanup_resources
release_lock
stage=complete
error_code=
success_payload_tmp="${payload_path}.${attempt_id}.private"
write_payload success "$success_payload_tmp"
if ! cleanup_tmp; then
  rm -f -- "$success_payload_tmp" || true
  success_payload_tmp=
  stage=cleanup
  error_code=resource_cleanup_failed
  exit 1
fi
if ! ln -- "$success_payload_tmp" "$payload_path" || ! rm -- "$success_payload_tmp"; then
  rm -f -- "$payload_path" "$success_payload_tmp" || true
  success_payload_tmp=
  stage=cleanup
  error_code=resource_cleanup_failed
  exit 1
fi
success_payload_tmp=
completed=1
printf 'provider=postgres\nattempt_id=%s\nresult=success\n' "$attempt_id"
