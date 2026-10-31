package main

import (
 "bytes"
 "context"
 "encoding/json"
 "fmt"
 "log"
 "os/signal"
 "sort"
 "strconv"
 "syscall"
 "os"
 "os/exec"
 "path/filepath"
 "strings"
 "time"
)

const statsUnitPath="/etc/systemd/system/freedom-stats.service"

// Routine Bot startup may inspect the collector but must never install it,
// restart Xray or make changes to the systemd service graph.
func trafficCollectorActive() bool {
 ctx,cancel:=context.WithTimeout(context.Background(),2*time.Second)
 defer cancel()
 return exec.CommandContext(ctx,"systemctl","is-active","--quiet","freedom-stats.service").Run()==nil
}

// applyVersionProvisioning closes the gap left by the in-menu self-update,
// which replaces the binary and restarts only the Bot service. Once per
// installed version it enables the Xray online-statistics policy (the core
// cannot report source IPs without it) and restarts the standalone sampler so
// it executes the new binary. Routine startups stay read-only.
func applyVersionProvisioning(cfg *BotConfig) {
 if cfg==nil||cfg.XrayConfigPath=="" {return}
 marker:=filepath.Join(filepath.Dir(cfg.XrayConfigPath),".freedom-bot-version")
 if raw,err:=os.ReadFile(marker);err==nil&&strings.TrimSpace(string(raw))==Version {return}
 if err:=ensureOnlineStatsPolicy(cfg);err!=nil {
  // Do not record the version: the next start retries, for example after the
  // operator fixes a core configuration the new policy was rejected against.
  log.Printf("[Init] online statistics policy provisioning failed: %v",err)
  return
 }
 if trafficCollectorActive() {
  if err:=runChecked("systemctl","restart","freedom-stats.service");err!=nil {
   log.Printf("[Init] freedom-stats restart after Bot upgrade failed: %v",err)
   return
  }
 }
 if err:=writeAtomically(marker,[]byte(Version+"\n"),0600);err!=nil {
  log.Printf("[Init] unable to record the provisioned Bot version: %v",err)
 }
}

// Runs independently of Telegram connectivity and authentication. The
// existing config lock serializes it with shell CLI and Bot reset/rename.
func runTrafficDaemon(cfg *BotConfig) error {
 ctx,stop:=context.WithCancel(context.Background())
 defer stop()
 signalStop:=make(chan os.Signal,1)
 signal.Notify(signalStop,os.Interrupt,syscall.SIGTERM)
 defer signal.Stop(signalStop)
 ticker:=time.NewTicker(5*time.Second)
 defer ticker.Stop()
 sample:=func(){
  if _,err:=snapshotTraffic(cfg.XrayConfigPath,cfg.XrayBinPath,cfg.StatsAPIServer);err!=nil {
   log.Printf("[freedom-stats] snapshot failed: %v",err)
  }
 }
 sample()
 for {
  select {
  case <-ctx.Done():return nil
  case <-signalStop:
   sample()
   return nil
  case <-ticker.C:sample()
  }
 }
}

// Enable every Xray statistics policy the Bot depends on: per-user counters and
// the online map for level 0 and each client level, plus the inbound counters
// used by the node traffic view. Preserving all other settings and validating
// before the guarded, rollback-capable restart keeps the operation safe on a
// live server.
func ensureOnlineStatsPolicy(cfg *BotConfig) error {
 if cfg.XrayConfigPath=="" {return fmt.Errorf("empty Xray config path")}
 lock,err:=acquireConfigLock(cfg.XrayConfigPath,25*time.Second)
 if err!=nil{return err}
 defer releaseConfigLock(lock)
 original,err:=os.ReadFile(cfg.XrayConfigPath)
 if err!=nil{return err}
 info,err:=os.Stat(cfg.XrayConfigPath)
 if err!=nil{return err}
 var root map[string]any
 if err=json.Unmarshal(original,&root);err!=nil{return err}
 if !enableStatsPolicy(root){return nil}
 next,err:=json.MarshalIndent(root,"","  ")
 if err!=nil{return err}
 next=append(next,'\n')
 file,err:=os.CreateTemp(filepath.Dir(cfg.XrayConfigPath),".freedom-online-policy-*.json")
 if err!=nil{return err}
 defer os.Remove(file.Name())
 if err=file.Chmod(info.Mode().Perm());err!=nil{file.Close();return err}
 if _,err=file.Write(next);err!=nil{file.Close();return err}
 if err=file.Close();err!=nil{return err}
 if err=runChecked(cfg.XrayBinPath,"run","-test","-config",file.Name());err!=nil{
  return fmt.Errorf("core rejected the statistics policy: %w",err)
 }
 snapshotTrafficBestEffortLocked(cfg.XrayConfigPath,cfg.XrayBinPath,cfg.StatsAPIServer)
 if err=writeAtomically(cfg.XrayConfigPath,next,info.Mode().Perm());err!=nil{return err}
 if err=runChecked("systemctl","restart","xray");err==nil {
  err=runChecked("systemctl","is-active","--quiet","xray")
 }
 if err!=nil{
  restoreErr:=writeAtomically(cfg.XrayConfigPath,original,info.Mode().Perm())
  _=runChecked("systemctl","restart","xray")
  return fmt.Errorf("enable statistics policy failed: %w (config restore: %v)",err,restoreErr)
 }
 return nil
}

// enableStatsPolicy mutates a decoded Xray config in place and reports whether
// any required statistics flag was missing. It is deliberately pure so the
// policy contents can be tested without touching systemd or a live core.
func enableStatsPolicy(root map[string]any) bool {
 policy,ok:=root["policy"].(map[string]any)
 if !ok {policy=map[string]any{};root["policy"]=policy}
 levels,ok:=policy["levels"].(map[string]any)
 if !ok {levels=map[string]any{};policy["levels"]=levels}
 system,ok:=policy["system"].(map[string]any)
 if !ok {system=map[string]any{};policy["system"]=system}
 changed:=false
 for _,flag:=range []string{"statsInboundUplink","statsInboundDownlink"} {
  if system[flag]!=true {system[flag]=true;changed=true}
 }

 wanted:=map[string]bool{"0":true}
 if inbounds,ok:=root["inbounds"].([]any);ok {
  for _,rawInbound:=range inbounds {
   inbound,ok:=rawInbound.(map[string]any);if !ok{continue}
   settings,ok:=inbound["settings"].(map[string]any);if !ok{continue}
   clients,ok:=settings["clients"].([]any);if !ok{continue}
   for _,rawClient:=range clients {
    client,ok:=rawClient.(map[string]any);if !ok{continue}
    switch level:=client["level"].(type) {
    case float64:
     wanted[strconv.Itoa(int(level))]=true
    case string:
     if level!=""{wanted[level]=true}
    }
   }
  }
 }

 names:=make([]string,0,len(wanted))
 for name:=range wanted{names=append(names,name)}
 sort.Strings(names)
 for _,name:=range names {
  level,ok:=levels[name].(map[string]any)
  if !ok {level=map[string]any{};levels[name]=level}
  for _,flag:=range []string{"statsUserUplink","statsUserDownlink","statsUserOnline"} {
   if level[flag]!=true {level[flag]=true;changed=true}
  }
 }
 return changed
}

// The Bot's first start after an update provisions the dedicated service.
// The CLI -local-traffic-install path does the same without any Bot Token.
func ensureTrafficCollectorUnit(configPath string,cfg *BotConfig) error {
 if os.Geteuid()!=0{return fmt.Errorf("statistics service install requires root")}
 if _,err:=os.Stat("/run/systemd/system");err!=nil{return fmt.Errorf("systemd not active: %w",err)}
 policyReady:=true
 if err:=ensureOnlineStatsPolicy(cfg);err!=nil{
  policyReady=false
  log.Printf("[freedom-stats] online IP collection not enabled: %v",err)
 }
 exe,err:=os.Executable();if err!=nil{return err}
 if strings.ContainsAny(exe+configPath,"\n\r") {return fmt.Errorf("invalid systemd path")}
 unit:=fmt.Sprintf(`[Unit]
Description=freedom-stats - independent Xray traffic and online-IP sampler
After=xray.service
Wants=xray.service

[Service]
Type=simple
User=root
ExecStart=%s -c %s -local-traffic-daemon
Restart=always
RestartSec=5
TimeoutStopSec=25
UMask=0077
NoNewPrivileges=true
PrivateTmp=true
ProtectHome=true

[Install]
WantedBy=multi-user.target
`,exe,configPath)
 old,_:=os.ReadFile(statsUnitPath)
 if !bytes.Equal(old,[]byte(unit)){
  if err=writeAtomically(statsUnitPath,[]byte(unit),0644);err!=nil{return err}
  if err=runChecked("systemctl","daemon-reload");err!=nil{return err}
 }
 if err=runChecked("systemctl","enable","freedom-stats.service");err!=nil{return err}
 // Ensures a replaced binary is picked up even if the unit content is stable.
 if err=runChecked("systemctl","restart","freedom-stats.service");err!=nil{return err}
 ctx,cancel:=context.WithTimeout(context.Background(),10*time.Second);defer cancel()
 if err=exec.CommandContext(ctx,"systemctl","is-active","--quiet","freedom-stats.service").Run();err!=nil{return err}
 // A complete install already provisioned this version, so the Bot's first
 // start need not repeat the work. A failed policy leaves the marker absent
 // and lets applyVersionProvisioning retry.
 if policyReady {
  marker:=filepath.Join(filepath.Dir(configPath),".freedom-bot-version")
  if err=writeAtomically(marker,[]byte(Version+"\n"),0600);err!=nil{
   log.Printf("[freedom-stats] unable to record the provisioned Bot version: %v",err)
  }
 }
 return nil
}
