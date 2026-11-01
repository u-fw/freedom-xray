package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/net/proxy"
)

// BotService wraps Telegram bot lifecycle and handlers
type BotService struct {
	cfg            *BotConfig
	tg             *telegramClient
	stopCh         chan struct{}
	collectorReady bool
	// startupNotice is set by main after a successful one-time migration and is
	// delivered once the polling loop has started.
	startupNotice htmlText
}

func telegramHTTPClient(socksAddress string) (*http.Client, error) {
	httpClient := &http.Client{Timeout: 30 * time.Second}
	if socksAddress != "" {
		dialer, err := proxy.SOCKS5("tcp", socksAddress, nil, proxy.Direct)
		if err != nil {
			return nil, fmt.Errorf("failed to create socks5 dialer: %w", err)
		}
		contextDialer, ok := dialer.(proxy.ContextDialer)
		if !ok {
			return nil, fmt.Errorf("socks5 dialer does not support cancellation")
		}
		httpClient.Transport = &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				// net/http can detach a dial from the original request; bound it
				// even when the proxy accepts TCP but never finishes SOCKS negotiation.
				ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
				defer cancel()
				return contextDialer.DialContext(ctx, network, addr)
			},
		}
	}
	return httpClient, nil
}

// NewBotService initializes Telegram Bot with optional SOCKS5 proxy
func NewBotService(cfg *BotConfig) (*BotService, error) {
	if cfg.Token == "" {
		return nil, fmt.Errorf("bot token is empty")
	}

	SetXrayBinPath(cfg.XrayBinPath)
	httpClient, err := telegramHTTPClient(cfg.Socks5Proxy)
	if err != nil {
		return nil, err
	}

	tg, err := newTelegramClient(cfg.Token, "", httpClient)
	if err != nil {
		return nil, fmt.Errorf("init telegram bot error: %w", err)
	}
	username, botID, err := tg.identity()
	if err != nil {
		return nil, fmt.Errorf("init telegram bot error: %w", err)
	}

	log.Printf("[Bot] Authorized on account @%s (ID: %d)", username, botID)
	if err := publishBotReadiness(); err != nil {
		log.Printf("[Bot] Startup readiness marker failed: %v", err)
	}

	return &BotService{
		cfg:    cfg,
		tg:     tg,
		stopCh: make(chan struct{}),
	}, nil
}

// Start begins the polling loop and periodic report ticker
func (s *BotService) Start() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	updates, err := s.tg.updates(ctx)
	if err != nil {
		return fmt.Errorf("start Telegram polling: %w", err)
	}
	if s.startupNotice != "" {
		s.notifyAdmins(s.startupNotice)
	}

	// Independently persist traffic, even when no report or chat is requested.
	if !s.collectorReady {
		go s.runTrafficSampler()
	}
	go s.runLimitEnforcer()

	// Start periodic report if enabled
	if s.cfg.ReportHours > 0 {
		go s.runPeriodicReport()
	}

	log.Println("[Bot] Long polling started. Waiting for messages...")

	for {
		select {
		case <-s.stopCh:
			log.Println("[Bot] Stopped polling.")
			return nil
		case update, ok := <-updates:
			if !ok {
				return errors.New("telegram update stream closed")
			}
			s.handleUpdate(update)
		}
	}
}

// Stop cleanly stops the bot
func (s *BotService) Stop() {
	if _, err := snapshotTraffic(s.cfg.XrayConfigPath, s.cfg.XrayBinPath, s.cfg.StatsAPIServer); err != nil {
		log.Printf("[Traffic] shutdown snapshot skipped: %v", err)
	}
	close(s.stopCh)
}

func (s *BotService) handleUpdate(update tgUpdate) {
	// 1. Auth verification
	var senderID int64
	var chatID int64
	var isCallback bool

	if update.Message != nil && update.Message.From != nil {
		senderID = update.Message.From.ID
		chatID = update.Message.Chat.ID
	} else if update.CallbackQuery != nil && update.CallbackQuery.From != nil {
		senderID = update.CallbackQuery.From.ID
		if update.CallbackQuery.Message != nil {
			chatID = update.CallbackQuery.Message.Chat.ID
		}
		isCallback = true
	} else {
		return
	}

	if !s.cfg.IsAdmin(senderID) {
		log.Printf("[Bot] Unauthorized access attempt from User ID: %d", senderID)
		if isCallback && update.CallbackQuery != nil {
			// Stop the client-side spinner on unauthorized button presses.
			if err := s.tg.answerCallback(update.CallbackQuery.ID, tr(s.cfg.Language, "auth.denied")); err != nil {
				log.Printf("[Bot] answer unauthorized callback failed: %v", err)
			}
		}
		if !isCallback && update.Message != nil && update.Message.Chat.IsPrivate() {
			if _, err := s.tg.sendText(chatID, string(trh(s.cfg.Language, "auth.unauthorized", senderID)), "HTML", nil, false); err != nil {
				log.Printf("[Bot] send unauthorized notice failed: %v", err)
			}
		}
		return
	}

	// Never expose keys or permit management from group chats.
	// In a private Telegram chat, chat ID equals the authorized user's ID.
	if chatID != senderID || (update.Message != nil && !update.Message.Chat.IsPrivate()) ||
		(update.CallbackQuery != nil && update.CallbackQuery.Message != nil && !update.CallbackQuery.Message.Chat.IsPrivate()) {
		if isCallback {
			_ = s.tg.answerCallback(update.CallbackQuery.ID, tr(s.cfg.Language, "auth.privateonly"))
		}
		return
	}

	// 2. Dispatch
	if isCallback {
		s.handleCallback(update.CallbackQuery)
		return
	}

	if update.Message != nil {
		s.handleMessage(update.Message)
	}
}

func (s *BotService) handleMessage(msg *tgMessage) {
	s.maybeAutoDetectLanguage(msg)
	if s.handleNodeCreateInput(msg) {
		return
	}
	if s.handleNodePathInput(msg) {
		return
	}
	if s.handleUserLimitInput(msg) {
		return
	}
	if s.handleNativeInboundInput(msg) {
		return
	}
	if s.handleDNSInput(msg) {
		return
	}
	if s.handleOutboundInput(msg) {
		return
	}
	if s.handleBalancerInput(msg) {
		return
	}
	if s.handleGuidedRoutingInput(msg) {
		return
	}
	if s.handleRoutingInput(msg) {
		return
	}
	if s.handleWizardMessage(msg) {
		return
	}
	text := strings.TrimSpace(msg.Text)
	cmd := msg.Command()
	if s.handleUserManagementCommand(msg) {
		return
	}

	switch cmd {
	case "start", "menu":
		clearInputSessions(msg.Chat.ID)
		s.registerAdminCommands(msg.Chat.ID)
		s.sendMainMenu(msg.Chat.ID, 0)
	case "status":
		s.sendStatusCard(msg.Chat.ID, 0)
	case "language", "lang":
		s.changeLanguage(msg)
	case "online":
		s.sendOnlineCard(msg.Chat.ID, 0)
	case "onlinedoctor", "onlinediag":
		s.sendOnlineDiagnostics(msg.Chat.ID, 0)
	case "stats", "switches":
		s.sendStatsSwitches(msg.Chat.ID, 0)
	case "routing":
		clearInputSessions(msg.Chat.ID)
		s.sendRoutingCard(msg.Chat.ID, 0)
	case "xrayconfig":
		clearInputSessions(msg.Chat.ID)
		s.sendXraySectionsPage(msg.Chat.ID, 0, "")
	case "traffic", "usage":
		s.sendTrafficCard(msg.Chat.ID, 0)
	case "inboundtraffic":
		s.sendInboundTrafficCard(msg.Chat.ID, 0)
	case "nodes", "inbounds":
		s.sendNodesCard(msg.Chat.ID, 0)
	case "users", "link":
		s.sendUsersCard(msg.Chat.ID, 0)
	case "restart":
		s.askRestart(msg.Chat.ID, 0)
	case "backup":
		s.sendBackupFile(msg.Chat.ID)
	case "update":
		s.runSafeUpdates(msg.Chat.ID, 0, []string{"script", "bot"})
	case "version", "versions":
		s.sendVersionCard(msg.Chat.ID, 0)
	case "help":
		s.sendHelpCard(msg.Chat.ID, 0)
	case "cancel":
		s.sendOrEdit(msg.Chat.ID, 0, trh(s.cfg.Language, "cancel.none"), s.backToMainMarkup())
	default:
		if strings.HasPrefix(text, "/") {
			s.sendAlert(msg.Chat.ID, trh(s.cfg.Language, "command.unknown"))
		} else {
			s.sendMainMenuNotice(msg.Chat.ID, 0, trh(s.cfg.Language, "menu.unexpected_text"))
		}
	}
}

func (s *BotService) handleCallback(cb *tgCallback) {
	if err := s.tg.answerCallback(cb.ID, ""); err != nil {
		log.Printf("[Bot] answer callback failed: %v", err)
	}

	if cb.Message == nil || cb.Message.Chat == nil || cb.From == nil {
		return
	}

	chatID := cb.Message.Chat.ID
	msgID := cb.Message.MessageID
	data := cb.Data
	if data == "menu_main" || data == "menu_users" || data == "menu_nodes" || data == "menu_routing" || data == "menu_traffic" || data == "menu_online" || data == "menu_status" || data == "menu_tools" {
		clearInputSessions(chatID)
	}

	switch {
	case data == "menu_main":
		s.sendMainMenu(chatID, msgID)
	case data == "menu_tools":
		s.sendToolsCard(chatID, msgID)
	case data == "menu_status":
		s.sendStatusCard(chatID, msgID)
	case data == "service_ask:start":
		s.askServiceAction(cb, "start")
	case data == "service_ask:stop":
		s.askServiceAction(cb, "stop")
	case data == "service_ask:collector":
		s.askServiceAction(cb, "collector")
	case strings.HasPrefix(data, "service_confirm:"):
		s.resolveServiceAction(cb)
	case data == "menu_online":
		s.sendOnlineCard(chatID, msgID)
	case data == "menu_online_doctor":
		s.sendOnlineDiagnostics(chatID, msgID)
	case data == "online_repair_ask":
		s.askOnlineRepair(chatID, msgID)
	case data == "online_repair_confirm":
		s.doOnlineRepair(chatID, msgID)
	case data == "online_repair_cancel":
		s.sendOnlineDiagnostics(chatID, msgID)
	case data == "menu_stats":
		s.sendStatsSwitches(chatID, msgID)
	case data == "stats_repair_ask":
		s.askStatsRepair(chatID, msgID)
	case data == "stats_repair_confirm":
		s.doStatsRepair(chatID, msgID)
	case data == "menu_routing":
		s.sendRoutingCard(chatID, msgID)
	case strings.HasPrefix(data, "rt:"):
		s.handleGuidedRoutingCallback(cb)
	case strings.HasPrefix(data, "bl:"):
		s.handleBalancerCallback(cb)
	case strings.HasPrefix(data, "mo:"):
		s.handleObservatoryCallback(cb)
	case strings.HasPrefix(data, "ob:"):
		s.handleOutboundCallback(cb)
	case strings.HasPrefix(data, "dn:"):
		s.handleDNSCallback(cb)
	case strings.HasPrefix(data, "dh:"):
		s.handleDNSHostCallback(cb)
	case strings.HasPrefix(data, "ce:"):
		s.handleNativeConfigExportCallback(cb)
	case strings.HasPrefix(data, "xs:"):
		s.handleXraySectionCallback(cb)
	case strings.HasPrefix(data, "ni:"):
		s.handleNativeInboundCallback(cb)
	case strings.HasPrefix(data, "nc:"):
		s.handleNativeClientCallback(cb)
	case strings.HasPrefix(data, "routing_manage:"):
		s.beginRoutingWizard(cb)
	case strings.HasPrefix(data, "routing_confirm:"):
		s.resolveRoutingChange(cb, true)
	case strings.HasPrefix(data, "routing_cancel:"):
		s.resolveRoutingChange(cb, false)
	case data == "menu_traffic":
		s.sendTrafficCard(chatID, msgID)
	case data == "menu_inbound_traffic":
		s.sendInboundTrafficCard(chatID, msgID)
	case data == "menu_nodes":
		clearNodeCreateState(chatID)
		s.sendNodesCard(chatID, msgID)
	case strings.HasPrefix(data, "nodes_page:"):
		if offset, err := strconv.Atoi(strings.TrimPrefix(data, "nodes_page:")); err == nil {
			s.sendNodesCardPage(chatID, msgID, offset)
		}
	case data == "node_new_menu":
		s.sendNodeTemplatePicker(chatID, msgID)
	case strings.HasPrefix(data, "node_new:"):
		s.beginNodeCreate(chatID, cb.From.ID, msgID, NodeTemplate(strings.TrimPrefix(data, "node_new:")))
	case strings.HasPrefix(data, "node_cert_source:"):
		s.handleNodeCertSource(cb)
	case strings.HasPrefix(data, "node_create_confirm:"):
		s.confirmNodeCreate(cb, true)
	case strings.HasPrefix(data, "node_create_cancel:"):
		s.confirmNodeCreate(cb, false)
	case strings.HasPrefix(data, "node_delete_confirm:"):
		s.confirmNodeDelete(cb, true)
	case strings.HasPrefix(data, "node_delete_cancel:"):
		s.confirmNodeDelete(cb, false)
	case strings.HasPrefix(data, "node_delete:"):
		s.showNodeDeleteConfirm(chatID, cb.From.ID, msgID, strings.TrimPrefix(data, "node_delete:"))
	case strings.HasPrefix(data, "node_detail:") || strings.HasPrefix(data, "node_advanced:") || strings.HasPrefix(data, "node_basic:") || strings.HasPrefix(data, "node_mode:") || strings.HasPrefix(data, "node_path:") || strings.HasPrefix(data, "node_public_port:") || strings.HasPrefix(data, "node_sni:") || strings.HasPrefix(data, "node_short_id:") || strings.HasPrefix(data, "node_xhttp_host:") || strings.HasPrefix(data, "node_xhttp_padding:") || strings.HasPrefix(data, "node_listen_port:") || strings.HasPrefix(data, "node_listen_address:") || strings.HasPrefix(data, "node_dest:") || strings.HasPrefix(data, "node_target:") || strings.HasPrefix(data, "node_reality_key:") || strings.HasPrefix(data, "node_mldsa:") || strings.HasPrefix(data, "node_decryption:") || strings.HasPrefix(data, "node_cert_pair:") || strings.HasPrefix(data, "node_alpn:"):
		s.handleNodeCallback(cb)
	case strings.HasPrefix(data, "node_confirm:"):
		s.confirmNodeChange(cb, true)
	case strings.HasPrefix(data, "node_cancel:"):
		s.confirmNodeChange(cb, false)
	case data == "menu_users":
		s.sendUsersCard(chatID, msgID)
	case strings.HasPrefix(data, "users_page:"):
		if offset, err := strconv.Atoi(strings.TrimPrefix(data, "users_page:")); err == nil {
			s.sendUsersCardPage(chatID, msgID, offset)
		}
	case data == "menu_disabled_users":
		s.sendDisabledUsersCard(chatID, msgID)
	case strings.HasPrefix(data, "manage_enable:"):
		s.enableDisabledUserCallback(cb)
	case data == "manage_add":
		s.beginUserWizard(chatID, cb.From.ID, msgID, "name", "")
	case strings.HasPrefix(data, "manage_scope:"):
		s.handleWizardScope(cb)
	case strings.HasPrefix(data, "manage_rename:"):
		email := lookupManagedUserByKey(s.cfg.XrayConfigPath, strings.TrimPrefix(data, "manage_rename:"))
		if email == "" {
			s.sendAlert(chatID, trh(s.cfg.Language, "manage.userrenamed"))
			return
		}
		s.beginUserWizard(chatID, cb.From.ID, msgID, "rename", email)
	case data == "menu_restart_ask":
		s.askRestart(chatID, msgID)
	case data == "menu_restart_confirm":
		s.doRestart(chatID, msgID)
	case data == "menu_backup":
		s.sendBackupFile(chatID)
	case data == "menu_update":
		s.runSafeUpdates(chatID, msgID, []string{"script", "bot"})
	case data == "menu_update_center":
		s.showUpdatesMenu(chatID, msgID)
	case data == "menu_version":
		s.sendVersionCard(chatID, msgID)
	case data == "menu_help":
		s.sendHelpCard(chatID, msgID)
	case strings.HasPrefix(data, "lang_set:"):
		s.setLanguage(chatID, msgID, strings.TrimPrefix(data, "lang_set:"))
	case data == "update_all":
		s.runSafeUpdates(chatID, msgID, []string{"script", "bot"})
	case strings.HasPrefix(data, "update_ask:"):
		s.askUpdate(cb, strings.TrimPrefix(data, "update_ask:"))
	case strings.HasPrefix(data, "update_confirm:"):
		s.resolveUpdate(cb, true)
	case strings.HasPrefix(data, "update_cancel:"):
		s.resolveUpdate(cb, false)
	case data == "traffic_reset_ask":
		s.askTrafficReset(chatID, msgID)
	case data == "traffic_reset_confirm":
		s.doTrafficReset(chatID, msgID)
	case strings.HasPrefix(data, "user_links:"):
		s.handleUserLinksCallback(chatID, strings.TrimPrefix(data, "user_links:"))
	case strings.HasPrefix(data, "user_export_one:"):
		s.handleUserExportCallback(cb, "one")
	case strings.HasPrefix(data, "user_export_all:"):
		s.handleUserExportCallback(cb, "all")
	case strings.HasPrefix(data, "user_export_qrs:"):
		s.handleUserExportCallback(cb, "qrs")
	case strings.HasPrefix(data, "manage_confirm:"):
		s.resolveUserChange(cb, true)
	case strings.HasPrefix(data, "manage_cancel:"):
		s.resolveUserChange(cb, false)
	case strings.HasPrefix(data, "manage_traffic_confirm:"):
		s.resolveUserTrafficReset(cb, true)
	case strings.HasPrefix(data, "manage_traffic_cancel:"):
		s.resolveUserTrafficReset(cb, false)
	case strings.HasPrefix(data, "manage_user:") || strings.HasPrefix(data, "manage_rotate:") ||
		strings.HasPrefix(data, "manage_delete:") || strings.HasPrefix(data, "manage_disable:") || strings.HasPrefix(data, "manage_node:") ||
		strings.HasPrefix(data, "manage_quota:") || strings.HasPrefix(data, "manage_expiry:") || strings.HasPrefix(data, "manage_limitclear:") ||
		strings.HasPrefix(data, "manage_traffic:"):
		s.handleManagedUserCallback(cb)
	default:
		// Buttons from removed or renamed menus must not fail silently.
		clearInputSessions(chatID)
		s.sendMainMenuNotice(chatID, msgID, trh(s.cfg.Language, "menu.expired"))
	}
}

// handleUserLinksCallback resolves the short stable key stored in callback
// data. Telegram caps callback_data at 64 bytes, so raw emails are not safe.
// Index callbacks and raw email payloads from older builds still work.
func (s *BotService) handleUserLinksCallback(chatID int64, payload string) {
	if payload == "" {
		return
	}

	_, loadErr := LoadXrayInbounds(s.cfg.XrayConfigPath)
	if loadErr != nil {
		s.sendAlert(chatID, trh(s.cfg.Language, "users.linkreadfailed", loadErr.Error()))
		return
	}
	emails, dirErr := allDirectoryNames(s.cfg.XrayConfigPath)
	if dirErr != nil {
		s.sendAlert(chatID, trh(s.cfg.Language, "users.directoryreadfailed", dirErr.Error()))
		return
	}

	// Current format: a 16-hex-character email hash, stable across refreshes.
	if len(payload) == 16 {
		if _, err := hex.DecodeString(payload); err == nil {
			for _, email := range emails {
				if userCallbackKey(email) == payload {
					s.sendUserLinksAndQRs(chatID, email)
					return
				}
			}
			s.sendAlert(chatID, trh(s.cfg.Language, "users.listchanged"))
			s.sendUsersCard(chatID, 0)
			return
		}
	}

	// Older index-based callback.
	if idx, err := strconv.Atoi(payload); err == nil {
		if idx >= 0 && idx < len(emails) {
			s.sendUserLinksAndQRs(chatID, emails[idx])
			return
		}
		s.sendAlert(chatID, trh(s.cfg.Language, "users.listchanged"))
		s.sendUsersCard(chatID, 0)
		return
	}

	// Legacy raw-email callback from older builds.
	s.sendUserLinksAndQRs(chatID, payload)
}

// Telegram rejects a message body longer than 4096 characters. Cards are built
// from user lists, so a larger deployment can exceed that limit and lose the
// whole page. Keep headroom for Markdown escapes and split at line boundaries;
// continuation messages belong to the card and are replaced on refresh.
const telegramChunkLimit = 3500
const maxChunkAnchors = 512

type chunkAnchor struct {
	ChatID int64
	MsgID  int64
}

var chunkTails = struct {
	sync.Mutex
	items map[chunkAnchor][]int
	added map[chunkAnchor]time.Time
}{items: map[chunkAnchor][]int{}, added: map[chunkAnchor]time.Time{}}

func takeChunkTail(chatID int64, msgID int) []int {
	key := chunkAnchor{ChatID: chatID, MsgID: int64(msgID)}
	chunkTails.Lock()
	defer chunkTails.Unlock()
	ids := chunkTails.items[key]
	if len(ids) > 0 {
		delete(chunkTails.items, key)
		delete(chunkTails.added, key)
	}
	return ids
}

func storeChunkTail(chatID int64, msgID int, ids []int) {
	if len(ids) == 0 {
		return
	}
	key := chunkAnchor{ChatID: chatID, MsgID: int64(msgID)}
	chunkTails.Lock()
	defer chunkTails.Unlock()
	if len(chunkTails.items) >= maxChunkAnchors {
		cutoff := time.Now().Add(-time.Hour)
		for anchor, at := range chunkTails.added {
			if at.Before(cutoff) {
				delete(chunkTails.added, anchor)
				delete(chunkTails.items, anchor)
			}
		}
		if len(chunkTails.items) >= maxChunkAnchors {
			chunkTails.items = map[chunkAnchor][]int{}
			chunkTails.added = map[chunkAnchor]time.Time{}
		}
	}
	chunkTails.items[key] = ids
	chunkTails.added[key] = time.Now()
}

func (s *BotService) deleteChunkTail(chatID int64, msgID int) {
	for _, id := range takeChunkTail(chatID, msgID) {
		if err := s.tg.deleteMessage(chatID, id); err != nil {
			log.Printf("[Bot] delete continuation message failed: %v", err)
		}
	}
}

// sendChunkTail delivers the parts after the anchor. The anchor keeps the
// keyboard so a refresh edits the same card and replaces these messages.
func (s *BotService) sendChunkTail(chatID int64, anchor int, chunks []htmlText) {
	if len(chunks) == 0 {
		return
	}
	ids := make([]int, 0, len(chunks))
	for _, chunk := range chunks {
		sent, err := s.tg.sendText(chatID, string(chunk), "HTML", nil, false)
		if err != nil {
			if sent, err = s.tg.sendText(chatID, plainHTML(chunk), "", nil, false); err != nil {
				log.Printf("[Bot] send continuation failed: %v", err)
				continue
			}
		}
		ids = append(ids, sent)
	}
	storeChunkTail(chatID, anchor, ids)
}

// sendTextChunks sends a long, button-less message such as the periodic report.
func (s *BotService) sendTextChunks(chatID int64, text htmlText) {
	for _, chunk := range splitTelegramHTML(text, telegramChunkLimit) {
		if _, err := s.tg.sendText(chatID, string(chunk), "HTML", nil, false); err == nil {
			continue
		}
		if _, err := s.tg.sendText(chatID, plainHTML(chunk), "", nil, false); err != nil {
			log.Printf("[Bot] send message chunk failed: %v", err)
		}
	}
}

// sendOrEdit edits an existing message if msgID > 0, otherwise sends a new one.
// Bodies longer than one Telegram message are split; the first part stays in
// the edited/sent message and the remainder are tracked continuations.
func (s *BotService) sendOrEdit(chatID int64, msgID int, text htmlText, markup tgKeyboard) {
	chunks := splitTelegramHTML(text, telegramChunkLimit)
	if msgID > 0 {
		if err := s.tg.editText(chatID, msgID, string(chunks[0]), "HTML", &markup); err == nil {
			s.deleteChunkTail(chatID, msgID)
			s.sendChunkTail(chatID, msgID, chunks[1:])
			return
		} else if strings.Contains(err.Error(), "message is not modified") {
			// User pressed the same refresh button twice; the card is already
			// up to date, so there is nothing to resend.
			return
		} else {
			log.Printf("[Bot] edit message failed: %v", err)
		}

		// Malformed Markdown must not duplicate the card; retry the same
		// message as plain text before falling back to a new message.
		if err := s.tg.editText(chatID, msgID, plainHTML(chunks[0]), "", &markup); err == nil {
			s.deleteChunkTail(chatID, msgID)
			s.sendChunkTail(chatID, msgID, chunks[1:])
			return
		} else {
			log.Printf("[Bot] plain-text edit failed: %v", err)
		}
	}

	sent, err := s.tg.sendText(chatID, string(chunks[0]), "HTML", &markup, false)
	if err != nil {
		log.Printf("[Bot] send message failed: %v", err)
		// A malformed Markdown entity must not swallow the whole card; resend
		// as plain text without parse mode.
		if sent, err = s.tg.sendText(chatID, plainHTML(chunks[0]), "", &markup, false); err != nil {
			log.Printf("[Bot] plain-text fallback failed: %v", err)
			return
		}
	}
	s.sendChunkTail(chatID, sent, chunks[1:])
}

func (s *BotService) sendMainMenu(chatID int64, msgID int) {
	s.sendMainMenuNotice(chatID, msgID, "")
}

// sendMainMenuNotice shows the dashboard with an optional explanatory line above it.
func (s *BotService) sendMainMenuNotice(chatID int64, msgID int, notice htmlText) {
	nodes, _ := LoadXrayInbounds(s.cfg.XrayConfigPath)
	names, dirErr := allDirectoryNames(s.cfg.XrayConfigPath)
	userCount := len(sortedEmails(nodes))
	if dirErr == nil {
		userCount = len(names)
	}
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "unknown"
	}
	status := CheckXrayServiceStatus()
	state := "🔴"
	if status == "active" {
		state = "🟢"
	}
	updateState := tr(s.cfg.Language, "menu.updates.disabled")
	if s.cfg.AllowUpdates {
		updateState = tr(s.cfg.Language, "menu.updates.enabled")
	}
	content := trh(s.cfg.Language, "menu.dashboard", hostname, state, status, len(nodes), userCount, Version, updateState)
	if notice != "" {
		content = notice + "\n\n" + content
	}
	keyboard := mainMenuMarkup(s.cfg.Language)
	s.sendOrEdit(chatID, msgID, content, keyboard)
}
func (s *BotService) sendStatusCard(chatID int64, msgID int) {
	stats, err := GetSystemStats()
	if err != nil {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "status.failure", err.Error()), s.backToMainMarkup())
		return
	}

	xrayStatus := stats.XrayStatus
	statusIcon := "🟢"
	if xrayStatus != "active" {
		statusIcon = "🔴"
	}

	text := trh(s.cfg.Language, "status.summary",
		stats.Hostname, stats.Platform,
		stats.UptimeStr,
		statusIcon, xrayStatus,
		stats.CPUCores, stats.CPUUsagePct,
		stats.Load1, stats.Load5, stats.Load15,
		FormatBytes(stats.MemUsedBytes), FormatBytes(stats.MemTotalBytes), stats.MemUsagePct,
		FormatBytes(stats.DiskUsedBytes), FormatBytes(stats.DiskTotalBytes), stats.DiskUsagePct,
		stats.BotUptime,
	)

	markup := newKeyboard(
		newRow(
			newButton(tr(s.cfg.Language, "common.refresh"), "menu_status"),
			newButton(tr(s.cfg.Language, "common.back"), "menu_main"),
		),
	)
	if s.cfg.AllowMutations {
		markup.InlineKeyboard = append([][]tgButton{newRow(
			newButton(tr(s.cfg.Language, "service.start"), "service_ask:start"),
			newButton(tr(s.cfg.Language, "service.stop"), "service_ask:stop"),
			newButton(tr(s.cfg.Language, "menu.restart"), "menu_restart_ask"))}, markup.InlineKeyboard...)
	}

	s.sendOrEdit(chatID, msgID, text, markup)
}

func (s *BotService) sendTrafficCard(chatID int64, msgID int) {
	state, err := snapshotTraffic(s.cfg.XrayConfigPath, s.cfg.XrayBinPath, s.cfg.StatsAPIServer)
	if err != nil {
		cached, readErr := loadTrafficStore(s.cfg.XrayConfigPath)
		if readErr != nil {
			s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "traffic.readerror", err.Error(), readErr.Error()), s.backToMainMarkup())
			return
		}
		state = cached
	}
	// Remove orphaned entries left by previous releases, but only after
	// validating the actual managed users and suspended user records.
	clean, cleanErr := reconcileStoredTraffic(s.cfg.XrayConfigPath)
	if cleanErr != nil {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "traffic.directoryerror", cleanErr.Error()), s.backToMainMarkup())
		return
	}
	state = clean
	traffics := storedUserTraffic(state)
	var sb htmlBuilder
	sb.Add(trh(s.cfg.Language, "traffic.title"))
	if err != nil {
		sb.Add(trh(s.cfg.Language, "traffic.stale", err.Error()))
	}

	if len(traffics) == 0 {
		sb.Add(trh(s.cfg.Language, "traffic.empty"))
	} else {
		var totalAll uint64
		for _, u := range traffics {
			totalAll += u.Total
			sb.Add(trh(s.cfg.Language, "traffic.row",
				u.Email,
				FormatBytes(u.Uplink),
				FormatBytes(u.Downlink),
				FormatBytes(u.Total),
			))
		}
		sb.Add(trh(s.cfg.Language, "traffic.total", FormatBytes(totalAll)))
	}

	sb.Add(trh(s.cfg.Language, "traffic.note"))
	trafficMissing := []string{}
	if report, reportErr := inspectStatsSwitches(s.cfg.XrayConfigPath, s.cfg.StatsAPIServer); reportErr == nil {
		trafficMissing = report.MissingTrafficLevels
		if len(trafficMissing) > 0 {
			sb.Add(trh(s.cfg.Language, "stats.traffic.off", strings.Join(trafficMissing, ", ")))
		}
	}
	trafficActions := []tgButton{
		newButton(tr(s.cfg.Language, "common.refresh"), "menu_traffic"),
	}
	if s.cfg.AllowMutations {
		trafficActions = append(trafficActions, newButton(tr(s.cfg.Language, "traffic.reset"), "traffic_reset_ask"))
	}
	rows := [][]tgButton{
		newRow(trafficActions...),
		newRow(
			newButton(tr(s.cfg.Language, "traffic.inbounds"), "menu_inbound_traffic"),
			newButton(tr(s.cfg.Language, "common.back"), "menu_main"),
		),
	}
	if len(trafficMissing) > 0 {
		rows = append(rows, newRow(newButton(tr(s.cfg.Language, "stats.open"), "menu_stats")))
	}
	markup := newKeyboard(rows...)

	s.sendOrEdit(chatID, msgID, sb.HTML(), markup)
}

func (s *BotService) askTrafficReset(chatID int64, msgID int) {
	if !s.cfg.AllowMutations {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "manage.writesdisabled"), s.backToMainMarkup())
		return
	}
	text := trh(s.cfg.Language, "traffic.resetconfirm")
	markup := newKeyboard(
		newRow(
			newButton(tr(s.cfg.Language, "traffic.resetbutton"), "traffic_reset_confirm"),
			newButton(tr(s.cfg.Language, "common.cancel"), "menu_traffic"),
		),
	)
	s.sendOrEdit(chatID, msgID, text, markup)
}

func (s *BotService) doTrafficReset(chatID int64, msgID int) {
	if !s.cfg.AllowMutations {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "manage.writesdisabled"), s.backToMainMarkup())
		return
	}
	lock, lockErr := acquireConfigLock(s.cfg.XrayConfigPath, 10*time.Second)
	if lockErr != nil {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "traffic.lock", lockErr.Error()), s.backToMainMarkup())
		return
	}
	defer releaseConfigLock(lock)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := QueryTrafficStats(ctx, s.cfg.XrayBinPath, s.cfg.StatsAPIServer, true)
	if err != nil {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "traffic.reseterror", err.Error()), s.backToMainMarkup())
		return
	}

	if err := resetTrafficAllUsersLocked(s.cfg.XrayConfigPath); err != nil {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "traffic.partialreset", err.Error()), s.backToMainMarkup())
		return
	}
	text := trh(s.cfg.Language, "traffic.resetsuccess")
	markup := newKeyboard(
		newRow(
			newButton(tr(s.cfg.Language, "traffic.view"), "menu_traffic"),
			newButton(tr(s.cfg.Language, "common.back"), "menu_main"),
		),
	)
	s.sendOrEdit(chatID, msgID, text, markup)
}

func (s *BotService) sendNodesCard(chatID int64, msgID int) {
	s.sendNodesCardPage(chatID, msgID, 0)
}

const nodesPerPage = 8

func (s *BotService) sendNodesCardPage(chatID int64, msgID, offset int) {
	nodes, err := LoadXrayInbounds(s.cfg.XrayConfigPath)
	if err != nil {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "nodes.failure", err.Error()), s.backToMainMarkup())
		return
	}
	var nativeNodes []nativeInboundRecord
	if root, e := decodeBindingMigrationConfig(s.cfg.XrayConfigPath); e == nil {
		if records, e := nativeInboundRecords(root); e == nil {
			values, _ := nativeInboundValues(root)
			for i, record := range records {
				item, _ := getMap(values[i])
				if item["tag"] != "api-internal" {
					nativeNodes = append(nativeNodes, record)
				}
			}
		}
	}

	var sb htmlBuilder
	sb.Add(trh(s.cfg.Language, "nodes.title"))

	if offset < 0 || len(nodes) == 0 {
		offset = 0
	}
	if len(nodes) > 0 && offset >= len(nodes) {
		offset = ((len(nodes) - 1) / nodesPerPage) * nodesPerPage
	}
	end := offset + nodesPerPage
	if end > len(nodes) {
		end = len(nodes)
	}
	if len(nodes) == 0 {
		sb.Add(trh(s.cfg.Language, "nodes.empty"))
	} else {
		sb.Add(trh(s.cfg.Language, "nodes.page", offset/nodesPerPage+1, (len(nodes)+nodesPerPage-1)/nodesPerPage, len(nodes)))
		for i, n := range nodes[offset:end] {
			sni := n.SNI
			if sni == "" {
				sni = "-"
			}
			if n.Protocol == "vless" {
				sb.Add(trh(s.cfg.Language, "nodes.row",
					offset+i+1, n.Tag, n.SharePort(),
					strings.ToUpper(n.Protocol), strings.ToUpper(n.Network),
					strings.ToUpper(n.Security), sni, len(n.Clients)))
			} else {
				port := "-"
				if len(nativeNodes) == len(nodes) && nativeNodes[offset+i].Tag == safeInboundField(n.Tag) && nativeNodes[offset+i].Protocol == safeInboundField(n.Protocol) && nativeNodes[offset+i].Port != "" {
					port = nativeNodes[offset+i].Port
				}
				sb.Add(trh(s.cfg.Language, "nodes.native_row",
					offset+i+1, n.Tag, port, strings.ToUpper(n.Protocol),
					strings.ToUpper(n.Network), strings.ToUpper(n.Security), sni))
			}
		}
	}

	var nodeRows [][]tgButton
	for i, node := range nodes[offset:end] {
		if node.Protocol == "vless" {
			nodeRows = append(nodeRows, newRow(newButton("⚙ "+truncateLabel(node.Tag, 40), "node_detail:"+userCallbackKey(node.Tag))))
		} else {
			callback := "ni:page:0"
			if len(nativeNodes) == len(nodes) {
				record := nativeNodes[offset+i]
				if record.Tag == safeInboundField(node.Tag) && record.Protocol == safeInboundField(node.Protocol) {
					callback = nativeInboundCallback("v", record.Index, record.ETag)
				}
			}
			name := node.Tag
			if name == "" {
				name = tr(s.cfg.Language, "inbound.native.untagged")
			}
			nodeRows = append(nodeRows, newRow(newButton("⚙ "+truncateLabel(name, 40), callback)))
		}
	}
	var pages []tgButton
	if offset > 0 {
		prev := offset - nodesPerPage
		if prev < 0 {
			prev = 0
		}
		pages = append(pages, newButton(tr(s.cfg.Language, "common.prev"), fmt.Sprintf("nodes_page:%d", prev)))
	}
	if end < len(nodes) {
		pages = append(pages, newButton(tr(s.cfg.Language, "common.next"), fmt.Sprintf("nodes_page:%d", end)))
	}
	if len(pages) > 0 {
		nodeRows = append(nodeRows, pages)
	}
	nodeRows = append(nodeRows, newRow(newButton(tr(s.cfg.Language, "inbound.native.button"), "ni:page:0")))
	if s.cfg.AllowMutations {
		nodeRows = append(nodeRows, newRow(newButton(tr(s.cfg.Language, "node.create.button"), "node_new_menu")))
	}
	nodeRows = append(nodeRows, newRow(newButton(tr(s.cfg.Language, "common.refresh"), fmt.Sprintf("nodes_page:%d", offset)), newButton(tr(s.cfg.Language, "common.back"), "menu_main")))
	s.sendOrEdit(chatID, msgID, sb.HTML(), newKeyboard(nodeRows...))
}

func (s *BotService) sendUsersCard(chatID int64, msgID int) {
	s.sendUsersCardPage(chatID, msgID, 0)
}

// usersPerPage keeps the button count under Telegram's 100-button limit even
// with two buttons per user.
const usersPerPage = 20

func (s *BotService) sendUsersCardPage(chatID int64, msgID, offset int) {
	_, err := LoadXrayInbounds(s.cfg.XrayConfigPath)
	if err != nil {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "users.failure", err.Error()), s.backToMainMarkup())
		return
	}

	emails, dirErr := allDirectoryNames(s.cfg.XrayConfigPath)
	if dirErr != nil {
		s.sendAlert(chatID, trh(s.cfg.Language, "users.directoryerror", dirErr.Error()))
		return
	}

	total := len(emails)
	if offset < 0 {
		offset = 0
	}
	if total == 0 {
		offset = 0
	} else if offset >= total {
		offset = ((total - 1) / usersPerPage) * usersPerPage
	}
	end := offset + usersPerPage
	if end > total {
		end = total
	}

	var rows [][]tgButton
	for _, email := range emails[offset:end] {
		key := userCallbackKey(email)
		rows = append(rows, newRow(
			newButton(truncateLabel("👤 "+email, 40), "manage_user:"+key),
			newButton(tr(s.cfg.Language, "users.export"), "user_links:"+key),
		))
	}
	rows = append([][]tgButton{
		newRow(newButton(tr(s.cfg.Language, "users.add"), "manage_add"), newButton(tr(s.cfg.Language, "users.suspended"), "menu_disabled_users")),
	}, rows...)
	if total > usersPerPage {
		var nav []tgButton
		if offset > 0 {
			previous := offset - usersPerPage
			if previous < 0 {
				previous = 0
			}
			nav = append(nav, newButton(tr(s.cfg.Language, "common.prev"), "users_page:"+strconv.Itoa(previous)))
		}
		if end < total {
			nav = append(nav, newButton(tr(s.cfg.Language, "common.next"), "users_page:"+strconv.Itoa(end)))
		}
		if len(nav) > 0 {
			rows = append(rows, newRow(nav...))
		}
	}
	rows = append(rows, newRow(
		newButton(tr(s.cfg.Language, "common.back"), "menu_main"),
	))

	text := trh(s.cfg.Language, "users.title")
	if total == 0 {
		text = trh(s.cfg.Language, "users.empty")
	} else if total > usersPerPage {
		pages := (total + usersPerPage - 1) / usersPerPage
		text += trh(s.cfg.Language, "users.page", offset/usersPerPage+1, pages, total)
	}

	markup := newKeyboard(rows...)
	s.sendOrEdit(chatID, msgID, text, markup)
}

// Export links as separate plain-text messages: REALITY parameters can exceed
// Telegram's photo caption limit (1024 characters) and Markdown escaping can
// invalidate long VLESS URIs. QR is an optional attachment, never the only copy.
// Legacy callback entry point: open an explicit selection menu, never auto-send
// every QR and never silently drop a long REALITY URI.
func (s *BotService) sendUserLinksAndQRs(chatID int64, email string) {
	s.sendUserExportMenu(chatID, 0, email)
}

func (s *BotService) askRestart(chatID int64, msgID int) {
	if !s.cfg.AllowMutations {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "manage.writesdisabled"), s.backToMainMarkup())
		return
	}
	text := trh(s.cfg.Language, "restart.ask")
	markup := newKeyboard(
		newRow(
			newButton(tr(s.cfg.Language, "restart.confirm"), "menu_restart_confirm"),
			newButton(tr(s.cfg.Language, "common.cancel"), "menu_main"),
		),
	)
	s.sendOrEdit(chatID, msgID, text, markup)
}

func (s *BotService) doRestart(chatID int64, msgID int) {
	if !s.cfg.AllowMutations {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "manage.writesdisabled"), s.backToMainMarkup())
		return
	}
	s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "restart.running"), tgKeyboard{})

	lock, lockErr := acquireConfigLock(s.cfg.XrayConfigPath, 15*time.Second)
	if lockErr != nil {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "restart.lockfail", lockErr.Error()), s.backToMainMarkup())
		return
	}
	snapshotTrafficBestEffortLocked(s.cfg.XrayConfigPath, s.cfg.XrayBinPath, s.cfg.StatsAPIServer)
	err := RestartXrayService()
	releaseConfigLock(lock)
	time.Sleep(1 * time.Second)
	status := CheckXrayServiceStatus()

	var text htmlText
	if err != nil || status != "active" {
		reason := tr(s.cfg.Language, "restart.unknown")
		if err != nil {
			reason = err.Error()
		}
		text = trh(s.cfg.Language, "restart.failed", status, reason)
	} else {
		text = trh(s.cfg.Language, "restart.success")
	}

	s.sendOrEdit(chatID, msgID, text, s.backToMainMarkup())
}

func (s *BotService) sendBackupFile(chatID int64) {
	if !s.cfg.AllowSensitiveBackup {
		s.sendAlert(chatID, trh(s.cfg.Language, "backup.disabled"))
		return
	}
	if _, err := os.Stat(s.cfg.XrayConfigPath); err != nil {
		s.sendAlert(chatID, trh(s.cfg.Language, "backup.missing", s.cfg.XrayConfigPath))
		return
	}

	data, err := os.ReadFile(s.cfg.XrayConfigPath)
	if err != nil {
		s.sendAlert(chatID, trh(s.cfg.Language, "backup.missing", s.cfg.XrayConfigPath))
		return
	}
	caption := tr(s.cfg.Language, "backup.caption", time.Now().Format("2006-01-02 15:04:05"))
	if err := s.tg.sendDocument(chatID, filepath.Base(s.cfg.XrayConfigPath), data, caption); err != nil {
		log.Printf("[Bot] send backup document failed: %v", err)
	}
}

func (s *BotService) runTrafficSampler() {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	sample := func() {
		if _, err := snapshotTraffic(s.cfg.XrayConfigPath, s.cfg.XrayBinPath, s.cfg.StatsAPIServer); err != nil {
			log.Printf("[Traffic] background snapshot failed: %v", err)
		}
	}
	sample()
	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			sample()
		}
	}
}

func (s *BotService) runPeriodicReport() {
	ticker := time.NewTicker(time.Duration(s.cfg.ReportHours) * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.broadcastReport()
		}
	}
}

func (s *BotService) broadcastReport() {
	stats, _ := GetSystemStats()
	state, err := snapshotTraffic(s.cfg.XrayConfigPath, s.cfg.XrayBinPath, s.cfg.StatsAPIServer)
	if err != nil {
		state, _ = loadTrafficStore(s.cfg.XrayConfigPath)
	}
	traffics := storedUserTraffic(state)

	var sb htmlBuilder
	sb.Add(trh(s.cfg.Language, "report.title"))
	if stats != nil {
		sb.Add(trh(s.cfg.Language, "report.host",
			stats.Hostname, stats.XrayStatus,
			stats.CPUUsagePct, stats.MemUsagePct, stats.DiskUsagePct,
		))
	}

	sb.Add(trh(s.cfg.Language, "report.top"))
	if len(traffics) == 0 {
		sb.Add(trh(s.cfg.Language, "report.empty"))
	} else {
		for _, u := range traffics {
			sb.Add(trh(s.cfg.Language, "report.row",
				u.Email, FormatBytes(u.Uplink), FormatBytes(u.Downlink), FormatBytes(u.Total)))
		}
	}

	reportText := sb.HTML()
	for _, adminID := range s.cfg.AdminIDs {
		s.sendTextChunks(adminID, reportText)
	}
}

func (s *BotService) backToMainMarkup() tgKeyboard {
	return newKeyboard(
		newRow(
			newButton(tr(s.cfg.Language, "common.back"), "menu_main"),
		),
	)
}

// sendAlert sends a one-off HTML notification and logs delivery failures.
func (s *BotService) sendAlert(chatID int64, text htmlText) {
	if _, err := s.tg.sendText(chatID, string(text), "HTML", nil, false); err == nil {
		return
	}
	if _, err := s.tg.sendText(chatID, plainHTML(text), "", nil, false); err != nil {
		log.Printf("[Bot] send message failed: %v", err)
	}
}

// sortedEmails returns deterministic, de-duplicated user names so callback
// data can reference a short list index instead of a potentially long email.
// A migrated user owns one binding email per inbound; callers always mean the
// historical username, never an individual accounting identity.
func sortedEmails(nodes []InboundDetails) []string {
	set := make(map[string]bool)
	for _, n := range nodes {
		if n.Protocol != "vless" {
			continue
		}
		for _, c := range n.Clients {
			if c.Email != "" {
				set[bindingOwner(c.Email)] = true
			}
		}
	}

	emails := make([]string, 0, len(set))
	for email := range set {
		emails = append(emails, email)
	}
	sort.Strings(emails)
	return emails
}

// userCallbackKey returns a stable 16-character key for an email so inline
// callback data stays well under Telegram's 64-byte limit.
func userCallbackKey(email string) string {
	sum := sha256.Sum256([]byte(email))
	return hex.EncodeToString(sum[:8])
}

// truncateLabel shortens s to at most max bytes without splitting a UTF-8
// rune, appending an ellipsis when truncated.
func truncateLabel(s string, max int) string {
	if len(s) <= max {
		return s
	}
	const ellipsis = "…"
	limit := max - len(ellipsis)
	if limit < 0 {
		limit = 0
	}
	for limit > 0 && !utf8.RuneStart(s[limit]) {
		limit--
	}
	return s[:limit] + ellipsis
}
