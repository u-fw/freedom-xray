# freedom-xray

Freedom: a modular Xray node manager (`freedom.sh`), a pure-Go Telegram bot (`freedom-bot`), and 1stream rule assets.

## 1stream.dat

This repository can manually build `1stream.dat` with GitHub Actions.

The generated file is a geosite/domain asset for Xray `domain` rules. It does not contain geoip data.

### Build

Run the `Build 1stream.dat` workflow manually from the GitHub Actions page.

The workflow publishes these files to the `1stream-latest` release:

- `1stream.dat`
- `1stream.dat.sha256`
- `1stream-tags.md`
- `source-info.json`

### Xray Usage

Download `1stream.dat` to Xray's asset directory, usually `/usr/local/share/xray`.

Example routing rule:

```json
{
  "type": "field",
  "domain": [
    "ext:1stream.dat:ai"
  ],
  "outboundTag": "proxy"
}
```

Download `1stream-tags.md` from the release to see all generated tags.

## Scripts

- `1stream.sh`: build `1stream.dat` from 1stream upstream domain rules.
- `freedom.sh`: Modern modular node lifecycle manager dedicated 100% exclusively to Xray-core.

### Quick Start (Linux)

Run directly in terminal without manual download or permissions:

```bash
# Direct connection (Overseas VPS)
sudo bash <(curl -fsSL https://raw.githubusercontent.com/u-fw/freedom-xray/main/freedom.sh)

# Accelerated mirror (CN / High-latency VPS)
sudo bash <(curl -fsSL https://ghproxy.net/https://raw.githubusercontent.com/u-fw/freedom-xray/main/freedom.sh)
```

### Key Highlights & Features

- **Live Version Dashboard & Global Shortcut (`freedom`)**:
  - Console header displays active Xray-core version, script version, systemd service status, managed node count, managed user count, and panel coexistence status at a glance.
  - Automatically registers `/usr/local/bin/freedom` so you can bring up the control panel anytime by simply typing `freedom`.
- **Native Multi-User Management & Real-Time Traffic Statistics (StatsService)**:
  - Powered 100% natively by Xray-core's internal `StatsService` gRPC API (`127.0.0.1:10085`, `dokodemo-door`), completely eliminating the need for external databases or third-party web panels.
  - Independent user management: add/delete users with custom email tags and distinct UUIDs, automatically synchronized across all managed node inbounds.
  - Real-time traffic query leaderboard (Uplink, Downlink, Total with automatic human-readable unit conversion: KB / MB / GB / TB).
  - One-click traffic reset (for a single user or all users) and instant export of user-specific `vless://` URLs and terminal ANSI QR codes.
- **Online Upgrade & Maintenance Center**:
  - **Script Self-Updater**: Check and update `freedom.sh` directly from GitHub with syntax verification and safe rollback.
  - **Xray-core Updater**: Query XTLS official releases and upgrade Xray-core in-place with zero downtime and automatic configuration re-validation.
  - **Asset Updater**: One-click updates for official GeoIP/GeoSite databases and repository-built `1stream.dat` streaming media routing rules.
- **Dynamic Modular Node Management (CRUD)**:
  - Add, list, edit, and delete inbounds dynamically within a single unified Xray configuration (`config.json`).
  - **Zero-Friction Node Setup**: Completely automated, silent UUID generation (no manual prompting or typing required).
  - **Automatic Firewall Automation**: Intelligently detects and opens listening TCP ports on `ufw`, `firewalld`, or `iptables` upon adding/modifying nodes, and cleans them up upon node deletion.
  - **Default Disguise**: Uses high-reputation `www.nvidia.com:443` as the default REALITY SNI target.
  - **Flexible User Identifiers**: Supports arbitrary nicknames (e.g. `admin`, `alice`, `my_phone`) without enforcing fake email domain suffixes.
  - Instant export of standard `vless://` URIs and terminal ANSI QR code rendering for all managed nodes.
- **3 Core Atomic Architecture Templates**:
  - **Template 1: VLESS + TCP + REALITY + Vision**: Highest performance, direct connection with Linux kernel Splice zero-copy hardware acceleration.
  - **Template 2: VLESS + XHTTP + REALITY**: Next-gen HTTP streaming disguise (Beyond REALITY) with dynamic padding (`xPaddingBytes`) and anti-DPI probing, no domain required.
  - **Template 3: VLESS + XHTTP + TLS**: Engineered for Cloudflare CDN proxying and IP unblocking (with automatic 10-year ECC self-signed certs and full ALPN negotiation).
- **Vision Penetration on XHTTP**:
  - Automatically enables `flow: "xtls-rprx-vision"` when paired with VLESS Encryption (`ML-KEM-768`), penetrating encryption layers to eliminate TLS-in-TLS fingerprints according to the official Xray specification.
- **Cloudflare ECH Zero-Exposure Integration**:
  - Automatically includes Outer SNI (`cloudflare-ech.com`) and secure DoH (`https://dns.alidns.com/dns-query`) guidance for CDN nodes, hiding origin custom domains completely from middleboxes.
- **Strict Dual-Layer Port Conflict Prevention**:
  - Automatically audits both internal Xray inbound definitions and global operating system TCP listeners (`ss`/`lsof`/`netstat`) before applying any port changes.
- **Live System Network Tuning Diagnostics & Reversible Snapshots (Menu Option 6)**:
  - One compact status view of the values that matter: BBR + FQ, TCP Fast Open, idle slow-start, 32MB socket buffers, `tcp_notsent_lowat`, local port range, connection queues, and live file-descriptor limits.
  - Full-stack tuning also applies TCP user timeout and keepalive to every managed inbound and the direct outbound, then restarts Xray once so the new descriptor limit takes effect.
  - **Automatic Original Snapshot Backup**: Automatically captures and archives native kernel parameter snapshots before applying any tuning (`/usr/local/etc/xray/sysctl_backup.conf`).
  - **One-Click Reversible Rollback**: Instant restoration to original factory OS kernel values at any time with verification.
  - System benchmark scoring (0-100) with a plain summary line.
- **Standalone Go Telegram Bot (`freedom-bot`, Menu Option 9)**:
  - Reference architecture based on 3X-UI, engineered in 100% pure Go with zero CGO or external database dependencies (`CGO_ENABLED=0`).
  - Managed, configured, and daemonized seamlessly via `freedom.sh` and systemd (`freedom-bot.service`).
  - **Interactive Inline Dashboard & Remote Control**:
    - `/start` / `/menu`: Brings up the rich inline control console with instant navigation buttons.
    - 📊 **Real-Time System Health**: Monitors CPU load, per-core utilization, RAM usage (Used/Total/%), Disk capacity, Uptime, and Xray status.
    - 📈 **Native Traffic Leaderboard**: Direct integration with Xray's `StatsService` to display real-time Uplink, Downlink, and Total consumption per user, with an interactive one-click traffic reset confirmation flow.
    - 📋 **Active Inbound Matrix**: Displays all active node ports, protocols (TCP / XHTTP), security layers (REALITY / TLS), and disguised SNIs.
    - 👥 **User Export with In-Memory QR Codes**: Select any user to instantly receive standard `vless://` URLs alongside pure-Go generated high-resolution PNG QR code photos.
    - 🔄 **Remote Service Operations**: Safe one-click Xray daemon reboot with confirmation dialogues.
    - 💾 **Remote Config Backup**: Instantly sends `/usr/local/etc/xray/config.json` to the admin chat as a document attachment.
    - 📢 **Automated Scheduled Reporting**: Configurable periodic broadcaster (e.g. every 12 hours) reporting VPS resource utilization and user traffic leaderboards directly to admins.
  - **Security & Censorship-Resistant Networking**:
    - Strict Admin ID validation (`admin_ids` whitelist), silently discarding or rejecting unauthorized requests.
    - Built-in SOCKS5 proxy support (`socks5_proxy`), ensuring reliable connectivity to Telegram API even on mainland or restricted VPS nodes.
  - **Automated CI/CD Cross-Compilation**:
    - Dedicated GitHub Actions workflow (`build-bot.yml`) automatically builds static binaries for `linux/amd64` and `linux/arm64` with sha256 checksums, available via GitHub Releases and high-speed mirrors.
- **Post-Quantum Cryptography (PQC)**:
  - Integrated `vlessenc` (ML-KEM-768 / X25519) for post-quantum forward secrecy against *Harvest Now, Decrypt Later* attacks.
  - Native `mldsa65` (ML-DSA-65 / Dilithium) post-quantum digital signature authentication for REALITY.

