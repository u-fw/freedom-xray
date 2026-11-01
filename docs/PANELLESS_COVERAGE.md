# Panel-free administration coverage

This is a coverage audit, not a claim of complete 3x-ui parity. Subscription features are excluded from the requested scope. Initial installation and permission setup are local. No production server upgrade is implied by a repository release.

The comparison uses the upstream [3x-ui configuration model](https://github.com/MHSanaei/3x-ui/blob/main/frontend/src/schemas/xray.ts), [Xray settings controller](https://github.com/MHSanaei/3x-ui/blob/main/internal/web/controller/xray_setting.go), and the [official Xray configuration modules](https://xtls.github.io/en/config/). The installed core remains the final compatibility check; this project does not add protocols by replacing it with another core.

| Area | Bot and agent CLI coverage | Remaining work or limits |
| --- | --- | --- |
| VLESS inbounds | Guided RAW/REALITY, XHTTP/REALITY and XHTTP/TLS templates; listener, transport and key changes; guarded deletion | Guided creation covers three templates. Other native transport combinations need further editing support. |
| Other inbounds | Native JSON creation/replacement, listener edits, deletion and full export | Protocol support depends on the installed Xray. Most protocol-specific settings use native JSON. |
| VLESS users | Independent identity directory; assignment, rename, rotation, deletion, suspension, restore, quotas, expiry and VLESS exports | Quota/expiry enforcement requires a running Bot and mutation permission. It restarts Xray and does not suspend an inbound's last remaining user. |
| VMess/Trojan accounts | Per-inbound list, generated credentials, email/level edits, credential rotation, deletion and native client export | Unified quotas, expiry, suspension and automatic connection links are incomplete. Other native account types still use whole-inbound JSON. |
| Routing rules | Full conditions and target details, guided field edits, reordering, deletion, named-rule JSON input, complete-rule export | Unknown extensions can be preserved and inspected but not all can be edited individually. Scripts limit safe managed changes. Route simulation is missing. |
| Outbounds | Native JSON CRUD/export, endpoint edits, default selection and reference-aware deletion | Runtime throughput accounting and genuine outbound connectivity tests need further work. |
| Balancers and observatories | Guided CRUD for selectors, strategies, fallback and probe settings; Bot/agent full native JSON creation, replacement and protected export; live preferred-target information | Live override controls remain incomplete. Preferred targets do not prove connectivity. Full replacement removes omitted fields; monitor types are mutually exclusive during managed creation. |
| DNS | Guided servers, order, flags, strategy and static host mappings; native JSON replacement/export | DNS script internals use native JSON; some guided operations are intentionally refused when references cannot be checked. |
| Logging, policy, FakeDNS | `/xrayconfig` and `xray.section.*`: safe inspection, native JSON replacement/export and reset | Runtime log viewing is separate and remains incomplete. Policy reset retains required statistics. |
| Core operations | Start/stop/restart, stats diagnostics/repair, Bot/script updates, explicit stable core upgrade and rollback | Configuration changes currently restart Xray; hot reconciliation is not implemented. |
| Backups and certificates | Sensitive Xray configuration export; existing certificate paths and managed self-signed TLS pairs | Complete backup/restore of Bot settings, certificates, sidecars and accounting is missing. Automatic public certificate issuance/renewal is missing. |
| Other Xray modules | Existing configuration is preserved by targeted edits | General Bot/CLI editors for metrics, environment, geodata, reverse and version constraints are incomplete. |
| Infrastructure | Local install and terminal tools | Cloud firewalls, DNS/CDN provisioning, multi-server orchestration and all kernel/firewall actions are not fully controlled through Bot/agent. |

## Verification scope

Bot interactions are tested against a captured Telegram API adapter, including private/admin authorization, confirmation ownership, expiry, replay prevention, stale configuration references and bounded file downloads. This does not claim a live Telegram account test.

The Go suite covers configuration transactions, candidate rejection and rollback behavior, native-account identity conflicts, section statistics invariants, native balancing JSON, field-case protection, stale confirmations, cancellation during downloads and redaction. `tests/xray-integration.py` uses an isolated real Xray core with loopback listeners to exercise authenticated VLESS/VMess/Trojan and SOCKS traffic, DNS host mappings, FakeDNS answers, balancer diagnostics, native balancing CLI changes and isolated accounting resets. Its temporary service stub prevents host systemd changes. Advanced balancing/monitor configuration is checked against the real core; this does not measure background-probe connectivity or validate every strategy parameter combination. Release verification checks both architecture checksums, build identity, latest/versioned asset consistency and the downloaded amd64 binary's integration flow.

See [Agent CLI](AGENT_CLI.md) for operation schemas, preview/apply semantics, sensitive-output opt-in and exact limitations. Unknown or unverified capabilities must not be inferred from a green test suite.
