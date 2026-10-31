#!/usr/bin/env bash
# freedom.sh v11.0-modular-manager
# 专为 Xray-core 深度定制的 VLESS (REALITY / XHTTP / TLS) 模块化节点生命周期管理控制台
# 官方规范参考：https://xtls.github.io/config/ 与 https://github.com/xtls/xray-core
#
# 架构原则：
# 1. 100% 专一服务于 Xray-core，完全抛弃第三方内核妥协与兼容包袱。
# 2. 统一配置管理：所有节点聚合维护在单一 Xray 主配置文件中 (inbounds 池)，支持动态增删改查 (CRUD)。
# 3. 基础原子模板原生支持：
#    - [模板 1] VLESS + TCP + REALITY + Vision (Splice 硬件零拷贝，直连极速)
#    - [模板 2] VLESS + XHTTP + REALITY (Beyond REALITY 动态流式分包伪装，抗深度探测，免域名)
#    - [模板 3] VLESS + XHTTP + TLS (Cloudflare CDN 代理穿透，隐藏源站 IP，救砖抗封)
# 4. 智能化运维与安全体系：
#    - Xray 内部端口 + 操作系统全局双层端口冲突主动探测与智能释放
#    - 后量子密码学全面护航 (ML-KEM-768 加密与 ML-DSA-65 数字签名)
#    - 生产级全栈极限调优 (BBR, TCP Fast Open, 32MB 宽带大窗口, 100万句柄, 512K 缓冲池)
#    - 随时调用查看节点看板、导出链接、生成终端二维码，永不丢失配置

set -Eeuo pipefail

RED='\033[31m'
GREEN='\033[32m'
YELLOW='\033[33m'
CYAN='\033[36m'
MAGENTA='\033[35m'
BOLD='\033[1m'
NC='\033[0m'

export PATH="$PATH:/usr/local/bin:/usr/bin:/bin:/usr/local/sbin:/usr/sbin:/sbin"

SCRIPT_VERSION="v13.2-synced-updates"
# 客户端侧 ECH（仅 TLS + XHTTP 导出；不修改 Xray 服务端 TLS）
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
# 节点辅助元数据（VLESS Encryption 客户端串等无法写入 Xray 配置的字段）
META_PATH="$CONFIG_DIR/freedom-meta.json"
STATS_STORE_PATH="$CONFIG_DIR/freedom-stats.json"
BACKUP_FILE=""

BOT_BIN_PATH="/usr/local/bin/freedom-bot"
BOT_CONFIG_PATH="$CONFIG_DIR/bot.json"
BOT_SERVICE="freedom-bot"
BOT_SERVICE_FILE="/etc/systemd/system/freedom-bot.service"

# 所有日志一律输出到 stderr：保证 $(...) 捕获的“纯数据输出”（端口号、证书路径等）
# 绝不会被日志文本污染。这是此前端口/证书获取失败的核心原因之一。
info() { echo -e "${CYAN}[INFO] $*${NC}" >&2; }
ok() { echo -e "${GREEN}[OK] $*${NC}" >&2; }
warn() { echo -e "${YELLOW}[WARN] $*${NC}" >&2; }
fatal() { echo -e "${RED}[ERROR] $*${NC}" >&2; exit 1; }

need_root() {
  [[ "$(id -u)" == "0" ]] || fatal "请使用 root 权限运行：sudo bash $0"
}

ensure_systemd_available() {
  command -v systemctl >/dev/null 2>&1 || fatal "未找到 systemctl；此脚本需要 systemd 管理 xray 服务。"
  # /run/systemd/system 存在即代表 systemd 为 PID 1。相比 systemctl list-units
  # 这种全量 D-Bus 查询，目录探测是微秒级的，不会造成启动卡顿。
  [[ -d /run/systemd/system ]] || fatal "当前环境无法使用 systemd（systemd 未作为 PID 1 运行）。若在容器内运行，请改用支持 systemd 的 VPS 宿主机环境。"
}

service_unit_exists() {
  local svc="$1"
  [[ -f "/etc/systemd/system/${svc}.service" || -f "/lib/systemd/system/${svc}.service" || -f "/usr/lib/systemd/system/${svc}.service" ]] && return 0
  systemctl cat "${svc}.service" >/dev/null 2>&1
}

print_service_diagnostics() {
  local svc="$1"
  warn "${svc}.service 状态："
  systemctl status "$svc" --no-pager -l >&2 || true
  warn "${svc}.service 最近日志："
  journalctl -u "$svc" -n 80 --no-pager >&2 || true
}

restore_backup() {
  [[ -n "${BACKUP_FILE:-}" && -f "$BACKUP_FILE" ]] || return 1
  cp -a "$BACKUP_FILE" "$CONFIG_PATH"
  chmod 640 "$CONFIG_PATH" 2>/dev/null || true
  ok "已恢复备份配置：$BACKUP_FILE -> $CONFIG_PATH"

  if service_unit_exists "$SERVICE_NAME"; then
    info "尝试重启 ${SERVICE_NAME}.service 以恢复旧配置"
    if systemctl restart "$SERVICE_NAME"; then
      sleep 1
      if systemctl is-active --quiet "$SERVICE_NAME"; then
        ok "${SERVICE_NAME}.service 已恢复运行。"
      else
        warn "${SERVICE_NAME}.service 重启后仍未处于 active 状态。"
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
    read -r -p "是否恢复本次备份配置？[y/N]: " ans || ans="N"
    case "${ans:-N}" in
      y|Y|yes|YES) restore_backup || warn "恢复备份失败，请手动检查：$BACKUP_FILE" ;;
      *) warn "已保留当前新配置。备份位置：$BACKUP_FILE" ;;
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
    fatal "找不到支持的包管理器，请手动安装：${pkgs[*]}"
  fi
}

install_port_detection_dep() {
  command -v ss >/dev/null 2>&1 && return 0
  command -v lsof >/dev/null 2>&1 && return 0
  command -v netstat >/dev/null 2>&1 && return 0

  info "安装端口检测依赖：ss"
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
    info "安装基础依赖：${missing[*]}"
    install_packages "${missing[@]}"
  fi

  install_port_detection_dep
}

sync_time_best_effort() {
  timedatectl set-ntp true >/dev/null 2>&1 || true
  if command -v ntpdate >/dev/null 2>&1; then
    # 加硬超时：避免 DNS/网络异常时拖慢脚本启动
    if command -v timeout >/dev/null 2>&1; then
      timeout 5 ntpdate -u pool.ntp.org >/dev/null 2>&1 || true
    else
      ntpdate -u pool.ntp.org >/dev/null 2>&1 || true
    fi
  fi
}

SYSCTL_BACKUP_PATH="$CONFIG_DIR/sysctl_backup.conf"

# 传输层调优是否改写了 config.json（用于决定是否重启 Xray）
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

  info "正在为当前系统原生网络栈参数建立原始快照备份..."
  {
    echo "# freedom.sh 调优前原生系统参数备份快照"
    echo "# 备份生成时间: $(date '+%Y-%m-%d %H:%M:%S')"
    for k in "${keys[@]}"; do
      local v
      v="$(sysctl -n "$k" 2>/dev/null || echo "")"
      if [[ -n "$v" ]]; then
        echo "${k} = ${v}"
      fi
    done
  } > "$SYSCTL_BACKUP_PATH"
  chmod 644 "$SYSCTL_BACKUP_PATH"
  ok "调优前系统原始参数已安全存档至: $SYSCTL_BACKUP_PATH"
}

restore_linux_kernel_tuning() {
  echo
  warn "警告：此操作将彻底移除 freedom.sh 调优配置并恢复机器原始网络参数！"
  read -r -p "确定要还原回调优前的系统参数吗？[y/N]: " r_ans || r_ans="N"
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
        info "正在重启 Xray 以恢复原句柄上限..."
        systemctl restart "$SERVICE_NAME" >/dev/null 2>&1 || warn "Xray 重启失败，句柄上限将在下次重启后恢复。"
      fi

      if [[ -f "$SYSCTL_BACKUP_PATH" ]]; then
        info "正在应用原生快照参数: $SYSCTL_BACKUP_PATH"
        sysctl -p "$SYSCTL_BACKUP_PATH" >/dev/null 2>&1 || true
        ok "内核网络栈参数已成功从原始快照彻底还原！"
      else
        info "正在重载系统默认内核参数..."
        sysctl --system >/dev/null 2>&1 || true
        ok "已重载系统默认设置。"
      fi
      ;;
    *) info "已取消还原操作。" ;;
  esac
}

# 为所有业务 inbound 与 direct 出站补齐 TCP 传输层调优参数。
# 合并时已有自定义值优先，不会被覆盖。
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
      warn "传输层调优配置校验失败，正在回滚原配置。"
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
  info "正在应用内核与 Xray 传输层调优 (BBR, TFO, 缓冲池, 连接保持)..."

  modprobe tcp_bbr >/dev/null 2>&1 || true
  install -d -m 755 /etc/sysctl.d 2>/dev/null || true
  cat <<'EOF' > /etc/sysctl.d/99-xray-tuning.conf
# Xray-core 极致吞吐与超低延迟网络栈优化
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

  # conntrack 由防火墙 (ufw/firewalld) 加载时才存在；提高上限以支撑高并发连接
  if [[ -e /proc/sys/net/netfilter/nf_conntrack_max ]]; then
    echo "net.netfilter.nf_conntrack_max = 262144" >> /etc/sysctl.d/99-xray-tuning.conf
  fi

  sysctl -p /etc/sysctl.d/99-xray-tuning.conf >/dev/null 2>&1 || sysctl --system >/dev/null 2>&1 || true

  patch_xray_sockopt_tuning || warn "Xray 传输层调优未应用（配置保持不变），内核参数已生效。"

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

    # 句柄上限只对新的进程生效；若运行中的 Xray 仍是旧上限则需要重启
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
    info "正在重启 Xray 以应用传输层与句柄调优..."
    if ! systemctl restart "$SERVICE_NAME" >/dev/null 2>&1; then
      warn "Xray 重启失败。"
      if (( XRAY_CONFIG_PATCHED == 1 )) && [[ -n "${BACKUP_FILE:-}" && -f "$BACKUP_FILE" ]]; then
        warn "正在回滚传输层配置..."
        cp -a "$BACKUP_FILE" "$CONFIG_PATH"
        systemctl restart "$SERVICE_NAME" >/dev/null 2>&1 || warn "回滚后 Xray 仍未启动，请检查日志。"
      fi
    fi
  fi

  local cc tfo xray_st="未变更"
  cc="$(sysctl -n net.ipv4.tcp_congestion_control 2>/dev/null || echo 'unknown')"
  tfo="$(sysctl -n net.ipv4.tcp_fastopen 2>/dev/null || echo 'unknown')"
  (( XRAY_CONFIG_PATCHED == 1 )) && xray_st="已应用"
  ok "调优完成：拥塞算法 [${cc}] | TCP Fast Open [${tfo}] | Xray 传输层 [${xray_st}]"
}

benchmark_and_diagnose_system_tuning() {
  while true; do
    echo
    echo -e "  ${BOLD}系统网络调优${NC}"
    echo "————————————————————————————————————————————————"

    local cur_cc cur_qdisc cur_tfo cur_idle cur_rmem cur_wmem cur_somax cur_syn cur_fmax cur_limit
    local cur_notsent cur_portrange
    cur_cc="$(sysctl -n net.ipv4.tcp_congestion_control 2>/dev/null || echo '未知')"
    cur_qdisc="$(sysctl -n net.core.default_qdisc 2>/dev/null || echo '未知')"
    cur_tfo="$(sysctl -n net.ipv4.tcp_fastopen 2>/dev/null || echo '0')"
    cur_idle="$(sysctl -n net.ipv4.tcp_slow_start_after_idle 2>/dev/null || echo '1')"
    cur_rmem="$(sysctl -n net.core.rmem_max 2>/dev/null || echo '0')"
    cur_wmem="$(sysctl -n net.core.wmem_max 2>/dev/null || echo '0')"
    cur_somax="$(sysctl -n net.core.somaxconn 2>/dev/null || echo '0')"
    cur_syn="$(sysctl -n net.ipv4.tcp_max_syn_backlog 2>/dev/null || echo '0')"
    cur_fmax="$(sysctl -n fs.file-max 2>/dev/null || echo '0')"
    cur_notsent="$(sysctl -n net.ipv4.tcp_notsent_lowat 2>/dev/null || echo '0')"
    cur_portrange="$(sysctl -n net.ipv4.ip_local_port_range 2>/dev/null || echo '未知')"

    # 读取运行中 Xray 进程的实际句柄上限，而不是只看配置文件
    cur_limit="$(ulimit -n 2>/dev/null || echo '未知')"
    local diag_pid diag_nofile
    diag_pid="$(systemctl show -p MainPID --value "$SERVICE_NAME" 2>/dev/null || echo 0)"
    if [[ "$diag_pid" =~ ^[0-9]+$ ]] && ((diag_pid > 0)) && [[ -r "/proc/${diag_pid}/limits" ]]; then
      diag_nofile="$(awk '/Max open files/ {print $4}' "/proc/${diag_pid}/limits" 2>/dev/null || true)"
      if [[ -n "$diag_nofile" ]]; then
        cur_limit="${diag_nofile} (运行中)"
      fi
    elif [[ -f "/etc/systemd/system/xray.service.d/10-limits.conf" ]]; then
      cur_limit="1000000 (待重启)"
    fi

    local rmem_mb="约 $((cur_rmem / 1024 / 1024))MB"
    ((cur_rmem < 1048576)) && rmem_mb="约 $((cur_rmem / 1024))KB"
    local wmem_mb="约 $((cur_wmem / 1024 / 1024))MB"
    ((cur_wmem < 1048576)) && wmem_mb="约 $((cur_wmem / 1024))KB"

    # 简洁状态摘要（3X-UI 风格）
    echo -e "  ${BOLD}调优状态${NC}"
    echo "————————————————————————————————————————————————"
    echo "  拥塞控制: ${cur_cc} | 队列规则: ${cur_qdisc} | TCP Fast Open: ${cur_tfo} | 空闲慢启动: ${cur_idle}"
    echo "  读缓冲: ${rmem_mb} | 写缓冲: ${wmem_mb} | 待发上限: ${cur_notsent} | 本地端口: ${cur_portrange}"
    echo "  连接队列: ${cur_somax} / ${cur_syn} | 文件句柄: ${cur_fmax} / ${cur_limit}"
    echo "————————————————————————————————————————————————"

    # 诊断与评级
    local score=0
    [[ "$cur_cc" == "bbr" ]] && ((score += 20))
    [[ "$cur_tfo" == "3" ]] && ((score += 15))
    [[ "$cur_idle" == "0" ]] && ((score += 15))
    ((cur_rmem >= 16777216)) && ((score += 20))
    ((cur_notsent >= 65536)) && ((score += 15))
    [[ "$cur_limit" == *"1000000"* || "$cur_limit" == *"unlimited"* ]] && ((score += 15))

    if ((score >= 85)); then
      ok "评分 ${score}/100：极限性能就绪"
    elif ((score >= 25)); then
      warn "评分 ${score}/100：基础级（仅 BBR，缺少大缓冲与 TFO 双向）"
    else
      warn "评分 ${score}/100：未调优"
    fi

    local bk_info="未建立"
    [[ -f "$SYSCTL_BACKUP_PATH" ]] && bk_info="${GREEN}已备份 (${SYSCTL_BACKUP_PATH})${NC}"
    echo -e "原始参数快照状态: ${bk_info}"
    echo "————————————————————————————————————————————————"
    echo -e "  ${GREEN}1.${NC} 应用全栈调优（内核 + Xray 传输层）"
    echo -e "  ${GREEN}2.${NC} 还原内核调优（Rollback）"
    echo -e "  ${GREEN}3.${NC} 查看调优前参数备份"
    echo -e "  ${GREEN}0.${NC} 返回主菜单"
    echo "————————————————————————————————————————————————"

    read -r -p "请选择操作 [0-3, 默认 0]: " diag_op || diag_op="0"
    case "${diag_op:-0}" in
      1)
        tune_linux_kernel_network
        echo && read -rp "调优已应用，按回车键刷新诊断报告..." _dummy
        ;;
      2)
        restore_linux_kernel_tuning
        echo && read -rp "还原已完成，按回车键刷新诊断报告..." _dummy
        ;;
      3)
        if [[ -f "$SYSCTL_BACKUP_PATH" ]]; then
          echo
          cat "$SYSCTL_BACKUP_PATH"
        else
          warn "尚未生成原始参数备份，首次应用调优时将自动创建快照。"
        fi
        echo && read -rp "按回车键继续..." _dummy
        ;;
      0) break ;;
      *) warn "无效选项。" ;;
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

# 生成 XHTTP 随机路径：128 位熵（等价 UUIDv4），无固定前缀，每次创建都不同
random_xhttp_path() {
  printf '/%s\n' "$(openssl rand -hex 16)"
}

# 防火墙自动化端口放行与管理 (ufw / firewalld / iptables)
auto_open_firewall_port() {
  local port="$1"
  local proto="${2:-tcp}"
  local tag="${3:-xray}"

  [[ -z "$port" ]] && return 0
  local handled=0

  # 1. 检测 ufw (Ubuntu/Debian)
  if command -v ufw >/dev/null 2>&1; then
    if ufw status 2>/dev/null | grep -qi "Status: active"; then
      ufw allow "${port}/${proto}" comment "Xray node ${tag}" >/dev/null 2>&1 && handled=1
      ok "防火墙 (ufw): 已自动放行端口 ${port}/${proto}"
    fi
  fi

  # 2. 检测 firewalld (CentOS/RHEL/AlmaLinux/Rocky)
  if command -v firewall-cmd >/dev/null 2>&1; then
    if systemctl is-active --quiet firewalld 2>/dev/null; then
      firewall-cmd --zone=public --add-port="${port}/${proto}" --permanent >/dev/null 2>&1 && \
      firewall-cmd --reload >/dev/null 2>&1 && handled=1
      ok "防火墙 (firewalld): 已自动放行端口 ${port}/${proto}"
    fi
  fi

  # 3. 检测 iptables 默认阻断并追加放行规则
  if command -v iptables >/dev/null 2>&1; then
    if iptables -L INPUT -n 2>/dev/null | grep -q "Chain INPUT (policy DROP)"; then
      iptables -C INPUT -p "${proto}" --dport "${port}" -j ACCEPT 2>/dev/null || \
      iptables -I INPUT -p "${proto}" --dport "${port}" -j ACCEPT 2>/dev/null && handled=1
      ok "防火墙 (iptables): 已自动追加放行规则 ${port}/${proto}"
    fi
  fi

  if (( handled == 0 )); then
    info "系统防火墙未启用或未检测到活动规则，端口 ${port} 无需额外本地拦截放行。"
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

  # iptables 直连规则同步清理（避免残留放行规则）
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

# 端口冲突双层排查函数：
# 1. 检查 Xray 内部配置是否已使用
# 2. 检查操作系统全局网络监听
check_port_available_strict() {
  local target_port="$1"
  local ignore_tag="${2:-}"

  # 1. 检查 Xray 现有配置文件是否已占用
  if [[ -f "$CONFIG_PATH" ]]; then
    local matched_tag
    matched_tag="$(jq -r --argjson p "$target_port" --arg itag "$ignore_tag" '
      .inbounds[]? | select(.port == $p and (.tag != $itag)) | .tag
    ' "$CONFIG_PATH" 2>/dev/null | head -n1 || true)"

    if [[ -n "$matched_tag" ]]; then
      warn "端口 ${target_port} 已经被 Xray 内部已有节点 [${matched_tag}] 占用，不能重复使用！"
      return 1
    fi
  fi

  # 2. 检查操作系统外部进程监听
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
      warn "检测到端口 ${target_port} 被外部系统服务/进程占用："
      printf '%s\n' "$owners" | sed 's/^/  - /'
      for conflict_name in $non_current; do
        if service_unit_exists "$conflict_name"; then
          read -r -p "是否尝试停止外部服务 ${conflict_name}.service 以释放端口 ${target_port}？[y/N]: " ans || ans="N"
          case "${ans:-N}" in
            y|Y|yes|YES)
              systemctl stop "$conflict_name" || true
              sleep 1
              ;;
            *) return 1 ;;
          esac
        fi
      done
      # 再次确认
      local recheck
      recheck="$(port_listener_summary "$target_port" || true)"
      if [[ -n "$recheck" ]]; then
        local recheck_non
        recheck_non="$(printf '%s\n' "$recheck" | awk '{print $1}' | sed 's#^.*/##' | grep -vxF "$SERVICE_NAME" || true)"
        if [[ -n "$recheck_non" ]]; then
          warn "端口 ${target_port} 仍被占用，请更换端口。"
          return 1
        fi
      fi
    fi
  fi

  return 0
}

# 交互式端口采集：结果写入全局 PORT_RESULT。
# 刻意不使用 $(prompt_and_check_port ...) 捕获：交互函数在子 shell 中执行会
# 导致 read 的输入/提示行为异常，也容易被日志文本污染返回值。
PORT_RESULT=""

prompt_and_check_port() {
  local prompt_text="$1"
  local def_port="$2"
  local ignore_tag="${3:-}"
  local p=""

  while true; do
    read -r -p "${prompt_text} (默认 ${def_port}): " p || fatal "输入已终止。"
    p="${p:-$def_port}"
    if ! valid_port "$p"; then
      warn "端口无效，请输入 1-65535 之间的有效数字。"
      continue
    fi
    # 规范化：去掉前导零，避免 "0443" 之类输入导致 jq --argjson 解析失败
    p=$((10#$p))
    if check_port_available_strict "$p" "$ignore_tag"; then
      PORT_RESULT="$p"
      return 0
    fi
    warn "端口 ${p} 不可用，请更换其他端口。"
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
    info "正在为独立 Xray 生成标准 systemd 守护服务文件..."
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
    systemctl daemon-reload >/dev/null 2>&1 || warn "systemd daemon-reload 失败，请手动执行 systemctl daemon-reload。"
    systemctl enable xray >/dev/null 2>&1 || true
    ok "已创建并启用标准 systemd 服务：/etc/systemd/system/xray.service"
  fi
}

# 通过 XTLS 官方脚本安装/更新 Xray-core（先完整下载再执行，避免 curl 失败时静默执行空脚本）
install_xray_via_official_script() {
  local installer
  installer="$(curl -fsSL --connect-timeout 8 https://github.com/XTLS/Xray-install/raw/main/install-release.sh 2>/dev/null || true)"
  if [[ -z "$installer" ]]; then
    warn "无法下载官方 Xray-install 安装脚本，请检查服务器网络或 DNS。"
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
      info "【系统环境探测】检测到本机已安装 3X-UI 面板，其内部私有 Xray 位于："
      echo -e "  -> ${CYAN}${BOLD}${xui_xray}${NC}"
      warn "说明：3X-UI 将 Xray 私有沙盒化在面板自身目录下，默认并未暴露给 Linux 系统全局 PATH。"
      echo
      echo "请选择核心处理方式："
      echo "1) 安装官方最新独立 Xray-core 系统服务 (推荐，路径标准独立，与 3X-UI 彻底解耦互不干扰) [默认]"
      echo "2) 直接复用 3X-UI 内置的 Xray 二进制核心 (软链接至 /usr/local/bin/xray，秒级免下载)"
      echo "0) 退出脚本"
      read -r -p "请选择 [1/2, 默认 1]: " xui_choice || xui_choice="1"
      case "${xui_choice:-1}" in
        2)
          ln -sf "$xui_xray" /usr/local/bin/xray
          ok "已成功将 3X-UI 内置核心软链接至全局路径 /usr/local/bin/xray！"
          ensure_xray_systemd_service
          ;;
        0) fatal "已退出。" ;;
        *)
          info "正在执行官方 Xray-install 安装/更新..."
          if ! install_xray_via_official_script; then
            fatal "Xray-core 安装失败，请检查网络后重试。"
          fi
          command -v xray >/dev/null 2>&1 || fatal "Xray 安装后仍未找到 xray 命令，请检查网络或权限。"
          systemctl enable xray >/dev/null 2>&1 || true
          ok "Xray-core 安装并启用服务成功！"
          ;;
      esac
    else
      warn "未在当前系统路径中找到 xray 二进制文件。"
      read -r -p "是否使用 XTLS 官方 Xray-install 脚本自动安装最新 Xray-core？[Y/n]: " ans || ans="Y"
      case "${ans:-Y}" in
        n|N|no|NO) fatal "已取消安装，脚本退出。" ;;
        *)
          info "正在执行官方 Xray-install 安装/更新..."
          if ! install_xray_via_official_script; then
            fatal "Xray-core 安装失败，请检查网络后重试。"
          fi
          command -v xray >/dev/null 2>&1 || fatal "Xray 安装后仍未找到 xray 命令，请检查网络或权限。"
          systemctl enable xray >/dev/null 2>&1 || true
          ok "Xray-core 安装并启用服务成功！"
          ;;
      esac
    fi
  fi
  BIN="$(command -v xray)"
  CORE_VERSION_CACHE=""
  ensure_xray_systemd_service
  local ver
  ver="$(get_xray_core_version)"
  ok "已定位 Xray-core：${BIN} (${ver})"
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
    warn "缺少 util-linux flock，已拒绝无锁修改配置。"
    exit 1
  fi
  exec 9>"$CONFIG_DIR/.freedom-config.lock" || exit 1
  if ! flock -w 20 9; then
    warn "已有其他 CLI/Bot 正在修改 Xray 配置，20 秒内未取得锁。请稍后重试。"
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
    info "已创建配置回滚快照：$BACKUP_FILE"
    ls -1t "${CONFIG_PATH}".bak.* 2>/dev/null | tail -n +8 | xargs -r rm -f || true
  fi
}

# 原子化写入配置：先校验 JSON 合法性，再落盘。
# 杜绝 jq 失败时 `> CONFIG_PATH` 把用户配置写成空文件的事故。
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
      warn "无法确定 Xray 运行用户 ${service_user} 的组，拒绝写入可能无法读取的密钥配置。"
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
    warn "配置生成结果为空，已放弃本次写入（原配置保持不变）。"
    return 1
  fi
  if ! printf '%s\n' "$new_json" | jq empty >/dev/null 2>&1; then
    warn "配置生成结果不是合法 JSON，已放弃本次写入（原配置保持不变）。"
    return 1
  fi
  local tmp_file="${CONFIG_PATH}.tmp.$$"
  if ! printf '%s\n' "$new_json" > "$tmp_file"; then
    rm -f "$tmp_file"
    warn "配置临时文件写入失败，原配置保持不变。"
    return 1
  fi
  chmod 640 "$tmp_file" 2>/dev/null || true
  if ! set_xray_config_permissions "$tmp_file"; then
    rm -f "$tmp_file"
    warn "配置文件权限设置失败，原配置未变更。"
    return 1
  fi
  if ! mv -f "$tmp_file" "$CONFIG_PATH"; then
    rm -f "$tmp_file"
    warn "配置替换失败，原配置保持不变。"
    return 1
  fi
  return 0
}

validate_and_restart_service() {
  info "检查 Xray 配置文件语法与内核校验"
  jq empty "$CONFIG_PATH" || fatal_with_rollback "JSON 语法错误：$CONFIG_PATH"

  if ! "$BIN" run -test -c "$CONFIG_PATH" >/tmp/freedom-xray-test.log 2>&1; then
    if ! "$BIN" -test -config "$CONFIG_PATH" >/tmp/freedom-xray-test.log 2>&1; then
      cat /tmp/freedom-xray-test.log >&2
      fatal_with_rollback "Xray 配置校验失败，已中止并提示回滚。"
    fi
  fi
  ok "Xray 配置文件测试校验通过！"

  info "正在平滑重载/重启服务：$SERVICE_NAME"
  if ! service_unit_exists "$SERVICE_NAME"; then
    warn "未找到 systemd 服务 ${SERVICE_NAME}.service，仅更新了配置文件：$CONFIG_PATH"
    return 0
  fi

  if ! systemctl restart "$SERVICE_NAME"; then
    print_service_diagnostics "$SERVICE_NAME"
    fatal_with_rollback "${SERVICE_NAME} 重启命令执行失败"
  fi

  sleep 2
  if ! systemctl is-active --quiet "$SERVICE_NAME"; then
    print_service_diagnostics "$SERVICE_NAME"
    fatal_with_rollback "${SERVICE_NAME} 启动后未处于 active 状态"
  fi
  ok "${SERVICE_NAME}.service 运行正常！"
}

# 密钥与证书辅助生成工具
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

# REALITY X25519 密钥对：结果写入全局 REALITY_PRIV / REALITY_PUB
REALITY_PRIV=""
REALITY_PUB=""

get_or_create_shared_reality_keys() {
  local priv="" pub=""
  if [[ -f "$CONFIG_PATH" ]]; then
    priv="$(jq -r '.inbounds[]? | select(.streamSettings.security == "reality") | .streamSettings.realitySettings.privateKey // empty' "$CONFIG_PATH" 2>/dev/null | head -n1 || true)"
  fi

  if [[ -z "$priv" ]]; then
    local out
    out="$("$BIN" x25519 2>&1)" || fatal "xray x25519 执行失败"
    priv="$(field_after_colon "$out" "PrivateKey|Private key|Private")"
    pub="$(field_after_colon "$out" "Password|PublicKey|Public key|Public")"
  else
    # 从已有私钥反推公钥，保证新旧节点使用同一组密钥
    local out
    out="$("$BIN" x25519 -i "$priv" 2>&1 || true)"
    pub="$(field_after_colon "$out" "Password|PublicKey|Public key|Public")"
    if [[ -z "$pub" ]]; then
      local tmpout
      tmpout="$("$BIN" x25519 2>&1)" || fatal "xray x25519 执行失败"
      priv="$(field_after_colon "$tmpout" "PrivateKey|Private key|Private")"
      pub="$(field_after_colon "$tmpout" "Password|PublicKey|Public key|Public")"
    fi
  fi

  [[ -n "$priv" && -n "$pub" ]] || fatal "无法生成或解析 REALITY X25519 密钥对，请检查 Xray-core 版本。"

  REALITY_PRIV="$priv"
  REALITY_PUB="$pub"
  return 0
}

# 由 REALITY 私钥反推公钥（用于生成分享链接），失败时返回空字符串
derive_reality_public_key() {
  local priv="$1"
  [[ -n "$priv" ]] || { echo ""; return 0; }
  local out
  out="$("$BIN" x25519 -i "$priv" 2>&1 || true)"
  field_after_colon "$out" "Password|PublicKey|Public key|Public"
}

# 判断当前核心是否支持某个子命令（vlessenc / mldsa65 需要较新核心）
xray_supports_subcommand() {
  local sub="$1"
  [[ -n "${BIN:-}" && -x "$BIN" ]] || return 1
  "$BIN" help 2>&1 | grep -qw "$sub"
}

# 生成 VLESS Encryption 的 ML-KEM-768 服务端/客户端字符串对
# 结果写入全局 VLESSENC_DECRYPTION / VLESSENC_ENCRYPTION
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

# 生成 ML-DSA-65 后量子签名种子 / 校验值
# 结果写入全局 MLDSA_SEED / MLDSA_VERIFY
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

# 由种子推导 Verify（pqv），用于分享链接
derive_mldsa65_verify() {
  local seed="$1"
  [[ -n "$seed" ]] || { echo ""; return 0; }
  local out verify
  out="$("$BIN" mldsa65 -i "$seed" 2>&1 || true)"
  verify="$(printf '%s\n' "$out" | awk -F': *' '/^Verify:/ {v=$2} END {print v}' | tr -d '[:space:]')"
  if [[ -z "$verify" ]]; then
    warn "无法从 mldsa65Seed 推导 Verify，导出的链接将缺少 pqv 参数。"
  fi
  printf '%s\n' "$verify"
}

# 自签名 ECC 证书生成（失败自动回退 RSA）
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

# TLS 证书选择：结果写入全局 TLS_CERT_FILE / TLS_KEY_FILE
TLS_CERT_FILE=""
TLS_KEY_FILE=""

setup_tls_certificate() {
  local domain="$1"
  install -d -m 755 "$CERTS_DIR"
  TLS_CERT_FILE=""
  TLS_KEY_FILE=""

  echo
  echo -e "${BOLD}请选择 TLS 证书提供方式（针对域名：${domain}）：${NC}"
  echo "1) 自动生成自签名 ECC 证书 (推荐配合 Cloudflare SSL Full 模式，秒级完成且免 80 端口与域名解析等待)"
  echo "2) 手动指定已有证书与私钥绝对路径"
  echo "3) 使用 acme.sh 自动申请免费证书 (需 80 端口空闲且域名已解析到本机)"

  local cert_file key_file
  while true; do
    read -r -p "证书模式选择 [1/2/3，默认 1]: " cm_sel || fatal "输入已终止。"
    case "${cm_sel:-1}" in
      1)
        info "正在为 ${domain} 生成自签名 ECC 证书 (prime256v1，有效期 10 年)..."
        generate_self_signed_cert "$domain" || fatal "自签名证书生成失败"
        TLS_CERT_FILE="${CERTS_DIR}/${domain}.crt"
        TLS_KEY_FILE="${CERTS_DIR}/${domain}.key"
        ok "自签名证书生成成功：${TLS_CERT_FILE}"
        return 0
        ;;
      2)
        while true; do
          read -r -p "请输入证书 fullchain 文件绝对路径 (.crt/.pem): " cert_file || fatal "输入已终止。"
          [[ -f "$cert_file" ]] && break
          warn "文件不存在：$cert_file，请重新输入。"
        done
        while true; do
          read -r -p "请输入私钥 private key 文件绝对路径 (.key): " key_file || fatal "输入已终止。"
          [[ -f "$key_file" ]] && break
          warn "文件不存在：$key_file，请重新输入。"
        done
        TLS_CERT_FILE="$cert_file"
        TLS_KEY_FILE="$key_file"
        return 0
        ;;
      3)
        local acme_ok=0
        if ! port_listener_summary "80" 2>/dev/null | grep -q .; then
          if [[ ! -x "$HOME/.acme.sh/acme.sh" ]]; then
            info "安装 acme.sh 客户端..."
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
          ok "acme.sh 证书申请成功！"
          return 0
        fi
        warn "80 端口被占用或 acme.sh 申请失败，自动回退为自签名 ECC 证书（配合 Cloudflare SSL Full 模式完全可用）。"
        generate_self_signed_cert "$domain" || fatal "自签名证书生成失败"
        TLS_CERT_FILE="${CERTS_DIR}/${domain}.crt"
        TLS_KEY_FILE="${CERTS_DIR}/${domain}.key"
        return 0
        ;;
      *) warn "请输入 1, 2 或 3。" ;;
    esac
  done
}

ensure_stats_api_scaffold() {
  [[ -f "$CONFIG_PATH" ]] || return 0

  local need_update=0
  if ! jq -e '.stats' "$CONFIG_PATH" >/dev/null 2>&1; then need_update=1; fi
  if ! jq -e '.api' "$CONFIG_PATH" >/dev/null 2>&1; then need_update=1; fi
  if ! jq -e '.inbounds[]? | select(.tag == "api-internal")' "$CONFIG_PATH" >/dev/null 2>&1; then need_update=1; fi
  if ! jq -e '.outbounds[]? | select(.tag == "api")' "$CONFIG_PATH" >/dev/null 2>&1; then need_update=1; fi
  if ! jq -e '.policy.levels."0".statsUserUplink' "$CONFIG_PATH" >/dev/null 2>&1; then need_update=1; fi

  if (( need_update == 1 )); then
    info "检测到配置文件缺少 API/流量统计模块，正在平滑升级 Xray 配置脚手架..."
    local updated
    updated="$(jq '
      .stats = (.stats // {}) |
      .api = (.api // { tag: "api", services: ["StatsService"] }) |
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
        statsUserDownlink: true
      }) |
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
      warn "统计模块 JSON 升级生成失败，原配置保持不变。"
      return 1
    fi
    commit_config "$updated" || return 1
    ok "Xray API 与流量统计脚手架注入完成！"
    if service_unit_exists "$SERVICE_NAME" && systemctl is-active --quiet "$SERVICE_NAME"; then
      systemctl restart "$SERVICE_NAME" >/dev/null 2>&1 || true
    fi
  fi
}

# 基础框架初始化（确保 config.json 存在基础框架：log, stats, api, policy, outbounds, routing）
ensure_config_scaffold() {
  install -d -m 755 "$CONFIG_DIR"
  if [[ -f "$CONFIG_PATH" ]]; then
    # 验证是否为合法 JSON 且包含 inbounds
    if jq -e '.inbounds' "$CONFIG_PATH" >/dev/null 2>&1; then
      ensure_stats_api_scaffold || warn "统计模块初始化失败，流量看板可能不可用（原配置保持不变）。"
      return 0
    fi
    warn "已存在的 $CONFIG_PATH 不是有效的 Xray 配置（缺少 inbounds 或 JSON 损坏），将先备份再重建脚手架。"
    backup_existing_config
  fi

  info "正在初始化 Xray-core 基础工程脚手架配置..."
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
            statsUserDownlink: true
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
    fatal "脚手架配置生成失败（jq 输出非法 JSON）。"
  fi
  mv -f "${CONFIG_PATH}.tmp.$$" "$CONFIG_PATH"
  set_xray_config_permissions "$CONFIG_PATH" || fatal "配置文件权限初始化失败"
  ok "基础架构配置脚手架已就绪。"
}

# 构造具体的 Inbound JSON 对象
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

# ==================== 多用户与实时流量统计体系 ====================

get_all_system_users() {
  [[ -f "$CONFIG_PATH" ]] || return 0
  jq -r '.inbounds[]? | select(.tag != "api-internal" and .settings.clients != null) | .settings.clients[]? | "\(.email // "admin")|\(.id)"' "$CONFIG_PATH" 2>/dev/null | sort -u || true
}

# ==================== 节点辅助元数据 (VLESS Encryption 客户端串) ====================
# Xray 服务端配置里只有 decryption，客户端链接需要配对的 encryption 串。
# 这些信息无法写进 config.json（未知字段会导致校验失败），因此单独存档。
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

# 节点的客户端加密串（无则返回 none）
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

# 服务端启用了 VLESS 加密但元数据缺少配对串时提示（旧版本创建的节点常见）
warn_if_client_encryption_missing() {
  local tag="$1" dec="$2" enc="$3"
  if [[ "$dec" != "none" && "$enc" == "none" ]]; then
    warn "节点 ${tag} 启用了 VLESS 加密，但缺少客户端配对串（可能由旧版本创建）；生成的链接将无法连接，请重新创建该节点。"
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

# ==================== 流量持久化存储体系 (重启无损) ====================

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
xhttp_extra_query() {
  local inbound_json="$1" mode="$2" padding extra
  padding="$(jq -r '.streamSettings.xhttpSettings.xPaddingBytes // "100-1000"' <<< "$inbound_json")" || return 1
  extra="$(jq -nc --arg mode "$mode" --arg padding "$padding" '{mode:$mode,xPaddingBytes:$padding}')" || return 1
  url_encode "$extra"
}
show_user_node_links() {
  local users
  users="$(get_all_system_users)"
  if [[ -z "$users" ]]; then
    warn "当前无任何用户记录。"
    return 0
  fi

  local sel_email="${1:-}" sel_uuid="${2:-}"
  if [[ -z "$sel_email" || -z "$sel_uuid" ]]; then
    local user_arr=()
    local i=0
    echo
    echo -e "  ${BOLD}请选择要导出分享链接的用户${NC}"
    while IFS='|' read -r email uuid; do
      [[ -z "$email" ]] && continue
      user_arr+=("${email}|${uuid}")
      echo "$((i+1))) 用户名: ${email} | UUID: ${uuid}"
      i=$((i + 1))
    done <<< "$users"
    echo "0) 取消并返回"

    local u_sel
    read -r -p "请选择用户序号 [1-${i}]: " u_sel || u_sel=""
    [[ "$u_sel" == "0" || -z "$u_sel" ]] && return 0
    if ! [[ "$u_sel" =~ ^[0-9]+$ ]] || ((u_sel < 1 || u_sel > i)); then
      warn "无效序号。"
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
    warn "当前尚未配置任何业务节点。"
    return 0
  fi

  echo
  echo -e "  ${BOLD}${GREEN}用户 [${sel_email}] 专属节点配置与链接 (共 ${n_count} 个节点)${NC}"
  echo -e "  用户 UUID: ${CYAN}${sel_uuid}${NC}"
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

    # 该用户在此节点上的实际凭证（不同节点可能尚未同步该用户）
    local node_user_id node_flow
    node_user_id="$(echo "$ib_json" | jq -r --arg em "$sel_email" '[.settings.clients[]? | select(.email == $em) | .id] | first // empty' 2>/dev/null || true)"
    node_flow="$(echo "$ib_json" | jq -r --arg em "$sel_email" '[.settings.clients[]? | select(.email == $em) | .flow] | first // empty' 2>/dev/null || true)"
    local link=""
    if [[ -z "$node_user_id" ]]; then
      node_links+=("")
      echo
      echo -e "${BOLD}${CYAN}[节点 $((idx+1))] ${tag}${NC} (端口: ${port} | 协议: ${net}+${sec})"
      warn "该用户尚未同步到此节点，已跳过链接生成。"
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
      [[ -n "$pbk" ]] || warn "节点 ${tag} 无法推导 REALITY 公钥，生成的链接可能不可用。"

      local seed vfy
      seed="$(echo "$ib_json" | jq -r '.streamSettings.realitySettings.mldsa65Seed // ""')"
      if [[ -n "$seed" ]]; then
        vfy="$(derive_mldsa65_verify "$seed")"
        [[ -n "$vfy" ]] && mldsa_pqv="&pqv=$(url_encode "$vfy")"
      fi

      if [[ "$net" == "tcp" ]]; then
        link="vless://${node_user_id}@${host_for_link}:${port}?type=tcp&security=reality&pbk=${pbk}${mldsa_pqv}&fp=chrome&sni=${sni}&sid=${sid}&spx=%2F&flow=${node_flow}&encryption=$(url_encode "$enc_param")#$(url_encode "${tag}-${sel_email%%@*}")"
      elif [[ "$net" == "xhttp" ]]; then
        path="$(echo "$ib_json" | jq -r '.streamSettings.xhttpSettings.path // "/"')"
        mode="$(echo "$ib_json" | jq -r '.streamSettings.xhttpSettings.mode // "auto"')"
        local flow_param=""
        [[ -n "$node_flow" ]] && flow_param="&flow=${node_flow}"
        link="vless://${node_user_id}@${host_for_link}:${port}?type=xhttp&security=reality&pbk=${pbk}${mldsa_pqv}&fp=chrome&sni=${sni}&sid=${sid}&spx=%2F&path=$(url_encode "$path")&mode=${mode}&extra=$(xhttp_extra_query "$ib_json" "$mode")${flow_param}&encryption=$(url_encode "$enc_param")#$(url_encode "${tag}-${sel_email%%@*}")"
      fi
    elif [[ "$sec" == "tls" ]]; then
      local cert_file domain path mode
      cert_file="$(echo "$ib_json" | jq -r '.streamSettings.tlsSettings.certificates[0].certificateFile // ""')"
      domain="$(basename "$cert_file" | sed 's/\.crt$//' | sed 's/\.pem$//')"
      [[ -z "$domain" || "$domain" == "." ]] && domain="$server_ip"
      path="$(echo "$ib_json" | jq -r '.streamSettings.xhttpSettings.path // "/"')"
      mode="$(echo "$ib_json" | jq -r '.streamSettings.xhttpSettings.mode // "auto"')"
      local flow_param=""
      [[ -n "$node_flow" ]] && flow_param="&flow=${node_flow}"
      link="vless://${node_user_id}@${domain}:${port}?type=xhttp&security=tls&sni=${domain}&alpn=h2&fp=chrome&ech=$(url_encode "$XHTTP_CDN_ECH_CONFIG")&path=$(url_encode "$path")&mode=${mode}&extra=$(xhttp_extra_query "$ib_json" "$mode")${flow_param}&encryption=$(url_encode "$enc_param")#$(url_encode "${tag}-${sel_email%%@*}")"
    fi

    node_links+=("$link")
    echo
    echo -e "${BOLD}${CYAN}[节点 $((idx+1))] ${tag}${NC} (端口: ${port} | 协议: ${net}+${sec})"
    if [[ -n "$link" ]]; then
      echo -e "  - ${GREEN}${BOLD}VLESS 专属导入链接：${NC}\n    ${link}"
    fi
    idx=$((idx + 1))
  done

  echo
  read -r -p "是否需要生成某节点的终端二维码？(输入序号 1-${n_count}，直接回车跳过): " qr_idx || qr_idx=""
  if [[ "$qr_idx" =~ ^[0-9]+$ ]] && ((1 <= qr_idx && qr_idx <= n_count)); then
    local sel_link="${node_links[$((qr_idx-1))]}"
    if [[ -n "$sel_link" ]]; then
      if ! command -v qrencode >/dev/null 2>&1; then
        install_packages qrencode || true
      fi
      if command -v qrencode >/dev/null 2>&1; then
        echo
        qrencode -t ANSIUTF8 "$sel_link" || warn "二维码输出异常，请使用链接导入。"
      fi
    fi
  fi
  echo && read -rp "按回车键继续..." _dummy
}

# Shared CLI/TG user directory engine (the Go binary needs no Telegram token for local use).
ensure_user_manager_binary() {
  if [[ -x "$BOT_BIN_PATH" ]]; then return 0; fi
  warn "独立用户管理需要 freedom-bot Go 管理程序。可在 Telegram 机器人菜单先安装（无需启用 Telegram）。"
  return 1
}
list_independent_user_names() {
  "$BOT_BIN_PATH" -c "$BOT_CONFIG_PATH" -local-list-users
}
add_new_user_interactive() {
  ensure_user_manager_binary || return 1
  local username selection node="" total
  read -r -p "新用户名称（默认只创建，不绑定节点）: " username || return 0
  if ! [[ "$username" =~ ^[A-Za-z0-9_.@-]{1,48}$ ]];then warn "用户名不合法。";return 1;fi
  total="$(jq '[.inbounds[]? | select(.protocol=="vless" and .tag!="api-internal")] | length' "$CONFIG_PATH")"
  echo "0) 只创建用户，不绑定节点（推荐）"
  jq -r '[.inbounds[]? | select(.protocol=="vless" and .tag!="api-internal") | .tag] | to_entries[] | "\(.key+1)) \(.value)"' "$CONFIG_PATH"
  echo "a) 绑定全部现有节点（谨慎）"
  read -r -p "绑定节点序号 [默认 0]: " selection || selection="0"
  case "$selection" in
    ""|0) ;;
    a|A) node="all" ;;
    *)
      if ! [[ "$selection" =~ ^[0-9]+$ ]] || ((10#$selection < 1 || 10#$selection > total));then warn "无效序号";return 1;fi
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
  read -r -p "输入需要删除的用户名（回车取消）: " username || return 0
  [[ -n "$username" ]] || return 0
  read -r -p "确认删除用户及全部绑定 $username？[y/N]: " confirm || confirm="N"
  case "$confirm" in
    y|Y|yes|YES) "$BOT_BIN_PATH" -c "$BOT_CONFIG_PATH" -local-user-action delete -local-user "$username" ;;
    *) info "已取消" ;;
  esac
}
reset_traffic_stats_interactive() {
  ensure_stats_api_scaffold || warn "统计模块初始化失败，流量看板可能不可用（原配置保持不变）。"
  if ! service_unit_exists "$SERVICE_NAME" || ! systemctl is-active --quiet "$SERVICE_NAME"; then
    warn "Xray 服务未运行，无法连接 StatsService API。"
    return 0
  fi

  echo
  echo -e "  ${BOLD}清零流量${NC}"
  echo "————————————————————————————————————————————————"
  echo -e "  ${GREEN}1.${NC} 重置所有用户流量"
  echo -e "  ${GREEN}2.${NC} 重置指定用户流量"
  echo -e "  ${GREEN}0.${NC} 取消并返回"
  echo "————————————————————————————————————————————————"
  read -r -p "请选择 [0-2]: " r_sel || r_sel="0"
  case "${r_sel:-0}" in
    1)
      read -r -p "确认清零所有用户的流量？[y/N]: " conf || conf="N"
      case "${conf:-N}" in
        y|Y|yes|YES)
          if ! "$BIN" api statsquery --server=127.0.0.1:10085 -pattern "user>>>" -reset=true >/dev/null 2>&1; then
            warn "Xray 流量计数器重置失败，未修改本地累计数据。"
            return 1
          fi
          # 同步重置本地累计数据，防止下一次查看时旧流量重新出现。
          if [[ -f "$STATS_STORE_PATH" ]]; then
            local reset_all
            reset_all="$(jq '.users |= with_entries(.value = {"total_up":0,"total_down":0,"last_up":0,"last_down":0})' "$STATS_STORE_PATH")" || return 1
            printf '%s\n' "$reset_all" > "$STATS_STORE_PATH"
            chmod 600 "$STATS_STORE_PATH" 2>/dev/null || true
          fi
          ok "已重置 Xray 计数器及本地累计流量！"
          echo && read -rp "按回车键继续..." _dummy
          ;;
        *) info "已取消。" ;;
      esac
      ;;
    2)
      local users
      users="$(get_all_system_users)"
      local u_arr=() i=0
      echo
      echo -e "  ${BOLD}请选择要清零流量的用户${NC}"
      echo "————————————————————————————————————————————————"
      while IFS='|' read -r em id; do
        [[ -z "$em" ]] && continue
        u_arr+=("$em")
        echo -e "  ${GREEN}$((i+1)).${NC} 用户名: ${em}"
        i=$((i + 1))
      done <<< "$users"
      echo "————————————————————————————————————————————————"
      echo -e "  ${GREEN}0.${NC} 取消"
      echo "————————————————————————————————————————————————"
      read -r -p "请输入用户序号 [1-${i}]: " u_idx || u_idx=""
      [[ "$u_idx" == "0" || -z "$u_idx" ]] && return 0
      if [[ "$u_idx" =~ ^[0-9]+$ ]] && ((1 <= u_idx && u_idx <= i)); then
        local target_em="${u_arr[$((u_idx-1))]}"
        "$BIN" api statsquery --server=127.0.0.1:10085 -pattern "user>>>${target_em}>>>" -reset=true >/dev/null 2>&1 || true
        if [[ -f "$STATS_STORE_PATH" ]]; then
          local reset_json
          reset_json="$(jq --arg email "$target_em" '.users[$email] = {"total_up":0, "total_down":0, "last_up":0, "last_down":0}' "$STATS_STORE_PATH" 2>/dev/null || true)"
          [[ -n "$reset_json" ]] && echo "$reset_json" > "$STATS_STORE_PATH"
        fi
        ok "已成功清零用户 [${target_em}] 的流量计数！"
        echo && read -rp "按回车键继续..." _dummy
      else
        warn "序号无效。"
      fi
      ;;
    *) ;;
  esac
}

# 用户详情：无需再次选择同一用户，即可查看链接、改名或更换 UUID。
# 仅对 email + UUID 同时匹配的客户端操作，避免误修改其他用户。
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
  if [[ -z "$users" ]]; then warn "没有用户。"; return 0; fi

  local entries=() email uuid idx=0
  echo
  echo -e "  ${BOLD}选择要管理的用户${NC}"
  while IFS='|' read -r email uuid; do
    [[ -z "$email" || -z "$uuid" ]] && continue
    entries+=("${email}|${uuid}")
    echo "  $((++idx))) ${email} | ${uuid}"
  done <<< "$users"
  echo "  0) 返回"

  local pick
  read -r -p "用户序号 [1-${idx}]: " pick || pick=""
  [[ -z "$pick" || "$pick" == "0" ]] && return 0
  if ! [[ "$pick" =~ ^[0-9]+$ ]] || ((10#$pick < 1 || 10#$pick > idx)); then
    warn "无效用户序号。"
    return 1
  fi
  local selected="${entries[$((10#$pick-1))]}"
  local selected_email="${selected%%|*}" selected_uuid="${selected##*|}"

  while true; do
    echo
    echo -e "  ${BOLD}用户: ${selected_email}${NC}"
    echo "  UUID: ${selected_uuid}"
    echo "  1) 查看该用户所有节点链接 / 二维码"
    echo "  2) 修改该用户标识名称"
    echo "  3) 为该用户重新生成 UUID（所有关联入站）"
    echo "  4) 绑定 / 解绑节点"
    echo "  0) 返回用户列表"
    local action
    read -r -p "请选择 [0-4]: " action || action="0"
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
      *) warn "无效选项。" ;;
    esac
  done
}


manage_user_node_binding_interactive() {
  ensure_user_manager_binary || return 1
  local name="$1" node selection state action confirm
  echo "用户: $name"
  jq -r '[.inbounds[]? | select(.protocol=="vless" and .tag!="api-internal")] | to_entries[] | "\(.key+1)) \(.value.tag) / 端口 \(.value.port)"' "$CONFIG_PATH"
  read -r -p "选择需要调整的节点序号（0 取消）: " selection || return 0
  [[ "$selection" == "0" ]] && return 0
  if ! [[ "$selection" =~ ^[0-9]+$ ]] || ((10#$selection < 1));then warn "节点序号无效。";return 1;fi
  node="$(jq -r --argjson idx "$((10#$selection-1))" '[.inbounds[]? | select(.protocol=="vless" and .tag!="api-internal") | .tag][$idx] // empty' "$CONFIG_PATH")"
  [[ -n "$node" ]] || { warn "节点不存在。";return 1; }
  state="$(jq -r --arg tag "$node" --arg name "$name" '[.inbounds[] | select(.tag==$tag) | .settings.clients[]? | select(.email==$name)] | length' "$CONFIG_PATH")"
  action="assign"
  if ((state>0));then action="unassign";fi
  read -r -p "确认对 $node 执行 $action？[y/N]: " confirm || confirm="N"
  case "$confirm" in
    y|Y|yes|YES) "$BOT_BIN_PATH" -c "$BOT_CONFIG_PATH" -local-user-action "$action" -local-user "$name" -local-node "$node" ;;
    *) info "已取消。" ;;
  esac
}

rename_managed_user() {
  ensure_user_manager_binary || return 1
  local old_name="$1" new_name
  read -r -p "新用户名（回车取消）: " new_name || return 0
  [[ -z "$new_name" ]] && return 0
  "$BOT_BIN_PATH" -c "$BOT_CONFIG_PATH" -local-user-action rename -local-user "$old_name" -local-new-name "$new_name"
}
rotate_managed_user_uuid() {
  ensure_user_manager_binary || return 1
  local name="$1" confirm
  read -r -p "确认轮换 $name 的 UUID？旧客户端链接将失效 [y/N]: " confirm || confirm="N"
  case "$confirm" in
    y|Y|yes|YES) "$BOT_BIN_PATH" -c "$BOT_CONFIG_PATH" -local-user-action rotate -local-user "$name" ;;
    *) info "已取消" ;;
  esac
}

toggle_user_status_interactive() {
  if [[ ! -x "$BOT_BIN_PATH" || ! -f "$BOT_CONFIG_PATH" ]]; then
    warn "停用/恢复需要安装并配置 freedom-bot（内部复用其事务管理器）。"
    return 1
  fi
  echo
  echo "1) 停用活跃用户"
  echo "2) 恢复已停用用户"
  echo "0) 取消"
  local option operation raw_users
  read -r -p "请选择 [0-2]: " option || option="0"
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
  if ((idx==0)); then warn "没有符合条件的用户。"; return 0; fi
  local selection selected confirm
  read -r -p "选择用户序号 [1-${idx}]: " selection || selection=""
  if ! [[ "$selection" =~ ^[0-9]+$ ]] || ((10#$selection < 1 || 10#$selection > idx)); then
    warn "序号无效，已取消。"; return 0
  fi
  selected="${names[$((10#$selection-1))]}"
  read -r -p "确认对 ${selected} 执行 ${operation}？会重启 Xray [y/N]: " confirm || confirm="N"
  case "$confirm" in
    y|Y|yes|YES)
      "$BOT_BIN_PATH" -c "$BOT_CONFIG_PATH" -local-user-action "$operation" -local-user "$selected" || warn "操作失败，请检查本地配置或日志。" ;;
    *) info "已取消。" ;;
  esac
}
user_management_menu() {
  locked_config_operation ensure_stats_api_scaffold || warn "统计模块初始化失败，流量看板可能不可用（原配置保持不变）。"
  while true; do
    local stats_data
    stats_data="$(get_xray_stats_json)"

    local users
    users="$(get_all_system_users)"

    echo
    echo -e "  ${BOLD}用户与流量统计${NC}"
    echo "—————————————————————————————————————————————————————————————————————————————————————"
    printf "${BOLD}%-6s %-20s %-38s %-12s %-12s %-12s${NC}\n" "序号" "用户名 (Email)" "UUID" "上传流量" "下载流量" "总流量"
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
        persisted="$(get_or_update_persistent_user_traffic "$u_email" "$live_up" "$live_down")"
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
      echo "当前暂无用户记录。请添加用户或通过添加新节点自动初始化用户。"
    fi
    echo "—————————————————————————————————————————————————————————————————————————————————————"
    echo -e "  ${GREEN}1.${NC} 查看用户链接 / 二维码"
    echo -e "  ${GREEN}2.${NC} 添加用户"
    echo -e "  ${GREEN}3.${NC} 删除用户"
    echo -e "  ${GREEN}4.${NC} 清零流量"
    echo -e "  ${GREEN}5.${NC} 刷新统计"
    echo -e "  ${GREEN}6.${NC} 选择用户并集中管理（链接 / 改名 / UUID）"
    echo -e "  ${GREEN}7.${NC} 停用 / 恢复用户（需安装 freedom-bot）"
    echo -e "  ${GREEN}0.${NC} 返回主菜单"
    echo "—————————————————————————————————————————————————————————————————————————————————————"

    read -r -p "请选择操作 [0-7]: " u_sel || u_sel="0"
    case "${u_sel:-0}" in
      1) show_user_node_links || true ;;
      2) add_new_user_interactive || true ;;
      3) delete_user_interactive || true ;;
      4) locked_config_operation reset_traffic_stats_interactive || true ;;
      5) ;;
      6) manage_single_user_interactive || true ;;
      7) toggle_user_status_interactive || true ;;
      0) return 0 ;;
      *) warn "无效选项，请重新选择。" ;;
    esac
  done
}

# ==================== 在线检查更新与升级中心 ====================

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
  info "正在从 GitHub main 下载最新 freedom.sh（通过内容对比，而非固定版本标签检测更新）..."
  local tmp_file
  tmp_file="$(mktemp /tmp/freedom_update_XXXXXXXX.sh)" || return 1
  if ! curl --proto "=https" -fsSL --connect-timeout 8 --max-time 50 --retry 2 "$RAW_SCRIPT_URL" -o "$tmp_file"; then
    rm -f "$tmp_file"
    warn "官方 GitHub 下载失败，未修改已安装脚本。不执行第三方镜像返回的 root 脚本。"
    return 1
  fi
  if ! head -n 1 "$tmp_file" | grep -q "^#!/usr/bin/env bash" ||
     ! grep -q '^SCRIPT_VERSION=' "$tmp_file" || ! bash -n "$tmp_file"; then
    rm -f "$tmp_file"
    warn "脚本校验失败：缺少预期标识或 Bash 语法错误；原脚本未修改。"
    return 1
  fi
  local target="/usr/local/bin/freedom"
  if [[ -f "$target" ]] && cmp -s "$tmp_file" "$target"; then
    rm -f "$tmp_file"
    ok "管理脚本内容与 GitHub main 一致。"
    maybe_offer_bot_refresh --force
    return $?
  fi
  if [[ -f "$target" ]]; then
    if ! cp -a "$target" "${target}.previous"; then
      rm -f "$tmp_file"
      warn "旧版管理脚本备份失败，已取消更新。"
      return 1
    fi
  fi
  if ! install -m 755 "$tmp_file" "${target}.next" || ! mv -f "${target}.next" "$target"; then
    rm -f "$tmp_file" "${target}.next"
    warn "安装管理脚本失败；旧版备份保留在 ${target}.previous。"
    return 1
  fi
  rm -f "$tmp_file"
  ok "已安装最新 freedom.sh，旧版备份：${target}.previous"
  info "重新加载新版管理脚本后，会直接询问是否同步升级已安装的 Telegram Bot。Xray-core 不会自动升级。"
  exec "$target"
}

upgrade_xray_core_official() {
  info "正在查询 Xray-core 官方最新版本..."
  local cur_ver="未知"
  if [[ -n "${BIN:-}" ]] && [[ -x "$BIN" ]]; then
    cur_ver="$($BIN version 2>&1 | head -n1 || true)"
    cur_ver="${cur_ver:-未知}"
  fi
  local r_tag
  r_tag="$(get_remote_xray_version)"

  echo -e "当前本地核心版本: ${BOLD}${CYAN}${cur_ver}${NC}"
  echo -e "GitHub 官方最新 Release: ${BOLD}${GREEN}${r_tag:-无法获取(将直接执行官方安装升级)}${NC}"
  echo
  read -r -p "确认调用 XTLS 官方 Xray-install 脚本执行升级？[Y/n]: " c_ans || c_ans="Y"
  case "${c_ans:-Y}" in
    n|N|no|NO) return 0 ;;
  esac

  info "正在运行官方 Xray-install 升级核心..."
  local installer
  installer="$(curl -fsSL --connect-timeout 8 https://github.com/XTLS/Xray-install/raw/main/install-release.sh 2>/dev/null || true)"
  if [[ -z "$installer" ]]; then
    warn "无法下载官方 Xray-install 脚本，请检查网络后重试。"
    return 1
  fi
  if ! bash -c "$installer" @ install; then
    warn "官方 Xray-install 执行失败，请检查网络后重试。"
    return 1
  fi
  local new_bin
  new_bin="$(command -v xray || true)"
  if [[ -z "$new_bin" || ! -x "$new_bin" ]]; then
    warn "升级后仍未找到 xray 可执行文件，请检查安装结果。"
    return 1
  fi
  BIN="$new_bin"
  CORE_VERSION_CACHE=""
  ensure_xray_systemd_service
  validate_and_restart_service

  local new_ver
  new_ver="$("$BIN" version 2>&1 | head -n1 || true)"
  ok "Xray-core 已成功更新至：${new_ver:-未知版本}！"
}

update_rule_assets_interactive() {
  info "正在更新官方 GeoIP / GeoSite 路由规则数据..."
  local installer
  installer="$(curl -fsSL --connect-timeout 8 https://github.com/XTLS/Xray-install/raw/main/install-release.sh 2>/dev/null || true)"
  if [[ -n "$installer" ]] && bash -c "$installer" @ install-geodata; then
    ok "GeoIP / GeoSite 规则数据更新完成。"
  else
    warn "Geo 数据更新异常（网络受限或官方脚本获取失败），继续执行 1stream 资产更新..."
  fi

  info "正在拉取最新构建的 1stream.dat 分流规则资产..."
  install -d -m 755 /usr/local/share/xray
  local stream_url="https://github.com/u-fw/freedom-xray/releases/download/1stream-latest/1stream.dat"
  local tmp_dat="/usr/local/share/xray/1stream.dat.tmp"
  if curl -fsSL --connect-timeout 8 "$stream_url" -o "$tmp_dat" 2>/dev/null; then
    mv -f "$tmp_dat" /usr/local/share/xray/1stream.dat
    chmod 644 /usr/local/share/xray/1stream.dat
    ok "1stream.dat 资产已成功更新到 /usr/local/share/xray/1stream.dat！"
  else
    rm -f "$tmp_dat"
    warn "1stream.dat 下载未完成，请检查网络。"
  fi

  validate_and_restart_service
  ok "规则资产更新流程完毕！"
}

system_update_center_menu() {
  while true; do
    local cur_core
    cur_core="$(get_xray_core_version)"

    echo
    echo -e "  ${BOLD}在线更新${NC}"
    echo -e "  脚本: ${SCRIPT_VERSION} | 核心: ${cur_core}"
    echo "————————————————————————————————————————————————"
    echo -e "  ${GREEN}1.${NC} 更新管理脚本并同步已安装 Bot"
    echo -e "  ${GREEN}2.${NC} 升级 Xray-core"
    echo -e "  ${GREEN}3.${NC} 更新 GeoIP / GeoSite / 1stream.dat"
    echo -e "  ${GREEN}4.${NC} 检查组件版本"
    echo -e "  ${GREEN}5.${NC} 仅更新 Telegram Bot"
    echo -e "  ${GREEN}0.${NC} 返回主菜单"
    echo "————————————————————————————————————————————————"

    read -r -p "请选择升级操作 [0-5]: " up_sel || up_sel="0"
    case "${up_sel:-0}" in
      1)
        update_freedom_script || true
        echo && read -rp "按回车键继续..." _dummy
        ;;
      5)
        if [[ -x "$BOT_BIN_PATH" || -f "$BOT_CONFIG_PATH" ]]; then
          install_or_update_freedom_bot || warn "Bot 更新失败，请检查日志。"
        else
          warn "尚未安装 Bot，请先在 Telegram 机器人菜单进行首次配置。"
        fi
        echo && read -rp "按回车键继续..." _dummy
        ;;
      2)
        upgrade_xray_core_official || true
        echo && read -rp "按回车键继续..." _dummy
        ;;
      3)
        update_rule_assets_interactive || true
        echo && read -rp "按回车键继续..." _dummy
        ;;
      4)
        info "正在获取 GitHub 远端版本数据..."
        local r_sc r_xr
        r_sc="$(get_remote_script_version)"
        r_xr="$(get_remote_xray_version)"
        echo
        echo -e "组件名称           本地运行版本           GitHub 远端最新"
        echo "─────────────────────────────────────────────────────────────────"
        printf "%-18s %-22s %-20s\n" "freedom.sh" "$SCRIPT_VERSION" "${r_sc:-检测失败}"
        printf "%-18s %-22s %-20s\n" "Xray-core" "$cur_core" "${r_xr:-检测失败}"
        echo "─────────────────────────────────────────────────────────────────"
        echo && read -rp "按回车键继续..." _dummy
        ;;
      0) return 0 ;;
      *) warn "无效选项。" ;;
    esac
  done
}

# ==================== 业务管理功能 ====================

# 1. 列表与查看节点详情
list_and_show_nodes() {
  ensure_config_scaffold
  local nodes_json
  nodes_json="$(jq -c '[.inbounds[]? | select(.tag != "api-internal")]' "$CONFIG_PATH" 2>/dev/null || echo '[]')"
  local count
  count="$(echo "$nodes_json" | jq 'length')"

  if ((count == 0)); then
    warn "当前配置文件中尚未添加任何业务节点服务。"
    return 0
  fi

  local server_ip
  server_ip="$(get_public_ip)"
  [[ -n "$server_ip" ]] || server_ip="YOUR_SERVER_IP"
  local host_for_link
  host_for_link="$(url_host "$server_ip")"

  echo
  echo -e "  ${BOLD}${GREEN}当前托管的 Xray-core 业务节点列表 (共 ${count} 个)${NC}"
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
    uuid="$(echo "$ib_json" | jq -r '.settings.clients[0].id // empty')"
    email="$(echo "$ib_json" | jq -r '.settings.clients[0].email // "admin@freedom"')"
    local client_count
    client_count="$(echo "$ib_json" | jq '.settings.clients | length')"

    echo
    echo -e "${BOLD}${CYAN}[节点序号 $((i+1))] Tag: ${tag}${NC}"
    echo "  - 端口: ${port} | 传输层: ${net} | 安全层: ${sec} | 客户端数: ${client_count} 个"
    if [[ -z "$uuid" ]]; then
      info "节点暂未绑定任何用户。请在用户管理 → 用户详情中授权后再导出链接。"
      i=$((i+1))
      continue
    fi
    echo "  - 当前显示用户: ${email} (${uuid})"

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
        link="vless://${uuid}@${host_for_link}:${port}?type=tcp&security=reality&pbk=${pbk}${mldsa_pqv}&fp=chrome&sni=${sni}&sid=${sid}&spx=%2F${flow_param}&encryption=$(url_encode "$enc_param")#$(url_encode "$tag")"
        echo "  - 伪装 SNI: ${sni} | shortId: ${sid} | 流控: ${flow}"
      elif [[ "$net" == "xhttp" ]]; then
        path="$(echo "$ib_json" | jq -r '.streamSettings.xhttpSettings.path // "/"')"
        mode="$(echo "$ib_json" | jq -r '.streamSettings.xhttpSettings.mode // "auto"')"
        flow="$(echo "$ib_json" | jq -r '.settings.clients[0].flow // empty')"
        local dec
        dec="$(echo "$ib_json" | jq -r '.settings.decryption // "none"')"
        local flow_param=""
        if [[ -n "$flow" ]]; then
          flow_param="&flow=${flow}"
        fi
        link="vless://${uuid}@${host_for_link}:${port}?type=xhttp&security=reality&pbk=${pbk}${mldsa_pqv}&fp=chrome&sni=${sni}&sid=${sid}&spx=%2F&path=$(url_encode "$path")&mode=${mode}&extra=$(xhttp_extra_query "$ib_json" "$mode")${flow_param}&encryption=$(url_encode "$enc_param")#$(url_encode "$tag")"
        echo "  - 伪装 SNI: ${sni} | XHTTP 路径: ${path} | 模式: ${mode} | 流控: ${flow:-无} | 解密层: ${dec}"
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
      if [[ -n "$flow" ]]; then
        flow_param="&flow=${flow}"
      fi
      link="vless://${uuid}@${domain}:${port}?type=xhttp&security=tls&sni=${domain}&alpn=h2&fp=chrome&ech=$(url_encode "$XHTTP_CDN_ECH_CONFIG")&path=$(url_encode "$path")&mode=${mode}&extra=$(xhttp_extra_query "$ib_json" "$mode")${flow_param}&encryption=$(url_encode "$enc_param")#$(url_encode "$tag")"
      echo "  - TLS 域名/SNI: ${domain} | XHTTP 路径: ${path} | 模式: ${mode} | 流控: ${flow:-无} (CDN 代理适配)"
      info "ECH 参数已写入客户端链接。该配置需域名/CDN 提供有效 ECH DNS 记录且客户端支持；连接异常时可在客户端清除 ECH。"
    fi

    if [[ "$enc_param" != "none" ]]; then
      echo "  - 后量子加密: ML-KEM-768 (配对客户端 encryption 已写入分享链接)"
    fi

    if [[ -n "$link" ]]; then
      echo -e "  - ${GREEN}${BOLD}VLESS 链接：${NC}\n    ${link}"
    fi

    i=$((i + 1))
  done

  echo
  read -r -p "是否需要生成某节点的终端二维码？(输入序号 1-${count}，直接回车跳过): " qr_idx || qr_idx=""
  if [[ "$qr_idx" =~ ^[0-9]+$ ]] && ((1 <= qr_idx && qr_idx <= count)); then
    local target_i=$((qr_idx - 1))
    local ib_json
    ib_json="$(echo "$nodes_json" | jq -c --argjson idx "$target_i" '.[$idx]')"
    local net sec port uuid tag
    tag="$(echo "$ib_json" | jq -r '.tag // "node-" + (.port|tostring)')"
    port="$(echo "$ib_json" | jq -r '.port')"
    net="$(echo "$ib_json" | jq -r '.streamSettings.network // "tcp"')"
    sec="$(echo "$ib_json" | jq -r '.streamSettings.security // "none"')"
    uuid="$(echo "$ib_json" | jq -r '.settings.clients[0].id // empty')"
    if [[ -z "$uuid" ]]; then
      warn "所选节点尚无任何用户绑定，无法生成可用二维码。请先为该节点授权用户。"
      echo && read -rp "按回车键返回主菜单..." _dummy
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
        target_link="vless://${uuid}@${host_for_link}:${port}?type=tcp&security=reality&pbk=${pbk}${mldsa_pqv}&fp=chrome&sni=${sni}&sid=${sid}&spx=%2F${flow_param}&encryption=$(url_encode "$enc_param")#$(url_encode "$tag")"
      else
        path="$(echo "$ib_json" | jq -r '.streamSettings.xhttpSettings.path // "/"')"
        mode="$(echo "$ib_json" | jq -r '.streamSettings.xhttpSettings.mode // "auto"')"
        local flow_param=""
        if [[ -n "$flow" ]]; then
          flow_param="&flow=${flow}"
        fi
        target_link="vless://${uuid}@${host_for_link}:${port}?type=xhttp&security=reality&pbk=${pbk}${mldsa_pqv}&fp=chrome&sni=${sni}&sid=${sid}&spx=%2F&path=$(url_encode "$path")&mode=${mode}&extra=$(xhttp_extra_query "$ib_json" "$mode")${flow_param}&encryption=$(url_encode "$enc_param")#$(url_encode "$tag")"
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
      if [[ -n "$flow" ]]; then
        flow_param="&flow=${flow}"
      fi
      target_link="vless://${uuid}@${domain}:${port}?type=xhttp&security=tls&sni=${domain}&alpn=h2&fp=chrome&ech=$(url_encode "$XHTTP_CDN_ECH_CONFIG")&path=$(url_encode "$path")&mode=${mode}&extra=$(xhttp_extra_query "$ib_json" "$mode")${flow_param}&encryption=$(url_encode "$enc_param")#$(url_encode "$tag")"
    fi

    if [[ -n "$target_link" ]]; then
      if ! command -v qrencode >/dev/null 2>&1; then
        install_packages qrencode || true
      fi
      if command -v qrencode >/dev/null 2>&1; then
        echo
        qrencode -t ANSIUTF8 "$target_link" || warn "二维码输出异常，请直接使用上面的 URL 链接。"
      fi
    fi
  fi
  echo && read -rp "按回车键返回主菜单..." _dummy
}

# 交互询问 VLESS Encryption 模式：结果写入全局 ASK_VDEC / ASK_VENC。
# 使用全局变量而非 echo 返回，避免与日志输出/子 shell 互相污染。
ASK_VDEC="none"
ASK_VENC="none"

ask_vless_encryption_mode() {
  ASK_VDEC="none"
  ASK_VENC="none"
  local title="${1:-VLESS 后量子加密选项 (ML-KEM-768)：}"
  if ! xray_supports_subcommand vlessenc; then
    info "当前 Xray-core 不支持 vlessenc 子命令，本次节点加密层使用 none。"
    return 0
  fi
  local enc_opt=""
  echo
  echo -e "${BOLD}${title}${NC}"
  echo "1) none (极速默认)"
  echo "2) ML-KEM-768 (抗量子加密)"
  read -r -p "选择加密模式 [1/2, 默认 1]: " enc_opt || enc_opt="1"
  if [[ "${enc_opt:-1}" == "2" ]]; then
    if generate_vlessenc_mlkem_pair; then
      ASK_VDEC="$VLESSENC_DECRYPTION"
      ASK_VENC="$VLESSENC_ENCRYPTION"
      ok "已启用 VLESS ML-KEM-768 后量子加密。"
    else
      warn "vlessenc 输出解析失败，已回退为 none。"
    fi
  fi
}

# 交互询问 REALITY ML-DSA-65 抗量子签名：结果写入全局 ASK_MLDSA_SEED
ASK_MLDSA_SEED=""

ask_mldsa65_signature() {
  ASK_MLDSA_SEED=""
  xray_supports_subcommand mldsa65 || return 0
  local ans=""
  read -r -p "是否启用 REALITY ML-DSA-65 抗量子签名？[y/N]: " ans || ans=""
  case "${ans:-N}" in
    y|Y|yes|YES)
      if generate_mldsa65_pair; then
        ASK_MLDSA_SEED="$MLDSA_SEED"
        ok "已启用 ML-DSA-65 后量子签名。"
      else
        warn "mldsa65 生成失败，本次跳过签名（不影响节点创建）。"
      fi
      ;;
  esac
}

# 2. 添加新节点 (原子模板 1/2/3 扩展)
add_new_node_interactive() {
  ensure_config_scaffold
  echo
  echo -e "  ${BOLD}添加节点${NC}"
  echo "————————————————————————————————————————————————"
  echo -e "  ${GREEN}1.${NC} VLESS + TCP + REALITY + Vision（直连）"
  echo -e "  ${GREEN}2.${NC} VLESS + XHTTP + REALITY（免域名）"
  echo -e "  ${GREEN}3.${NC} VLESS + XHTTP + TLS（CDN）"
  echo -e "  ${GREEN}0.${NC} 返回主菜单"
  echo "————————————————————————————————————————————————"

  local t_sel
  read -r -p "请选择模板序号 [1-3, 默认 1]: " t_sel || t_sel="1"
  t_sel="${t_sel:-1}"
  [[ "$t_sel" == "0" ]] && return 0

  # 1. 端口收集与严格冲突排查（结果经全局 PORT_RESULT 返回，避免命令替换污染）
  local new_port
  case "$t_sel" in
    1) prompt_and_check_port "请输入新节点监听端口" "443" ;;
    2) prompt_and_check_port "请输入新节点监听端口" "8443" ;;
    3) prompt_and_check_port "请输入新节点监听端口 (若过 CDN 建议 443/2053/8443 等)" "2053" ;;
    *) warn "无效选项，返回主菜单。"; return 0 ;;
  esac
  new_port="$PORT_RESULT"

  # 2. 收集 Tag（严格去重，避免重复 tag 导致 Xray 校验失败）
  local default_tag="node-${new_port}"
  case "$t_sel" in
    1) default_tag="TCP-REALITY-${new_port}" ;;
    2) default_tag="XHTTP-REALITY-${new_port}" ;;
    3) default_tag="XHTTP-TLS-CDN-${new_port}" ;;
  esac
  local new_tag
  read -r -p "节点标签 Tag (默认 ${default_tag}): " new_tag || new_tag=""
  new_tag="${new_tag:-$default_tag}"
  if jq -e --arg t "$new_tag" '[.inbounds[]? | select(.tag == $t)] | length > 0' "$CONFIG_PATH" >/dev/null 2>&1; then
    warn "节点标签 [${new_tag}] 已存在，请使用其他标签后重试。"
    return 1
  fi

  # 新节点不隐式授予任何用户访问权限。仅生成模板所需的临时 UUID；
  # 提交前会清空 clients，后续由独立用户目录显式绑定。
  local new_uuid new_email
  new_uuid="$("$BIN" uuid 2>/dev/null || cat /proc/sys/kernel/random/uuid)"
  new_email="unbound-template"

  local new_inbound_json=""
  local client_enc_for_meta="none"

  if [[ "$t_sel" == "1" ]]; then
    # TCP + REALITY
    local sni
    read -r -p "REALITY 伪装域名/SNI (默认 ${DEFAULT_SNI}): " sni || sni=""
    sni="$(sanitize_domain "${sni:-$DEFAULT_SNI}")"

    get_or_create_shared_reality_keys
    local priv="$REALITY_PRIV"
    local sid
    sid="$(openssl rand -hex 8 | tr -d '[:space:]')"

    ask_vless_encryption_mode "VLESS 后量子加密选项 (ML-KEM-768)："
    local vdec="$ASK_VDEC"
    client_enc_for_meta="$ASK_VENC"

    ask_mldsa65_signature
    local mldsa_seed="$ASK_MLDSA_SEED"

    new_inbound_json="$(build_inbound_tcp_reality "$new_port" "$new_tag" "$new_uuid" "$sni" "$priv" "$sid" "$vdec" "$mldsa_seed" "$new_email")"

  elif [[ "$t_sel" == "2" ]]; then
    # XHTTP + REALITY
    local sni
    read -r -p "REALITY 伪装域名/SNI (默认 ${DEFAULT_SNI}): " sni || sni=""
    sni="$(sanitize_domain "${sni:-$DEFAULT_SNI}")"

    get_or_create_shared_reality_keys
    local priv="$REALITY_PRIV"
    local sid
    sid="$(openssl rand -hex 8 | tr -d '[:space:]')"

    local rand_path
    rand_path="$(random_xhttp_path)"
    local xpath
    read -r -p "XHTTP 请求路径 (直接回车使用随机 ${rand_path}): " xpath || xpath=""
    xpath="$(sanitize_path "${xpath:-$rand_path}")"

    echo "XHTTP 流控模式：1) auto (默认自适应)  2) packet-up (分块上传)  3) stream-up  4) stream-one"
    local xmode_sel
    read -r -p "选择模式 [1-4，默认 1]: " xmode_sel || xmode_sel="1"
    local xmode="auto"
    case "${xmode_sel:-1}" in
      2) xmode="packet-up" ;;
      3) xmode="stream-up" ;;
      4) xmode="stream-one" ;;
      *) xmode="auto" ;;
    esac

    ask_vless_encryption_mode "VLESS 后量子加密选项 (ML-KEM-768)："
    local vdec="$ASK_VDEC" in_flow=""
    client_enc_for_meta="$ASK_VENC"
    if [[ "$vdec" != "none" ]]; then
      in_flow="xtls-rprx-vision"
      ok "已启用 ML-KEM-768，并为该节点启用 Vision 穿透流控。"
    else
      info "未启用 VLESS Encryption：不启用 Vision。"
    fi

    ask_mldsa65_signature
    local mldsa_seed="$ASK_MLDSA_SEED"

    new_inbound_json="$(build_inbound_xhttp_reality "$new_port" "$new_tag" "$new_uuid" "$sni" "$priv" "$sid" "$xpath" "$xmode" "$mldsa_seed" "$vdec" "$in_flow" "$new_email")"

  elif [[ "$t_sel" == "3" ]]; then
    # XHTTP + TLS (CDN)
    local domain
    while true; do
      read -r -p "请输入用于 CDN 代理的真实解析域名 (如 cdn.example.com): " domain || domain=""
      domain="$(sanitize_domain "${domain:-}")"
      [[ -n "$domain" ]] && break
      warn "过 CDN 模式必须提供真实域名。"
    done

    setup_tls_certificate "$domain"
    local cert_file="$TLS_CERT_FILE" key_file="$TLS_KEY_FILE"
    if [[ -z "$cert_file" || -z "$key_file" || ! -f "$cert_file" || ! -f "$key_file" ]]; then
      warn "TLS 证书获取失败（证书或私钥文件缺失），操作已取消。"
      return 1
    fi

    local rand_path
    rand_path="$(random_xhttp_path)"
    local xpath
    read -r -p "XHTTP 请求路径 (直接回车使用随机 ${rand_path}): " xpath || xpath=""
    xpath="$(sanitize_path "${xpath:-$rand_path}")"

    echo "XHTTP 流控模式：1) auto (默认推荐)  2) packet-up (Cloudflare CDN 100% 穿透兼容)"
    local xmode_sel
    read -r -p "选择模式 [1/2，默认 1]: " xmode_sel || xmode_sel="1"
    local xmode="auto"
    [[ "${xmode_sel:-1}" == "2" ]] && xmode="packet-up"

    ask_vless_encryption_mode "VLESS 后量子加密选项 (ML-KEM-768)："
    local vdec="$ASK_VDEC" in_flow=""
    client_enc_for_meta="$ASK_VENC"
    if [[ "$vdec" != "none" ]]; then
      in_flow="xtls-rprx-vision"
      ok "已启用 ML-KEM-768，并为该节点启用 Vision 穿透流控。"
    fi

    new_inbound_json="$(build_inbound_xhttp_tls "$new_port" "$new_tag" "$new_uuid" "$cert_file" "$key_file" "$xpath" "$xmode" "$vdec" "$in_flow" "$new_email")"
  fi

  if [[ -z "$new_inbound_json" ]]; then
    warn "节点配置生成失败，操作已取消（原配置未改动）。"
    return 1
  fi

  # 新节点默认没有客户端，不能把 admin/所有旧用户复制过去。
  new_inbound_json="$(jq -c '.settings.clients = []' <<< "$new_inbound_json")" || return 1
  # 写入配置文件（原子提交 + JSON 校验，绝不覆盖成空文件）
  backup_existing_config
  local updated_config
  updated_config="$(jq --argjson new_in "$new_inbound_json" '.inbounds += [$new_in]' "$CONFIG_PATH")"
  commit_config "$updated_config" || { warn "写入配置失败，已保留原始配置。"; return 1; }

  # 记录客户端配对 encryption（Xray 配置不允许未知字段）
  if [[ "$client_enc_for_meta" != "none" ]]; then
    if meta_set "$new_tag" "client_encryption" "$client_enc_for_meta"; then
      ok "已保存该节点的客户端加密串（用于生成可用的分享链接）。"
    else
      warn "客户端加密串保存失败，分享链接可能无法直接使用，请重新生成节点。"
    fi
  fi

  # 不再自动跨节点同步用户：管理员在用户管理中显式绑定。
  info "新节点当前没有任何用户，需要从用户管理中绑定账号后才能连接。"

  validate_and_restart_service
  auto_open_firewall_port "$new_port" "tcp" "$new_tag"
  ok "新节点 [${new_tag}] 添加成功！"
  list_and_show_nodes
}

# 3. 删除已有节点
delete_node_interactive() {
  ensure_config_scaffold
  local nodes_json
  nodes_json="$(jq -c '[.inbounds[]? | select(.tag != "api-internal")]' "$CONFIG_PATH" 2>/dev/null || echo '[]')"
  local count
  count="$(echo "$nodes_json" | jq 'length')"

  if ((count == 0)); then
    warn "当前无任何业务节点可供删除。"
    return 0
  fi

  echo
  echo -e "  ${BOLD}请选择要删除的业务节点序号${NC}"
  echo "————————————————————————————————————————————————"
  local i=0
  while ((i < count)); do
    local ib_json tag port net sec
    ib_json="$(echo "$nodes_json" | jq -c --argjson idx "$i" '.[$idx]')"
    tag="$(echo "$ib_json" | jq -r '.tag // "node-" + (.port|tostring)')"
    port="$(echo "$ib_json" | jq -r '.port')"
    net="$(echo "$ib_json" | jq -r '.streamSettings.network')"
    sec="$(echo "$ib_json" | jq -r '.streamSettings.security')"
    echo -e "  ${GREEN}$((i+1)).${NC} 标签: ${tag} | 端口: ${port} | 协议: ${net}+${sec}"
    i=$((i + 1))
  done
  echo "————————————————————————————————————————————————"
  echo -e "  ${GREEN}0.${NC} 取消并返回"
  echo "————————————————————————————————————————————————"

  read -r -p "请输入要删除的节点序号 [1-${count}]: " del_sel || del_sel=""
  [[ "$del_sel" == "0" || -z "$del_sel" ]] && return 0

  if ! [[ "$del_sel" =~ ^[0-9]+$ ]] || ((del_sel < 1 || del_sel > count)); then
    warn "序号无效，操作取消。"
    return 0
  fi

  local del_idx=$((del_sel - 1))
  local del_tag del_port
  del_tag="$(echo "$nodes_json" | jq -r --argjson idx "$del_idx" '.[$idx].tag')"
  del_port="$(echo "$nodes_json" | jq -r --argjson idx "$del_idx" '.[$idx].port')"

  read -r -p "确认彻底删除节点 [${del_tag}] (端口: ${del_port})？[y/N]: " confirm_del || confirm_del="N"
  case "${confirm_del:-N}" in
    y|Y|yes|YES)
      backup_existing_config
      local updated_config
      updated_config="$(jq --arg dtag "$del_tag" '.inbounds = [ .inbounds[] | select(.tag != $dtag) ]' "$CONFIG_PATH")"
      if ! commit_config "$updated_config"; then
        warn "删除节点失败：配置写入未通过校验，原配置保持不变。"
        echo && read -rp "按回车键返回主菜单..." _dummy
        return 1
      fi
      meta_del "$del_tag" || warn "节点元数据清理失败，可手动检查：${META_PATH}"
      validate_and_restart_service
      auto_close_firewall_port "$del_port" "tcp"
      ok "节点 [${del_tag}] 已成功移除，防火墙端口 ${del_port} 已清理。"
      echo && read -rp "按回车键返回主菜单..." _dummy
      ;;
    *)
      info "已取消删除。"
      echo && read -rp "按回车键返回主菜单..." _dummy
      ;;
  esac
}

# 4. 修改节点配置
modify_node_interactive() {
  ensure_config_scaffold
  local nodes_json
  nodes_json="$(jq -c '[.inbounds[]? | select(.tag != "api-internal")]' "$CONFIG_PATH" 2>/dev/null || echo '[]')"
  local count
  count="$(echo "$nodes_json" | jq 'length')"

  if ((count == 0)); then
    warn "当前无任何业务节点可供修改。"
    return 0
  fi

  echo
  echo -e "  ${BOLD}请选择要修改的业务节点序号${NC}"
  echo "————————————————————————————————————————————————"
  local i=0
  while ((i < count)); do
    local ib_json tag port net sec
    ib_json="$(echo "$nodes_json" | jq -c --argjson idx "$i" '.[$idx]')"
    tag="$(echo "$ib_json" | jq -r '.tag // "node-" + (.port|tostring)')"
    port="$(echo "$ib_json" | jq -r '.port')"
    net="$(echo "$ib_json" | jq -r '.streamSettings.network')"
    sec="$(echo "$ib_json" | jq -r '.streamSettings.security')"
    echo -e "  ${GREEN}$((i+1)).${NC} 标签: ${tag} | 端口: ${port} | 协议: ${net}+${sec}"
    i=$((i + 1))
  done
  echo "————————————————————————————————————————————————"
  echo -e "  ${GREEN}0.${NC} 取消并返回"
  echo "————————————————————————————————————————————————"

  read -r -p "请输入要修改的节点序号 [1-${count}]: " mod_sel || mod_sel=""
  [[ "$mod_sel" == "0" || -z "$mod_sel" ]] && return 0

  if ! [[ "$mod_sel" =~ ^[0-9]+$ ]] || ((mod_sel < 1 || mod_sel > count)); then
    warn "序号无效，操作取消。"
    return 0
  fi

  local mod_idx=$((mod_sel - 1))
  local current_json cur_tag cur_port cur_net
  current_json="$(echo "$nodes_json" | jq -c --argjson idx "$mod_idx" '.[$idx]')"
  cur_tag="$(echo "$current_json" | jq -r '.tag')"
  cur_port="$(echo "$current_json" | jq -r '.port')"
  cur_net="$(echo "$current_json" | jq -r '.streamSettings.network')"

  echo
  info "当前正在修改节点 [${cur_tag}] (端口: ${cur_port} | 传输层: ${cur_net})"
  echo "————————————————————————————————————————————————"
  echo -e "  ${GREEN}1.${NC} 修改监听端口"
  echo -e "  ${GREEN}2.${NC} UUID 属于用户身份；请前往「用户管理」调整"
  echo -e "  ${GREEN}3.${NC} 修改伪装域名 / SNI"
  echo -e "  ${GREEN}4.${NC} 修改 XHTTP 访问路径 (仅限 xhttp 节点)"
  echo -e "  ${GREEN}0.${NC} 取消"
  echo "————————————————————————————————————————————————"

  read -r -p "请选择修改项目 [0-4]: " item_sel || item_sel=""
  case "$item_sel" in
    1)
      prompt_and_check_port "请输入新端口" "$cur_port" "$cur_tag"
      local new_p="$PORT_RESULT"
      backup_existing_config
      local updated
      updated="$(jq --arg tag "$cur_tag" --argjson p "$new_p" '
        (.inbounds[] | select(.tag == $tag)).port = $p
      ' "$CONFIG_PATH")"
      commit_config "$updated" || { warn "端口修改写入失败，原配置保持不变。"; return 1; }
      validate_and_restart_service
      if [[ "$new_p" != "$cur_port" ]]; then
        auto_close_firewall_port "$cur_port" "tcp"
        auto_open_firewall_port "$new_p" "tcp" "$cur_tag"
      fi
      ok "节点 [${cur_tag}] 端口已修改为: ${new_p} (防火墙已同步更新)"
      ;;
    2)
      warn "UUID 属于独立用户身份，请到用户管理中调整。"
      ;;
    3)
      local new_sni
      read -r -p "请输入新的 SNI / 伪装域名: " new_sni || new_sni=""
      new_sni="$(sanitize_domain "$new_sni")"
      [[ -n "$new_sni" ]] || { warn "域名不能为空"; return 0; }
      local cur_sec
      cur_sec="$(echo "$current_json" | jq -r '.streamSettings.security // "none"')"
      if [[ "$cur_sec" != "reality" && "$cur_sec" != "tls" ]]; then
        warn "当前节点安全层为 ${cur_sec}，不存在可修改的 SNI / 伪装域名。"
        return 0
      fi
      backup_existing_config
      local updated
      updated="$(jq --arg tag "$cur_tag" --arg sni "$new_sni" '
        (.inbounds[] | select(.tag == $tag)) |= (
          if .streamSettings.realitySettings then
            .streamSettings.realitySettings.serverNames = [$sni] |
            .streamSettings.realitySettings.dest = ($sni + ":443") |
            .streamSettings.realitySettings.target = ($sni + ":443")
          elif .streamSettings.tlsSettings then
            .streamSettings.tlsSettings.serverName = $sni
          else . end
        )
      ' "$CONFIG_PATH")"
      commit_config "$updated" || { warn "SNI 修改写入失败，原配置保持不变。"; return 1; }
      validate_and_restart_service
      ok "节点 [${cur_tag}] 伪装域名已更新为: ${new_sni}"
      ;;
    4)
      if [[ "$cur_net" != "xhttp" ]]; then
        warn "当前节点并非 xhttp 传输层，无法修改 XHTTP 路径。"
        return 0
      fi
      local new_path rand_path
      rand_path="$(random_xhttp_path)"
      read -r -p "请输入新的 XHTTP 路径 (直接回车使用随机 ${rand_path}): " new_path || new_path=""
      new_path="$(sanitize_path "${new_path:-$rand_path}")"
      [[ -n "$new_path" ]] || { warn "路径不能为空。"; return 0; }
      backup_existing_config
      local updated
      updated="$(jq --arg tag "$cur_tag" --arg path "$new_path" '
        (.inbounds[] | select(.tag == $tag)).streamSettings.xhttpSettings.path = $path
      ' "$CONFIG_PATH")"
      commit_config "$updated" || { warn "路径修改写入失败，原配置保持不变。"; return 1; }
      validate_and_restart_service
      ok "节点 [${cur_tag}] XHTTP 路径已更新为: ${new_path}"
      ;;
    *) info "已取消修改。" ;;
  esac
  echo && read -rp "按回车键返回主菜单..." _dummy
}

# 5. 服务运维管理
service_operations_menu() {
  while true; do
    echo
    echo -e "  ${BOLD}服务运维${NC}"
    echo "————————————————————————————————————————————————"
    echo -e "  ${GREEN}1.${NC} 启动 Xray"
    echo -e "  ${GREEN}2.${NC} 停止 Xray"
    echo -e "  ${GREEN}3.${NC} 重启 Xray"
    echo -e "  ${GREEN}4.${NC} 查看服务状态"
    echo -e "  ${GREEN}5.${NC} 查看运行日志"
    echo -e "  ${GREEN}0.${NC} 返回主菜单"
    echo "————————————————————————————————————————————————"

    read -r -p "请选择操作 [0-5]: " op_sel || op_sel="0"
    case "${op_sel:-0}" in
      1)
        if ! service_unit_exists "$SERVICE_NAME"; then
          warn "未找到 ${SERVICE_NAME}.service，请先在主菜单执行“安装/修复 Xray 服务”。"
        elif systemctl start "$SERVICE_NAME"; then
          sleep 1
          if systemctl is-active --quiet "$SERVICE_NAME" 2>/dev/null; then
            ok "Xray 服务启动成功！"
          else
            warn "启动未就绪，请查看状态或日志。"
          fi
        else
          warn "Xray 服务启动失败，请查看状态或日志排查。"
        fi
        echo && read -rp "按回车键继续..." _dummy
        ;;
      2)
        if systemctl stop "$SERVICE_NAME"; then
          ok "Xray 服务已停止。"
        else
          warn "停止 Xray 服务失败，请检查服务状态。"
        fi
        echo && read -rp "按回车键继续..." _dummy
        ;;
      3)
        if systemctl restart "$SERVICE_NAME"; then
          sleep 1
          if systemctl is-active --quiet "$SERVICE_NAME" 2>/dev/null; then
            ok "Xray 服务重启成功！"
          else
            warn "服务重启未就绪，请检查配置与日志。"
          fi
        else
          warn "重启命令执行失败，请检查配置与日志。"
        fi
        echo && read -rp "按回车键继续..." _dummy
        ;;
      4)
        echo
        systemctl status "$SERVICE_NAME" --no-pager -l || true
        echo && read -rp "按回车键继续..." _dummy
        ;;
      5)
        echo
        journalctl -u "$SERVICE_NAME" -n 50 --no-pager || true
        echo && read -rp "按回车键继续..." _dummy
        ;;
      0) break ;;
      *) warn "无效选项，请重新选择。" ;;
    esac
  done
}

# ─────────────────────────────────────────────────────────────
# 6. Telegram 机器人管理模块 (freedom-bot, 参考 3X-UI 架构)
# ─────────────────────────────────────────────────────────────

get_bot_status_str() {
  if ! [[ -x "$BOT_BIN_PATH" ]]; then
    echo -e "${RED}未安装${NC}"
    return
  fi
  if ! [[ -f "$BOT_CONFIG_PATH" ]]; then
    echo -e "${YELLOW}未配置${NC}"
    return
  fi
  if systemctl is-active --quiet "$BOT_SERVICE" 2>/dev/null; then
    echo -e "${GREEN}运行中${NC}"
  else
    echo -e "${YELLOW}已停止${NC}"
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
  info "检测到已安装 Telegram Bot。可以直接同步 GitHub Release 的最新 Bot 二进制，无需进入二级菜单。"
  local ans
  read -r -p "是否同步更新 Telegram Bot？[y/N]（不会修改 Xray-core）: " ans || ans="N"
  case "$ans" in
    y|Y|yes|YES)
      if ! install_or_update_freedom_bot; then
        warn "Bot 更新失败，保留原程序或已尝试回滚。下次启动 freedom 可重新尝试。"
        return 1
      fi
      ;;
    *) info "已跳过本次 Bot 同步；日后可从在线更新菜单单独更新。";;
  esac
  if ! (umask 077; printf "%s\n" "$script_hash" > "${stamp}.next" && mv -f "${stamp}.next" "$stamp"); then
    warn "Bot 更新检查标记保存失败；不影响已安装版本。"
  fi
}
install_or_update_freedom_bot() {
  echo
  info "正在检测系统 CPU 架构与 Freedom Bot 最新程序..."
  local arch=""
  case "$(uname -m)" in
    x86_64|amd64) arch="amd64" ;;
    aarch64|arm64) arch="arm64" ;;
    *) fatal "暂不支持的 CPU 架构: $(uname -m)" ;;
  esac

  local bin_name="freedom-bot-linux-${arch}"
  local dl_url="https://github.com/${GITHUB_REPO}/releases/download/bot-latest/${bin_name}"
  info "目标架构: linux/${arch}，从官方 GitHub Release 拉取二进制与 SHA-256..."
  local tmp_dir
  tmp_dir="$(mktemp -d)" || return 1
  local tmp_bin="$tmp_dir/$bin_name" sums_file="$tmp_dir/${bin_name}.sha256"
  if ! curl --proto "=https" -fsSL --retry 2 -m 60 "$dl_url" -o "$tmp_bin" || \
     ! curl --proto "=https" -fsSL --retry 2 -m 30 "${dl_url}.sha256" -o "$sums_file"; then
    warn "无法从 GitHub 官方 Release 获取二进制及校验值；为安全起见不使用第三方加速镜像。"
    rm -rf "$tmp_dir"
    return 1
  fi
  local expected actual
  expected="$(awk -v name="$bin_name" '$2 == name { print $1; exit }' "$sums_file")"
  actual="$(sha256sum "$tmp_bin" | awk '{print $1}')"
  if ! [[ "$expected" =~ ^[0-9a-fA-F]{64}$ ]] || [[ "$expected" != "$actual" ]]; then
    warn "Bot 发布包 SHA-256 校验失败，拒绝安装。"
    rm -rf "$tmp_dir"
    return 1
  fi
  chmod 755 "$tmp_bin"
  local magic bot_ver
  magic="$(head -c 4 "$tmp_bin" 2>/dev/null | od -An -tx1 | tr -d ' \n')"
  if [[ "$magic" != "7f454c46" ]] || ! bot_ver="$("$tmp_bin" -v 2>/dev/null)"; then
    warn "下载的程序不是可执行的 ELF Bot。"
    rm -rf "$tmp_dir"
    return 1
  fi
  [[ ! -f "$BOT_BIN_PATH" ]] || cp -a "$BOT_BIN_PATH" "${BOT_BIN_PATH}.previous"
  if ! install -m 755 "$tmp_bin" "${BOT_BIN_PATH}.next" || ! mv -f "${BOT_BIN_PATH}.next" "$BOT_BIN_PATH"; then
    warn "安装 Bot 程序失败；保留备份。"
    rm -rf "$tmp_dir"
    return 1
  fi
  rm -rf "$tmp_dir"
  ok "Freedom Bot 已就绪: $BOT_BIN_PATH (${bot_ver})"

  # 首次升级旧 Bot 必须真正重启服务，才能加载新的菜单和 /update。
  if [[ -f "$BOT_CONFIG_PATH" ]] && [[ -f "$BOT_SERVICE_FILE" ]]; then
    if systemctl restart "$BOT_SERVICE" && sleep 2 && systemctl is-active --quiet "$BOT_SERVICE"; then
      ok "freedom-bot 已重启，正在运行新版程序：$("$BOT_BIN_PATH" -v 2>/dev/null)"
    else
      warn "新 Bot 启动检测失败，尝试恢复旧版二进制。"
      if [[ -s "${BOT_BIN_PATH}.previous" ]]; then
        install -m 755 "${BOT_BIN_PATH}.previous" "$BOT_BIN_PATH" || return 1
        systemctl restart "$BOT_SERVICE" || warn "旧版 Bot 恢复启动失败，请检查 systemctl/journalctl。"
      fi
      warn "查看日志：journalctl -u freedom-bot -n 50 --no-pager"
      return 1
    fi
  fi
}

configure_freedom_bot_interactive() {
  echo
  echo -e "  ${BOLD}配置 Telegram 机器人${NC}"
  echo "提示：需先在 Telegram 中找 @BotFather 创建机器人获取 Token；"
  echo "可在 Telegram 找 @userinfobot 获取您本人的数字 Chat ID 作为管理员。"
  echo "─────────────────────────────────────────────────────────────────"

  local cur_token=""
  local cur_admins=""
  local cur_proxy=""
  local cur_hours=12
  local cur_mutations=false
  local cur_updates=false cur_core_updates=false cur_sensitive_backup=false

  if [[ -f "$BOT_CONFIG_PATH" ]]; then
    cur_token="$(jq -r '.token // ""' "$BOT_CONFIG_PATH" 2>/dev/null || true)"
    cur_admins="$(jq -r '(.admin_ids // []) | join(",")' "$BOT_CONFIG_PATH" 2>/dev/null || true)"
    cur_proxy="$(jq -r '.socks5_proxy // ""' "$BOT_CONFIG_PATH" 2>/dev/null || true)"
    cur_hours="$(jq -r '.report_hours // 12' "$BOT_CONFIG_PATH" 2>/dev/null || echo 12)"
    cur_mutations="$(jq -r '.allow_mutations // false' "$BOT_CONFIG_PATH" 2>/dev/null || echo false)"
    cur_updates="$(jq -r '.allow_updates // false' "$BOT_CONFIG_PATH" 2>/dev/null || echo false)"
    cur_core_updates="$(jq -r '.allow_core_updates // false' "$BOT_CONFIG_PATH" 2>/dev/null || echo false)"
    cur_sensitive_backup="$(jq -r '.allow_sensitive_backup // false' "$BOT_CONFIG_PATH" 2>/dev/null || echo false)"
  fi

  local cur_token_display="未设置"
  if [[ -n "$cur_token" ]]; then
    cur_token_display="${cur_token:0:8}...(已隐藏)"
  fi
  read -r -p "请输入 Telegram Bot Token [当前: ${cur_token_display}]: " in_token || in_token=""
  local final_token="${in_token:-$cur_token}"
  if [[ -z "$final_token" ]]; then
    warn "Bot Token 不能为空，配置未保存。"
    return 1
  fi
  if ! [[ "$final_token" =~ ^[0-9]{5,}:[A-Za-z0-9_-]{20,}$ ]]; then
    warn "Bot Token 格式不正确（应形如 123456789:AA...），配置未保存。"
    return 1
  fi

  read -r -p "请输入管理员 Telegram User ID (多个用英文逗号分隔) [当前: ${cur_admins:-未设置}]: " in_admins || in_admins=""
  local final_admins="${in_admins:-$cur_admins}"
  local json_admins
  json_admins="$(printf '%s' "$final_admins" | tr ',' '\n' | sed 's/[^0-9]//g' | grep -v '^$' | jq -R . | jq -s 'map(tonumber)')"
  if [[ -z "$json_admins" || "$json_admins" == "[]" ]]; then
    warn "至少需要指定一个数字管理员 User ID 以保证安全性，配置未保存。"
    return 1
  fi

  read -r -p "请输入 SOCKS5 代理地址 (可选，例如 127.0.0.1:10808，回车跳过) [当前: ${cur_proxy:-无}]: " in_proxy || in_proxy=""
  local final_proxy="${in_proxy:-$cur_proxy}"

  read -r -p "请输入定时系统/流量报告推送间隔 (小时，0=关闭) [当前: ${cur_hours}]: " in_hours || in_hours=""
  local final_hours="${in_hours:-$cur_hours}"
  if ! [[ "$final_hours" =~ ^[0-9]+$ ]] || ((final_hours > 168)); then
    warn "报告间隔必须为 0-168 之间的整数小时，配置未保存。"
    return 1
  fi

  local in_mutations final_mutations="$cur_mutations"
  read -r -p "允许 Telegram 私聊管理员修改 Xray 用户配置？[y/N；当前 ${cur_mutations}]（建议先关闭）: " in_mutations || in_mutations=""
  case "$in_mutations" in
    y|Y|yes|YES) final_mutations=true ;;
    n|N|no|NO) final_mutations=false ;;
    "") ;;
    *) warn "远程写入选项无效，配置未保存。"; return 1 ;;
  esac

  local in_updates in_core_updates final_updates="$cur_updates" final_core_updates="$cur_core_updates"
  read -r -p "允许 Telegram 私聊管理员检查并更新脚本/Bot？[y/N；当前 ${cur_updates}]: " in_updates || in_updates=""
  case "$in_updates" in
    y|Y|yes|YES) final_updates=true ;;
    n|N|no|NO) final_updates=false ;;
    "") ;;
    *) warn "更新权限选项无效"; return 1 ;;
  esac
  read -r -p "额外允许远程升级 Xray-core？[y/N；当前 ${cur_core_updates}]（可能引入协议兼容性变化）: " in_core_updates || in_core_updates=""
  case "$in_core_updates" in
    y|Y|yes|YES) final_core_updates=true ;;
    n|N|no|NO) final_core_updates=false ;;
    "") ;;
    *) warn "核心更新权限选项无效"; return 1 ;;
  esac
  if [[ "$final_updates" != "true" ]]; then final_core_updates=false; fi

  local in_sensitive_backup final_sensitive_backup="$cur_sensitive_backup"
  read -r -p "允许 TG 发送包含私钥的完整 Xray 配置备份？[y/N；当前 ${cur_sensitive_backup}]（建议关闭）: " in_sensitive_backup || in_sensitive_backup=""
  case "$in_sensitive_backup" in
    y|Y|yes|YES) final_sensitive_backup=true ;;
    n|N|no|NO) final_sensitive_backup=false ;;
    "") ;;
    *) warn "敏感备份权限选项无效"; return 1 ;;
  esac

  install -d -m 755 "$CONFIG_DIR"

  # 原子写入并校验：避免 jq 失败时把 bot.json 清空成 0 字节
  local bot_tmp="${BOT_CONFIG_PATH}.tmp.$$"
  if ! jq -n \
    --arg token "$final_token" \
    --argjson admins "$json_admins" \
    --arg proxy "$final_proxy" \
    --argjson hours "$final_hours" \
    --argjson mutations "$final_mutations" \
    --argjson updates "$final_updates" \
    --argjson core_updates "$final_core_updates" \
    --argjson sensitive_backup "$final_sensitive_backup" \
    --arg xray_cfg "$CONFIG_PATH" \
    --arg xray_bin "$BIN" \
    '{
      token: $token,
      admin_ids: $admins,
      socks5_proxy: $proxy,
      report_hours: $hours,
      allow_mutations: $mutations,
      allow_updates: $updates,
      allow_core_updates: $core_updates,
      allow_sensitive_backup: $sensitive_backup,
      xray_config_path: $xray_cfg,
      xray_bin_path: $xray_bin,
      stats_api_server: "127.0.0.1:10085"
    }' > "$bot_tmp"; then
    rm -f "$bot_tmp"
    warn "机器人配置生成失败（参数格式异常），配置未保存。"
    return 1
  fi
  chmod 600 "$bot_tmp" 2>/dev/null || true
  mv -f "$bot_tmp" "$BOT_CONFIG_PATH"
  ok "配置已成功保存至 $BOT_CONFIG_PATH"

  # 如果二进制尚未安装，自动提示安装
  if ! [[ -x "$BOT_BIN_PATH" ]]; then
    warn "检测到尚未安装 freedom-bot 二进制，正在自动拉取安装..."
    if ! install_or_update_freedom_bot; then
      warn "自动安装失败，可稍后在菜单中选择“安装 / 升级 freedom-bot”重试。"
      return 1
    fi
  fi

  # 编写/更新 systemd 服务
  info "正在注册 systemd 守护进程服务: $BOT_SERVICE_FILE"
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
    warn "systemd 守护进程重载失败，请检查 systemctl 状态。"
    return 1
  fi
  systemctl enable "$BOT_SERVICE" >/dev/null 2>&1 || warn "设置 ${BOT_SERVICE} 开机自启失败，可稍后手动执行 systemctl enable ${BOT_SERVICE}。"
  if ! systemctl restart "$BOT_SERVICE"; then
    warn "freedom-bot 启动命令执行失败，请选择查看运行日志排查。"
    return 1
  fi
  sleep 1.5

  if systemctl is-active --quiet "$BOT_SERVICE"; then
    ok "freedom-bot 服务已成功启动并在后台守护运行！"
    info "现在您可以在 Telegram 中向您的机器人发送 /start 或 /menu 体验完整控制中心！"
  else
    warn "freedom-bot 启动未就绪，请选择查看运行日志排查。"
    return 1
  fi
  return 0
}

uninstall_freedom_bot_interactive() {
  echo
  warn "警告：此操作将停止并卸载 Freedom Bot 机器人服务及程序！"
  local ans=""
  read -r -p "确定卸载 Freedom Bot？[y/N]: " ans || ans="N"
  case "${ans:-N}" in
    y|Y|yes|YES)
      systemctl stop "$BOT_SERVICE" 2>/dev/null || true
      systemctl disable "$BOT_SERVICE" 2>/dev/null || true
      rm -f "$BOT_SERVICE_FILE" "$BOT_BIN_PATH" "$BOT_CONFIG_PATH"
      systemctl daemon-reload 2>/dev/null || true
      ok "Freedom Bot 已彻底卸载清理。"
      return 0
      ;;
    *)
      info "已取消卸载。"
      return 1
      ;;
  esac
}

telegram_bot_management_menu() {
  while true; do
    echo
    echo -e "  ${BOLD}Telegram 机器人${NC}"
    local cur_st="$(get_bot_status_str)"
    echo -e "  状态: ${cur_st}"
    if [[ -x "$BOT_BIN_PATH" ]]; then
      local b_ver="$("$BOT_BIN_PATH" -v 2>/dev/null || echo "unknown")"
      echo -e "  版本: ${b_ver}"
    fi
    echo "————————————————————————————————————————————————"
    echo -e "  ${GREEN}1.${NC} 配置机器人（权限 / 更新 / 管理员 / 定时报告）"
    echo -e "  ${GREEN}2.${NC} 安装 / 升级 freedom-bot"
    echo -e "  ${GREEN}3.${NC} 启动服务"
    echo -e "  ${GREEN}4.${NC} 停止服务"
    echo -e "  ${GREEN}5.${NC} 重启服务"
    echo -e "  ${GREEN}6.${NC} 查看日志"
    echo -e "  ${GREEN}7.${NC} 卸载 freedom-bot"
    echo -e "  ${GREEN}0.${NC} 返回主菜单"
    echo "————————————————————————————————————————————————"

    read -r -p "请选择操作 [0-7]: " b_choice || b_choice="0"
    case "${b_choice:-0}" in
      1)
        configure_freedom_bot_interactive || warn "机器人配置未完成，请根据上方提示处理后重试。"
        echo && read -rp "按回车键继续..." _dummy
        ;;
      2)
        install_or_update_freedom_bot || warn "安装/升级未完成，请检查网络后重试。"
        echo && read -rp "按回车键继续..." _dummy
        ;;
      3)
        if systemctl start "$BOT_SERVICE"; then
          sleep 1
          if systemctl is-active --quiet "$BOT_SERVICE"; then
            ok "启动成功！"
          else
            warn "启动失败，请检查配置与日志。"
          fi
        else
          warn "启动命令执行失败，请检查服务是否已安装。"
        fi
        echo && read -rp "按回车键继续..." _dummy
        ;;
      4)
        if systemctl stop "$BOT_SERVICE"; then
          ok "服务已停止。"
        else
          warn "停止服务失败，请检查服务状态。"
        fi
        echo && read -rp "按回车键继续..." _dummy
        ;;
      5)
        if systemctl restart "$BOT_SERVICE"; then
          sleep 1
          if systemctl is-active --quiet "$BOT_SERVICE"; then
            ok "重启成功！"
          else
            warn "重启失败，请检查配置与日志。"
          fi
        else
          warn "重启命令执行失败，请检查服务是否已安装。"
        fi
        echo && read -rp "按回车键继续..." _dummy
        ;;
      6)
        echo
        journalctl -u "$BOT_SERVICE" -n 50 --no-pager || true
        echo && read -rp "按回车键继续..." _dummy
        ;;
      7)
        if uninstall_freedom_bot_interactive; then
          echo && read -rp "按回车键返回主菜单..." _dummy
          break
        fi
        echo && read -rp "按回车键继续..." _dummy
        ;;
      0) break ;;
      *) warn "无效选择，请重新输入。" ;;
    esac
  done
}

# 7. 完全卸载
uninstall_xray_completely() {
  echo
  warn "警告：此操作将彻底停止并卸载 Xray-core 及所有配置文件！"
  read -r -p "确定完全卸载？[y/N]: " u_ans || u_ans="N"
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
      ok "Xray 及 Freedom Bot 服务已彻底卸载清理完成。"
      exit 0
      ;;
    *) info "已取消卸载。" ;;
  esac
}

# 状态概览（3X-UI 风格：纯文本一行）
show_status() {
  local node_count=0
  local user_count=0
  local svc_text="${RED}未运行${NC}"
  local core_ver
  core_ver="$(get_xray_core_version)"

  if [[ -f "$CONFIG_PATH" ]]; then
    node_count="$(jq '[.inbounds[]? | select(.tag != "api-internal")] | length' "$CONFIG_PATH" 2>/dev/null)" || node_count=0
    [[ "$node_count" =~ ^[0-9]+$ ]] || node_count=0
    user_count="$(get_all_system_users 2>/dev/null | wc -l)" || user_count=0
    [[ "$user_count" =~ ^[0-9]+$ ]] || user_count=0
  fi
  if systemctl is-active --quiet "$SERVICE_NAME" 2>/dev/null; then
    svc_text="${GREEN}运行中${NC}"
  fi

  local xui_tip=""
  if [[ -d "/usr/local/x-ui" ]] || service_unit_exists "x-ui"; then
    local xui_st="${YELLOW}已停止${NC}"
    systemctl is-active --quiet "x-ui" 2>/dev/null && xui_st="${GREEN}运行中${NC}"
    xui_tip=" | 3X-UI: ${xui_st}"
  fi

  local bot_st
  bot_st="$(get_bot_status_str)"

  echo -e "  Xray: ${svc_text} (${core_ver}) | 节点: ${node_count} | 用户: ${user_count} | Bot: ${bot_st}${xui_tip}"
}

# 主控制台交互菜单（3X-UI 简洁风格）
main_menu() {
  while true; do
    echo
    echo -e "  ${BOLD}freedom.sh 管理脚本 (${SCRIPT_VERSION})${NC}"
    echo "————————————————————————————————————————————————"
    show_status
    echo "————————————————————————————————————————————————"
    echo -e "  ${GREEN}0.${NC} 退出脚本"
    echo "————————————————————————————————————————————————"
    echo -e "  ${GREEN}1.${NC} 节点列表与分享链接"
    echo -e "  ${GREEN}2.${NC} 添加节点"
    echo -e "  ${GREEN}3.${NC} 删除节点"
    echo -e "  ${GREEN}4.${NC} 修改节点"
    echo "————————————————————————————————————————————————"
    echo -e "  ${GREEN}5.${NC} 用户管理与流量统计"
    echo -e "  ${GREEN}6.${NC} 系统网络调优"
    echo -e "  ${GREEN}7.${NC} 服务运维"
    echo -e "  ${GREEN}8.${NC} 在线更新"
    echo -e "  ${GREEN}9.${NC} Telegram 机器人"
    echo "————————————————————————————————————————————————"
    echo -e " ${GREEN}10.${NC} 卸载 Xray"
    echo "————————————————————————————————————————————————"

    read -r -p "请选择 [0-10]: " menu_choice || menu_choice="0"
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
      0) ok "退出脚本。再见！"; exit 0 ;;
      *) warn "无效选项，请重新选择。" ;;
    esac
  done
}

main() {
  need_root
  ensure_systemd_available
  install_base_deps
  sync_time_best_effort
  ensure_xray_installed
  locked_config_operation ensure_config_scaffold || fatal "初始化配置时未能获得独占锁。"

  # 首次安装才注册快捷指令。以后由显式更新流程负责覆盖，
  # 防止运行旧版脚本副本时静默降级 /usr/local/bin/freedom。
  if [[ ! -f "/usr/local/bin/freedom" ]]; then
    if [[ -f "$0" && "$0" != "/dev/fd/"* ]]; then
      install -m 755 "$0" /usr/local/bin/freedom || warn "注册 freedom 快捷指令失败。"
    else
      warn "当前使用非文件输入启动，请手动安装到 /usr/local/bin/freedom。"
    fi
  fi

  # 检测首次运行的新脚本是否需要同步升级已安装的 Bot。
  # 提示只出现一次/脚本内容变更；拒绝时不会触发任何网络下载。
  maybe_offer_bot_refresh || warn "Bot 同步检查未完成，可以通过主菜单 8 -> 5 重试。"

  # 若未配置或业务节点数为 0，则引导添加首个节点
  local initial_nodes=0
  if [[ -f "$CONFIG_PATH" ]]; then
    initial_nodes="$(jq '[.inbounds[]? | select(.tag != "api-internal")] | length' "$CONFIG_PATH" 2>/dev/null)" || initial_nodes=0
    [[ "$initial_nodes" =~ ^[0-9]+$ ]] || initial_nodes=0
  fi
  if ((initial_nodes == 0)); then
    clear || true
    echo -e "  ${BOLD}检测到当前尚未配置任何 Xray 业务节点，将引导您初始化创建第 1 个节点${NC}"
    locked_config_operation tune_linux_kernel_network
    locked_config_operation add_new_node_interactive || true
  fi

  # 之后进入全功能控制台主菜单
  main_menu
}

main "$@"
