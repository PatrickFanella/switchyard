#!/usr/bin/env bash
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)

required_docs=(
  docs/status.md
  docs/roadmap.md
  docs/adr/0003-native-switchyard-modules.md
  docs/runbooks/release-and-rollback.md
  docs/runbooks/postgres-backup-and-restore.md
  docs/runbooks/incident-response.md
  docs/runbooks/native-model-broker-cutover.md
  docs/reference/client-compatibility.md
  docs/openapi.yaml
)

for path in "${required_docs[@]}"; do
  test -f "$root/$path"
done

required_routes=(
  /api/operator-summary
  /api/operator-safety
  /api/agent-profiles
  /api/workflow-definitions
  /api/runs
  /api/triggers
)

for route in "${required_routes[@]}"; do
  grep -Fq "$route" "$root/docs/openapi.yaml"
  grep -Fq "$route" "$root/backend/internal/controlplane/http_routes.go"
done

if grep -RIl '^# .*Implementation Plan$' "$root/docs/superpowers/plans" |
  while read -r plan; do
    sed -n '1,8p' "$plan" | grep -Eq 'Historical|completed plan'
  done; then
  :
else
  echo "every dated implementation plan must declare historical/completed status near its title" >&2
  exit 1
fi

grep -Fq 'make verify-production' "$root/docs/status.md"
grep -Fq 'production-smoke' "$root/docs/status.md"
echo "Documentation consistency checks passed"
