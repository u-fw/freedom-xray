package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/u-fw/freedom-xray/bot/internal/xrayapi"
	"golang.org/x/net/idna"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	grpcstatus "google.golang.org/grpc/status"
)

const routeTestMethod = "/xray.app.router.command.RoutingService/TestRoute"

type routeTestInput struct {
	Domain     string            `json:"domain,omitempty"`
	IP         string            `json:"ip,omitempty"`
	TargetIPs  []string          `json:"target_ips,omitempty"`
	Port       int               `json:"port,omitempty"`
	Network    string            `json:"network,omitempty"`
	InboundTag string            `json:"inbound_tag,omitempty"`
	Protocol   string            `json:"protocol,omitempty"`
	User       string            `json:"user,omitempty"`
	SourceIPs  []string          `json:"source_ips,omitempty"`
	SourcePort int               `json:"source_port,omitempty"`
	LocalIPs   []string          `json:"local_ips,omitempty"`
	LocalPort  int               `json:"local_port,omitempty"`
	VlessRoute int               `json:"vless_route,omitempty"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

type routeTestDefault struct {
	Index int    `json:"index"`
	Tag   string `json:"tag"`
}

type routeTestResult struct {
	Matched              bool              `json:"matched"`
	OutboundTag          string            `json:"outbound_tag,omitempty"`
	GroupTags            []string          `json:"group_tags,omitempty"`
	SavedDefaultOutbound *routeTestDefault `json:"saved_default_outbound,omitempty"`
	Target               string            `json:"target"`
	Network              string            `json:"network"`
	ConfigETag           string            `json:"config_etag"`
	CheckedAt            time.Time         `json:"checked_at"`
}

func validRouteTestText(value string, limit int) bool {
	return len(value) <= limit && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\x00\r\n")
}

func routeTestIPs(values []string) ([][]byte, error) {
	if len(values) > 16 {
		return nil, errors.New("at most 16 IP addresses are allowed per field")
	}
	result := make([][]byte, 0, len(values))
	for _, text := range values {
		ip, err := netip.ParseAddr(text)
		if err != nil || ip.Zone() != "" {
			return nil, errors.New("expected plain IPv4 or IPv6 addresses without zones, CIDRs or ports")
		}
		result = append(result, ip.Unmap().AsSlice())
	}
	return result, nil
}

func buildRouteTestContext(input routeTestInput) (*xrayapi.RoutingContext, string, string, error) {
	if input.Port == 0 {
		input.Port = 443
	}
	if input.Port < 1 || input.Port > 65535 || input.SourcePort < 0 || input.SourcePort > 65535 || input.LocalPort < 0 || input.LocalPort > 65535 || input.VlessRoute < 0 || input.VlessRoute > 65535 {
		return nil, "", "", errors.New("ports and vless_route must be between 0 and 65535; target port defaults to 443")
	}
	network := strings.ToLower(input.Network)
	if network == "" {
		network = "tcp"
	}
	wireNetwork := xrayapi.Network_NETWORK_TCP
	if network == "udp" {
		wireNetwork = xrayapi.Network_NETWORK_UDP
	} else if network != "tcp" {
		return nil, "", "", errors.New("network must be tcp or udp")
	}
	if !validRouteTestText(input.Domain, 253) || !validRouteTestText(input.InboundTag, 256) || !validRouteTestText(input.Protocol, 64) || !validRouteTestText(input.User, 256) || len(input.Attributes) > 32 {
		return nil, "", "", errors.New("invalid or oversized route context text")
	}
	if input.Domain != "" {
		domain, err := idna.Lookup.ToASCII(strings.TrimSuffix(input.Domain, "."))
		if err != nil || len(domain) == 0 || len(domain) > 253 || !serverHostnamePattern.MatchString(domain) || net.ParseIP(domain) != nil {
			return nil, "", "", errors.New("domain must be a DNS name; use ip or target_ips for IP literals")
		}
		// Validate IDNA compatibility without rewriting the synthetic context.
		// Let the core normalize it; a trailing dot can affect regex matching.
	}
	for key, value := range input.Attributes {
		if key == "" || len(key) > 256 || len(value) > 2048 || strings.ContainsAny(key+value, "\x00\r\n") {
			return nil, "", "", errors.New("invalid or oversized route attributes")
		}
	}
	if input.IP != "" {
		if len(input.TargetIPs) != 0 {
			return nil, "", "", errors.New("use ip or target_ips, not both")
		}
		input.TargetIPs = []string{input.IP}
	}
	targets, err := routeTestIPs(input.TargetIPs)
	if err != nil {
		return nil, "", "", err
	}
	if input.Domain == "" && len(targets) == 0 {
		return nil, "", "", errors.New("provide domain, ip or target_ips")
	}
	sources, err := routeTestIPs(input.SourceIPs)
	if err != nil {
		return nil, "", "", err
	}
	locals, err := routeTestIPs(input.LocalIPs)
	if err != nil {
		return nil, "", "", err
	}
	target := input.Domain
	if target == "" {
		target = net.IP(targets[0]).String()
	}
	target = net.JoinHostPort(target, strconv.Itoa(input.Port))
	if len(targets) > 1 {
		target += fmt.Sprintf(" (+%d IPs)", len(targets)-1)
	}
	return &xrayapi.RoutingContext{InboundTag: input.InboundTag, Network: wireNetwork, SourceIps: sources, TargetIps: targets,
		SourcePort: uint32(input.SourcePort), TargetPort: uint32(input.Port), TargetDomain: input.Domain, Protocol: input.Protocol,
		User: input.User, Attributes: input.Attributes, LocalIps: locals, LocalPort: uint32(input.LocalPort), VlessRoute: uint32(input.VlessRoute)}, target, network, nil
}

func routeTestRPC(ctx context.Context, address string, input *xrayapi.RoutingContext) (*xrayapi.RoutingContext, error) {
	ip, port, err := parseLoopbackAPIAddress(address)
	if err != nil {
		return nil, err
	}
	address = net.JoinHostPort(ip.String(), strconv.Itoa(port))
	// Pin the validated loopback address; test input cannot supply a network
	// endpoint. A custom dialer also bypasses environment proxies and DNS.
	dialer := &net.Dialer{Timeout: 3 * time.Second}
	connection, err := grpc.NewClient("passthrough:///"+address,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return dialer.DialContext(ctx, "tcp", address) }),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(256*1024), grpc.MaxCallSendMsgSize(128*1024)))
	if err != nil {
		return nil, errors.New("cannot initialize the local routing API client")
	}
	defer connection.Close()
	var response xrayapi.RoutingContext
	request := &xrayapi.TestRouteRequest{RoutingContext: input, FieldSelectors: []string{"outbound"}, PublishResult: false}
	if err := connection.Invoke(ctx, routeTestMethod, request, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func queryRouteTest(cfg *BotConfig, input routeTestInput) (routeTestResult, error) {
	request, target, network, err := buildRouteTestContext(input)
	if err != nil {
		return routeTestResult{}, err
	}
	root, err := decodeBindingMigrationConfig(cfg.XrayConfigPath)
	if err != nil {
		return routeTestResult{}, err
	}
	address, err := routingAPIAddress(cfg, root)
	if err != nil {
		return routeTestResult{}, err
	}
	etag, err := routingRuleETag(root)
	if err != nil {
		return routeTestResult{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	response, err := routeTestRPC(ctx, address, request)
	result := routeTestResult{Target: target, Network: network, ConfigETag: etag, CheckedAt: time.Now().UTC()}
	if err != nil {
		// Only the exact no-match error implies normal default dispatch. Other
		// DNS, transport and routing failures must not become a fake success.
		message := grpcstatus.Convert(err).Message()
		if grpcstatus.Code(err) == codes.Unknown && (message == "not enough information for making a decision" || message == "common: not enough information for making a decision") {
			outbounds, _ := root["outbounds"].([]any)
			if len(outbounds) > 0 {
				first, _ := getMap(outbounds[0])
				tag, _ := first["tag"].(string)
				result.SavedDefaultOutbound = &routeTestDefault{Index: 1, Tag: tag}
			}
			return result, nil
		}
		switch grpcstatus.Code(err) {
		case codes.DeadlineExceeded:
			return routeTestResult{}, errors.New("routing query timed out; core evaluation may still be running")
		case codes.Unimplemented:
			return routeTestResult{}, errors.New("the installed core does not support RoutingService.TestRoute")
		case codes.Unavailable:
			return routeTestResult{}, errors.New("cannot reach the local Xray routing API; check the service")
		default:
			return routeTestResult{}, errors.New("Xray could not evaluate the route; inspect local core logs")
		}
	}
	if response.OutboundTag == "" {
		return routeTestResult{}, errors.New("Xray returned an empty route decision")
	}
	result.Matched, result.OutboundTag, result.GroupTags = true, response.OutboundTag, response.OutboundGroupTags
	return result, nil
}
