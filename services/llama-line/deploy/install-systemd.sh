#!/usr/bin/env bash
set -Eeuo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
binary="$repo_root/bin/llama-line"

install -d -m 0755 /etc/llama-line
if [[ ! -f /etc/llama-line/env ]]; then
  install -m 0644 "$repo_root/deploy/llama-line.env.example" /etc/llama-line/env
fi

if command -v go >/dev/null 2>&1; then
  go build -o "$binary" "$repo_root/cmd/app"
elif [[ -x "$binary" ]]; then
  printf 'go not found in root PATH; using existing binary: %s\n' "$binary"
else
  printf 'error: go not found in root PATH and %s does not exist. Run `go build -o bin/llama-line ./cmd/app` as your user first.\n' "$binary" >&2
  exit 1
fi
install -m 0644 "$repo_root/deploy/llama-line.service" /etc/systemd/system/llama-line.service
systemctl daemon-reload
systemctl enable llama-line.service
systemctl restart llama-line.service
systemctl --no-pager --full status llama-line.service
