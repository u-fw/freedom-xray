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
			if _, err := s.bot.Request(tgbotapi.NewCallback(update.CallbackQuery.ID, "⛔ 未授权访问")); err != nil {
				log.Printf("[Bot] answer unauthorized callback failed: %v", err)
			}
		}
		if !isCallback && update.Message != nil && update.Message.Chat.IsPrivate() {
			msg := tgbotapi.NewMessage(chatID, fmt.Sprintf(
				"⛔ 未授权访问 (Unauthorized)\n\n"+
					"您的 Telegram User ID 是: `%d`\n"+
					"请在 VPS 上使用 `freedom` 脚本将其加入管理员白名单。",
				senderID,
			))
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
		if isCallback { _,_ = s.bot.Request(tgbotapi.NewCallback(update.CallbackQuery.ID, "仅允许管理员私聊操作")) }
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
		s.showUpdatesMenu(msg.Chat.ID,0)
	case "version", "versions":
		s.sendVersionCard(msg.Chat.ID,0)
	case "help":
		s.sendHelpCard(msg.Chat.ID,0)
	default:
		if strings.HasPrefix(text, "/") {
			reply := tgbotapi.NewMessage(msg.Chat.ID, "未知命令，输入 /menu 或 /start 查看控制中心。")
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
	case data == "menu_traffic":
		s.sendTrafficCard(chatID, msgID)
	case data == "menu_inbound_traffic":
		s.sendInboundTrafficCard(chatID,msgID)
	case data == "menu_nodes":
		s.sendNodesCard(chatID, msgID)
	case strings.HasPrefix(data,"node_detail:") || strings.HasPrefix(data,"node_mode:") || strings.HasPrefix(data,"node_path:"):
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
		nodes,err:=LoadXrayInbounds(s.cfg.XrayConfigPath)
		if err!=nil{s.sendAlert(chatID,"读取用户失败");return}
		email:=lookupManagedUserEmail(nodes,strings.TrimPrefix(data,"manage_rename:"))
		if email==""{s.sendAlert(chatID,"用户不存在");return}
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
		s.sendAlert(chatID, "❌ 读取用户列表失败: "+loadErr.Error())
		return
	}
	emails,dirErr := allDirectoryNames(s.cfg.XrayConfigPath)
	if dirErr!=nil {s.sendAlert(chatID,"用户目录读取失败："+dirErr.Error());return}

	// Current format: a 16-hex-character email hash, stable across refreshes.
	if len(payload) == 16 {
		if _, err := hex.DecodeString(payload); err == nil {
			for _, email := range emails {
				if userCallbackKey(email) == payload {
					s.sendUserLinksAndQRs(chatID, email)
					return
				}
			}
			s.sendAlert(chatID, "用户列表已变化，请返回重新选择。")
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
		s.sendAlert(chatID, "用户列表已变化，请返回重新选择。")
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
 updateState:="🔒 更新权限关闭";if s.cfg.AllowUpdates{updateState="✅ 更新可用"}
 content:=fmt.Sprintf("🏠 *Freedom Bot 控制台*\n\n🖥️ 服务器：`%s`\n%s Xray：`%s` | 节点：`%d` | 用户：`%d`\n🤖 Bot 版本：`%s`\n%s\n\n选择要执行的操作：",mdCode(hostname),state,mdCode(status),len(nodes),userCount,mdCode(Version),updateState)
 keyboard:=tgbotapi.NewInlineKeyboardMarkup(
  tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("👥 用户管理","menu_users"),tgbotapi.NewInlineKeyboardButtonData("📡 节点管理","menu_nodes")),
  tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("📈 流量统计","menu_traffic"),tgbotapi.NewInlineKeyboardButtonData("🖥️ 运行状态","menu_status")),
  tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("⬆️ 软件更新","menu_update"),tgbotapi.NewInlineKeyboardButtonData("🧩 版本检测","menu_version")),
  tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("🔄 重启 Xray","menu_restart_ask"),tgbotapi.NewInlineKeyboardButtonData("💾 配置备份","menu_backup")),
  tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("📖 使用帮助","menu_help"),tgbotapi.NewInlineKeyboardButtonData("↻ 刷新面板","menu_main")),
 )
 s.sendOrEdit(chatID,msgID,content,keyboard)
}
func (s *BotService) sendStatusCard(chatID int64, msgID int) {
	stats, err := GetSystemStats()
	if err != nil {
		s.sendOrEdit(chatID, msgID, "❌ 获取系统状态失败: "+mdEsc(err.Error()), backToMainMarkup())
		return
	}

	xrayStatus := stats.XrayStatus
	statusIcon := "🟢"
	if xrayStatus != "active" {
		statusIcon = "🔴"
	}

	text := fmt.Sprintf(
		"📊 *VPS 系统实时健康监控*\n\n"+
			"🖥️ *主机名:* `%s` (%s)\n"+
			"⏱️ *系统开机时间:* `%s`\n"+
			"⚙️ *Xray 服务状态:* %s `%s`\n\n"+
			"🔥 *CPU:* `%d 核` | 使用率: `%.1f%%`\n"+
			"📈 *平均负载:* `%s, %s, %s`\n"+
			"🧠 *内存使用:* `%s / %s` (`%.1f%%`)\n"+
			"💾 *磁盘使用 (/):* `%s / %s` (`%.1f%%`)\n"+
			"🤖 *Bot 连续运行:* `%s`",
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
			tgbotapi.NewInlineKeyboardButtonData("⚡ 刷新", "menu_status"),
			tgbotapi.NewInlineKeyboardButtonData("🔙 返回主菜单", "menu_main"),
		),
	)

	s.sendOrEdit(chatID, msgID, text, markup)
}

func (s *BotService) sendTrafficCard(chatID int64, msgID int) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	traffics, err := QueryTrafficStats(ctx, s.cfg.XrayBinPath, s.cfg.StatsAPIServer, false)
	if err != nil {
		s.sendOrEdit(chatID, msgID, fmt.Sprintf(
			"❌ *获取流量失败*\n\n原因: `%s`\n\n请确认 Xray 正在运行且 StatsService (10085 端口) 已开启。", mdCode(err.Error())),
			backToMainMarkup())
		return
	}

	var sb strings.Builder
	sb.WriteString("📈 *用户流量统计实时排行榜*\n\n")

	if len(traffics) == 0 {
		sb.WriteString("暂无用户流量记录或 Xray 刚启动尚未产生数据。\n")
	} else {
		var totalAll uint64
		for _, u := range traffics {
			totalAll += u.Total
			sb.WriteString(fmt.Sprintf(
				"👤 *%s*\n  ⬆️ 上传: `%s` | ⬇️ 下载: `%s`\n  📊 总计: `%s`\n\n",
				mdEsc(u.Email),
				FormatBytes(u.Uplink),
				FormatBytes(u.Downlink),
				FormatBytes(u.Total),
			))
		}
		sb.WriteString(fmt.Sprintf("🌐 *全站总流量:* `%s`\n", FormatBytes(totalAll)))
	}

	markup := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("⚡ 刷新", "menu_traffic"),
			tgbotapi.NewInlineKeyboardButtonData("🔄 清零流量", "traffic_reset_ask"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("📡 节点流量", "menu_inbound_traffic"),
			tgbotapi.NewInlineKeyboardButtonData("🔙 返回主菜单", "menu_main"),
		),
	)

	s.sendOrEdit(chatID, msgID, sb.String(), markup)
}

func (s *BotService) askTrafficReset(chatID int64, msgID int) {
	text := "⚠️ *确认清零所有用户的流量统计吗？*\n\n此操作会重置 Xray 核心内存中当前记录的全部上传与下载量，无法撤回。"
	markup := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🔴 确认清零", "traffic_reset_confirm"),
			tgbotapi.NewInlineKeyboardButtonData("取消", "menu_traffic"),
		),
	)
	s.sendOrEdit(chatID, msgID, text, markup)
}

func (s *BotService) doTrafficReset(chatID int64, msgID int) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := QueryTrafficStats(ctx, s.cfg.XrayBinPath, s.cfg.StatsAPIServer, true)
	if err != nil {
		s.sendOrEdit(chatID, msgID, "❌ 清零失败: "+mdEsc(err.Error()), backToMainMarkup())
		return
	}

	if err := resetTrafficStore(s.cfg.XrayConfigPath); err != nil {
		s.sendOrEdit(chatID,msgID,"⚠️ Xray 内存计数器已清零，但本地累计流量重置失败: "+mdEsc(err.Error()),backToMainMarkup())
		return
	}
	text := "✅ *用户流量计数器和本地累计流量均已清零！*"
	markup := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("📊 查看流量统计", "menu_traffic"),
			tgbotapi.NewInlineKeyboardButtonData("🔙 返回主菜单", "menu_main"),
		),
	)
	s.sendOrEdit(chatID, msgID, text, markup)
}

func (s *BotService) sendNodesCard(chatID int64, msgID int) {
	nodes, err := LoadXrayInbounds(s.cfg.XrayConfigPath)
	if err != nil {
		s.sendOrEdit(chatID, msgID, "❌ 读取节点列表失败: "+mdEsc(err.Error()), backToMainMarkup())
		return
	}

	var sb strings.Builder
	sb.WriteString("📋 *当前托管业务节点列表*\n\n")

	if len(nodes) == 0 {
		sb.WriteString("未找到任何业务节点，请使用 `freedom` 脚本添加。\n")
	} else {
		for i, n := range nodes {
			sni := n.SNI
			if sni == "" {
				sni = "-"
			}
			sb.WriteString(fmt.Sprintf(
				"*%d. [%s] 端口: %d*\n"+
					"  • 协议: `%s` | 传输: `%s`\n"+
					"  • 安全层: `%s` | 伪装/SNI: `%s`\n"+
					"  • 绑定用户数: `%d` 个\n\n",
				i+1, mdEsc(n.Tag), n.Port,
				mdCode(strings.ToUpper(n.Protocol)), mdCode(strings.ToUpper(n.Network)),
				mdCode(strings.ToUpper(n.Security)), mdCode(sni),
				len(n.Clients),
			))
		}
	}

	var nodeRows [][]tgbotapi.InlineKeyboardButton
	for _,node:=range nodes {
		if node.Network=="xhttp" {
			nodeRows=append(nodeRows,tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("⚙ "+truncateLabel(node.Tag,40),"node_detail:"+userCallbackKey(node.Tag))))
		}
	}
	markup := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("⚡ 刷新", "menu_nodes"),
			tgbotapi.NewInlineKeyboardButtonData("🔙 返回主菜单", "menu_main"),
		),
	)

	markup.InlineKeyboard=append(nodeRows,markup.InlineKeyboard...)
	s.sendOrEdit(chatID, msgID, sb.String(), markup)
}

func (s *BotService) sendUsersCard(chatID int64, msgID int) {
	_, err := LoadXrayInbounds(s.cfg.XrayConfigPath)
	if err != nil {
		s.sendOrEdit(chatID, msgID, "❌ 读取用户列表失败: "+mdEsc(err.Error()), backToMainMarkup())
		return
	}

	emails,dirErr := allDirectoryNames(s.cfg.XrayConfigPath)
	if dirErr!=nil {s.sendAlert(chatID,"用户目录读取失败："+dirErr.Error());return}

	var rows [][]tgbotapi.InlineKeyboardButton
	for _, email := range emails {
		key := userCallbackKey(email)
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(truncateLabel("👤 "+email, 40),"manage_user:"+key),
			tgbotapi.NewInlineKeyboardButtonData("🔗 导出", "user_links:"+key),
		))
	}
	rows = append([][]tgbotapi.InlineKeyboardButton{
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("➕ 新增用户","manage_add"), tgbotapi.NewInlineKeyboardButtonData("⏸ 停用用户","menu_disabled_users")),
	}, rows...)
	rows = append(rows, tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData("🔙 返回主菜单", "menu_main"),
	))

	text := "👥 *独立用户管理*\n\n新用户默认没有绑定节点。进入用户详情后可逐节点授权；也可使用 `/adduser 用户名 节点标签` 创建并绑定。"
	if len(emails) == 0 {
		text = "还没有用户。点击「新增用户」先建立独立身份，再按需绑定节点。"
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
	text := "🔄 *确认立即重启 Xray 核心服务吗？*\n\n重启过程耗时约 1 秒，现有活跃连接会快速重连。"
	markup := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🟢 确认重启", "menu_restart_confirm"),
			tgbotapi.NewInlineKeyboardButtonData("取消", "menu_main"),
		),
	)
	s.sendOrEdit(chatID, msgID, text, markup)
}

func (s *BotService) doRestart(chatID int64, msgID int) {
	s.sendOrEdit(chatID, msgID, "⏳ 正在重启 Xray 服务...", tgbotapi.InlineKeyboardMarkup{})

	err := RestartXrayService()
	time.Sleep(1 * time.Second)
	status := CheckXrayServiceStatus()

	var text string
	if err != nil || status != "active" {
		reason := "未知错误"
		if err != nil {
			reason = err.Error()
		}
		text = fmt.Sprintf("❌ *Xray 重启失败*\n\n当前状态: `%s`\n错误: `%s`", mdCode(status), mdCode(reason))
	} else {
		text = "✅ *Xray 核心服务重启成功！*\n\n当前状态: `active (running)`"
	}

	s.sendOrEdit(chatID, msgID, text, backToMainMarkup())
}

func (s *BotService) sendBackupFile(chatID int64) {
  if !s.cfg.AllowSensitiveBackup {
    s.sendAlert(chatID,"🔒 完整配置包含 REALITY 与 VLESS 密钥。Telegram 发送默认关闭；如确需远程备份，请在 VPS 上显式开启 allow_sensitive_backup。")
    return
  }
	if _, err := os.Stat(s.cfg.XrayConfigPath); err != nil {
		s.sendAlert(chatID, "❌ 配置文件不存在: "+s.cfg.XrayConfigPath)
		return
	}

	doc := tgbotapi.NewDocument(chatID, tgbotapi.FilePath(s.cfg.XrayConfigPath))
	doc.Caption = fmt.Sprintf("💾 freedom.sh Xray 配置备份文件\n备份时间: %s", time.Now().Format("2006-01-02 15:04:05"))
	if _, err := s.bot.Send(doc); err != nil {
		log.Printf("[Bot] send backup document failed: %v", err)
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	traffics, _ := QueryTrafficStats(ctx, s.cfg.XrayBinPath, s.cfg.StatsAPIServer, false)
	cancel()

	var sb strings.Builder
	sb.WriteString("📢 *freedom.sh 定时健康与流量报告*\n\n")
	if stats != nil {
		sb.WriteString(fmt.Sprintf(
			"🖥️ *主机:* `%s` | Xray: `%s`\n"+
				"🔥 *CPU:* `%.1f%%` | 🧠 *内存:* `%.1f%%` | 💾 *磁盘:* `%.1f%%`\n\n",
			mdCode(stats.Hostname), mdCode(stats.XrayStatus),
			stats.CPUUsagePct, stats.MemUsagePct, stats.DiskUsagePct,
		))
	}

	sb.WriteString("📈 *今日流量榜单:*\n")
	if len(traffics) == 0 {
		sb.WriteString("暂无流量数据。\n")
	} else {
		for _, u := range traffics {
			sb.WriteString(fmt.Sprintf("• `%s`: ⬆️ %s | ⬇️ %s (共 %s)\n",
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

func backToMainMarkup() tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🔙 返回主菜单", "menu_main"),
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
