package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
)

// Public ports describe an existing external HTTPS/Origin Rule route.
// They do not create a Cloudflare rule or modify Xray's listening port.
type publicPortEntry struct {
	ListenPort int `json:"listen_port"`
	PublicPort int `json:"public_port"`
}
type publicPortConfig struct {
	Nodes map[string]publicPortEntry `json:"nodes"`
}

func publicPortsPath(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), "freedom-public-ports.json")
}
func readPublicPorts(configPath string) (publicPortConfig, error) {
	v := publicPortConfig{Nodes: map[string]publicPortEntry{}}
	raw, err := os.ReadFile(publicPortsPath(configPath))
	if os.IsNotExist(err) {
		return v, nil
	}
	if err != nil {
		return v, err
	}
	if err = json.Unmarshal(raw, &v); err != nil {
		return v, fmt.Errorf("Invalid public port metadata JSON: %w", err)
	}
	if v.Nodes == nil {
		v.Nodes = map[string]publicPortEntry{}
	}
	for tag, e := range v.Nodes {
		if tag == "" || e.ListenPort < 1 || e.ListenPort > 65535 || e.PublicPort < 1 || e.PublicPort > 65535 {
			return v, fmt.Errorf("Public port metadata for %q contains an invalid port", tag)
		}
	}
	return v, nil
}

func applyPublicPortOverrides(nodes []InboundDetails, overrides publicPortConfig) {
	for i := range nodes {
		n := &nodes[i]
		n.PublicPort = 0 // invalid or stale overrides must never survive a reload
		entry, ok := overrides.Nodes[n.Tag]
		if !ok {
			continue
		}
		if entry.ListenPort != n.Port {
			log.Printf("[Bot] public-port override for %s ignored: listener changed from %d to %d", n.Tag, entry.ListenPort, n.Port)
			continue
		}
		if n.Network != "xhttp" || n.Security != "tls" {
			log.Printf("[Bot] public-port override for %s ignored: only XHTTP + TLS is supported", n.Tag)
			continue
		}
		n.PublicPort = entry.PublicPort
	}
}

func attachInboundPublicPorts(configPath string, nodes []InboundDetails) error {
	overrides, err := readPublicPorts(configPath)
	if err != nil {
		return err
	}
	applyPublicPortOverrides(nodes, overrides)
	return nil
}

// Public port is intentionally a sidecar mutation, not an Xray config edit:
// no Xray restart, no firewall modifications, no TLS changes.
func SetInboundPublicPort(configPath, tag string, publicPort int) (string, error) {
	if tag == "" || publicPort < 0 || publicPort > 65535 {
		return "", errors.New("Invalid public port; choose 0 (clear) or 1–65535")
	}
	lock, err := acquireConfigLock(configPath, 20_000_000_000)
	if err != nil {
		return "", err
	}
	defer releaseConfigLock(lock)
	data, err := os.ReadFile(configPath)
	if err != nil {
		return "", err
	}
	var root map[string]any
	if err = json.Unmarshal(data, &root); err != nil {
		return "", err
	}
	inbounds, err := usableInbounds(root)
	if err != nil {
		return "", err
	}
	found := false
	listenPort := 0
	for _, in := range inbounds {
		if in["tag"] != tag {
			continue
		}
		if found {
			return "", errors.New("Duplicate inbound tags; refusing overwrite")
		}
		stream, ok := getMap(in["streamSettings"])
		if !ok || stream["network"] != "xhttp" || stream["security"] != "tls" {
			return "", errors.New("Public CDN ports are supported only for XHTTP + TLS inbounds")
		}
		portFloat, ok := in["port"].(float64)
		if !ok || portFloat < 1 || portFloat > 65535 {
			return "", errors.New("Invalid inbound listener port")
		}
		listenPort = int(portFloat)
		found = true
	}
	if !found {
		return "", errors.New("Inbound not found")
	}
	cfg, err := readPublicPorts(configPath)
	if err != nil {
		return "", err
	}
	if publicPort == 0 || publicPort == listenPort {
		if _, exists := cfg.Nodes[tag]; !exists {
			return "", errors.New("No public-port override exists for this inbound")
		}
		delete(cfg.Nodes, tag)
	} else {
		if previous, ok := cfg.Nodes[tag]; ok && previous.ListenPort == listenPort && previous.PublicPort == publicPort {
			return "", errors.New("Public port unchanged")
		}
		cfg.Nodes[tag] = publicPortEntry{ListenPort: listenPort, PublicPort: publicPort}
	}
	encoded, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", err
	}
	if err = writeAtomically(publicPortsPath(configPath), append(encoded, 10), 0600); err != nil {
		return "", err
	}
	if publicPort == 0 || publicPort == listenPort {
		return fmt.Sprintf("Cleared public-port override for %s; exports now use listener port %d. No Xray restart needed.", tag, listenPort), nil
	}
	return fmt.Sprintf("Set %s: public port %d → origin port %d. Affects exports only; configure Cloudflare Origin Rules yourself. No Xray restart needed.", tag, publicPort, listenPort), nil
}
