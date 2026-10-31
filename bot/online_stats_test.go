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

// Older Xray-core releases implement statsonlineiplist for one email but do
// not support the aggregate -all flag. The Bot must keep collecting online IPs
// through the per-email compatibility path instead of logging an unsupported
// API on every sample.
func TestOnlinePerEmailFallbackForOlderCore(t *testing.T){
 bin:=filepath.Join(t.TempDir(),"xray")
 mock:=`#!/bin/sh
for arg in "$@"; do
  case "$arg" in
    -all) printf '%s\n' 'flag provided but not defined: -all' >&2; exit 2 ;;
    -email=*)
      email="${arg#-email=}"
      printf '{"name":"%s","ips":{"203.0.113.7":1800000000}}\n' "$email"
      exit 0 ;;
  esac
done
printf '%s\n' 'unexpected arguments' >&2
exit 3
`
 if err:=os.WriteFile(bin,[]byte(mock),0700);err!=nil{t.Fatal(err)}
 ctx,cancel:=context.WithTimeout(context.Background(),5*time.Second);defer cancel()
 users,err:=queryOnlineClientsForEmails(ctx,bin,"127.0.0.1:10085",[]string{"phone","alice"})
 if err!=nil{t.Fatal(err)}
 if len(users)!=2{t.Fatalf("per-email fallback lost users: %+v",users)}
 seen:=map[string]string{}
 for _,user:=range users {
  if len(user.IPs)!=1{continue}
  seen[user.Email]=user.IPs[0].IP
 }
 if seen["phone"]!="203.0.113.7"||seen["alice"]!="203.0.113.7"{
  t.Fatalf("per-email fallback addresses wrong: %+v",users)
 }
}

// Some core builds accept "-all" but return an empty list while the per-email
// query has data. The Bot probes once, then prefers per-email for a bounded
// period and backs off probing after an empty probe.
func TestOnlineEmptyAggregateFallsBackToPerEmail(t *testing.T){
 bin:=filepath.Join(t.TempDir(),"xray")
 mock:=`#!/bin/sh
for arg in "$@"; do
  case "$arg" in
    -all) printf '%s\n' '{"users":[]}'; exit 0 ;;
    -email=*)
      email="${arg#-email=}"
      printf '{"name":"%s","ips":{"203.0.113.9":1800000000}}\n' "$email"
      exit 0 ;;
  esac
done
exit 3
`
 if err:=os.WriteFile(bin,[]byte(mock),0700);err!=nil{t.Fatal(err)}
 ctx,cancel:=context.WithTimeout(context.Background(),5*time.Second);defer cancel()
 users,err:=queryOnlineClientsForEmails(ctx,bin,"127.0.0.1:10085",[]string{"phone"})
 if err!=nil{t.Fatal(err)}
 if len(users)!=1||len(users[0].IPs)!=1||users[0].IPs[0].IP!="203.0.113.9"{
  t.Fatalf("empty aggregate did not fall back: %+v",users)
 }
 usePerEmail,allowProbe:=onlineFallbackPlan(bin,1,time.Now())
 if !usePerEmail||allowProbe{t.Fatalf("fallback not cached: use=%v probe=%v",usePerEmail,allowProbe)}
 users,err=queryOnlineClientsForEmails(ctx,bin,"127.0.0.1:10085",[]string{"phone"})
 if err!=nil||len(users)!=1{t.Fatalf("cached per-email path failed: %+v %v",users,err)}
 usePerEmail,allowProbe=onlineFallbackPlan(bin,1,time.Now().Add(11*time.Minute))
 if usePerEmail||!allowProbe{t.Fatalf("fallback did not expire: use=%v probe=%v",usePerEmail,allowProbe)}
 recordOnlineFallback(bin,false,time.Now())
 usePerEmail,allowProbe=onlineFallbackPlan(bin,1,time.Now().Add(time.Minute))
 if usePerEmail||allowProbe{t.Fatalf("empty probe did not back off: use=%v probe=%v",usePerEmail,allowProbe)}
 if usePerEmail,allowProbe=onlineFallbackPlan(bin,40,time.Now());usePerEmail||allowProbe{
  t.Fatalf("oversized email list must not trigger a probe: use=%v probe=%v",usePerEmail,allowProbe)
 }
}

func TestOnlineEmailResponseShapes(t *testing.T){
 mapForm:=[]byte(`{"name":"phone","ips":{"203.0.113.7":1800000000}}`)
 user,err:=parseOnlineEmailResponse(mapForm,"")
 if err!=nil{t.Fatal(err)}
 if user.Email!="phone"||len(user.IPs)!=1||user.IPs[0].IP!="203.0.113.7"{t.Fatalf("map form not parsed: %+v",user)}
 arrayForm:=[]byte(`{"email":"alice","ips":[{"ip":"2001:db8::5","lastSeen":"1800000000"}]}`)
 user,err=parseOnlineEmailResponse(arrayForm,"")
 if err!=nil{t.Fatal(err)}
 if user.Email!="alice"||len(user.IPs)!=1||user.IPs[0].IP!="2001:db8::5"{t.Fatalf("array form not parsed: %+v",user)}
}
