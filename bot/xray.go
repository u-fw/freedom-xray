package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// XrayClient represents an inbound client in config.json
type XrayClient struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Flow  string `json:"flow,omitempty"`
}

// InboundDetails holds parsed business node information
type InboundDetails struct {
	Tag           string
	Port          int
	Protocol      string
	Network       string
	Security      string
	SNI           string
	PublicKey     string
	PrivateKey    string
	MLDSA65Seed   string
	ShortID       string
	SpiderX       string
	XHTTPPath     string
	XHTTPHost     string
	XHTTPMode     string
	ClientEncrypt string
	Clients       []XrayClient
}

// UserTraffic holds traffic stats for one user
type UserTraffic struct {
	Email    string
	Uplink   uint64
	Downlink uint64
	Total    uint64
}

var (
	cachedPublicIP   string
	cachedPublicIPMu sync.RWMutex
	cachedIPExpire   time.Time

	// xrayBinPath is used by the x25519 / mldsa65 derivation helpers below.
	xrayBinPath = "xray"

	// Key derivations are deterministic; caching avoids spawning the xray
	// binary for every link refresh.
	derivationMu    sync.Mutex
	derivationCache = make(map[string]string)
)

// defaultRealitySNI mirrors DEFAULT_SNI in freedom.sh so bot-generated links
// stay usable even if a legacy node has no serverNames entry.
const defaultRealitySNI = "www.nvidia.com"

// SetXrayBinPath records the Xray binary used for client-link key derivation.
func SetXrayBinPath(path string) {
	if p := strings.TrimSpace(path); p != "" {
		xrayBinPath = p
	}
}

// runXrayHelper executes a short-lived xray CLI helper command.
func runXrayHelper(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, xrayBinPath, args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("xray %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

// fieldAfterColon extracts a `Key: value` field from xray CLI output.
// Keys may carry parenthetical suffixes, e.g. "Password (PublicKey): ...".
func fieldAfterColon(text string, keys ...string) string {
	wanted := make(map[string]bool, len(keys))
	for _, k := range keys {
		wanted[strings.ToLower(strings.TrimSpace(k))] = true
	}

	for _, line := range strings.Split(text, "\n") {
		idx := strings.Index(line, ":")
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		if p := strings.Index(key, " ("); p >= 0 {
			key = strings.TrimSpace(key[:p])
		}
		if !wanted[strings.ToLower(key)] {
			continue
		}
		return strings.TrimSpace(line[idx+1:])
	}
	return ""
}

// deriveRealityPublicKey mirrors `xray x25519 -i <privateKey>`; the server
// config only stores privateKey, while clients need the derived publicKey.
func deriveRealityPublicKey(privateKey string) string {
	if privateKey == "" {
		return ""
	}

	cacheKey := "x25519|" + privateKey
	derivationMu.Lock()
	cached, ok := derivationCache[cacheKey]
	derivationMu.Unlock()
	if ok {
		return cached
	}

	out, err := runXrayHelper("x25519", "-i", privateKey)
	if err != nil {
		log.Printf("[Xray] REALITY public key derivation failed: %v", err)
		return ""
	}

	pub := fieldAfterColon(out, "Password", "PublicKey", "Public key", "Public")
	if pub != "" {
		derivationMu.Lock()
		derivationCache[cacheKey] = pub
		derivationMu.Unlock()
	}
	return pub
}

// deriveMLDSA65Verify mirrors `xray mldsa65 -i <seed>`; clients receive the
// derived verify value as the `pqv` link parameter.
func deriveMLDSA65Verify(seed string) string {
	if seed == "" {
		return ""
	}

	cacheKey := "mldsa65|" + seed
	derivationMu.Lock()
	cached, ok := derivationCache[cacheKey]
	derivationMu.Unlock()
	if ok {
		return cached
	}

	out, err := runXrayHelper("mldsa65", "-i", seed)
	if err != nil {
		log.Printf("[Xray] ML-DSA-65 verify derivation failed: %v", err)
		return ""
	}

	verify := strings.Join(strings.Fields(fieldAfterColon(out, "Verify")), "")
	if verify != "" {
		derivationMu.Lock()
		derivationCache[cacheKey] = verify
		derivationMu.Unlock()
	}
	return verify
}

// GetPublicIP returns public IP with caching
func GetPublicIP() string {
	cachedPublicIPMu.RLock()
	if cachedPublicIP != "" && time.Now().Before(cachedIPExpire) {
		ip := cachedPublicIP
		cachedPublicIPMu.RUnlock()
		return ip
	}
	cachedPublicIPMu.RUnlock()

	client := &http.Client{Timeout: 3 * time.Second}
	endpoints := []string{
		"https://api.ipify.org",
		"https://icanhazip.com",
		"https://ifconfig.me/ip",
	}

	for _, ep := range endpoints {
		resp, err := client.Get(ep)
		if err != nil {
			continue
		}
		if resp.StatusCode != http.StatusOK {
			// Always close the body; a non-200 response must not leak the
			// underlying connection across 10-minute refresh cycles.
			resp.Body.Close()
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 256))
		resp.Body.Close()
		if readErr != nil {
			continue
		}
		// Public-IP endpoints are untrusted input; only accept a valid literal
		// address so it cannot inject markup into generated URIs.
		ip := strings.TrimSpace(string(body))
		if net.ParseIP(ip) != nil {
			cachedPublicIPMu.Lock()
			cachedPublicIP = ip
			cachedIPExpire = time.Now().Add(10 * time.Minute)
			cachedPublicIPMu.Unlock()
			return ip
		}
	}

	return "127.0.0.1"
}

// LoadXrayInbounds parses /usr/local/etc/xray/config.json into InboundDetails
func LoadXrayInbounds(configPath string) ([]InboundDetails, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("read xray config: %w", err)
	}

	var raw struct {
		Inbounds []map[string]interface{} `json:"inbounds"`
	}

	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse xray config: %w", err)
	}

	metaEncryptions := loadMetaClientEncryptions(
		filepath.Join(filepath.Dir(configPath), "freedom-meta.json"),
	)

	var inbounds []InboundDetails
	for _, in := range raw.Inbounds {
		tag, _ := in["tag"].(string)
		if tag == "api-internal" {
			continue // Skip internal management inbound
		}

		portFloat, _ := in["port"].(float64)
		port := int(portFloat)
		proto, _ := in["protocol"].(string)

		node := InboundDetails{
			Tag:           tag,
			Port:          port,
			Protocol:      proto,
			Network:       "tcp",
			Security:      "none",
			ClientEncrypt: "none",
		}
		if enc, ok := metaEncryptions[tag]; ok {
			node.ClientEncrypt = enc
		}

		// Clients
		if settings, ok := in["settings"].(map[string]interface{}); ok {
			if clientsRaw, ok := settings["clients"].([]interface{}); ok {
				for _, c := range clientsRaw {
					if cMap, ok := c.(map[string]interface{}); ok {
						id, _ := cMap["id"].(string)
						email, _ := cMap["email"].(string)
						flow, _ := cMap["flow"].(string)
						if id != "" {
							node.Clients = append(node.Clients, XrayClient{
								ID:    id,
								Email: email,
								Flow:  flow,
							})
						}
					}
				}
			}
		}

		// StreamSettings
		if stream, ok := in["streamSettings"].(map[string]interface{}); ok {
			if net, ok := stream["network"].(string); ok && net != "" {
				node.Network = net
			}
			if sec, ok := stream["security"].(string); ok && sec != "" {
				node.Security = sec
			}

			// REALITY Settings
			if reality, ok := stream["realitySettings"].(map[string]interface{}); ok {
				if names, ok := reality["serverNames"].([]interface{}); ok && len(names) > 0 {
					node.SNI, _ = names[0].(string)
				}
				node.PublicKey, _ = reality["publicKey"].(string)
				node.PrivateKey, _ = reality["privateKey"].(string)
				node.MLDSA65Seed, _ = reality["mldsa65Seed"].(string)
				node.SpiderX, _ = reality["spiderX"].(string)
				if sids, ok := reality["shortIds"].([]interface{}); ok && len(sids) > 0 {
					node.ShortID, _ = sids[0].(string)
				}
			}

			// TLS Settings
			if tls, ok := stream["tlsSettings"].(map[string]interface{}); ok {
				if sni, ok := tls["serverName"].(string); ok && sni != "" {
					node.SNI = sni
				}
				if certs, ok := tls["certificates"].([]interface{}); ok && len(certs) > 0 {
					if cert, ok := certs[0].(map[string]interface{}); ok {
						certFile, _ := cert["certificateFile"].(string)
						if domain := domainFromCertFile(certFile); domain != "" && node.SNI == "" {
							node.SNI = domain
						}
					}
				}
			}

			// XHTTP Settings
			if xhttp, ok := stream["xhttpSettings"].(map[string]interface{}); ok {
				node.XHTTPPath, _ = xhttp["path"].(string)
				node.XHTTPHost, _ = xhttp["host"].(string)
				node.XHTTPMode, _ = xhttp["mode"].(string)
			}
		}

		inbounds = append(inbounds, node)
	}

	return inbounds, nil
}

// loadMetaClientEncryptions reads the freedom-meta.json sidecar written by
// freedom.sh. Xray rejects unknown config.json fields, so the client-side
// VLESS encryption pairing string lives there, keyed by node tag.
func loadMetaClientEncryptions(metaPath string) map[string]string {
	result := make(map[string]string)

	data, err := os.ReadFile(metaPath)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("[Xray] read %s failed: %v", metaPath, err)
		}
		return result
	}

	var meta struct {
		Nodes map[string]map[string]interface{} `json:"nodes"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		log.Printf("[Xray] parse %s failed: %v", metaPath, err)
		return result
	}

	for tag, values := range meta.Nodes {
		if enc, ok := values["client_encryption"].(string); ok && enc != "" {
			result[tag] = enc
		}
	}
	return result
}

// domainFromCertFile derives the TLS domain from a certificate filename;
// freedom.sh stores certificates as <domain>.crt / <domain>.pem.
func domainFromCertFile(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	base := filepath.Base(path)
	base = strings.TrimSuffix(base, ".crt")
	base = strings.TrimSuffix(base, ".pem")
	if base == "" || base == "." || base == string(filepath.Separator) {
		return ""
	}
	return base
}

// formatHost wraps IPv6 literals in brackets for use in a URI authority.
func formatHost(host string) string {
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		return "[" + host + "]"
	}
	return host
}

// GenerateVlessURI builds a standard vless:// URI for the given client.
// The parameter set mirrors freedom.sh so the bot and the CLI produce the
// same working links: REALITY public key derived from privateKey, ML-DSA-65
// verify value derived from the seed, paired ML-KEM client encryption, and
// TLS nodes addressed through their certificate domain.
func (node *InboundDetails) GenerateVlessURI(client XrayClient, serverHost string) string {
	if serverHost == "" {
		serverHost = GetPublicIP()
	}

	host := serverHost
	flow := client.Flow
	encryption := node.ClientEncrypt
	if encryption == "" {
		encryption = "none"
	}

	q := url.Values{}
	q.Set("type", node.Network)
	q.Set("security", node.Security)

	switch node.Security {
	case "reality":
		sni := node.SNI
		if sni == "" {
			sni = defaultRealitySNI
		}
		q.Set("fp", "chrome")
		q.Set("sni", sni)

		publicKey := node.PublicKey
		if node.PrivateKey != "" {
			if derived := deriveRealityPublicKey(node.PrivateKey); derived != "" {
				publicKey = derived
			}
		}
		if publicKey != "" {
			q.Set("pbk", publicKey)
		} else {
			log.Printf("[Xray] node %s has no usable REALITY public key", node.Tag)
		}

		if node.ShortID != "" {
			q.Set("sid", node.ShortID)
		}

		spiderX := node.SpiderX
		if spiderX == "" {
			spiderX = "/"
		}
		q.Set("spx", spiderX)

		if node.MLDSA65Seed != "" {
			if verify := deriveMLDSA65Verify(node.MLDSA65Seed); verify != "" {
				q.Set("pqv", verify)
			} else {
				log.Printf("[Xray] node %s ML-DSA-65 verify derivation failed", node.Tag)
			}
		}

		switch node.Network {
		case "tcp":
			if flow == "" {
				flow = "xtls-rprx-vision"
			}
		case "xhttp":
			if node.XHTTPPath != "" {
				q.Set("path", node.XHTTPPath)
			}
			if node.XHTTPHost != "" {
				q.Set("host", node.XHTTPHost)
			}
			mode := node.XHTTPMode
			if mode == "" {
				mode = "auto"
			}
			q.Set("mode", mode)
			q.Set("extra", `{"xPaddingBytes":"100-1000"}`)
		}
	case "tls":
		// CDN-friendly: address the node through its certificate domain
		// rather than the origin IP.
		domain := node.SNI
		if domain == "" {
			domain = serverHost
		}
		host = domain
		q.Set("fp", "chrome")
		q.Set("sni", domain)
		q.Set("alpn", "h2")
		if node.XHTTPHost != "" {
			q.Set("host", node.XHTTPHost)
		}
		if node.Network == "xhttp" {
			if node.XHTTPPath != "" {
				q.Set("path", node.XHTTPPath)
			}
			mode := node.XHTTPMode
			if mode == "" {
				mode = "auto"
			}
			q.Set("mode", mode)
			q.Set("extra", `{"xPaddingBytes":"100-1000"}`)
		}
	default:
		if node.SNI != "" {
			q.Set("sni", node.SNI)
		}
	}

	if flow != "" {
		q.Set("flow", flow)
	}
	q.Set("encryption", encryption)

	remark := fmt.Sprintf("%s-%d-%s", node.Tag, node.Port, client.Email)
	return fmt.Sprintf("vless://%s@%s:%d?%s#%s",
		client.ID,
		formatHost(host),
		node.Port,
		q.Encode(),
		url.QueryEscape(remark),
	)
}

// QueryTrafficStats calls xray api statsquery to fetch user traffic
func QueryTrafficStats(ctx context.Context, xrayBin, statsServer string, reset bool) ([]UserTraffic, error) {
	args := []string{
		"api", "statsquery",
		"--server=" + statsServer,
		"-pattern=user>>>",
	}
	if reset {
		args = append(args, "-reset=true")
	}

	cmd := exec.CommandContext(ctx, xrayBin, args...)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("xray statsquery failed: %w (output: %s)", err, string(out))
	}

	var res struct {
		Stat []struct {
			Name  string      `json:"name"`
			Value interface{} `json:"value"` // can be string or number in different xray versions
		} `json:"stat"`
	}

	if err := json.Unmarshal(out, &res); err != nil {
		return nil, fmt.Errorf("parse stats json: %w", err)
	}

	// Map email -> UserTraffic
	trafficMap := make(map[string]*UserTraffic)

	for _, item := range res.Stat {
		// e.g. user>>>admin@freedom>>>traffic>>>downlink
		parts := strings.Split(item.Name, ">>>")
		if len(parts) < 4 || parts[0] != "user" || parts[2] != "traffic" {
			continue
		}

		email := parts[1]
		direction := parts[3]

		var val uint64
		switch v := item.Value.(type) {
		case string:
			val, _ = strconv.ParseUint(v, 10, 64)
		case float64:
			val = uint64(v)
		case int64:
			val = uint64(v)
		}

		ut, exists := trafficMap[email]
		if !exists {
			ut = &UserTraffic{Email: email}
			trafficMap[email] = ut
		}

		if direction == "uplink" {
			ut.Uplink += val
		} else if direction == "downlink" {
			ut.Downlink += val
		}
		ut.Total = ut.Uplink + ut.Downlink
	}

	var results []UserTraffic
	for _, ut := range trafficMap {
		results = append(results, *ut)
	}

	return results, nil
}
