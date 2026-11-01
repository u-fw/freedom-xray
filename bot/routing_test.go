package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeRoutingConfig(t *testing.T, body string) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(file, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestLoadRoutingSnapshot(t *testing.T) {
	file := writeRoutingConfig(t, `{"routing":{"domainStrategy":"IPIfNonMatch","rules":[{"type":"field","ruleTag":"cn-direct","inboundTag":["vless-in"],"outboundTag":"direct","domain":["geosite:cn","domain:example.cn"],"ip":["geoip:cn"],"port":"443","network":"tcp","protocol":["tls"]},{"type":"field","balancerTag":"fast","domain":["geosite:google"],"source":["10.0.0.0/8"]}],"balancers":[{"tag":"fast","selector":["hk","us"]}]},"outbounds":[{"tag":"proxy","protocol":"vless"},{"tag":"direct","protocol":"freedom"},{"protocol":"blackhole"}]}`)
	snapshot, err := LoadRouting(file)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.DomainStrategy != "IPIfNonMatch" {
		t.Fatalf("strategy lost: %#v", snapshot)
	}
	if len(snapshot.Rules) != 2 {
		t.Fatalf("unexpected rule count: %#v", snapshot.Rules)
	}
	first := snapshot.Rules[0]
	if first.Index != 1 || first.RuleTag != "cn-direct" || first.OutboundTag != "direct" {
		t.Fatalf("rule identity lost: %#v", first)
	}
	if len(first.InboundTags) != 1 || first.InboundTags[0] != "vless-in" {
		t.Fatalf("inbound tags lost: %#v", first)
	}
	if len(first.Domains) != 2 || len(first.IPs) != 1 {
		t.Fatalf("domain/IP lists lost: %#v", first)
	}
	if len(first.Ports) != 1 || first.Ports[0] != "443" {
		t.Fatalf("scalar port not preserved: %#v", first.Ports)
	}
	if len(first.Networks) != 1 || len(first.Protocols) != 1 {
		t.Fatalf("network/protocol lost: %#v", first)
	}
	second := snapshot.Rules[1]
	if second.BalancerTag != "fast" || len(second.SourceIPs) != 1 {
		t.Fatalf("balancer or source lost: %#v", second)
	}
	if strings.Join(snapshot.Outbounds, ",") != "direct,proxy" {
		t.Fatalf("untagged outbound must be skipped: %#v", snapshot.Outbounds)
	}
	if len(snapshot.Balancers) != 1 || snapshot.Balancers[0] != "fast" {
		t.Fatalf("balancer list lost: %#v", snapshot.Balancers)
	}
}

func TestRoutingCardText(t *testing.T) {
	snapshot := routingSnapshot{
		DomainStrategy: "AsIs",
		Outbounds:      []string{"direct", "proxy"},
		Balancers:      []string{"fast"},
		Rules: []routingRule{
			{Index: 1, RuleTag: "cn-direct", InboundTags: []string{"vless-in"}, OutboundTag: "direct", Domains: []string{"geosite:cn"}, Ports: []string{"443"}},
			{Index: 2, BalancerTag: "fast", Domains: []string{"geosite:google"}},
		},
	}
	body := routingCardText(snapshot, "en")
	for _, wanted := range []string{"Strategy:", "Rules: 2", "cn-direct", "vless-in", "balancer:fast", "domains 1", "Named field rules"} {
		if !strings.Contains(body, wanted) {
			t.Fatalf("missing %q in routing card: %s", wanted, body)
		}
	}
	empty := routingCardText(routingSnapshot{}, "en")
	if !strings.Contains(empty, "No routing rules") {
		t.Fatalf("empty routing state not explained: %s", empty)
	}
	many := routingSnapshot{}
	for i := 0; i < 22; i++ {
		many.Rules = append(many.Rules, routingRule{Index: i + 1, OutboundTag: "direct"})
	}
	truncated := routingCardText(many, "en")
	if !strings.Contains(truncated, "Showing the first 20 rules") {
		t.Fatalf("truncation not disclosed: %s", truncated)
	}
}

func TestLoadRoutingRejectsMalformedConfig(t *testing.T) {
	file := writeRoutingConfig(t, `{"routing":`)
	if _, err := LoadRouting(file); err == nil {
		t.Fatal("accepted malformed config")
	}
	if _, err := LoadRouting(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("accepted missing config")
	}
}
