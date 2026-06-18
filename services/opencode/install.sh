#!/usr/bin/env sh
set -eu

CONFIG_DIR="${OPENCODE_CONFIG_DIR:-/root/.config/opencode}"
DEFAULT_DIR="/opt/switchyard-opencode/default-config"

mkdir -p "$CONFIG_DIR/plugins" "$CONFIG_DIR/agents" "$CONFIG_DIR/skills"

copy_tree() {
  src="$1"
  dst="$2"
  if [ -d "$src" ]; then
    mkdir -p "$dst"
    cp -R "$src"/. "$dst"/
  fi
}

copy_tree "$DEFAULT_DIR/plugins" "$CONFIG_DIR/plugins"
copy_tree "$DEFAULT_DIR/agents" "$CONFIG_DIR/agents"
copy_tree "$DEFAULT_DIR/skills" "$CONFIG_DIR/skills"

node "$DEFAULT_DIR/merge-opencode-config.mjs" "$CONFIG_DIR/opencode.json" "$DEFAULT_DIR/opencode.defaults.json"

if command -v opencode >/dev/null 2>&1; then
  opencode --version || true
fi

if command -v rtk >/dev/null 2>&1; then
  rtk --version || true
fi
