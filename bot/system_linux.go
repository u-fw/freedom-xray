//go:build linux

package main

import (
	"bufio"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// GetSystemStats returns live hardware and OS stats on Linux
func GetSystemStats() (*SystemStats, error) {
	stats := &SystemStats{
		Platform: runtime.GOOS + "/" + runtime.GOARCH,
		CPUCores: runtime.NumCPU(),
	}

	// Hostname
	if h, err := os.Hostname(); err == nil {
		stats.Hostname = h
	}

	// Bot uptime
	stats.BotUptime = formatDuration(time.Since(botStartTime))

	// System uptime
	if upt, err := os.ReadFile("/proc/uptime"); err == nil {
		parts := strings.Fields(string(upt))
		if len(parts) > 0 {
			if sec, err := strconv.ParseFloat(parts[0], 64); err == nil {
				stats.UptimeStr = formatDuration(time.Duration(sec) * time.Second)
			}
		}
	}

	// Load Average
	if lavg, err := os.ReadFile("/proc/loadavg"); err == nil {
		parts := strings.Fields(string(lavg))
		if len(parts) >= 3 {
			stats.Load1 = parts[0]
			stats.Load5 = parts[1]
			stats.Load15 = parts[2]
		}
	}

	// Memory from /proc/meminfo
	readMemInfo(stats)

	// Disk usage for root /
	readDiskUsage(stats)

	// CPU usage sample
	stats.CPUUsagePct = sampleCPUUsage()

	// Xray service status
	stats.XrayStatus = CheckXrayServiceStatus()

	return stats, nil
}

func readMemInfo(stats *SystemStats) {
	file, err := os.Open("/proc/meminfo")
	if err != nil {
		return
	}
	defer file.Close()

	var memTotal, memAvailable uint64
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		key := strings.TrimSuffix(fields[0], ":")
		val, _ := strconv.ParseUint(fields[1], 10, 64)
		valBytes := val * 1024 // meminfo is in kB

		if key == "MemTotal" {
			memTotal = valBytes
		} else if key == "MemAvailable" {
			memAvailable = valBytes
		}
	}

	stats.MemTotalBytes = memTotal
	if memTotal > memAvailable {
		stats.MemUsedBytes = memTotal - memAvailable
		stats.MemUsagePct = (float64(stats.MemUsedBytes) / float64(memTotal)) * 100.0
	}
}

func readDiskUsage(stats *SystemStats) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs("/", &stat); err == nil {
		stats.DiskTotalBytes = stat.Blocks * uint64(stat.Bsize)
		free := stat.Bfree * uint64(stat.Bsize)
		if stats.DiskTotalBytes > free {
			stats.DiskUsedBytes = stats.DiskTotalBytes - free
			stats.DiskUsagePct = (float64(stats.DiskUsedBytes) / float64(stats.DiskTotalBytes)) * 100.0
		}
	}
}

func sampleCPUUsage() float64 {
	readCPU := func() (idle, total uint64) {
		file, err := os.Open("/proc/stat")
		if err != nil {
			return 0, 0
		}
		defer file.Close()

		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			fields := strings.Fields(scanner.Text())
			if len(fields) > 4 && fields[0] == "cpu" {
				var sum uint64
				for i := 1; i < len(fields); i++ {
					v, _ := strconv.ParseUint(fields[i], 10, 64)
					sum += v
					if i == 4 { // idle is 4th field
						idle = v
					}
				}
				total = sum
				return
			}
		}
		return 0, 0
	}

	idle1, total1 := readCPU()
	time.Sleep(100 * time.Millisecond)
	idle2, total2 := readCPU()

	totalDelta := float64(total2 - total1)
	idleDelta := float64(idle2 - idle1)

	if totalDelta <= 0 {
		return 0.0
	}
	usage := ((totalDelta - idleDelta) / totalDelta) * 100.0
	if usage < 0 {
		usage = 0
	} else if usage > 100 {
		usage = 100
	}
	return usage
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
