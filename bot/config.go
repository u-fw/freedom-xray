package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// BotConfig represents freedom-bot configuration options
type BotConfig struct {
	Token            string  `json:"token"`
  Language string `json:"language,omitempty"`
  LanguageExplicit bool `json:"language_explicit,omitempty"`
 ConfigFile string `json:"-"`
	AdminIDs         []int64 `json:"admin_ids"`
	Socks5Proxy      string  `json:"socks5_proxy,omitempty"` // e.g. "127.0.0.1:10808"
	ReportHours      int     `json:"report_hours,omitempty"`
	AllowMutations   bool    `json:"allow_mutations,omitempty"`
	AllowUpdates bool `json:"allow_updates,omitempty"`
	AllowCoreUpdates bool `json:"allow_core_updates,omitempty"`
	AllowSensitiveBackup bool `json:"allow_sensitive_backup,omitempty"` // allow full native configuration exports through Telegram
	XrayConfigPath   string  `json:"xray_config_path"`
	XrayBinPath      string  `json:"xray_bin_path"`
	StatsAPIServer   string  `json:"stats_api_server"`
	// ServerHost is the public address clients use to reach this VPS. It is
	// required for REALITY links when automatic public-IP discovery is blocked
	// by NAT, a firewall, or an outbound proxy.
	ServerHost       string  `json:"server_host,omitempty"`
}

// DefaultConfig returns default paths and settings
func DefaultConfig() *BotConfig {
	return &BotConfig{
		Token:          "",
  Language: "en",
		AdminIDs:       []int64{},
		Socks5Proxy:    "",
		ReportHours:    12, // Default every 12 hours
		AllowMutations: false, // Local opt-in is required before Telegram can write configurations
		AllowUpdates: false,
		AllowCoreUpdates: false,
		AllowSensitiveBackup: false,
		XrayConfigPath: "/usr/local/etc/xray/config.json",
		XrayBinPath:    "/usr/local/bin/xray",
		StatsAPIServer: "127.0.0.1:10085",
	}
}

// LoadConfig reads config from given path
func LoadConfig(path string) (*BotConfig, error) {
	cfg := DefaultConfig()
 cfg.ConfigFile=path
 data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, fmt.Errorf("read config error: %w", err)
	}

	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config error: %w", err)
	}

	// The English locale is the default for all installs and upgrades.
 cfg.Language=normalizeLanguage(cfg.Language)
 // Older bot.json files have no language_explicit marker. A non-English
 // language there was an explicit choice, so do not auto-override it.
 var rawConfig map[string]json.RawMessage
 if json.Unmarshal(data,&rawConfig)==nil {
  if _,ok:=rawConfig["language_explicit"];!ok && cfg.Language!="en" {
   cfg.LanguageExplicit=true
  }
 }
 // Fallback empty paths to defaults
	if cfg.XrayConfigPath == "" {
		cfg.XrayConfigPath = "/usr/local/etc/xray/config.json"
	}
	if cfg.XrayBinPath == "" {
		cfg.XrayBinPath = "/usr/local/bin/xray"
	}
	if cfg.StatsAPIServer == "" {
		cfg.StatsAPIServer = "127.0.0.1:10085"
	}
	if cfg.ServerHost != "" {
		host, hostErr := normalizeServerHost(cfg.ServerHost)
		if hostErr != nil {
			return nil, fmt.Errorf("invalid server_host in %s: %w", path, hostErr)
		}
		cfg.ServerHost = host
	}

	return cfg, nil
}

// Save writes config to file atomically
func (c *BotConfig) Save(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}

	tmpFile := path + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0600); err != nil {
		return err
	}

	return os.Rename(tmpFile, path)
}

// IsAdmin checks if given Telegram user ID is authorized
func (c *BotConfig) IsAdmin(userID int64) bool {
	for _, id := range c.AdminIDs {
		if id == userID {
			return true
		}
	}
	return false
}
