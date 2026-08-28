#!/usr/bin/env sh

load_opencode_password() {
  password_file="${OPENCODE_SERVER_PASSWORD_FILE:-}"
  if [ -z "$password_file" ]; then
    [ -n "${OPENCODE_SERVER_PASSWORD:-}" ] || {
      echo "OpenCode server password or password file is required" >&2
      return 64
    }
    return 0
  fi
  [ -z "${OPENCODE_SERVER_PASSWORD:-}" ] || {
    echo "OpenCode password and password file cannot both be configured" >&2
    return 64
  }
  case "$password_file" in
    /*) ;;
    *) echo "OpenCode password file must be absolute" >&2; return 64 ;;
  esac
  [ -f "$password_file" ] && [ ! -L "$password_file" ] || {
    echo "OpenCode password file must be a regular non-symlink file" >&2
    return 73
  }
  password_owner=$(stat -c %u "$password_file")
  if [ "$password_owner" -ne 0 ] && [ "$password_owner" -ne "${OPENCODE_RUNTIME_UID:-10001}" ]; then
    echo "OpenCode password file owner is invalid" >&2
    return 73
  fi
  password_mode=$(stat -c %a "$password_file")
  if [ $((password_mode % 100)) -ne 0 ]; then
    echo "OpenCode password file must not grant group or world access" >&2
    return 73
  fi
  password_size=$(stat -c %s "$password_file")
  runtime_uid="${OPENCODE_RUNTIME_UID:-10001}"
  runtime_gid="${OPENCODE_RUNTIME_GID:-10001}"
  if [ "$(id -u)" -eq 0 ] && [ "$password_owner" -eq "$runtime_uid" ] && [ "$password_owner" -ne 0 ]; then
    command -v su-exec >/dev/null 2>&1 || {
      echo "OpenCode password file owner cannot be assumed" >&2
      return 73
    }
    password_lines=$(su-exec "$runtime_uid:$runtime_gid" sh -c 'wc -l < "$1"' sh "$password_file") || {
      echo "OpenCode password file is unreadable by its owner" >&2
      return 73
    }
    OPENCODE_SERVER_PASSWORD=$(su-exec "$runtime_uid:$runtime_gid" sh -c 'IFS= read -r value < "$1"; printf "%s" "$value"' sh "$password_file") || {
      echo "OpenCode password file is unreadable by its owner" >&2
      return 73
    }
  else
    password_lines=$(wc -l < "$password_file") || {
      echo "OpenCode password file is unreadable" >&2
      return 73
    }
    IFS= read -r OPENCODE_SERVER_PASSWORD < "$password_file" || {
      echo "OpenCode password file is unreadable" >&2
      return 73
    }
  fi
  if [ "$password_size" -lt 32 ] || [ "$password_size" -gt 4096 ] || [ "$password_lines" -ne 1 ]; then
    echo "OpenCode password file content is invalid" >&2
    return 73
  fi
  [ "${#OPENCODE_SERVER_PASSWORD}" -ge 32 ] || {
    echo "OpenCode password file content is invalid" >&2
    return 73
  }
  export OPENCODE_SERVER_PASSWORD
}
