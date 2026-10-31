package main

import (
 "bytes"
 "context"
 "encoding/json"
 "fmt"
 "log"
 "os/signal"
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

// Enable Xray's official online-stats policy once, preserving all other
// settings. Validate before the guarded, rollback-capable restart.
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
 policy,ok:=root["policy"].(map[string]any)
 if !ok {policy=map[string]any{};root["policy"]=policy}
 levels,ok:=policy["levels"].(map[string]any)
 if !ok {levels=map[string]any{};policy["levels"]=levels}
 level,ok:=levels["0"].(map[string]any)
 if !ok {level=map[string]any{};levels["0"]=level}
 if level["statsUserOnline"]==true{return nil}
 level["statsUserOnline"]=true
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
  return fmt.Errorf("core lacks online stats policy or rejected config: %w",err)
 }
 snapshotTrafficBestEffortLocked(cfg.XrayConfigPath,cfg.XrayBinPath,cfg.StatsAPIServer)
 if err=writeAtomically(cfg.XrayConfigPath,next,info.Mode().Perm());err!=nil{return err}
 if err=runChecked("systemctl","restart","xray");err==nil {
  err=runChecked("systemctl","is-active","--quiet","xray")
 }
 if err!=nil{
  restoreErr:=writeAtomically(cfg.XrayConfigPath,original,info.Mode().Perm())
  _=runChecked("systemctl","restart","xray")
  return fmt.Errorf("enable statsUserOnline failed: %w (config restore: %v)",err,restoreErr)
 }
 return nil
}

// The Bot's first start after an update provisions the dedicated service.
// The CLI -local-traffic-install path does the same without any Bot Token.
func ensureTrafficCollectorUnit(configPath string,cfg *BotConfig) error {
 if os.Geteuid()!=0{return fmt.Errorf("statistics service install requires root")}
 if _,err:=os.Stat("/run/systemd/system");err!=nil{return fmt.Errorf("systemd not active: %w",err)}
 if err:=ensureOnlineStatsPolicy(cfg);err!=nil{
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
 return exec.CommandContext(ctx,"systemctl","is-active","--quiet","freedom-stats.service").Run()
}
