package main

import (
	"encoding/json"
	"net"
	"os"
	"strconv"
	"strings"
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
	if len(server) > 0 && server[0] != "" {
		apiAddress = server[0]
	}
	directListen, _ := api["listen"].(string)
	directAPI := directListen != ""
	if _, safetyErr := validateAPIAccess(root, apiAddress); safetyErr != nil {
		report.APIMissing = append(report.APIMissing, "api safety: "+safetyErr.Error())
	}
	host, portText, addressErr := net.SplitHostPort(apiAddress)
	if strings.EqualFold(host, "localhost") {
		host = "127.0.0.1"
	}
	apiIP := net.ParseIP(host)
	apiPort, portErr := strconv.Atoi(portText)
	if inbounds, ok := root["inbounds"].([]any); ok {
		for _, raw := range inbounds {
			inbound, _ := getMap(raw)
			if inbound["tag"] == "api-internal" {
				if hasAPIInbound {
					report.APIMissing = append(report.APIMissing, "duplicate api-internal")
				}
				hasAPIInbound = true
				listen, _ := inbound["listen"].(string)
				ip := net.ParseIP(listen)
				settings, _ := getMap(inbound["settings"])
				dest, _ := settings["address"].(string)
				destIP := net.ParseIP(dest)
				if addressErr != nil || portErr != nil || apiIP == nil || !apiIP.IsLoopback() || ip == nil || !ip.IsLoopback() || (!directAPI && (!ip.Equal(apiIP) || inboundPortValue(inbound["port"]) != apiPort)) || inbound["protocol"] != "dokodemo-door" || destIP == nil || !destIP.IsLoopback() {
					report.APIMissing = append(report.APIMissing, "unsafe or mismatched api-internal listener")
				}
			}
		}
	}
	if !hasAPIInbound && !directAPI {
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
	if !hasAPIOutbound && !directAPI {
		report.APIMissing = append(report.APIMissing, apiTag+" outbound")
	}
	hasAPIRoute := false
	routing, _ := getMap(root["routing"])
	if script, ok := routing["script"].(string); ok && script != "" {
		report.APIMissing = append(report.APIMissing, "routing script: internal API handling requires manual verification")
	}
	if rules, ok := routing["rules"].([]any); ok {
		if len(rules) > 0 {
			rule, _ := getMap(rules[0])
			hasAPIRoute = isInternalAPIRoute(rule, apiTag)
		}
	}
	if !hasAPIRoute && !directAPI {
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

func statsSwitchesText(lang string, report statsSwitchReport, readErr error) htmlText {
	var out htmlBuilder
	out.Add(trh(lang, "stats.title"))
	if readErr != nil {
		out.Add(trh(lang, "stats.readerror", readErr.Error()))
		return out.HTML()
	}
	on := tr(lang, "stats.on")
	trafficState := on
	if len(report.MissingTrafficLevels) > 0 {
		trafficState = tr(lang, "stats.offlevels", strings.Join(report.MissingTrafficLevels, ", "))
	}
	onlineState := on
	if len(report.MissingOnlineLevels) > 0 {
		onlineState = tr(lang, "stats.offlevels", strings.Join(report.MissingOnlineLevels, ", "))
	}
	inboundState := on
	if !report.SystemInbound {
		inboundState = tr(lang, "stats.off")
	}
	apiState := on
	if len(report.APIMissing) > 0 {
		apiState = tr(lang, "stats.offmissing", strings.Join(report.APIMissing, ", "))
	}
	out.Add(trh(lang, "stats.item.traffic", trafficState))
	out.Add(trh(lang, "stats.item.online", onlineState))
	out.Add(trh(lang, "stats.item.inbound", inboundState))
	out.Add(trh(lang, "stats.item.api", apiState))
	if len(report.XHTTPWithoutXFF) > 0 {
		out.Add(trh(lang, "stats.xff", strings.Join(report.XHTTPWithoutXFF, ", ")))
	}
	if len(report.APIMissing) > 0 {
		out.Add(trh(lang, "stats.api.hint"))
	}
	if report.allReady() {
		out.Add(trh(lang, "stats.allgood"))
	} else if !report.policyComplete() {
		out.Add(trh(lang, "stats.repairhint"))
	}
	return out.HTML()
}

func statsSwitchesMarkup(lang string, report statsSwitchReport, readErr error) tgKeyboard {
	rows := [][]tgButton{}
	if readErr == nil && !report.allReady() {
		rows = append(rows, newRow(
			newButton(tr(lang, "stats.repair"), "stats_repair_ask"),
		))
	}
	rows = append(rows, newRow(
		newButton(tr(lang, "common.refresh"), "menu_stats"),
		newButton(tr(lang, "common.main"), "menu_main"),
	))
	return newKeyboard(rows...)
}

func statsSwitchesBackMarkup(lang string) tgKeyboard {
	return newKeyboard(
		newRow(
			newButton(tr(lang, "common.refresh"), "menu_stats"),
			newButton(tr(lang, "common.main"), "menu_main"),
		),
	)
}

func (s *BotService) sendStatsSwitches(chatID int64, msgID int) {
	report, err := inspectStatsSwitches(s.cfg.XrayConfigPath, s.cfg.StatsAPIServer)
	s.sendOrEdit(chatID, msgID, statsSwitchesText(s.cfg.Language, report, err), statsSwitchesMarkup(s.cfg.Language, report, err))
}

func (s *BotService) askStatsRepair(chatID int64, msgID int) {
	if !s.cfg.AllowMutations {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "online.doctor.mutations"), statsSwitchesBackMarkup(s.cfg.Language))
		return
	}
	markup := newKeyboard(
		newRow(
			newButton(tr(s.cfg.Language, "common.confirm"), "stats_repair_confirm"),
			newButton(tr(s.cfg.Language, "common.cancel"), "menu_stats"),
		),
	)
	s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "stats.repairconfirm"), markup)
}

func (s *BotService) doStatsRepair(chatID int64, msgID int) {
	if !s.cfg.AllowMutations {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "online.doctor.mutations"), statsSwitchesBackMarkup(s.cfg.Language))
		return
	}
	if err := ensureStatsInfrastructure(s.cfg); err != nil {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "stats.repairfailed", err.Error()), statsSwitchesBackMarkup(s.cfg.Language))
		return
	}
	report, err := inspectStatsSwitches(s.cfg.XrayConfigPath, s.cfg.StatsAPIServer)
	text := trh(s.cfg.Language, "stats.repairdone") + "\n\n" + statsSwitchesText(s.cfg.Language, report, err)
	s.sendOrEdit(chatID, msgID, text, statsSwitchesMarkup(s.cfg.Language, report, err))
}
