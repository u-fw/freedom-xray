package main

import (
 "os"
 "path/filepath"
 "testing"
 "time"
)

func TestParseQuotaGB(t *testing.T) {
 for _,test:=range []struct{
  input string
  want int64
  clear bool
 }{
  {"100",100*gibBytes,false},
  {"0.5",gibBytes/2,false},
  {"0",0,true},
  {"-",0,true},
 }{
  bytes,clear,err:=parseQuotaGB(test.input)
  if err!=nil{t.Fatalf("rejected %q: %v",test.input,err)}
  if bytes!=test.want||clear!=test.clear{t.Fatalf("%q parsed as %d clear=%v",test.input,bytes,clear)}
 }
 for _,input:=range []string{"","abc","-1","NaN","Inf",".","1048577"}{
  if _,_,err:=parseQuotaGB(input);err==nil{t.Fatalf("accepted invalid quota %q",input)}
 }
 if bytes,_,err:=parseQuotaGB("1");err!=nil||bytes!=gibBytes{t.Fatalf("1 GB must be one GiB: %d %v",bytes,err)}
}

func TestParseExpiryInput(t *testing.T) {
 now:=time.Date(2026,1,1,12,0,0,0,time.UTC)
 stamp,clear,err:=parseExpiryInput("30d",now)
 if err!=nil||clear{t.Fatalf("30d rejected: %v clear=%v",err,clear)}
 if want:=now.AddDate(0,0,30).Unix();stamp!=want{t.Fatalf("30d = %d, want %d",stamp,want)}
 stamp,clear,err=parseExpiryInput("2026-02-01",now)
 if err!=nil||clear{t.Fatalf("date rejected: %v clear=%v",err,clear)}
 if want:=time.Date(2026,2,1,23,59,59,0,time.UTC).Unix();stamp!=want{t.Fatalf("date end = %d, want %d",stamp,want)}
 for _,input:=range []string{"0","-"}{if _,clear,err:=parseExpiryInput(input,now);err!=nil||!clear{t.Fatalf("%q must clear: %v",input,err)}}
 for _,input:=range []string{"","abc","2025-12-31","2026-13-01","-5d","36501d"}{if _,_,err:=parseExpiryInput(input,now);err==nil{t.Fatalf("accepted invalid expiry %q",input)}}
}

func TestEvaluateUserLimit(t *testing.T) {
 now:=time.Unix(1800000000,0)
 quota:=userLimit{QuotaBytes:1000}
 if action:=evaluateUserLimit(quota,799,now);action.Warn80||action.Disable{t.Fatalf("warned below threshold: %+v",action)}
 if action:=evaluateUserLimit(quota,800,now);!action.Warn80||action.Disable{t.Fatalf("80%% threshold wrong: %+v",action)}
 quota.Notified80=true
 if action:=evaluateUserLimit(quota,800,now);action.Warn80{t.Fatalf("80%% warning repeated: %+v",action)}
 if action:=evaluateUserLimit(quota,1000,now);!action.Disable||action.Reason!="quota"||!action.WarnQuota{t.Fatalf("quota not enforced: %+v",action)}
 quota.NotifiedQuota=true
 if action:=evaluateUserLimit(quota,1000,now);!action.Disable||action.WarnQuota{t.Fatalf("quota notification repeated: %+v",action)}

 expiry:=userLimit{ExpiryUnix:now.Unix()+3*24*3600-1}
 if action:=evaluateUserLimit(expiry,0,now);!action.WarnExpiry||action.Disable{t.Fatalf("3-day warning missing: %+v",action)}
 farther:=userLimit{ExpiryUnix:now.Unix()+3*24*3600+1}
 if action:=evaluateUserLimit(farther,0,now);action.WarnExpiry||action.Disable{t.Fatalf("premature expiry warning: %+v",action)}
 expired:=userLimit{ExpiryUnix:now.Unix()}
 action:=evaluateUserLimit(expired,0,now)
 if !action.Disable||action.Reason!="expired"||!action.WarnExpired{t.Fatalf("expiry not enforced: %+v",action)}
 expired.NotifiedExpired=true
 if action:=evaluateUserLimit(expired,0,now);!action.Disable||action.WarnExpired{t.Fatalf("expiry notification repeated: %+v",action)}
}

func TestUserLimitStoreRoundTrip(t *testing.T) {
 config:=filepath.Join(t.TempDir(),"config.json")
 email:="phone"
 err:=updateUserLimits(config,func(users map[string]userLimit)error{
  users[email]=userLimit{QuotaBytes:64*gibBytes,ExpiryUnix:1800000000}
  return nil
 })
 if err!=nil{t.Fatal(err)}
 limits,err:=snapshotUserLimits(config)
 if err!=nil{t.Fatal(err)}
 limit,ok:=limits[email]
 if !ok||limit.QuotaBytes!=64*gibBytes||limit.ExpiryUnix!=1800000000{t.Fatalf("limit not persisted: %+v",limits)}
 markUserLimit(config,email,func(l *userLimit){l.Notified80=true})
 limits,_=snapshotUserLimits(config)
 if !limits[email].Notified80{t.Fatal("notification flag not persisted")}
 if err:=updateUserLimits(config,func(users map[string]userLimit)error{delete(users,email);return nil});err!=nil{t.Fatal(err)}
 limits,_=snapshotUserLimits(config)
 if len(limits)!=0{t.Fatalf("limit not removed: %+v",limits)}
 if err:=os.WriteFile(userLimitStorePath(config),[]byte("{"),0600);err!=nil{t.Fatal(err)}
 if _,err:=snapshotUserLimits(config);err==nil{t.Fatal("accepted corrupted limit store")}
}
