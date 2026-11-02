# UI/UX overhaul: status and remaining work

This document records what the 2026-11 Bot and terminal overhaul changed, what is deliberately not implemented yet, and how to implement each remaining item. It complements the [panel-free coverage audit](PANELLESS_COVERAGE.md), which lists functional gaps against a web panel.

## Implemented

| Area | Change | Main code |
| --- | --- | --- |
| Telegram transport | The unmaintained `go-telegram-bot-api` was replaced with [telego](https://github.com/mymmrac/telego). Builds use `-tags stdjson`, so telego encodes JSON with the standard library. Errors are scrubbed of the Bot token before they are logged or shown. | `bot/telegram_client.go`, `bot/telegram_types.go` |
| Message rendering | Messages use Telegram HTML. `htmlText` is the only type senders accept; `trh` renders locale templates (`*bold*`, `` `code` ``, fenced blocks) and escapes every argument, `tr` returns plain text for buttons and captions. Long messages split without breaking tags or entities. | `bot/render.go` |
| Input flows | One chat has at most one text-input flow (`clearInputSessions`). Invalid input keeps the flow open with a correction hint (`retryInput`). `/cancel` with nothing pending, stray text and unknown or outdated buttons are explained instead of ignored. | `bot/input_session.go`, `bot/telegram.go` |
| Navigation | Dashboard links always read "Main menu"; "Back" means one level up; screens opened from Tools return to Tools. | `bot/ux_menu.go` |
| Slow work | Update center, version check and update runs show a waiting notice and run in the background. A late result never overwrites a screen the admin opened meanwhile; update results are always delivered. | `bot/screen_async.go` |
| Confirmations | Update buttons and Xray restart, traffic reset and repair actions use one-time, owner-bound confirmations that expire after three minutes. A typed `/update` remains immediate consent. | `bot/action_tokens.go`, `bot/update_menu.go` |
| Daily tasks | Share links are tap-to-copy code spans with a copy button when Telegram's 256-character limit allows. The user card puts sharing first, shows a quota meter and expiry countdown, hides changes in read-only mode and is shown again after a confirmed change. The dashboard summarizes presence, traffic and limit alerts. The traffic card is paged. Inbound wizard defaults and yes/no steps are buttons. | `bot/export_menu.go`, `bot/management_bot.go`, `bot/dashboard_summary.go`, `bot/node_create.go` |
| Terminal safety | Generated configurations are tested by Xray before they replace the live file. Menu numbers are parsed as decimal. A rollback snapshot is only offered while the live file is still what the manager wrote. Firewall results are reported truthfully. Uninstall archives the configuration directory first and removes Xray without running a downloaded script. | `freedom.sh`, `tests/cli-bindings.sh` |
| Terminal links | With the Bot installed, the terminal exports user links through `freedom-bot agent users.export`, so they match Telegram exports (including `server_host` and TLS server names). The inbound overview no longer prints a client's credentials. | `freedom.sh` |

## Not implemented yet

### 1. Localizing Go-side messages (maintainer)

**Problem.** About 60 success summaries and 140 error texts are English strings created in Go (`errors.New`, `fmt.Errorf`, returned result messages). The Chinese interface shows them untranslated. Most come from `node_management.go`, `node_templates.go`, `user_directory.go`, `inbound_native.go`, `agent_mutations.go` and `updater.go`.

**Design.**

1. Add a localizable error type next to `render.go`:

   ```go
   // uiError is an error with a locale key. Error() stays English for logs and
   // the agent CLI; the Bot renders the key in the admin's language.
   type uiError struct {
       Key  string
       Args []any
   }

   func (e uiError) Error() string { return tr("en", e.Key, e.Args...) }

   func newUIError(key string, args ...any) error { return uiError{Key: key, Args: args} }
   ```

2. Replace user-facing validation errors with `newUIError("node.error.sni_invalid", value)` and add the key to both locale files. Keep wrapped system errors (`fmt.Errorf("...: %w", err)`) as they are; they are diagnostics, not interface text.
3. In the Bot, render errors through one helper instead of passing `err.Error()` to `trh`:

   ```go
   func localizedError(lang string, err error) htmlText {
       var ui uiError
       if errors.As(err, &ui) {
           return trh(lang, ui.Key, ui.Args...)
       }
       return esc(err.Error())
   }
   ```

4. For success summaries, return a `uiMessage{Key, Args}` (or a slice of them) from mutation functions instead of an English string. The agent CLI keeps emitting English text (and may add the key for automation); the Bot renders with `trh`.
5. Terminal: `cli_message` covers about 20 of roughly 700 strings. Either extend `locales/zh-CN.json` for the terminal, or, once the terminal calls the agent for most operations, let the agent return localized text so one catalog serves both front ends.

**Tests.** `TestI18nKeyCoverage` already enforces key parity, English-only English text and identical format verbs. Add a test that every `newUIError`/`trh`/`tr` literal key exists in `en.json` (scan the Go files), and render each `uiError` key in both languages with dummy arguments to catch `%!` artifacts.

### 2. Bot interaction framework

1. **One input-session store.** Ten maps still hold wizard state (`managementWizards`, `userLimitWizards`, `nodeCreateStates`, `nodePathWizards`, `routingWizards`, `guidedRoutingWizards`, `balancerWizards`, `outboundWizards`, `dnsWizards`, `nativeInboundWizards`), and `handleMessage` tries nine handlers in order. Replace them with one map keyed by chat: `inputSession{Kind string; AdminID int64; Expires time.Time; Data any}` plus a `map[string]func(*BotService, *tgMessage, inputSession)` registry. Migrate one wizard at a time; `clearInputSessions` already defines the "one flow per chat" rule the store must keep.
2. **One confirmation component.** `pendingRoutingChanges` covers 13 change kinds through a long if/else in `resolveRoutingChange`; users, nodes, node creation/deletion, service actions, updates and native exports have their own stores. Generalize `actionConfirmations` (`bot/action_tokens.go`) to carry a payload and a resolver function, so every confirmation shares TTL, ownership, one-time use and the expired-button message.
3. **Callback routing table.** Replace the `switch` in `handleCallback` with a `map[string]func(*BotService, *tgCallback)` keyed by callback prefix, registered by each feature file. The current `default` branch becomes the table's fallback.
4. **Paging helper.** Users, inbounds, routing rules and traffic each implement paging inline. Extract `pageBounds(total, offset, size)` and `pageButtons(prefix, offset, size, total)`. Apply it to the online card (currently capped at 25 users) and other long cards. Continuation chunks keep their keyboard on the first chunk; moving it to the last chunk requires tracking that chunk as the edit anchor.
5. **More background work.** Online diagnostics, statistics repair and every configuration change (Xray validation plus restart) still run on the polling goroutine. Use `renderLater`/`deliverLater`, or process updates per chat in worker goroutines so one admin never blocks another.

### 3. Bot screens

- **Add user:** let the admin choose several inbounds with toggle buttons, then land on the new user's share card. The current flow offers one choice (none, all, or a single inbound).
- **Inbound wizard:** a Back button per step, editing from the summary, and buttons for the certificate source paths.
- **Readable labels:** balancer strategies and observatory types still appear as configuration identifiers (`leastPing`, `burstObservatory`). Add locale labels with the identifier as secondary text.
- **Help:** the help card omits 11 of the 25 registered commands; generate it from the command list in `commands.go`.
- **Per-admin language:** `bot.json` stores one `language` for all admins; store a map of admin ID to language.
- **Sensitive exports:** with `allow_sensitive_backup` disabled, hide the Backup and Export JSON buttons (as balancers and observatories already do) or explain the setting on the button screen.
- **Limit edits:** after setting a quota or expiry, edit the user card in place with a notice instead of sending an alert plus a new card.

### 4. Terminal as a thin front end

1. **Make the Bot binary mandatory.** User management already requires it. Install it with the manager, then delete the Bash link builders (`show_user_node_links` fallback and the legacy branch of `list_and_show_nodes`), the four jq copies of the binding-email encoding and the Bash JSON traffic ledger.
2. **Inbound operations through the agent.** Replace the jq edits in `add_new_node_interactive`, `delete_node_interactive` and `modify_node_interactive` with `freedom-bot agent nodes.create|nodes.delete|nodes.update` (preview, then `--apply` with the returned `etag`). This removes the validation gap where Bash accepts tags and paths that Go rejects, and gives deletion the same impact checks as the Bot.
3. **Lock scope.** Collect all wizard input first, then take the configuration lock only for commit and restart. Never hold it during acme.sh or "Press Enter" prompts; Bot operations give up after waiting 10 to 25 seconds for the lock. Agent operations already take the lock themselves.
4. **Reads through the agent.** Use `users.list` for the user table (it includes unassigned users, which the terminal currently omits) and `traffic.*` reads for totals.

### 5. Security hardening

- **Xray installation and upgrades:** `bash -c "$installer"` runs XTLS/Xray-install from its main branch. Install from the official release ZIP verified against its `.dgst` SHA-256 instead (the Bot's core update already does this), or pin the installer to a commit and checksum.
- **acme.sh:** `curl https://get.acme.sh | sh` runs unpinned with errors silenced. Download a pinned release archive, verify its checksum and surface failures.
- **Manager self-update:** `update_freedom_script` checks only `bash -n`. Publish a checksum (or signature) for `freedom.sh` and verify it before replacing `/usr/local/bin/freedom`.
- **Firewall cleanup:** `auto_close_firewall_port` deletes every rule matching the port, including rules the administrator added. Record the rules the manager creates (ufw comment, firewalld zone and port, iptables rule spec) and remove only those.

### 6. Functional gaps

- **Backup and restore:** add agent operations that archive the whole configuration directory (Xray configuration, users, limits, metadata, public ports, accounting database, Bot settings) and restore it with validation, a pre-restore archive and an Xray test before restart. Expose them in the Bot behind `allow_sensitive_backup` and a confirmation.
- **Certificates:** issue and renew public certificates (ACME) from the Bot and agent, with renewal status on the inbound card.
- **Logs:** a read-only, redacted log viewer (recent Bot, collector and Xray journal lines with filters).
- **Connectivity tests:** test an outbound by sending a request through a temporary loopback SOCKS inbound and report latency and errors. Routing queries already report decisions but not reachability.
- **Fewer restarts:** add or remove clients through Xray's `HandlerService` (`AlterInbound`) instead of rewriting the configuration and restarting, keeping the file as the source of truth.
- **VMess and Trojan:** quotas, expiry, suspension and share links on par with VLESS users.

## Known limitations

- Telegram copy buttons hold at most 256 characters, so long post-quantum links are copied by tapping the code span instead.
- Message splitting counts bytes (3500 per chunk), which is conservative for Chinese text.
- A slow screen's result is dropped when the admin opens another screen in the same message; only update results are re-sent as a new message.
- Bot tests use a fake Telegram API at the HTTP level. Releases are verified against a real Xray core, not against a live Telegram account.
