package main

import (
 "archive/zip"
 "bytes"
 "context"
 "crypto/sha256"
 "encoding/hex"
 "encoding/json"
 "errors"
 "fmt"
 "io"
 "net/http"
 "os"
 "os/exec"
 "path/filepath"
 "regexp"
 "runtime"
 "strings"
 "time"
)

const (
 scriptRepo = "u-fw/freedom-xray"
 coreRepo = "XTLS/Xray-core"
 maxUpdateBytes = 80 << 20
)

var updateHTTPClient = &http.Client{Timeout: 90*time.Second}
var githubSHA = regexp.MustCompile("^[a-f0-9]{40}$")
var safeTag = regexp.MustCompile("^v[0-9]+[.][0-9]+([.][0-9]+)?([a-zA-Z0-9.-]+)?$")

type githubRelease struct {
 TagName string `json:"tag_name"`
}
type githubCommit struct {
 SHA string `json:"sha"`
}

func downloadVerifiedHTTPS(ctx context.Context,url string,limit int64) ([]byte,error) {
 if !strings.HasPrefix(url,"https://"){return nil,errors.New("拒绝非 HTTPS 下载")}
 req,err:=http.NewRequestWithContext(ctx,http.MethodGet,url,nil);if err!=nil{return nil,err}
 resp,err:=updateHTTPClient.Do(req);if err!=nil{return nil,err}
 defer resp.Body.Close()
 if resp.StatusCode!=http.StatusOK{return nil,fmt.Errorf("下载失败 HTTP %d",resp.StatusCode)}
 data,err:=io.ReadAll(io.LimitReader(resp.Body,limit+1));if err!=nil{return nil,err}
 if int64(len(data))>limit{return nil,errors.New("下载内容超出大小限制")}
 return data,nil
}

func apiJSON(ctx context.Context,url string,dst any) error {
 data,err:=downloadVerifiedHTTPS(ctx,url,4<<20);if err!=nil{return err}
 return json.Unmarshal(data,dst)
}
func remoteComponentVersion(ctx context.Context,component string) (string,error) {
 switch component {
 case "script":
  var commit githubCommit
  err:=apiJSON(ctx,"https://api.github.com/repos/"+scriptRepo+"/commits/main",&commit)
  if err!=nil{return "",err}
  if !githubSHA.MatchString(commit.SHA){return "",errors.New("上游提交 SHA 格式无效")}
  return commit.SHA,nil
 case "bot":
  var release githubRelease
  err:=apiJSON(ctx,"https://api.github.com/repos/"+scriptRepo+"/releases/tags/bot-latest",&release)
  if err!=nil{return "",err}
  if release.TagName!="bot-latest"{return "",errors.New("Bot Release 名称异常")}
  return release.TagName,nil
 case "core":
  var release githubRelease
  err:=apiJSON(ctx,"https://api.github.com/repos/"+coreRepo+"/releases/latest",&release)
  if err!=nil{return "",err}
  if !safeTag.MatchString(release.TagName){return "",errors.New("Xray 稳定版标签异常")}
  return release.TagName,nil
 }
 return "",errors.New("未知更新组件")
}

func currentComponentVersion(component,xrayBinary string) string {
 switch component {
 case "script":
  b,err:=os.ReadFile("/usr/local/bin/freedom");if err!=nil{return "未安装"}
  for _,line:=range strings.Split(string(b),"\n") {
   if strings.HasPrefix(line,"SCRIPT_VERSION="){return strings.TrimSpace(line[len("SCRIPT_VERSION="):])}
  }
  return "未知"
 case "bot": return Version
 case "core":
  out,err:=exec.Command(xrayBinary,"version").Output();if err!=nil{return "未知"}
  fields:=strings.Fields(string(out));if len(fields)>1{return fields[1]}
 }
 return "未知"
}

func verifiedDigest(payload,checksum []byte,expectedName string) error {
 // GitHub release checksums: either sha256sum format or SHA256=... .dgst.
 var hash string
 for _,line:=range strings.Split(string(checksum),"\n") {
  l:=strings.TrimSpace(line)
  if len(l)==0 {continue}
  kv:=strings.SplitN(l,"=",2)
  if len(kv)==2 && strings.Contains(strings.ToUpper(kv[0]),"256") {
   token:=strings.Fields(strings.TrimSpace(kv[1]))
   if len(token)>0 {hash=token[0];break}
  }
  tokens:=strings.Fields(l)
  if len(tokens)>=2 && strings.TrimPrefix(tokens[1],"*")==expectedName && len(tokens[0])==64 {hash=tokens[0];break}
 }
 if len(hash)!=64{return errors.New("未找到可信的 SHA-256 校验值")}
 decoded,err:=hex.DecodeString(strings.ToLower(hash));if err!=nil{return err}
 sum:=sha256.Sum256(payload)
 if !bytes.Equal(sum[:],decoded){return errors.New("SHA-256 校验失败，拒绝安装")}
 return nil
}

func archNames() (botArch,coreArch string,err error) {
 if runtime.GOOS!="linux" {return "","",errors.New("更新仅支持 Linux")}
 switch runtime.GOARCH {
 case "amd64":return "amd64","64",nil
 case "arm64":return "arm64","arm64-v8a",nil
 }
 return "","",errors.New("暂不支持此架构的远程更新")
}

func checkedScriptUpdate(ctx context.Context,commit string) (string,error) {
 if !githubSHA.MatchString(commit){return "",errors.New("非法脚本提交 SHA")}
 url:="https://raw.githubusercontent.com/"+scriptRepo+"/"+commit+"/freedom.sh"
 script,err:=downloadVerifiedHTTPS(ctx,url,5<<20);if err!=nil{return "",err}
 if !bytes.Contains(script,[]byte("SCRIPT_VERSION=")) || !bytes.HasPrefix(script,[]byte("#!")) {return "",errors.New("不是有效的 freedom Shell 脚本")}
 target:="/usr/local/bin/freedom"
 old,err:=os.ReadFile(target);if err!=nil {return "",fmt.Errorf("先在机器上安装 freedom 脚本: %w",err)}
 tmp,err:=os.CreateTemp(filepath.Dir(target),".freedom-check-*");if err!=nil{return "",err}
 defer os.Remove(tmp.Name())
 if _,err=tmp.Write(script);err!=nil{tmp.Close();return "",err}
 if err=tmp.Close();err!=nil{return "",err}
 if err=runChecked("bash","-n",tmp.Name());err!=nil{return "",fmt.Errorf("Shell 语法检查失败: %w",err)}
 if bytes.Equal(old,script){return "脚本已是此版本",nil}
 backup:=target+".previous"
 if err=writeAtomically(backup,old,0700);err!=nil{return "",err}
 if err=writeAtomically(target,script,0755);err!=nil{return "",err}
 return "已更新 freedom 脚本（不会影响运行中的 Xray）；备份："+backup,nil
}

func checkedBotUpdate(ctx context.Context) (string,error) {
 arch,_,err:=archNames();if err!=nil{return "",err}
 name:="freedom-bot-linux-"+arch
 base:="https://github.com/"+scriptRepo+"/releases/download/bot-latest/"+name
 binary,err:=downloadVerifiedHTTPS(ctx,base,maxUpdateBytes);if err!=nil{return "",err}
 checks,err:=downloadVerifiedHTTPS(ctx,base+".sha256",8<<10);if err!=nil{return "",err}
 if err=verifiedDigest(binary,checks,name);err!=nil{return "",err}
 if len(binary)<4||!bytes.Equal(binary[:4],[]byte{0x7f,'E','L','F'}) {return "",errors.New("下载产物不是 ELF 程序")}
 target,err:=os.Executable();if err!=nil{return "",err}
 target,err=filepath.EvalSymlinks(target);if err!=nil{return "",err}
 old,err:=os.ReadFile(target);if err!=nil{return "",err}
 if bytes.Equal(binary,old) {return "Bot 已是当前发布版本",nil}
 tmp,err:=os.CreateTemp(filepath.Dir(target),".freedom-bot-check-*");if err!=nil{return "",err}
 defer os.Remove(tmp.Name())
 if err=tmp.Chmod(0755);err!=nil{tmp.Close();return "",err}
 if _,err=tmp.Write(binary);err!=nil{tmp.Close();return "",err}
 if err=tmp.Close();err!=nil{return "",err}
 if err=runChecked(tmp.Name(),"-v");err!=nil{return "",fmt.Errorf("新 Bot 程序自检失败: %w",err)}
 backup:=target+".previous"
 if err=writeAtomically(backup,old,0755);err!=nil{return "",err}
 if err=writeAtomically(target,binary,0755);err!=nil{return "",err}
 // Restart from a transient systemd timer instead of killing the process
 // handling the current callback.
 nameUnit:=fmt.Sprintf("freedom-bot-restart-%d",time.Now().Unix())
 if err=runChecked("systemd-run","--unit="+nameUnit,"--on-active=3s","/usr/bin/systemctl","restart","freedom-bot.service");err!=nil{
  _=writeAtomically(target,old,0755)
  return "",fmt.Errorf("无法注册 Bot 重启任务，已恢复旧文件: %w",err)
 }
 return "Bot 更新已安装并登记 systemd 重启；如果新进程启动失败，可用 VPS 上的 previous 备份回滚。",nil
}

func extractCoreExecutable(archive []byte) ([]byte,error) {
 zr,err:=zip.NewReader(bytes.NewReader(archive),int64(len(archive)));if err!=nil{return nil,err}
 for _,f:=range zr.File {
  if f.Name!="xray"{continue}
  if f.UncompressedSize64>maxUpdateBytes{return nil,errors.New("Xray 可执行文件过大")}
  rc,err:=f.Open();if err!=nil{return nil,err}
  out,err:=io.ReadAll(io.LimitReader(rc,maxUpdateBytes+1))
  rc.Close()
  if err!=nil{return nil,err}
  if len(out)<4 || !bytes.Equal(out[:4],[]byte{0x7f,'E','L','F'}){return nil,errors.New("不是有效的 Xray ELF")}
  return out,nil
 }
 return nil,errors.New("发行包中没有 xray 可执行文件")
}

func checkedCoreUpdate(ctx context.Context,target,configPath,tag string) (string,error) {
 _,arch,err:=archNames();if err!=nil{return "",err}
 if !safeTag.MatchString(tag){return "",errors.New("非法版本号")}
 name:="Xray-linux-"+arch+".zip"
 base:="https://github.com/"+coreRepo+"/releases/download/"+tag+"/"+name
 archive,err:=downloadVerifiedHTTPS(ctx,base,maxUpdateBytes);if err!=nil{return "",err}
 dgst,err:=downloadVerifiedHTTPS(ctx,base+".dgst",8<<10);if err!=nil{return "",err}
 if err=verifiedDigest(archive,dgst,name);err!=nil{return "",err}
 executable,err:=extractCoreExecutable(archive);if err!=nil{return "",err}
 previous,err:=os.ReadFile(target);if err!=nil{return "",err}
 tmp,err:=os.CreateTemp(filepath.Dir(target),".xray-check-*");if err!=nil{return "",err}
 defer os.Remove(tmp.Name())
 if err=tmp.Chmod(0755);err!=nil{tmp.Close();return "",err}
 if _,err=tmp.Write(executable);err!=nil{tmp.Close();return "",err}
 if err=tmp.Close();err!=nil{return "",err}
 if err=runChecked(tmp.Name(),"run","-test","-config",configPath);err!=nil{return "",fmt.Errorf("新内核不兼容现有配置，已取消升级: %w",err)}
 if bytes.Equal(previous,executable){return "Xray 已是该版本",nil}
 backup:=target+".previous"
 if err=writeAtomically(backup,previous,0755);err!=nil{return "",err}
 if err=writeAtomically(target,executable,0755);err!=nil{return "",err}
 if err=runChecked("systemctl","restart","xray");err==nil {err=runChecked("systemctl","is-active","--quiet","xray")}
 if err!=nil {
  _=writeAtomically(target,previous,0755)
  restoreErr:=runChecked("systemctl","restart","xray")
  return "",fmt.Errorf("新核心启动失败并已尝试回滚 (恢复服务: %v): %w",restoreErr,err)
 }
 return "已更新 Xray-core 至 "+tag+"；旧核心："+backup+"（请验证真实客户端握手）",nil
}


func checkedCoreRollback(target,configPath string) (string,error) {
 previousPath:=target+".previous"
 old,err:=os.ReadFile(previousPath);if err!=nil{return "",fmt.Errorf("没有可用的上一版核心备份: %w",err)}
 current,err:=os.ReadFile(target);if err!=nil{return "",err}
 tmp,err:=os.CreateTemp(filepath.Dir(target),".xray-rollback-*");if err!=nil{return "",err}
 defer os.Remove(tmp.Name())
 if err=tmp.Chmod(0755);err!=nil{tmp.Close();return "",err}
 if _,err=tmp.Write(old);err!=nil{tmp.Close();return "",err}
 if err=tmp.Close();err!=nil{return "",err}
 if err=runChecked(tmp.Name(),"run","-test","-config",configPath);err!=nil{return "",fmt.Errorf("上一版核心与现有配置不兼容: %w",err)}
 if err=writeAtomically(target,old,0755);err!=nil{return "",err}
 if err=runChecked("systemctl","restart","xray");err==nil{err=runChecked("systemctl","is-active","--quiet","xray")}
 if err!=nil {
  _=writeAtomically(target,current,0755)
  restoreErr:=runChecked("systemctl","restart","xray")
  return "",fmt.Errorf("旧版本回滚失败，已尝试恢复当前版本 (服务状态: %v): %w",restoreErr,err)
 }
 if err=writeAtomically(previousPath,current,0755);err!=nil{return "已回滚核心，但无法更新 previous 备份："+err.Error(),nil}
 return "已恢复上一版 Xray-core；如需返回新版本，可再次执行回滚。",nil
}
