#!/usr/bin/env sh
set -eu

# Compose starts this bootstrap as root only to initialize empty named volumes.
# Bind-mounted config/data directories must already have safe ownership/modes.
runtime_uid="${OPENCODE_RUNTIME_UID:-10001}"
runtime_gid="${OPENCODE_RUNTIME_GID:-10001}"
runtime_home="/home/opencode"
runtime_config_home="${XDG_CONFIG_HOME:-$runtime_home/.config}"
runtime_data_home="${XDG_DATA_HOME:-$runtime_home/.local/share}"
runtime_cache_home="${XDG_CACHE_HOME:-$runtime_home/.cache}"
runtime_state_home="${XDG_STATE_HOME:-$runtime_home/.local/state}"
worktree_root="${OPEN_PILOT_WORKTREE_ROOT:-/workspace/.open-pilot-worktrees}"

. /usr/local/libexec/switchyard-opencode-password
load_opencode_password

case "$runtime_uid:$runtime_gid" in
  *[!0-9:]* | *:*:*)
    echo "OPENCODE_RUNTIME_UID and OPENCODE_RUNTIME_GID must be numeric" >&2
    exit 64
    ;;
esac
if [ "$runtime_uid" -eq 0 ] || [ "$runtime_gid" -eq 0 ]; then
  echo "OPENCODE_RUNTIME_UID and OPENCODE_RUNTIME_GID must be non-root" >&2
  exit 64
fi

if [ "$runtime_config_home" != "/home/opencode/.config" ] || \
   [ "$runtime_data_home" != "/home/opencode/.local/share" ] || \
   [ "$runtime_cache_home" != "/home/opencode/.cache" ] || \
   [ "$runtime_state_home" != "/home/opencode/.local/state" ]; then
  echo "OpenCode XDG directories must use the managed container paths" >&2
  exit 64
fi

bind_dirs="$runtime_config_home/opencode $runtime_data_home/opencode"
named_volume_dirs="$runtime_state_home $runtime_cache_home"

if [ "$(id -u)" -eq 0 ]; then
  for directory in $bind_dirs; do
    if [ ! -d "$directory" ] || [ -L "$directory" ]; then
      echo "$directory must be an existing non-symlink directory" >&2
      exit 73
    fi
    if [ "$(stat -c '%u:%g' "$directory")" != "$runtime_uid:$runtime_gid" ]; then
      echo "$directory ownership must match OPENCODE_RUNTIME_UID:OPENCODE_RUNTIME_GID" >&2
      exit 73
    fi
    case "$(stat -c '%A' "$directory")" in
      ?????w????|????????w?)
        echo "$directory must not be group- or world-writable" >&2
        exit 73
        ;;
    esac
    if ! su-exec "$runtime_uid:$runtime_gid" test -r "$directory" || \
       ! su-exec "$runtime_uid:$runtime_gid" test -w "$directory" || \
       ! su-exec "$runtime_uid:$runtime_gid" test -x "$directory"; then
      echo "$directory must be readable, writable, and traversable by the OpenCode runtime user" >&2
      exit 73
    fi
  done

  for directory in $named_volume_dirs; do
    mkdir -p "$directory"
    if [ "$(stat -c '%u:%g' "$directory")" != "$runtime_uid:$runtime_gid" ]; then
      if [ -n "$(ls -A "$directory")" ]; then
        echo "$directory has content with unexpected ownership; refusing recursive chown" >&2
        exit 73
      fi
      chown "$runtime_uid:$runtime_gid" "$directory"
    fi
  done

  if ! su-exec "$runtime_uid:$runtime_gid" test -w "$worktree_root"; then
    echo "OPEN_PILOT_WORKTREE_ROOT must be writable by OPENCODE_RUNTIME_UID:OPENCODE_RUNTIME_GID ($runtime_uid:$runtime_gid)" >&2
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

if ! test -w "$worktree_root"; then
  echo "OPEN_PILOT_WORKTREE_ROOT is not writable by the OpenCode runtime user" >&2
  exit 73
fi

exec "$@"
