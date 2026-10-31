package main

import (
 "encoding/json"
 "os"
 "path/filepath"
 "testing"
 "time"
)

func TestSQLiteMigrationBackupAndRestartPersistence(t *testing.T){
 dir:=t.TempDir()
 config:=filepath.Join(dir,"config.json")
 legacy:=[]byte(`{"users":{"alice":{"total_up":900,"total_down":100,"last_up":90,"last_down":10,"epoch":"abc"}},"inbounds":{"node":{"total_up":800,"total_down":99,"last_up":80,"last_down":9,"epoch":"abc"}}}`)
 if err:=os.WriteFile(trafficStorePath(config),legacy,0600);err!=nil{t.Fatal(err)}
 state,err:=loadTrafficStore(config)
 if err!=nil{t.Fatal(err)}
 if state.Users["alice"].TotalUp!=900||state.Inbounds["node"].TotalDown!=99{t.Fatalf("lost legacy counters: %+v",state)}
 bak,err:=os.ReadFile(trafficStorePath(config)+".pre-sqlite.bak")
 if err!=nil{t.Fatal(err)}
 if string(bak)!=string(legacy){t.Fatal("legacy backup changed")}
 if err=saveTrafficStore(config,state);err!=nil{t.Fatal(err)}
 restored,err:=loadTrafficStore(config)
 if err!=nil{t.Fatal(err)}
 if restored.Users["alice"].TotalUp!=900||restored.Inbounds["node"].TotalDown!=99 {t.Fatalf("failed to persist after restart: %+v",restored)}
 bak,err=os.ReadFile(trafficStorePath(config)+".pre-sqlite.bak");if err!=nil{t.Fatal(err)}
 if string(bak)!=string(legacy){t.Fatal("immutable backup overwritten")}
}

func TestSQLiteOnlineObservationsAndRetention(t *testing.T){
 config:=filepath.Join(t.TempDir(),"config.json")
 state,err:=loadTrafficStore(config)
 if err!=nil{t.Fatal(err)}
 now:=time.Unix(1800000000,0)
 if !applyOnlineSnapshot(&state,[]onlineUser{{Email:"alice",IPs:[]onlineClientIP{{IP:"203.0.113.4",LastSeen:now.Unix()}}}},now){t.Fatal("online snapshot unchanged")}
 if err=saveTrafficStore(config,state);err!=nil{t.Fatal(err)}
 again,err:=loadTrafficStore(config)
 if err!=nil{t.Fatal(err)}
 ips,known:=recentOnline(again,"alice",now)
 if !known||len(ips)!=1||ips[0].IP!="203.0.113.4"{t.Fatalf("live state missing: %+v %v",ips,known)}
 if !applyOnlineSnapshot(&again,nil,now.Add(5*time.Second)){t.Fatal("offline transition not recorded")}
 if len(again.LiveIPs)!=0{t.Fatal("stale live IP retained")}
 if again.LastOnline["alice"]!=now.Unix(){t.Fatal("offline poll changed last seen")}
 if !applyOnlineSnapshot(&again,nil,now.Add(31*time.Minute)){t.Fatal("stale IP not pruned")}
 if len(again.OnlineIPs)!=0{t.Fatal("stale IP history retained")}
 if _,known=recentOnline(again,"alice",now.Add(32*time.Minute));known{t.Fatal("stale poll must not report online")}
}

func TestSQLiteCorruptLegacyFailsClosed(t *testing.T){
 config:=filepath.Join(t.TempDir(),"config.json")
 original:=[]byte("{invalid")
 if err:=os.WriteFile(trafficStorePath(config),original,0600);err!=nil{t.Fatal(err)}
 if _,err:=loadTrafficStore(config);err==nil{t.Fatal("corrupt legacy journal was accepted")}
 data,err:=os.ReadFile(trafficStorePath(config));if err!=nil{t.Fatal(err)}
 if string(data)!=string(original){t.Fatal("corrupt file was replaced")}
}

func TestOnlinePollTimestampAndIPValidation(t *testing.T){
 now:=time.Now().Unix()
 raw,_:=json.Marshal(now)
 if parseOnlineTimestamp(raw,now)!=now{t.Fatal("numeric timestamp was not parsed")}
 if parseOnlineTimestamp([]byte(`"1"`),now)!=1{t.Fatal("string timestamp was not parsed")}
 if parseOnlineTimestamp([]byte(`"bogus"`),now)!=now{t.Fatal("invalid timestamp should fall back")}
}
