#!/usr/bin/env bash
set -euo pipefail

control_url=${SWITCHYARD_CONTROL_URL:-http://127.0.0.1:18080}
token=${SWITCHYARD_CONTROL_READ_TOKEN:-${SWITCHYARD_CONTROL_API_TOKEN:-}}
limit=${SWITCHYARD_COMPATIBILITY_REPORT_LIMIT:-200}

if [[ -z "$token" ]]; then
  echo "SWITCHYARD_CONTROL_READ_TOKEN or SWITCHYARD_CONTROL_API_TOKEN is required" >&2
  exit 2
fi

curl --fail --silent --show-error --max-time 20 \
  -H "Authorization: Bearer $token" \
  "$control_url/api/history?limit=$limit" |
python3 -c '
import collections
import json
import sys

payload = json.load(sys.stdin)
rows = payload.get("requests", [])
counts = collections.Counter()
for row in rows:
    correlations = row.get("correlation_ids") or {}
    deprecated = correlations.get("switchyard.deprecated_backend")
    if deprecated:
        counts[deprecated] += 1

print(f"reviewed_requests={len(rows)}")
print(f"deprecated_attempts={sum(counts.values())}")
for backend in sorted(counts):
    print(f"deprecated_backend.{backend}={counts[backend]}")
'
