package main

import (
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// SystemStats represents real-time system metrics
type SystemStats struct {
	Hostname       string
	Platform       string
	UptimeStr      string
	CPUCores       int
	CPUUsagePct    float64
	Load1          string
	Load5          string
	Load15         string
	MemTotalBytes  uint64
	MemUsedBytes   uint64
	MemUsagePct    float64
	DiskTotalBytes uint64
	DiskUsedBytes  uint64
	DiskUsagePct   float64
	XrayStatus     string
	BotUptime      string
}

var botStartTime = time.Now()

// FormatBytes formats raw byte count to readable string
func FormatBytes(bytes uint64) string {
	const (
		KB = 1024
		MB = 1024 * KB
		GB = 1024 * MB
		TB = 1024 * GB
	)

	switch {
	case bytes >= TB:
		return fmt.Sprintf("%.2f TB", float64(bytes)/float64(TB))
	case bytes >= GB:
		return fmt.Sprintf("%.2f GB", float64(bytes)/float64(GB))
	case bytes >= MB:
		return fmt.Sprintf("%.2f MB", float64(bytes)/float64(MB))
	case bytes >= KB:
		return fmt.Sprintf("%.2f KB", float64(bytes)/float64(KB))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}

// CheckXrayServiceStatus checks if xray.service is active
func CheckXrayServiceStatus() string {
	cmd := exec.Command("systemctl", "is-active", "xray")
	out, err := cmd.Output()
	if err != nil {
		status := strings.TrimSpace(string(out))
		if status != "" {
			return status
		}
		return "inactive"
	}
	return strings.TrimSpace(string(out))
}

// RestartXrayService attempts to restart xray systemd service
func RestartXrayService() error {
	cmd := exec.Command("systemctl", "restart", "xray")
	return cmd.Run()
}
