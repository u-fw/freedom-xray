package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

// The public server address embedded in client links is not the same thing as
// the Bot's outbound proxy. A SOCKS5 proxy reports the proxy's egress address,
// which is usually not the address clients use to reach this VPS. Discovery
// therefore always goes directly over IPv4; an operator behind a proxy or NAT
// must set server_host in bot.json explicitly.

const serverHostCacheTTL = 10 * time.Minute

var (
	serverHostCacheMu sync.Mutex
	serverHostCache   string
	serverHostExpires time.Time
)

var serverHostDiscoveryTargets = []string{
	"https://api.ip.sb/ip",
	"https://api.ipify.org",
	"https://ifconfig.me/ip",
	"https://ipv4.icanhazip.com",
}

var serverHostnamePattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*$`)

// normalizeServerHost accepts a bare DNS name or IP literal. Brackets around an
// IPv6 literal are removed because formatHost adds them when the link is built.
func normalizeServerHost(value string) (string, error) {
	host := strings.TrimSpace(value)
	if host == "" {
		return "", nil
	}
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast() {
			return "", errors.New("must not be a loopback, unspecified, or multicast address")
		}
		return host, nil
	}
	host = strings.TrimSuffix(host, ".")
	if len(host) == 0 || len(host) > 253 || !serverHostnamePattern.MatchString(host) {
		return "", errors.New("must be a DNS hostname or IP address without a scheme, path, or port")
	}
	return host, nil
}

func isGlobalUnicastIP(ip net.IP) bool {
	return ip != nil && ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsUnspecified() && !ip.IsLinkLocalUnicast()
}

// interfacePublicHost is a last-resort fallback for hosts where the public-IP
// HTTP endpoints are unreachable. It deliberately rejects private addresses:
// a NATed VPS needs an explicit server_host because clients cannot dial it.
func interfacePublicHost() string {
	interfaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	var v6 string
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, address := range addresses {
			var ip net.IP
			switch value := address.(type) {
			case *net.IPNet:
				ip = value.IP
			case *net.IPAddr:
				ip = value.IP
			default:
				continue
			}
			if !isGlobalUnicastIP(ip) {
				continue
			}
			if ip4 := ip.To4(); ip4 != nil {
				return ip4.String()
			}
			if v6 == "" {
				v6 = ip.String()
			}
		}
	}
	return v6
}

func fetchPublicIPv4(ctx context.Context, client *http.Client, endpoint string) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("User-Agent", "freedom-bot/public-address")
	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 256))
	if err != nil {
		return "", err
	}
	ip := net.ParseIP(strings.TrimSpace(string(body)))
	if ip == nil || ip.To4() == nil || !isGlobalUnicastIP(ip) {
		return "", fmt.Errorf("endpoint returned an unusable address")
	}
	return ip.To4().String(), nil
}

func cachedServerHostValue(now time.Time) string {
	serverHostCacheMu.Lock()
	defer serverHostCacheMu.Unlock()
	if serverHostCache == "" || !now.Before(serverHostExpires) {
		return ""
	}
	return serverHostCache
}

func storeCachedServerHost(host string, now time.Time) {
	serverHostCacheMu.Lock()
	serverHostCache = host
	serverHostExpires = now.Add(serverHostCacheTTL)
	serverHostCacheMu.Unlock()
}

// resolveServerHost returns the address that clients should dial. An explicit
// server_host always wins; otherwise discovery is cached briefly so link
// refreshes do not depend on outbound HTTP for every button press.
func resolveServerHost(cfg *BotConfig) (string, error) {
	configured := ""
	if cfg != nil {
		configured = cfg.ServerHost
	}
	// Bot and agent changes to the public address take effect on the next export
	// without racing a mutation of the shared in-memory BotConfig.
	if cfg != nil && cfg.ConfigFile != "" {
		raw, err := os.ReadFile(cfg.ConfigFile)
		if err != nil {
			return "", fmt.Errorf("read bot config: %w", err)
		}
		var saved struct {
			ServerHost string `json:"server_host"`
		}
		if err = json.Unmarshal(raw, &saved); err != nil {
			return "", fmt.Errorf("read server_host: %w", err)
		}
		configured = saved.ServerHost
	}
	if strings.TrimSpace(configured) != "" {
		host, err := normalizeServerHost(configured)
		if err != nil {
			return "", fmt.Errorf("invalid server_host in bot.json: %w", err)
		}
		return host, nil
	}
	if host := cachedServerHostValue(time.Now()); host != "" {
		return host, nil
	}

	// Bind the whole discovery attempt to one deadline instead of multiplying the
	// timeout by the number of endpoints.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			// Never inherit an outbound proxy here: it would report the proxy's
			// address rather than this server's inbound address.
			Proxy:             nil,
			ForceAttemptHTTP2: true,
			DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
				return dialer.DialContext(ctx, "tcp4", address)
			},
		},
	}
	var failures []string
	for _, endpoint := range serverHostDiscoveryTargets {
		if ctx.Err() != nil {
			break
		}
		host, err := fetchPublicIPv4(ctx, client, endpoint)
		if err != nil {
			failures = append(failures, endpoint+": "+err.Error())
			continue
		}
		storeCachedServerHost(host, time.Now())
		return host, nil
	}
	if host := interfacePublicHost(); host != "" {
		log.Printf("[ServerHost] public-IP endpoints unavailable; using interface address %s", host)
		storeCachedServerHost(host, time.Now())
		return host, nil
	}
	if len(failures) > 0 {
		return "", fmt.Errorf("unable to determine the public server address (%s); set server_host in bot.json", strings.Join(failures, "; "))
	}
	return "", errors.New("unable to determine the public server address; set server_host in bot.json")
}

// GetPublicIP is retained for callers that only need an address string. It
// returns an empty string instead of the loopback address when discovery
// fails, so link generation cannot silently emit an unusable REALITY host.
func GetPublicIP() string {
	host, err := resolveServerHost(nil)
	if err != nil {
		log.Printf("[ServerHost] %v", err)
		return ""
	}
	return host
}
