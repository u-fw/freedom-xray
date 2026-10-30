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
	AdminIDs         []int64 `json:"admin_ids"`
	Socks5Proxy      string  `json:"socks5_proxy,omitempty"` // e.g. "127.0.0.1:10808"
	ReportHours      int     `json:"report_hours,omitempty"` // periodic report interval in hours (0 = disabled)
	XrayConfigPath   string  `json:"xray_config_path"`
	XrayBinPath      string  `json:"xray_bin_path"`
	StatsAPIServer   string  `json:"stats_api_server"`
}

// DefaultConfig returns default paths and settings
func DefaultConfig() *BotConfig {
	return &BotConfig{
		Token:          "",
		AdminIDs:       []int64{},
		Socks5Proxy:    "",
		ReportHours:    12, // Default every 12 hours
		XrayConfigPath: "/usr/local/etc/xray/config.json",
		XrayBinPath:    "/usr/local/bin/xray",
		StatsAPIServer: "127.0.0.1:10085",
	}
}

// LoadConfig reads config from given path
func LoadConfig(path string) (*BotConfig, error) {
	cfg := DefaultConfig()
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
