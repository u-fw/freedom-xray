package main

import (
 "context"
 "os"
 "path/filepath"
 "strings"
 "testing"
 "time"
)

func TestREALITYOnlineAPIParsesSourceIP(t *testing.T){
 bin:=filepath.Join(t.TempDir(),"xray")
 mock:=`#!/bin/sh
printf '%s\n' '{"users":[{"email":"phone","ips":[{"ip":"203.0.113.7","lastSeen":"1800000000"},{"ip":"not-an-ip","lastSeen":"1800000000"}]},{"email":"alice","ips":[{"ip":"2001:db8::5","lastSeen":"1800000000"}]}]}'
`
 if err:=os.WriteFile(bin,[]byte(mock),0700);err!=nil{t.Fatal(err)}
 ctx,cancel:=context.WithTimeout(context.Background(),5*time.Second);defer cancel()
 users,err:=queryOnlineClients(ctx,bin,"127.0.0.1:10085")
 if err!=nil{t.Fatal(err)}
 if len(users)!=2{t.Fatalf("unexpected users: %+v",users)}
 if len(users[0].IPs)!=1||users[0].IPs[0].IP!="203.0.113.7"{t.Fatalf("REALITY IPv4 source missing: %+v",users)}
 if len(users[1].IPs)!=1||users[1].IPs[0].IP!="2001:db8::5"{t.Fatalf("IPv6 source missing: %+v",users)}
}

func TestOnlineCardShowsSourceAndCumulativeUserTraffic(t *testing.T){
 now:=time.Now()
 state:=trafficStore{
  Users:map[string]trafficCounter{"phone":{TotalUp:1048576,TotalDown:2097152}},
  LiveIPs:map[string][]onlineClientIP{"phone":{{IP:"203.0.113.7",LastSeen:now.Unix()}}},
  OnlineIPs:map[string][]onlineClientIP{"phone":{{IP:"203.0.113.7",LastSeen:now.Unix()}}},
  OnlinePollAt:now.Unix(),
 }
 body:=onlineCardText(state,now,"en")
 for _,wanted:=range []string{"phone","203.0.113.7","Online","Total:","per user"}{
  if !strings.Contains(body,wanted){t.Fatalf("missing %q in online view: %s",wanted,body)}
 }
 state.LiveIPs=map[string][]onlineClientIP{}
 body=onlineCardText(state,now,"en")
 if !strings.Contains(body,"Recently observed (not online)"){t.Fatalf("recent IP was lost or mislabeled: %s",body)}
}

func TestOnlineCardKeepsUserWithoutObservedIP(t *testing.T){
 state:=trafficStore{Users:map[string]trafficCounter{"reality-user":{TotalDown:1024}},OnlinePollAt:time.Now().Unix()}
 message:=onlineCardText(state,time.Now(),"en")
 if !strings.Contains(message,"reality-user")||!strings.Contains(message,"Source IP not observed")||!strings.Contains(message,"Total:"){
  t.Fatalf("user with traffic must remain visible without a captured IP: %s",message)
 }
}
