#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
env_file="${SWITCHYARD_ENV_FILE:-$repo_root/.env}"
unit_dir="${SWITCHYARD_SYSTEMD_UNIT_DIR:-/etc/systemd/system}"
nft_dir="${SWITCHYARD_NFT_CONFIG_DIR:-/etc/switchyard}"
lib_dir="${SWITCHYARD_LIB_DIR:-/usr/lib/switchyard}"
unit_dst="$unit_dir/switchyard-web-gate.service"
rules_dst="$nft_dir/web-gate.nft"
renderer_dst="$lib_dir/render_tailscale_web_gate.py"
ufw_helper_dst="$lib_dir/manage_tailscale_web_forward.py"
docker_dropin_dir="$unit_dir/docker.service.d"
docker_dropin_dst="$docker_dropin_dir/50-switchyard-web-gate.conf"
unit_src="$repo_root/deploy/switchyard-web-gate.service"
renderer_src="$repo_root/scripts/render_tailscale_web_gate.py"
ufw_helper_src="$repo_root/scripts/manage_tailscale_web_forward.py"

if [[ "$(id -u)" -ne 0 ]]; then
  echo "install-web-gate.sh must be run with sudo/root" >&2
  exit 1
fi
for command in python3 nft ufw iptables systemctl install docker cmp mv stat; do
  if ! command -v "$command" >/dev/null 2>&1; then
    echo "$command is required" >&2
    exit 1
  fi
done

# The env file is the sole configuration input. Reject aliases and symlinks so
# both installed units and Compose retain one stable absolute path.
if ! env_file="$(python3 - "$env_file" <<'PY'
import pathlib, stat, sys
path = pathlib.Path(sys.argv[1])
if not path.is_absolute():
    raise SystemExit(1)
try:
    info = path.lstat()
    resolved = path.resolve(strict=True)
except OSError:
    raise SystemExit(1)
if stat.S_ISLNK(info.st_mode) or not stat.S_ISREG(info.st_mode) or str(path) != str(resolved):
    raise SystemExit(1)
print(resolved)
PY
)"; then
  echo "SWITCHYARD_ENV_FILE must be an absolute regular non-symlink path" >&2
  exit 1
fi
if [[ ! "$env_file" =~ ^/[A-Za-z0-9_./-]+$ ]]; then
  echo "SWITCHYARD_ENV_FILE contains characters unsafe for systemd" >&2
  exit 1
fi
if [[ ! -f "$unit_src" || -L "$unit_src" || ! -f "$renderer_src" || -L "$renderer_src" || ! -f "$ufw_helper_src" || -L "$ufw_helper_src" || ! -d "$unit_dir" || -L "$unit_dir" ]]; then
  echo "web gate install inputs are missing or unsafe" >&2
  exit 1
fi
for path in "$unit_dir" "$nft_dir" "$lib_dir" "$docker_dropin_dir"; do
  if [[ "$path" != /* || ! "$path" =~ ^/[A-Za-z0-9_./-]+$ || -L "$path" || -e "$path" && ! -d "$path" ]]; then
    echo "web gate install paths are unsafe" >&2
    exit 1
  fi
done
for install_dir in "$nft_dir" "$lib_dir"; do
  ancestor="$install_dir"
  while [[ ! -e "$ancestor" ]]; do ancestor="$(dirname "$ancestor")"; done
  read -r owner mode < <(stat -Lc '%u %a' "$ancestor")
  if [[ "$owner" != 0 || ! "$mode" =~ ^[0-7]{3,4}$ || $((8#$mode & 8#022)) -ne 0 || $((8#$mode & 8#001)) -eq 0 ]]; then
    echo "web gate install paths must be root-owned, traversable, and not group/other writable" >&2
    exit 1
  fi
done
for destination in "$unit_dst" "$rules_dst" "$renderer_dst" "$ufw_helper_dst" "$docker_dropin_dst"; do
  if [[ -L "$destination" || -e "$destination" && ! -f "$destination" ]]; then
    echo "web gate install destination must be a regular file" >&2
    exit 1
  fi
done

tmp_rules="$(mktemp)"
tmp_unit="$(mktemp)"
tmp_dropin="$(mktemp)"
rules_snapshot="$(mktemp)"
unit_snapshot="$(mktemp)"
renderer_snapshot="$(mktemp)"
ufw_helper_snapshot="$(mktemp)"
dropin_snapshot="$(mktemp)"
kernel_snapshot="$(mktemp)"
live_table="$(mktemp)"
mutation_started=0
nft_dir_created=0
lib_dir_created=0
dropin_dir_created=0
rules_existed=0
unit_existed=0
renderer_existed=0
ufw_helper_existed=0
dropin_existed=0
table_existed=0
was_enabled=0
was_active=0
forward_rule_created=0
model_rule_created=0

atomic_install() {
  local source=$1 destination=$2 mode=$3 temporary="${2}.switchyard-install.$$"
  rm -f -- "$temporary"
  install -o root -g root -m "$mode" "$source" "$temporary"
  mv -fT -- "$temporary" "$destination"
}

verify_live_gate() {
  systemctl is-active --quiet switchyard-web-gate.service || return 1
  python3 "$ufw_helper_dst" verify --env-file "$env_file" >/dev/null || return 1
  nft list table inet switchyard_web_gate >"$live_table" 2>/dev/null || return 1
  python3 - "$tmp_rules" "$live_table" <<'PY'
import pathlib, re, sys

expected = pathlib.Path(sys.argv[1]).read_text(encoding="utf-8").splitlines()
live = [" ".join(line.split()) for line in pathlib.Path(sys.argv[2]).read_text(encoding="utf-8").splitlines()]
rules = [" ".join(line.split()) for line in expected if " counter " in line]
if len(rules) != 3 or live.count("table inet switchyard_web_gate {") != 1:
    raise SystemExit(1)
chains = {"prerouting": False, "output": False}
hooks = {"prerouting": False, "output": False}
live_rules = []
current = None
for line in live:
    match = re.fullmatch(r"chain (prerouting|output) \{", line)
    if match:
        current = match.group(1)
        if chains[current]:
            raise SystemExit(1)
        chains[current] = True
        continue
    if line == "}" or line == "table inet switchyard_web_gate {":
        current = None if line == "}" else current
        continue
    if line.startswith("type filter hook "):
        if current is None or hooks[current]:
            raise SystemExit(1)
        hook = "prerouting" if current == "prerouting" else "output"
        if not re.fullmatch(rf"type filter hook {hook} priority (?:dstnat - 1|-101); policy accept;", line):
            raise SystemExit(1)
        hooks[current] = True
        continue
    if line:
        live_rules.append(line)
if not all(chains.values()) or not all(hooks.values()) or len(live_rules) != 3:
    raise SystemExit(1)
for rule in rules:
    prefix, suffix = rule.split(" counter ", 1)
    pattern = re.compile(rf"{re.escape(prefix)} counter packets [0-9]+ bytes [0-9]+ {re.escape(suffix)}")
    matches = [line for line in live_rules if pattern.fullmatch(line)]
    if len(matches) != 1:
        raise SystemExit(1)
PY
}

restore_file() {
  local snapshot=$1 destination=$2 existed=$3 temporary="${2}.switchyard-rollback.$$"
  if [[ "$existed" -eq 0 ]]; then
    rm -f -- "$destination"
    return
  fi
  rm -f -- "$temporary"
  cp -p -- "$snapshot" "$temporary"
  mv -fT -- "$temporary" "$destination"
}

cleanup() {
  status=$?
  if [[ $status -ne 0 && $mutation_started -eq 1 ]]; then
    systemctl disable --now switchyard-web-gate.service >/dev/null 2>&1 || true
    if [[ $model_rule_created -eq 1 ]]; then
      python3 "$ufw_helper_dst" delete-model --env-file "$env_file" >/dev/null 2>&1 || true
    fi
    if [[ $forward_rule_created -eq 1 ]]; then
      python3 "$ufw_helper_dst" delete-web --env-file "$env_file" >/dev/null 2>&1 || true
    fi
    restore_file "$renderer_snapshot" "$renderer_dst" "$renderer_existed" || true
    restore_file "$ufw_helper_snapshot" "$ufw_helper_dst" "$ufw_helper_existed" || true
    restore_file "$rules_snapshot" "$rules_dst" "$rules_existed" || true
    restore_file "$unit_snapshot" "$unit_dst" "$unit_existed" || true
    restore_file "$dropin_snapshot" "$docker_dropin_dst" "$dropin_existed" || true
    [[ $dropin_dir_created -eq 0 ]] || rmdir "$docker_dropin_dir" >/dev/null 2>&1 || true
    [[ $lib_dir_created -eq 0 ]] || rmdir "$lib_dir" >/dev/null 2>&1 || true
    [[ $nft_dir_created -eq 0 ]] || rmdir "$nft_dir" >/dev/null 2>&1 || true
    systemctl daemon-reload >/dev/null 2>&1 || true
    [[ $was_enabled -eq 0 ]] || systemctl enable switchyard-web-gate.service >/dev/null 2>&1 || true
    [[ $was_active -eq 0 ]] || systemctl start switchyard-web-gate.service >/dev/null 2>&1 || true
    # Starting the restored oneshot reapplies generated rules and resets
    # counters. Restore the exact prior kernel table only after unit state.
    nft delete table inet switchyard_web_gate >/dev/null 2>&1 || true
    [[ $table_existed -eq 0 ]] || nft --file "$kernel_snapshot" >/dev/null 2>&1 || true
  fi
  rm -f "$tmp_rules" "$tmp_unit" "$tmp_dropin" "$rules_snapshot" "$unit_snapshot" "$renderer_snapshot" "$ufw_helper_snapshot" "$dropin_snapshot" "$kernel_snapshot" "$live_table"
  rm -f -- "${renderer_dst}.switchyard-install.$$" "${renderer_dst}.switchyard-rollback.$$" \
    "${rules_dst}.switchyard-install.$$" "${rules_dst}.switchyard-rollback.$$" \
    "${unit_dst}.switchyard-install.$$" "${unit_dst}.switchyard-rollback.$$" \
    "${ufw_helper_dst}.switchyard-install.$$" "${ufw_helper_dst}.switchyard-rollback.$$" \
    "${docker_dropin_dst}.switchyard-install.$$" "${docker_dropin_dst}.switchyard-rollback.$$"
  return "$status"
}
trap cleanup EXIT

# Complete all input, Compose, and syntax probes before changing endpoint state.
python3 "$renderer_src" --env-file "$env_file" --check
PYTHONPATH="$repo_root/scripts" python3 "$ufw_helper_src" check-config --env-file "$env_file"
python3 "$renderer_src" --env-file "$env_file" --output "$tmp_rules"
nft --check --file "$tmp_rules"
if ! ufw status | grep -Fxq 'Status: active'; then
  echo "UFW must be active" >&2
  exit 1
fi
python3 - "$unit_src" "$tmp_unit" "$rules_dst" "$renderer_dst" "$ufw_helper_dst" "$env_file" <<'PY'
import pathlib, sys
source, target, rules, renderer, ufw_helper, env_file = map(pathlib.Path, sys.argv[1:])
value = source.read_text(encoding="utf-8")
for placeholder, replacement in (("__NFT_RULES__", rules), ("__RENDERER__", renderer), ("__UFW_HELPER__", ufw_helper), ("__ENV_FILE__", env_file)):
    value = value.replace(placeholder, str(replacement))
if any(placeholder in value for placeholder in ("__NFT_RULES__", "__RENDERER__", "__UFW_HELPER__", "__ENV_FILE__")):
    raise SystemExit("unresolved web gate unit placeholder")
target.write_text(value, encoding="utf-8")
PY
cat >"$tmp_dropin" <<'EOF'
[Unit]
Requires=switchyard-web-gate.service
After=switchyard-web-gate.service
EOF
docker compose --env-file "$env_file" -f "$repo_root/docker-compose.yml" config --services >/dev/null

if [[ -e "$rules_dst" ]]; then rules_existed=1; cp -p -- "$rules_dst" "$rules_snapshot"; fi
if [[ -e "$unit_dst" ]]; then unit_existed=1; cp -p -- "$unit_dst" "$unit_snapshot"; fi
if [[ -e "$renderer_dst" ]]; then renderer_existed=1; cp -p -- "$renderer_dst" "$renderer_snapshot"; fi
if [[ -e "$ufw_helper_dst" ]]; then ufw_helper_existed=1; cp -p -- "$ufw_helper_dst" "$ufw_helper_snapshot"; fi
if [[ -e "$docker_dropin_dst" ]]; then dropin_existed=1; cp -p -- "$docker_dropin_dst" "$dropin_snapshot"; fi
if nft list table inet switchyard_web_gate >"$kernel_snapshot" 2>/dev/null; then table_existed=1; fi
if systemctl is-enabled --quiet switchyard-web-gate.service; then was_enabled=1; fi
if systemctl is-active --quiet switchyard-web-gate.service; then was_active=1; fi

changed=0
for pair in "$tmp_rules:$rules_dst" "$tmp_unit:$unit_dst" "$renderer_src:$renderer_dst" "$ufw_helper_src:$ufw_helper_dst" "$tmp_dropin:$docker_dropin_dst"; do
  source=${pair%%:*}
  destination=${pair#*:}
  if [[ ! -f "$destination" ]] || ! cmp --silent "$source" "$destination"; then changed=1; fi
done
if [[ -f "$rules_dst" && "$(stat -Lc '%u %g %a' "$rules_dst")" != "0 0 644" ]]; then changed=1; fi
if [[ -f "$renderer_dst" && "$(stat -Lc '%u %g %a' "$renderer_dst")" != "0 0 755" ]]; then changed=1; fi
if [[ -f "$ufw_helper_dst" && "$(stat -Lc '%u %g %a' "$ufw_helper_dst")" != "0 0 755" ]]; then changed=1; fi

enforcement_active=0
if [[ -x "$ufw_helper_dst" ]] && verify_live_gate; then enforcement_active=1; fi

web_running=0
if docker compose --env-file "$env_file" -f "$repo_root/docker-compose.yml" ps --status running --services | grep -Fxq web; then
  web_running=1
fi
if [[ ( $changed -eq 1 || $enforcement_active -eq 0 ) && $web_running -eq 1 ]]; then
  mutation_started=1
  # Fail closed: never leave the old published endpoint up while gate files or
  # live rules transition. Any later failure deliberately leaves web stopped.
  docker compose --env-file "$env_file" -f "$repo_root/docker-compose.yml" stop web
fi

if [[ $changed -eq 1 ]]; then
  mutation_started=1
  if [[ ! -d "$nft_dir" ]]; then install -d -o root -g root -m 0755 "$nft_dir"; nft_dir_created=1; fi
  if [[ ! -d "$lib_dir" ]]; then install -d -o root -g root -m 0755 "$lib_dir"; lib_dir_created=1; fi
  if [[ ! -d "$docker_dropin_dir" ]]; then install -d -o root -g root -m 0755 "$docker_dropin_dir"; dropin_dir_created=1; fi
  atomic_install "$renderer_src" "$renderer_dst" 0755
  atomic_install "$ufw_helper_src" "$ufw_helper_dst" 0755
  atomic_install "$tmp_rules" "$rules_dst" 0644
  atomic_install "$tmp_unit" "$unit_dst" 0644
  atomic_install "$tmp_dropin" "$docker_dropin_dst" 0644
  systemctl daemon-reload
fi
if ! ufw_output="$(python3 "$ufw_helper_dst" ensure --env-file "$env_file")"; then
  echo "could not ensure exact UFW rules" >&2
  exit 1
fi
mapfile -t ufw_results <<<"$ufw_output"
if [[ ${#ufw_results[@]} -ne 2 || ! " ${ufw_results[*]} " =~ " web=" || ! " ${ufw_results[*]} " =~ " model=" ]]; then
  echo "UFW helper returned an invalid result" >&2
  exit 1
fi
for result in "${ufw_results[@]}"; do
  case "$result" in
    web=created) mutation_started=1; forward_rule_created=1 ;;
    model=created) mutation_started=1; model_rule_created=1 ;;
    web=existing|model=existing) ;;
    *) echo "UFW helper returned an invalid result" >&2; exit 1 ;;
  esac
done
for property in Requires After; do
  if ! systemctl show docker.service --property="$property" --value | tr ' ' '\n' | grep -Fxq switchyard-web-gate.service; then
    echo "docker.service does not load the Switchyard web gate dependency" >&2
    exit 1
  fi
done
if [[ $was_enabled -eq 0 ]]; then mutation_started=1; systemctl enable switchyard-web-gate.service; fi
if [[ $changed -eq 1 || $enforcement_active -eq 0 ]]; then
  mutation_started=1
  systemctl restart switchyard-web-gate.service
fi
if ! verify_live_gate; then
  echo "switchyard web gate is not active with the expected live rules" >&2
  exit 1
fi
