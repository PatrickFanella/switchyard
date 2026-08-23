#!/usr/bin/env sh
set -eu

CONFIG_DIR="${OPENCODE_CONFIG_DIR:-${XDG_CONFIG_HOME:-/home/opencode/.config}/opencode}"
DEFAULT_DIR="${1:-/opt/switchyard-opencode/default-config}"
MANAGED_CONFIG_DIR="${XDG_CONFIG_HOME:-/home/opencode/.config}/opencode"
AUTH_FILE="${XDG_DATA_HOME:-${HOME:-/home/opencode}/.local/share}/opencode/auth.json"

case "${OPENCODE_SECURITY_MODE:-strict}" in
  strict)
    if [ "$CONFIG_DIR" != "$MANAGED_CONFIG_DIR" ]; then
      echo "strict mode requires the managed OpenCode config directory" >&2
      exit 64
    fi
    if [ "${OPENCODE_DISABLE_PROJECT_CONFIG:-}" != "true" ]; then
      echo "strict mode requires OPENCODE_DISABLE_PROJECT_CONFIG=true" >&2
      exit 64
    fi
    if [ "${OPENCODE_DB:-}" != ":memory:" ]; then
      echo "strict mode requires OPENCODE_DB=:memory:" >&2
      exit 64
    fi
    if [ "${OPENCODE_DISABLE_CLAUDE_CODE_PROMPT:-}" != "true" ]; then
      echo "strict mode requires OPENCODE_DISABLE_CLAUDE_CODE_PROMPT=true" >&2
      exit 64
    fi
    for variable in OPENCODE_CONFIG OPENCODE_CONFIG_CONTENT OPENCODE_PERMISSION OPENCODE_AUTH_CONTENT; do
      case "$variable" in
        OPENCODE_CONFIG) value="${OPENCODE_CONFIG:-}" ;;
        OPENCODE_CONFIG_CONTENT) value="${OPENCODE_CONFIG_CONTENT:-}" ;;
        OPENCODE_PERMISSION) value="${OPENCODE_PERMISSION:-}" ;;
        OPENCODE_AUTH_CONTENT) value="${OPENCODE_AUTH_CONTENT:-}" ;;
      esac
      if [ -n "$value" ]; then
        echo "strict mode does not allow $variable" >&2
        exit 64
      fi
    done
    if [ -e "${HOME:-/home/opencode}/.claude/CLAUDE.md" ] || [ -L "${HOME:-/home/opencode}/.claude/CLAUDE.md" ]; then
      echo "strict mode does not allow a global Claude instruction file" >&2
      exit 64
    fi
    node "$DEFAULT_DIR/sanitize-opencode-auth.mjs" "$AUTH_FILE"
    for directory in plugin plugins agent agents command commands mode modes skill skills blacktower instruction instructions; do
      rm -rf "$CONFIG_DIR/$directory"
    done
    rm -f \
      "$CONFIG_DIR/opencode.json" \
      "$CONFIG_DIR/opencode.jsonc" \
      "$CONFIG_DIR/config" \
      "$CONFIG_DIR/config.json" \
      "$CONFIG_DIR/AGENTS.md" \
      "$CONFIG_DIR/CLAUDE.md" \
      "$CONFIG_DIR/instructions.md"
    ;;
  development) ;;
  *)
    echo "OPENCODE_SECURITY_MODE must be strict or development" >&2
    exit 64
    ;;
esac

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
