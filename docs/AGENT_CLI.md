# Local agent CLI

`freedom-bot agent` is a non-interactive, local interface to the same manager used by the Telegram Bot. It does not start Telegram, accept arbitrary shell commands, or expose a web service. Version 1 of the JSON contract was introduced in Bot v1.4.0.

```bash
freedom-bot agent capabilities
freedom-bot agent version
sudo freedom-bot agent status.get
sudo freedom-bot agent nodes.list
```

The operation must immediately follow `agent`. Flags follow the operation:

```text
freedom-bot agent OPERATION [--config /path/to/bot.json] [--apply] [--include-sensitive]
```

The default configuration is `/usr/local/etc/xray/bot.json`. All operations except `capabilities` and `version` require root. Operations marked with an input object below read exactly one JSON object from standard input, up to 1 MiB. Unknown fields are rejected at the operation envelope level. Native outbound and DNS objects are passed through to Xray. Shell here-documents and pipes avoid putting credentials in command-line arguments or shell history.

Every invocation writes one JSON object to stdout. Diagnostics from underlying components may appear on stderr. Success and error envelopes are:

```json
{"schema_version":1,"operation":"nodes.list","ok":true,"data":[]}
{"schema_version":1,"operation":"example","ok":false,"error":{"code":"operation_failed","message":"reason"}}
```

Exit status is `0` on success, `1` for an execution failure, `2` for an unknown operation or invalid invocation, and `3` for a permission failure. Input validation inside an operation is an execution failure. Clients should check both the exit status and `ok`, branch on `error.code`, and treat `message` as human-readable rather than a stable API. Objects may acquire additional fields; ignore unknown response fields.

Mutations **preview by default**. Repeat the same request with `--apply` to execute it. Preview checks input and, for some operations, the current configuration in memory. It does not guarantee that the action will succeed: state can change, and certificate checks, credential generation, core validation, service operations and network access can occur only during apply. Preview is not a reservation or an Xray config test. Config mutations use the shared lock, test the candidate with Xray, back up the prior configuration, and restart Xray with rollback on failure where the underlying transaction supports it. Traffic resets and service or component updates have their own behavior and are not reversible configuration transactions.

Sensitive output is off by default. `users.export`, `outbounds.get`, `dns.get` and `backup.config` require `--include-sensitive`, because links, configuration, outbound settings and DNS URLs can contain credentials. Client identifiers, traffic and source IPs returned by other read operations are also sensitive operational data. Do not publish stdout, logs, exported links, backups, or Bot configuration. Telegram Bot conversations are not end-to-end encrypted; supply proxy credentials locally through this CLI.

Most traffic reads initialize or migrate the local SQLite ledger on first use, which can create the database, WAL files and a legacy backup. They do not reset traffic. Online observations are time-sensitive: check `fresh`; an old poll is unknown, not proof a user is offline.

## Operations

`—` means no stdin object. Examples show the complete top-level input; operation-specific constraints are enforced at apply as well.

| Operation | Input | Effect |
| --- | --- | --- |
| `capabilities`, `version` | — | Discover operations and build identity. |
| `status.get`, `config.get` | — | System/Xray status and non-secret Bot settings. |
| `config.server-host` | `{"host":"vpn.example.com"}` | Set the public REALITY link address; `""` restores discovery. Effective on next export. |
| `nodes.list` | — | List credential-free inbound summaries. |
| `nodes.get`, `nodes.delete` | `{"tag":"raw"}` | Inspect or delete an inbound; deletion reports impacts in preview and protects suspended bindings. |
| `nodes.create` | See node section | Create one of the three supported VLESS templates, initially without users. |
| `nodes.update` | `{"tag":"raw","kind":"port","value":"8443"}` | Change an allowlisted setting; see node section. |
| `nodes.public-port` | `{"tag":"cdn","public_port":443}` | Set an XHTTP/TLS public-port override; `0` clears it. |
| `users.list` | — | Users, assignments, suspension, usage and limits. |
| `users.get`, `users.export` | `{"user":"phone"}` | Read one user, or explicitly export sensitive VLESS links. |
| `users.add` | `{"user":"phone","node":"raw"}` | Create a user; omit `node` for no assignment, or use `"all"` for every eligible inbound. |
| `users.rename` | `{"user":"phone","new_name":"phone2"}` | Rename a user, preserving supported metadata. |
| `users.rotate`, `users.delete`, `users.disable`, `users.enable` | `{"user":"phone"}` | Rotate UUID, delete, suspend or restore. |
| `users.assign`, `users.unassign` | `{"user":"phone","node":"raw"}` | Add or remove a specific binding. |
| `limits.list` | — | List locally stored quota and expiry settings. |
| `limits.quota` | `{"user":"phone","quota_bytes":10737418240}` | Set total quota in bytes; `0` removes it. |
| `limits.expiry` | `{"user":"phone","expiry_unix":1893456000}` | Set a future Unix timestamp in seconds; `0` removes it. |
| `limits.clear` | `{"user":"phone"}` | Remove quota and expiry. |
| `traffic.read`, `online.read`, `online.diagnose`, `stats.inspect` | — | Read ledger, observed IPs, online policy diagnostics or statistics scaffold. |
| `traffic.snapshot`, `online.refresh`, `online.repair`, `stats.repair` | — | Sample/refresh or repair policy/API configuration. |
| `traffic.reset-user` | `{"user":"phone"}` | Reset one user's runtime and persistent counters. |
| `traffic.reset-all` | — | Reset all runtime and persistent user counters. |
| `collector.install` | — | Install/refresh and start the independent systemd sampler; may restart Xray to enable policies. Execute the installed `/usr/local/bin/freedom-bot`. |
| `bindings.migrate`, `bindings.revert` | — | Preview or perform per-inbound accounting identity migration/reversion. |
| `routing.read` | — | Read summarized rules, outbound tags and balancers. |
| `routing.add`, `routing.update` | `{"rule":{"ruleTag":"ads","domain":["domain:example.com"],"outboundTag":"block"}}` | Add or replace a named field rule; `routing.add` optionally accepts a 1-based `position`. |
| `routing.delete` | `{"rule_tag":"ads"}` | Delete a named rule. |
| `outbounds.list` | — | List tags, protocols and the default outbound. |
| `outbounds.get` | `{"tag":"proxy"}` | Export one native outbound; sensitive opt-in required. |
| `outbounds.put` | `{"outbound":{"tag":"block2","protocol":"blackhole"}}` | Add or replace a native Xray outbound; validated by the installed core. |
| `outbounds.delete` | `{"tag":"block2"}` | Delete an unreferenced non-default outbound. |
| `dns.inspect`, `dns.get` | — | Summary or full native DNS object; `dns.get` requires sensitive opt-in. |
| `dns.put` | `{"dns":{"servers":["1.1.1.1"]}}` | Replace the entire native DNS object. |
| `backup.config` | — | Export only Xray config.json, including credentials; sensitive opt-in required. It is not a complete backup of Bot settings, certificates, the ledger or sidecars. |
| `service.start`, `service.stop`, `service.restart` | — | Control Xray; stop/restart snapshot traffic best-effort first. |
| `updates.check` | — | Query installed and available manager, Bot and core versions; individual lookup errors appear in the result. |
| `updates.script`, `updates.bot`, `updates.core-rollback` | — | Update the script/Bot or restore the previous core binary. For `updates.bot`, execute the installed `/usr/local/bin/freedom-bot`. |
| `updates.core` | `{"version":"v26.10.31"}` | Install the specified published stable Xray release, with checksum and compatibility checks. |

`limits.*` enforcement depends on a running Bot with `allow_mutations`; exceeding a quota does not automatically re-enable an account when limits are raised. Binding migration/reversion can restart Xray and does not reconstruct historical per-node traffic. Resetting the ledger can lose unsampled runtime traffic. Native outbound deletion refuses the first/default outbound, the API tag, any referenced outbound, and all deletions while balancers exist. Routing editing requires a unique `ruleTag` and an existing target. The loopback API route is protected and must remain first. Unnamed existing rules and balancers are displayed but are not editable by these operations.

## Node inputs

Create inputs require `template`, `tag`, `port`, and usually `sni`. Templates are `tcp-reality`, `xhttp-reality` and `xhttp-tls`. Optional fields are `path`, `mode`, `xhttp_host`, `xhttp_padding`, `private_key`, `short_id`, `decryption`, `client_encryption`, `mldsa65_seed`, `generate_mldsa`, `generate_encryption`, `vision`, `certificate_source`, `cert_file`, `key_file`, and `alpn`.

For REALITY, a missing private key reuses an existing REALITY key when available or generates one; a missing short ID is generated. For XHTTP a missing path is generated. For XHTTP/TLS use `certificate_source: "existing"` with absolute `cert_file` and `key_file`, or `"self-signed"` for a managed self-signed pair. Self-signed certificates are not publicly trusted. A custom `decryption` must be paired with `client_encryption`; alternatively request `generate_encryption`. Generated ML-DSA applies to REALITY. Port/firewall rules, DNS records and CDN Origin Rules are external to this operation.

`nodes.update.kind` accepts `reality_sni`, `reality_short_id`, `path`, `mode`, `port`, `listen`, `xhttp_host`, `xhttp_padding`, `reality_dest`, `reality_target`, `reality_private_key`, `reality_mldsa_seed`, `decryption`, `tls_pair`, and `alpn`. Ordinary values use `value`; private key and ML-DSA use `secret`; `decryption` uses `secret` and its paired `client_secret`; `tls_pair` uses the absolute certificate path in `value` and key path in `secret`. Empty `listen` clears the explicit listener IP, empty ML-DSA disables it, and empty/`none` decryption disables VLESS encryption. ALPN is comma-separated. Some changes require clients to reimport links.

## Examples

Preview, then add a user to one node:

```bash
printf '%s\n' '{"user":"phone","node":"raw"}' | sudo freedom-bot agent users.add
printf '%s\n' '{"user":"phone","node":"raw"}' | sudo freedom-bot agent users.add --apply
```

Create a node, set the public address, then explicitly export the link:

```bash
printf '%s\n' '{"template":"tcp-reality","tag":"raw2","port":8443,"sni":"www.example.com"}' | sudo freedom-bot agent nodes.create --apply
printf '%s\n' '{"host":"vpn.example.com"}' | sudo freedom-bot agent config.server-host --apply
printf '%s\n' '{"user":"phone","node":"raw2"}' | sudo freedom-bot agent users.assign --apply
printf '%s\n' '{"user":"phone"}' | sudo freedom-bot agent users.export --include-sensitive
```

Add a route and inspect traffic:

```bash
printf '%s\n' '{"rule":{"ruleTag":"example","domain":["domain:example.com"],"outboundTag":"block"}}' | sudo freedom-bot agent routing.add --apply
sudo freedom-bot agent traffic.read
sudo freedom-bot agent online.diagnose
```

For an outbound containing credentials, provide the complete native Xray JSON through a protected file or a controlled pipe; do not pass it as an argument. For example, create a root-readable input file using a secure mechanism, then run `sudo freedom-bot agent outbounds.put --apply < /path/to/protected-input.json` and securely manage that file according to your environment.

## Scope and differences from 3x-ui

The Bot and CLI cover the operations above without a web panel, after local installation and permission setup. This is not complete 3x-ui parity: managed inbound creation is limited to the three VLESS templates; there is no public subscription server, full web panel, multi-server orchestration, per-IP/device enforcement, GUI balancer editor, automatic public certificate issuance/renewal, complete backup-and-restore workflow, or Bot/agent control of all firewall and kernel-tuning actions. Creating a node does not create cloud firewall rules, DNS records or CDN configuration. The terminal manager or external infrastructure may still be needed for those tasks.
