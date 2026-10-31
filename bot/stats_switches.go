package main

import (
	"encoding/json"
	"net"
	"os"
	"strconv"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// statsSwitchReport is the read-only view of every Xray switch the Bot needs
// for traffic, online-IP and node-traffic collection.
type statsSwitchReport struct {
	SystemInbound        bool     `json:"system_inbound"`
	APIMissing           []string `json:"api_missing"`
	MissingTrafficLevels []string `json:"missing_traffic_levels"`
	MissingOnlineLevels  []string `json:"missing_online_levels"`
	XHTTPWithoutXFF      []string `json:"xhttp_without_xff"`
}

func (r statsSwitchReport) policyComplete() bool {
	return r.SystemInbound && len(r.MissingTrafficLevels) == 0 && len(r.MissingOnlineLevels) == 0
}

func (r statsSwitchReport) allReady() bool {
	return r.policyComplete() && len(r.APIMissing) == 0
}

func inspectStatsSwitches(configPath string, server ...string) (statsSwitchReport, error) {
	var report statsSwitchReport
	data, err := os.ReadFile(configPath)
	if err != nil {
		return report, err
	}
	var root map[string]any
	if err = json.Unmarshal(data, &root); err != nil {
		return report, err
	}

	levels, err := inspectOnlinePolicy(configPath)
	if err != nil {
		return report, err
	}
	for _, item := range levels {
		if item.Clients == 0 {
			continue
		}
		if !item.Enabled {
			report.MissingOnlineLevels = append(report.MissingOnlineLevels, item.Level)
		}
		if !item.TrafficEnabled {
			report.MissingTrafficLevels = append(report.MissingTrafficLevels, item.Level)
		}
	}

	policy, _ := getMap(root["policy"])
	system, _ := getMap(policy["system"])
	report.SystemInbound = system["statsInboundUplink"] == true && system["statsInboundDownlink"] == true

	if _, ok := root["stats"]; !ok {
		report.APIMissing = append(report.APIMissing, "stats")
	}
	api, _ := getMap(root["api"])
	services, _ := api["services"].([]any)
	hasStatsService := false
	for _, raw := range services {
		if name, ok := raw.(string); ok && name == "StatsService" {
			hasStatsService = true
		}
	}
	if !hasStatsService {
		report.APIMissing = append(report.APIMissing, "StatsService")
	}
	apiTag := "api"
	if tag, ok := api["tag"].(string); ok && tag != "" {
		apiTag = tag
	}
	hasAPIInbound := false
	apiAddress := "127.0.0.1:10085"
	if len(server)>0 && server[0]!="" {apiAddress=server[0]}
	host, portText, addressErr := net.SplitHostPort(apiAddress)
	if strings.EqualFold(host,"localhost") {host="127.0.0.1"}
	apiIP:=net.ParseIP(host); apiPort,portErr:=strconv.Atoi(portText)
	if inbounds, ok := root["inbounds"].([]any); ok {
		for _, raw := range inbounds {
			inbound, _ := getMap(raw)
			if inbound["tag"] == "api-internal" {
				if hasAPIInbound {report.APIMissing=append(report.APIMissing,"duplicate api-internal")}
				hasAPIInbound = true
				listen,_:=inbound["listen"].(string); ip:=net.ParseIP(listen)
				settings,_:=getMap(inbound["settings"]); dest,_:=settings["address"].(string);destIP:=net.ParseIP(dest)
				if addressErr!=nil||portErr!=nil||apiIP==nil||!apiIP.IsLoopback()||ip==nil||!ip.IsLoopback()||!ip.Equal(apiIP)||inboundPortValue(inbound["port"])!=apiPort||inbound["protocol"]!="dokodemo-door"||destIP==nil||!destIP.IsLoopback() {
					report.APIMissing=append(report.APIMissing,"unsafe or mismatched api-internal listener")
				}
			}
		}
	}
	if !hasAPIInbound {
		report.APIMissing = append(report.APIMissing, "api-internal")
	}
	hasAPIOutbound := false
	if outbounds, ok := root["outbounds"].([]any); ok {
		for _, raw := range outbounds {
			outbound, _ := getMap(raw)
			if outbound["tag"] == apiTag {
				hasAPIOutbound = true
			}
		}
	}
	if !hasAPIOutbound {
		report.APIMissing = append(report.APIMissing, apiTag+" outbound")
	}
	hasAPIRoute := false
	routing, _ := getMap(root["routing"])
	if rules, ok := routing["rules"].([]any); ok {
		if len(rules)>0 {rule,_:=getMap(rules[0]);hasAPIRoute=isInternalAPIRoute(rule,apiTag)}
	}
	if !hasAPIRoute {
		report.APIMissing = append(report.APIMissing, "api route")
	}

	report.XHTTPWithoutXFF = xhttpInboundsWithoutXFF(root)
	return report, nil
}

// Only XHTTP inbounds can name the real client address through a trusted
// forwarding header. Direct REALITY inbounds do not need this setting.
func xhttpInboundsWithoutXFF(root map[string]any) []string {
	var missing []string
	inbounds, ok := root["inbounds"].([]any)
	if !ok {
		return missing
	}
	for _, raw := range inbounds {
		inbound, _ := getMap(raw)
		stream, _ := getMap(inbound["streamSettings"])
		if network, _ := stream["network"].(string); network != "xhttp" {
			continue
		}
		sockopt, _ := getMap(stream["sockopt"])
		forwarded, _ := sockopt["trustedXForwardedFor"].([]any)
		if len(forwarded) > 0 {
			continue
		}
		tag, _ := inbound["tag"].(string)
		if tag == "" {
			tag = "(untagged)"
		}
		missing = append(missing, tag)
	}
	return missing
}

func statsSwitchesText(lang string, report statsSwitchReport, readErr error) string {
	var out strings.Builder
	out.WriteString(tr(lang, "stats.title"))
	if readErr != nil {
		out.WriteString(tr(lang, "stats.readerror", mdEsc(readErr.Error())))
		return out.String()
	}
	on := tr(lang, "stats.on")
	trafficState := on
	if len(report.MissingTrafficLevels) > 0 {
		trafficState = tr(lang, "stats.offlevels", mdCode(strings.Join(report.MissingTrafficLevels, ", ")))
	}
	onlineState := on
	if len(report.MissingOnlineLevels) > 0 {
		onlineState = tr(lang, "stats.offlevels", mdCode(strings.Join(report.MissingOnlineLevels, ", ")))
	}
	inboundState := on
	if !report.SystemInbound {
		inboundState = tr(lang, "stats.off")
	}
	apiState := on
	if len(report.APIMissing) > 0 {
		apiState = tr(lang, "stats.offmissing", mdCode(strings.Join(report.APIMissing, ", ")))
	}
	out.WriteString(tr(lang, "stats.item.traffic", trafficState))
	out.WriteString(tr(lang, "stats.item.online", onlineState))
	out.WriteString(tr(lang, "stats.item.inbound", inboundState))
	out.WriteString(tr(lang, "stats.item.api", apiState))
	if len(report.XHTTPWithoutXFF) > 0 {
		out.WriteString(tr(lang, "stats.xff", mdCode(strings.Join(report.XHTTPWithoutXFF, ", "))))
	}
	if len(report.APIMissing) > 0 {
		out.WriteString(tr(lang, "stats.api.hint"))
	}
	if report.allReady() {
		out.WriteString(tr(lang, "stats.allgood"))
	} else if !report.policyComplete() {
		out.WriteString(tr(lang, "stats.repairhint"))
	}
	return out.String()
}

func statsSwitchesMarkup(lang string, report statsSwitchReport, readErr error) tgbotapi.InlineKeyboardMarkup {
	rows := [][]tgbotapi.InlineKeyboardButton{}
	if readErr == nil && !report.allReady() {
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(tr(lang, "stats.repair"), "stats_repair_ask"),
		))
	}
	rows = append(rows, tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData(tr(lang, "common.refresh"), "menu_stats"),
		tgbotapi.NewInlineKeyboardButtonData(tr(lang, "common.main"), "menu_main"),
	))
	return tgbotapi.NewInlineKeyboardMarkup(rows...)
}

func statsSwitchesBackMarkup(lang string) tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(tr(lang, "common.refresh"), "menu_stats"),
			tgbotapi.NewInlineKeyboardButtonData(tr(lang, "common.main"), "menu_main"),
		),
	)
}

func (s *BotService) sendStatsSwitches(chatID int64, msgID int) {
	report, err := inspectStatsSwitches(s.cfg.XrayConfigPath,s.cfg.StatsAPIServer)
	s.sendOrEdit(chatID, msgID, statsSwitchesText(s.cfg.Language, report, err), statsSwitchesMarkup(s.cfg.Language, report, err))
}

func (s *BotService) askStatsRepair(chatID int64, msgID int) {
	if !s.cfg.AllowMutations {
		s.sendOrEdit(chatID, msgID, tr(s.cfg.Language, "online.doctor.mutations"), statsSwitchesBackMarkup(s.cfg.Language))
		return
	}
	markup := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language, "common.confirm"), "stats_repair_confirm"),
			tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language, "common.cancel"), "menu_stats"),
		),
	)
	s.sendOrEdit(chatID, msgID, tr(s.cfg.Language, "stats.repairconfirm"), markup)
}

func (s *BotService) doStatsRepair(chatID int64, msgID int) {
	if !s.cfg.AllowMutations {
		s.sendOrEdit(chatID, msgID, tr(s.cfg.Language, "online.doctor.mutations"), statsSwitchesBackMarkup(s.cfg.Language))
		return
	}
	if err := ensureStatsInfrastructure(s.cfg); err != nil {
		s.sendOrEdit(chatID, msgID, tr(s.cfg.Language, "stats.repairfailed", mdEsc(err.Error())), statsSwitchesBackMarkup(s.cfg.Language))
		return
	}
	report, err := inspectStatsSwitches(s.cfg.XrayConfigPath,s.cfg.StatsAPIServer)
	text := tr(s.cfg.Language, "stats.repairdone") + "\n\n" + statsSwitchesText(s.cfg.Language, report, err)
	s.sendOrEdit(chatID, msgID, text, statsSwitchesMarkup(s.cfg.Language, report, err))
}
