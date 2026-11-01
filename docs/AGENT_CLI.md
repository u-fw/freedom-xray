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

Sensitive output is off by default. `users.export`, `inbounds.get`, `inbounds.clients.export`, `outbounds.get`, `dns.get`, `routing.rule.get` and `backup.config` require `--include-sensitive`, because links, configuration, inbound/outbound settings and DNS URLs can contain credentials. `routing.rules`, `balancers.list` and `observatories.list` hide potentially credential-bearing extensions or probe URLs by default and return complete native objects only with `--include-sensitive`. Client identifiers, domains, traffic and source IPs returned by other read operations are also sensitive operational data. Do not publish stdout, logs, exported links, backups, or Bot configuration. Telegram Bot conversations are not end-to-end encrypted; supply credentials locally through this CLI when possible.

Most traffic reads initialize or migrate the local SQLite ledger on first use, which can create the database, WAL files and a legacy backup. They do not reset traffic. Online observations are time-sensitive: check `fresh`; an old poll is unknown, not proof a user is offline.

## Operations

`—` means no stdin object. Examples show the complete top-level input; operation-specific constraints are enforced at apply as well.

| Operation | Input | Effect |
| --- | --- | --- |
| `capabilities`, `version` | — | Discover operations and build identity. |
| `status.get`, `config.get` | — | System/Xray status and non-secret Bot settings. |
| `config.server-host` | `{"host":"vpn.example.com"}` | Set the public REALITY link address; `""` restores discovery. Effective on next export. |
| `nodes.list` | — | List credential-free inbound summaries. |
| `nodes.get`, `nodes.delete` | `{"tag":"raw"}` | Inspect or delete an inbound; deletion previews its impact and edit reference, and protects suspended bindings. |
| `nodes.create` | See node section | Create one of the three supported VLESS templates, initially without users. |
| `nodes.update` | `{"tag":"raw","kind":"port","value":"8443"}` | Change an allowlisted setting; see node section. |
| `nodes.public-port` | `{"tag":"cdn","public_port":443}` | Set an XHTTP/TLS public-port override; `0` clears it. |
| `inbounds.list` | — | List all native inbound summaries, including unnamed and protected inbounds, with edit references. |
| `inbounds.get` | `{"index":2,"etag":"<etag>"}` | Export a complete native inbound; requires sensitive opt-in. |
| `inbounds.add` | `{"list_etag":"<list_etag>","inbound":{"tag":"socks","listen":"127.0.0.1","port":1080,"protocol":"socks","settings":{"auth":"noauth"}}}` | Add a native non-VLESS inbound. |
| `inbounds.replace` | `{"index":2,"etag":"<etag>","inbound":{...}}` | Replace a complete non-VLESS object while retaining its tag and protocol. |
| `inbounds.patch` | `{"index":2,"etag":"<etag>","field":"port","value":"1081"}` | Change `port` or `listen` while retaining other settings; `""` clears the listen address. |
| `inbounds.remove` | `{"index":2,"etag":"<etag>","list_etag":"<list_etag>"}` | Preview removal; tagged inbounds additionally require the returned `preview.etag` as `impact_etag` when applying. |
| `inbounds.clients.list` | `{"index":2,"etag":"<etag>"}` | List VMess or Trojan clients without credentials. |
| `inbounds.clients.export` | `{"index":2,"etag":"<etag>","client_index":1}` | Export one native client JSON object; requires `--include-sensitive`. |
| `inbounds.clients.add` | `{"index":2,"etag":"<etag>","value":"person@example.com"}` | Create a client with an automatically generated credential. |
| `inbounds.clients.patch` | `{"index":2,"etag":"<etag>","client_index":1,"field":"level","value":"5"}` | Edit `email` or policy `level`; applying an email change requires the preview's `routing_etag`. |
| `inbounds.clients.rotate`, `inbounds.clients.remove` | `{"index":2,"etag":"<etag>","client_index":1}` | Rotate credentials or preview removal; applying removal requires the preview's `routing_etag`. |
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
| `xray.sections` | — | List advanced `log`, `policy` and `fakedns` sections with edit references and presence/type information. |
| `xray.section.inspect` | `{"name":"policy","etag":"<etag>"}` | Read common settings with file paths, unknown extensions and malformed values hidden; large collections are summarized. |
| `xray.section.get` | `{"name":"log","etag":"<etag>"}` | Export one complete native section value; requires `--include-sensitive`. |
| `xray.section.put` | `{"name":"log","etag":"<etag>","value":{"loglevel":"warning","dnsLog":false}}` | Create or replace a supported native section. |
| `xray.section.reset` | `{"name":"policy","etag":"<etag>"}` | Remove custom section settings; policy reset retains manager-required statistics switches. |
| `routing.read` | — | Read summarized rules, outbound tags and balancers. |
| `routing.rules` | — | List rules, positions, content references and list references. Unknown or potentially credential-bearing extensions are redacted unless `--include-sensitive` is supplied. |
| `routing.rule.get` | `{"index":2,"etag":"<etag>"}` | Export one complete native rule including hidden extensions; requires `--include-sensitive`. |
| `routing.rule.patch` | `{"index":2,"etag":"<etag>","field":"domain","value":["domain:example.com"]}` | Change one supported field without replacing the rest of the rule; `null` clears it. |
| `routing.rule.target` | `{"index":2,"etag":"<etag>","field":"outboundTag","value":"block"}` | Select an existing `outboundTag` or `balancerTag`. |
| `routing.rule.move` | `{"index":2,"etag":"<etag>","list_etag":"<list_etag>","direction":"down"}` | Move one place `up` or `down`; rechecks the complete rule list. |
| `routing.rule.delete` | `{"index":2,"etag":"<etag>"}` | Delete a rule, including an unnamed rule. |
| `routing.add`, `routing.update` | `{"rule":{"ruleTag":"ads","domain":["domain:example.com"],"outboundTag":"block"}}` | Add or replace a named field rule; `routing.add` optionally accepts a 1-based `position`. |
| `routing.delete` | `{"rule_tag":"ads"}` | Delete a named rule. |
| `routing.api.enable` | — | Enable the loopback-only routing API used for live diagnostics; validates and restarts Xray. |
| `balancers.list` | — | List balancers and content references; native extensions are hidden unless sensitive output is enabled. |
| `balancers.live` | `{"tag":"pool"}` | Read the current preferred candidates and override reported by Xray's routing API. |
| `balancers.put` | `{"balancer":{"tag":"pool","selector":["proxy-"],"strategy":{"type":"random"}}}` | Add a native balancer; replacing one also requires its current `etag`. |
| `balancers.patch` | `{"tag":"pool","etag":"<etag>","field":"strategy","value":"roundRobin"}` | Change `selector`, `strategy` or `fallbackTag`; `null` clears a fallback. |
| `balancers.delete` | `{"tag":"pool","etag":"<etag>"}` | Delete an unreferenced balancer. |
| `observatories.list` | — | List background/burst monitors and edit references, with probe URLs hidden by default. |
| `observatories.put` | `{"kind":"observatory","config":{"subjectSelector":["proxy-"],"probeInterval":"1m"}}` | Create a native monitor; replacing one also requires its current `etag`. |
| `observatories.selectors` | `{"kind":"observatory","etag":"<etag>","selectors":["proxy-"]}` | Replace monitored outbound tag prefixes. |
| `observatories.patch` | `{"kind":"observatory","etag":"<etag>","field":"interval","value":"1m"}` | Change probe `interval` or `url`; provide credential-bearing URLs via stdin. |
| `observatories.delete` | `{"kind":"observatory","etag":"<etag>"}` | Delete a monitor when no balancer requires the last monitor. |
| `outbounds.list` | — | List tags, protocols and the default outbound. |
| `outbounds.inspect` | — | List outbounds, safe endpoint summaries and edit references without credentials. |
| `outbounds.create` | `{"tag":"block2","protocol":"blackhole","list_etag":"<list_etag>"}` | Add a direct (`freedom`) or blocking (`blackhole`) outbound. |
| `outbounds.endpoint` | `{"index":2,"etag":"<etag>","field":"port","value":"443"}` | Change a supported proxy server `address` or `port` while retaining credentials and other settings. |
| `outbounds.default` | `{"index":2,"etag":"<etag>","list_etag":"<list_etag>"}` | Move an outbound to the default position; unmatched traffic may change. |
| `outbounds.remove` | `{"index":2,"etag":"<etag>"}` | Delete an unreferenced, non-default outbound by its edit reference. |
| `outbounds.get` | `{"tag":"proxy"}` | Export one native outbound; sensitive opt-in required. |
| `outbounds.put` | `{"outbound":{"tag":"block2","protocol":"blackhole"}}` | Add or replace a native Xray outbound; validated by the installed core. |
| `outbounds.delete` | `{"tag":"block2"}` | Delete an unreferenced non-default outbound. |
| `dns.inspect`, `dns.get` | — | Summary or full native DNS object; `dns.get` requires sensitive opt-in. |
| `dns.view` | — | Redacted server addresses, strategy, switches and a DNS configuration `etag`. |
| `dns.hosts` | — | List static host matches, targets and the DNS edit reference. |
| `dns.host.add`, `dns.host.update` | `{"etag":"<etag>","name":"full:example.com","targets":["192.0.2.1","2001:db8::1"]}` | Add or update one mapping. |
| `dns.host.rename` | `{"etag":"<etag>","name":"full:example.com","new_name":"example.com"}` | Change a mapping's match while preserving its targets. |
| `dns.host.remove` | `{"etag":"<etag>","name":"example.com"}` | Remove one mapping. |
| `dns.server.add` | `{"etag":"<etag>","value":"1.1.1.1"}` | Append a DNS server; can create the DNS object if absent. |
| `dns.server.update` | `{"etag":"<etag>","index":1,"value":"8.8.8.8"}` | Change one server address while retaining its other settings. |
| `dns.server.move` | `{"etag":"<etag>","index":2,"value":"up"}` | Move a server one position `up` or `down`. |
| `dns.server.remove` | `{"etag":"<etag>","index":1}` | Remove a DNS server if its tag is unreferenced. |
| `dns.strategy` | `{"etag":"<etag>","value":"UseIPv4"}` | Change the global query strategy; an empty value clears it. |
| `dns.flag` | `{"etag":"<etag>","field":"disableCache","enabled":true}` | Set an allowlisted DNS switch to the specified Boolean value. |
| `dns.put` | `{"dns":{"servers":["1.1.1.1"]}}` | Replace the entire native DNS object. |
| `backup.config` | — | Export only Xray config.json, including credentials; sensitive opt-in required. It is not a complete backup of Bot settings, certificates, the ledger or sidecars. |
| `service.start`, `service.stop`, `service.restart` | — | Control Xray; stop/restart snapshot traffic best-effort first. |
| `updates.check` | — | Query installed and available manager, Bot and core versions; individual lookup errors appear in the result. |
| `updates.script`, `updates.bot`, `updates.core-rollback` | — | Update the script/Bot or restore the previous core binary. For `updates.bot`, execute the installed `/usr/local/bin/freedom-bot`. |
| `updates.core` | `{"version":"v26.10.31"}` | Install the specified published stable Xray release, with checksum and compatibility checks. |

`limits.*` enforcement depends on a running Bot with `allow_mutations`; exceeding a quota does not automatically re-enable an account when limits are raised. Binding migration/reversion can restart Xray and does not reconstruct historical per-node traffic. Resetting the ledger can lose unsampled runtime traffic. Outbound deletion refuses the default, the API tag, and outbounds referenced by routing, proxy chains or balancer selectors. Deletion is refused when a routing script makes references impossible to verify. The older `routing.add`, `routing.update` and `routing.delete` operations require a unique `ruleTag`; the `routing.rule.*` operations also support unnamed rules. An existing target is required, and the loopback API route is protected and must remain first. Routing scripts cannot be combined with managed routing rules; stats repair refuses script configurations because their API handling requires manual verification.

For `routing.rule.*`, first call `routing.rules` and copy the selected rule's 1-based `index` and `etag` from that result. Moving also requires its `list_etag`. References are based on the full native configuration even when extension values are redacted. A changed rule, changed position, or changed list for a move causes rejection; read the rules again and review before retrying. Supported patch fields are `ruleTag`, `domain`, `domains`, `ip`, `port`, `network`, `protocol`, `source`, `sourceIP`, `sourcePort`, `localIP`, `localPort`, `user`, `inboundTag` and `vlessRoute`. Xray prioritizes `domains` over `domain` and `sourceIP` over `source` when both are present. Fields outside this list are preserved but cannot be patched individually. At least one recognized matching condition must remain.

`balancers.list` and `observatories.list` return `etag` values based on complete native objects, including redacted fields. Supply that value for every update or deletion; stale operations are rejected. Selector values match outbound tag prefixes. Selectors that could match the internal API tag are refused, and a referenced balancer cannot be deleted. `leastPing`, `leastLoad`, and a configured fallback require an existing `observatory` or `burstObservatory`; monitor coverage should include the selected outbounds. Health checks periodically send requests via those outbounds. The guided Bot flow creates a monitor using Xray's default probe URL; the local CLI can set a custom URL. Creating the other monitor type requires first removing the existing type; a monitor required by a balancer cannot be removed until its strategy or fallback is changed. Strategy-specific settings and other native fields are retained by individual field edits.

`balancers.live` requires Xray's `RoutingService`. The Bot offers to enable it after confirmation; the CLI provides `routing.api.enable`, with the usual preview and `--apply` behavior. Enabling it validates the API's loopback listener and routing exposure, tests the candidate, and restarts Xray. The API is powerful and may modify routes, so unsafe or ambiguous API configurations are refused. The returned candidates and override are Xray's current routing information, not a connectivity test, latency measurement, or list of active connections.

For a deletion guarded against changes after review, run `nodes.delete` without `--apply`, then include the returned `preview.etag` as `{"tag":"raw","etag":"<etag>"}` with `--apply`. The transaction rechecks the inbound, routing and reported sidecar impacts under the configuration lock; if they changed, read and review a new preview. Omitting `etag` preserves the existing unguarded CLI behavior.

`inbounds.list` provides complete-list and per-object references without returning credentials. Use `inbounds.add` for native non-VLESS objects, and `inbounds.replace` when the complete existing object is available; replacement overwrites all its settings and clients, so construct it from an explicitly sensitive `inbounds.get` export. `inbounds.patch` supports a single numeric port or an IP listen address and preserves the rest; native JSON is needed for ranges and Unix sockets. VLESS inbounds use the managed node operations, and internal API listeners are protected. Tagged `inbounds.remove` previews the impact and returns `preview.etag`; supply it as `impact_etag` on apply. Unnamed inbound removal instead uses the object and list references. Native non-VLESS accounts are not part of the managed VLESS user, quota, suspension or link workflows.

For VMess and Trojan, pass the inbound's `index` and `etag` to `inbounds.clients.list`, then use the returned 1-based client index. Creation generates a UUID or random password; the result deliberately omits it, so refresh the inbound reference and explicitly use `inbounds.clients.export --include-sensitive` to retrieve the native client JSON. That JSON is not a connection link. Rotation invalidates the old credential after the Xray restart. `email` and `level` edits preserve the client's other fields, and the chosen level receives the statistics policy switches. New emails accept 1–128 ASCII letters, digits, `_`, `.`, `@`, `+` and `-`. Before applying an email edit or removal, preview the operation and supply its `preview.routing_etag` as `routing_etag`. The preview lists 1-based routing rules that match the old email. Those rules are not rewritten: they may be shared by other accounts. A changed inbound or routing rules invalidates the reference; email edits and removals are refused when a routing script makes its references impossible to inspect. Each applied change validates the configuration and restarts Xray. Guided non-VLESS account suspension, quotas, expiry and automatic connection links are not implemented.

For supported Xray inbounds, both `settings.users` and `settings.clients` are recognized. A non-null `clients` array takes precedence, even when empty; otherwise `users` is used. Managed user updates preserve the active field and leave any inactive array untouched.

Xray keys user statistics by email across protocols. Creating or replacing a native inbound is refused if one of its account emails maps to a managed or suspended VLESS user; creating, renaming, assigning or restoring a managed user is also refused when that identity is already used by a native account. Existing conflicts are marked in the local statistics database. A configuration change is refused if an existing collision cannot be recorded, so correcting the email cannot silently erase the evidence of mixed traffic. `users.list` and `users.get` expose `traffic_ambiguous: true`, and the Bot warns on that user's card and notifies administrators when a configured quota is paused. Quota enforcement remains paused, including after the conflicting email is corrected, because historic totals may already be mixed; expiry enforcement remains independent. After reviewing the history, change the conflicting native email and use `traffic.reset-user` to reset that user's live and persisted counters and clear the ambiguity marker. This reset discards that user's recorded traffic; a reset is refused while the collision still exists. Managed configuration changes restart Xray; after changing the file outside the manager, restart Xray and verify that it loaded the corrected configuration before resetting. The marker records collisions detected while this version is running and cannot reconstruct conflicts that disappeared before the upgrade.

`outbounds.inspect` returns the 1-based `index`, `etag` and `list_etag` from the full outbound objects. An address or port edit and a delete require the selected object's reference; creating or changing the default also checks the whole list. Supported endpoint editing covers the unique server in VLESS, VMess, Trojan, Shadowsocks, SOCKS and HTTP outbounds, including their flat and legacy single-server forms. Multi-server legacy layouts and other protocols remain accessible through native outbound JSON rather than the guided endpoint editor. Credentials and transport settings are not returned by `outbounds.inspect` and are preserved during endpoint edits.

`dns.view` returns an `etag` based on the entire native DNS object, even though URL paths, queries and credentials are hidden. Supply the current reference to every guided DNS operation; updates, moves and deletions use a 1-based server index. Changing an object server's address preserves its per-server settings. Allowlisted flags are `disableCache`, `disableFallback`, `disableFallbackIfMatch`, `enableParallelQuery` and `useSystemHosts`; support depends on the installed Xray version. With a DNS script, server deletion and changes to built-in ordering, strategy or flags are refused because the script controls query behavior and may reference servers dynamically. Full native DNS configuration and advanced server settings remain available through `dns.get` and `dns.put`.

`dns.hosts` uses the same `etag` as `dns.view`; any DNS change invalidates it. A mapping can target one IP, replacement domain or `#` response code, or multiple IPs. Xray checks static mappings before DNS server queries, even with a DNS script; traffic must actually use Xray's built-in DNS for these mappings to take effect. Matches and targets are operational data. Unsupported or malformed native mappings are hidden from the guided view and remain available through sensitive `dns.get` and native JSON.

## Advanced Xray sections

Use `name` and `etag` from `xray.sections`. Supply the section value in `value`, without an additional section-name wrapper. Logging and policy values must be objects; FakeDNS accepts one pool object or a non-empty array. Each pool requires a CIDR `ipPool` and an explicit positive integer `poolSize`, smaller than the subnet address count. The tested Xray core fails to start with an omitted size or a pool occupying the whole subnet.

Replacing or resetting `policy` keeps user counters and online statistics enabled for level 0 and active client levels, plus the required inbound counters. Reset clears custom timeouts, buffers and other policy settings. Resetting logging leaves existing log files intact. Resetting FakeDNS removes explicit pools; Xray can initialize defaults if DNS still selects `fakedns`. Unique case variants are recognized, while ambiguous duplicate variants are refused. Applies recheck the section reference under the config lock, test the candidate with the installed core and restart with rollback. The Bot provides these workflows in `/xrayconfig`, including text/document input and protected complete-section export. `xray.section.get` requires `--include-sensitive`.

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

To edit an existing rule, retrieve `index` and `etag` from `sudo freedom-bot agent routing.rules`, substitute those values in the following JSON, preview it, and rerun with `--apply` after review:

```bash
printf '%s\n' '{"index":2,"etag":"<etag-from-routing.rules>","field":"domain","value":["domain:example.com"]}' | sudo freedom-bot agent routing.rule.patch
```

For an outbound containing credentials, provide the complete native Xray JSON through a protected file or a controlled pipe; do not pass it as an argument. For example, create a root-readable input file using a secure mechanism, then run `sudo freedom-bot agent outbounds.put --apply < /path/to/protected-input.json` and securely manage that file according to your environment.

## Scope and differences from 3x-ui

The Bot and CLI cover the operations above without a web panel, after local installation and permission setup. This is not complete 3x-ui parity: guided VLESS inbound creation is limited to three templates; other protocols use native JSON for inbound creation. VMess and Trojan have guided per-account management, while other non-VLESS account types still require native JSON. There is no public subscription server, full web panel, multi-server orchestration, per-IP/device enforcement, route simulation, automatic public certificate issuance/renewal, complete backup-and-restore workflow, or Bot/agent control of all firewall and kernel-tuning actions. Creating a node does not create cloud firewall rules, DNS records or CDN configuration. The terminal manager or external infrastructure may still be needed for those tasks.
