package main

import (
	"flag"
	"fmt"
 "encoding/json"
	"log"
	"os"
	"os/signal"
	"syscall"
)

var (
	Version   = "v1.14.0"
	Commit    = "dev"
	BuildTime = "unknown"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "agent" {
		os.Exit(runAgentCLI(os.Args[2:], os.Stdin, os.Stdout))
	}
	configPath := flag.String("c", "/usr/local/etc/xray/bot.json", "path to bot.json configuration file")
	showVersion := flag.Bool("v", false, "display version and exit")
	localAction := flag.String("local-user-action", "", "offline CLI user management action (root only)")
 localNode := flag.String("local-node", "", "target inbound tag for bind/unbind or initial assignment")
 localNewName := flag.String("local-new-name", "", "new name for rename")
 localList := flag.Bool("local-list-users", false, "list independent users without Telegram")
	localUser := flag.String("local-user", "", "user email/name for offline action")
 localTrafficSnapshot := flag.Bool("local-traffic-snapshot",false,"collect and persist current Xray traffic (root only)")
 localTrafficSnapshotLocked := flag.Bool("local-traffic-snapshot-locked",false,"collect traffic with config lock already held by caller (root only)")
 localTrafficRead := flag.Bool("local-traffic-read",false,"print persisted traffic JSON (root only)")
 localTrafficDaemon:=flag.Bool("local-traffic-daemon",false,"standalone persistent Xray traffic sampler")
 localTrafficInstall:=flag.Bool("local-traffic-install",false,"install or refresh standalone collector service (root only)")
 localTrafficResetLocked:=flag.Bool("local-traffic-reset-locked",false,"reset persisted traffic under caller-held config lock")
 localUserTrafficResetLocked:=flag.Bool("local-user-traffic-reset-locked",false,"reset a user's Xray and persisted counters under caller-held config lock")
 localMigrateBindings:=flag.Bool("local-migrate-bindings",false,"rewrite managed client emails to per-inbound binding identities (root only)")
 localRevertBindings:=flag.Bool("local-revert-bindings",false,"rewrite binding client emails back to plain usernames (root only)")
	flag.Parse()

	if *showVersion {
		fmt.Printf("freedom-bot %s (commit %s, build %s)\n", Version, Commit, BuildTime)
		os.Exit(0)
	}

 if *localMigrateBindings&&*localRevertBindings {
  log.Fatal("choose only one of -local-migrate-bindings or -local-revert-bindings")
 }
 if *localMigrateBindings||*localRevertBindings {
  if os.Geteuid()!=0 {log.Fatal("binding migration requires root")}
  cfg,err:=LoadConfig(*configPath);if err!=nil{log.Fatalf("load config: %v",err)}
  var stats bindingMigrationStats
  if *localMigrateBindings {
   stats,err=migrateBindingEmailsForced(cfg.XrayConfigPath,cfg.XrayBinPath,cfg.StatsAPIServer)
  }else{
   stats,err=revertBindingEmails(cfg.XrayConfigPath,cfg.XrayBinPath,cfg.StatsAPIServer)
  }
  if err!=nil{log.Fatalf("binding migration failed: %v",err)}
  fmt.Println(stats.String())
  if warning:=bindingMigrationWarning(stats.SuspendedSkipped);warning!="" {
   fmt.Printf("suspended records skipped: %s\n",warning)
  }
  if stats.BackupPath!=""{fmt.Printf("accounting backup: %s\n",stats.BackupPath)}
  return
 }


 if *localTrafficDaemon||*localTrafficInstall {
  cfg,err:=LoadConfig(*configPath);if err!=nil{log.Fatal(err)}
  if *localTrafficDaemon {if err=runTrafficDaemon(cfg);err!=nil{log.Fatal(err)};return}
  if err=ensureTrafficCollectorUnit(*configPath,cfg);err!=nil{log.Fatal(err)}
  fmt.Println("freedom-stats.service enabled and running")
  return
 }
 if *localTrafficSnapshot||*localTrafficSnapshotLocked||*localTrafficRead||*localTrafficResetLocked||*localUserTrafficResetLocked {
  if os.Geteuid()!=0 {log.Fatal("traffic maintenance requires root")}
  cfg,err:=LoadConfig(*configPath);if err!=nil{log.Fatalf("load config: %v",err)}
  if *localUserTrafficResetLocked {
   if *localUser==""{log.Fatal("-local-user required")}
   if err=resetUserTrafficLocked(cfg.XrayConfigPath,cfg.XrayBinPath,cfg.StatsAPIServer,*localUser);err!=nil{log.Fatalf("reset user traffic: %v",err)}
   fmt.Println("user traffic reset completed")
   return
  }
  if *localTrafficResetLocked {
   if *localUser==""{err=resetTrafficAllUsersLocked(cfg.XrayConfigPath)}else{err=resetTrafficUserLocked(cfg.XrayConfigPath,*localUser)}
   if err!=nil{log.Fatalf("reset stored traffic: %v",err)}
   fmt.Println("persistent traffic reset completed")
   return
  }
  if *localTrafficRead{
   state,err:=loadTrafficStore(cfg.XrayConfigPath)
   if err!=nil{log.Fatalf("read traffic: %v",err)}
   raw,err:=json.Marshal(state);if err!=nil{log.Fatal(err)}
   fmt.Println(string(raw))
   return
  }
  if *localTrafficSnapshotLocked {
   state,err:=snapshotTrafficLocked(cfg.XrayConfigPath,cfg.XrayBinPath,cfg.StatsAPIServer)
   if err!=nil{log.Fatalf("snapshot: %v",err)}
   fmt.Printf("saved users=%d inbounds=%d\n",len(state.Users),len(state.Inbounds))
   return
  }
  state,err:=snapshotTraffic(cfg.XrayConfigPath,cfg.XrayBinPath,cfg.StatsAPIServer)
  if err!=nil{log.Fatalf("snapshot: %v",err)}
  fmt.Printf("saved users=%d inbounds=%d\n",len(state.Users),len(state.Inbounds))
  return
 }
 if *localList || *localAction!="" {
  if os.Geteuid()!=0{log.Fatal("local user operations require root")}
  cfg,err:=LoadConfig(*configPath);if err!=nil{log.Fatalf("load config: %v",err)}
  if *localList {
   names,err:=allDirectoryNames(cfg.XrayConfigPath)
   if err!=nil{log.Fatalf("list users: %v",err)}
   for _,name:=range names{fmt.Println(name)}
   return
  }
  valid:=map[string]bool{"add":true,"rename":true,"rotate":true,"delete":true,"assign":true,"unassign":true,"disable":true,"enable":true}
  if !valid[*localAction]{log.Fatal("unsupported local action")}
  if *localUser==""{log.Fatal("-local-user required")}
  change:=UserChange{Kind:*localAction,User:*localUser,Node:*localNode,NewName:*localNewName}
  result,err:=changeManagedUser(cfg.XrayConfigPath,cfg.XrayBinPath,cfg.StatsAPIServer,change)
  if err!=nil{log.Fatalf("local user operation failed: %v",err)}
  fmt.Println(result)
  return
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

 // One-time upgrade to per-inbound accounting identities. A failure is not
 // fatal: the Bot remains usable and the migration retries on the next start.
 bindingStats,bindingErr:=migrateBindingEmails(cfg.XrayConfigPath,cfg.XrayBinPath,cfg.StatsAPIServer)
 if bindingErr!=nil {
  log.Printf("[Binding] per-node accounting migration failed and will be retried: %v",bindingErr)
 }else if bindingStats.changed() {
  log.Printf("[Binding] per-node accounting enabled: %s (backup %s)",bindingStats.String(),bindingStats.BackupPath)
 }
 if warning:=bindingMigrationWarning(bindingStats.SuspendedSkipped);warning!="" {
  log.Printf("[Binding] suspended records were preserved without rewriting: %s",warning)
 }

 // Routine Bot startup must not modify Xray configuration or systemd units.
 // applyVersionProvisioning is the one exception: it runs once per installed
 // version so an in-menu upgrade still enables the online-statistics policy
 // and refreshes the standalone sampler.
 applyVersionProvisioning(cfg)
 collectorReady:=trafficCollectorActive()
 if !collectorReady {
  log.Printf("[freedom-stats] dedicated collector unavailable; using Bot sampler")
 }
 service, err := NewBotService(cfg)
	if err != nil {
		log.Fatalf("[Fatal] Failed to initialize Telegram Bot: %v", err)
	}

	service.collectorReady=collectorReady
 if bindingErr==nil&&bindingStats.changed() {
  service.startupNotice=tr(cfg.Language,"binding.notify.migrated",bindingStats.Rewritten,bindingStats.Inbounds)
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
