#!/usr/bin/env bash
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
release_dir="$root/release"
revision=$(git -C "$root" rev-parse HEAD)
tag=$(git -C "$root" describe --tags --exact-match 2>/dev/null || true)
built_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
ci_url=${CI_RUN_URL:-}
migrations=("$root"/backend/internal/controlplane/migrations/*.sql)
migration_head=${migrations[$((${#migrations[@]} - 1))]##*/}
archives=(
  switchyard-api.oci.tar
  switchyard-web.oci.tar
  switchyard-opencode.oci.tar
)

test -f "$release_dir/SHA256SUMS"
for archive in "${archives[@]}"; do
  test -f "$release_dir/$archive"
done

json_escape() {
  local value=$1
  value=${value//\\/\\\\}
  value=${value//\"/\\\"}
  value=${value//$'\n'/\\n}
  printf '%s' "$value"
}

{
  printf '{\n'
  printf '  "source_revision": "%s",\n' "$(json_escape "$revision")"
  printf '  "release_tag": "%s",\n' "$(json_escape "$tag")"
  printf '  "built_at": "%s",\n' "$(json_escape "$built_at")"
  printf '  "ci_run_url": "%s",\n' "$(json_escape "$ci_url")"
  printf '  "migration_head": "%s",\n' "$(json_escape "$migration_head")"
  printf '  "images": [\n'
  for index in "${!archives[@]}"; do
    archive=${archives[$index]}
    archive_sha=$(sha256sum "$release_dir/$archive" | awk '{print $1}')
    index_digest=$(tar -xOf "$release_dir/$archive" index.json |
      node -e 'let s="";process.stdin.on("data",d=>s+=d);process.stdin.on("end",()=>process.stdout.write(JSON.parse(s).manifests[0].digest))')
    comma=,
    if [[ "$index" -eq $((${#archives[@]} - 1)) ]]; then
      comma=
    fi
    printf '    {"archive": "%s", "archive_sha256": "%s", "oci_index_digest": "%s"}%s\n' \
      "$(json_escape "$archive")" "$(json_escape "$archive_sha")" "$(json_escape "$index_digest")" "$comma"
  done
  printf '  ],\n'
  printf '  "checksums_file": "SHA256SUMS"\n'
  printf '}\n'
} >"$release_dir/manifest.json"
