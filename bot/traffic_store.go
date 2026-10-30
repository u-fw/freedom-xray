package main

import (
 "encoding/json"
 "errors"
 "os"
 "path/filepath"
)

// The terminal menu and Telegram bot share freedom-stats.json.
// Updates are atomic and owner-only; a missing store is not an error.
func editTrafficStore(configPath string, edit func(map[string]any) error) error {
 file:=filepath.Join(filepath.Dir(configPath),"freedom-stats.json")
 data,err:=os.ReadFile(file)
 if os.IsNotExist(err) {return nil}
 if err!=nil{return err}
 var root map[string]any
 if err=json.Unmarshal(data,&root);err!=nil{return err}
 if err=edit(root);err!=nil{return err}
 updated,err:=json.MarshalIndent(root,"","  ");if err!=nil{return err}
 return writeAtomically(file,append(updated,'\n'),0600)
}

func renameTrafficStore(configPath,oldName,newName string) error {
 return editTrafficStore(configPath,func(root map[string]any) error {
  users,ok:=getMap(root["users"]);if !ok{return errors.New("用户统计格式异常")}
  value,ok:=users[oldName];if !ok{return nil}
  stats,ok:=getMap(value);if !ok{return errors.New("用户流量记录格式异常")}
  if _,exists:=users[newName];exists{return errors.New("新名称已经有流量统计数据")}
  // Xray restart starts a new epoch of user counters.
  stats["last_up"]=0
  stats["last_down"]=0
  users[newName]=stats
  delete(users,oldName)
  return nil
 })
}

func resetTrafficStore(configPath string) error {
 return editTrafficStore(configPath,func(root map[string]any) error {
  users,ok:=getMap(root["users"]);if !ok{return errors.New("用户统计格式异常")}
  for name:=range users {
   users[name]=map[string]any{"total_up":0,"total_down":0,"last_up":0,"last_down":0}
  }
  return nil
 })
}
