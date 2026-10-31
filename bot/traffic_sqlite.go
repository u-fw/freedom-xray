package main

import (
 "database/sql"
 "encoding/json"
 "errors"
 "fmt"
 "os"
 "path/filepath"
 "time"

 _ "modernc.org/sqlite"
)

// The SQLite file is the canonical accounting ledger. The previous JSON file
// is preserved as an upgrade backup and kept as a compatibility mirror for
// the shell menu and older Bot binaries.
func trafficDBPath(configPath string) string {
 return filepath.Join(filepath.Dir(configPath), "freedom-stats.db")
}
func readLegacyTraffic(configPath string) (trafficStore, error) {
 state:=trafficStore{Users:map[string]trafficCounter{},Inbounds:map[string]trafficCounter{},LastOnline:map[string]int64{},OnlineIPs:map[string][]onlineClientIP{},LiveIPs:map[string][]onlineClientIP{}}
 b,err:=os.ReadFile(trafficStorePath(configPath))
 if errors.Is(err,os.ErrNotExist) {return state,nil}
 if err!=nil{return state,err}
 if err=json.Unmarshal(b,&state);err!=nil{return state,fmt.Errorf("Legacy traffic JSON is corrupted; refusing migration or overwrite: %w",err)}
 ensureTrafficMaps(&state)
 return state,nil
}
// Keep a one-time copy of the legacy ledger before the SQLite mirror
// eventually replaces freedom-stats.json. The copy is never overwritten.
func preserveLegacyTrafficBackup(configPath string) error {
 original:=trafficStorePath(configPath)
 data,err:=os.ReadFile(original)
 if errors.Is(err,os.ErrNotExist){return nil}
 if err!=nil{return err}
 backup:=original+".pre-sqlite.bak"
 if _,err=os.Stat(backup);err==nil{return nil}else if !errors.Is(err,os.ErrNotExist){return err}
 return writeAtomically(backup,data,0600)
}
func ensureTrafficMaps(state *trafficStore) {
 if state.Users==nil {state.Users=map[string]trafficCounter{}}
 if state.Inbounds==nil {state.Inbounds=map[string]trafficCounter{}}
 if state.LastOnline==nil {state.LastOnline=map[string]int64{}}
 if state.OnlineIPs==nil {state.OnlineIPs=map[string][]onlineClientIP{}}
 if state.LiveIPs==nil {state.LiveIPs=map[string][]onlineClientIP{}}
}
func openTrafficDB(configPath string) (*sql.DB,error) {
 path:=trafficDBPath(configPath)
 if err:=os.MkdirAll(filepath.Dir(path),0755);err!=nil{return nil,err}
 db,err:=sql.Open("sqlite",path)
 if err!=nil{return nil,err}
 db.SetMaxOpenConns(1)
 // WAL readers never block a writer; FULL synchronizes committed WAL frames.
 for _,statement:=range []string{
  "PRAGMA busy_timeout=5000",
  "PRAGMA journal_mode=WAL",
  "PRAGMA synchronous=FULL",
  "CREATE TABLE IF NOT EXISTS counters (kind TEXT NOT NULL, name TEXT NOT NULL, total_up INTEGER NOT NULL, total_down INTEGER NOT NULL, last_up INTEGER NOT NULL, last_down INTEGER NOT NULL, epoch TEXT NOT NULL DEFAULT '', PRIMARY KEY(kind,name))",
  "CREATE TABLE IF NOT EXISTS last_online (email TEXT PRIMARY KEY, at INTEGER NOT NULL)",
  "CREATE TABLE IF NOT EXISTS live_online (email TEXT NOT NULL, ip TEXT NOT NULL, last_seen INTEGER NOT NULL, PRIMARY KEY(email,ip))",
  "CREATE TABLE IF NOT EXISTS online_ips (email TEXT NOT NULL, ip TEXT NOT NULL, last_seen INTEGER NOT NULL, PRIMARY KEY(email,ip))",
  "CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)",
 } {
  if _,err=db.Exec(statement);err!=nil{db.Close();return nil,fmt.Errorf("SQLite init (%s): %w",statement,err)}
 }
 var marker string
 err=db.QueryRow("SELECT value FROM meta WHERE key='legacy_migrated'").Scan(&marker)
 if err!=nil && !errors.Is(err,sql.ErrNoRows){db.Close();return nil,err}
 if errors.Is(err,sql.ErrNoRows){
  legacy,e:=readLegacyTraffic(configPath)
  if e!=nil{db.Close();return nil,e}
  if e=preserveLegacyTrafficBackup(configPath);e!=nil{db.Close();return nil,fmt.Errorf("backup legacy accounting ledger: %w",e)}
  tx,e:=db.Begin();if e!=nil{db.Close();return nil,e}
  // Concurrent first-time readers cannot import twice (IGNORE + primary key).
  if e=writeTrafficTx(tx,legacy,true);e==nil {
   _,e=tx.Exec("INSERT OR IGNORE INTO meta(key,value) VALUES ('legacy_migrated','1')")
  }
  if e!=nil {tx.Rollback();db.Close();return nil,e}
  if e=tx.Commit();e!=nil{db.Close();return nil,e}
 }
 if err=os.Chmod(path,0600);err!=nil{db.Close();return nil,err}
 return db,nil
}
func writeTrafficTx(tx *sql.Tx, state trafficStore, migrate bool) error {
 for kind,group:=range map[string]map[string]trafficCounter{"user":state.Users,"inbound":state.Inbounds}{
  for name,c:=range group {
   statement:="INSERT INTO counters(kind,name,total_up,total_down,last_up,last_down,epoch) VALUES(?,?,?,?,?,?,?)"
   if migrate {statement="INSERT OR IGNORE INTO counters(kind,name,total_up,total_down,last_up,last_down,epoch) VALUES(?,?,?,?,?,?,?)"}
   if _,err:=tx.Exec(statement,kind,name,int64(c.TotalUp),int64(c.TotalDown),int64(c.LastUp),int64(c.LastDown),c.Epoch);err!=nil{return err}
  }
 }
 for name,at:=range state.LastOnline {
  statement:="INSERT INTO last_online(email,at) VALUES(?,?)"
  if migrate {statement="INSERT OR IGNORE INTO last_online(email,at) VALUES(?,?)"}
  if _,err:=tx.Exec(statement,name,at);err!=nil{return err}
 }
 for name,ips:=range state.OnlineIPs {
  for _,ip:=range ips {
   statement:="INSERT INTO online_ips(email,ip,last_seen) VALUES(?,?,?)"
   if migrate {statement="INSERT OR IGNORE INTO online_ips(email,ip,last_seen) VALUES(?,?,?)"}
   if _,err:=tx.Exec(statement,name,ip.IP,ip.LastSeen);err!=nil{return err}
  }
 }
 for name,ips:=range state.LiveIPs {
  for _,ip:=range ips {
   statement:="INSERT INTO live_online(email,ip,last_seen) VALUES(?,?,?)"
   if migrate {statement="INSERT OR IGNORE INTO live_online(email,ip,last_seen) VALUES(?,?,?)"}
   if _,err:=tx.Exec(statement,name,ip.IP,ip.LastSeen);err!=nil{return err}
  }
 }
 if _,err:=tx.Exec("INSERT OR REPLACE INTO meta(key,value) VALUES('online_poll_at',?)",state.OnlinePollAt);err!=nil{return err}
 return nil
}
func loadTrafficStore(configPath string) (trafficStore,error) {
 state:=trafficStore{}
 ensureTrafficMaps(&state)
 db,err:=openTrafficDB(configPath);if err!=nil{return state,err}
 defer db.Close()
 rows,err:=db.Query("SELECT kind,name,total_up,total_down,last_up,last_down,epoch FROM counters")
 if err!=nil{return state,err}
 for rows.Next() {
  var kind,name,epoch string
  var up,down,lastUp,lastDown int64
  if err=rows.Scan(&kind,&name,&up,&down,&lastUp,&lastDown,&epoch);err!=nil{break}
  if up<0||down<0||lastUp<0||lastDown<0 {err=fmt.Errorf("invalid negative traffic counter for %s",name);break}
  c:=trafficCounter{TotalUp:uint64(up),TotalDown:uint64(down),LastUp:uint64(lastUp),LastDown:uint64(lastDown),Epoch:epoch}
  switch kind {case "user":state.Users[name]=c;case "inbound":state.Inbounds[name]=c}
 }
 if e:=rows.Err();err==nil{err=e}
 rows.Close();if err!=nil{return state,err}
 online,err:=db.Query("SELECT email,at FROM last_online")
 if err!=nil{return state,err}
 for online.Next(){var name string;var at int64;if err=online.Scan(&name,&at);err!=nil{break};state.LastOnline[name]=at}
 if e:=online.Err();err==nil{err=e}
 online.Close();if err!=nil{return state,err}
 ips,err:=db.Query("SELECT email,ip,last_seen FROM online_ips")
 if err!=nil{return state,err}
 for ips.Next(){var name,ip string;var at int64;if err=ips.Scan(&name,&ip,&at);err!=nil{break};state.OnlineIPs[name]=append(state.OnlineIPs[name],onlineClientIP{IP:ip,LastSeen:at})}
 if e:=ips.Err();err==nil{err=e}
 ips.Close();if err!=nil{return state,err}
 current,err:=db.Query("SELECT email,ip,last_seen FROM live_online")
 if err!=nil{return state,err}
 for current.Next(){var name,ip string;var at int64;if err=current.Scan(&name,&ip,&at);err!=nil{break};state.LiveIPs[name]=append(state.LiveIPs[name],onlineClientIP{IP:ip,LastSeen:at})}
 if e:=current.Err();err==nil{err=e}
 current.Close();if err!=nil{return state,err}
 _=db.QueryRow("SELECT value FROM meta WHERE key='online_poll_at'").Scan(&state.OnlinePollAt)
 return state,nil
}
func saveTrafficStore(configPath string,state trafficStore) error {
 ensureTrafficMaps(&state)
 db,err:=openTrafficDB(configPath);if err!=nil{return err}
 defer db.Close()
 tx,err:=db.Begin();if err!=nil{return err}
 for _,query:=range []string{"DELETE FROM counters","DELETE FROM last_online","DELETE FROM online_ips","DELETE FROM live_online"} {
  if _,err=tx.Exec(query);err!=nil{tx.Rollback();return err}
 }
 if err=writeTrafficTx(tx,state,false);err!=nil{tx.Rollback();return err}
 if err=tx.Commit();err!=nil{return err}
 // Mirror is not the source of truth. If writing it fails, the committed DB
 // remains intact and the next successful snapshot regenerates the mirror.
 b,err:=json.MarshalIndent(state,"","  ");if err!=nil{return err}
 return writeAtomically(trafficStorePath(configPath),append(b,'\n'),0600)
}
func pruneObservedIPs(state *trafficStore, now time.Time) bool {
 cutoff:=now.Add(-onlineHistoryRetention).Unix()
 changed:=false
 for email,items:=range state.OnlineIPs {
  kept:=items[:0]
  for _,ip:=range items{if ip.LastSeen>=cutoff{kept=append(kept,ip)}else{changed=true}}
  if len(kept)==0 {delete(state.OnlineIPs,email)}else{state.OnlineIPs[email]=kept}
 }
 return changed
}
