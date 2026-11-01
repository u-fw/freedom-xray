package main

import (
	"errors"
	"fmt"
	"log"
	"net"
	"net/url"
	"sort"
	"strings"
)

// A user only receives links for VLESS inbounds explicitly bound to that user.
// The global user directory may contain accounts without any inbound bindings.
type userExportEntry struct {
	Node   InboundDetails
	Client XrayClient
}

func boundUserExportEntries(nodes []InboundDetails, email string) []userExportEntry {
	var items []userExportEntry
	for _, node := range nodes {
		if node.Protocol != "vless" {
			continue
		}
		for _, client := range node.Clients {
			if bindingOwner(client.Email) == email {
				items = append(items, userExportEntry{Node: node, Client: client})
			}
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Node.Port != items[j].Node.Port {
			return items[i].Node.Port < items[j].Node.Port
		}
		return items[i].Node.Tag < items[j].Node.Tag
	})
	return items
}

// errExportHostUnavailable identifies failures that an administrator can fix
// by setting server_host in bot.json instead of a node-level configuration
// problem. Callers localize this case and show technical details separately.
var errExportHostUnavailable = errors.New("no usable public server address for REALITY links")

func validatedUserExportURI(item userExportEntry, serverHost string) (string, error) {
	if item.Client.ID == "" {
		return "", fmt.Errorf("Inbound %s has no user UUID", item.Node.Tag)
	}
	uri := item.Node.GenerateVlessURI(item.Client, serverHost)
	parsed, err := url.Parse(uri)
	if err != nil || parsed.Scheme != "vless" || parsed.User == nil {
		return "", fmt.Errorf("Inbound %s generated an invalid link", item.Node.Tag)
	}
	// Public-IP discovery can fail in a NAT, firewalled, or proxied environment.
	// Reject an empty or loopback authority instead of handing clients a link
	// that can never connect.
	host := parsed.Hostname()
	if host == "" {
		return "", fmt.Errorf("%w: inbound %s", errExportHostUnavailable, item.Node.Tag)
	}
	if ip := net.ParseIP(host); ip != nil && (ip.IsLoopback() || ip.IsUnspecified()) {
		return "", fmt.Errorf("%w: inbound %s resolved to %s", errExportHostUnavailable, item.Node.Tag, host)
	}
	if item.Node.Security == "reality" {
		q := parsed.Query()
		if q.Get("pbk") == "" {
			return "", fmt.Errorf("Inbound %s cannot generate the REALITY pbk public key; check the server private key", item.Node.Tag)
		}
		if item.Node.MLDSA65Seed != "" && q.Get("pqv") == "" {
			return "", fmt.Errorf("Inbound %s cannot generate the ML-DSA pqv parameter; check the Xray-core version and logs", item.Node.Tag)
		}
	}
	return uri, nil
}

// exportServerHost resolves the client-facing address once per export action.
// Failure is not fatal for TLS nodes (they are addressed by certificate SNI),
// so the error is logged and the empty host is left for validation to reject
// only the links that genuinely cannot work.
func (s *BotService) exportServerHost() string {
	host, err := resolveServerHost(s.cfg)
	if err != nil {
		log.Printf("[Export] %v", err)
	}
	return host
}

func (s *BotService) sendUserExportMenu(chatID int64, msgID int, email string) {
	nodes, err := LoadXrayInbounds(s.cfg.XrayConfigPath)
	if err != nil {
		s.sendOrEdit(chatID, msgID, tr(s.cfg.Language, "export.loadfailed", err.Error()), s.backToMainMarkup())
		return
	}
	entries := boundUserExportEntries(nodes, email)
	key := userCallbackKey(email)
	rows := make([][]tgButton, 0, len(entries)+3)
	for _, entry := range entries {
		rows = append(rows, newRow(
			newButton("🔗 "+truncateLabel(entry.Node.Tag, 29)+" :"+fmt.Sprint(entry.Node.SharePort()),
				"user_export_one:"+key+":"+userCallbackKey(entry.Node.Tag)),
		))
	}
	if len(entries) > 0 {
		rows = append(rows, newRow(
			newButton(tr(s.cfg.Language, "export.all"), "user_export_all:"+key),
		))
		rows = append(rows, newRow(
			newButton(tr(s.cfg.Language, "export.qrs"), "user_export_qrs:"+key),
		))
	}
	rows = append(rows, newRow(
		newButton(tr(s.cfg.Language, "export.backuser"), "manage_user:"+key),
		newButton(tr(s.cfg.Language, "export.userlist"), "menu_users"),
	))
	content := tr(s.cfg.Language, "export.menu.title", email, len(entries))
	if len(entries) == 0 {
		content = tr(s.cfg.Language, "export.menu.empty", email)
	}
	s.sendOrEdit(chatID, msgID, content, newKeyboard(rows...))
}

func (s *BotService) handleUserExportCallback(cb *tgCallback, mode string) {
	if cb.Message == nil {
		return
	}
	chatID := cb.Message.Chat.ID
	parts := strings.Split(cb.Data, ":")
	minParts := 2
	if mode == "one" {
		minParts = 3
	}
	if len(parts) != minParts {
		s.sendAlert(chatID, tr(s.cfg.Language, "export.invalid"))
		return
	}
	email := lookupManagedUserByKey(s.cfg.XrayConfigPath, parts[1])
	if email == "" {
		s.sendAlert(chatID, tr(s.cfg.Language, "export.usermissing"))
		return
	}
	nodes, err := LoadXrayInbounds(s.cfg.XrayConfigPath)
	if err != nil {
		s.sendAlert(chatID, tr(s.cfg.Language, "export.readfailed", err.Error()))
		return
	}
	entries := boundUserExportEntries(nodes, email)
	if len(entries) == 0 {
		s.sendAlert(chatID, tr(s.cfg.Language, "export.noinbounds"))
		return
	}
	if mode == "one" {
		for _, item := range entries {
			if userCallbackKey(item.Node.Tag) == parts[2] {
				s.exportOneUserNode(chatID, email, item, s.exportServerHost())
				return
			}
		}
		s.sendAlert(chatID, tr(s.cfg.Language, "export.unassigned"))
		return
	}
	s.exportAllUserNodes(chatID, email, entries, mode == "qrs")
}

func (s *BotService) sendUserLink(chatID int64, item userExportEntry, email, uri string) bool {
	label := tr(s.cfg.Language, "export.linklabel", item.Node.Tag, item.Node.SharePort(), email, item.Node.Network, item.Node.Security)
	if item.Node.SharePort() != item.Node.Port {
		label += tr(s.cfg.Language, "export.originport", item.Node.Port)
	}
	payload := label + uri
	if len(payload) > 3500 {
		name := fmt.Sprintf("freedom-node-%s-%d.txt", userCallbackKey(item.Node.Tag)[:8], item.Node.SharePort())
		if err := s.tg.sendDocument(chatID, name, []byte(uri+"\n"), tr(s.cfg.Language, "export.txtcaption", item.Node.Tag, item.Node.SharePort())); err != nil {
			log.Printf("[Bot] VLESS TXT send failed for node %s: %v", item.Node.Tag, err)
			s.sendAlert(chatID, tr(s.cfg.Language, "export.txtfail", item.Node.Tag, item.Node.Port, err.Error()))
			return false
		}
		return true
	}
	// Plain text is essential: post-quantum query parameters often contain
	// Markdown metacharacters. Never trim, reformat or Markdown-escape the URI.
	if _, err := s.tg.sendText(chatID, payload, "", nil, true); err != nil {
		log.Printf("[Bot] VLESS text send failed for node %s: %v", item.Node.Tag, err)
		s.sendAlert(chatID, tr(s.cfg.Language, "export.linkfail", item.Node.Tag, item.Node.Port, err.Error()))
		return false
	}
	return true
}

func (s *BotService) sendUserQR(chatID int64, item userExportEntry, email, uri string) bool {
	png, err := GenerateQRCodePNG(uri, 512)
	if err != nil {
		log.Printf("[Bot] QR generation failed for %s URI bytes=%d: %v", item.Node.Tag, len(uri), err)
		s.sendAlert(chatID, tr(s.cfg.Language, "export.qrfail", item.Node.Tag, item.Node.Port, len(uri), err.Error()))
		return false
	}
	name := fmt.Sprintf("freedom-qr-%s-%d.png", userCallbackKey(item.Node.Tag)[:8], item.Node.SharePort())
	if err = s.tg.sendPhoto(chatID, name, png, tr(s.cfg.Language, "export.qrcaption", item.Node.Tag, item.Node.SharePort(), email)); err != nil {
		log.Printf("[Bot] QR send failed for node %s: %v", item.Node.Tag, err)
		s.sendAlert(chatID, tr(s.cfg.Language, "export.qrsendfail", item.Node.Tag, item.Node.Port, err.Error()))
		return false
	}
	return true
}

// exportIssueText localizes failures an administrator can act on while keeping
// node-specific technical details visible.
func (s *BotService) exportIssueText(err error) string {
	if errors.Is(err, errExportHostUnavailable) {
		return tr(s.cfg.Language, "export.hostunavailable")
	}
	return err.Error()
}

func (s *BotService) exportOneUserNode(chatID int64, email string, item userExportEntry, host string) {
	uri, err := validatedUserExportURI(item, host)
	if err != nil {
		s.sendAlert(chatID, s.exportIssueText(err))
		return
	}
	if !s.sendUserLink(chatID, item, email, uri) {
		return
	}
	s.sendUserQR(chatID, item, email, uri)
}

func assembleBatchUserLinks(items []userExportEntry, host string) (string, []string) {
	links := make([]string, 0, len(items))
	var issues []string
	for _, item := range items {
		uri, err := validatedUserExportURI(item, host)
		if err != nil {
			issues = append(issues, err.Error())
			continue
		}
		links = append(links, uri)
	}
	return strings.Join(links, "\n") + "\n", issues
}

func (s *BotService) exportAllUserNodes(chatID int64, email string, items []userExportEntry, includeQR bool) {
	host := s.exportServerHost()
	data, issues := assembleBatchUserLinks(items, host)
	if len(strings.TrimSpace(data)) == 0 {
		localized := make([]string, 0, len(issues))
		for _, issue := range issues {
			localized = append(localized, s.localizedExportIssue(issue))
		}
		s.sendAlert(chatID, tr(s.cfg.Language, "export.noexportable", strings.Join(localized, "; ")))
		return
	}
	caption := tr(s.cfg.Language, "export.batchcaption", email, len(items)-len(issues), len(items))
	if err := s.tg.sendDocument(chatID, "freedom-vless-"+userCallbackKey(email)+".txt", []byte(data), caption); err != nil {
		log.Printf("[Bot] batch VLESS TXT failed for user %s: %v", email, err)
		s.sendAlert(chatID, tr(s.cfg.Language, "export.batchfail", err.Error()))
		return
	}
	if len(issues) > 0 {
		localized := make([]string, 0, len(issues))
		for _, issue := range issues {
			localized = append(localized, s.localizedExportIssue(issue))
		}
		s.sendAlert(chatID, tr(s.cfg.Language, "export.partial", strings.Join(localized, "\n")))
	}
	if !includeQR {
		return
	}
	for _, item := range items {
		uri, err := validatedUserExportURI(item, host)
		if err != nil {
			continue
		}
		s.sendUserQR(chatID, item, email, uri)
	}
}

// localizedExportIssue maps the shared host-resolution sentinel, which carries
// no language of its own, onto the active locale.
func (s *BotService) localizedExportIssue(issue string) string {
	if strings.Contains(issue, errExportHostUnavailable.Error()) {
		return tr(s.cfg.Language, "export.hostunavailable")
	}
	return issue
}
