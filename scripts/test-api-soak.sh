#!/usr/bin/env bash
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
duration=${SWITCHYARD_SOAK_DURATION:-30}
clients=${SWITCHYARD_SOAK_CLIENTS:-8}
minimum_requests=${SWITCHYARD_SOAK_MIN_REQUESTS:-200}
token=soak-control-token
tmp=$(mktemp -d)
api_pid=
client_pids=()

cleanup() {
  if [[ -n "$api_pid" ]] && kill -0 "$api_pid" 2>/dev/null; then
    kill -TERM "$api_pid" 2>/dev/null || true
    wait "$api_pid" 2>/dev/null || true
  fi
  rm -rf "$tmp"
}
trap cleanup EXIT

if ! [[ "$duration" =~ ^[1-9][0-9]*$ && "$clients" =~ ^[1-9][0-9]*$ && "$minimum_requests" =~ ^[1-9][0-9]*$ ]]; then
  echo "soak duration, clients, and minimum requests must be positive integers" >&2
  exit 2
fi

port=$(python3 - <<'PY'
import socket
s = socket.socket()
s.bind(("127.0.0.1", 0))
print(s.getsockname()[1])
s.close()
PY
)

(cd "$root/backend" && CGO_ENABLED=0 go build -trimpath -ldflags='-buildid=' -o "$tmp/api" ./cmd/app)
SWITCHYARD_LISTEN_ADDR="127.0.0.1:$port" \
SWITCHYARD_MODEL_LISTEN_ADDR="127.0.0.1:0" \
SWITCHYARD_CONTROL_API_TOKEN="$token" \
SWITCHYARD_RATE_LIMIT_PER_SECOND=100000 \
SWITCHYARD_WORKER_ENABLED=false \
SWITCHYARD_SCHEDULER_ENABLED=false \
"$tmp/api" >"$tmp/api.log" 2>&1 &
api_pid=$!

base="http://127.0.0.1:$port"
for _ in $(seq 1 100); do
  if curl --fail --silent --show-error "$base/readyz" >/dev/null 2>&1; then
    break
  fi
  if ! kill -0 "$api_pid" 2>/dev/null; then
    cat "$tmp/api.log" >&2
    exit 1
  fi
  sleep 0.1
done
curl --fail --silent --show-error "$base/readyz" >/dev/null

deadline=$((SECONDS + duration))
for client in $(seq 1 "$clients"); do
  (
    count=0
    while (( SECONDS < deadline )); do
      for path in /api/workflows /api/triggers /api/runs /api/model-broker/status /metrics; do
        curl --fail --silent --show-error --max-time 5 \
          -H "Authorization: Bearer $token" "$base$path" >/dev/null || exit 1
        count=$((count + 1))
      done
    done
    printf '%s\n' "$count" >"$tmp/count.$client"
  ) >"$tmp/client.$client.log" 2>&1 &
  client_pids+=("$!")
done

failed=0
for job in "${client_pids[@]}"; do
  if ! wait "$job"; then
    failed=1
  fi
done
if (( failed != 0 )); then
  cat "$tmp"/client.*.log >&2
  cat "$tmp/api.log" >&2
  exit 1
fi
if ! kill -0 "$api_pid" 2>/dev/null; then
  cat "$tmp/api.log" >&2
  echo "API exited during soak" >&2
  exit 1
fi

requests=$(awk '{ total += $1 } END { print total + 0 }' "$tmp"/count.*)
if (( requests < minimum_requests )); then
  echo "soak completed only $requests requests; expected at least $minimum_requests" >&2
  exit 1
fi

kill -TERM "$api_pid"
if ! wait "$api_pid"; then
  cat "$tmp/api.log" >&2
  echo "API did not terminate cleanly after soak" >&2
  exit 1
fi
api_pid=
echo "API soak passed: $requests requests across $clients clients for ${duration}s"
