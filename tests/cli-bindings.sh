#!/usr/bin/env bash
set -Eeuo pipefail

repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work_dir="$(mktemp -d)"
trap 'rm -rf -- "$work_dir"' EXIT

# Load the manager without invoking its interactive main function.
source <(sed '$d' "$repo_dir/freedom.sh")
CONFIG_DIR="$work_dir"
CONFIG_PATH="$work_dir/config.json"
META_PATH="$work_dir/freedom-meta.json"
PUBLIC_PORTS_PATH="$work_dir/freedom-public-ports.json"
STATS_STORE_PATH="$work_dir/freedom-stats.json"
BOT_BIN_PATH="$work_dir/freedom-bot"
BOT_CONFIG_PATH="$work_dir/bot.json"

cat > "$CONFIG_PATH" <<'JSON'
{"inbounds":[
 {"tag":"raw","port":443,"protocol":"vless","settings":{"clients":[{"id":"test-uuid","email":"alice+cmF3","flow":"xtls-rprx-vision"},{"id":"legacy-id","email":"bob"}]},"streamSettings":{"network":"tcp","security":"reality","realitySettings":{"serverNames":["example.com"],"shortIds":["abcd"],"privateKey":"key"}}},
 {"tag":"cdn","port":8443,"protocol":"vless","settings":{"clients":[{"id":"test-uuid","email":"alice+Y2Ru"}]},"streamSettings":{"network":"xhttp","security":"tls","tlsSettings":{"certificates":[{"certificateFile":"/tmp/example.com.crt"}]},"xhttpSettings":{"path":"/test"}}}
]}
JSON
cat > "$BOT_BIN_PATH" <<'SH'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$CONFIG_DIR/bot-args"
SH
chmod 700 "$BOT_BIN_PATH"
export CONFIG_DIR

[[ "$(get_all_system_users)" == $'alice|test-uuid\nbob|legacy-id' ]]
[[ "$(binding_email_for_node alice raw)" == 'alice+cmF3' ]]

get_public_ip() { printf '%s' '203.0.113.1'; }
derive_reality_public_key() { printf '%s' 'public-key'; }
# Without the Bot binary the terminal still builds links locally.
links="$(BOT_BIN_PATH="$work_dir/missing-bot" show_user_node_links alice test-uuid <<< $'\n' 2>/dev/null)"
[[ "$links" == *'vless://test-uuid@203.0.113.1:443?'* ]]
[[ "$links" == *'vless://test-uuid@example.com:8443?'* ]]
[[ "$links" != *'not assigned to the inbound'* ]]

# With the Bot binary, links come from its agent, exactly as Telegram exports them.
agent_bot="$work_dir/agent-bot"
cat > "$agent_bot" <<'SH'
#!/usr/bin/env bash
printf '%s\n' "$*" > "$CONFIG_DIR/agent-args"
cat > "$CONFIG_DIR/agent-input"
printf '%s\n' '{"schema_version":1,"operation":"users.export","ok":true,"data":[{"tag":"raw","uri":"vless://agent-link@vpn.example.com:443?security=reality#raw"}]}'
SH
chmod 700 "$agent_bot"
links="$(BOT_BIN_PATH="$agent_bot" show_user_node_links alice test-uuid <<< $'\n')"
[[ "$links" == *'vless://agent-link@vpn.example.com:443?security=reality#raw'* && "$links" != *'203.0.113.1'* ]]
[[ "$(cat "$CONFIG_DIR/agent-args")" == "agent users.export --include-sensitive --config $BOT_CONFIG_PATH" ]]
[[ "$(jq -c . "$CONFIG_DIR/agent-input")" == '{"user":"alice"}' ]]

manage_user_node_binding_interactive alice <<< $'1\ny\n' >/dev/null
grep -q -- '-local-user-action unassign -local-user alice -local-node raw' "$CONFIG_DIR/bot-args"

ensure_stats_api_scaffold() { :; }
service_unit_exists() { :; }
systemctl() { :; }
reset_traffic_stats_interactive <<< $'2\n1\n\n' >/dev/null
grep -q -- '-local-user-traffic-reset-locked -local-user alice' "$CONFIG_DIR/bot-args"

locked_config_operation() { :; }
get_xray_stats_json() {
  printf '%s\n' '{"stat":[{"name":"user>>>alice+cmF3>>>traffic>>>uplink","value":"100"},{"name":"user>>>alice+Y2Ru>>>traffic>>>uplink","value":"200"},{"name":"user>>>alice+cmF3>>>traffic>>>downlink","value":"10"},{"name":"user>>>alice+Y2Ru>>>traffic>>>downlink","value":"20"}]}'
}
get_or_update_persistent_user_traffic() { printf '%s|%s' "$2" "$3"; }
BOT_BIN_PATH="$work_dir/missing-bot"
dashboard="$(user_management_menu <<< '0')"
[[ "$dashboard" == *'300 B'* && "$dashboard" == *'30 B'* && "$dashboard" == *'330 B'* ]]

online='{"users":[{"email":"alice+cmF3","ips":[{"ip":"203.0.113.7"}]},{"email":"alice+Y2Ru","ips":{"203.0.113.8":1800000000}},{"email":"bob","ips":[]}]}'
rows="$(online_user_rows "$online")"
[[ "$rows" == *$'alice\t203.0.113.7, 203.0.113.8'* ]]
[[ "$rows" != *'alice+cmF3'* && "$rows" != *'alice+Y2Ru'* ]]

# set -e ignores negated commands, so expected failures are asserted explicitly.
fails() { if "$@"; then echo "unexpected success: $*" >&2; return 1; fi; }

# Menu choices stay decimal with leading zeros instead of aborting or reading octal.
[[ "$(menu_index 08 9)" == 7 && "$(menu_index 010 10)" == 9 && "$(menu_index 1 1)" == 0 ]]
fails menu_index 0 3
fails menu_index 4 3
fails menu_index abc 3
fails menu_index 1234567 9999999

# The core must accept a candidate before it replaces the live configuration.
BIN="$work_dir/fake-xray"
cat > "$BIN" <<'SH'
#!/usr/bin/env bash
[[ "$1 $2 $3" == 'run -test -config' && "$4" == *.json ]] || exit 2
! grep -q '"reject-me"' "$4"
SH
chmod 700 "$BIN"
set_xray_config_permissions() { :; }
original="$(cat "$CONFIG_PATH")"
fails commit_config '{"inbounds":[],"tag":"reject-me"}' 2>/dev/null
[[ "$(cat "$CONFIG_PATH")" == "$original" ]]
backup_existing_config 2>/dev/null
commit_config '{"inbounds":[]}'
[[ "$(jq -c . "$CONFIG_PATH")" == '{"inbounds":[]}' ]]
[[ -z "$(find "$CONFIG_DIR" -maxdepth 1 -name '.freedom-check-*')" ]]
backup_matches_live_config
# A later edit made elsewhere, such as by the Bot, must not be offered for rollback.
printf '%s\n' '{"inbounds":[],"log":{}}' > "$CONFIG_PATH"
fails backup_matches_live_config
commit_config '{"inbounds":[]}'
fails backup_matches_live_config
BIN=""
fails commit_config '{"inbounds":[]}' 2>/dev/null

# A failed firewall rule is reported as a failure, never as applied.
(
  systemctl() { return 1; }
  iptables() { return 1; }
  ufw() { [[ "$1" == "status" ]] && { echo "Status: active"; return 0; }; return 1; }
  out="$(auto_open_firewall_port 8443 tcp test 2>&1)"
  [[ "$out" == *"could not allow port 8443/tcp"* && "$out" != *"[OK]"* && "$out" != *"No active firewall"* ]]
  ufw() { [[ "$1" == "status" ]] && echo "Status: active"; return 0; }
  out="$(auto_open_firewall_port 8443 tcp test 2>&1)"
  [[ "$out" == *"[OK] Firewall (ufw): allowed port 8443/tcp"* ]]
)

# Uninstall archives the configuration first and never runs a remote script.
(
  backup_dir="$(mktemp -d)"
  trap 'rm -rf -- "$backup_dir"' EXIT
  UNINSTALL_BACKUP_DIR="$backup_dir"
  rm() { printf '%s\n' "$*" >> "$backup_dir/rm-args"; }
  systemctl() { :; }
  curl() { echo "curl must not run" >&2; return 1; }
  restore_linux_kernel_tuning() { :; }
  out="$(uninstall_xray_completely <<< 'y' 2>&1)" || true
  archive="$(find "$backup_dir" -name 'freedom-xray-backup-*.tar.gz')"
  [[ -n "$archive" && "$(stat -c %a "$archive")" == 600 ]]
  tar -tzf "$archive" | grep -q 'config.json'
  grep -q -- '-rf /usr/local/bin/xray' "$backup_dir/rm-args"
  [[ "$out" != *'curl must not run'* && "$out" == *'Configuration archived to'* ]]
)

# With the Bot installed, the inbound overview shows nobody's credentials.
(
  ensure_config_scaffold() { :; }
  cat > "$CONFIG_PATH" <<'JSON'
{"inbounds":[{"tag":"raw","port":443,"protocol":"vless","settings":{"clients":[{"id":"test-uuid","email":"alice+cmF3","flow":"xtls-rprx-vision"}]},"streamSettings":{"network":"tcp","security":"reality","realitySettings":{"serverNames":["example.com"],"shortIds":["abcd"],"privateKey":"key"}}}]}
JSON
  out="$(BOT_BIN_PATH="$agent_bot" list_and_show_nodes <<< $'\n' 2>&1)"
  [[ "$out" == *'raw'* && "$out" != *'vless://'* && "$out" == *'Links belong to users'* ]]
  out="$(BOT_BIN_PATH="$work_dir/missing-bot" list_and_show_nodes <<< $'\n\n' 2>&1)"
  [[ "$out" == *'vless://test-uuid@'* ]]
)

echo 'CLI binding compatibility checks passed'
