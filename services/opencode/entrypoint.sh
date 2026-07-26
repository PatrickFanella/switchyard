#!/usr/bin/env sh
set -eu

# Compose starts this small bootstrap as root only so it can make the persistent
# OpenCode state writable for the configured unprivileged runtime identity. The
# server itself is always exec'd through su-exec below.
runtime_uid="${OPENCODE_RUNTIME_UID:-10001}"
runtime_gid="${OPENCODE_RUNTIME_GID:-10001}"
runtime_home="/home/opencode"
runtime_config_home="${XDG_CONFIG_HOME:-$runtime_home/.config}"
runtime_data_home="${XDG_DATA_HOME:-$runtime_home/.local/share}"
runtime_cache_home="${XDG_CACHE_HOME:-$runtime_home/.cache}"
runtime_state_home="${XDG_STATE_HOME:-$runtime_home/.local/state}"

case "$runtime_uid:$runtime_gid" in
  *[!0-9:]* | *:*:*)
    echo "OPENCODE_RUNTIME_UID and OPENCODE_RUNTIME_GID must be numeric" >&2
    exit 64
    ;;
esac

state_dirs="$runtime_config_home/opencode $runtime_data_home/opencode $runtime_state_home $runtime_cache_home"

if [ "$(id -u)" -eq 0 ]; then
  for directory in $state_dirs; do
    mkdir -p "$directory"
    # Existing bind-mounted OpenCode trees can contain mode-0700 backups.
    # With only CAP_CHOWN (and deliberately no DAC_OVERRIDE), bootstrap root
    # cannot traverse those trees even when they already belong to the target
    # runtime identity. Avoid the unnecessary recursive walk in that case.
    directory_owner="$(stat -c '%u:%g' "$directory")"
    if [ "$directory_owner" != "$runtime_uid:$runtime_gid" ]; then
      chown -R "$runtime_uid:$runtime_gid" "$directory"
    fi
  done

  if ! su-exec "$runtime_uid:$runtime_gid" test -w /workspace; then
    echo "/workspace must be writable by OPENCODE_RUNTIME_UID:OPENCODE_RUNTIME_GID ($runtime_uid:$runtime_gid)" >&2
    exit 73
  fi

  # su-exec derives HOME from /etc/passwd. UID 1000 is the base image's `node`
  # user, so explicitly retain the managed OpenCode XDG locations after drop.
  exec su-exec "$runtime_uid:$runtime_gid" env \
    HOME="$runtime_home" \
    XDG_CONFIG_HOME="$runtime_config_home" \
    XDG_DATA_HOME="$runtime_data_home" \
    XDG_CACHE_HOME="$runtime_cache_home" \
    XDG_STATE_HOME="$runtime_state_home" \
    "$@"
fi

if ! test -w /workspace; then
  echo "/workspace is not writable by the OpenCode runtime user" >&2
  exit 73
fi

exec "$@"
