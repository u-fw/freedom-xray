//go:build !linux

package main

import (
	"fmt"
	"os"
	"runtime"
	"time"
)

// GetSystemStats returns stub stats on non-Linux platforms
func GetSystemStats() (*SystemStats, error) {
	stats := &SystemStats{
		Platform:       runtime.GOOS + "/" + runtime.GOARCH,
		CPUCores:       runtime.NumCPU(),
		CPUUsagePct:    0.0,
		Load1:          "N/A",
		Load5:          "N/A",
		Load15:         "N/A",
		UptimeStr:      "N/A",
		BotUptime:      formatDuration(time.Since(botStartTime)),
		MemTotalBytes:  16 * 1024 * 1024 * 1024,
		MemUsedBytes:   8 * 1024 * 1024 * 1024,
		MemUsagePct:    50.0,
		DiskTotalBytes: 100 * 1024 * 1024 * 1024,
		DiskUsedBytes:  20 * 1024 * 1024 * 1024,
		DiskUsagePct:   20.0,
		XrayStatus:     "unsupported on " + runtime.GOOS,
	}

	if h, err := os.Hostname(); err == nil {
		stats.Hostname = h
	}

	return stats, nil
}

func formatDuration(d time.Duration) string {
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60
	seconds := int(d.Seconds()) % 60

	if days > 0 {
		return fmt.Sprintf("%dd %dh %dm", days, hours, minutes)
	}
	if hours > 0 {
		return fmt.Sprintf("%dh %dm %ds", hours, minutes, seconds)
	}
	return fmt.Sprintf("%dm %ds", minutes, seconds)
}
