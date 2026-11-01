package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// NodeChange only permits explicitly supported protocol settings. Secret
// carries key material that must never be rendered in a Telegram prompt;
// ClientSecret carries the paired client-side value written to freedom-meta.
type NodeChange struct {
	Kind         string
	Tag          string
	Value        string
	Secret       string
	ClientSecret string
}

var safeXHTTPPath = regexp.MustCompile(`^/[A-Za-z0-9/_~.-]{0,127}$`)
var safeDNSLabel = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)
var safeXHTTPHost = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)
var safeXHTTPPadding = regexp.MustCompile(`^([0-9]{1,5})-([0-9]{1,5})$`)
var safeRealityShortID = regexp.MustCompile(`^[0-9a-fA-F]{2}(?:[0-9a-fA-F]{2}){0,7}$`)
var safeRealityPrivateKey = regexp.MustCompile(`^[A-Za-z0-9_+/=-]{32,128}$`)
var safeNodeSecret = regexp.MustCompile(`^[A-Za-z0-9._+/=:-]+$`)

func validateRealityPrivateKey(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return errors.New("REALITY private key is required")
	}
	if !safeRealityPrivateKey.MatchString(value) {
		return errors.New("Invalid REALITY private key")
	}
	return nil
}

func validateNodeSecret(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return errors.New("A value is required")
	}
	if len(value) > 4096 {
		return errors.New("Value is too long")
	}
	if !safeNodeSecret.MatchString(value) {
		return errors.New("Value contains unsupported characters")
	}
	return nil
}

func validateRealityTarget(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("Target is required")
	}
	if strings.ContainsAny(value, " \t\r\n") {
		return "", errors.New("Target must not contain whitespace")
	}
	host, portText, err := net.SplitHostPort(value)
	if err != nil {
		return "", errors.New("Target must be host:port, for example www.example.com:443")
	}
	if err := validateXHTTPHost(host); err != nil {
		return "", err
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return "", errors.New("Target port must be between 1 and 65535")
	}
	return value, nil
}

func nodeTLSDomainFromSettings(tlsSettings map[string]any) string {
	if name, ok := tlsSettings["serverName"].(string); ok && name != "" {
		return name
	}
	if certs, ok := tlsSettings["certificates"].([]any); ok && len(certs) > 0 {
		if cert, ok := getMap(certs[0]); ok {
			if path, ok := cert["certificateFile"].(string); ok {
				return domainFromCertFile(path)
			}
		}
	}
	return ""
}

func validateNodeALPNText(value string) ([]any, error) {
	parts := strings.Split(value, ",")
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		item := strings.TrimSpace(part)
		if item == "" {
			continue
		}
		values = append(values, item)
	}
	if err := validateNodeALPN(values); err != nil {
		return nil, err
	}
	out := make([]any, 0, len(values))
	for _, item := range values {
		out = append(out, item)
	}
	return out, nil
}

func validateXHTTPHost(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if len(value) > 253 {
		return errors.New("Host is too long")
	}
	if strings.ContainsAny(value, "/:@ \t\r\n") {
		return errors.New("Host must be a DNS name or IP without scheme, path, port, or credentials")
	}
	if net.ParseIP(value) != nil {
		return nil
	}
	if !safeXHTTPHost.MatchString(value) {
		return errors.New("Invalid DNS hostname")
	}
	for _, label := range strings.Split(value, ".") {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return errors.New("Invalid DNS hostname label")
		}
	}
	return nil
}

func validateXHTTPPadding(value string) error {
	value = strings.TrimSpace(value)
	matches := safeXHTTPPadding.FindStringSubmatch(value)
	if matches == nil {
		return errors.New("Padding must be a range such as 100-1000")
	}
	start, err := strconv.Atoi(matches[1])
	if err != nil {
		return errors.New("Invalid padding start")
	}
	end, err := strconv.Atoi(matches[2])
	if err != nil {
		return errors.New("Invalid padding end")
	}
	if start > 65535 || end > 65535 {
		return errors.New("Padding values must not exceed 65535")
	}
	if start > end {
		return errors.New("Padding start must not exceed the end")
	}
	return nil
}

func validateRealityShortID(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if !safeRealityShortID.MatchString(value) {
		return errors.New("Short ID must contain 1–8 bytes as even-length hexadecimal")
	}
	return nil
}

func validateListenPort(value string) (int, error) {
	port, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || port < 1 || port > 65535 {
		return 0, errors.New("Listener port must be between 1 and 65535")
	}
	return port, nil
}

func inboundPortValue(value any) int {
	switch v := value.(type) {
	case float64:
		return int(v)
	case json.Number:
		n, err := v.Int64()
		if err == nil {
			return int(n)
		}
	case int:
		return v
	}
	return 0
}

func validateRealitySNI(value string) error {
	if len(value) > 253 || len(value) < 3 || strings.HasSuffix(value, ".") {
		return errors.New("Invalid DNS hostname length")
	}
	labels := strings.Split(value, ".")
	if len(labels) < 2 {
		return errors.New("Enter a fully qualified DNS hostname")
	}
	for _, label := range labels {
		if !safeDNSLabel.MatchString(label) {
			return errors.New("Only ASCII DNS hostnames are supported; use punycode for IDNs")
		}
	}
	if !regexp.MustCompile(`^[A-Za-z]`).MatchString(labels[len(labels)-1]) {
		return errors.New("Invalid top-level domain format")
	}
	return nil
}

func applyManagedNodeChange(root map[string]any, change NodeChange) (string, error) {
	inbounds, err := usableInbounds(root)
	if err != nil {
		return "", err
	}
	var target map[string]any
	for _, in := range inbounds {
		if in["tag"] == change.Tag {
			if target != nil {
				return "", errors.New("Duplicate inbound tag; operation rejected")
			}
			target = in
		}
	}
	if target == nil {
		return "", errors.New("Target inbound not found")
	}
	stream, ok := getMap(target["streamSettings"])
	if !ok {
		return "", errors.New("Invalid inbound streamSettings")
	}
	network, _ := stream["network"].(string)
	security, _ := stream["security"].(string)
	switch change.Kind {
	case "reality_sni":
		if security != "reality" {
			return "", errors.New("Only REALITY SNI edits are supported")
		}
		if err := validateRealitySNI(change.Value); err != nil {
			return "", err
		}
		settings, ok := getMap(stream["realitySettings"])
		if !ok {
			return "", errors.New("Invalid REALITY configuration")
		}
		names, ok := settings["serverNames"].([]any)
		if !ok || len(names) != 1 {
			return "", errors.New("Inbounds with multiple or missing SNI values must be edited locally")
		}
		old, ok := names[0].(string)
		if !ok || old == "" {
			return "", errors.New("Original SNI is invalid")
		}
		if strings.EqualFold(old, change.Value) {
			return "", errors.New("SNI unchanged")
		}
		settings["serverNames"] = []any{change.Value}
		// Only rewrite the standard generated target. Never clobber customized targets.
		oldTarget := old + ":443"
		newTarget := change.Value + ":443"
		adjusted := false
		for _, field := range []string{"target", "dest"} {
			if current, ok := settings[field].(string); ok && strings.EqualFold(current, oldTarget) {
				settings[field] = newTarget
				adjusted = true
			}
		}
		suffix := "Custom target/dest preserved. Verify the certificate matches the new SNI."
		if adjusted {
			suffix = "Default target/dest updated."
		}
		return fmt.Sprintf("REALITY inbound %s: SNI changed to %s. %s Clients must update SNI.", change.Tag, change.Value, suffix), nil
	case "path", "mode":
		if network != "xhttp" {
			return "", errors.New("Only XHTTP path and mode edits are supported")
		}
		settings, ok := getMap(stream["xhttpSettings"])
		if !ok {
			return "", errors.New("Invalid XHTTP inbound configuration")
		}
		if change.Kind == "path" {
			if !safeXHTTPPath.MatchString(change.Value) {
				return "", errors.New("Path must begin with / and contain up to 128 letters, digits, /, _, -, ., or ~")
			}
			if settings["path"] == change.Value {
				return "", errors.New("Path unchanged")
			}
			settings["path"] = change.Value
			return fmt.Sprintf("Inbound %s XHTTP path changed to %s. Clients must be updated.", change.Tag, change.Value), nil
		}
		switch change.Value {
		case "auto", "packet-up", "stream-up", "stream-one":
		default:
			return "", errors.New("Unsupported XHTTP mode")
		}
		if settings["mode"] == change.Value {
			return "", errors.New("Mode unchanged")
		}
		settings["mode"] = change.Value
		return fmt.Sprintf("Inbound %s XHTTP mode changed to %s. Some clients must reimport the link.", change.Tag, change.Value), nil
	case "port":
		port, err := validateListenPort(change.Value)
		if err != nil {
			return "", err
		}
		if inboundPortValue(target["port"]) == port {
			return "", errors.New("Listener port unchanged")
		}
		if raw, ok := root["inbounds"].([]any); ok {
			for _, value := range raw {
				other, ok := getMap(value)
				if !ok || other["tag"] == change.Tag {
					continue
				}
				if inboundPortValue(other["port"]) == port {
					return "", fmt.Errorf("Port %d is already used by inbound %v", port, other["tag"])
				}
			}
		}
		target["port"] = port
		return fmt.Sprintf("Inbound %s listener port changed to %d. Open %d/TCP in the firewall or security group, update any CDN Origin Rule, and reconfigure the public-port mapping. Xray was restarted after validation.", change.Tag, port, port), nil
	case "xhttp_host":
		if network != "xhttp" {
			return "", errors.New("Only XHTTP host edits are supported")
		}
		settings, ok := getMap(stream["xhttpSettings"])
		if !ok {
			return "", errors.New("Invalid XHTTP inbound configuration")
		}
		value := strings.TrimSpace(change.Value)
		if err := validateXHTTPHost(value); err != nil {
			return "", err
		}
		current, _ := settings["host"].(string)
		if current == value {
			return "", errors.New("XHTTP host unchanged")
		}
		if value == "" {
			delete(settings, "host")
			return fmt.Sprintf("Inbound %s XHTTP host cleared. Clients that pinned the old host must update their link.", change.Tag), nil
		}
		settings["host"] = value
		return fmt.Sprintf("Inbound %s XHTTP host changed to %s. Clients that pinned the old host must reimport the link.", change.Tag, value), nil
	case "xhttp_padding":
		if network != "xhttp" {
			return "", errors.New("Only XHTTP padding edits are supported")
		}
		settings, ok := getMap(stream["xhttpSettings"])
		if !ok {
			return "", errors.New("Invalid XHTTP inbound configuration")
		}
		value := strings.TrimSpace(change.Value)
		if err := validateXHTTPPadding(value); err != nil {
			return "", err
		}
		extra, ok := getMap(settings["extra"])
		if !ok && settings["extra"] != nil {
			return "", errors.New("XHTTP extra is not an object")
		}
		if extra == nil {
			extra = map[string]any{}
			settings["extra"] = extra
		}
		if current, _ := extra["xPaddingBytes"].(string); current == value {
			return "", errors.New("Padding unchanged")
		}
		extra["xPaddingBytes"] = value
		// Remove the legacy top-level copy so exports and Xray read one value.
		delete(settings, "xPaddingBytes")
		return fmt.Sprintf("Inbound %s XHTTP padding changed to %s. Clients must reimport the link to use the new range.", change.Tag, value), nil
	case "reality_short_id":
		if security != "reality" {
			return "", errors.New("Only REALITY short-ID edits are supported")
		}
		settings, ok := getMap(stream["realitySettings"])
		if !ok {
			return "", errors.New("Invalid REALITY configuration")
		}
		shortIDs, ok := settings["shortIds"].([]any)
		if !ok || len(shortIDs) != 1 {
			return "", errors.New("Inbounds with multiple or missing short IDs must be edited locally")
		}
		current, ok := shortIDs[0].(string)
		if !ok {
			return "", errors.New("Original short ID is invalid")
		}
		if err := validateRealityShortID(change.Value); err != nil {
			return "", err
		}
		if strings.EqualFold(current, change.Value) {
			return "", errors.New("Short ID unchanged")
		}
		settings["shortIds"] = []any{strings.TrimSpace(change.Value)}
		return fmt.Sprintf("REALITY inbound %s short ID changed. Clients must update sid or reimport the link.", change.Tag), nil
	case "reality_dest", "reality_target":
		if security != "reality" {
			return "", errors.New("Only REALITY destination edits are supported")
		}
		value, err := validateRealityTarget(change.Value)
		if err != nil {
			return "", err
		}
		settings, ok := getMap(stream["realitySettings"])
		if !ok {
			return "", errors.New("Invalid REALITY configuration")
		}
		if current, _ := settings[change.Kind[8:]].(string); current == value {
			return "", errors.New("Target unchanged")
		}
		settings[change.Kind[8:]] = value
		return fmt.Sprintf("REALITY inbound %s %s changed to %s. Verify the decoy server and certificate.", change.Tag, change.Kind[8:], value), nil
	case "reality_private_key":
		if security != "reality" {
			return "", errors.New("Only REALITY key edits are supported")
		}
		secret := strings.TrimSpace(change.Secret)
		if err := validateRealityPrivateKey(secret); err != nil {
			return "", err
		}
		settings, ok := getMap(stream["realitySettings"])
		if !ok {
			return "", errors.New("Invalid REALITY configuration")
		}
		settings["privateKey"] = secret
		// A stale publicKey would no longer match the rotated private key.
		delete(settings, "publicKey")
		return fmt.Sprintf("REALITY inbound %s private key rotated. The Bot derives the new public key for exports; every client must reimport its link.", change.Tag), nil
	case "reality_mldsa_seed":
		if security != "reality" {
			return "", errors.New("Only REALITY ML-DSA edits are supported")
		}
		settings, ok := getMap(stream["realitySettings"])
		if !ok {
			return "", errors.New("Invalid REALITY configuration")
		}
		secret := strings.TrimSpace(change.Secret)
		if secret == "" {
			if _, exists := settings["mldsa65Seed"]; !exists {
				return "", errors.New("ML-DSA-65 seed is already unset")
			}
			delete(settings, "mldsa65Seed")
			return fmt.Sprintf("ML-DSA-65 signatures disabled on %s. Clients must reimport updated links.", change.Tag), nil
		}
		if err := validateNodeSecret(secret); err != nil {
			return "", err
		}
		if current, _ := settings["mldsa65Seed"].(string); current == secret {
			return "", errors.New("ML-DSA-65 seed unchanged")
		}
		settings["mldsa65Seed"] = secret
		return fmt.Sprintf("ML-DSA-65 seed updated on %s. Clients must reimport updated links.", change.Tag), nil
	case "decryption":
		settings, ok := getMap(target["settings"])
		if !ok {
			return "", errors.New("Inbound lacks settings")
		}
		secret := strings.TrimSpace(change.Secret)
		if secret == "" || secret == "none" {
			if current, _ := settings["decryption"].(string); current == "" || current == "none" {
				return "", errors.New("VLESS encryption is already disabled")
			}
			settings["decryption"] = "none"
			return fmt.Sprintf("VLESS encryption disabled on %s. Clients must reimport links without encryption.", change.Tag), nil
		}
		if err := validateNodeSecret(secret); err != nil {
			return "", err
		}
		if current, _ := settings["decryption"].(string); current == secret {
			return "", errors.New("VLESS decryption unchanged")
		}
		settings["decryption"] = secret
		return fmt.Sprintf("VLESS encryption rotated on %s. The paired client value was saved for exports; every client must reimport its link.", change.Tag), nil
	case "tls_pair":
		if security != "tls" {
			return "", errors.New("Only TLS certificate edits are supported")
		}
		tlsSettings, ok := getMap(stream["tlsSettings"])
		if !ok {
			return "", errors.New("Invalid TLS configuration")
		}
		certs, ok := tlsSettings["certificates"].([]any)
		if !ok || len(certs) == 0 {
			return "", errors.New("TLS inbound has no certificate entry")
		}
		cert, ok := getMap(certs[0])
		if !ok {
			return "", errors.New("Invalid TLS certificate entry")
		}
		certPath := strings.TrimSpace(change.Value)
		keyPath := strings.TrimSpace(change.Secret)
		if !filepath.IsAbs(certPath) || !filepath.IsAbs(keyPath) {
			return "", errors.New("Certificate paths must be absolute")
		}
		certPath = filepath.Clean(certPath)
		keyPath = filepath.Clean(keyPath)
		if current, _ := cert["certificateFile"].(string); current == certPath {
			if currentKey, _ := cert["keyFile"].(string); currentKey == keyPath {
				return "", errors.New("Certificate paths unchanged")
			}
		}
		domain := nodeTLSDomainFromSettings(tlsSettings)
		if err := ValidateCertificatePair(certPath, keyPath, domain); err != nil {
			return "", err
		}
		cert["certificateFile"] = certPath
		cert["keyFile"] = keyPath
		return fmt.Sprintf("TLS certificate pair replaced on %s. The certificate/key pair and hostname coverage were verified; clients must reimport links if the certificate identity changed.", change.Tag), nil
	case "alpn":
		if security != "tls" {
			return "", errors.New("Only TLS ALPN edits are supported")
		}
		tlsSettings, ok := getMap(stream["tlsSettings"])
		if !ok {
			return "", errors.New("Invalid TLS configuration")
		}
		values, err := validateNodeALPNText(change.Value)
		if err != nil {
			return "", err
		}
		tlsSettings["alpn"] = values
		return fmt.Sprintf("TLS ALPN updated on %s to %s. Clients must support the same protocols.", change.Tag, strings.TrimSpace(change.Value)), nil
	case "listen":
		value := strings.TrimSpace(change.Value)
		if value == "" {
			if _, exists := target["listen"]; !exists {
				return "", errors.New("Listener address is already unset")
			}
			delete(target, "listen")
			return fmt.Sprintf("Inbound %s listens on all interfaces. Open the port only on interfaces you intend to expose.", change.Tag), nil
		}
		ip := net.ParseIP(value)
		if ip == nil {
			return "", errors.New("Listen address must be an IP address, for example 0.0.0.0 or 127.0.0.1")
		}
		if current, _ := target["listen"].(string); current == value {
			return "", errors.New("Listen address unchanged")
		}
		target["listen"] = value
		return fmt.Sprintf("Inbound %s listen address changed to %s.", change.Tag, value), nil
	default:
		return "", errors.New("This inbound operation is not on the safety allowlist")
	}
}

func ChangeNodeConfig(configPath, xrayBinary, statsServer string, change NodeChange) (string, error) {
	var sidecars []configSidecar
	if change.Kind == "decryption" {
		sidecars = append(sidecars, metaSetClientEncryptionSidecar(configPath, change.Tag, change.ClientSecret))
	}
	return commitManagedConfigTx(configPath, xrayBinary, statsServer, sidecars, func(root map[string]any) (string, error) {
		return applyManagedNodeChange(root, change)
	})
}
