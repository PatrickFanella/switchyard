#!/usr/bin/env bash
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
image='postgres:17@sha256:0af65001d05296a2ead57ac4a6412433d8913d1bb5d0c88435a7d1e1ee5cb04b'
attempt="restore-fixture-$$"
postgres_name="${attempt}-postgres"
api_name="${attempt}-api"
api_image="${attempt}:local"
network_name="${attempt}-network"
password=$(python3 -c 'import secrets; print(secrets.token_urlsafe(32))')
api_token=$(python3 -c 'import secrets; print(secrets.token_urlsafe(32))')
tmp=$(mktemp -d)
chmod 0700 "$tmp"
mkdir "$tmp/bundles" "$tmp/evidence"
chmod 0700 "$tmp/bundles" "$tmp/evidence"

cleanup() {
  docker rm -f "$api_name" "$postgres_name" >/dev/null 2>&1 || true
  docker network rm "$network_name" >/dev/null 2>&1 || true
  docker image rm "$api_image" >/dev/null 2>&1 || true
  rm -rf "$tmp"
}
trap cleanup EXIT

python3 - "$tmp/policy.json" <<'PY'
import json, pathlib, sys
pathlib.Path(sys.argv[1]).write_text(json.dumps({
    "contract_version": "switchyard.operator-policy.v1",
    "policy_id": "integration-fixture",
    "policy_version": 1,
    "provider": "postgres",
    "storage_owner": "continuous-integration",
    "encryption_owner": "continuous-integration",
    "encryption_expectation": "ephemeral runner storage",
    "retention_duration": "P1D",
    "declared_rpo_seconds": 3600,
    "declared_rto_seconds": 300,
    "drill_calendar": "manual production gate",
    "max_drill_interval_seconds": 86400,
    "alert_grace_seconds": 0
}), encoding="utf-8")
PY

docker network create --internal "$network_name" >/dev/null
docker run --detach --name "$postgres_name" --network "$network_name" \
  --env POSTGRES_DB=switchyard_fixture --env POSTGRES_USER=switchyard_fixture --env POSTGRES_PASSWORD="$password" \
  "$image" >/dev/null
for _ in $(seq 1 240); do
  docker exec "$postgres_name" psql --username switchyard_fixture --dbname switchyard_fixture --command 'select 1' >/dev/null 2>&1 && break
  sleep 0.25
done
docker exec "$postgres_name" psql --username switchyard_fixture --dbname switchyard_fixture --command 'select 1' >/dev/null
docker build --quiet --tag "$api_image" "$root/backend" >/dev/null
docker run --detach --name "$api_name" --network "$network_name" \
  --user 1000:1000 --read-only --cap-drop ALL --security-opt no-new-privileges \
  --tmpfs /tmp:rw,exec,nosuid,size=256m --tmpfs /run:rw,noexec,nosuid,size=16m \
  --env "DATABASE_URL=host=$postgres_name port=5432 user=switchyard_fixture password=$password dbname=switchyard_fixture sslmode=disable" \
  --env SWITCHYARD_LISTEN_ADDR=0.0.0.0:8080 --env SWITCHYARD_CONTROL_API_TOKEN="$api_token" \
  --env SWITCHYARD_WORKER_ENABLED=false --env SWITCHYARD_SCHEDULER_ENABLED=false "$api_image" >/dev/null
for _ in $(seq 1 100); do
  docker exec "$api_name" wget --quiet --output-document=/dev/null http://127.0.0.1:8080/readyz >/dev/null 2>&1 && break
  sleep 0.1
done
docker exec "$api_name" wget --quiet --output-document=/dev/null http://127.0.0.1:8080/readyz >/dev/null
docker rm -f "$api_name" >/dev/null

docker exec "$postgres_name" pg_dump --format=custom --no-owner --no-privileges \
  --username switchyard_fixture --dbname switchyard_fixture >"$tmp/bundles/.fixture.dump"
docker exec "$postgres_name" psql --username switchyard_fixture --dbname switchyard_fixture --tuples-only --no-align \
  --command 'select version from schema_migrations order by version' >"$tmp/ledger"
database_version=$(docker exec "$postgres_name" psql --username switchyard_fixture --dbname switchyard_fixture --tuples-only --no-align \
  --command 'show server_version_num' | tr -d '[:space:]')
python3 "$root/scripts/restore_evidence.py" publish-bundle \
  --policy "$tmp/policy.json" --archive-temp "$tmp/bundles/.fixture.dump" --output-dir "$tmp/bundles" \
  --bundle-id fixture --archive-basename fixture.dump --source-revision "$(git -C "$root" rev-parse HEAD)" \
  --database-version "$database_version" --migration-ledger "$tmp/ledger" --created-at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" >/dev/null

if ! python3 "$root/scripts/restore_evidence.py" restore --bundle "$tmp/bundles/fixture.complete.json" \
  --policy "$tmp/policy.json" --evidence-dir "$tmp/evidence" --timeout-seconds 300 --disposable >/dev/null; then
  python3 - "$tmp/evidence" <<'PY'
import json, pathlib, sys
files = list(pathlib.Path(sys.argv[1]).glob("restore-evidence-*.json"))
if files:
    value = json.loads(files[0].read_text(encoding="utf-8"))
    print(f"restore fixture failed: stage={value['stage']} code={value['error_code']}", file=sys.stderr)
PY
  exit 1
fi
python3 - "$tmp/evidence" <<'PY'
import json, pathlib, sys
files = list(pathlib.Path(sys.argv[1]).glob("restore-evidence-*.json"))
if len(files) != 1:
    raise SystemExit(1)
value = json.loads(files[0].read_text(encoding="utf-8"))
if value["result"] != "success" or not value["qualification"]["current_policy_qualified"]:
    raise SystemExit(1)
PY

printf 'PostgreSQL completed-bundle restore integration passed\n'
