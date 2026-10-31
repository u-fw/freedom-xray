package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"golang.org/x/net/proxy"
)

// BotService wraps Telegram bot lifecycle and handlers
type BotService struct {
	cfg    *BotConfig
	bot    *tgbotapi.BotAPI
	stopCh chan struct{}
 collectorReady bool
}

// NewBotService initializes Telegram Bot with optional SOCKS5 proxy
func NewBotService(cfg *BotConfig) (*BotService, error) {
	if cfg.Token == "" {
		return nil, fmt.Errorf("bot token is empty")
	}

	SetXrayBinPath(cfg.XrayBinPath)

	httpClient := &http.Client{Timeout: 30 * time.Second}
	if cfg.Socks5Proxy != "" {
		dialer, err := proxy.SOCKS5("tcp", cfg.Socks5Proxy, nil, proxy.Direct)
		if err != nil {
			return nil, fmt.Errorf("failed to create socks5 dialer: %w", err)
		}
		httpClient.Transport = &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return dialer.Dial(network, addr)
			},
		}
	}

	bot, err := tgbotapi.NewBotAPIWithClient(cfg.Token, tgbotapi.APIEndpoint, httpClient)
	if err != nil {
		return nil, fmt.Errorf("init telegram bot error: %w", err)
	}

	log.Printf("[Bot] Authorized on account @%s (ID: %d)", bot.Self.UserName, bot.Self.ID)
	if err := publishBotReadiness(); err != nil {
		log.Printf("[Bot] Startup readiness marker failed: %v",err)
	}

	return &BotService{
		cfg:    cfg,
		bot:    bot,
		stopCh: make(chan struct{}),
	}, nil
}

// Start begins the polling loop and periodic report ticker
func (s *BotService) Start() error {
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 30

	updates := s.bot.GetUpdatesChan(u)

	// Independently persist traffic, even when no report or chat is requested.
 if !s.collectorReady {go s.runTrafficSampler()}

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
		case update := <-updates:
			s.handleUpdate(update)
		}
	}
}

// Stop cleanly stops the bot
func (s *BotService) Stop() {
  if _,err:=snapshotTraffic(s.cfg.XrayConfigPath,s.cfg.XrayBinPath,s.cfg.StatsAPIServer);err!=nil{
    log.Printf("[Traffic] shutdown snapshot skipped: %v",err)
  }
  close(s.stopCh)
}

func (s *BotService) handleUpdate(update tgbotapi.Update) {
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
			if _, err := s.bot.Request(tgbotapi.NewCallback(update.CallbackQuery.ID, tr(s.cfg.Language,"auth.denied"))); err != nil {
				log.Printf("[Bot] answer unauthorized callback failed: %v", err)
			}
		}
		if !isCallback && update.Message != nil && update.Message.Chat.IsPrivate() {
			msg := tgbotapi.NewMessage(chatID, fmt.Sprintf(tr(s.cfg.Language,"auth.unauthorized"), senderID))
			msg.ParseMode = "Markdown"
			if _, err := s.bot.Send(msg); err != nil {
				log.Printf("[Bot] send unauthorized notice failed: %v", err)
			}
		}
		return
	}

	// Never expose keys or permit management from group chats.
	// In a private Telegram chat, chat ID equals the authorized user's ID.
	if chatID != senderID || (update.Message != nil && !update.Message.Chat.IsPrivate()) ||
		(update.CallbackQuery != nil && update.CallbackQuery.Message != nil && !update.CallbackQuery.Message.Chat.IsPrivate()) {
		if isCallback { _,_ = s.bot.Request(tgbotapi.NewCallback(update.CallbackQuery.ID, tr(s.cfg.Language,"auth.privateonly"))) }
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

func (s *BotService) handleMessage(msg *tgbotapi.Message) {
	s.maybeAutoDetectLanguage(msg)
	if s.handleNodePathInput(msg) { return }
	if s.handleWizardMessage(msg) { return }
	text := strings.TrimSpace(msg.Text)
	cmd := msg.Command()
	if s.handleUserManagementCommand(msg) { return }

	switch cmd {
	case "start", "menu":
		s.registerAdminCommands(msg.Chat.ID)
		s.sendMainMenu(msg.Chat.ID, 0)
	case "status":
		s.sendStatusCard(msg.Chat.ID, 0)
	case "language", "lang":
  s.changeLanguage(msg)
 case "online":
  s.sendOnlineCard(msg.Chat.ID,0)
 case "traffic", "usage":
		s.sendTrafficCard(msg.Chat.ID, 0)
	case "inboundtraffic":
		s.sendInboundTrafficCard(msg.Chat.ID,0)
	case "nodes", "inbounds":
		s.sendNodesCard(msg.Chat.ID, 0)
	case "users", "link":
		s.sendUsersCard(msg.Chat.ID, 0)
	case "restart":
		s.askRestart(msg.Chat.ID, 0)
	case "backup":
		s.sendBackupFile(msg.Chat.ID)
	case "update":
		s.runSafeUpdates(msg.Chat.ID,0,[]string{"script","bot"})
	case "version", "versions":
		s.sendVersionCard(msg.Chat.ID,0)
	case "help":
		s.sendHelpCard(msg.Chat.ID,0)
	default:
		if strings.HasPrefix(text, "/") {
			reply := tgbotapi.NewMessage(msg.Chat.ID, tr(s.cfg.Language,"command.unknown"))
			s.bot.Send(reply)
		} else {
			s.sendMainMenu(msg.Chat.ID, 0)
		}
	}
}

func (s *BotService) handleCallback(cb *tgbotapi.CallbackQuery) {
	callback := tgbotapi.NewCallback(cb.ID, "")
	if _, err := s.bot.Request(callback); err != nil {
		log.Printf("[Bot] answer callback failed: %v", err)
	}

	if cb.Message == nil {
		return
	}

	chatID := cb.Message.Chat.ID
	msgID := cb.Message.MessageID
	data := cb.Data

	switch {
	case data == "menu_main":
		s.sendMainMenu(chatID, msgID)
	case data == "menu_status":
		s.sendStatusCard(chatID, msgID)
	case data == "menu_online":
  s.sendOnlineCard(chatID,msgID)
 case data == "menu_traffic":
		s.sendTrafficCard(chatID, msgID)
	case data == "menu_inbound_traffic":
		s.sendInboundTrafficCard(chatID,msgID)
	case data == "menu_nodes":
		s.sendNodesCard(chatID, msgID)
	case strings.HasPrefix(data,"node_detail:") || strings.HasPrefix(data,"node_mode:") || strings.HasPrefix(data,"node_path:") || strings.HasPrefix(data,"node_public_port:") || strings.HasPrefix(data,"node_sni:"):
		s.handleNodeCallback(cb)
	case strings.HasPrefix(data,"node_confirm:"):
		s.confirmNodeChange(cb,true)
	case strings.HasPrefix(data,"node_cancel:"):
		s.confirmNodeChange(cb,false)
	case data == "menu_users":
		s.sendUsersCard(chatID, msgID)
	case data == "menu_disabled_users":
		s.sendDisabledUsersCard(chatID,msgID)
	case strings.HasPrefix(data,"manage_enable:"):
		s.enableDisabledUserCallback(cb)
	case data == "manage_add":
		s.beginUserWizard(chatID,cb.From.ID,msgID,"name","")
	case strings.HasPrefix(data,"manage_scope:"):
		s.handleWizardScope(cb)
	case strings.HasPrefix(data,"manage_rename:"):
		email:=lookupManagedUserByKey(s.cfg.XrayConfigPath,strings.TrimPrefix(data,"manage_rename:"))
		if email==""{s.sendAlert(chatID,tr(s.cfg.Language,"manage.userrenamed"));return}
		s.beginUserWizard(chatID,cb.From.ID,msgID,"rename",email)
	case data == "menu_restart_ask":
		s.askRestart(chatID, msgID)
	case data == "menu_restart_confirm":
		s.doRestart(chatID, msgID)
	case data == "menu_backup":
		s.sendBackupFile(chatID)
	case data == "menu_update":
		s.showUpdatesMenu(chatID,msgID)
	case data == "menu_version":
		s.sendVersionCard(chatID,msgID)
			case data == "menu_help":
				s.sendHelpCard(chatID,msgID)
			case strings.HasPrefix(data,"lang_set:"):
				s.setLanguage(chatID,msgID,strings.TrimPrefix(data,"lang_set:"))
	case strings.HasPrefix(data,"update_ask:"):
		s.askUpdate(cb,strings.TrimPrefix(data,"update_ask:"))
	case strings.HasPrefix(data,"update_confirm:"):
		s.resolveUpdate(cb,true)
	case strings.HasPrefix(data,"update_cancel:"):
		s.resolveUpdate(cb,false)
	case data == "traffic_reset_ask":
		s.askTrafficReset(chatID, msgID)
	case data == "traffic_reset_confirm":
		s.doTrafficReset(chatID, msgID)
	case strings.HasPrefix(data, "user_links:"):
		s.handleUserLinksCallback(chatID, strings.TrimPrefix(data, "user_links:"))
	case strings.HasPrefix(data,"user_export_one:"):
		s.handleUserExportCallback(cb,"one")
	case strings.HasPrefix(data,"user_export_all:"):
		s.handleUserExportCallback(cb,"all")
	case strings.HasPrefix(data,"user_export_qrs:"):
		s.handleUserExportCallback(cb,"qrs")
	case strings.HasPrefix(data, "manage_confirm:"):
		s.resolveUserChange(cb, true)
	case strings.HasPrefix(data, "manage_cancel:"):
		s.resolveUserChange(cb, false)
	case strings.HasPrefix(data, "manage_user:") || strings.HasPrefix(data, "manage_rotate:") ||
		strings.HasPrefix(data, "manage_delete:") || strings.HasPrefix(data, "manage_disable:") || strings.HasPrefix(data, "manage_node:"):
		s.handleManagedUserCallback(cb)
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
		s.sendAlert(chatID, tr(s.cfg.Language,"users.linkreadfailed",loadErr.Error()))
		return
	}
	emails,dirErr := allDirectoryNames(s.cfg.XrayConfigPath)
	if dirErr!=nil {s.sendAlert(chatID,tr(s.cfg.Language,"users.directoryreadfailed",dirErr.Error()));return}

	// Current format: a 16-hex-character email hash, stable across refreshes.
	if len(payload) == 16 {
		if _, err := hex.DecodeString(payload); err == nil {
			for _, email := range emails {
				if userCallbackKey(email) == payload {
					s.sendUserLinksAndQRs(chatID, email)
					return
				}
			}
			s.sendAlert(chatID, tr(s.cfg.Language,"users.listchanged"))
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
		s.sendAlert(chatID, tr(s.cfg.Language,"users.listchanged"))
		s.sendUsersCard(chatID, 0)
		return
	}

	// Legacy raw-email callback from older builds.
	s.sendUserLinksAndQRs(chatID, payload)
}

// sendOrEdit edits an existing message if msgID > 0, otherwise sends a new one
func (s *BotService) sendOrEdit(chatID int64, msgID int, text string, markup tgbotapi.InlineKeyboardMarkup) {
	if msgID > 0 {
		edit := tgbotapi.NewEditMessageTextAndMarkup(chatID, msgID, text, markup)
		edit.ParseMode = "Markdown"
		if _, err := s.bot.Send(edit); err == nil {
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
		plainEdit := tgbotapi.NewEditMessageTextAndMarkup(chatID, msgID, text, markup)
		if _, err := s.bot.Send(plainEdit); err == nil {
			return
		} else {
			log.Printf("[Bot] plain-text edit failed: %v", err)
		}
	}

	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = "Markdown"
	msg.ReplyMarkup = markup
	if _, err := s.bot.Send(msg); err == nil {
		return
	} else {
		log.Printf("[Bot] send message failed: %v", err)
	}

	// A malformed Markdown entity must not swallow the whole card; resend as
	// plain text without parse mode.
	plain := tgbotapi.NewMessage(chatID, text)
	plain.ReplyMarkup = markup
	if _, err := s.bot.Send(plain); err != nil {
		log.Printf("[Bot] plain-text fallback failed: %v", err)
	}
}

func (s *BotService) sendMainMenu(chatID int64,msgID int) {
 nodes,_:=LoadXrayInbounds(s.cfg.XrayConfigPath)
 names,dirErr:=allDirectoryNames(s.cfg.XrayConfigPath)
 userCount:=len(sortedEmails(nodes));if dirErr==nil{userCount=len(names)}
 hostname,_:=os.Hostname()
 if hostname==""{hostname="unknown"}
 status:=CheckXrayServiceStatus()
 state:="🔴";if status=="active"{state="🟢"}
 updateState:=tr(s.cfg.Language,"menu.updates.disabled");if s.cfg.AllowUpdates{updateState=tr(s.cfg.Language,"menu.updates.enabled")}
 content:=fmt.Sprintf(tr(s.cfg.Language,"menu.dashboard"),mdCode(hostname),state,mdCode(status),len(nodes),userCount,mdCode(Version),updateState)
 keyboard:=tgbotapi.NewInlineKeyboardMarkup(
  tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"menu.users"),"menu_users"),tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"menu.nodes"),"menu_nodes")),
  tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"menu.traffic"),"menu_traffic"),tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"menu.online"),"menu_online"),tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"menu.status"),"menu_status")),
  tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"menu.update"),"menu_update"),tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"menu.version"),"menu_version")),
  tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"menu.restart"),"menu_restart_ask"),tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"menu.backup"),"menu_backup")),
  tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"menu.help"),"menu_help"),tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"menu.refresh"),"menu_main")),
  tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(languageButtonLabel("en",s.cfg.Language),"lang_set:en"),tgbotapi.NewInlineKeyboardButtonData(languageButtonLabel("zh-CN",s.cfg.Language),"lang_set:zh-CN")),
 )
 s.sendOrEdit(chatID,msgID,content,keyboard)
}
func (s *BotService) sendStatusCard(chatID int64, msgID int) {
	stats, err := GetSystemStats()
	if err != nil {
		s.sendOrEdit(chatID, msgID, tr(s.cfg.Language,"status.failure",mdEsc(err.Error())), s.backToMainMarkup())
		return
	}

	xrayStatus := stats.XrayStatus
	statusIcon := "🟢"
	if xrayStatus != "active" {
		statusIcon = "🔴"
	}

	text := fmt.Sprintf(
  tr(s.cfg.Language,"status.summary"),
		mdCode(stats.Hostname), mdEsc(stats.Platform),
		mdCode(stats.UptimeStr),
		statusIcon, mdCode(xrayStatus),
		stats.CPUCores, stats.CPUUsagePct,
		mdCode(stats.Load1), mdCode(stats.Load5), mdCode(stats.Load15),
		FormatBytes(stats.MemUsedBytes), FormatBytes(stats.MemTotalBytes), stats.MemUsagePct,
		FormatBytes(stats.DiskUsedBytes), FormatBytes(stats.DiskTotalBytes), stats.DiskUsagePct,
		mdCode(stats.BotUptime),
	)

	markup := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"common.refresh"), "menu_status"),
			tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"common.back"), "menu_main"),
		),
	)

	s.sendOrEdit(chatID, msgID, text, markup)
}

func (s *BotService) sendTrafficCard(chatID int64, msgID int) {
 state,err:=snapshotTraffic(s.cfg.XrayConfigPath,s.cfg.XrayBinPath,s.cfg.StatsAPIServer)
 if err!=nil {
  cached,readErr:=loadTrafficStore(s.cfg.XrayConfigPath)
  if readErr!=nil{s.sendOrEdit(chatID,msgID,tr(s.cfg.Language,"traffic.readerror",mdEsc(err.Error()),mdEsc(readErr.Error())),s.backToMainMarkup());return}
  state=cached
 }
 // Remove orphaned entries left by previous releases, but only after
 // validating the actual managed users and suspended user records.
 clean,cleanErr:=reconcileStoredTraffic(s.cfg.XrayConfigPath)
 if cleanErr!=nil{s.sendOrEdit(chatID,msgID,tr(s.cfg.Language,"traffic.directoryerror",mdEsc(cleanErr.Error())),s.backToMainMarkup());return}
 state=clean
 traffics:=storedUserTraffic(state)
 var sb strings.Builder
 sb.WriteString(tr(s.cfg.Language,"traffic.title"))
 if err!=nil {sb.WriteString(tr(s.cfg.Language,"traffic.stale",mdEsc(err.Error())))}

	if len(traffics) == 0 {
		sb.WriteString(tr(s.cfg.Language,"traffic.empty"))
	} else {
		var totalAll uint64
		for _, u := range traffics {
			totalAll += u.Total
			sb.WriteString(fmt.Sprintf(tr(s.cfg.Language,"traffic.row"),
				mdEsc(u.Email),
				FormatBytes(u.Uplink),
				FormatBytes(u.Downlink),
				FormatBytes(u.Total),
			))
		}
		sb.WriteString(tr(s.cfg.Language,"traffic.total",FormatBytes(totalAll)))
	}

 sb.WriteString(tr(s.cfg.Language,"traffic.note"))
	markup := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"common.refresh"), "menu_traffic"),
			tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"traffic.reset"), "traffic_reset_ask"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"traffic.inbounds"), "menu_inbound_traffic"),
			tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"common.back"), "menu_main"),
		),
	)

	s.sendOrEdit(chatID, msgID, sb.String(), markup)
}

func (s *BotService) askTrafficReset(chatID int64, msgID int) {
	text := tr(s.cfg.Language,"traffic.resetconfirm")
	markup := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"traffic.resetbutton"), "traffic_reset_confirm"),
			tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"common.cancel"), "menu_traffic"),
		),
	)
	s.sendOrEdit(chatID, msgID, text, markup)
}

func (s *BotService) doTrafficReset(chatID int64, msgID int) {
 lock,lockErr:=acquireConfigLock(s.cfg.XrayConfigPath,10*time.Second)
 if lockErr!=nil{s.sendOrEdit(chatID,msgID,tr(s.cfg.Language,"traffic.lock",mdEsc(lockErr.Error())),s.backToMainMarkup());return}
 defer releaseConfigLock(lock)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := QueryTrafficStats(ctx, s.cfg.XrayBinPath, s.cfg.StatsAPIServer, true)
	if err != nil {
		s.sendOrEdit(chatID, msgID, tr(s.cfg.Language,"traffic.reseterror",mdEsc(err.Error())), s.backToMainMarkup())
		return
	}

	if err := resetTrafficAllUsersLocked(s.cfg.XrayConfigPath); err != nil {
		s.sendOrEdit(chatID,msgID,tr(s.cfg.Language,"traffic.partialreset",mdEsc(err.Error())),s.backToMainMarkup())
		return
	}
	text := tr(s.cfg.Language,"traffic.resetsuccess")
	markup := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"traffic.view"), "menu_traffic"),
			tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"common.back"), "menu_main"),
		),
	)
	s.sendOrEdit(chatID, msgID, text, markup)
}

func (s *BotService) sendNodesCard(chatID int64, msgID int) {
	nodes, err := LoadXrayInbounds(s.cfg.XrayConfigPath)
	if err != nil {
		s.sendOrEdit(chatID, msgID, tr(s.cfg.Language,"nodes.failure",mdEsc(err.Error())), s.backToMainMarkup())
		return
	}

	var sb strings.Builder
	sb.WriteString(tr(s.cfg.Language,"nodes.title"))

	if len(nodes) == 0 {
		sb.WriteString(tr(s.cfg.Language,"nodes.empty"))
	} else {
		for i, n := range nodes {
			sni := n.SNI
			if sni == "" {
				sni = "-"
			}
			sb.WriteString(fmt.Sprintf(tr(s.cfg.Language,"nodes.row"),
				i+1, mdEsc(n.Tag), n.SharePort(),
				mdCode(strings.ToUpper(n.Protocol)), mdCode(strings.ToUpper(n.Network)),
				mdCode(strings.ToUpper(n.Security)), mdCode(sni),
				len(n.Clients),
			))
		}
	}

	var nodeRows [][]tgbotapi.InlineKeyboardButton
	for _,node:=range nodes {
		if node.Protocol=="vless" {
			nodeRows=append(nodeRows,tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("⚙ "+truncateLabel(node.Tag,40),"node_detail:"+userCallbackKey(node.Tag))))
		}
	}
	markup := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"common.refresh"), "menu_nodes"),
			tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"common.back"), "menu_main"),
		),
	)

	markup.InlineKeyboard=append(nodeRows,markup.InlineKeyboard...)
	s.sendOrEdit(chatID, msgID, sb.String(), markup)
}

func (s *BotService) sendUsersCard(chatID int64, msgID int) {
	_, err := LoadXrayInbounds(s.cfg.XrayConfigPath)
	if err != nil {
		s.sendOrEdit(chatID, msgID, tr(s.cfg.Language,"users.failure",mdEsc(err.Error())), s.backToMainMarkup())
		return
	}

	emails,dirErr := allDirectoryNames(s.cfg.XrayConfigPath)
	if dirErr!=nil {s.sendAlert(chatID,tr(s.cfg.Language,"users.directoryerror",dirErr.Error()));return}

	var rows [][]tgbotapi.InlineKeyboardButton
	for _, email := range emails {
		key := userCallbackKey(email)
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(truncateLabel("👤 "+email, 40),"manage_user:"+key),
			tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"users.export"), "user_links:"+key),
		))
	}
	rows = append([][]tgbotapi.InlineKeyboardButton{
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"users.add"),"manage_add"), tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"users.suspended"),"menu_disabled_users")),
	}, rows...)
	rows = append(rows, tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"common.back"), "menu_main"),
	))

	text := tr(s.cfg.Language,"users.title")
	if len(emails) == 0 {
		text = tr(s.cfg.Language,"users.empty")
	}

	markup := tgbotapi.NewInlineKeyboardMarkup(rows...)
	s.sendOrEdit(chatID, msgID, text, markup)
}

// Export links as separate plain-text messages: REALITY parameters can exceed
// Telegram's photo caption limit (1024 characters) and Markdown escaping can
// invalidate long VLESS URIs. QR is an optional attachment, never the only copy.
// Legacy callback entry point: open an explicit selection menu, never auto-send
// every QR and never silently drop a long REALITY URI.
func (s *BotService) sendUserLinksAndQRs(chatID int64,email string) {
 s.sendUserExportMenu(chatID,0,email)
}

func (s *BotService) askRestart(chatID int64, msgID int) {
	text := tr(s.cfg.Language,"restart.ask")
	markup := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"restart.confirm"), "menu_restart_confirm"),
			tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"common.cancel"), "menu_main"),
		),
	)
	s.sendOrEdit(chatID, msgID, text, markup)
}

func (s *BotService) doRestart(chatID int64, msgID int) {
	s.sendOrEdit(chatID, msgID, tr(s.cfg.Language,"restart.running"), tgbotapi.InlineKeyboardMarkup{})

	lock,lockErr:=acquireConfigLock(s.cfg.XrayConfigPath,15*time.Second)
 if lockErr!=nil{s.sendOrEdit(chatID,msgID,tr(s.cfg.Language,"restart.lockfail",mdEsc(lockErr.Error())),s.backToMainMarkup());return}
 snapshotTrafficBestEffortLocked(s.cfg.XrayConfigPath,s.cfg.XrayBinPath,s.cfg.StatsAPIServer)
 err := RestartXrayService()
 releaseConfigLock(lock)
	time.Sleep(1 * time.Second)
	status := CheckXrayServiceStatus()

	var text string
	if err != nil || status != "active" {
		reason := tr(s.cfg.Language,"restart.unknown")
		if err != nil {
			reason = err.Error()
		}
		text = fmt.Sprintf(tr(s.cfg.Language,"restart.failed"), mdCode(status), mdCode(reason))
	} else {
		text = tr(s.cfg.Language,"restart.success")
	}

	s.sendOrEdit(chatID, msgID, text, s.backToMainMarkup())
}

func (s *BotService) sendBackupFile(chatID int64) {
  if !s.cfg.AllowSensitiveBackup {
    s.sendAlert(chatID,tr(s.cfg.Language,"backup.disabled"))
    return
  }
	if _, err := os.Stat(s.cfg.XrayConfigPath); err != nil {
		s.sendAlert(chatID, tr(s.cfg.Language,"backup.missing",s.cfg.XrayConfigPath))
		return
	}

	doc := tgbotapi.NewDocument(chatID, tgbotapi.FilePath(s.cfg.XrayConfigPath))
	doc.Caption = fmt.Sprintf(tr(s.cfg.Language,"backup.caption"), time.Now().Format("2006-01-02 15:04:05"))
	if _, err := s.bot.Send(doc); err != nil {
		log.Printf("[Bot] send backup document failed: %v", err)
	}
}

func (s *BotService) runTrafficSampler() {
 ticker:=time.NewTicker(15*time.Second)
 defer ticker.Stop()
 sample:=func(){if _,err:=snapshotTraffic(s.cfg.XrayConfigPath,s.cfg.XrayBinPath,s.cfg.StatsAPIServer);err!=nil{
  log.Printf("[Traffic] background snapshot failed: %v",err)
 }}
 sample()
 for {select{case <-s.stopCh:return;case <-ticker.C:sample()}}
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = ctx
 state,err:=snapshotTraffic(s.cfg.XrayConfigPath,s.cfg.XrayBinPath,s.cfg.StatsAPIServer)
 if err!=nil {state,_=loadTrafficStore(s.cfg.XrayConfigPath)}
 traffics:=storedUserTraffic(state)
	cancel()

	var sb strings.Builder
	sb.WriteString(tr(s.cfg.Language,"report.title"))
	if stats != nil {
		sb.WriteString(tr(s.cfg.Language,"report.host",
			mdCode(stats.Hostname), mdCode(stats.XrayStatus),
			stats.CPUUsagePct, stats.MemUsagePct, stats.DiskUsagePct,
		))
	}

	sb.WriteString(tr(s.cfg.Language,"report.top"))
	if len(traffics) == 0 {
		sb.WriteString(tr(s.cfg.Language,"report.empty"))
	} else {
		for _, u := range traffics {
			sb.WriteString(tr(s.cfg.Language,"report.row",
				mdCode(u.Email), FormatBytes(u.Uplink), FormatBytes(u.Downlink), FormatBytes(u.Total)))
		}
	}

	reportText := sb.String()
	for _, adminID := range s.cfg.AdminIDs {
		msg := tgbotapi.NewMessage(adminID, reportText)
		msg.ParseMode = "Markdown"
		if _, err := s.bot.Send(msg); err != nil {
			log.Printf("[Bot] send periodic report to %d failed: %v", adminID, err)
		}
	}
}

func (s *BotService) backToMainMarkup() tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"common.back"), "menu_main"),
		),
	)
}

// mdEsc escapes characters that Telegram legacy Markdown treats as entities.
// User emails, node tags, SNI values and error text all reach message bodies.
var mdEscaper = strings.NewReplacer(
	`\`, `\\`,
	`_`, `\_`,
	`*`, `\*`,
	"`", "\\`",
	`[`, `\[`,
)

func mdEsc(s string) string { return mdEscaper.Replace(s) }

// mdCode renders untrusted text inside a single-backtick code span. Backticks
// and newlines cannot be escaped inside code entities in legacy Markdown, so
// they are replaced instead of breaking the whole message.
func mdCode(s string) string {
	s = strings.ReplaceAll(s, "`", "'")
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return s
}

// sendAlert sends a plain one-off notification and logs delivery failures.
func (s *BotService) sendAlert(chatID int64, text string) {
	msg := tgbotapi.NewMessage(chatID, text)
	if _, err := s.bot.Send(msg); err != nil {
		log.Printf("[Bot] send message failed: %v", err)
	}
}

// sortedEmails returns deterministic, de-duplicated user emails so callback
// data can reference a short list index instead of a potentially long email.
func sortedEmails(nodes []InboundDetails) []string {
	set := make(map[string]bool)
	for _, n := range nodes {
		for _, c := range n.Clients {
			if c.Email != "" {
				set[c.Email] = true
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
