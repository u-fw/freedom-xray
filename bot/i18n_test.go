package main

import (
 "os"
 "path/filepath"
 "strings"
 "testing"
 "unicode"
)

func TestEnglishDefaultAndLanguageNormalization(t *testing.T) {
 if DefaultConfig().Language!="en"{t.Fatal("English must be default")}
 for input,expected:=range map[string]string{"en":"en","en-US":"en","EN":"en","zh":"zh-CN","zh-CN":"zh-CN","fr":"en"}{
  if actual:=normalizeLanguage(input);actual!=expected{t.Errorf("%q -> %q, want %q",input,actual,expected)}
 }
}
func TestI18nKeyCoverage(t *testing.T){
 en:=languageBundles["en"]
 zh:=languageBundles["zh-CN"]
 if len(en)==0 || len(zh)!=len(en){t.Fatalf("missing language entries")}
 for key,msg:=range en {
  if strings.TrimSpace(msg)=="" {t.Errorf("empty English translation %q",key)}
  for _,r:=range msg {if unicode.Is(unicode.Han,r){t.Errorf("non-English default translation: %q",key);break}}
  if zh[key]=="" {t.Errorf("missing Chinese translation %q",key)}
 }
 if got:=tr("fr","menu.users");got!=tr("en","menu.users"){t.Fatalf("unsupported locale did not fall back to English")}
}
func TestLanguagePersistence(t *testing.T){
 path:=filepath.Join(t.TempDir(),"bot.json")
 c:=DefaultConfig()
 c.Language="zh-CN"
 if err:=c.Save(path);err!=nil{t.Fatal(err)}
 got,err:=LoadConfig(path)
 if err!=nil{t.Fatal(err)}
 if got.Language!="zh-CN"||got.ConfigFile!=path{t.Fatalf("language not retained: %+v",got)}
 raw,err:=os.ReadFile(path);if err!=nil{t.Fatal(err)}
 if strings.Contains(string(raw),"ConfigFile"){t.Fatal("private config path serialized")}
}

func TestLanguageUpdatePreservesUnknownFields(t *testing.T) {
 path:=filepath.Join(t.TempDir(),"bot.json")
 original:=[]byte(`{"token":"example","admin_ids":[1234],"custom_extension":{"feature":true},"language":"en"}`)
 if err:=os.WriteFile(path,original,0600);err!=nil{t.Fatal(err)}
 if err:=persistLanguage(path,"zh-CN");err!=nil{t.Fatal(err)}
 data,err:=os.ReadFile(path);if err!=nil{t.Fatal(err)}
 if !strings.Contains(string(data),`"feature": true`) || !strings.Contains(string(data),`"language": "zh-CN"`){t.Fatalf("language update discarded fields: %s",data)}
}
