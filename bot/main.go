package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
)

var (
	Version   = "v1.0.0"
	BuildTime = "2026-10-30"
)

func main() {
	configPath := flag.String("c", "/usr/local/etc/xray/bot.json", "path to bot.json configuration file")
	showVersion := flag.Bool("v", false, "display version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("freedom-bot %s (build %s)\n", Version, BuildTime)
		os.Exit(0)
	}

	// Fallback to local ./bot.json if default does not exist but local does
	if *configPath == "/usr/local/etc/xray/bot.json" {
		if _, err := os.Stat(*configPath); os.IsNotExist(err) {
			if _, localErr := os.Stat("bot.json"); localErr == nil {
				*configPath = "bot.json"
			}
		}
	}

	cfg, err := LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("[Fatal] Failed to load configuration from %s: %v", *configPath, err)
	}

	if cfg.Token == "" {
		log.Fatalf("[Fatal] Telegram Bot Token is empty. Please configure 'token' in %s", *configPath)
	}

	if len(cfg.AdminIDs) == 0 {
		log.Printf("[Warn] No admin_ids configured in %s. Bot will reject all commands until an admin is specified.", *configPath)
	} else {
		log.Printf("[Init] Configured %d authorized admin(s)", len(cfg.AdminIDs))
	}

	service, err := NewBotService(cfg)
	if err != nil {
		log.Fatalf("[Fatal] Failed to initialize Telegram Bot: %v", err)
	}

	// Handle graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigCh
		log.Println("[Shutdown] Signal received, stopping bot service...")
		service.Stop()
	}()

	if err := service.Start(); err != nil {
		log.Fatalf("[Fatal] Bot runtime error: %v", err)
	}
}
