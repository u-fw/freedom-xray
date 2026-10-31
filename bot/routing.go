package main

import (
 "encoding/json"
 "fmt"
 "os"
 "sort"
 "strings"

 tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

type routingRule struct {
	Index       int      `json:"index"`
	RuleTag     string   `json:"rule_tag,omitempty"`
	InboundTags []string `json:"inbound_tags,omitempty"`
	OutboundTag string   `json:"outbound_tag,omitempty"`
	BalancerTag string   `json:"balancer_tag,omitempty"`
	Domains     []string `json:"domains,omitempty"`
	IPs         []string `json:"ips,omitempty"`
	Ports       []string `json:"ports,omitempty"`
	Networks    []string `json:"networks,omitempty"`
	Protocols   []string `json:"protocols,omitempty"`
	SourceIPs   []string `json:"source_ips,omitempty"`
	SourcePorts []string `json:"source_ports,omitempty"`
	UserEmails  []string `json:"user_emails,omitempty"`
}

type routingSnapshot struct {
	Rules          []routingRule `json:"rules"`
	Outbounds      []string      `json:"outbounds"`
	Balancers      []string      `json:"balancers"`
	DomainStrategy string        `json:"domain_strategy"`
}

func stringList(value any) []string {
 // Xray accepts a scalar string for fields such as port and sourcePort, and
 // arrays for domain, ip and tag lists. Preserve both shapes.
 if text,ok:=value.(string);ok {
  if text==""{return nil}
  return []string{text}
 }
 raw,ok:=value.([]any)
 if !ok{return nil}
 out:=make([]string,0,len(raw))
 for _,item:=range raw {
  if text,ok:=item.(string);ok&&text!=""{out=append(out,text)}
 }
 return out
}

// LoadRouting reads the routing section without modifying it. The Bot keeps
// custom rules, outbounds and balancers visible even when it did not create
// them; management remains a separate, validated operation.
func LoadRouting(configPath string) (routingSnapshot, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return routingSnapshot{}, err
	}
	var root struct {
		Routing struct {
			DomainStrategy string           `json:"domainStrategy"`
			Rules          []map[string]any `json:"rules"`
			Balancers      []map[string]any `json:"balancers"`
		} `json:"routing"`
		Outbounds []map[string]any `json:"outbounds"`
	}
	if err = json.Unmarshal(data, &root); err != nil {
		return routingSnapshot{}, err
	}

	snapshot := routingSnapshot{DomainStrategy: root.Routing.DomainStrategy}
	for _, outbound := range root.Outbounds {
		if tag, ok := outbound["tag"].(string); ok && tag != "" {
			snapshot.Outbounds = append(snapshot.Outbounds, tag)
		}
	}
	sort.Strings(snapshot.Outbounds)
	for _, balancer := range root.Routing.Balancers {
		if tag, ok := balancer["tag"].(string); ok && tag != "" {
			snapshot.Balancers = append(snapshot.Balancers, tag)
		}
	}
	sort.Strings(snapshot.Balancers)

	for i, rule := range root.Routing.Rules {
		item := routingRule{Index: i + 1}
		item.RuleTag, _ = rule["ruleTag"].(string)
		item.InboundTags = stringList(rule["inboundTag"])
		item.OutboundTag, _ = rule["outboundTag"].(string)
		item.BalancerTag, _ = rule["balancerTag"].(string)
		item.Domains = stringList(rule["domain"])
		item.IPs = stringList(rule["ip"])
		item.Ports = stringList(rule["port"])
		item.Networks = stringList(rule["network"])
		item.Protocols = stringList(rule["protocol"])
		item.SourceIPs = stringList(rule["source"])
		if len(item.SourceIPs) == 0 {
			item.SourceIPs = stringList(rule["sourceIP"])
		}
		item.SourcePorts = stringList(rule["sourcePort"])
		item.UserEmails = stringList(rule["user"])
		snapshot.Rules = append(snapshot.Rules, item)
	}
	return snapshot, nil
}

func joinOrEmpty(values []string,empty string) string {
 if len(values)==0{return empty}
 return strings.Join(values,", ")
}

func routingCardText(snapshot routingSnapshot,lang string) string {
 var out strings.Builder
 out.WriteString(tr(lang,"routing.title"))
 strategy:=snapshot.DomainStrategy
 if strategy==""{strategy="default"}
 out.WriteString(tr(lang,"routing.summary",mdCode(strategy),len(snapshot.Rules),len(snapshot.Outbounds),len(snapshot.Balancers)))
 if len(snapshot.Rules)==0 {
  out.WriteString(tr(lang,"routing.empty"))
 }else{
  limit:=len(snapshot.Rules);if limit>20{limit=20}
  for _,rule:=range snapshot.Rules[:limit] {
   tag:=rule.RuleTag
   if tag==""{tag=fmt.Sprintf("#%d",rule.Index)}
   inbound:=joinOrEmpty(rule.InboundTags,tr(lang,"routing.any"))
   outbound:=rule.OutboundTag
   if rule.BalancerTag!=""{outbound="balancer:"+rule.BalancerTag}
   if outbound==""{outbound=tr(lang,"routing.none")}
   ports:=joinOrEmpty(rule.Ports,tr(lang,"routing.none"))
   protocols:=joinOrEmpty(rule.Protocols,tr(lang,"routing.none"))
   out.WriteString(tr(lang,"routing.rule",
    rule.Index,mdCode(truncateLabel(tag,24)),mdCode(truncateLabel(inbound,40)),mdCode(truncateLabel(outbound,40)),
    len(rule.Domains),len(rule.IPs),mdCode(truncateLabel(ports,40)),mdCode(truncateLabel(protocols,40))))
  }
  if len(snapshot.Rules)>limit{out.WriteString(tr(lang,"routing.truncated",limit))}
 }
 out.WriteString(tr(lang,"routing.outbounds",mdCode(truncateLabel(joinOrEmpty(snapshot.Outbounds,tr(lang,"routing.none")),180))))
 out.WriteString(tr(lang,"routing.balancers",mdCode(truncateLabel(joinOrEmpty(snapshot.Balancers,tr(lang,"routing.none")),180))))
 out.WriteString(tr(lang,"routing.note"))
 return out.String()
}

func (s *BotService) sendRoutingCard(chatID int64, msgID int) {
	snapshot, err := LoadRouting(s.cfg.XrayConfigPath)
	if err != nil {
		s.sendOrEdit(chatID, msgID, tr(s.cfg.Language, "routing.error", mdEsc(err.Error())), s.backToMainMarkup())
		return
	}
	rows := [][]tgbotapi.InlineKeyboardButton{}
	if s.cfg.AllowMutations {
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language, "routing.edit.add"), "routing_manage:add"),
			tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language, "routing.edit.update"), "routing_manage:update"),
			tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language, "routing.edit.delete"), "routing_manage:delete")))
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language, "network.outbound.put"), "routing_manage:outbound_put"),
			tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language, "network.outbound.delete"), "routing_manage:outbound_delete"),
			tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language, "network.dns.put"), "routing_manage:dns_put")))
	}
	rows = append(rows, tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language, "common.refresh"), "menu_routing"),
		tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language, "menu.home"), "menu_main")))
	markup := tgbotapi.NewInlineKeyboardMarkup(rows...)
	s.sendOrEdit(chatID, msgID, routingCardText(snapshot, s.cfg.Language), markup)
}
