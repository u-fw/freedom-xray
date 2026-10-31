#!/usr/bin/env bash
# freedom.sh v11.0-modular-manager
# A modular Xray-core lifecycle manager for VLESS (REALITY, XHTTP, TLS).
# Official configuration references: https://xtls.github.io/config/ and https://github.com/xtls/xray-core
#
# Architecture principles:
# 1. Uses the official Xray-core without introducing alternative proxy cores.
# 2. Stores managed inbounds in one Xray configuration file and supports CRUD operations.
# 3. Built-in configuration templates:
#    - Template 1: VLESS + TCP + REALITY + Vision for direct connections.
#    - Template 2: VLESS + XHTTP + REALITY without an external TLS certificate.
#    - Template 3: VLESS + XHTTP + TLS for trusted CDN deployments.
# 4. Operations and security features:
#    - Checks both Xray configuration and system listener ports for conflicts.
#    - Supports optional ML-KEM-768 and ML-DSA-65 where the installed core provides them.
#    - Offers explicit BBR, TCP Fast Open, buffer and file-descriptor tuning.
#    - Lists inbounds and exports client URLs and terminal QR codes.

set -Eeuo pipefail

RED='\033[31m'
GREEN='\033[32m'
YELLOW='\033[33m'
CYAN='\033[36m'
MAGENTA='\033[35m'
BOLD='\033[1m'
NC='\033[0m'

export PATH="$PATH:/usr/local/bin:/usr/bin:/bin:/usr/local/sbin:/usr/sbin:/sbin"

SCRIPT_VERSION="v13.5.0"
# Optional client-side ECH configuration for XHTTP/TLS links; does not modify origin TLS.
XHTTP_CDN_ECH_CONFIG="cloudflare-ech.com+https://dns.alidns.com/dns-query"
DEFAULT_SNI="www.nvidia.com"
DEFAULT_PORT="443"
DEFAULT_NODE_NAME="Premium-Node"
DEFAULT_ADMIN_EMAIL="admin"

GITHUB_REPO="u-fw/freedom-xray"
RAW_SCRIPT_URL="https://raw.githubusercontent.com/${GITHUB_REPO}/main/freedom.sh"
GHPROXY_SCRIPT_URL="https://ghproxy.net/https://raw.githubusercontent.com/${GITHUB_REPO}/main/freedom.sh"

BIN=""
SERVICE_NAME="xray"
CONFIG_DIR="/usr/local/etc/xray"
CONFIG_PATH="$CONFIG_DIR/config.json"
CERTS_DIR="$CONFIG_DIR/certs"
# Auxiliary inbound metadata for fields such as VLESS client encryption not stored in Xray configuration.
META_PATH="$CONFIG_DIR/freedom-meta.json"
PUBLIC_PORTS_PATH="$CONFIG_DIR/freedom-public-ports.json"
STATS_STORE_PATH="$CONFIG_DIR/freedom-stats.json"
BACKUP_FILE=""

BOT_BIN_PATH="/usr/local/bin/freedom-bot"
BOT_CONFIG_PATH="$CONFIG_DIR/bot.json"
BOT_SERVICE="freedom-bot"
BOT_SERVICE_FILE="/etc/systemd/system/freedom-bot.service"

# Send diagnostics to stderr so command substitutions capture only returned data.
# Log messages must not corrupt values such as listener ports or certificate paths.
info() { echo -e "${CYAN}[INFO] $*${NC}" >&2; }
ok() { echo -e "${GREEN}[OK] $*${NC}" >&2; }
warn() { echo -e "${YELLOW}[WARN] $*${NC}" >&2; }
fatal() { echo -e "${RED}[ERROR] $*${NC}" >&2; exit 1; }

need_root() {
  [[ "$(id -u)" == "0" ]] || fatal "Run as root: sudo bash $0"
}

ensure_systemd_available() {
  command -v systemctl >/dev/null 2>&1 || fatal "systemctl not found; systemd is required to manage Xray."
  # A systemd instance normally exposes /run/systemd/system when it is PID 1.
  # Checking this directory is less expensive than enumerating systemctl D-Bus units.
  [[ -d /run/systemd/system ]] || fatal "systemd is not PID 1. Run this script on a VPS with a functional systemd instance, not an unsupported container."
}

# Synchronize live Xray counters to the Bot's persistent journal before a
# managed restart. Detect an inherited config lock to avoid recursive flock.
snapshot_traffic_before_restart() {
  [[ -x "$BOT_BIN_PATH" ]] || return 0
  local mode="-local-traffic-snapshot"
  local held
  held="$(readlink "/proc/$BASHPID/fd/9" 2>/dev/null || true)"
  [[ "$held" != "$CONFIG_DIR/.freedom-config.lock" ]] || mode="-local-traffic-snapshot-locked"
  if ! "$BOT_BIN_PATH" -c "$BOT_CONFIG_PATH" "$mode" >/dev/null; then
    warn "Pre-restart traffic sampling failed; the last unsampled window may be lost. Historical totals are preserved."
  fi
}
restart_xray_preserving_traffic() {
  snapshot_traffic_before_restart
  systemctl restart "$SERVICE_NAME"
}

service_unit_exists() {
  local svc="$1"
  [[ -f "/etc/systemd/system/${svc}.service" || -f "/lib/systemd/system/${svc}.service" || -f "/usr/lib/systemd/system/${svc}.service" ]] && return 0
  systemctl cat "${svc}.service" >/dev/null 2>&1
}

print_service_diagnostics() {
  local svc="$1"
  warn "${svc}.service status: "
  systemctl status "$svc" --no-pager -l >&2 || true
  warn "${svc}.service recent logs: "
  journalctl -u "$svc" -n 80 --no-pager >&2 || true
}

restore_backup() {
  [[ -n "${BACKUP_FILE:-}" && -f "$BACKUP_FILE" ]] || return 1
  cp -a "$BACKUP_FILE" "$CONFIG_PATH"
  chmod 640 "$CONFIG_PATH" 2>/dev/null || true
  ok "Restored backup configuration: $BACKUP_FILE -> $CONFIG_PATH"

  if service_unit_exists "$SERVICE_NAME"; then
    info "Attempting to restart ${SERVICE_NAME}.service to restore the previous configuration"
    if restart_xray_preserving_traffic; then
      sleep 1
      if systemctl is-active --quiet "$SERVICE_NAME"; then
        ok "${SERVICE_NAME}.service is running again."
      else
        warn "${SERVICE_NAME}.service is not active after restart."
        print_service_diagnostics "$SERVICE_NAME"
      fi
    fi
  fi
}

fatal_with_rollback() {
  local msg="$1"
  local ans=""
  if [[ -n "${BACKUP_FILE:-}" && -f "$BACKUP_FILE" ]]; then
    warn "$msg"
    read -r -p "Restore this backup configuration? [y/N]: " ans || ans="N"
    case "${ans:-N}" in
      y|Y|yes|YES) restore_backup || warn "Backup restoration failed. Check manually: $BACKUP_FILE" ;;
      *) warn "Current configuration retained. Backup: $BACKUP_FILE" ;;
    esac
  fi
  fatal "$msg"
}

install_packages() {
  local pkgs=("$@")
  ((${#pkgs[@]} > 0)) || return 0

  if command -v apt-get >/dev/null 2>&1; then
    apt-get update -y >/dev/null
    DEBIAN_FRONTEND=noninteractive apt-get install -y "${pkgs[@]}" >/dev/null
  elif command -v dnf >/dev/null 2>&1; then
    dnf install -y "${pkgs[@]}" >/dev/null
  elif command -v yum >/dev/null 2>&1; then
    yum install -y "${pkgs[@]}" >/dev/null
  elif command -v apk >/dev/null 2>&1; then
    apk add --no-cache "${pkgs[@]}" >/dev/null
  else
    fatal "No supported package manager found. Install manually: ${pkgs[*]}"
  fi
}

install_port_detection_dep() {
  command -v ss >/dev/null 2>&1 && return 0
  command -v lsof >/dev/null 2>&1 && return 0
  command -v netstat >/dev/null 2>&1 && return 0

  info "Installing port inspection dependency: ss"
  if command -v apt-get >/dev/null 2>&1; then
    apt-get update -y >/dev/null
    DEBIAN_FRONTEND=noninteractive apt-get install -y iproute2 >/dev/null
  elif command -v dnf >/dev/null 2>&1; then
    dnf install -y iproute >/dev/null
  elif command -v yum >/dev/null 2>&1; then
    yum install -y iproute >/dev/null
  elif command -v apk >/dev/null 2>&1; then
    apk add --no-cache iproute2 >/dev/null
  fi
}

install_base_deps() {
  local missing=()
  for c in curl jq openssl; do
    command -v "$c" >/dev/null 2>&1 || missing+=("$c")
  done

  if ((${#missing[@]} > 0)); then
    info "Installing base dependencies: ${missing[*]}"
    install_packages "${missing[@]}"
  fi

  install_port_detection_dep
}

sync_time_best_effort() {
  timedatectl set-ntp true >/dev/null 2>&1 || true
  if command -v ntpdate >/dev/null 2>&1; then
    # Enforce short timeouts to avoid slow startup when DNS or network access fails.
    if command -v timeout >/dev/null 2>&1; then
      timeout 5 ntpdate -u pool.ntp.org >/dev/null 2>&1 || true
    else
      ntpdate -u pool.ntp.org >/dev/null 2>&1 || true
    fi
  fi
}

SYSCTL_BACKUP_PATH="$CONFIG_DIR/sysctl_backup.conf"

# Track whether transport tuning modified Xray config so a restart can be justified.
XRAY_CONFIG_PATCHED=0

backup_sysctl_params_if_needed() {
  [[ -f "$SYSCTL_BACKUP_PATH" ]] && return 0
  install -d -m 755 "$CONFIG_DIR" 2>/dev/null || true

  local keys=(
    "net.core.default_qdisc"
    "net.ipv4.tcp_congestion_control"
    "net.ipv4.tcp_fastopen"
    "net.ipv4.tcp_slow_start_after_idle"
    "net.core.somaxconn"
    "net.ipv4.tcp_max_syn_backlog"
    "net.core.netdev_max_backlog"
    "net.ipv4.tcp_tw_reuse"
    "net.core.rmem_max"
    "net.core.wmem_max"
    "net.ipv4.tcp_rmem"
    "net.ipv4.tcp_wmem"
    "net.ipv4.tcp_mtu_probing"
    "net.ipv4.tcp_fin_timeout"
    "net.ipv4.tcp_notsent_lowat"
    "net.ipv4.ip_local_port_range"
    "net.ipv4.tcp_keepalive_time"
    "net.ipv4.tcp_keepalive_intvl"
    "net.ipv4.tcp_keepalive_probes"
    "net.core.optmem_max"
    "net.ipv4.tcp_timestamps"
    "net.ipv4.tcp_window_scaling"
    "net.netfilter.nf_conntrack_max"
    "fs.file-max"
  )

  info "Saving a snapshot of the current network kernel parameters..."
  {
    echo "# freedom.sh original kernel parameters before tuning"
    echo "# Backup generated:  $(date '+%Y-%m-%d %H:%M:%S')"
    for k in "${keys[@]}"; do
      local v
      v="$(sysctl -n "$k" 2>/dev/null || echo "")"
      if [[ -n "$v" ]]; then
        echo "${k} = ${v}"
      fi
    done
  } > "$SYSCTL_BACKUP_PATH"
  chmod 644 "$SYSCTL_BACKUP_PATH"
  ok "Original kernel parameters saved to:  $SYSCTL_BACKUP_PATH"
}

restore_linux_kernel_tuning() {
  echo
  warn "Warning: this removes freedom.sh tuning and restores the original network configuration!"
  read -r -p "Restore the original kernel parameters? [y/N]: " r_ans || r_ans="N"
  case "${r_ans:-N}" in
    y|Y|yes|YES)
      local had_limits=0
      if [[ -f /etc/systemd/system/xray.service.d/10-limits.conf ]]; then
        had_limits=1
      fi
      rm -f /etc/sysctl.d/99-xray-tuning.conf
      rm -rf /etc/systemd/system/xray.service.d/10-limits.conf
      systemctl daemon-reload >/dev/null 2>&1 || true
      if (( had_limits == 1 )) && systemctl is-active --quiet "$SERVICE_NAME" 2>/dev/null; then
        info "Restarting Xray to restore its previous file-descriptor limit..."
        restart_xray_preserving_traffic >/dev/null 2>&1 || warn "Xray restart failed; the file-descriptor limit will be restored on the next restart."
      fi

      if [[ -f "$SYSCTL_BACKUP_PATH" ]]; then
        info "Applying original kernel parameters from:  $SYSCTL_BACKUP_PATH"
        sysctl -p "$SYSCTL_BACKUP_PATH" >/dev/null 2>&1 || true
        ok "Kernel network parameters restored from the original snapshot."
      else
        info "Reloading default system kernel settings..."
        sysctl --system >/dev/null 2>&1 || true
        ok "Default system settings reloaded."
      fi
      ;;
    *) info "Restore cancelled." ;;
  esac
}

# Apply transport socket tuning to service inbounds and the direct outbound.
# Preserve any explicitly configured socket option during merges.
patch_xray_sockopt_tuning() {
  XRAY_CONFIG_PATCHED=0
  [[ -f "$CONFIG_PATH" ]] || return 0

  local updated
  updated="$(jq '
    def tune_sockopt:
      {
        tcpFastOpen: true,
        tcpCongestion: "bbr",
        tcpNoDelay: true,
        tcpUserTimeout: 10000,
        tcpKeepAliveIdle: 45,
        tcpKeepAliveInterval: 15
      } + (. // {});
    if (.inbounds | type) == "array" then
      .inbounds |= map(
        if (.tag == "api-internal" or (.streamSettings | type) != "object") then .
        else .streamSettings.sockopt = (.streamSettings.sockopt | tune_sockopt)
        end
      )
    else . end |
    if (.outbounds | type) == "array" then
      .outbounds |= map(
        if (.tag == "direct" and (.streamSettings | type) == "object") then
          .streamSettings.sockopt = (.streamSettings.sockopt | tune_sockopt)
        else . end
      )
    else . end
  ' "$CONFIG_PATH" 2>/dev/null)" || return 1

  [[ -n "$updated" ]] || return 1
  printf '%s\n' "$updated" | jq empty >/dev/null 2>&1 || return 1

  local before after
  before="$(jq -S . "$CONFIG_PATH" 2>/dev/null)" || return 1
  after="$(printf '%s\n' "$updated" | jq -S . 2>/dev/null)" || return 1
  [[ "$before" != "$after" ]] || return 0

  backup_existing_config
  commit_config "$updated" || return 1

  if ! "$BIN" run -test -c "$CONFIG_PATH" >/tmp/freedom-xray-test.log 2>&1; then
    if ! "$BIN" -test -config "$CONFIG_PATH" >>/tmp/freedom-xray-test.log 2>&1; then
      warn "Transport tuning configuration failed validation; rolling back."
      if [[ -n "${BACKUP_FILE:-}" && -f "$BACKUP_FILE" ]]; then
        cp -a "$BACKUP_FILE" "$CONFIG_PATH"
      fi
      return 1
    fi
  fi

  XRAY_CONFIG_PATCHED=1
  return 0
}

tune_linux_kernel_network() {
  backup_sysctl_params_if_needed
  info "Applying kernel and Xray transport tuning (BBR, TFO, buffers, keepalive)..."

  modprobe tcp_bbr >/dev/null 2>&1 || true
  install -d -m 755 /etc/sysctl.d 2>/dev/null || true
  cat <<'EOF' > /etc/sysctl.d/99-xray-tuning.conf
# Optional Xray-core network stack tuning for throughput and latency.
net.core.default_qdisc = fq
net.ipv4.tcp_congestion_control = bbr
net.ipv4.tcp_fastopen = 3
net.ipv4.tcp_slow_start_after_idle = 0
net.core.somaxconn = 65535
net.ipv4.tcp_max_syn_backlog = 16384
net.core.netdev_max_backlog = 16384
net.ipv4.tcp_tw_reuse = 1
net.core.rmem_max = 33554432
net.core.wmem_max = 33554432
net.ipv4.tcp_rmem = 4096 87380 33554432
net.ipv4.tcp_wmem = 4096 65536 33554432
net.ipv4.tcp_mtu_probing = 1
net.ipv4.tcp_fin_timeout = 15
net.ipv4.tcp_notsent_lowat = 131072
net.ipv4.ip_local_port_range = 1024 65535
net.ipv4.tcp_keepalive_time = 600
net.ipv4.tcp_keepalive_intvl = 30
net.ipv4.tcp_keepalive_probes = 3
net.core.optmem_max = 65536
net.ipv4.tcp_timestamps = 1
net.ipv4.tcp_window_scaling = 1
fs.file-max = 2097152
EOF

  # conntrack can be loaded by firewall services; raise limits only when present.
  if [[ -e /proc/sys/net/netfilter/nf_conntrack_max ]]; then
    echo "net.netfilter.nf_conntrack_max = 262144" >> /etc/sysctl.d/99-xray-tuning.conf
  fi

  sysctl -p /etc/sysctl.d/99-xray-tuning.conf >/dev/null 2>&1 || sysctl --system >/dev/null 2>&1 || true

  patch_xray_sockopt_tuning || warn "Xray transport tuning was not applied; the original config remains. Kernel settings are active."

  local need_restart=0
  (( XRAY_CONFIG_PATCHED == 1 )) && need_restart=1

  if [[ -f "/etc/systemd/system/xray.service" || -f "/lib/systemd/system/xray.service" ]]; then
    install -d -m 755 "/etc/systemd/system/xray.service.d" 2>/dev/null || true
    cat <<'EOF' > /etc/systemd/system/xray.service.d/10-limits.conf
[Service]
LimitNOFILE=1000000
LimitNPROC=512000
EOF
    systemctl daemon-reload >/dev/null 2>&1 || true

    # File-descriptor limits apply to newly started processes; an existing Xray process may require a restart.
    local xray_pid live_nofile
    xray_pid="$(systemctl show -p MainPID --value "$SERVICE_NAME" 2>/dev/null || echo 0)"
    if [[ "$xray_pid" =~ ^[0-9]+$ ]] && ((xray_pid > 0)) && [[ -r "/proc/${xray_pid}/limits" ]]; then
      live_nofile="$(awk '/Max open files/ {print $4}' "/proc/${xray_pid}/limits" 2>/dev/null || echo 0)"
      if [[ "$live_nofile" =~ ^[0-9]+$ ]] && ((live_nofile < 1000000)); then
        need_restart=1
      fi
    fi
  fi

  if (( need_restart == 1 )) && systemctl is-active --quiet "$SERVICE_NAME" 2>/dev/null; then
    info "Restarting Xray to apply transport and file-descriptor tuning..."
    if ! restart_xray_preserving_traffic >/dev/null 2>&1; then
      warn "Xray restart failed."
      if (( XRAY_CONFIG_PATCHED == 1 )) && [[ -n "${BACKUP_FILE:-}" && -f "$BACKUP_FILE" ]]; then
        warn "Rolling back transport configuration..."
        cp -a "$BACKUP_FILE" "$CONFIG_PATH"
        restart_xray_preserving_traffic >/dev/null 2>&1 || warn "Xray did not start after rollback; inspect logs."
      fi
    fi
  fi

  local cc tfo xray_st="Unchanged"
  cc="$(sysctl -n net.ipv4.tcp_congestion_control 2>/dev/null || echo 'unknown')"
  tfo="$(sysctl -n net.ipv4.tcp_fastopen 2>/dev/null || echo 'unknown')"
  (( XRAY_CONFIG_PATCHED == 1 )) && xray_st="Applied"
  ok "Tuning complete: congestion control [${cc}] | TCP Fast Open [${tfo}] | Xray transport [${xray_st}]"
}

benchmark_and_diagnose_system_tuning() {
  while true; do
    echo
    echo -e "  ${BOLD}System Network Tuning${NC}"
    echo "————————————————————————————————————————————————"

    local cur_cc cur_qdisc cur_tfo cur_idle cur_rmem cur_wmem cur_somax cur_syn cur_fmax cur_limit
    local cur_notsent cur_portrange
    cur_cc="$(sysctl -n net.ipv4.tcp_congestion_control 2>/dev/null || echo 'Unknown')"
    cur_qdisc="$(sysctl -n net.core.default_qdisc 2>/dev/null || echo 'Unknown')"
    cur_tfo="$(sysctl -n net.ipv4.tcp_fastopen 2>/dev/null || echo '0')"
    cur_idle="$(sysctl -n net.ipv4.tcp_slow_start_after_idle 2>/dev/null || echo '1')"
    cur_rmem="$(sysctl -n net.core.rmem_max 2>/dev/null || echo '0')"
    cur_wmem="$(sysctl -n net.core.wmem_max 2>/dev/null || echo '0')"
    cur_somax="$(sysctl -n net.core.somaxconn 2>/dev/null || echo '0')"
    cur_syn="$(sysctl -n net.ipv4.tcp_max_syn_backlog 2>/dev/null || echo '0')"
    cur_fmax="$(sysctl -n fs.file-max 2>/dev/null || echo '0')"
    cur_notsent="$(sysctl -n net.ipv4.tcp_notsent_lowat 2>/dev/null || echo '0')"
    cur_portrange="$(sysctl -n net.ipv4.ip_local_port_range 2>/dev/null || echo 'Unknown')"

    # Read the actual running Xray process limits rather than trusting unit configuration alone.
    cur_limit="$(ulimit -n 2>/dev/null || echo 'Unknown')"
    local diag_pid diag_nofile
    diag_pid="$(systemctl show -p MainPID --value "$SERVICE_NAME" 2>/dev/null || echo 0)"
    if [[ "$diag_pid" =~ ^[0-9]+$ ]] && ((diag_pid > 0)) && [[ -r "/proc/${diag_pid}/limits" ]]; then
      diag_nofile="$(awk '/Max open files/ {print $4}' "/proc/${diag_pid}/limits" 2>/dev/null || true)"
      if [[ -n "$diag_nofile" ]]; then
        cur_limit="${diag_nofile} (Running)"
      fi
    elif [[ -f "/etc/systemd/system/xray.service.d/10-limits.conf" ]]; then
      cur_limit="1000000 (Pending restart)"
    fi

    local rmem_mb="Approximately $((cur_rmem / 1024 / 1024))MB"
    ((cur_rmem < 1048576)) && rmem_mb="Approximately $((cur_rmem / 1024))KB"
    local wmem_mb="Approximately $((cur_wmem / 1024 / 1024))MB"
    ((cur_wmem < 1048576)) && wmem_mb="Approximately $((cur_wmem / 1024))KB"

    # Compact status summary.
    echo -e "  ${BOLD}Tuning Status${NC}"
    echo "————————————————————————————————————————————————"
    echo "  Congestion control: ${cur_cc} | Queue discipline: ${cur_qdisc} | TCP Fast Open: ${cur_tfo} | Idle restart slow-start: ${cur_idle}"
    echo "  Receive buffer: ${rmem_mb} | Send buffer: ${wmem_mb} | Unsent bytes limit: ${cur_notsent} | Local ports: ${cur_portrange}"
    echo "  Connection queue: ${cur_somax} / ${cur_syn} | File descriptors: ${cur_fmax} / ${cur_limit}"
    echo "————————————————————————————————————————————————"

    # System tuning diagnostics and rating.
    local score=0
    [[ "$cur_cc" == "bbr" ]] && ((score += 20))
    [[ "$cur_tfo" == "3" ]] && ((score += 15))
    [[ "$cur_idle" == "0" ]] && ((score += 15))
    ((cur_rmem >= 16777216)) && ((score += 20))
    ((cur_notsent >= 65536)) && ((score += 15))
    [[ "$cur_limit" == *"1000000"* || "$cur_limit" == *"unlimited"* ]] && ((score += 15))

    if ((score >= 85)); then
      ok "Score ${score}/100：Performance tuning ready"
    elif ((score >= 25)); then
      warn "Score ${score}/100：Basic (BBR only; no large buffers or bidirectional TFO)"
    else
      warn "Score ${score}/100：Not tuned"
    fi

    local bk_info="Not created"
    [[ -f "$SYSCTL_BACKUP_PATH" ]] && bk_info="${GREEN}Backed up (${SYSCTL_BACKUP_PATH})${NC}"
    echo -e "Original settings snapshot:  ${bk_info}"
    echo "————————————————————————————————————————————————"
    echo -e "  ${GREEN}1.${NC} Apply full-stack tuning (kernel + Xray transport)"
    echo -e "  ${GREEN}2.${NC} Restore original kernel settings (rollback)"
    echo -e "  ${GREEN}3.${NC} View pre-tuning backup"
    echo -e "  ${GREEN}0.${NC} Back to main menu"
    echo "————————————————————————————————————————————————"

    read -r -p "Choose [0-3, default 0]: " diag_op || diag_op="0"
    case "${diag_op:-0}" in
      1)
        tune_linux_kernel_network
        echo && read -rp "Tuning applied. Press Enter to refresh diagnostics..." _dummy
        ;;
      2)
        restore_linux_kernel_tuning
        echo && read -rp "Restore complete. Press Enter to refresh diagnostics..." _dummy
        ;;
      3)
        if [[ -f "$SYSCTL_BACKUP_PATH" ]]; then
          echo
          cat "$SYSCTL_BACKUP_PATH"
        else
          warn "No original-parameter backup yet; one will be created before tuning."
        fi
        echo && read -rp "Press Enter to continue..." _dummy
        ;;
      0) break ;;
      *) warn "Invalid choice." ;;
    esac
  done
}

valid_port() {
  [[ "$1" =~ ^[0-9]+$ ]] && ((1 <= 10#$1 && 10#$1 <= 65535))
}

sanitize_domain() {
  local v="$1"
  v="${v#http://}"
  v="${v#https://}"
  v="${v%%/*}"
  v="${v%%:*}"
  echo "$v"
}

sanitize_path() {
  local p="$1"
  [[ -z "$p" ]] && p="/"
  [[ "$p" != /* ]] && p="/$p"
  echo "$p"
}

# Create random XHTTP paths with UUIDv4-equivalent entropy and no fixed prefix.
random_xhttp_path() {
  printf '/%s\n' "$(openssl rand -hex 16)"
}

# Firewall port-management helpers for ufw, firewalld and iptables.
auto_open_firewall_port() {
  local port="$1"
  local proto="${2:-tcp}"
  local tag="${3:-xray}"

  [[ -z "$port" ]] && return 0
  local handled=0

  # 1. Detect an active ufw firewall.
  if command -v ufw >/dev/null 2>&1; then
    if ufw status 2>/dev/null | grep -qi "Status: active"; then
      ufw allow "${port}/${proto}" comment "Xray node ${tag}" >/dev/null 2>&1 && handled=1
      ok "Firewall (ufw): allowed port ${port}/${proto}"
    fi
  fi

  # 2. Detect an active firewalld firewall.
  if command -v firewall-cmd >/dev/null 2>&1; then
    if systemctl is-active --quiet firewalld 2>/dev/null; then
      firewall-cmd --zone=public --add-port="${port}/${proto}" --permanent >/dev/null 2>&1 && \
      firewall-cmd --reload >/dev/null 2>&1 && handled=1
      ok "Firewall (firewalld): allowed port ${port}/${proto}"
    fi
  fi

  # 3. Add an allow rule when iptables policy would otherwise block the listener.
  if command -v iptables >/dev/null 2>&1; then
    if iptables -L INPUT -n 2>/dev/null | grep -q "Chain INPUT (policy DROP)"; then
      iptables -C INPUT -p "${proto}" --dport "${port}" -j ACCEPT 2>/dev/null || \
      iptables -I INPUT -p "${proto}" --dport "${port}" -j ACCEPT 2>/dev/null && handled=1
      ok "Firewall (iptables): added allow rule for ${port}/${proto}"
    fi
  fi

  if (( handled == 0 )); then
    info "No active firewall detected; port ${port} requires no additional local allow rule."
  fi
}

auto_close_firewall_port() {
  local port="$1"
  local proto="${2:-tcp}"
  [[ -z "$port" ]] && return 0

  if command -v ufw >/dev/null 2>&1; then
    if ufw status 2>/dev/null | grep -qi "Status: active"; then
      ufw delete allow "${port}/${proto}" >/dev/null 2>&1 || true
    fi
  fi

  if command -v firewall-cmd >/dev/null 2>&1; then
    if systemctl is-active --quiet firewalld 2>/dev/null; then
      firewall-cmd --zone=public --remove-port="${port}/${proto}" --permanent >/dev/null 2>&1 || true
      firewall-cmd --reload >/dev/null 2>&1 || true
    fi
  fi

  # Remove managed iptables direct allow rules on cleanup.
  if command -v iptables >/dev/null 2>&1; then
    while iptables -C INPUT -p "${proto}" --dport "${port}" -j ACCEPT 2>/dev/null; do
      iptables -D INPUT -p "${proto}" --dport "${port}" -j ACCEPT 2>/dev/null || break
    done
  fi
}

field_after_colon() {
  local text="$1"
  local pattern="$2"
  printf '%s\n' "$text" | awk -v pat="$pattern" '
    BEGIN {
      n = split(pat, names, /\|/)
      for (i = 1; i <= n; i++) {
        want[i] = tolower(names[i])
        gsub(/^[[:space:]]+|[[:space:]]+$/, "", want[i])
      }
    }
    index($0, ":") {
      line = $0
      key = line
      sub(/:.*/, "", key)
      gsub(/^[[:space:]]+|[[:space:]]+$/, "", key)

      key_no_paren = key
      sub(/[[:space:]]*\(.*/, "", key_no_paren)
      gsub(/^[[:space:]]+|[[:space:]]+$/, "", key_no_paren)

      key_l = tolower(key)
      key_np_l = tolower(key_no_paren)

      val = line
      sub(/^[^:]*:[[:space:]]*/, "", val)
      gsub(/[[:space:]\r\n\"]/, "", val)

      for (i = 1; i <= n; i++) {
        if (key_l == want[i] || key_np_l == want[i] || index(key_l, want[i]) == 1) {
          if (val != "") {
            print val
            exit
          }
        }
      }
    }
  '
}

extract_vlessenc_value() {
  local text="$1"
  local alg="$2"
  local key="$3"
  printf '%s\n' "$text" | awk -v alg="$alg" -v key="$key" '
    index($0, alg) { f = 1 }
    f && $0 ~ "\\\"" key "\\\"[[:space:]]*:" {
      line = $0
      sub(".*\\\"" key "\\\"[[:space:]]*:[[:space:]]*\\\"", "", line)
      while (line !~ /\"/) {
        if ((getline more) <= 0) break
        line = line more
      }
      sub("\\\".*", "", line)
      gsub(/[[:space:]\r\n,]/, "", line)
      print line
      exit
    }
  '
}

url_encode() {
  jq -rn --arg v "$1" '$v|@uri'
}

url_host() {
  local h="$1"
  if [[ "$h" == *:* && "$h" != \[*\] ]]; then
    echo "[$h]"
  else
    echo "$h"
  fi
}

get_public_ip() {
  local ip=""
  for endpoint in \
    "https://api.ip.sb/ip" \
    "https://api.ipify.org" \
    "https://ifconfig.me" \
    "https://ipv4.icanhazip.com"; do
    ip="$(curl -fsS4 --max-time 6 "$endpoint" 2>/dev/null | tr -d '[:space:]' || true)"
    [[ -n "$ip" ]] && break
  done
  [[ -n "$ip" ]] || ip="$(hostname -I 2>/dev/null | awk '{print $1}' || true)"
  echo "$ip"
}

port_listener_summary() {
  local port="$1"
  local lines=""

  if command -v ss >/dev/null 2>&1; then
    lines="$(ss -H -ltnp "sport = :${port}" 2>/dev/null || true)"
    if [[ -n "$lines" ]]; then
      printf '%s\n' "$lines" | awk '
        {
          line = $0
          found = 0
          while (match(line, /"[^"]+",pid=[0-9]+/)) {
            s = substr(line, RSTART, RLENGTH)
            name = s
            sub(/^"/, "", name)
            sub(/",pid=.*/, "", name)
            pid = s
            sub(/.*pid=/, "", pid)
            print name " pid=" pid " listen=" $4
            line = substr(line, RSTART + RLENGTH)
            found = 1
          }
          if (!found) print "unknown " $0
        }
      ' | sort -u
    fi
    return 0
  fi

  if command -v lsof >/dev/null 2>&1; then
    lsof -nP -iTCP:"${port}" -sTCP:LISTEN 2>/dev/null | awk 'NR>1 {print $1 " pid=" $2 " " $9}' | sort -u
    return 0
  fi

  if command -v netstat >/dev/null 2>&1; then
    netstat -ltnp 2>/dev/null | awk -v p=":${port}" '$4 ~ p"$" {print $7 " " $4}' | sort -u
    return 0
  fi

  return 1
}

# Inspect port collisions at two independent layers:
# 1. Ports already declared in Xray inbounds.
# 2. Ports already bound by system processes.
check_port_available_strict() {
  local target_port="$1"
  local ignore_tag="${2:-}"

  # Check existing inbound listener configuration.
  if [[ -f "$CONFIG_PATH" ]]; then
    local matched_tag
    matched_tag="$(jq -r --argjson p "$target_port" --arg itag "$ignore_tag" '
      .inbounds[]? | select(.port == $p and (.tag != $itag)) | .tag
    ' "$CONFIG_PATH" 2>/dev/null | head -n1 || true)"

    if [[ -n "$matched_tag" ]]; then
      warn "Port ${target_port} is already used by Xray inbound [${matched_tag}]."
      return 1
    fi
  fi

  # Check listener ports owned by other system processes.
  local owners
  if ! owners="$(port_listener_summary "$target_port")"; then
    return 0
  fi

  if [[ -n "$owners" ]]; then
    local names
    names="$(printf '%s\n' "$owners" | awk '{print $1}' | sed 's#^.*/##' | sort -u)"
    local non_current
    non_current="$(printf '%s\n' "$names" | grep -vxF "$SERVICE_NAME" || true)"

    if [[ -n "$non_current" ]]; then
      warn "Port ${target_port} is occupied by another service/process:"
      printf '%s\n' "$owners" | sed 's/^/  - /'
      for conflict_name in $non_current; do
        if service_unit_exists "$conflict_name"; then
          read -r -p "Stop ${conflict_name}.service to free port ${target_port}? [y/N]: " ans || ans="N"
          case "${ans:-N}" in
            y|Y|yes|YES)
              systemctl stop "$conflict_name" || true
              sleep 1
              ;;
            *) return 1 ;;
          esac
        fi
      done
      # Verify that the target port has actually been released.
      local recheck
      recheck="$(port_listener_summary "$target_port" || true)"
      if [[ -n "$recheck" ]]; then
        local recheck_non
        recheck_non="$(printf '%s\n' "$recheck" | awk '{print $1}' | sed 's#^.*/##' | grep -vxF "$SERVICE_NAME" || true)"
        if [[ -n "$recheck_non" ]]; then
          warn "Port ${target_port} is still occupied; choose another port."
          return 1
        fi
      fi
    fi
  fi

  return 0
}

# Interactive port prompt stores the result in the global PORT_RESULT variable.
# Avoid command substitution around interactive prompts: it runs in a subshell
# and can break read input or mix diagnostics into the result.
PORT_RESULT=""

prompt_and_check_port() {
  local prompt_text="$1"
  local def_port="$2"
  local ignore_tag="${3:-}"
  local p=""

  while true; do
    read -r -p "${prompt_text} (default ${def_port}): " p || fatal "Input cancelled."
    p="${p:-$def_port}"
    if ! valid_port "$p"; then
      warn "Invalid port. Choose 1-65535."
      continue
    fi
    # Normalize numeric ports by removing leading zeros before passing to jq --argjson.
    p=$((10#$p))
    if check_port_available_strict "$p" "$ignore_tag"; then
      PORT_RESULT="$p"
      return 0
    fi
    warn "Port ${p} is unavailable; choose another port."
  done
}

detect_xui_environment() {
  local candidate=""
  if [[ -d "/usr/local/x-ui" || -f "/etc/x-ui/x-ui.db" || -f "/etc/systemd/system/x-ui.service" ]] || command -v x-ui >/dev/null 2>&1; then
    candidate="$(find /usr/local/x-ui/bin /usr/local/x-ui -maxdepth 2 -type f -name "xray*" 2>/dev/null | grep -E 'xray(-linux-[a-z0-9]+)?$' | head -n1 || true)"
  fi
  echo "$candidate"
}

ensure_xray_systemd_service() {
  if ! service_unit_exists "$SERVICE_NAME"; then
    info "Creating a standard systemd service for standalone Xray..."
    local svc_bin="${BIN:-/usr/local/bin/xray}"
    cat <<EOF > /etc/systemd/system/xray.service
[Unit]
Description=Xray Service
Documentation=https://github.com/xtls
After=network.target nss-lookup.target

[Service]
User=root
CapabilityBoundingSet=CAP_NET_ADMIN CAP_NET_BIND_SERVICE
AmbientCapabilities=CAP_NET_ADMIN CAP_NET_BIND_SERVICE
NoNewPrivileges=true
ExecStart=${svc_bin} run -config ${CONFIG_PATH}
Restart=on-failure
RestartPreventExitStatus=23
LimitNPROC=512000
LimitNOFILE=1000000

[Install]
WantedBy=multi-user.target
EOF
    systemctl daemon-reload >/dev/null 2>&1 || warn "systemd daemon-reload failed; run systemctl daemon-reload manually."
    systemctl enable xray >/dev/null 2>&1 || true
    ok "Created and enabled standard systemd service: /etc/systemd/system/xray.service"
  fi
}

# Download and run the official Xray installer only after validating that the download succeeded.
install_xray_via_official_script() {
  local installer
  installer="$(curl -fsSL --connect-timeout 8 https://github.com/XTLS/Xray-install/raw/main/install-release.sh 2>/dev/null || true)"
  if [[ -z "$installer" ]]; then
    warn "Unable to download the official Xray installer. Check DNS and network connectivity."
    return 1
  fi
  bash -c "$installer" @ install
}

ensure_xray_installed() {
  if ! command -v xray >/dev/null 2>&1; then
    local xui_xray
    xui_xray="$(detect_xui_environment)"

    if [[ -n "$xui_xray" && -x "$xui_xray" ]]; then
      echo
      info "Detected an existing 3x-ui panel. Its private Xray binary is located at:"
      echo -e "  -> ${CYAN}${BOLD}${xui_xray}${NC}"
      warn "3x-ui keeps its Xray binary inside the panel directory and does not expose it on the system PATH."
      echo
      echo "Choose an Xray installation method:"
      echo "1) Install the official standalone Xray-core system service (recommended; independent of 3x-ui) [default]"
      echo "2) Reuse the Xray binary bundled with 3x-ui (symlink to /usr/local/bin/xray; no download)"
      echo "0) Exit"
      read -r -p "Choose [1/2, default 1]: " xui_choice || xui_choice="1"
      case "${xui_choice:-1}" in
        2)
          ln -sf "$xui_xray" /usr/local/bin/xray
          ok "Linked 3x-ui's Xray binary to /usr/local/bin/xray."
          ensure_xray_systemd_service
          ;;
        0) fatal "Exited." ;;
        *)
          info "Running the official Xray installer/updater..."
          if ! install_xray_via_official_script; then
            fatal "Xray-core installation failed. Check network access and try again."
          fi
          command -v xray >/dev/null 2>&1 || fatal "Xray command was not found after installation. Check permissions and PATH."
          systemctl enable xray >/dev/null 2>&1 || true
          ok "Xray-core installed and enabled successfully."
          ;;
      esac
    else
      warn "No xray executable found in the current PATH."
      read -r -p "Install the latest Xray-core using the official XTLS installer? [Y/n]: " ans || ans="Y"
      case "${ans:-Y}" in
        n|N|no|NO) fatal "Installation cancelled; exiting." ;;
        *)
          info "Running the official Xray installer/updater..."
          if ! install_xray_via_official_script; then
            fatal "Xray-core installation failed. Check network access and try again."
          fi
          command -v xray >/dev/null 2>&1 || fatal "Xray command was not found after installation. Check permissions and PATH."
          systemctl enable xray >/dev/null 2>&1 || true
          ok "Xray-core installed and enabled successfully."
          ;;
      esac
    fi
  fi
  BIN="$(command -v xray)"
  CORE_VERSION_CACHE=""
  ensure_xray_systemd_service
  local ver
  ver="$(get_xray_core_version)"
  ok "Found Xray-core: ${BIN} (${ver})"
}

CORE_VERSION_CACHE=""

get_xray_core_version() {
  if [[ -n "$CORE_VERSION_CACHE" ]]; then
    echo "$CORE_VERSION_CACHE"
    return 0
  fi
  if [[ -n "${BIN:-}" ]] && [[ -x "$BIN" ]]; then
    local v
    v="$("$BIN" version 2>/dev/null | awk 'NR==1 {print $1,$2}')"
    if [[ -n "$v" ]]; then
      CORE_VERSION_CACHE="$v"
      echo "$v"
      return 0
    fi
  fi
  echo "Xray unknown"
}

# Shared with the Go Bot via flock(2): covers each entire read/modify/write/restart action.
# The dashboard does not hold the lock while waiting for a menu choice.
locked_config_operation() (
  install -d -m 755 "$CONFIG_DIR" || exit 1
  if ! command -v flock >/dev/null 2>&1; then
    warn "util-linux flock is missing; refusing an unlocked configuration change."
    exit 1
  fi
  exec 9>"$CONFIG_DIR/.freedom-config.lock" || exit 1
  if ! flock -w 20 9; then
    warn "Another CLI or Bot operation holds the Xray config lock. Timed out after 20 seconds."
    exit 1
  fi
  "$@"
)
backup_existing_config() {
  install -d -m 755 "$CONFIG_DIR"
  BACKUP_FILE=""
  if [[ -f "$CONFIG_PATH" ]]; then
    BACKUP_FILE="${CONFIG_PATH}.bak.$(date +%Y%m%d%H%M%S)"
    cp -a "$CONFIG_PATH" "$BACKUP_FILE"
    info "Created rollback snapshot: $BACKUP_FILE"
    ls -1t "${CONFIG_PATH}".bak.* 2>/dev/null | tail -n +8 | xargs -r rm -f || true
  fi
}

# Atomically replace config only after validating generated JSON.
# Never redirect a failed jq result directly into the live configuration file.
# Match the real Xray systemd identity before tightening config permissions.
# A root:root 0640 config otherwise breaks services running as nobody.
set_xray_config_permissions() {
  local target="${1:-$CONFIG_PATH}"
  [[ -f "$target" ]] || return 0
  local service_user service_group
  service_user="$(systemctl show "$SERVICE_NAME" -p User --value 2>/dev/null || true)"
  service_group="$(systemctl show "$SERVICE_NAME" -p Group --value 2>/dev/null || true)"
  if [[ -n "$service_user" && "$service_user" != "root" ]]; then
    [[ -n "$service_group" ]] || service_group="$(id -gn "$service_user" 2>/dev/null || true)"
    if [[ -z "$service_group" ]]; then
      warn "Cannot determine the group of Xray service user ${service_user}; refusing to write potentially unreadable secrets."
      return 1
    fi
    chown "root:${service_group}" "$target" || return 1
  else
    chown root:root "$target" || return 1
  fi
  chmod 640 "$target" || return 1
}
commit_config() {
  local new_json="$1"
  if [[ -z "$new_json" ]]; then
    warn "Generated config is empty. No changes were made."
    return 1
  fi
  if ! printf '%s\n' "$new_json" | jq empty >/dev/null 2>&1; then
    warn "Generated config is not valid JSON. No changes were made."
    return 1
  fi
  local tmp_file="${CONFIG_PATH}.tmp.$$"
  if ! printf '%s\n' "$new_json" > "$tmp_file"; then
    rm -f "$tmp_file"
    warn "Unable to write temporary config. The original remains unchanged."
    return 1
  fi
  chmod 640 "$tmp_file" 2>/dev/null || true
  if ! set_xray_config_permissions "$tmp_file"; then
    rm -f "$tmp_file"
    warn "Unable to set config file permissions; no changes committed."
    return 1
  fi
  if ! mv -f "$tmp_file" "$CONFIG_PATH"; then
    rm -f "$tmp_file"
    warn "Unable to replace config. The original remains unchanged."
    return 1
  fi
  return 0
}

validate_and_restart_service() {
  info "Checking Xray configuration syntax and core validation"
  jq empty "$CONFIG_PATH" || fatal_with_rollback "Invalid JSON syntax: $CONFIG_PATH"

  if ! "$BIN" run -test -c "$CONFIG_PATH" >/tmp/freedom-xray-test.log 2>&1; then
    if ! "$BIN" -test -config "$CONFIG_PATH" >/tmp/freedom-xray-test.log 2>&1; then
      cat /tmp/freedom-xray-test.log >&2
      fatal_with_rollback "Xray configuration validation failed; aborted with rollback option."
    fi
  fi
  ok "Xray configuration validation passed."

  info "Reloading/restarting service: $SERVICE_NAME"
  if ! service_unit_exists "$SERVICE_NAME"; then
    warn "systemd service ${SERVICE_NAME}.service was not found; only the config was updated: $CONFIG_PATH"
    return 0
  fi

  if ! restart_xray_preserving_traffic; then
    print_service_diagnostics "$SERVICE_NAME"
    fatal_with_rollback "${SERVICE_NAME} Restart command failed"
  fi

  sleep 2
  if ! systemctl is-active --quiet "$SERVICE_NAME"; then
    print_service_diagnostics "$SERVICE_NAME"
    fatal_with_rollback "${SERVICE_NAME} Not active after startup"
  fi
  ok "${SERVICE_NAME}.service is running normally."
}

# Helpers for generating keys and certificates.
get_or_create_shared_uuid() {
  local uid=""
  if [[ -f "$CONFIG_PATH" ]]; then
    uid="$(jq -r '.inbounds[]? | select(.tag != "api-internal") | .settings.clients[0].id // empty' "$CONFIG_PATH" 2>/dev/null | head -n1 || true)"
  fi
  if [[ -z "$uid" ]]; then
    uid="$("$BIN" uuid 2>/dev/null || cat /proc/sys/kernel/random/uuid | tr -d '[:space:]')"
  fi
  echo "$uid"
}

get_or_create_shared_email() {
  local em=""
  if [[ -f "$CONFIG_PATH" ]]; then
    em="$(jq -r '.inbounds[]? | select(.tag != "api-internal") | .settings.clients[0].email // empty' "$CONFIG_PATH" 2>/dev/null | head -n1 || true)"
  fi
  if [[ -z "$em" ]]; then
    em="$DEFAULT_ADMIN_EMAIL"
  fi
  echo "$em"
}

# REALITY X25519 key generation writes to REALITY_PRIV and REALITY_PUB.
REALITY_PRIV=""
REALITY_PUB=""

get_or_create_shared_reality_keys() {
  local priv="" pub=""
  if [[ -f "$CONFIG_PATH" ]]; then
    priv="$(jq -r '.inbounds[]? | select(.streamSettings.security == "reality") | .streamSettings.realitySettings.privateKey // empty' "$CONFIG_PATH" 2>/dev/null | head -n1 || true)"
  fi

  if [[ -z "$priv" ]]; then
    local out
    out="$("$BIN" x25519 2>&1)" || fatal "xray x25519 command failed"
    priv="$(field_after_colon "$out" "PrivateKey|Private key|Private")"
    pub="$(field_after_colon "$out" "Password|PublicKey|Public key|Public")"
  else
    # Derive the public key from an existing REALITY private key to preserve identity.
    local out
    out="$("$BIN" x25519 -i "$priv" 2>&1 || true)"
    pub="$(field_after_colon "$out" "Password|PublicKey|Public key|Public")"
    if [[ -z "$pub" ]]; then
      local tmpout
      tmpout="$("$BIN" x25519 2>&1)" || fatal "xray x25519 command failed"
      priv="$(field_after_colon "$tmpout" "PrivateKey|Private key|Private")"
      pub="$(field_after_colon "$tmpout" "Password|PublicKey|Public key|Public")"
    fi
  fi

  [[ -n "$priv" && -n "$pub" ]] || fatal "Unable to generate or parse REALITY X25519 keys. Check the Xray-core version."

  REALITY_PRIV="$priv"
  REALITY_PUB="$pub"
  return 0
}

# Derive the REALITY public key for sharing links; return empty on failure.
derive_reality_public_key() {
  local priv="$1"
  [[ -n "$priv" ]] || { echo ""; return 0; }
  local out
  out="$("$BIN" x25519 -i "$priv" 2>&1 || true)"
  field_after_colon "$out" "Password|PublicKey|Public key|Public"
}

# Check whether the installed Xray core supports optional subcommands.
xray_supports_subcommand() {
  local sub="$1"
  [[ -n "${BIN:-}" && -x "$BIN" ]] || return 1
  "$BIN" help 2>&1 | grep -qw "$sub"
}

# Generate a paired VLESS ML-KEM-768 server/client encryption configuration.
# The result is returned in VLESSENC_DECRYPTION and VLESSENC_ENCRYPTION.
VLESSENC_DECRYPTION=""
VLESSENC_ENCRYPTION=""

generate_vlessenc_mlkem_pair() {
  VLESSENC_DECRYPTION=""
  VLESSENC_ENCRYPTION=""
  local out
  out="$("$BIN" vlessenc 2>&1 || true)"
  if [[ -z "$out" ]]; then
    return 1
  fi
  VLESSENC_DECRYPTION="$(extract_vlessenc_value "$out" "ML-KEM-768" "decryption")"
  VLESSENC_ENCRYPTION="$(extract_vlessenc_value "$out" "ML-KEM-768" "encryption")"
  [[ -n "$VLESSENC_DECRYPTION" && -n "$VLESSENC_ENCRYPTION" ]] || return 1
  return 0
}

# Generate an ML-DSA-65 seed and verification value.
# The result is returned in MLDSA_SEED and MLDSA_VERIFY.
MLDSA_SEED=""
MLDSA_VERIFY=""

generate_mldsa65_pair() {
  MLDSA_SEED=""
  MLDSA_VERIFY=""
  local out
  out="$("$BIN" mldsa65 2>&1 || true)"
  MLDSA_SEED="$(printf '%s\n' "$out" | awk -F': *' '/^Seed:/ {v=$2} END {print v}')"
  MLDSA_VERIFY="$(printf '%s\n' "$out" | awk -F': *' '/^Verify:/ {v=$2} END {print v}')"
  MLDSA_SEED="${MLDSA_SEED//[[:space:]]/}"
  MLDSA_VERIFY="${MLDSA_VERIFY//[[:space:]]/}"
  [[ -n "$MLDSA_SEED" && -n "$MLDSA_VERIFY" ]] || return 1
  return 0
}

# Derive the shared-link verification value (pqv) from an ML-DSA seed.
derive_mldsa65_verify() {
  local seed="$1"
  [[ -n "$seed" ]] || { echo ""; return 0; }
  local out verify
  out="$("$BIN" mldsa65 -i "$seed" 2>&1 || true)"
  verify="$(printf '%s\n' "$out" | awk -F': *' '/^Verify:/ {v=$2} END {print v}' | tr -d '[:space:]')"
  if [[ -z "$verify" ]]; then
    warn "Unable to derive ML-DSA Verify from mldsa65Seed; exported links will lack pqv."
  fi
  printf '%s\n' "$verify"
}

# Generate a self-signed ECC certificate, with an RSA fallback on failure.
generate_self_signed_cert() {
  local domain="$1"
  local cert_file="${CERTS_DIR}/${domain}.crt"
  local key_file="${CERTS_DIR}/${domain}.key"
  if openssl ecparam -name prime256v1 -genkey -noout -out "$key_file" 2>/dev/null && \
     openssl req -new -x509 -key "$key_file" -out "$cert_file" -days 3650 -subj "/CN=${domain}" 2>/dev/null; then
    :
  else
    openssl req -x509 -nodes -newkey rsa:2048 -keyout "$key_file" -out "$cert_file" -days 3650 -subj "/CN=${domain}" 2>/dev/null || return 1
  fi
  chmod 600 "$key_file" 2>/dev/null || true
  chmod 644 "$cert_file" 2>/dev/null || true
  return 0
}

# TLS certificate selection stores file paths in TLS_CERT_FILE and TLS_KEY_FILE.
TLS_CERT_FILE=""
TLS_KEY_FILE=""

setup_tls_certificate() {
  local domain="$1"
  install -d -m 755 "$CERTS_DIR"
  TLS_CERT_FILE=""
  TLS_KEY_FILE=""

  echo
  echo -e "${BOLD}Choose a TLS certificate source for domain: ${domain}）：${NC}"
  echo "1) Generate a self-signed ECC certificate (fast; Cloudflare Full mode; no HTTP-01 validation)"
  echo "2) Use existing certificate and private key files"
  echo "3) Issue a free certificate using acme.sh (requires port 80 and DNS pointing to this VPS)"

  local cert_file key_file
  while true; do
    read -r -p "Certificate option [1/2/3, default 1]: " cm_sel || fatal "Input cancelled."
    case "${cm_sel:-1}" in
      1)
        info "Generating a self-signed ECC certificate for ${domain} (prime256v1, 10-year validity)..."
        generate_self_signed_cert "$domain" || fatal "Failed to generate a self-signed certificate"
        TLS_CERT_FILE="${CERTS_DIR}/${domain}.crt"
        TLS_KEY_FILE="${CERTS_DIR}/${domain}.key"
        ok "Generated self-signed certificate: ${TLS_CERT_FILE}"
        return 0
        ;;
      2)
        while true; do
          read -r -p "Enter absolute path to fullchain certificate (.crt/.pem): " cert_file || fatal "Input cancelled."
          [[ -f "$cert_file" ]] && break
          warn "File does not exist: $cert_file. Try again."
        done
        while true; do
          read -r -p "Enter absolute path to private key (.key): " key_file || fatal "Input cancelled."
          [[ -f "$key_file" ]] && break
          warn "File does not exist: $key_file. Try again."
        done
        TLS_CERT_FILE="$cert_file"
        TLS_KEY_FILE="$key_file"
        return 0
        ;;
      3)
        local acme_ok=0
        if ! port_listener_summary "80" 2>/dev/null | grep -q .; then
          if [[ ! -x "$HOME/.acme.sh/acme.sh" ]]; then
            info "Installing acme.sh..."
            curl -fsSL https://get.acme.sh | sh -s email="admin@${domain}" >/dev/null 2>&1 || true
          fi
          local acme_cmd="$HOME/.acme.sh/acme.sh"
          if [[ -x "$acme_cmd" ]]; then
            cert_file="${CERTS_DIR}/${domain}.crt"
            key_file="${CERTS_DIR}/${domain}.key"
            if "$acme_cmd" --issue -d "$domain" --standalone --keylength ec-256 --force >/dev/null 2>&1 && \
               "$acme_cmd" --install-cert -d "$domain" --ecc \
                 --fullchain-file "$cert_file" \
                 --key-file "$key_file" >/dev/null 2>&1; then
              acme_ok=1
            fi
          fi
        fi
        if (( acme_ok == 1 )); then
          chmod 600 "${CERTS_DIR}/${domain}.key" 2>/dev/null || true
          chmod 644 "${CERTS_DIR}/${domain}.crt" 2>/dev/null || true
          TLS_CERT_FILE="${CERTS_DIR}/${domain}.crt"
          TLS_KEY_FILE="${CERTS_DIR}/${domain}.key"
          ok "acme.sh successfully issued a certificate."
          return 0
        fi
        warn "Port 80 is unavailable or ACME failed. Falling back to a self-signed ECC certificate for Cloudflare Full mode."
        generate_self_signed_cert "$domain" || fatal "Failed to generate a self-signed certificate"
        TLS_CERT_FILE="${CERTS_DIR}/${domain}.crt"
        TLS_KEY_FILE="${CERTS_DIR}/${domain}.key"
        return 0
        ;;
      *) warn "Choose 1, 2 or 3." ;;
    esac
  done
}

ensure_stats_api_scaffold() {
  [[ -f "$CONFIG_PATH" ]] || return 0

  local need_update=0
  if ! jq -e '.stats' "$CONFIG_PATH" >/dev/null 2>&1; then need_update=1; fi
  if ! jq -e '.api' "$CONFIG_PATH" >/dev/null 2>&1; then need_update=1; fi
  if ! jq -e '(.api.services // []) | index("StatsService")' "$CONFIG_PATH" >/dev/null 2>&1; then need_update=1; fi
  if ! jq -e '.inbounds[]? | select(.tag == "api-internal")' "$CONFIG_PATH" >/dev/null 2>&1; then need_update=1; fi
  if ! jq -e '.outbounds[]? | select(.tag == "api")' "$CONFIG_PATH" >/dev/null 2>&1; then need_update=1; fi
  if ! jq -e '.policy.system.statsInboundUplink == true' "$CONFIG_PATH" >/dev/null 2>&1; then need_update=1; fi
  if ! jq -e '.policy.system.statsInboundDownlink == true' "$CONFIG_PATH" >/dev/null 2>&1; then need_update=1; fi
  if ! jq -e '.policy.levels."0".statsUserUplink == true' "$CONFIG_PATH" >/dev/null 2>&1; then need_update=1; fi
  if ! jq -e '.policy.levels."0".statsUserDownlink == true' "$CONFIG_PATH" >/dev/null 2>&1; then need_update=1; fi
  if ! jq -e '.policy.levels."0".statsUserOnline == true' "$CONFIG_PATH" >/dev/null 2>&1; then need_update=1; fi
  # Every client level used by an inbound needs the per-user counters.
  # Xray reports user traffic/online state only for levels with these flags,
  # so a custom client level would otherwise stay invisible to the Bot.
  if ! jq -e '
    ([0] + [.inbounds[]? | .settings.clients[]? | .level? // 0] | unique | map(tostring)) as $levels
    | [ $levels[] as $l
        | select((.policy.levels[$l].statsUserUplink == true
              and .policy.levels[$l].statsUserDownlink == true
              and .policy.levels[$l].statsUserOnline == true) | not) ]
    | length == 0
  ' "$CONFIG_PATH" >/dev/null 2>&1; then need_update=1; fi

  if (( need_update == 1 )); then
    info "Xray config lacks API/traffic statistics. Updating the config scaffold..."
    local updated
    updated="$(jq '
      .stats = (.stats // {}) |
      .api |= ((. // {}) + {
        tag: (.tag // "api"),
        services: (((.services // []) + ["StatsService"]) | unique)
      }) |
      .policy = (.policy // {}) |
      .policy.system = (.policy.system // {}) |
      .policy.system.statsInboundUplink = true |
      .policy.system.statsInboundDownlink = true |
      .policy.levels = (.policy.levels // {}) |
      .policy.levels."0" = ((.policy.levels."0" // {}) + {
        handshake: 4,
        connIdle: 300,
        uplinkOnly: 2,
        downlinkOnly: 5,
        bufferSize: 512,
        statsUserUplink: true,
        statsUserDownlink: true,
        statsUserOnline: true
      }) |
      ([0] + [.inbounds[]? | .settings.clients[]? | .level? // 0] | unique | map(tostring)) as $levels |
      reduce $levels[] as $level (.;
        .policy.levels[$level] = ((.policy.levels[$level] // {}) + {
          statsUserUplink: true,
          statsUserDownlink: true,
          statsUserOnline: true
        })
      ) |
      (if ([.inbounds[]? | select(.tag == "api-internal")] | length) == 0 then
        .inbounds = [{
          tag: "api-internal",
          listen: "127.0.0.1",
          port: 10085,
          protocol: "dokodemo-door",
          settings: { address: "127.0.0.1" }
        }] + (.inbounds // [])
      else . end) |
      (if ([.outbounds[]? | select(.tag == "api")] | length) == 0 then
        .outbounds = (.outbounds // []) + [{
          protocol: "freedom",
          tag: "api"
        }]
      else . end) |
      (if ([.routing.rules[]? | select(.outboundTag == "api")] | length) == 0 then
        .routing.rules = [{
          type: "field",
          inboundTag: ["api-internal"],
          outboundTag: "api"
        }] + (.routing.rules // [])
      else . end)
    ' "$CONFIG_PATH" 2>/dev/null || true)"
    if [[ -z "$updated" ]]; then
      warn "Unable to generate updated Stats API JSON; original config preserved."
      return 1
    fi
    commit_config "$updated" || return 1
    ok "Xray API and traffic statistics scaffold installed."
    if service_unit_exists "$SERVICE_NAME" && systemctl is-active --quiet "$SERVICE_NAME"; then
      restart_xray_preserving_traffic >/dev/null 2>&1 || true
    fi
  fi
}

# Ensure the base config contains log, stats, api, policy, outbounds and routing.
ensure_config_scaffold() {
  install -d -m 755 "$CONFIG_DIR"
  if [[ -f "$CONFIG_PATH" ]]; then
    # Reject malformed JSON or configurations missing the inbounds array.
    if jq -e '.inbounds' "$CONFIG_PATH" >/dev/null 2>&1; then
      ensure_stats_api_scaffold || warn "Stats API initialization failed; the traffic dashboard may be unavailable. Original config preserved."
      return 0
    fi
    warn "Existing $CONFIG_PATH is not a valid Xray config; backing it up before rebuilding."
    backup_existing_config
  fi

  info "Initializing base Xray-core configuration scaffold..."
  local rules_json='[
    {"type":"field","domain":["geosite:geolocation-!cn"],"outboundTag":"direct"},
    {"type":"field","domain":["geosite:cn"],"outboundTag":"block"},
    {"type":"field","ip":["geoip:private","geoip:cn"],"outboundTag":"block"}
  ]'

  if [[ -f "/usr/local/share/xray/1stream.dat" ]]; then
    rules_json="$(jq -n --argjson r "$rules_json" '[{"type":"field","domain":["ext:1stream.dat:ai"],"outboundTag":"direct"}] + $r')"
  fi

  jq -n \
    --argjson rules "$rules_json" \
    '
    {
      log: { loglevel: "warning" },
      stats: {},
      api: {
        tag: "api",
        services: [ "StatsService" ]
      },
      policy: {
        levels: {
          "0": {
            handshake: 4,
            connIdle: 300,
            uplinkOnly: 2,
            downlinkOnly: 5,
            bufferSize: 512,
            statsUserUplink: true,
            statsUserDownlink: true,
            statsUserOnline: true
          }
        },
        system: {
          statsInboundUplink: true,
          statsInboundDownlink: true
        }
      },
      inbounds: [
        {
          tag: "api-internal",
          listen: "127.0.0.1",
          port: 10085,
          protocol: "dokodemo-door",
          settings: { address: "127.0.0.1" }
        }
      ],
      outbounds: [
        {
          protocol: "freedom",
          tag: "direct",
          settings: { domainStrategy: "AsIs" },
          streamSettings: {
            sockopt: {
              tcpFastOpen: true,
              tcpCongestion: "bbr",
              tcpNoDelay: true,
              tcpUserTimeout: 10000,
              tcpKeepAliveIdle: 45,
              tcpKeepAliveInterval: 15
            }
          }
        },
        {
          protocol: "blackhole",
          tag: "block"
        },
        {
          protocol: "freedom",
          tag: "api"
        }
      ],
      routing: {
        domainStrategy: "AsIs",
        rules: ([
          {
            type: "field",
            inboundTag: ["api-internal"],
            outboundTag: "api"
          }
        ] + $rules)
      }
    }' > "${CONFIG_PATH}.tmp.$$"
  if ! jq empty "${CONFIG_PATH}.tmp.$$" >/dev/null 2>&1; then
    rm -f "${CONFIG_PATH}.tmp.$$"
    fatal "Failed to generate valid scaffold JSON using jq."
  fi
  mv -f "${CONFIG_PATH}.tmp.$$" "$CONFIG_PATH"
  set_xray_config_permissions "$CONFIG_PATH" || fatal "Unable to initialize config file permissions"
  ok "Base configuration scaffold is ready."
}

# Build the inbound JSON configuration objects.
build_inbound_tcp_reality() {
  local in_port="$1"
  local in_tag="$2"
  local in_uuid="$3"
  local in_sni="$4"
  local in_priv="$5"
  local in_sid="$6"
  local in_vdec="${7:-none}"
  local in_mldsa_seed="${8:-}"
  local in_email="${9:-$DEFAULT_ADMIN_EMAIL}"

  jq -n \
    --argjson port "$in_port" \
    --arg tag "$in_tag" \
    --arg uuid "$in_uuid" \
    --arg email "$in_email" \
    --arg sni "$in_sni" \
    --arg private_key "$in_priv" \
    --arg short_id "$in_sid" \
    --arg vdec "$in_vdec" \
    --arg mldsa_seed "$in_mldsa_seed" \
    '{
      tag: $tag,
      port: $port,
      protocol: "vless",
      settings: {
        clients: [ { id: $uuid, email: $email, flow: "xtls-rprx-vision" } ],
        decryption: $vdec
      },
      streamSettings: {
        network: "tcp",
        security: "reality",
        realitySettings: (
          {
            show: false,
            dest: ($sni + ":443"),
            target: ($sni + ":443"),
            xver: 0,
            serverNames: [ $sni ],
            privateKey: $private_key,
            shortIds: [ $short_id ],
            maxTimeDiff: 60000
          }
          + (if $mldsa_seed != "" then { mldsa65Seed: $mldsa_seed } else {} end)
        ),
        sockopt: {
          tcpFastOpen: true,
          tcpCongestion: "bbr",
          tcpNoDelay: true,
          tcpUserTimeout: 10000,
          tcpKeepAliveIdle: 45,
          tcpKeepAliveInterval: 15
        }
      },
      sniffing: {
        enabled: true,
        destOverride: ["http", "tls", "quic"],
        routeOnly: true
      }
    }'
}

build_inbound_xhttp_reality() {
  local in_port="$1"
  local in_tag="$2"
  local in_uuid="$3"
  local in_sni="$4"
  local in_priv="$5"
  local in_sid="$6"
  local in_path="$7"
  local in_mode="$8"
  local in_mldsa_seed="${9:-}"
  local in_vdec="${10:-none}"
  local in_flow="${11:-}"
  local in_email="${12:-$DEFAULT_ADMIN_EMAIL}"

  jq -n \
    --argjson port "$in_port" \
    --arg tag "$in_tag" \
    --arg uuid "$in_uuid" \
    --arg email "$in_email" \
    --arg sni "$in_sni" \
    --arg private_key "$in_priv" \
    --arg short_id "$in_sid" \
    --arg path "$in_path" \
    --arg mode "$in_mode" \
    --arg mldsa_seed "$in_mldsa_seed" \
    --arg vdec "$in_vdec" \
    --arg flow "$in_flow" \
    '{
      tag: $tag,
      port: $port,
      protocol: "vless",
      settings: {
        clients: [
          ( { id: $uuid, email: $email } + (if $flow != "" then { flow: $flow } else {} end) )
        ],
        decryption: $vdec
      },
      streamSettings: {
        network: "xhttp",
        security: "reality",
        realitySettings: (
          {
            show: false,
            dest: ($sni + ":443"),
            target: ($sni + ":443"),
            xver: 0,
            serverNames: [ $sni ],
            privateKey: $private_key,
            shortIds: [ $short_id ],
            maxTimeDiff: 60000
          }
          + (if $mldsa_seed != "" then { mldsa65Seed: $mldsa_seed } else {} end)
        ),
        xhttpSettings: {
          path: $path,
          mode: $mode,
          xPaddingBytes: "100-1000"
        },
        sockopt: {
          tcpFastOpen: true,
          tcpCongestion: "bbr",
          tcpNoDelay: true,
          tcpUserTimeout: 10000,
          tcpKeepAliveIdle: 45,
          tcpKeepAliveInterval: 15
        }
      },
      sniffing: {
        enabled: true,
        destOverride: ["http", "tls", "quic"],
        routeOnly: true
      }
    }'
}

build_inbound_xhttp_tls() {
  local in_port="$1"
  local in_tag="$2"
  local in_uuid="$3"
  local in_cert="$4"
  local in_key="$5"
  local in_path="$6"
  local in_mode="$7"
  local in_vdec="${8:-none}"
  local in_flow="${9:-}"
  local in_email="${10:-$DEFAULT_ADMIN_EMAIL}"

  jq -n \
    --argjson port "$in_port" \
    --arg tag "$in_tag" \
    --arg uuid "$in_uuid" \
    --arg email "$in_email" \
    --arg cert "$in_cert" \
    --arg key "$in_key" \
    --arg path "$in_path" \
    --arg mode "$in_mode" \
    --arg vdec "$in_vdec" \
    --arg flow "$in_flow" \
    '{
      tag: $tag,
      port: $port,
      protocol: "vless",
      settings: {
        clients: [
          ( { id: $uuid, email: $email } + (if $flow != "" then { flow: $flow } else {} end) )
        ],
        decryption: $vdec
      },
      streamSettings: {
        network: "xhttp",
        security: "tls",
        tlsSettings: {
          certificates: [
            {
              certificateFile: $cert,
              keyFile: $key
            }
          ],
          alpn: ["h2", "http/1.1"]
        },
        xhttpSettings: {
          path: $path,
          mode: $mode,
          xPaddingBytes: "100-1000"
        },
        sockopt: {
          trustedXForwardedFor: ["CF-Connecting-IP"],
          tcpFastOpen: true,
          tcpCongestion: "bbr",
          tcpNoDelay: true,
          tcpUserTimeout: 10000,
          tcpKeepAliveIdle: 45,
          tcpKeepAliveInterval: 15
        }
      },
      sniffing: {
        enabled: true,
        destOverride: ["http", "tls", "quic"],
        routeOnly: true
      }
    }'
}

# Multi-user management and live traffic accounting.

get_all_system_users() {
  [[ -f "$CONFIG_PATH" ]] || return 0
  jq -r '.inbounds[]? | select(.tag != "api-internal" and .settings.clients != null) | .settings.clients[]? | "\(.email // "admin")|\(.id)"' "$CONFIG_PATH" 2>/dev/null | sort -u || true
}

# Inbound-specific auxiliary metadata for VLESS client encryption.
# Xray stores decryption on the server while the client link needs matching encryption.
# Store the paired string separately; Xray rejects unknown top-level config keys.
meta_ensure() {
  install -d -m 755 "$CONFIG_DIR" 2>/dev/null || true
  if [[ ! -f "$META_PATH" ]]; then
    printf '{\n  "nodes": {}\n}\n' > "$META_PATH"
    chmod 600 "$META_PATH" 2>/dev/null || true
  fi
}

meta_get() {
  local tag="$1" key="$2"
  [[ -f "$META_PATH" ]] || { echo ""; return 0; }
  jq -r --arg t "$tag" --arg k "$key" '.nodes[$t][$k] // empty' "$META_PATH" 2>/dev/null || true
}

meta_set() {
  local tag="$1" key="$2" value="$3"
  meta_ensure
  local updated
  updated="$(jq --arg t "$tag" --arg k "$key" --arg v "$value" '
    .nodes = (.nodes // {}) |
    .nodes[$t] = ((.nodes[$t] // {}) + {($k): $v})
  ' "$META_PATH" 2>/dev/null || true)"
  [[ -n "$updated" ]] || return 1
  printf '%s\n' "$updated" > "$META_PATH"
  chmod 600 "$META_PATH" 2>/dev/null || true
  return 0
}

meta_del() {
  local tag="$1"
  [[ -f "$META_PATH" ]] || return 0
  local updated
  updated="$(jq --arg t "$tag" 'del(.nodes[$t])' "$META_PATH" 2>/dev/null || true)"
  [[ -n "$updated" ]] || return 0
  printf '%s\n' "$updated" > "$META_PATH"
  return 0
}

# Retrieve the client encryption string for an inbound (empty if unavailable).
node_client_encryption() {
  local tag="$1"
  local enc
  enc="$(meta_get "$tag" client_encryption)"
  if [[ -n "$enc" ]]; then
    echo "$enc"
  else
    echo "none"
  fi
}

# Warn when VLESS Encryption is enabled but the paired client string was not saved.
warn_if_client_encryption_missing() {
  local tag="$1" dec="$2" enc="$3"
  if [[ "$dec" != "none" && "$enc" == "none" ]]; then
    warn "Inbound ${tag} enables VLESS Encryption but lacks the paired client encryption string (possibly created by an older version). Recreate the inbound."
  fi
}

format_bytes() {
  local b="${1:-0}"
  b="$(echo "$b" | tr -cd '0-9')"
  [[ -z "$b" ]] && b=0
  if (( b < 1024 )); then
    echo "${b} B"
  elif (( b < 1048576 )); then
    awk -v b="$b" 'BEGIN {printf "%.2f KB", b/1024}'
  elif (( b < 1073741824 )); then
    awk -v b="$b" 'BEGIN {printf "%.2f MB", b/1048576}'
  elif (( b < 1099511627776 )); then
    awk -v b="$b" 'BEGIN {printf "%.2f GB", b/1073741824}'
  else
    awk -v b="$b" 'BEGIN {printf "%.2f TB", b/1099511627776}'
  fi
}

# Persistent traffic accounting across controlled restarts.

get_or_update_persistent_user_traffic() {
  local email="$1"
  local live_up="${2:-0}"
  local live_down="${3:-0}"

  install -d -m 755 "$CONFIG_DIR"
  [[ -f "$STATS_STORE_PATH" ]] || echo '{"users":{}}' > "$STATS_STORE_PATH"

  local updated
  updated="$(jq --arg email "$email" --argjson up "$live_up" --argjson down "$live_down" '
    .users[$email] = (
      (.users[$email] // {"total_up":0, "total_down":0, "last_up":0, "last_down":0}) as $u |
      (if $up >= $u.last_up then ($up - $u.last_up) else $up end) as $delta_up |
      (if $down >= $u.last_down then ($down - $u.last_down) else $down end) as $delta_down |
      {
        total_up: ($u.total_up + $delta_up),
        total_down: ($u.total_down + $delta_down),
        last_up: $up,
        last_down: $down
      }
    )
  ' "$STATS_STORE_PATH" 2>/dev/null || true)"

  if [[ -n "$updated" ]]; then
    echo "$updated" > "$STATS_STORE_PATH"
    local p_up p_down
    p_up="$(echo "$updated" | jq -r --arg email "$email" ".users[\$email].total_up // $live_up")"
    p_down="$(echo "$updated" | jq -r --arg email "$email" ".users[\$email].total_down // $live_down")"
    echo "${p_up}|${p_down}"
  else
    echo "${live_up}|${live_down}"
  fi
}

get_xray_stats_json() {
  if [[ -z "${BIN:-}" ]] || ! command -v "$BIN" >/dev/null 2>&1; then
    echo "{}"
    return 0
  fi
  if ! service_unit_exists "$SERVICE_NAME" || ! systemctl is-active --quiet "$SERVICE_NAME"; then
    echo "{}"
    return 0
  fi
  local out
  out="$("$BIN" api statsquery --server=127.0.0.1:10085 -pattern "user>>>" 2>/dev/null || true)"
  if jq -e '.stat' <<<"$out" >/dev/null 2>&1; then
    echo "$out"
  else
    echo "{}"
  fi
}

# Encode the full XHTTP settings required by v2rayN/v2rayNG. Keep mode both
# in the flat URI field and inside extra for compatibility with 3x-ui imports.
# Client-facing XHTTP mode remains a top-level URL option, not in extra.
xhttp_extra_query() {
  local inbound_json="$1" padding extra
  padding="$(jq -r '.streamSettings.xhttpSettings.extra.xPaddingBytes // .streamSettings.xhttpSettings.xPaddingBytes // "100-1000"' <<< "$inbound_json")" || return 1
  extra="$(jq -nc --arg padding "$padding" '{xPaddingBytes:$padding}')" || return 1
  url_encode "$extra"
}

# External port mapping is only metadata for exported XHTTP+TLS URLs.
# Does not alter Xray listener, TLS, firewall, or Cloudflare Origin Rules.
public_share_port_for_inbound() {
  local tag="$1" listen="$2" net="$3" sec="$4" mapped=""
  if [[ "$net" == "xhttp" && "$sec" == "tls" && -f "$PUBLIC_PORTS_PATH" ]]; then
    local configured
    if ! jq -e '.nodes | type=="object"' "$PUBLIC_PORTS_PATH" >/dev/null 2>&1; then
      warn "Public port metadata is corrupted; inbound $tag will use listener port $listen."
    else
      configured="$(jq -c --arg tag "$tag" '.nodes[$tag] // empty' "$PUBLIC_PORTS_PATH" 2>/dev/null || true)"
      if [[ -n "$configured" ]]; then
        mapped="$(jq -er --arg tag "$tag" --argjson listen "$listen" '.nodes[$tag] | select(.listen_port == $listen) | .public_port | select(type=="number" and .>=1 and .<=65535)' "$PUBLIC_PORTS_PATH" 2>/dev/null || true)"
        [[ -n "$mapped" ]] || warn "Public-port mapping for inbound $tag is invalid or stale; ignoring the old mapping." 
      fi
    fi
  fi
  if [[ "$mapped" =~ ^[0-9]+$ ]]; then printf "%s" "$mapped"; else printf "%s" "$listen"; fi
}

show_user_node_links() {
  local users
  users="$(get_all_system_users)"
  if [[ -z "$users" ]]; then
    warn "No user records found."
    return 0
  fi

  local sel_email="${1:-}" sel_uuid="${2:-}"
  if [[ -z "$sel_email" || -z "$sel_uuid" ]]; then
    local user_arr=()
    local i=0
    echo
    echo -e "  ${BOLD}Select a user to export links${NC}"
    while IFS='|' read -r email uuid; do
      [[ -z "$email" ]] && continue
      user_arr+=("${email}|${uuid}")
      echo "$((i+1))) Username: ${email} | UUID: ${uuid}"
      i=$((i + 1))
    done <<< "$users"
    echo "0) Cancel and return"

    local u_sel
    read -r -p "Choose user [1-${i}]: " u_sel || u_sel=""
    [[ "$u_sel" == "0" || -z "$u_sel" ]] && return 0
    if ! [[ "$u_sel" =~ ^[0-9]+$ ]] || ((u_sel < 1 || u_sel > i)); then
      warn "Invalid selection."
      return 0
    fi
    local chosen="${user_arr[$((u_sel-1))]}"
    sel_email="${chosen%%|*}"
    sel_uuid="${chosen##*|}"
  fi

  local server_ip
  server_ip="$(get_public_ip)"
  [[ -n "$server_ip" ]] || server_ip="YOUR_SERVER_IP"
  local host_for_link
  host_for_link="$(url_host "$server_ip")"

  local nodes_json
  nodes_json="$(jq -c '[.inbounds[]? | select(.tag != "api-internal")]' "$CONFIG_PATH" 2>/dev/null || echo '[]')"
  local n_count
  n_count="$(echo "$nodes_json" | jq 'length')"

  if (( n_count == 0 )); then
    warn "No managed inbounds have been configured."
    return 0
  fi

  echo
  echo -e "  ${BOLD}${GREEN}User [${sel_email}] inbound configurations and links (${n_count} inbounds)${NC}"
  echo -e "  User UUID: ${CYAN}${sel_uuid}${NC}"
  echo "—————————————————————————————————————————————————————————————————"

  local node_links=()
  local idx=0
  while (( idx < n_count )); do
    local ib_json
    ib_json="$(echo "$nodes_json" | jq -c --argjson i "$idx" '.[$i]')"
    local tag port net sec
    tag="$(echo "$ib_json" | jq -r '.tag // "node-" + (.port|tostring)')"
    port="$(echo "$ib_json" | jq -r '.port')"
    net="$(echo "$ib_json" | jq -r '.streamSettings.network // "tcp"')"
    sec="$(echo "$ib_json" | jq -r '.streamSettings.security // "none"')"
    local share_port
    share_port="$(public_share_port_for_inbound "$tag" "$port" "$net" "$sec")"

    # Select the user's actual credentials for each inbound assignment.
    local node_user_id node_flow
    node_user_id="$(echo "$ib_json" | jq -r --arg em "$sel_email" '[.settings.clients[]? | select(.email == $em) | .id] | first // empty' 2>/dev/null || true)"
    node_flow="$(echo "$ib_json" | jq -r --arg em "$sel_email" '[.settings.clients[]? | select(.email == $em) | .flow] | first // empty' 2>/dev/null || true)"
    local link=""
    if [[ -z "$node_user_id" ]]; then
      node_links+=("")
      echo
      echo -e "${BOLD}${CYAN}[Inbound $((idx+1))] ${tag}${NC} (Port: ${port} | Protocol: ${net}+${sec})"
      warn "This user is not assigned to the inbound; skipping link generation."
      idx=$((idx + 1))
      continue
    fi
    local enc_param
    enc_param="$(node_client_encryption "$tag")"
    warn_if_client_encryption_missing "$tag" \
      "$(echo "$ib_json" | jq -r '.settings.decryption // "none"')" "$enc_param"

    if [[ "$sec" == "reality" ]]; then
      local sni sid pbk path mode mldsa_pqv=""
      sni="$(echo "$ib_json" | jq -r '.streamSettings.realitySettings.serverNames[0] // ""')"
      [[ -n "$sni" ]] || sni="$DEFAULT_SNI"
      sid="$(echo "$ib_json" | jq -r '.streamSettings.realitySettings.shortIds[0] // ""')"
      local priv
      priv="$(echo "$ib_json" | jq -r '.streamSettings.realitySettings.privateKey // ""')"
      pbk="$(derive_reality_public_key "$priv")"
      [[ -n "$pbk" ]] || warn "Cannot derive the REALITY public key for inbound ${tag}; the exported link may be unusable."

      local seed vfy
      seed="$(echo "$ib_json" | jq -r '.streamSettings.realitySettings.mldsa65Seed // ""')"
      if [[ -n "$seed" ]]; then
        vfy="$(derive_mldsa65_verify "$seed")"
        [[ -n "$vfy" ]] && mldsa_pqv="&pqv=$(url_encode "$vfy")"
      fi

      if [[ "$net" == "tcp" ]]; then
        local node_flow_query=""
        [[ -n "$node_flow" ]] && node_flow_query="&flow=$(url_encode "$node_flow")"
        link="vless://${node_user_id}@${host_for_link}:${share_port}?type=tcp&security=reality&pbk=${pbk}${mldsa_pqv}&fp=chrome&sni=${sni}&sid=${sid}&spx=%2F${node_flow_query}&encryption=$(url_encode "$enc_param")#$(url_encode "${tag}-${sel_email%%@*}")"
      elif [[ "$net" == "xhttp" ]]; then
        path="$(echo "$ib_json" | jq -r '.streamSettings.xhttpSettings.path // "/"')"
        mode="$(echo "$ib_json" | jq -r '.streamSettings.xhttpSettings.mode // "auto"')"
        local flow_param=""
        [[ -n "$node_flow" && "$enc_param" != "none" ]] && flow_param="&flow=$(url_encode "$node_flow")"
        link="vless://${node_user_id}@${host_for_link}:${share_port}?type=xhttp&security=reality&pbk=${pbk}${mldsa_pqv}&fp=chrome&sni=${sni}&sid=${sid}&spx=%2F&path=$(url_encode "$path")&mode=${mode}&extra=$(xhttp_extra_query "$ib_json")${flow_param}&encryption=$(url_encode "$enc_param")#$(url_encode "${tag}-${sel_email%%@*}")"
      fi
    elif [[ "$sec" == "tls" ]]; then
      local cert_file domain path mode
      cert_file="$(echo "$ib_json" | jq -r '.streamSettings.tlsSettings.certificates[0].certificateFile // ""')"
      domain="$(basename "$cert_file" | sed 's/\.crt$//' | sed 's/\.pem$//')"
      [[ -z "$domain" || "$domain" == "." ]] && domain="$server_ip"
      path="$(echo "$ib_json" | jq -r '.streamSettings.xhttpSettings.path // "/"')"
      mode="$(echo "$ib_json" | jq -r '.streamSettings.xhttpSettings.mode // "auto"')"
      local flow_param=""
      [[ -n "$node_flow" && "$enc_param" != "none" ]] && flow_param="&flow=$(url_encode "$node_flow")"
      link="vless://${node_user_id}@${domain}:${share_port}?type=xhttp&security=tls&sni=${domain}&alpn=h2&fp=chrome&ech=$(url_encode "$XHTTP_CDN_ECH_CONFIG")&path=$(url_encode "$path")&mode=${mode}&extra=$(xhttp_extra_query "$ib_json")${flow_param}&encryption=$(url_encode "$enc_param")#$(url_encode "${tag}-${sel_email%%@*}")"
    fi

    node_links+=("$link")
    echo
    echo -e "${BOLD}${CYAN}[Inbound $((idx+1))] ${tag}${NC} (Port: ${port} | Protocol: ${net}+${sec})"
    if [[ -n "$link" ]]; then
      echo -e "  - ${GREEN}${BOLD}VLESS import link:${NC}\n    ${link}"
    fi
    idx=$((idx + 1))
  done

  echo
  read -r -p "Generate a terminal QR code for an inbound? Choose 1-${n_count}, or press Enter to skip: " qr_idx || qr_idx=""
  if [[ "$qr_idx" =~ ^[0-9]+$ ]] && ((1 <= qr_idx && qr_idx <= n_count)); then
    local sel_link="${node_links[$((qr_idx-1))]}"
    if [[ -n "$sel_link" ]]; then
      if ! command -v qrencode >/dev/null 2>&1; then
        install_packages qrencode || true
      fi
      if command -v qrencode >/dev/null 2>&1; then
        echo
        qrencode -t ANSIUTF8 "$sel_link" || warn "QR output failed; import using the VLESS link instead."
      fi
    fi
  fi
  echo && read -rp "Press Enter to continue..." _dummy
}

# Shared CLI/TG user directory engine (the Go binary needs no Telegram token for local use).
ensure_user_manager_binary() {
  if [[ -x "$BOT_BIN_PATH" ]]; then return 0; fi
  warn "Independent user management requires the freedom-bot binary. Install it from the Telegram Bot menu (no token needed for local management)."
  return 1
}
list_independent_user_names() {
  "$BOT_BIN_PATH" -c "$BOT_CONFIG_PATH" -local-list-users
}
add_new_user_interactive() {
  ensure_user_manager_binary || return 1
  local username selection node="" total
  read -r -p "New username (create unassigned by default): " username || return 0
  if ! [[ "$username" =~ ^[A-Za-z0-9_.@-]{1,48}$ ]];then warn "Invalid username.";return 1;fi
  total="$(jq '[.inbounds[]? | select(.protocol=="vless" and .tag!="api-internal")] | length' "$CONFIG_PATH")"
  echo "0) Create user without assigning an inbound (recommended)"
  jq -r '[.inbounds[]? | select(.protocol=="vless" and .tag!="api-internal") | .tag] | to_entries[] | "\(.key+1)) \(.value)"' "$CONFIG_PATH"
  echo "a) Assign every existing inbound (use caution)"
  read -r -p "Assign inbound number [default 0]: " selection || selection="0"
  case "$selection" in
    ""|0) ;;
    a|A) node="all" ;;
    *)
      if ! [[ "$selection" =~ ^[0-9]+$ ]] || ((10#$selection < 1 || 10#$selection > total));then warn "Invalid selection";return 1;fi
      node="$(jq -r --argjson ix "$((10#$selection-1))" '[.inbounds[]? | select(.protocol=="vless" and .tag!="api-internal") | .tag][$ix] // empty' "$CONFIG_PATH")"
      [[ -n "$node" ]] || return 1
      ;;
  esac
  "$BOT_BIN_PATH" -c "$BOT_CONFIG_PATH" -local-user-action add -local-user "$username" -local-node "$node"
}
delete_user_interactive() {
  ensure_user_manager_binary || return 1
  local username confirm
  list_independent_user_names
  read -r -p "Username to delete (Enter cancels): " username || return 0
  [[ -n "$username" ]] || return 0
  read -r -p "Delete user $username and all inbound assignments? [y/N]: " confirm || confirm="N"
  case "$confirm" in
    y|Y|yes|YES) "$BOT_BIN_PATH" -c "$BOT_CONFIG_PATH" -local-user-action delete -local-user "$username" ;;
    *) info "Cancelled" ;;
  esac
}
reset_traffic_stats_interactive() {
  ensure_stats_api_scaffold || warn "Stats API initialization failed; the traffic dashboard may be unavailable. Original config preserved."
  if ! service_unit_exists "$SERVICE_NAME" || ! systemctl is-active --quiet "$SERVICE_NAME"; then
    warn "Xray is not running; StatsService is unavailable."
    return 0
  fi

  echo
  echo -e "  ${BOLD}Reset Traffic${NC}"
  echo "————————————————————————————————————————————————"
  echo -e "  ${GREEN}1.${NC} Reset traffic for all users"
  echo -e "  ${GREEN}2.${NC} Reset traffic for one user"
  echo -e "  ${GREEN}0.${NC} Cancel and return"
  echo "————————————————————————————————————————————————"
  read -r -p "Choose [0-2]: " r_sel || r_sel="0"
  case "${r_sel:-0}" in
    1)
      read -r -p "Reset traffic for all users? [y/N]: " conf || conf="N"
      case "${conf:-N}" in
        y|Y|yes|YES)
          if ! "$BIN" api statsquery --server=127.0.0.1:10085 -pattern "user>>>" -reset=true >/dev/null 2>&1; then
            warn "Unable to reset Xray counters; persisted data was not modified."
            return 1
          fi
          # Reset the durable local counters when Xray runtime counters are cleared.
          if [[ -x "$BOT_BIN_PATH" ]]; then
            "$BOT_BIN_PATH" -c "$BOT_CONFIG_PATH" -local-traffic-reset-locked || return 1
          elif [[ -f "$STATS_STORE_PATH" ]]; then
            warn "The updated Bot management binary is missing; cannot reset the SQLite ledger safely."
            return 1
          fi
          ok "Xray counters and persisted traffic totals reset."
          echo && read -rp "Press Enter to continue..." _dummy
          ;;
        *) info "Cancelled。" ;;
      esac
      ;;
    2)
      local users
      users="$(get_all_system_users)"
      local u_arr=() i=0
      echo
      echo -e "  ${BOLD}Select the user whose traffic should be reset${NC}"
      echo "————————————————————————————————————————————————"
      while IFS='|' read -r em id; do
        [[ -z "$em" ]] && continue
        u_arr+=("$em")
        echo -e "  ${GREEN}$((i+1)).${NC} Username: ${em}"
        i=$((i + 1))
      done <<< "$users"
      echo "————————————————————————————————————————————————"
      echo -e "  ${GREEN}0.${NC} Cancel"
      echo "————————————————————————————————————————————————"
      read -r -p "Enter user number [1-${i}]: " u_idx || u_idx=""
      [[ "$u_idx" == "0" || -z "$u_idx" ]] && return 0
      if [[ "$u_idx" =~ ^[0-9]+$ ]] && ((1 <= u_idx && u_idx <= i)); then
        local target_em="${u_arr[$((u_idx-1))]}"
        if ! "$BIN" api statsquery --server=127.0.0.1:10085 -pattern "user>>>${target_em}>>>" -reset=true >/dev/null 2>&1; then
          warn "Core user counter reset failed; persisted data was not changed."
          return 1
        fi
        if [[ -x "$BOT_BIN_PATH" ]]; then
          "$BOT_BIN_PATH" -c "$BOT_CONFIG_PATH" -local-traffic-reset-locked -local-user "$target_em" || return 1
        else
          warn "Update the Bot before resetting user totals in the SQLite ledger."
          return 1
        fi
        ok "Reset traffic totals for user [${target_em}]."
        echo && read -rp "Press Enter to continue..." _dummy
      else
        warn "Invalid number."
      fi
      ;;
    *) ;;
  esac
}

# User detail menu avoids selecting the same user repeatedly for each action.
# Match both email and UUID to avoid editing another user's inbound credential.
manage_single_user_interactive() {
  ensure_user_manager_binary || return 1
  local users="" name id
  while IFS= read -r name; do
    [[ -z "$name" ]] && continue
    id=""
    if [[ -f "$CONFIG_DIR/freedom-users.json" ]]; then
      id="$(jq -r --arg name "$name" '.users[$name].id // empty' "$CONFIG_DIR/freedom-users.json" 2>/dev/null || true)"
    fi
    if [[ -z "$id" ]]; then
      id="$(jq -r --arg name "$name" '[.inbounds[]? | .settings.clients[]? | select(.email==$name) | .id] | first // empty' "$CONFIG_PATH")"
    fi
    [[ -n "$id" ]] && users+="${name}|${id}"$'\n'
  done < <(list_independent_user_names)
  if [[ -z "$users" ]]; then warn "No users."; return 0; fi

  local entries=() email uuid idx=0
  echo
  echo -e "  ${BOLD}Select a user to manage${NC}"
  while IFS='|' read -r email uuid; do
    [[ -z "$email" || -z "$uuid" ]] && continue
    entries+=("${email}|${uuid}")
    echo "  $((++idx))) ${email} | ${uuid}"
  done <<< "$users"
  echo "  0) Back"

  local pick
  read -r -p "User number [1-${idx}]: " pick || pick=""
  [[ -z "$pick" || "$pick" == "0" ]] && return 0
  if ! [[ "$pick" =~ ^[0-9]+$ ]] || ((10#$pick < 1 || 10#$pick > idx)); then
    warn "Invalid user number."
    return 1
  fi
  local selected="${entries[$((10#$pick-1))]}"
  local selected_email="${selected%%|*}" selected_uuid="${selected##*|}"

  while true; do
    echo
    echo -e "  ${BOLD}User: ${selected_email}${NC}"
    echo "  UUID: ${selected_uuid}"
    echo "  1) View all inbound links and QR codes for this user"
    echo "  2) Rename this user"
    echo "  3) Rotate this user's UUID across all assigned inbounds"
    echo "  4) Assign / unassign inbounds"
    echo "  0) Back to user list"
    local action
    read -r -p "Choose [0-4]: " action || action="0"
    case "$action" in
      1) show_user_node_links "$selected_email" "$selected_uuid" ;;
      2)
        rename_managed_user "$selected_email" "$selected_uuid" || true
        return 0
        ;;
      3)
        rotate_managed_user_uuid "$selected_email" "$selected_uuid" || true
        return 0
        ;;
      4) manage_user_node_binding_interactive "$selected_email" || true ;;
      0|"") return 0 ;;
      *) warn "Invalid choice." ;;
    esac
  done
}


manage_user_node_binding_interactive() {
  ensure_user_manager_binary || return 1
  local name="$1" node selection state action confirm
  echo "User: $name"
  jq -r '[.inbounds[]? | select(.protocol=="vless" and .tag!="api-internal")] | to_entries[] | "\(.key+1)) \(.value.tag) / port \(.value.port)"' "$CONFIG_PATH"
  read -r -p "Select inbound number to modify (0 cancels): " selection || return 0
  [[ "$selection" == "0" ]] && return 0
  if ! [[ "$selection" =~ ^[0-9]+$ ]] || ((10#$selection < 1));then warn "Invalid inbound number.";return 1;fi
  node="$(jq -r --argjson idx "$((10#$selection-1))" '[.inbounds[]? | select(.protocol=="vless" and .tag!="api-internal") | .tag][$idx] // empty' "$CONFIG_PATH")"
  [[ -n "$node" ]] || { warn "Inbound not found.";return 1; }
  state="$(jq -r --arg tag "$node" --arg name "$name" '[.inbounds[] | select(.tag==$tag) | .settings.clients[]? | select(.email==$name)] | length' "$CONFIG_PATH")"
  action="assign"
  if ((state>0));then action="unassign";fi
  read -r -p "Run $action for inbound $node? [y/N]: " confirm || confirm="N"
  case "$confirm" in
    y|Y|yes|YES) "$BOT_BIN_PATH" -c "$BOT_CONFIG_PATH" -local-user-action "$action" -local-user "$name" -local-node "$node" ;;
    *) info "Cancelled。" ;;
  esac
}

rename_managed_user() {
  ensure_user_manager_binary || return 1
  local old_name="$1" new_name
  read -r -p "New username (Enter cancels): " new_name || return 0
  [[ -z "$new_name" ]] && return 0
  "$BOT_BIN_PATH" -c "$BOT_CONFIG_PATH" -local-user-action rename -local-user "$old_name" -local-new-name "$new_name"
}
rotate_managed_user_uuid() {
  ensure_user_manager_binary || return 1
  local name="$1" confirm
  read -r -p "Rotate UUID for $name? Existing links will stop working. [y/N]: " confirm || confirm="N"
  case "$confirm" in
    y|Y|yes|YES) "$BOT_BIN_PATH" -c "$BOT_CONFIG_PATH" -local-user-action rotate -local-user "$name" ;;
    *) info "Cancelled" ;;
  esac
}

toggle_user_status_interactive() {
  if [[ ! -x "$BOT_BIN_PATH" || ! -f "$BOT_CONFIG_PATH" ]]; then
    warn "Suspend/restore requires the installed freedom-bot management binary."
    return 1
  fi
  echo
  echo "1) Suspend active user"
  echo "2) Restore suspended user"
  echo "0) Cancel"
  local option operation raw_users
  read -r -p "Choose [0-2]: " option || option="0"
  case "$option" in
    1) operation="disable"; raw_users="$(get_all_system_users | cut -d "|" -f1 | sort -u)" ;;
    2) operation="enable";
       raw_users=""
       if [[ -f "$CONFIG_DIR/freedom-disabled-users.json" ]]; then
         raw_users="$(jq -r '.users | keys[]' "$CONFIG_DIR/freedom-disabled-users.json" 2>/dev/null || true)"
       fi ;;
    *) return 0 ;;
  esac
  local names=() name idx=0
  while IFS= read -r name; do
    [[ -z "$name" ]] && continue
    names+=("$name")
    printf "  %d. %s\n" "$((++idx))" "$name"
  done <<< "$raw_users"
  if ((idx==0)); then warn "No matching users."; return 0; fi
  local selection selected confirm
  read -r -p "Choose user number [1-${idx}]: " selection || selection=""
  if ! [[ "$selection" =~ ^[0-9]+$ ]] || ((10#$selection < 1 || 10#$selection > idx)); then
    warn "Invalid number; operation cancelled."; return 0
  fi
  selected="${names[$((10#$selection-1))]}"
  read -r -p "Run ${operation} for ${selected}? Xray will restart. [y/N]: " confirm || confirm="N"
  case "$confirm" in
    y|Y|yes|YES)
      "$BOT_BIN_PATH" -c "$BOT_CONFIG_PATH" -local-user-action "$operation" -local-user "$selected" || warn "Operation failed. Check the local configuration or logs." ;;
    *) info "Cancelled。" ;;
  esac
}
show_inbound_traffic() {
  if [[ ! -x "$BOT_BIN_PATH" ]]; then
    warn "Inbound traffic totals require the installed freedom-bot management binary and its SQLite ledger."
    return 1
  fi
  local data
  if ! data="$("$BOT_BIN_PATH" -c "$BOT_CONFIG_PATH" -local-traffic-read 2>/dev/null)"; then
    data="$(cat "$STATS_STORE_PATH" 2>/dev/null || true)"
  fi
  if ! jq -e '(.inbounds // {}) | type == "object"' <<<"$data" >/dev/null 2>&1; then
    warn "The persistent traffic ledger is unreadable or has no inbound counters."
    return 1
  fi
  local count
  count="$(jq '(.inbounds // {}) | length' <<<"$data")"
  if (( count == 0 )); then
    info "No inbound traffic has been recorded yet."
    return 0
  fi

  echo
  echo -e "  ${BOLD}Inbound traffic totals${NC}"
  echo "—————————————————————————————————————————————————————————————————————"
  printf "%-30s %-14s %-14s %-14s\n" "Inbound" "Upload" "Download" "Total"
  echo "—————————————————————————————————————————————————————————————————————"
  local tag up down total
  while IFS=$'\t' read -r tag up down; do
    [[ -z "$tag" ]] && continue
    [[ "$up" =~ ^[0-9]+$ ]] || up=0
    [[ "$down" =~ ^[0-9]+$ ]] || down=0
    total=$((up + down))
    printf "%-30s %-14s %-14s %-14s\n" \
      "$tag" "$(format_bytes "$up")" "$(format_bytes "$down")" "$(format_bytes "$total")"
  done < <(jq -r '(.inbounds // {}) | to_entries | sort_by(.key)[] | [.key, (.value.total_up // 0), (.value.total_down // 0)] | @tsv' <<<"$data")
  echo "—————————————————————————————————————————————————————————————————————"
  info "Totals are persisted SQLite counters; user-level totals are shown in the user table."
  echo && read -rp "Press Enter to continue..." _dummy
}

show_online_users() {
  if [[ -z "${BIN:-}" || ! -x "$BIN" ]]; then
    warn "Xray binary is unavailable; cannot query online sessions."
    return 1
  fi
  local out
  if ! out="$("$BIN" api statsonlineiplist --server=127.0.0.1:10085 -all 2>/dev/null)"; then
    warn "Online-stats API is unavailable. Ensure Xray is running and policy.levels.0.statsUserOnline is true."
    return 1
  fi
  if ! jq -e '.users | type == "array"' <<<"$out" >/dev/null 2>&1; then
    # The API returns {} when no user currently has an online session.
    info "No online users are currently reported by Xray."
    return 0
  fi
  local count
  count="$(jq '.users | length' <<<"$out")"
  if (( count == 0 )); then
    info "No online users are currently reported by Xray."
    return 0
  fi

  echo
  echo -e "  ${BOLD}Online users (live Xray sessions)${NC}"
  echo "—————————————————————————————————————————————————————————————————————"
  printf "%-24s %s\n" "User" "Source IPs"
  echo "—————————————————————————————————————————————————————————————————————"
  local email ips
  while IFS=$'\t' read -r email ips; do
    [[ -z "$email" ]] && continue
    printf "%-24s %s\n" "$email" "${ips:-unknown}"
  done < <(jq -r '.users[]? | [.email, (((.ips // []) | map(.ip) | join(", ")) // "")] | @tsv' <<<"$out")
  echo "—————————————————————————————————————————————————————————————————————"
  info "Live sessions only; recent-IP history and last-online data are maintained by freedom-bot's collector."
  echo && read -rp "Press Enter to continue..." _dummy
}

user_management_menu() {
  locked_config_operation ensure_stats_api_scaffold || warn "Stats API initialization failed; the traffic dashboard may be unavailable. Original config preserved."
  while true; do
    local stats_data
    stats_data="$(get_xray_stats_json)"
    local stored_stats=""
    if [[ -x "$BOT_BIN_PATH" ]]; then
      "$BOT_BIN_PATH" -c "$BOT_CONFIG_PATH" -local-traffic-snapshot >/dev/null 2>&1 || warn "Traffic sampling failed; showing the last saved totals."
      # The SQLite ledger is canonical; the JSON mirror is only a compatibility fallback.
      stored_stats="$("$BOT_BIN_PATH" -c "$BOT_CONFIG_PATH" -local-traffic-read 2>/dev/null || true)"
      if ! jq -e '.users | type=="object"' <<< "$stored_stats" >/dev/null 2>&1; then
        stored_stats="$(cat "$STATS_STORE_PATH" 2>/dev/null || true)"
      fi
      if ! jq -e '.users | type=="object"' <<< "$stored_stats" >/dev/null 2>&1; then
        warn "Unable to read the persistent traffic ledger; showing current Xray runtime counters."
        stored_stats=""
      fi
    else
      warn "Bot manager is not installed; statistics are collected on demand and cannot be persisted in the background."
    fi

    local users
    users="$(get_all_system_users)"

    echo
    echo -e "  ${BOLD}Users and Traffic${NC}"
    echo "—————————————————————————————————————————————————————————————————————————————————————"
    printf "${BOLD}%-6s %-20s %-38s %-12s %-12s %-12s${NC}\n" "No." "Username (email)" "UUID" "Upload" "Download" "Total traffic"
    echo "—————————————————————————————————————————————————————————————————————————————————————"

    local user_list=()
    local idx=0
    if [[ -n "$users" ]]; then
      while IFS='|' read -r u_email u_id; do
        [[ -z "$u_email" || -z "$u_id" ]] && continue
        user_list+=("${u_email}|${u_id}")
        idx=$((idx + 1))

        local live_up live_down
        live_up="$(jq -r --arg email "$u_email" '.stat[]? | select(.name == ("user>>>" + $email + ">>>traffic>>>uplink")) | .value // "0"' <<<"$stats_data" | tail -n1)"
        live_down="$(jq -r --arg email "$u_email" '.stat[]? | select(.name == ("user>>>" + $email + ">>>traffic>>>downlink")) | .value // "0"' <<<"$stats_data" | tail -n1)"
        [[ -z "$live_up" ]] && live_up=0
        [[ -z "$live_down" ]] && live_down=0

        local persisted p_up p_down total_bytes
        if [[ -n "$stored_stats" ]]; then
          p_up="$(jq -r --arg email "$u_email" '.users[$email].total_up // 0' <<< "$stored_stats")"
          p_down="$(jq -r --arg email "$u_email" '.users[$email].total_down // 0' <<< "$stored_stats")"
          persisted="${p_up}|${p_down}"
        elif [[ -x "$BOT_BIN_PATH" ]]; then
          # Do not write from the CLI when the journal is unreadable. The Go
          # collector owns all updates and preserves damaged files for repair.
          persisted="${live_up}|${live_down}"
        else
          persisted="$(get_or_update_persistent_user_traffic "$u_email" "$live_up" "$live_down")"
        fi
        p_up="${persisted%%|*}"
        p_down="${persisted##*|}"
        [[ -z "$p_up" ]] && p_up="$live_up"
        [[ -z "$p_down" ]] && p_down="$live_down"
        total_bytes=$((p_up + p_down))

        local up_fmt down_fmt total_fmt
        up_fmt="$(format_bytes "$p_up")"
        down_fmt="$(format_bytes "$p_down")"
        total_fmt="$(format_bytes "$total_bytes")"

        printf "%-6s %-20s %-38s %-12s %-12s %-12s\n" \
          "$idx" "$u_email" "$u_id" "$up_fmt" "$down_fmt" "$total_fmt"
      done <<< "$users"
    fi

    if (( idx == 0 )); then
      echo "No users found. Create an independent user and explicitly assign an inbound."
    fi
    echo "—————————————————————————————————————————————————————————————————————————————————————"
    echo -e "  ${GREEN}1.${NC} View user links and QR codes"
    echo -e "  ${GREEN}2.${NC} Add user"
    echo -e "  ${GREEN}3.${NC} Delete user"
    echo -e "  ${GREEN}4.${NC} Reset Traffic"
    echo -e "  ${GREEN}5.${NC} Refresh statistics"
    echo -e "  ${GREEN}6.${NC} Manage a selected user (links / rename / UUID)"
    echo -e "  ${GREEN}7.${NC} Suspend / restore users (requires freedom-bot)"
    echo -e "  ${GREEN}8.${NC} View inbound traffic totals (requires freedom-bot)"
    echo -e "  ${GREEN}9.${NC} View online users (live Xray sessions)"
    echo -e "  ${GREEN}0.${NC} Back to main menu"
    echo "—————————————————————————————————————————————————————————————————————————————————————"

    read -r -p "Choose action [0-9]: " u_sel || u_sel="0"
    case "${u_sel:-0}" in
      1) show_user_node_links || true ;;
      2) add_new_user_interactive || true ;;
      3) delete_user_interactive || true ;;
      4) locked_config_operation reset_traffic_stats_interactive || true ;;
      5) ;;
      6) manage_single_user_interactive || true ;;
      7) toggle_user_status_interactive || true ;;
      8) show_inbound_traffic || true ;;
      9) show_online_users || true ;;
      0) return 0 ;;
      *) warn "Invalid option; try again." ;;
    esac
  done
}

# Online update and release management.

get_remote_script_version() {
  local url="$RAW_SCRIPT_URL"
  local ver
  ver="$(curl -fsSL --connect-timeout 4 "$url" 2>/dev/null | grep -E '^SCRIPT_VERSION=' | head -n1 | cut -d'"' -f2 || true)"
  if [[ -z "$ver" ]]; then
    url="$GHPROXY_SCRIPT_URL"
    ver="$(curl -fsSL --connect-timeout 4 "$url" 2>/dev/null | grep -E '^SCRIPT_VERSION=' | head -n1 | cut -d'"' -f2 || true)"
  fi
  echo "$ver"
}

get_remote_xray_version() {
  local ver
  ver="$(curl -fsSL --connect-timeout 5 "https://api.github.com/repos/XTLS/Xray-core/releases/latest" 2>/dev/null | grep -Po '"tag_name":\s*"\K[^"]*' || true)"
  if [[ -z "$ver" ]]; then
    ver="$(curl -fsSL --connect-timeout 5 "https://api.github.com/repos/XTLS/Xray-core/tags" 2>/dev/null | grep -Po '"name":\s*"\K[^"]*' | head -n1 || true)"
  fi
  echo "$ver"
}

# Only the script and an already installed Bot are eligible for combined updates.
# Xray-core remains a separate, explicitly confirmed upgrade.
update_freedom_script() {
  info "Downloading the latest freedom.sh from GitHub main (content-based update detection)..."
  local tmp_file
  tmp_file="$(mktemp /tmp/freedom_update_XXXXXXXX.sh)" || return 1
  if ! curl --proto "=https" -fsSL --connect-timeout 8 --max-time 50 --retry 2 "$RAW_SCRIPT_URL" -o "$tmp_file"; then
    rm -f "$tmp_file"
    warn "Official GitHub download failed. The installed script was not modified; third-party mirrors are not trusted."
    return 1
  fi
  if ! head -n 1 "$tmp_file" | grep -q "^#!/usr/bin/env bash" ||
     ! grep -q '^SCRIPT_VERSION=' "$tmp_file" || ! bash -n "$tmp_file"; then
    rm -f "$tmp_file"
    warn "Script validation failed: expected marker missing or invalid Bash syntax. Installed version unchanged."
    return 1
  fi
  local target="/usr/local/bin/freedom"
  if [[ -f "$target" ]] && cmp -s "$tmp_file" "$target"; then
    rm -f "$tmp_file"
    ok "Manager script matches GitHub main."
    maybe_offer_bot_refresh --force
    return $?
  fi
  if [[ -f "$target" ]]; then
    if ! cp -a "$target" "${target}.previous"; then
      rm -f "$tmp_file"
      warn "Unable to back up the previous script; update cancelled."
      return 1
    fi
  fi
  if ! install -m 755 "$tmp_file" "${target}.next" || ! mv -f "${target}.next" "$target"; then
    rm -f "$tmp_file" "${target}.next"
    warn "Manager installation failed; previous copy retained at ${target}.previous."
    return 1
  fi
  rm -f "$tmp_file"
  ok "Latest freedom.sh installed. Previous backup: ${target}.previous"
  info "Restart the manager to receive the Bot sync prompt. Xray-core will not be upgraded automatically."
  exec "$target"
}

upgrade_xray_core_official() {
  info "Checking the latest official Xray-core version..."
  local cur_ver="Unknown"
  if [[ -n "${BIN:-}" ]] && [[ -x "$BIN" ]]; then
    cur_ver="$($BIN version 2>&1 | head -n1 || true)"
    cur_ver="${cur_ver:-Unknown}"
  fi
  local r_tag
  r_tag="$(get_remote_xray_version)"

  echo -e "Installed core version:  ${BOLD}${CYAN}${cur_ver}${NC}"
  echo -e "Latest official GitHub release:  ${BOLD}${GREEN}${r_tag:-Unavailable (will use official installer)}${NC}"
  echo
  read -r -p "Run the official XTLS Xray installer to upgrade? [Y/n]: " c_ans || c_ans="Y"
  case "${c_ans:-Y}" in
    n|N|no|NO) return 0 ;;
  esac

  info "Running official Xray-core installer..."
  # Preserve the current binary so the terminal rollback path can restore it.
  local old_bin="${BIN:-}"
  if [[ -n "$old_bin" && -x "$old_bin" ]]; then
    if ! cp -a "$old_bin" "${old_bin}.previous"; then
      warn "Unable to back up the current Xray binary; rollback may be unavailable after this upgrade."
    fi
  fi
  local installer
  installer="$(curl -fsSL --connect-timeout 8 https://github.com/XTLS/Xray-install/raw/main/install-release.sh 2>/dev/null || true)"
  if [[ -z "$installer" ]]; then
    warn "Unable to download the official Xray installer; check network connectivity."
    return 1
  fi
  if ! bash -c "$installer" @ install; then
    warn "Official Xray installer failed; check logs and network connectivity."
    return 1
  fi
  BIN="$(command -v xray || true)"
  if [[ -z "$BIN" || ! -x "$BIN" ]]; then
    warn "No Xray executable found after upgrade; inspect installation."
    return 1
  fi
  if [[ -n "$old_bin" && "$BIN" != "$old_bin" && -x "${old_bin}.previous" && ! -e "${BIN}.previous" ]]; then
    cp -a "${old_bin}.previous" "${BIN}.previous" 2>/dev/null || warn "Unable to carry the previous-core backup to ${BIN}.previous."
  fi
  CORE_VERSION_CACHE=""
  ensure_xray_systemd_service
  validate_and_restart_service

  local new_ver
  new_ver="$("$BIN" version 2>&1 | head -n1 || true)"
  ok "Xray-core successfully updated to: ${new_ver:-Unknown version}！"
}

update_rule_assets_interactive() {
  info "Updating official GeoIP / GeoSite assets..."
  local installer
  installer="$(curl -fsSL --connect-timeout 8 https://github.com/XTLS/Xray-install/raw/main/install-release.sh 2>/dev/null || true)"
  if [[ -n "$installer" ]] && bash -c "$installer" @ install-geodata; then
    ok "GeoIP / GeoSite assets updated."
  else
    warn "Geo data update failed; continuing with the 1stream asset update."
  fi

  info "Downloading the latest 1stream.dat routing asset..."
  install -d -m 755 /usr/local/share/xray
  local stream_url="https://github.com/u-fw/freedom-xray/releases/download/1stream-latest/1stream.dat"
  local tmp_dat="/usr/local/share/xray/1stream.dat.tmp"
  if curl -fsSL --connect-timeout 8 "$stream_url" -o "$tmp_dat" 2>/dev/null; then
    mv -f "$tmp_dat" /usr/local/share/xray/1stream.dat
    chmod 644 /usr/local/share/xray/1stream.dat
    ok "Updated /usr/local/share/xray/1stream.dat successfully."
  else
    rm -f "$tmp_dat"
    warn "1stream.dat download failed; check network connectivity."
  fi

  validate_and_restart_service
  ok "Routing asset updates completed."
}

rollback_xray_core_interactive() {
  local current="${BIN:-/usr/local/bin/xray}"
  local previous="${current}.previous"
  if [[ ! -x "$current" ]]; then
    warn "Current Xray binary not found or not executable: ${current}"
    return 1
  fi
  if [[ ! -x "$previous" ]]; then
    warn "No previous Xray binary is available at ${previous}."
    return 1
  fi
  if [[ ! -f "$CONFIG_PATH" ]]; then
    warn "Xray configuration not found: ${CONFIG_PATH}"
    return 1
  fi

  info "Current core: $("$current" version 2>&1 | head -n1)"
  info "Previous core: $("$previous" version 2>&1 | head -n1)"
  if ! "$previous" run -test -c "$CONFIG_PATH" >/tmp/freedom-xray-rollback-test.log 2>&1 &&
     ! "$previous" -test -config "$CONFIG_PATH" >>/tmp/freedom-xray-rollback-test.log 2>&1; then
    cat /tmp/freedom-xray-rollback-test.log >&2
    warn "The previous core rejected the current configuration; rollback cancelled."
    return 1
  fi
  read -r -p "Roll back Xray-core and restart the service? [y/N]: " confirm || confirm="N"
  case "${confirm:-N}" in
    y|Y|yes|YES) ;;
    *) info "Cancelled."; return 0 ;;
  esac

  local backup="${current}.rollback.$(date +%Y%m%d%H%M%S)"
  if ! cp -a "$current" "$backup"; then
    warn "Unable to back up the current Xray binary; rollback cancelled."
    return 1
  fi
  snapshot_traffic_before_restart
  if ! install -m 755 "$previous" "$current"; then
    warn "Unable to install the previous Xray binary; rollback cancelled."
    return 1
  fi
  BIN="$current"
  if systemctl restart "$SERVICE_NAME" && sleep 2 && systemctl is-active --quiet "$SERVICE_NAME"; then
    if install -m 755 "$backup" "$previous"; then
      ok "Xray-core rolled back to the previous binary. The replaced core is now ${previous}."
    else
      warn "Rollback succeeded, but updating ${previous} failed; inspect the binary manually."
    fi
  else
    print_service_diagnostics "$SERVICE_NAME"
    warn "The previous core failed to start; restoring the current binary."
    if install -m 755 "$backup" "$current"; then
      systemctl restart "$SERVICE_NAME" || true
    else
      warn "Restoring the current binary failed; recover manually from ${backup}."
    fi
    return 1
  fi
}

system_update_center_menu() {
  while true; do
    local cur_core
    cur_core="$(get_xray_core_version)"

    echo
    echo -e "  ${BOLD}Online Updates${NC}"
    echo -e "  Script: ${SCRIPT_VERSION} | Core: ${cur_core}"
    echo "————————————————————————————————————————————————"
    echo -e "  ${GREEN}1.${NC} Update manager and installed Bot"
    echo -e "  ${GREEN}2.${NC} Upgrade Xray-core"
    echo -e "  ${GREEN}3.${NC} Update GeoIP / GeoSite / 1stream.dat"
    echo -e "  ${GREEN}4.${NC} Check component versions"
    echo -e "  ${GREEN}5.${NC} Update Telegram Bot only"
    echo -e "  ${GREEN}6.${NC} Roll back Xray-core to the previous binary"
    echo -e "  ${GREEN}0.${NC} Back to main menu"
    echo "————————————————————————————————————————————————"

    read -r -p "Choose update action [0-6]: " up_sel || up_sel="0"
    case "${up_sel:-0}" in
      1)
        update_freedom_script || true
        echo && read -rp "Press Enter to continue..." _dummy
        ;;
      5)
        if [[ -x "$BOT_BIN_PATH" || -f "$BOT_CONFIG_PATH" ]]; then
          install_or_update_freedom_bot || warn "Bot update failed; check logs."
        else
          warn "Bot not installed; configure it in the Telegram Bot menu first."
        fi
        echo && read -rp "Press Enter to continue..." _dummy
        ;;
      6)
        rollback_xray_core_interactive || true
        echo && read -rp "Press Enter to continue..." _dummy
        ;;
      2)
        upgrade_xray_core_official || true
        echo && read -rp "Press Enter to continue..." _dummy
        ;;
      3)
        update_rule_assets_interactive || true
        echo && read -rp "Press Enter to continue..." _dummy
        ;;
      4)
        info "Fetching upstream versions from GitHub..."
        local r_sc r_xr
        r_sc="$(get_remote_script_version)"
        r_xr="$(get_remote_xray_version)"
        echo
        echo -e "Component           Installed version        GitHub latest"
        echo "─────────────────────────────────────────────────────────────────"
        printf "%-18s %-22s %-20s\n" "freedom.sh" "$SCRIPT_VERSION" "${r_sc:-Check failed}"
        printf "%-18s %-22s %-20s\n" "Xray-core" "$cur_core" "${r_xr:-Check failed}"
        echo "─────────────────────────────────────────────────────────────────"
        echo && read -rp "Press Enter to continue..." _dummy
        ;;
      0) return 0 ;;
      *) warn "Invalid choice." ;;
    esac
  done
}

# Managed inbound operations.

# 1. List inbounds and show details.
list_and_show_nodes() {
  ensure_config_scaffold
  local nodes_json
  nodes_json="$(jq -c '[.inbounds[]? | select(.tag != "api-internal")]' "$CONFIG_PATH" 2>/dev/null || echo '[]')"
  local count
  count="$(echo "$nodes_json" | jq 'length')"

  if ((count == 0)); then
    warn "No managed inbounds are configured."
    return 0
  fi

  local server_ip
  server_ip="$(get_public_ip)"
  [[ -n "$server_ip" ]] || server_ip="YOUR_SERVER_IP"
  local host_for_link
  host_for_link="$(url_host "$server_ip")"

  echo
  echo -e "  ${BOLD}${GREEN}Managed Xray-core inbounds (${count})${NC}"
  echo "—————————————————————————————————————————————————————————————————"

  local i=0
  while ((i < count)); do
    local ib_json
    ib_json="$(echo "$nodes_json" | jq -c --argjson idx "$i" '.[$idx]')"

    local tag port net sec uuid email
    tag="$(echo "$ib_json" | jq -r '.tag // "node-" + (.port|tostring)')"
    port="$(echo "$ib_json" | jq -r '.port')"
    net="$(echo "$ib_json" | jq -r '.streamSettings.network // "tcp"')"
    sec="$(echo "$ib_json" | jq -r '.streamSettings.security // "none"')"
    local share_port
    share_port="$(public_share_port_for_inbound "$tag" "$port" "$net" "$sec")"
    uuid="$(echo "$ib_json" | jq -r '.settings.clients[0].id // empty')"
    email="$(echo "$ib_json" | jq -r '.settings.clients[0].email // "admin@freedom"')"
    local client_count
    client_count="$(echo "$ib_json" | jq '.settings.clients | length')"

    echo
    echo -e "${BOLD}${CYAN}[Inbound $((i+1))] Tag: ${tag}${NC}"
    echo "  - Listen port: ${port} | Public port: ${share_port} | Transport: ${net} | Security: ${sec} | Clients: ${client_count}"
    if [[ -z "$uuid" ]]; then
      info "No users assigned to this inbound. Assign access in user management before exporting links."
      i=$((i+1))
      continue
    fi
    echo "  - Displayed user: ${email} (${uuid})"

    local enc_param
    enc_param="$(node_client_encryption "$tag")"
    warn_if_client_encryption_missing "$tag" \
      "$(echo "$ib_json" | jq -r '.settings.decryption // "none"')" "$enc_param"
    local link=""
    if [[ "$sec" == "reality" ]]; then
      local sni sid pbk flow path mode mldsa_pqv=""
      sni="$(echo "$ib_json" | jq -r '.streamSettings.realitySettings.serverNames[0] // ""')"
      [[ -n "$sni" ]] || sni="$DEFAULT_SNI"
      sid="$(echo "$ib_json" | jq -r '.streamSettings.realitySettings.shortIds[0] // ""')"
      local priv
      priv="$(echo "$ib_json" | jq -r '.streamSettings.realitySettings.privateKey // ""')"
      pbk="$(derive_reality_public_key "$priv")"

      local seed vfy
      seed="$(echo "$ib_json" | jq -r '.streamSettings.realitySettings.mldsa65Seed // ""')"
      if [[ -n "$seed" ]]; then
        vfy="$(derive_mldsa65_verify "$seed")"
        [[ -n "$vfy" ]] && mldsa_pqv="&pqv=$(url_encode "$vfy")"
      fi

      if [[ "$net" == "tcp" ]]; then
        flow="$(echo "$ib_json" | jq -r '.settings.clients[0].flow // ""')"
        local flow_param=""
        [[ -n "$flow" ]] && flow_param="&flow=${flow}"
        link="vless://${uuid}@${host_for_link}:${share_port}?type=tcp&security=reality&pbk=${pbk}${mldsa_pqv}&fp=chrome&sni=${sni}&sid=${sid}&spx=%2F${flow_param}&encryption=$(url_encode "$enc_param")#$(url_encode "$tag")"
        echo "  - Decoy SNI: ${sni} | shortId: ${sid} | Flow: ${flow}"
      elif [[ "$net" == "xhttp" ]]; then
        path="$(echo "$ib_json" | jq -r '.streamSettings.xhttpSettings.path // "/"')"
        mode="$(echo "$ib_json" | jq -r '.streamSettings.xhttpSettings.mode // "auto"')"
        flow="$(echo "$ib_json" | jq -r '.settings.clients[0].flow // empty')"
        local dec
        dec="$(echo "$ib_json" | jq -r '.settings.decryption // "none"')"
        local flow_param=""
        if [[ -n "$flow" && "$enc_param" != "none" ]]; then
          flow_param="&flow=$(url_encode "$flow")"
        fi
        link="vless://${uuid}@${host_for_link}:${share_port}?type=xhttp&security=reality&pbk=${pbk}${mldsa_pqv}&fp=chrome&sni=${sni}&sid=${sid}&spx=%2F&path=$(url_encode "$path")&mode=${mode}&extra=$(xhttp_extra_query "$ib_json")${flow_param}&encryption=$(url_encode "$enc_param")#$(url_encode "$tag")"
        echo "  - Decoy SNI: ${sni} | XHTTP path: ${path} | Mode: ${mode} | Flow: ${flow:-none} | Decryption: ${dec}"
      fi
    elif [[ "$sec" == "tls" ]]; then
      local cert_file domain path mode flow
      cert_file="$(echo "$ib_json" | jq -r '.streamSettings.tlsSettings.certificates[0].certificateFile // ""')"
      domain="$(basename "$cert_file" | sed 's/\.crt$//' | sed 's/\.pem$//')"
      [[ -z "$domain" || "$domain" == "." ]] && domain="$server_ip"
      path="$(echo "$ib_json" | jq -r '.streamSettings.xhttpSettings.path // "/"')"
      mode="$(echo "$ib_json" | jq -r '.streamSettings.xhttpSettings.mode // "auto"')"
      flow="$(echo "$ib_json" | jq -r '.settings.clients[0].flow // empty')"
      local flow_param=""
      if [[ -n "$flow" && "$enc_param" != "none" ]]; then
        flow_param="&flow=$(url_encode "$flow")"
      fi
      link="vless://${uuid}@${domain}:${share_port}?type=xhttp&security=tls&sni=${domain}&alpn=h2&fp=chrome&ech=$(url_encode "$XHTTP_CDN_ECH_CONFIG")&path=$(url_encode "$path")&mode=${mode}&extra=$(xhttp_extra_query "$ib_json")${flow_param}&encryption=$(url_encode "$enc_param")#$(url_encode "$tag")"
      echo "  - TLS domain/SNI: ${domain} | XHTTP path: ${path} | Mode: ${mode} | Flow: ${flow:-none} (CDN proxy mode)"
      info "ECH parameters are included in the client link. The CDN must publish valid ECH DNS records and the client must support ECH; remove ECH when troubleshooting."
    fi

    if [[ "$enc_param" != "none" ]]; then
      echo "  - Post-quantum encryption: ML-KEM-768 (Paired client encryption was included in the sharing link)"
    fi

    if [[ -n "$link" ]]; then
      echo -e "  - ${GREEN}${BOLD}VLESS link:${NC}\n    ${link}"
    fi

    i=$((i + 1))
  done

  echo
  read -r -p "Generate a terminal QR code? Choose inbound 1-${count}, or press Enter to skip: " qr_idx || qr_idx=""
  if [[ "$qr_idx" =~ ^[0-9]+$ ]] && ((1 <= qr_idx && qr_idx <= count)); then
    local target_i=$((qr_idx - 1))
    local ib_json
    ib_json="$(echo "$nodes_json" | jq -c --argjson idx "$target_i" '.[$idx]')"
    local net sec port uuid tag
    tag="$(echo "$ib_json" | jq -r '.tag // "node-" + (.port|tostring)')"
    port="$(echo "$ib_json" | jq -r '.port')"
    net="$(echo "$ib_json" | jq -r '.streamSettings.network // "tcp"')"
    sec="$(echo "$ib_json" | jq -r '.streamSettings.security // "none"')"
    local share_port
    share_port="$(public_share_port_for_inbound "$tag" "$port" "$net" "$sec")"
    uuid="$(echo "$ib_json" | jq -r '.settings.clients[0].id // empty')"
    if [[ -z "$uuid" ]]; then
      warn "Selected inbound has no assigned users. Assign a user before generating a QR code."
      echo && read -rp "Press Enter to return to the main menu..." _dummy
      return 0
    fi

    local enc_param
    enc_param="$(node_client_encryption "$tag")"
    warn_if_client_encryption_missing "$tag" \
      "$(echo "$ib_json" | jq -r '.settings.decryption // "none"')" "$enc_param"
    local target_link=""
    if [[ "$sec" == "reality" ]]; then
      local sni sid pbk flow path mode mldsa_pqv=""
      sni="$(echo "$ib_json" | jq -r '.streamSettings.realitySettings.serverNames[0] // ""')"
      [[ -n "$sni" ]] || sni="$DEFAULT_SNI"
      sid="$(echo "$ib_json" | jq -r '.streamSettings.realitySettings.shortIds[0] // ""')"
      local priv
      priv="$(echo "$ib_json" | jq -r '.streamSettings.realitySettings.privateKey // ""')"
      pbk="$(derive_reality_public_key "$priv")"
      local seed vfy
      seed="$(echo "$ib_json" | jq -r '.streamSettings.realitySettings.mldsa65Seed // ""')"
      if [[ -n "$seed" ]]; then
        vfy="$(derive_mldsa65_verify "$seed")"
        [[ -n "$vfy" ]] && mldsa_pqv="&pqv=$(url_encode "$vfy")"
      fi
      flow="$(echo "$ib_json" | jq -r '.settings.clients[0].flow // empty')"
      if [[ "$net" == "tcp" ]]; then
        local flow_param=""
        [[ -n "$flow" ]] && flow_param="&flow=${flow}"
        target_link="vless://${uuid}@${host_for_link}:${share_port}?type=tcp&security=reality&pbk=${pbk}${mldsa_pqv}&fp=chrome&sni=${sni}&sid=${sid}&spx=%2F${flow_param}&encryption=$(url_encode "$enc_param")#$(url_encode "$tag")"
      else
        path="$(echo "$ib_json" | jq -r '.streamSettings.xhttpSettings.path // "/"')"
        mode="$(echo "$ib_json" | jq -r '.streamSettings.xhttpSettings.mode // "auto"')"
        local flow_param=""
        if [[ -n "$flow" && "$enc_param" != "none" ]]; then
          flow_param="&flow=$(url_encode "$flow")"
        fi
        target_link="vless://${uuid}@${host_for_link}:${share_port}?type=xhttp&security=reality&pbk=${pbk}${mldsa_pqv}&fp=chrome&sni=${sni}&sid=${sid}&spx=%2F&path=$(url_encode "$path")&mode=${mode}&extra=$(xhttp_extra_query "$ib_json")${flow_param}&encryption=$(url_encode "$enc_param")#$(url_encode "$tag")"
      fi
    elif [[ "$sec" == "tls" ]]; then
      local cert_file domain path mode flow
      cert_file="$(echo "$ib_json" | jq -r '.streamSettings.tlsSettings.certificates[0].certificateFile // ""')"
      domain="$(basename "$cert_file" | sed 's/\.crt$//' | sed 's/\.pem$//')"
      [[ -z "$domain" || "$domain" == "." ]] && domain="$server_ip"
      path="$(echo "$ib_json" | jq -r '.streamSettings.xhttpSettings.path // "/"')"
      mode="$(echo "$ib_json" | jq -r '.streamSettings.xhttpSettings.mode // "auto"')"
      flow="$(echo "$ib_json" | jq -r '.settings.clients[0].flow // empty')"
      local flow_param=""
      if [[ -n "$flow" && "$enc_param" != "none" ]]; then
        flow_param="&flow=$(url_encode "$flow")"
      fi
      target_link="vless://${uuid}@${domain}:${share_port}?type=xhttp&security=tls&sni=${domain}&alpn=h2&fp=chrome&ech=$(url_encode "$XHTTP_CDN_ECH_CONFIG")&path=$(url_encode "$path")&mode=${mode}&extra=$(xhttp_extra_query "$ib_json")${flow_param}&encryption=$(url_encode "$enc_param")#$(url_encode "$tag")"
    fi

    if [[ -n "$target_link" ]]; then
      if ! command -v qrencode >/dev/null 2>&1; then
        install_packages qrencode || true
      fi
      if command -v qrencode >/dev/null 2>&1; then
        echo
        qrencode -t ANSIUTF8 "$target_link" || warn "QR output failed; use the URL link above."
      fi
    fi
  fi
  echo && read -rp "Press Enter to return to the main menu..." _dummy
}

# Ask about VLESS Encryption; return selection via ASK_VDEC and ASK_VENC.
# Use globals rather than echo to keep prompts out of command-substitution output.
ASK_VDEC="none"
ASK_VENC="none"

ask_vless_encryption_mode() {
  ASK_VDEC="none"
  ASK_VENC="none"
  local title="${1:-VLESS post-quantum encryption (ML-KEM-768):}"
  if ! xray_supports_subcommand vlessenc; then
    info "This Xray-core lacks the vlessenc subcommand. Using no VLESS Encryption for this inbound."
    return 0
  fi
  local enc_opt=""
  echo
  echo -e "${BOLD}${title}${NC}"
  echo "1) none (fast default)"
  echo "2) ML-KEM-768 (post-quantum encryption)"
  read -r -p "Encryption mode [1/2, default 1]: " enc_opt || enc_opt="1"
  if [[ "${enc_opt:-1}" == "2" ]]; then
    if generate_vlessenc_mlkem_pair; then
      ASK_VDEC="$VLESSENC_DECRYPTION"
      ASK_VENC="$VLESSENC_ENCRYPTION"
      ok "Enabled VLESS ML-KEM-768 post-quantum encryption."
    else
      warn "Could not parse vlessenc output; falling back to none."
    fi
  fi
}

# Ask about REALITY ML-DSA-65 signatures; store the seed in ASK_MLDSA_SEED.
ASK_MLDSA_SEED=""

ask_mldsa65_signature() {
  ASK_MLDSA_SEED=""
  xray_supports_subcommand mldsa65 || return 0
  local ans=""
  read -r -p "Enable REALITY ML-DSA-65 post-quantum signatures? [y/N]: " ans || ans=""
  case "${ans:-N}" in
    y|Y|yes|YES)
      if generate_mldsa65_pair; then
        ASK_MLDSA_SEED="$MLDSA_SEED"
        ok "Enabled ML-DSA-65 post-quantum signatures."
      else
        warn "ML-DSA-65 key generation failed; continuing without signatures."
      fi
      ;;
  esac
}

# 2. Create a new inbound from one of three base templates.
add_new_node_interactive() {
  ensure_config_scaffold
  echo
  echo -e "  ${BOLD}Add inbound${NC}"
  echo "————————————————————————————————————————————————"
  echo -e "  ${GREEN}1.${NC} VLESS + TCP + REALITY + Vision (direct)"
  echo -e "  ${GREEN}2.${NC} VLESS + XHTTP + REALITY (no domain required)"
  echo -e "  ${GREEN}3.${NC} VLESS + XHTTP + TLS（CDN）"
  echo -e "  ${GREEN}0.${NC} Back to main menu"
  echo "————————————————————————————————————————————————"

  local t_sel
  read -r -p "Choose template [1-3, default 1]: " t_sel || t_sel="1"
  t_sel="${t_sel:-1}"
  [[ "$t_sel" == "0" ]] && return 0

  # Validate the requested port; return PORT_RESULT without command substitution.
  local new_port
  case "$t_sel" in
    1) prompt_and_check_port "Enter new inbound listener port" "443" ;;
    2) prompt_and_check_port "Enter new inbound listener port" "8443" ;;
    3) prompt_and_check_port "Enter new inbound listener port (for a CDN, consider 443/2053/8443)" "2053" ;;
    *) warn "Invalid option; returning to the main menu."; return 0 ;;
  esac
  new_port="$PORT_RESULT"

  # Collect a unique inbound tag; duplicate tags invalidate Xray config.
  local default_tag="node-${new_port}"
  case "$t_sel" in
    1) default_tag="TCP-REALITY-${new_port}" ;;
    2) default_tag="XHTTP-REALITY-${new_port}" ;;
    3) default_tag="XHTTP-TLS-CDN-${new_port}" ;;
  esac
  local new_tag
  read -r -p "Inbound tag (default ${default_tag}): " new_tag || new_tag=""
  new_tag="${new_tag:-$default_tag}"
  if jq -e --arg t "$new_tag" '[.inbounds[]? | select(.tag == $t)] | length > 0' "$CONFIG_PATH" >/dev/null 2>&1; then
    warn "Inbound tag [${new_tag}] already exists; choose a different tag."
    return 1
  fi

  # New inbounds never implicitly authorize any user. A temporary UUID is only used
  # for template generation; the clients array is cleared before committing.
  local new_uuid new_email
  new_uuid="$("$BIN" uuid 2>/dev/null || cat /proc/sys/kernel/random/uuid)"
  new_email="unbound-template"

  local new_inbound_json=""
  local client_enc_for_meta="none"

  if [[ "$t_sel" == "1" ]]; then
    # TCP + REALITY
    local sni
    read -r -p "REALITY decoy domain/SNI (default ${DEFAULT_SNI}): " sni || sni=""
    sni="$(sanitize_domain "${sni:-$DEFAULT_SNI}")"

    get_or_create_shared_reality_keys
    local priv="$REALITY_PRIV"
    local sid
    sid="$(openssl rand -hex 8 | tr -d '[:space:]')"

    ask_vless_encryption_mode "VLESS post-quantum encryption (ML-KEM-768):"
    local vdec="$ASK_VDEC"
    client_enc_for_meta="$ASK_VENC"

    ask_mldsa65_signature
    local mldsa_seed="$ASK_MLDSA_SEED"

    new_inbound_json="$(build_inbound_tcp_reality "$new_port" "$new_tag" "$new_uuid" "$sni" "$priv" "$sid" "$vdec" "$mldsa_seed" "$new_email")"

  elif [[ "$t_sel" == "2" ]]; then
    # XHTTP + REALITY
    local sni
    read -r -p "REALITY decoy domain/SNI (default ${DEFAULT_SNI}): " sni || sni=""
    sni="$(sanitize_domain "${sni:-$DEFAULT_SNI}")"

    get_or_create_shared_reality_keys
    local priv="$REALITY_PRIV"
    local sid
    sid="$(openssl rand -hex 8 | tr -d '[:space:]')"

    local rand_path
    rand_path="$(random_xhttp_path)"
    local xpath
    read -r -p "XHTTP path (Enter for random ${rand_path}): " xpath || xpath=""
    xpath="$(sanitize_path "${xpath:-$rand_path}")"

    echo "XHTTP mode: 1) auto (adaptive default)  2) packet-up (chunked upload)  3) stream-up  4) stream-one"
    local xmode_sel
    read -r -p "Choose mode [1-4, default 1]: " xmode_sel || xmode_sel="1"
    local xmode="auto"
    case "${xmode_sel:-1}" in
      2) xmode="packet-up" ;;
      3) xmode="stream-up" ;;
      4) xmode="stream-one" ;;
      *) xmode="auto" ;;
    esac

    ask_vless_encryption_mode "VLESS post-quantum encryption (ML-KEM-768):"
    local vdec="$ASK_VDEC" in_flow=""
    client_enc_for_meta="$ASK_VENC"
    if [[ "$vdec" != "none" ]]; then
      in_flow="xtls-rprx-vision"
      ok "Enabled ML-KEM-768 and Vision for the encrypted inbound."
    else
      info "VLESS Encryption is disabled; Vision is not enabled."
    fi

    ask_mldsa65_signature
    local mldsa_seed="$ASK_MLDSA_SEED"

    new_inbound_json="$(build_inbound_xhttp_reality "$new_port" "$new_tag" "$new_uuid" "$sni" "$priv" "$sid" "$xpath" "$xmode" "$mldsa_seed" "$vdec" "$in_flow" "$new_email")"

  elif [[ "$t_sel" == "3" ]]; then
    # XHTTP + TLS (CDN)
    local domain
    while true; do
      read -r -p "Enter the CDN hostname (for example cdn.example.com): " domain || domain=""
      domain="$(sanitize_domain "${domain:-}")"
      [[ -n "$domain" ]] && break
      warn "CDN mode requires a valid hostname."
    done

    setup_tls_certificate "$domain"
    local cert_file="$TLS_CERT_FILE" key_file="$TLS_KEY_FILE"
    if [[ -z "$cert_file" || -z "$key_file" || ! -f "$cert_file" || ! -f "$key_file" ]]; then
      warn "TLS certificate or key file is missing; operation cancelled."
      return 1
    fi

    local rand_path
    rand_path="$(random_xhttp_path)"
    local xpath
    read -r -p "XHTTP path (Enter for random ${rand_path}): " xpath || xpath=""
    xpath="$(sanitize_path "${xpath:-$rand_path}")"

    echo "XHTTP mode: 1) auto (recommended)  2) packet-up (CDN-compatible chunked upload)"
    local xmode_sel
    read -r -p "Choose mode [1/2, default 1]: " xmode_sel || xmode_sel="1"
    local xmode="auto"
    [[ "${xmode_sel:-1}" == "2" ]] && xmode="packet-up"

    ask_vless_encryption_mode "VLESS post-quantum encryption (ML-KEM-768):"
    local vdec="$ASK_VDEC" in_flow=""
    client_enc_for_meta="$ASK_VENC"
    if [[ "$vdec" != "none" ]]; then
      in_flow="xtls-rprx-vision"
      ok "Enabled ML-KEM-768 and Vision for the encrypted inbound."
    fi

    new_inbound_json="$(build_inbound_xhttp_tls "$new_port" "$new_tag" "$new_uuid" "$cert_file" "$key_file" "$xpath" "$xmode" "$vdec" "$in_flow" "$new_email")"
  fi

  if [[ -z "$new_inbound_json" ]]; then
    warn "Failed to generate inbound config; operation cancelled. Original configuration retained."
    return 1
  fi

  # New inbounds start without users; never duplicate existing admin/user assignments.
  new_inbound_json="$(jq -c '.settings.clients = []' <<< "$new_inbound_json")" || return 1
  # Commit configuration atomically after JSON validation.
  backup_existing_config
  local updated_config
  updated_config="$(jq --argjson new_in "$new_inbound_json" '.inbounds += [$new_in]' "$CONFIG_PATH")"
  commit_config "$updated_config" || { warn "Failed to write config; original configuration retained."; return 1; }

  # Store paired client encryption separately because Xray rejects unknown fields.
  if [[ "$client_enc_for_meta" != "none" ]]; then
    if meta_set "$new_tag" "client_encryption" "$client_enc_for_meta"; then
      ok "Saved the inbound's paired client encryption string for link exports."
    else
      warn "Failed to save the client encryption string; exported links may not work. Recreate this inbound."
    fi
  fi

  # Users are assigned explicitly; do not synchronize users to new inbounds automatically.
  info "New inbound has no assigned users. Assign a user in user management before connecting."

  validate_and_restart_service
  auto_open_firewall_port "$new_port" "tcp" "$new_tag"
  ok "Created inbound [${new_tag}]."
  list_and_show_nodes
}

# 3. Delete an inbound.
delete_node_interactive() {
  ensure_config_scaffold
  local nodes_json
  nodes_json="$(jq -c '[.inbounds[]? | select(.tag != "api-internal")]' "$CONFIG_PATH" 2>/dev/null || echo '[]')"
  local count
  count="$(echo "$nodes_json" | jq 'length')"

  if ((count == 0)); then
    warn "No managed inbounds available to delete."
    return 0
  fi

  echo
  echo -e "  ${BOLD}Select the inbound to delete${NC}"
  echo "————————————————————————————————————————————————"
  local i=0
  while ((i < count)); do
    local ib_json tag port net sec
    ib_json="$(echo "$nodes_json" | jq -c --argjson idx "$i" '.[$idx]')"
    tag="$(echo "$ib_json" | jq -r '.tag // "node-" + (.port|tostring)')"
    port="$(echo "$ib_json" | jq -r '.port')"
    net="$(echo "$ib_json" | jq -r '.streamSettings.network')"
    sec="$(echo "$ib_json" | jq -r '.streamSettings.security')"
    echo -e "  ${GREEN}$((i+1)).${NC} Tag: ${tag} | Port: ${port} | Protocol: ${net}+${sec}"
    i=$((i + 1))
  done
  echo "————————————————————————————————————————————————"
  echo -e "  ${GREEN}0.${NC} Cancel and return"
  echo "————————————————————————————————————————————————"

  read -r -p "Inbound to delete [1-${count}]: " del_sel || del_sel=""
  [[ "$del_sel" == "0" || -z "$del_sel" ]] && return 0

  if ! [[ "$del_sel" =~ ^[0-9]+$ ]] || ((del_sel < 1 || del_sel > count)); then
    warn "Invalid number; operation cancelled."
    return 0
  fi

  local del_idx=$((del_sel - 1))
  local del_tag del_port
  del_tag="$(echo "$nodes_json" | jq -r --argjson idx "$del_idx" '.[$idx].tag')"
  del_port="$(echo "$nodes_json" | jq -r --argjson idx "$del_idx" '.[$idx].port')"

  read -r -p "Permanently delete inbound [${del_tag}] (port: ${del_port})? [y/N]: " confirm_del || confirm_del="N"
  case "${confirm_del:-N}" in
    y|Y|yes|YES)
      backup_existing_config
      local updated_config
      updated_config="$(jq --arg dtag "$del_tag" '.inbounds = [ .inbounds[] | select(.tag != $dtag) ]' "$CONFIG_PATH")"
      if ! commit_config "$updated_config"; then
        warn "Failed to delete inbound: the new config did not pass validation. Original config retained."
        echo && read -rp "Press Enter to return to the main menu..." _dummy
        return 1
      fi
      meta_del "$del_tag" || warn "Failed to clean inbound metadata; inspect manually: ${META_PATH}"
      validate_and_restart_service
      auto_close_firewall_port "$del_port" "tcp"
      ok "Deleted inbound [${del_tag}] and removed the firewall rule for port ${del_port}."
      echo && read -rp "Press Enter to return to the main menu..." _dummy
      ;;
    *)
      info "Deletion cancelled."
      echo && read -rp "Press Enter to return to the main menu..." _dummy
      ;;
  esac
}

# 4. Edit an inbound.
# Edit only the client-facing public-port sidecar, never Xray listening ports.
set_node_public_port_interactive() {
  local tag="$1" listen="$2" net="$3" sec="$4" value confirm tmp raw data
  if [[ "$net" != "xhttp" || "$sec" != "tls" ]]; then warn "Only XHTTP + TLS is supported"; return 1; fi
  info "Current listener port: $listen. This setting does not create a Cloudflare Origin Rule or restart Xray."
  read -r -p "Public HTTPS port (for example 443; 0 clears): " value || return 0
  if ! [[ "$value" =~ ^[0-9]{1,5}$ ]] || ((10#$value > 65535));then warn "Invalid port";return 1;fi
  value="$((10#$value))"
  [[ "$value" == "0" || "$value" == "$listen" ]] || info "Verify Cloudflare Origin Rules map public port $value to origin port $listen."
  read -r -p "Update exported port metadata? [y/N]: " confirm || confirm="N"
  case "$confirm" in y|Y|yes|YES);; *) info "Cancelled";return 0;; esac
  install -d -m 755 "$CONFIG_DIR" || return 1
  tmp="$(mktemp "$CONFIG_DIR/.freedom-public-ports.XXXXXX")" || return 1
  if [[ -f "$PUBLIC_PORTS_PATH" ]];then
    if ! raw="$(jq -c '.' "$PUBLIC_PORTS_PATH")";then rm -f "$tmp";warn "Metadata is corrupted; refusing overwrite";return 1;fi
  else raw='{"nodes":{}}';fi
  if [[ "$value" == "0" || "$value" == "$listen" ]];then
    data="$(jq -c --arg tag "$tag" '.nodes = (.nodes // {}) | .nodes |= del(.[$tag])' <<< "$raw")" || { rm -f "$tmp";return 1; }
  else
    data="$(jq -c --arg tag "$tag" --argjson listen "$listen" --argjson port "$value" '.nodes = (.nodes // {}) | .nodes[$tag]={listen_port:$listen,public_port:$port}' <<< "$raw")" || { rm -f "$tmp";return 1; }
  fi
  if ! printf "%s\n" "$data" > "$tmp" || ! chmod 600 "$tmp" || ! mv -f "$tmp" "$PUBLIC_PORTS_PATH";then
    rm -f "$tmp";warn "Save failed";return 1
  fi
  ok "Saved: $tag client port $(public_share_port_for_inbound "$tag" "$listen" "$net" "$sec") → listener $listen. Xray was not restarted."
}
modify_node_interactive() {
  ensure_config_scaffold
  local nodes_json
  nodes_json="$(jq -c '[.inbounds[]? | select(.tag != "api-internal")]' "$CONFIG_PATH" 2>/dev/null || echo '[]')"
  local count
  count="$(echo "$nodes_json" | jq 'length')"

  if ((count == 0)); then
    warn "No managed inbounds to edit."
    return 0
  fi

  echo
  echo -e "  ${BOLD}Select the inbound to edit${NC}"
  echo "————————————————————————————————————————————————"
  local i=0
  while ((i < count)); do
    local ib_json tag port net sec
    ib_json="$(echo "$nodes_json" | jq -c --argjson idx "$i" '.[$idx]')"
    tag="$(echo "$ib_json" | jq -r '.tag // "node-" + (.port|tostring)')"
    port="$(echo "$ib_json" | jq -r '.port')"
    net="$(echo "$ib_json" | jq -r '.streamSettings.network')"
    sec="$(echo "$ib_json" | jq -r '.streamSettings.security')"
    echo -e "  ${GREEN}$((i+1)).${NC} Tag: ${tag} | Port: ${port} | Protocol: ${net}+${sec}"
    i=$((i + 1))
  done
  echo "————————————————————————————————————————————————"
  echo -e "  ${GREEN}0.${NC} Cancel and return"
  echo "————————————————————————————————————————————————"

  read -r -p "Choose inbound to edit [1-${count}]: " mod_sel || mod_sel=""
  [[ "$mod_sel" == "0" || -z "$mod_sel" ]] && return 0

  if ! [[ "$mod_sel" =~ ^[0-9]+$ ]] || ((mod_sel < 1 || mod_sel > count)); then
    warn "Invalid number; operation cancelled."
    return 0
  fi

  local mod_idx=$((mod_sel - 1))
  local current_json cur_tag cur_port cur_net
  current_json="$(echo "$nodes_json" | jq -c --argjson idx "$mod_idx" '.[$idx]')"
  cur_tag="$(echo "$current_json" | jq -r '.tag')"
  cur_port="$(echo "$current_json" | jq -r '.port')"
  cur_net="$(echo "$current_json" | jq -r '.streamSettings.network')"

  echo
  info "Editing inbound [${cur_tag}] (port: ${cur_port}, transport: ${cur_net})"
  echo "————————————————————————————————————————————————"
  echo -e "  ${GREEN}1.${NC} Change listener port"
  echo -e "  ${GREEN}2.${NC} UUID belongs to an independent user; change it in User Management"
  echo -e "  ${GREEN}3.${NC} Change REALITY decoy hostname / SNI (REALITY only)"
  echo -e "  ${GREEN}4.${NC} Change XHTTP request path (XHTTP only)"
  echo -e "  ${GREEN}5.${NC} Change XHTTP mode (XHTTP only)"
  echo -e "  ${GREEN}6.${NC} Configure CDN public port (Origin Rule)"
  echo -e "  ${GREEN}0.${NC} Cancel"
  echo "————————————————————————————————————————————————"

  read -r -p "Choose field to change [0-6]: " item_sel || item_sel=""
  case "$item_sel" in
    1)
      prompt_and_check_port "Enter new port" "$cur_port" "$cur_tag"
      local new_p="$PORT_RESULT"
      backup_existing_config
      local updated
      updated="$(jq --arg tag "$cur_tag" --argjson p "$new_p" '
        (.inbounds[] | select(.tag == $tag)).port = $p
      ' "$CONFIG_PATH")"
      commit_config "$updated" || { warn "Failed to update port; original configuration retained."; return 1; }
      validate_and_restart_service
      if [[ "$new_p" != "$cur_port" ]]; then
        auto_close_firewall_port "$cur_port" "tcp"
        auto_open_firewall_port "$new_p" "tcp" "$cur_tag"
      fi
      ok "Changed inbound [${cur_tag}] listener port to ${new_p}; firewall rules updated."
      ;;
    2)
      warn "UUID belongs to the independent user identity; use User Management to change it."
      ;;
    3)
      local new_sni
      read -r -p "New REALITY SNI / decoy hostname: " new_sni || new_sni=""
      new_sni="$(sanitize_domain "$new_sni")"
      [[ -n "$new_sni" ]] || { warn "Domain must not be empty"; return 0; }
      local cur_sec old_sni
      cur_sec="$(echo "$current_json" | jq -r '.streamSettings.security // "none"')"
      if [[ "$cur_sec" != "reality" ]]; then
        warn "Inbound security is ${cur_sec}; only REALITY serverNames can be edited here. TLS certificate selection follows the client SNI, so a server-side serverName edit has no effect."
        return 0
      fi
      old_sni="$(echo "$current_json" | jq -r '.streamSettings.realitySettings.serverNames[0] // empty')"
      if [[ -z "$old_sni" ]]; then
        warn "Inbound has no REALITY serverNames entry; edit the configuration manually."
        return 0
      fi
      if [[ "$old_sni" == "$new_sni" ]]; then
        info "REALITY SNI is unchanged."
        return 0
      fi
      backup_existing_config
      local updated
      updated="$(jq --arg tag "$cur_tag" --arg old "$old_sni" --arg sni "$new_sni" '
        (.inbounds[] | select(.tag == $tag)) |= (
          .streamSettings.realitySettings.serverNames = [$sni] |
          (if (.streamSettings.realitySettings.dest // "") == ($old + ":443") then
             .streamSettings.realitySettings.dest = ($sni + ":443")
           else . end) |
          (if (.streamSettings.realitySettings.target // "") == ($old + ":443") then
             .streamSettings.realitySettings.target = ($sni + ":443")
           else . end)
        )
      ' "$CONFIG_PATH")"
      commit_config "$updated" || { warn "SNI edit failed; original configuration retained."; return 1; }
      validate_and_restart_service
      ok "Changed inbound [${cur_tag}] REALITY SNI to ${new_sni}. Default :443 target/dest were updated; custom targets were preserved."
      ;;
    4)
      if [[ "$cur_net" != "xhttp" ]]; then
        warn "This inbound does not use XHTTP; its path cannot be edited."
        return 0
      fi
      local new_path rand_path
      rand_path="$(random_xhttp_path)"
      read -r -p "New XHTTP path (Enter for random ${rand_path}): " new_path || new_path=""
      new_path="$(sanitize_path "${new_path:-$rand_path}")"
      [[ -n "$new_path" ]] || { warn "Path must not be empty."; return 0; }
      backup_existing_config
      local updated
      updated="$(jq --arg tag "$cur_tag" --arg path "$new_path" '
        (.inbounds[] | select(.tag == $tag)).streamSettings.xhttpSettings.path = $path
      ' "$CONFIG_PATH")"
      commit_config "$updated" || { warn "Failed to update path; original configuration retained."; return 1; }
      validate_and_restart_service
      ok "Changed inbound [${cur_tag}] XHTTP path to ${new_path}."
      ;;
    5)
      if [[ "$cur_net" != "xhttp" ]]; then
        warn "This inbound does not use XHTTP; its mode cannot be edited."
        return 0
      fi
      local cur_mode mode_sel new_mode
      cur_mode="$(echo "$current_json" | jq -r '.streamSettings.xhttpSettings.mode // "auto"')"
      echo "Current XHTTP mode: ${cur_mode}"
      echo "1) auto (adaptive default)"
      echo "2) packet-up (chunked upload; CDN-friendly)"
      echo "3) stream-up"
      echo "4) stream-one"
      read -r -p "Choose mode [1-4, default 1]: " mode_sel || mode_sel="1"
      case "${mode_sel:-1}" in
        2) new_mode="packet-up" ;;
        3) new_mode="stream-up" ;;
        4) new_mode="stream-one" ;;
        *) new_mode="auto" ;;
      esac
      if [[ "$new_mode" == "$cur_mode" ]]; then
        info "XHTTP mode is unchanged (${cur_mode})."
        return 0
      fi
      backup_existing_config
      local updated
      updated="$(jq --arg tag "$cur_tag" --arg mode "$new_mode" '
        (.inbounds[] | select(.tag == $tag)).streamSettings.xhttpSettings.mode = $mode
      ' "$CONFIG_PATH")"
      commit_config "$updated" || { warn "Failed to update XHTTP mode; original configuration retained."; return 1; }
      validate_and_restart_service
      ok "Changed inbound [${cur_tag}] XHTTP mode from ${cur_mode} to ${new_mode}. Clients may need to reimport the link."
      ;;
    6) set_node_public_port_interactive "$cur_tag" "$cur_port" "$cur_net" "$(echo "$current_json" | jq -r '.streamSettings.security // "none"')" ;;
    *) info "Edit cancelled." ;;
  esac
  echo && read -rp "Press Enter to return to the main menu..." _dummy
}

# 5. Service operations.
service_operations_menu() {
  while true; do
    echo
    echo -e "  ${BOLD}Service Operations${NC}"
    echo "————————————————————————————————————————————————"
    echo -e "  ${GREEN}1.${NC} Start Xray"
    echo -e "  ${GREEN}2.${NC} Stop Xray"
    echo -e "  ${GREEN}3.${NC} Restart Xray"
    echo -e "  ${GREEN}4.${NC} View service status"
    echo -e "  ${GREEN}5.${NC} View service logs"
    echo -e "  ${GREEN}0.${NC} Back to main menu"
    echo "————————————————————————————————————————————————"

    read -r -p "Choose action [0-5]: " op_sel || op_sel="0"
    case "${op_sel:-0}" in
      1)
        if ! service_unit_exists "$SERVICE_NAME"; then
          warn "Service ${SERVICE_NAME}.service was not found. Install/repair the Xray service first."
        elif systemctl start "$SERVICE_NAME"; then
          sleep 1
          if systemctl is-active --quiet "$SERVICE_NAME" 2>/dev/null; then
            ok "Xray service started."
          else
            warn "Service is not ready; inspect status and logs."
          fi
        else
          warn "Xray failed to start; inspect status and logs."
        fi
        echo && read -rp "Press Enter to continue..." _dummy
        ;;
      2)
        if systemctl stop "$SERVICE_NAME"; then
          ok "Xray service stopped."
        else
          warn "Unable to stop Xray; check service status."
        fi
        echo && read -rp "Press Enter to continue..." _dummy
        ;;
      3)
        if restart_xray_preserving_traffic; then
          sleep 1
          if systemctl is-active --quiet "$SERVICE_NAME" 2>/dev/null; then
            ok "Xray service restarted."
          else
            warn "Service is not ready after restart; check config and logs."
          fi
        else
          warn "Restart command failed; check config and logs."
        fi
        echo && read -rp "Press Enter to continue..." _dummy
        ;;
      4)
        echo
        systemctl status "$SERVICE_NAME" --no-pager -l || true
        echo && read -rp "Press Enter to continue..." _dummy
        ;;
      5)
        echo
        journalctl -u "$SERVICE_NAME" -n 50 --no-pager || true
        echo && read -rp "Press Enter to continue..." _dummy
        ;;
      0) break ;;
      *) warn "Invalid option; try again." ;;
    esac
  done
}

# ─────────────────────────────────────────────────────────────
# 6. Telegram Bot integration; modeled on the existing standalone core manager.
# ─────────────────────────────────────────────────────────────

get_bot_status_str() {
  if ! [[ -x "$BOT_BIN_PATH" ]]; then
    echo -e "${RED}Not installed${NC}"
    return
  fi
  if ! [[ -f "$BOT_CONFIG_PATH" ]]; then
    echo -e "${YELLOW}Not configured${NC}"
    return
  fi
  if systemctl is-active --quiet "$BOT_SERVICE" 2>/dev/null; then
    echo -e "${GREEN}Running${NC}"
  else
    echo -e "${YELLOW}Stopped${NC}"
  fi
}

# Check once per installed script revision whether to synchronize an existing Bot.
# Old CLI versions re-exec the new script after downloading it; this first-run
# hook means the user does not need to find a separate Bot submenu.
maybe_offer_bot_refresh() {
  [[ -x "$BOT_BIN_PATH" && -f "$BOT_CONFIG_PATH" ]] || return 0
  local installed_script="/usr/local/bin/freedom"
  [[ -f "$installed_script" ]] || return 0
  local script_hash stamp="$CONFIG_DIR/.freedom-bot-sync-script.sha256"
  script_hash="$(sha256sum "$installed_script" | awk '{print $1}')"
  [[ "$script_hash" =~ ^[a-fA-F0-9]{64}$ ]] || return 1
  if [[ "${1:-}" != "--force" && -r "$stamp" && "$(cat "$stamp")" == "$script_hash" ]]; then
    return 0
  fi
  echo
  info "An installed Telegram Bot was detected. The latest binary can be synchronized from the official GitHub Release."
  local ans
  read -r -p "Synchronize the Telegram Bot update? [y/N] (Xray-core will not be changed): " ans || ans="N"
  case "$ans" in
    y|Y|yes|YES)
      if ! install_or_update_freedom_bot; then
        warn "Bot update failed; the old binary was retained or rollback attempted. Retry from the manager."
        return 1
      fi
      ;;
    *) info "Skipped Bot synchronization; use Online Updates to upgrade later.";;
  esac
  if ! (umask 077; printf "%s\n" "$script_hash" > "${stamp}.next" && mv -f "${stamp}.next" "$stamp"); then
    warn "Unable to persist Bot update check marker; the installed version is unaffected."
  fi
}
install_or_update_freedom_bot() {
  echo
  info "Checking architecture and latest Freedom Bot binary..."
  local arch=""
  case "$(uname -m)" in
    x86_64|amd64) arch="amd64" ;;
    aarch64|arm64) arch="arm64" ;;
    *) fatal "Unsupported CPU architecture: $(uname -m)" ;;
  esac

  local bin_name="freedom-bot-linux-${arch}"
  local dl_url="https://github.com/${GITHUB_REPO}/releases/download/bot-latest/${bin_name}"
  info "Target: linux/${arch}. Fetching binary and checksum from the official GitHub Release..."
  local tmp_dir
  tmp_dir="$(mktemp -d)" || return 1
  local tmp_bin="$tmp_dir/$bin_name" sums_file="$tmp_dir/${bin_name}.sha256"
  if ! curl --proto "=https" -fsSL --retry 2 -m 60 "$dl_url" -o "$tmp_bin" || \
     ! curl --proto "=https" -fsSL --retry 2 -m 30 "${dl_url}.sha256" -o "$sums_file"; then
    warn "Unable to download binary and checksum from the official GitHub Release. Third-party mirrors are not trusted."
    rm -rf "$tmp_dir"
    return 1
  fi
  local expected actual
  expected="$(awk -v name="$bin_name" '$2 == name { print $1; exit }' "$sums_file")"
  actual="$(sha256sum "$tmp_bin" | awk '{print $1}')"
  if ! [[ "$expected" =~ ^[0-9a-fA-F]{64}$ ]] || [[ "$expected" != "$actual" ]]; then
    warn "Bot release SHA-256 verification failed; refusing installation."
    rm -rf "$tmp_dir"
    return 1
  fi
  chmod 755 "$tmp_bin"
  local magic bot_ver
  magic="$(head -c 4 "$tmp_bin" 2>/dev/null | od -An -tx1 | tr -d ' \n')"
  if [[ "$magic" != "7f454c46" ]] || ! bot_ver="$("$tmp_bin" -v 2>/dev/null)"; then
    warn "Downloaded file is not an executable ELF Bot."
    rm -rf "$tmp_dir"
    return 1
  fi
  [[ ! -f "$BOT_BIN_PATH" ]] || cp -a "$BOT_BIN_PATH" "${BOT_BIN_PATH}.previous"
  if ! install -m 755 "$tmp_bin" "${BOT_BIN_PATH}.next" || ! mv -f "${BOT_BIN_PATH}.next" "$BOT_BIN_PATH"; then
    warn "Failed to install Bot binary; backup retained."
    rm -rf "$tmp_dir"
    return 1
  fi
  rm -rf "$tmp_dir"
  ok "Freedom Bot is ready: $BOT_BIN_PATH (${bot_ver})"
  if ! "$BOT_BIN_PATH" -c "$BOT_CONFIG_PATH" -local-traffic-install; then
    warn "Unable to install independent freedom-stats collector. Re-run the explicit collector installation after reviewing logs."
  fi

  # After a Bot upgrade, restart its service so the new commands are loaded.
  if [[ -f "$BOT_CONFIG_PATH" ]] && [[ -f "$BOT_SERVICE_FILE" ]]; then
    if systemctl restart "$BOT_SERVICE" && sleep 2 && systemctl is-active --quiet "$BOT_SERVICE"; then
      ok "freedom-bot restarted and is running the updated binary:$("$BOT_BIN_PATH" -v 2>/dev/null)"
    else
      warn "Updated Bot failed startup verification; attempting to restore the previous binary."
      if [[ -s "${BOT_BIN_PATH}.previous" ]]; then
        install -m 755 "${BOT_BIN_PATH}.previous" "$BOT_BIN_PATH" || return 1
        systemctl restart "$BOT_SERVICE" || warn "Previous Bot failed to start; inspect systemctl and journalctl."
      fi
      warn "See logs: journalctl -u freedom-bot -n 50 --no-pager"
      return 1
    fi
  fi
}

configure_freedom_bot_interactive() {
  echo
  echo -e "  ${BOLD}Configure Telegram Bot${NC}"
  echo "Create a bot with @BotFather to obtain your Telegram Bot token."
  echo "Use @userinfobot to find your numeric Telegram user ID for administration."
  echo "─────────────────────────────────────────────────────────────────"

  local cur_token=""
  local cur_admins=""
  local cur_proxy=""
  local cur_hours=12
  local cur_mutations=false
  local cur_updates=false cur_core_updates=false cur_sensitive_backup=false
  local cur_server_host=""
  local bot_base='{}'

  if [[ -f "$BOT_CONFIG_PATH" ]]; then
    # Keep unknown keys (language, language_explicit, future settings) instead
    # of replacing the whole file with the fields prompted below.
    if ! bot_base="$(jq -c 'if type == "object" then . else {} end' "$BOT_CONFIG_PATH" 2>/dev/null)"; then
      warn "Existing $BOT_CONFIG_PATH is not valid JSON; refusing to overwrite it. Repair or remove it first."
      return 1
    fi
    cur_token="$(jq -r '.token // ""' "$BOT_CONFIG_PATH" 2>/dev/null || true)"
    cur_admins="$(jq -r '(.admin_ids // []) | join(",")' "$BOT_CONFIG_PATH" 2>/dev/null || true)"
    cur_proxy="$(jq -r '.socks5_proxy // ""' "$BOT_CONFIG_PATH" 2>/dev/null || true)"
    cur_hours="$(jq -r '.report_hours // 12' "$BOT_CONFIG_PATH" 2>/dev/null || echo 12)"
    cur_mutations="$(jq -r '.allow_mutations // false' "$BOT_CONFIG_PATH" 2>/dev/null || echo false)"
    cur_updates="$(jq -r '.allow_updates // false' "$BOT_CONFIG_PATH" 2>/dev/null || echo false)"
    cur_core_updates="$(jq -r '.allow_core_updates // false' "$BOT_CONFIG_PATH" 2>/dev/null || echo false)"
    cur_sensitive_backup="$(jq -r '.allow_sensitive_backup // false' "$BOT_CONFIG_PATH" 2>/dev/null || echo false)"
    cur_server_host="$(jq -r '.server_host // ""' "$BOT_CONFIG_PATH" 2>/dev/null || true)"
  fi

  local cur_token_display="Not set"
  if [[ -n "$cur_token" ]]; then
    cur_token_display="${cur_token:0:8}...(hidden)"
  fi
  read -r -p "Enter Telegram Bot token [current: ${cur_token_display}]: " in_token || in_token=""
  local final_token="${in_token:-$cur_token}"
  if [[ -z "$final_token" ]]; then
    warn "Bot token is required; configuration not saved."
    return 1
  fi
  if ! [[ "$final_token" =~ ^[0-9]{5,}:[A-Za-z0-9_-]{20,}$ ]]; then
    warn "Invalid Bot token format (expected 123456789:AA...); configuration not saved."
    return 1
  fi

  read -r -p "Enter administrator Telegram user IDs (comma-separated) [current: ${cur_admins:-Not set}]: " in_admins || in_admins=""
  local final_admins="${in_admins:-$cur_admins}"
  local json_admins
  json_admins="$(printf '%s' "$final_admins" | tr ',' '\n' | sed 's/[^0-9]//g' | grep -v '^$' | jq -R . | jq -s 'map(tonumber)')"
  if [[ -z "$json_admins" || "$json_admins" == "[]" ]]; then
    warn "At least one numeric administrator user ID is required; configuration not saved."
    return 1
  fi

  read -r -p "SOCKS5 proxy (optional; e.g., 127.0.0.1:10808) [current: ${cur_proxy:-none}]: " in_proxy || in_proxy=""
  local final_proxy="${in_proxy:-$cur_proxy}"

  # REALITY links embed an address clients dial directly. Automatic discovery
  # fails behind NAT or a proxy, so allow an explicit public IP or hostname.
  read -r -p "Public server address for client links (IP or DNS name; '-' clears) [current: ${cur_server_host:-auto-detect}]: " in_server_host || in_server_host=""
  local final_server_host="${in_server_host:-$cur_server_host}"
  if [[ "$in_server_host" == "-" ]]; then final_server_host=""; fi
  if [[ -n "$final_server_host" ]]; then
    if [[ "$final_server_host" == *"://"* || "$final_server_host" == */* || "$final_server_host" =~ [[:space:]] ]]; then
      warn "Public server address must be a bare IP address or DNS hostname (no scheme, path, or spaces); configuration not saved."
      return 1
    fi
    local host_check="${final_server_host#[}"
    host_check="${host_check%]}"
    if [[ "$host_check" =~ ^[^:]+:[0-9]+$ && "$host_check" != *:*:* ]]; then
      warn "Public server address must not include a port; configuration not saved."
      return 1
    fi
  fi

  read -r -p "Periodic system/traffic report interval (hours, 0 disables) [current: ${cur_hours}]: " in_hours || in_hours=""
  local final_hours="${in_hours:-$cur_hours}"
  if ! [[ "$final_hours" =~ ^[0-9]+$ ]] || ((final_hours > 168)); then
    warn "Report interval must be an integer between 0 and 168 hours; configuration not saved."
    return 1
  fi

  local in_mutations final_mutations="$cur_mutations"
  read -r -p "Allow Telegram administrators to edit Xray users? [y/N; current ${cur_mutations}] (default off): " in_mutations || in_mutations=""
  case "$in_mutations" in
    y|Y|yes|YES) final_mutations=true ;;
    n|N|no|NO) final_mutations=false ;;
    "") ;;
    *) warn "Invalid remote-mutations choice; configuration not saved."; return 1 ;;
  esac

  local in_updates in_core_updates final_updates="$cur_updates" final_core_updates="$cur_core_updates"
  read -r -p "Allow administrators to check and update the manager/Bot? [y/N; current ${cur_updates}]: " in_updates || in_updates=""
  case "$in_updates" in
    y|Y|yes|YES) final_updates=true ;;
    n|N|no|NO) final_updates=false ;;
    "") ;;
    *) warn "Invalid update-permission choice"; return 1 ;;
  esac
  read -r -p "Additionally allow remote Xray-core upgrades? [y/N; current ${cur_core_updates}] (client compatibility may change): " in_core_updates || in_core_updates=""
  case "$in_core_updates" in
    y|Y|yes|YES) final_core_updates=true ;;
    n|N|no|NO) final_core_updates=false ;;
    "") ;;
    *) warn "Invalid core-update permission choice"; return 1 ;;
  esac
  if [[ "$final_updates" != "true" ]]; then final_core_updates=false; fi

  local in_sensitive_backup final_sensitive_backup="$cur_sensitive_backup"
  read -r -p "Allow Telegram to send full Xray backups containing private keys? [y/N; current ${cur_sensitive_backup}] (default off): " in_sensitive_backup || in_sensitive_backup=""
  case "$in_sensitive_backup" in
    y|Y|yes|YES) final_sensitive_backup=true ;;
    n|N|no|NO) final_sensitive_backup=false ;;
    "") ;;
    *) warn "Invalid sensitive-backup permission choice"; return 1 ;;
  esac

  install -d -m 755 "$CONFIG_DIR"

  # Write Bot configuration atomically; never truncate bot.json on a jq failure.
  local bot_tmp="${BOT_CONFIG_PATH}.tmp.$$"
  if ! jq -n \
    --argjson base "$bot_base" \
    --arg token "$final_token" \
    --argjson admins "$json_admins" \
    --arg proxy "$final_proxy" \
    --arg server_host "$final_server_host" \
    --argjson hours "$final_hours" \
    --argjson mutations "$final_mutations" \
    --argjson updates "$final_updates" \
    --argjson core_updates "$final_core_updates" \
    --argjson sensitive_backup "$final_sensitive_backup" \
    --arg xray_cfg "$CONFIG_PATH" \
    --arg xray_bin "$BIN" \
    '$base + {
      token: $token,
      admin_ids: $admins,
      socks5_proxy: $proxy,
      report_hours: $hours,
      allow_mutations: $mutations,
      allow_updates: $updates,
      allow_core_updates: $core_updates,
      allow_sensitive_backup: $sensitive_backup,
      server_host: $server_host,
      xray_config_path: $xray_cfg,
      xray_bin_path: $xray_bin,
      stats_api_server: "127.0.0.1:10085"
    }' > "$bot_tmp"; then
    rm -f "$bot_tmp"
    warn "Unable to generate Bot configuration (invalid input); nothing was saved."
    return 1
  fi
  chmod 600 "$bot_tmp" 2>/dev/null || true
  mv -f "$bot_tmp" "$BOT_CONFIG_PATH"
  ok "Configuration saved to $BOT_CONFIG_PATH"

  # Install the Bot binary when it is not already available.
  if ! [[ -x "$BOT_BIN_PATH" ]]; then
    warn "freedom-bot is not installed; downloading the binary..."
    if ! install_or_update_freedom_bot; then
      warn "Automatic installation failed; retry using Install/Upgrade Bot in the menu."
      return 1
    fi
  fi

  # Create or update the systemd service unit.
  info "Registering systemd service:  $BOT_SERVICE_FILE"
  cat > "$BOT_SERVICE_FILE" <<EOF
[Unit]
Description=freedom-bot - Telegram Bot for freedom.sh & Xray-core
After=network.target xray.service
Wants=xray.service

[Service]
Type=simple
User=root
ExecStart=${BOT_BIN_PATH} -c ${BOT_CONFIG_PATH}
Restart=on-failure
RestartSec=5s
LimitNOFILE=65535
UMask=0077
NoNewPrivileges=true
PrivateTmp=true
ProtectHome=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
ProtectKernelLogs=true
LockPersonality=true
RestrictSUIDSGID=true
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6

[Install]
WantedBy=multi-user.target
EOF

  if ! systemctl daemon-reload; then
    warn "Failed to reload systemd service units; check systemctl."
    return 1
  fi
  systemctl enable "$BOT_SERVICE" >/dev/null 2>&1 || warn "Unable to enable ${BOT_SERVICE} at boot; run systemctl enable ${BOT_SERVICE} manually."
  if ! systemctl restart "$BOT_SERVICE"; then
    warn "freedom-bot failed to start; inspect service logs."
    return 1
  fi
  sleep 1.5

  if systemctl is-active --quiet "$BOT_SERVICE"; then
    ok "freedom-bot started and is running under systemd."
    info "Use /start or /menu in your administrator's Telegram private chat to open the dashboard."
  else
    warn "freedom-bot is not ready; inspect service logs."
    return 1
  fi
  return 0
}

uninstall_freedom_bot_interactive() {
  echo
  warn "Warning: this stops and uninstalls the Freedom Bot service and binary!"
  local ans=""
  read -r -p "Uninstall Freedom Bot? [y/N]: " ans || ans="N"
  case "${ans:-N}" in
    y|Y|yes|YES)
      systemctl stop "$BOT_SERVICE" 2>/dev/null || true
      systemctl disable "$BOT_SERVICE" 2>/dev/null || true
      rm -f "$BOT_SERVICE_FILE" "$BOT_BIN_PATH" "$BOT_CONFIG_PATH"
      systemctl daemon-reload 2>/dev/null || true
      ok "Freedom Bot has been uninstalled."
      return 0
      ;;
    *)
      info "Uninstall cancelled."
      return 1
      ;;
  esac
}

telegram_bot_management_menu() {
  while true; do
    echo
    echo -e "  ${BOLD}Telegram Bot${NC}"
    local cur_st="$(get_bot_status_str)"
    echo -e "  Status: ${cur_st}"
    if [[ -x "$BOT_BIN_PATH" ]]; then
      local b_ver="$("$BOT_BIN_PATH" -v 2>/dev/null || echo "unknown")"
      echo -e "  Version: ${b_ver}"
    fi
    echo "————————————————————————————————————————————————"
    echo -e "  ${GREEN}1.${NC} Configure Bot (permissions / updates / admins / reports)"
    echo -e "  ${GREEN}2.${NC} Install / Upgrade freedom-bot"
    echo -e "  ${GREEN}3.${NC} Start service"
    echo -e "  ${GREEN}4.${NC} Stop service"
    echo -e "  ${GREEN}5.${NC} Restart service"
    echo -e "  ${GREEN}6.${NC} View logs"
    echo -e "  ${GREEN}7.${NC} Uninstall freedom-bot"
    echo -e "  ${GREEN}0.${NC} Back to main menu"
    echo "————————————————————————————————————————————————"

    read -r -p "Choose action [0-7]: " b_choice || b_choice="0"
    case "${b_choice:-0}" in
      1)
        configure_freedom_bot_interactive || warn "Bot configuration was not completed; review the prompts and try again."
        echo && read -rp "Press Enter to continue..." _dummy
        ;;
      2)
        install_or_update_freedom_bot || warn "Install/upgrade did not complete; check network connectivity."
        echo && read -rp "Press Enter to continue..." _dummy
        ;;
      3)
        if systemctl start "$BOT_SERVICE"; then
          sleep 1
          if systemctl is-active --quiet "$BOT_SERVICE"; then
            ok "Started successfully."
          else
            warn "Startup failed; inspect configuration and logs."
          fi
        else
          warn "Start command failed; verify the service is installed."
        fi
        echo && read -rp "Press Enter to continue..." _dummy
        ;;
      4)
        if systemctl stop "$BOT_SERVICE"; then
          ok "Service stopped."
        else
          warn "Failed to stop service; check status."
        fi
        echo && read -rp "Press Enter to continue..." _dummy
        ;;
      5)
        if systemctl restart "$BOT_SERVICE"; then
          sleep 1
          if systemctl is-active --quiet "$BOT_SERVICE"; then
            ok "Restarted successfully."
          else
            warn "Restart failed; inspect config and logs."
          fi
        else
          warn "Restart command failed; verify the service is installed."
        fi
        echo && read -rp "Press Enter to continue..." _dummy
        ;;
      6)
        echo
        journalctl -u "$BOT_SERVICE" -n 50 --no-pager || true
        echo && read -rp "Press Enter to continue..." _dummy
        ;;
      7)
        if uninstall_freedom_bot_interactive; then
          echo && read -rp "Press Enter to return to the main menu..." _dummy
          break
        fi
        echo && read -rp "Press Enter to continue..." _dummy
        ;;
      0) break ;;
      *) warn "Invalid choice. Try again." ;;
    esac
  done
}

# 7. Complete uninstall.
uninstall_xray_completely() {
  echo
  warn "Warning: this stops and removes Xray-core and all configuration files!"
  read -r -p "Permanently uninstall Xray and its configuration? [y/N]: " u_ans || u_ans="N"
  case "${u_ans:-N}" in
    y|Y|yes|YES)
      systemctl stop "$BOT_SERVICE" 2>/dev/null || true
      systemctl disable "$BOT_SERVICE" 2>/dev/null || true
      rm -f "$BOT_SERVICE_FILE" "$BOT_BIN_PATH"

      systemctl stop "$SERVICE_NAME" 2>/dev/null || true
      systemctl disable "$SERVICE_NAME" 2>/dev/null || true
      if [[ -x "/usr/local/bin/xray" ]]; then
        bash -c "$(curl -fsSL https://github.com/XTLS/Xray-install/raw/main/install-release.sh)" @ remove --purge 2>/dev/null || true
      fi
      rm -rf "$CONFIG_DIR" "/etc/systemd/system/xray.service.d"
      systemctl daemon-reload 2>/dev/null || true
      ok "Xray and Freedom Bot have been completely uninstalled."
      exit 0
      ;;
    *) info "Uninstall cancelled." ;;
  esac
}

# One-line service status summary.
show_status() {
  local node_count=0
  local user_count=0
  local svc_text="${RED}Not running${NC}"
  local core_ver
  core_ver="$(get_xray_core_version)"

  if [[ -f "$CONFIG_PATH" ]]; then
    node_count="$(jq '[.inbounds[]? | select(.tag != "api-internal")] | length' "$CONFIG_PATH" 2>/dev/null)" || node_count=0
    [[ "$node_count" =~ ^[0-9]+$ ]] || node_count=0
    user_count="$(get_all_system_users 2>/dev/null | wc -l)" || user_count=0
    [[ "$user_count" =~ ^[0-9]+$ ]] || user_count=0
  fi
  if systemctl is-active --quiet "$SERVICE_NAME" 2>/dev/null; then
    svc_text="${GREEN}Running${NC}"
  fi

  local xui_tip=""
  if [[ -d "/usr/local/x-ui" ]] || service_unit_exists "x-ui"; then
    local xui_st="${YELLOW}Stopped${NC}"
    systemctl is-active --quiet "x-ui" 2>/dev/null && xui_st="${GREEN}Running${NC}"
    xui_tip=" | 3X-UI: ${xui_st}"
  fi

  local bot_st
  bot_st="$(get_bot_status_str)"

  echo -e "  Xray: ${svc_text} (${core_ver}) | $(cli_message status.nodes): ${node_count} | $(cli_message status.users): ${user_count} | Bot: ${bot_st}${xui_tip}"
}

# User-selectable CLI locale. English is the default for every new install.
# Set FREEDOM_LANG=en or FREEDOM_LANG=zh-CN to override the saved preference.
FREEDOM_CLI_LANG_FILE="/usr/local/etc/xray/freedom-cli-language"
if [[ -z "${FREEDOM_LANG:-}" ]]; then
  if [[ -r "$FREEDOM_CLI_LANG_FILE" ]]; then
    IFS= read -r FREEDOM_LANG < "$FREEDOM_CLI_LANG_FILE" || true
  fi
fi
case "${FREEDOM_LANG:-en}" in
  zh|zh-CN|zh-Hans) FREEDOM_LANG="zh-CN" ;;
  *) FREEDOM_LANG="en" ;;
esac

CLI_ZH_LOCALE="/usr/local/etc/xray/locales/zh-CN.json"

cli_message() {
  local id="$1"
  if [[ "$FREEDOM_LANG" == "zh-CN" && -r "$CLI_ZH_LOCALE" ]]; then
    local localized=""
    localized="$(jq -r --arg id "$id" '.[$id] // empty' "$CLI_ZH_LOCALE" 2>/dev/null)" || localized=""
    if [[ -n "$localized" ]]; then
      printf '%s\n' "$localized"
      return 0
    fi
  fi
    case "$id" in
      title) echo "freedom.sh Manager" ;;
      status.nodes) echo "Inbounds" ;;
      status.users) echo "Users" ;;
      exit) echo "Exit" ;;
      nodes) echo "List nodes and export links" ;;
      add) echo "Add inbound" ;;
      delete) echo "Delete inbound" ;;
      modify) echo "Edit inbound" ;;
      users) echo "Manage users and traffic" ;;
      tuning) echo "Network tuning" ;;
      service) echo "Service operations" ;;
      update) echo "Software updates" ;;
      bot) echo "Telegram Bot" ;;
      uninstall) echo "Uninstall Xray" ;;
      language) echo "Language" ;;
      prompt) echo "Choose [0-11]: " ;;
      bye) echo "Exited freedom.sh." ;;
      invalid) echo "Invalid choice. Try again." ;;
      language.prompt) echo "Language: 1) English  2) Simplified Chinese [1/2]: " ;;
      language.saved) echo "Language preference saved." ;;
      *) echo "$id" ;;
    esac

}

install_chinese_cli_locale() {
  local source="" tmp=""
  tmp="$(mktemp)" || return 1
  if [[ -r "./locales/zh-CN.json" ]]; then
    cp -- "./locales/zh-CN.json" "$tmp" || { rm -f "$tmp"; return 1; }
  else
    source="https://raw.githubusercontent.com/u-fw/freedom-xray/main/locales/zh-CN.json"
    if ! curl --proto "=https" -fsSL --connect-timeout 8 --max-time 30 "$source" -o "$tmp"; then
      rm -f "$tmp"
      warn "Unable to download the optional Chinese locale from the official repository."
      return 1
    fi
  fi
  if ! jq -e 'type == "object" and .title and .["language.prompt"] and .["menu.home"] == null' "$tmp" >/dev/null 2>&1; then
    rm -f "$tmp"
    warn "Invalid CLI locale data; keeping the current language."
    return 1
  fi
  install -d -m 700 "$(dirname "$CLI_ZH_LOCALE")" || { rm -f "$tmp"; return 1; }
  install -m 600 "$tmp" "$CLI_ZH_LOCALE" || { rm -f "$tmp"; return 1; }
  rm -f "$tmp"
}

configure_cli_language() {
  local choice=""
  read -r -p "$(cli_message language.prompt)" choice || return 0
  case "$choice" in
    1) FREEDOM_LANG="en" ;;
    2)
      install_chinese_cli_locale || return 1
      FREEDOM_LANG="zh-CN"
      ;;
    *) warn "$(cli_message invalid)"; return 0 ;;
  esac
  install -d -m 700 "$(dirname "$FREEDOM_CLI_LANG_FILE")"
  local temp_file="${FREEDOM_CLI_LANG_FILE}.tmp.$$"
  (umask 077; printf '%s\n' "$FREEDOM_LANG" > "$temp_file" && mv -f "$temp_file" "$FREEDOM_CLI_LANG_FILE") || return 1
  ok "$(cli_message language.saved)"
}

main_menu() {
  while true; do
    echo
    echo -e "  ${BOLD}$(cli_message title) (${SCRIPT_VERSION})${NC}"
    echo "------------------------------------------------"
    show_status
    echo "------------------------------------------------"
    echo -e "  ${GREEN}0.${NC} $(cli_message exit)"
    echo "------------------------------------------------"
    echo -e "  ${GREEN}1.${NC} $(cli_message nodes)"
    echo -e "  ${GREEN}2.${NC} $(cli_message add)"
    echo -e "  ${GREEN}3.${NC} $(cli_message delete)"
    echo -e "  ${GREEN}4.${NC} $(cli_message modify)"
    echo "------------------------------------------------"
    echo -e "  ${GREEN}5.${NC} $(cli_message users)"
    echo -e "  ${GREEN}6.${NC} $(cli_message tuning)"
    echo -e "  ${GREEN}7.${NC} $(cli_message service)"
    echo -e "  ${GREEN}8.${NC} $(cli_message update)"
    echo -e "  ${GREEN}9.${NC} $(cli_message bot)"
    echo "------------------------------------------------"
    echo -e " ${GREEN}10.${NC} $(cli_message uninstall)"
    echo -e " ${GREEN}11.${NC} $(cli_message language)"
    echo "------------------------------------------------"

    read -r -p "$(cli_message prompt)" menu_choice || menu_choice="0"
    case "${menu_choice:-0}" in
      1) list_and_show_nodes || true ;;
      2) locked_config_operation add_new_node_interactive || true ;;
      3) locked_config_operation delete_node_interactive || true ;;
      4) locked_config_operation modify_node_interactive || true ;;
      5) user_management_menu || true ;;
      6) benchmark_and_diagnose_system_tuning || true ;;
      7) service_operations_menu || true ;;
      8) system_update_center_menu || true ;;
      9) telegram_bot_management_menu || true ;;
      10) uninstall_xray_completely || true ;;
      11) configure_cli_language || true ;;
      0) ok "$(cli_message bye)"; exit 0 ;;
      *) warn "$(cli_message invalid)" ;;
    esac
  done
}

main() {
  need_root
  ensure_systemd_available
  install_base_deps
  sync_time_best_effort
  ensure_xray_installed
  locked_config_operation ensure_config_scaffold || fatal "Unable to acquire exclusive lock for configuration initialization."

  # Install the CLI shortcut only on first setup; subsequent updates are explicit.
  # Never allow an old downloaded script copy to silently downgrade /usr/local/bin/freedom.
  if [[ ! -f "/usr/local/bin/freedom" ]]; then
    if [[ -f "$0" && "$0" != "/dev/fd/"* ]]; then
      install -m 755 "$0" /usr/local/bin/freedom || warn "Failed to install the freedom CLI shortcut."
    else
      warn "The script is running from a non-file input. Install it at /usr/local/bin/freedom manually."
    fi
  fi

  # Offer Bot synchronization once when a revised manager script starts.
  # Declining the prompt must not trigger any network request or automatic update.
  maybe_offer_bot_refresh || warn "Bot synchronization check failed; retry via main menu 8 -> 5."

  # If there are no managed inbounds, offer the first inbound setup wizard.
  local initial_nodes=0
  if [[ -f "$CONFIG_PATH" ]]; then
    initial_nodes="$(jq '[.inbounds[]? | select(.tag != "api-internal")] | length' "$CONFIG_PATH" 2>/dev/null)" || initial_nodes=0
    [[ "$initial_nodes" =~ ^[0-9]+$ ]] || initial_nodes=0
  fi
  if ((initial_nodes == 0)); then
    clear || true
    echo -e "  ${BOLD}No Xray inbounds configured; the first-inbound setup wizard will start${NC}"
    locked_config_operation tune_linux_kernel_network
    locked_config_operation add_new_node_interactive || true
  fi

  # Start the full-featured interactive manager menu.
  main_menu
}

main "$@"
