# freedom-xray

A lightweight, self-hosted Xray-core manager with a terminal interface (`freedom.sh`) and an optional Telegram bot (`freedom-bot`).

It manages the official Xray-core under systemd; no web panel is required. It is intended for personal and small-server administration. No transport can guarantee resistance to fingerprinting or censorship.

## Install

Review the script before running it as root:

```bash
curl -fsSLo freedom.sh https://raw.githubusercontent.com/u-fw/freedom-xray/main/freedom.sh
bash -n freedom.sh
less freedom.sh
sudo bash ./freedom.sh
```

Run `sudo freedom` to open the terminal menu. The Telegram submenu can install the bot and configure its BotFather token, administrator Telegram user IDs and remote permissions.

## Features

| Capability | Terminal | Telegram |
| --- | --- | --- |
| VLESS RAW/REALITY/Vision, XHTTP/REALITY and XHTTP/TLS/CDN | Create and manage | Create, manage and export |
| Other inbound protocols | Native JSON through agent CLI | Inspect, edit listener, export, create or replace native JSON, delete |
| VMess and Trojan inbound accounts | Agent CLI: list, add, edit, rotate, export and delete | Guided account management and credential export |
| Independent user identities and node assignments | Yes | Yes |
| Add, rename, remove, suspend and restore users | Yes (Go manager required) | Yes |
| Rotate user UUIDs | Yes | Yes |
| Per-node VLESS URI, text export and QR codes | Yes | Yes |
| Change XHTTP path/mode, host, padding, listener port/address and REALITY SNI/short ID | Yes; address via agent CLI | Yes (confirmation required) |
| User and inbound traffic totals | Yes | Yes |
| Per-user/per-node traffic since binding migration | Aggregate user totals | Yes |
| Per-user traffic quota and expiry with automatic suspension and admin notices | Local metadata | Yes |
| Online users, source IPs and last-online information | Via collector | `/online`, `/onlinedoctor` and user details |
| Routing rules, outbounds, balancers and DNS | Config file or agent CLI | `/routing` manages rules, outbound endpoints, balancers, DNS servers and static host mappings; runtime route queries, balancer diagnostics and temporary target switching use a local Xray API; advanced objects use JSON |
| Logging, connection policies and FakeDNS pools | Agent CLI, including preview and guarded apply | `/xrayconfig`: inspect common values, export, replace native JSON or restore defaults |
| Xray start, stop and restart; statistics repair | Yes | Status and diagnostics menus (confirmation required) |
| Bot and manager updates; Xray upgrades and rollback | Yes | Permission-gated |
| Public subscription service and full web dashboard | Not provided | Not provided |

VLESS user identities are kept separately from Xray inbounds in a local, permission-restricted `freedom-users.json`. A newly created user is **not** automatically granted access to every inbound. Assign access explicitly by node. Existing VLESS users are imported from Xray configuration without rotating their UUIDs. Conflicting identities are rejected rather than merged silently. VMess and Trojan accounts can be managed within their inbound, but are not part of the VLESS user, quota, suspension or connection-link workflows. Their credential export is a native client JSON object, not a ready-made connection link.

For advanced non-VLESS inbound, routing-rule, outbound, DNS, balancer and health-check settings, the Bot accepts the corresponding JSON object as text or a document up to 1 MiB. Invalid input can be corrected without restarting the interaction, and confirmations reject relevant configuration changes made after review. Balancer and health-check edits return to the object's detail with refreshed buttons. Exporting a complete native object requires `allow_sensitive_backup` and an explicit private-chat confirmation; the file may include credentials and private keys. Telegram chats are not end-to-end encrypted, so the local agent CLI is preferable for sensitive configuration. See the [coverage audit](docs/PANELLESS_COVERAGE.md) for remaining panel-free management gaps.

Suspended users retain their UUID and previous bindings in the local `freedom-disabled-users.json` file (mode 0600). This file is not encrypted. If an inbound is deleted, restoring its former binding can fail.

Traffic quotas and expiry dates are stored in `freedom-user-limits.json` (mode 0600) because Xray has no native quota fields. The Bot checks them once a minute while it is running and suspends a client through the same reversible suspension flow when its total traffic reaches the quota or its expiry date passes. Administrators receive an 80% warning, expiry warnings three days ahead, and final suspension notices. Enforcement requires `allow_mutations`, never suspends the last remaining client of an inbound, and restarts Xray once per enforced suspension. An unreadable or ambiguous traffic ledger prevents quota decisions, but does not prevent expiry enforcement. Raising the quota or resetting traffic does not re-enable an automatically suspended user; restore it explicitly after reviewing the limits.

### Bot commands

```text
/start                     Main menu
/users                     User management
/adduser phone             Create an unassigned user
/adduser phone cdn         Create and assign to inbound "cdn"
/renameuser old new        Rename a user
/rotateuser phone          Rotate a UUID
/removeuser phone          Delete a user
/disableuser phone         Suspend a user
/enableuser phone          Restore a suspended user
/assignuser phone cdn      Assign a user to an inbound
/unassignuser phone cdn    Remove a user from an inbound
/nodes                     Inbound list
/traffic                   Persisted user traffic totals
/inboundtraffic            Persisted inbound traffic totals
/online                    Online users and recent source IPs (v1.3.0)
/onlinedoctor              Diagnose online statistics and repair the policy
/routing                   Inspect and edit routing rules; manage outbounds and DNS
/xrayconfig                Logging, connection policies and FakeDNS settings
/status                    Server status
/version                   Installed and upstream component versions
/update                    Update the manager and bot
/restart                   Confirmed Xray restart
/help                      Help
/cancel                    Cancel the current input flow
```

The bot only accepts authorized administrator messages in **private chats**. Remote configuration changes and Xray service control, updates, core updates and full sensitive configuration backups must each be explicitly enabled in the local `bot.json`:

```json
{
  "allow_mutations": false,
  "allow_updates": false,
  "allow_core_updates": false,
  "allow_sensitive_backup": false
}
```

`allow_core_updates` additionally requires `allow_updates`. Keep sensitive backup sending disabled unless required. Use a different Telegram Bot token for each VPS unless you have a dedicated centralized polling architecture.

The dashboard groups less frequent actions under **Tools**; the public link address setting takes effect without a Bot restart. In `/routing`, select a rule to see its conditions and target, including rules without names. Choose a field, send its replacement, and review the confirmation before applying it; `-` clears a field and `/cancel` leaves the input flow. Rules can also be created, moved and deleted. Rule order determines precedence, and the internal API rule stays protected. A configured routing script takes control of routing and disables rule editing; its handling of the internal API must be checked manually. **Outbounds** shows non-secret details, changes supported proxy addresses and ports, creates direct/blocking outbounds and chooses the default. **Balancers** manages selector prefixes, strategy and fallback, plus health-check selectors, probe interval and URL. Health-check configurations send periodic probe requests through selected outbounds. **DNS** manages servers, their order, query strategy and common cache/fallback switches. Advanced outbound and DNS JSON and the previous JSON rule editor remain under **Outbounds / DNS / JSON**. Candidate configurations are validated by Xray before restart. Telegram chats are not end-to-end encrypted; potentially credential-bearing routing extensions are hidden and outbound/DNS secrets or authenticated probe URLs should be entered through the local agent CLI instead.

### Agent CLI

The local `freedom-bot agent` interface supports non-interactive JSON input and output. Mutations preview by default and execute only with `--apply`; exporting links or complete native objects requires `--include-sensitive`. For the operation list, examples, input formats and current limitations, see [Agent CLI](docs/AGENT_CLI.md) and the [panel-free coverage matrix](docs/PANELLESS_COVERAGE.md).

### Public server address in client links

VLESS links that use REALITY embed the address clients dial directly. The Bot resolves it with direct IPv4 HTTP discovery (bypassing `socks5_proxy`, because a proxy reports its own egress address), falls back to a global-unicast interface address, and caches the result for 10 minutes. Behind NAT, a firewall that blocks outbound HTTP, or a relay/CDN topology where clients use a different hostname, set the address explicitly:

```json
{
  "server_host": "vpn.example.com"
}
```

`server_host` accepts a bare IP address (IPv4 or IPv6) or DNS hostname without a scheme, path, or port. The local configuration menu, Bot dashboard, and agent CLI can change it while preserving other keys. If discovery fails and no override is set, REALITY exports fail with an explicit error instead of emitting `127.0.0.1`; TLS links remain addressed by certificate SNI.

### Exporting user configurations

The user export menu lets you choose one assigned inbound or export all assigned links as a plain-text file. Individual links are sent without truncation; oversized links are delivered as TXT. QR generation is optional, and a failed QR does not suppress the underlying link.

The exporter preserves REALITY public-key, encryption, post-quantum and short-ID parameters. XHTTP exports use a standalone `mode=auto` (or the configured mode) and `extra={"xPaddingBytes":"100-1000"}`: `mode` is not duplicated inside `extra`. Padding can be read from `xhttpSettings.extra.xPaddingBytes` or the legacy top-level XHTTP setting.

The default XHTTP/TLS/CDN link includes an optional ECH value `cloudflare-ech.com+https://dns.alidns.com/dns-query`, intended for compatible v2rayN/v2rayNG imports. Compatibility depends on the client's core and the CDN's support; if the imported connection fails, test without ECH. ECH does not eliminate all metadata exposure.

### VLESS Vision compatibility

| Transport / encryption | `xtls-rprx-vision` |
| --- | --- |
| RAW/TCP with TLS or REALITY | Supported when applicable |
| XHTTP with `decryption=none` / `encryption=none` | Do not enable |
| XHTTP with supported VLESS Encryption | Can be used with Encryption; not the same as RAW TCP splice |

The exported flow must match the inbound user configuration. Neither CLI nor bot fabricates a Vision flow when none is configured.

## Internationalization

English is the **default locale** for the repository, terminal manager and Telegram Bot.

- **Telegram:** the dashboard has an **English / Chinese** toggle; `/language en` and `/language zh-CN` remain available. The preference is saved in `bot.json` as `"language": "en"` or `"zh-CN"`. On the first authorized private message, if `language_explicit` is absent or `false`, the Bot follows Telegram's `language_code` (`zh*` → `zh-CN`, `en*` → `en`); an explicit `/language` or button choice sets `"language_explicit": true` and stops automatic changes. The main dashboard, traffic, online monitoring, help, commands and user details use embedded locale catalogs in `bot/locales/`; untranslated legacy messages fall back to English.
- **Terminal:** choose **11. Language** from the main menu, or set `FREEDOM_LANG=en` / `FREEDOM_LANG=zh-CN`. The preference is stored at `/usr/local/etc/xray/freedom-cli-language`. Simplified Chinese main-menu strings are an optional, separately installed catalog (`locales/zh-CN.json`) downloaded from the official repository when selected; if unavailable, English is used. Other terminal submenus use English.
- All development comments, errors, default UI text, documentation and commit messages are English. Translated strings are kept in language resource files. The default locale never depends on the host's operating-system locale.

Language preferences do not alter Xray inbound settings, credentials, DNS rules, CDN headers or client sharing links.

## Cloudflare Origin Rules and external ports

When clients connect to `cdn.example.com:443` and a Cloudflare **Origin Rule** forwards traffic to `VPS:55555`, keep `inbounds[].port=55555` in Xray. Set the node's public-facing port to `443` in the terminal or Telegram node configuration. Exports then use the public port, without changing Xray's listener.

This mapping is stored separately in `/usr/local/etc/xray/freedom-public-ports.json`:

```json
{
  "nodes": {
    "XHTTP-TLS-CDN-55555": {
      "listen_port": 55555,
      "public_port": 443
    }
  }
}
```

Only XHTTP/TLS nodes support this override. A value of `0` clears it. Stale mappings are ignored if the actual inbound listening port changes. The metadata does not modify Cloudflare rules, the firewall, TLS certificates or the Xray service itself.

Configure the DNS record as **Proxied**, then use **Rules → Origin Rules → Destination Port** for the intended hostname/path. URL Rewrite Rules do not replace Origin Rules. Prefer a valid origin certificate and Cloudflare **Full (strict)** TLS mode. The origin port must be reachable from Cloudflare; limiting access to Cloudflare IP ranges is recommended when practical. Do not inadvertently restrict a separate direct REALITY listener.

### Real client IPs behind Cloudflare: deferred hardening

**Current decision: do not automatically sanitize X-Forwarded-For (XFF).** Keep the existing CDN and Origin Rule configuration unchanged. A direct REALITY inbound obtains its source IP from the socket without a CDN forwarding header.

For online-client telemetry, Xray supports `policy.levels["0"].statsUserOnline: true` and, on a trusted Cloudflare XHTTP inbound, `streamSettings.sockopt.trustedXForwardedFor: ["CF-Connecting-IP"]`. New XHTTP/TLS/CDN inbounds generated by the manager include the latter setting. Existing inbounds must be inspected and explicitly updated if they need real-client-IP observation. This is a **server-side inbound setting**, not an exported client `extra` field.

**Important:** Xray's current header handling checks that a configured trusted header is present but actually extracts the **first address in X-Forwarded-For**. It does not read the `CF-Connecting-IP` value directly. A client-supplied XFF header may therefore forge the reported source IP. Without validation, source addresses are informational only. **Never use these IPs for automatic bans, connection limits or other security decisions.**

If IP-based enforcement is added later:

1. Add a Cloudflare **Request Header Transform Rule** scoped to the XHTTP hostname that removes the incoming `X-Forwarded-For`. Verify Cloudflare's regenerated forwarding chain, including any Workers or unusual proxy paths.
2. Restrict the CDN origin listener (for example, `55555/TCP`) to trusted Cloudflare egress and use stronger origin authentication when appropriate. Restricting to Cloudflare IPs alone does not prove a request belongs to your zone. Do not block the separate REALITY `443` listener.
3. Test ordinary IPv4/IPv6 clients, deliberately forged XFF values, and the Xray online-stats API before enabling automated IP decisions.

References: [Cloudflare request header transformations](https://developers.cloudflare.com/rules/transform/request-header-modification/), [Cloudflare HTTP headers](https://developers.cloudflare.com/fundamentals/reference/http-headers/), [Xray-core header implementation](https://github.com/XTLS/Xray-core/blob/main/common/protocol/http/headers.go).

## Traffic accounting and persistence

Previously, the bot sampled Xray StatsService about every 15 seconds and saved totals to `/usr/local/etc/xray/freedom-stats.json`. v1.3.0 introduces a standalone `freedom-stats.service` and a transactional SQLite ledger at `/usr/local/etc/xray/freedom-stats.db` with a migration path from the JSON ledger. The JSON remains available as a compatibility mirror; it is not the canonical ledger after migration.

The sampler maintains totals by **user**, **inbound**, and (for migrated bindings) **user and inbound**. Historical user totals from before the binding migration cannot be split accurately across inbounds. Runtime resets and service restarts are handled by counter baselines and systemd invocation identifiers.

By default, Bot startup migrates eligible plain client emails to per-inbound identities for per-node accounting. If changes are needed, the migration validates and backs up the configuration and accounting database, then restarts Xray; it can therefore interrupt connections even when `allow_mutations` is disabled, since that setting controls remote changes. The local `-local-revert-bindings` and `-local-migrate-bindings` commands revert or reapply the identity migration; reverting keeps subsequent user changes in the plain-email mode. Keep the manager script and Bot binary in sync: the terminal's per-user traffic reset requires a Bot binary that supports `-local-user-traffic-reset-locked`.

Committed historical totals survive ordinary service reboots. Abrupt crashes or power loss may still omit traffic generated after the last successful sample. This is **not** exact billing-grade accounting, and Xray-level payload statistics are not interchangeable with hosting-provider transfer quotas.

Recent observed IPs are retained for seven days (up to 64 addresses per user) and the last-online timestamp is kept until the user is deleted or renamed. These records come from Xray's online map, which only contains **currently open sessions**: Xray removes an address as soon as the last session closes, and it discards loopback sources (`127.0.0.1`, `[::1]`). A REALITY client that is idle between requests can therefore be offline at poll time, and a local relay or port-forward in front of REALITY hides the real client address. The `/onlinedoctor` per-user probe shows active session counts next to observed IPs so these cases are distinguishable. Online presence is ephemeral: stale polls must be shown as **unknown**, not as connected. CDN source IPs have the unvalidated-XFF caveat above. Full access logging is not required for the online-stats API.

### Deleted users and live client IP diagnostics

When a user is deleted, their per-user traffic and recent IP records are removed from the SQLite ledger after the account change succeeds. Legacy orphaned rows are reconciled against active and suspended accounts when opening the traffic view; suspended user history and inbound totals are preserved.

The `/online` page now refreshes the Xray online API immediately and displays *online* IPs separately from *recently observed* IPs (seven days, up to 64 addresses per user), together with each user's last-online time. Xray only reports sessions that are open at poll time, so an idle REALITY client can have no live IP even though it connected moments earlier; the saved history still shows the observation. When the aggregate call fails or returns nothing, the Bot probes the per-email API (up to 128 users), and `/onlinedoctor` shows the first few users with their session count and observed IP count to distinguish "not connected" from "connected but no address recorded". If a session exists but no IP is recorded, REALITY is likely behind a local relay that makes Xray see a loopback source. REALITY direct nodes need no proxy-header configuration. Online tracking requires `statsUserOnline` at the user's policy level and an active authenticated session. Traffic shown beside a client is the **cumulative per-user total across all inbounds**, not per-IP consumption. The manager enables the counter flags for level `0` and for every client level referenced by an inbound, because Xray reports nothing for levels without them. Xray-core builds without the aggregate `-all` flag are queried per email automatically. To diagnose no IPs while generating active REALITY traffic:

```bash
sudo jq '.policy.levels' /usr/local/etc/xray/config.json
sudo /usr/local/bin/xray api statsonlineiplist --server=127.0.0.1:10085 -all
# Older Xray-core builds: query one user at a time instead
sudo /usr/local/bin/xray api statsonlineiplist --server=127.0.0.1:10085 -email=USER
sudo systemctl status freedom-stats --no-pager
sudo journalctl -u freedom-stats -n 40 --no-pager
```

Also confirm that the inbound's `settings.clients[].level` (or the level the client actually uses) has `statsUserUplink`, `statsUserDownlink`, and `statsUserOnline` set to `true`. A relay or CDN that terminates the connection can hide the client's real source address even when online tracking works.

## Updating and troubleshooting

Download and inspect the current manager, then start its menu:

```bash
curl --proto "=https" -fsSLo /tmp/freedom-latest.sh https://raw.githubusercontent.com/u-fw/freedom-xray/main/freedom.sh
bash -n /tmp/freedom-latest.sh
less /tmp/freedom-latest.sh
sudo install -m 755 /tmp/freedom-latest.sh /usr/local/bin/freedom
sudo freedom
```

Use CLI update option 1 ("Update manager and installed Bot") to update the manager and an installed Bot in one action. The Telegram update center offers the same combined action plus separate component buttons; remote updates require `allow_updates`, and Xray-core upgrade and rollback require separate authorization and confirmation. Bot binaries are downloaded from this repository's GitHub Release and verified with published SHA-256 checksums. An updated GitHub source tree does **not** mean the VPS is running the latest process.

```bash
sudo /usr/local/bin/freedom-bot -v
sudo systemctl status freedom-bot --no-pager
sudo systemctl status freedom-stats --no-pager
sudo journalctl -u freedom-bot -n 50 --no-pager
sudo journalctl -u freedom-stats -n 50 --no-pager
```

Core updates may break compatibility with older Shadowrocket/v2rayN/v2rayNG REALITY or ML-KEM implementations. Keep an independent SSH/Tailscale recovery route and a working alternate node. An Xray process starting successfully does not establish that clients can complete a handshake.

`StatsService` binds to `127.0.0.1` by default and should not be exposed to the public internet. The Bot currently needs elevated local privileges to manage configuration and systemd; protect its token, administrator chat and release sources. Controlled restarts can preserve a final snapshot, but cannot promise uninterrupted client sessions.

## CI/CD

GitHub Actions workflows are **manually dispatched** to avoid redundant builds and notification spam during development. Use **Source validation** to check Bash syntax and run Go tests; after validation, manually run **Build Freedom Bot** to publish the versioned Bot binary and its SHA-256 checksums. The separate `1stream.dat` builder is already manual. The build workflow never commits generated module files. Releases may also be triggered by explicitly changing `.github/release-trigger`; ordinary source commits do not publish a Bot binary.

The Bot uses a separate stable `vMAJOR.MINOR.PATCH` version in `bot/main.go`: backward-compatible features increment the minor version and fixes increment the patch version. Each new Bot version receives a fixed `bot-vMAJOR.MINOR.PATCH` release with the source commit and checksummed binaries; the workflow refuses to reuse that version for different Bot source. `bot-latest` remains the rolling compatibility release used by existing installations. The terminal script keeps its independent version in `freedom.sh` and is incremented when that script changes. A fixed release identifies its original build; `bot-latest` can be rebuilt from equivalent source.

Source builds require Go 1.26.7 or later, the minimum of the Telegram client library ([telego](https://github.com/mymmrac/telego)), and use the `stdjson` build tag so that library encodes JSON with Go's standard library: `go build -tags stdjson` and `go test -tags stdjson ./...`. Use a current security-patched Go release. Generated protobuf messages are committed; normal builds need no protobuf compiler. Installing a release binary does not require Go.

For an optional end-to-end check on Linux, build the Bot locally with `-tags stdjson` and run `sudo env XRAY_BIN=/path/to/xray FREEDOM_BOT_BIN=/path/to/freedom-bot python3 tests/xray-integration.py`. It requires Xray 26.10.31 or newer and tests temporary loopback-only listeners, per-binding traffic and a single-user reset without modifying system services.

## 1stream geosite data

`1stream.sh` generates the optional `1stream.dat` domain/geosite asset. It does not include GeoIP data and is separate from the user, node and Bot-management components.
