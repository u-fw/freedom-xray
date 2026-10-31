# freedom-xray

中文说明 | Xray-core 裸核管理工具：freedom.sh（终端）+ freedom-bot（Telegram）。

使用官方 Xray-core 与 systemd，不安装独立的 Web 面板或数据库。本项目用于自用节点维护，不保证协议不可识别或一定避免网络封锁。

## 首次安装

建议下载脚本、进行语法检查并审阅后运行，而不是直接执行不明镜像提供的 root 脚本。

```bash
curl -fsSLo freedom.sh https://raw.githubusercontent.com/u-fw/freedom-xray/main/freedom.sh
bash -n freedom.sh
less freedom.sh
sudo bash ./freedom.sh
```

安装后使用 sudo freedom 进入终端菜单。在「Telegram 机器人」中首次配置 BotFather Token、自己的 Telegram User ID，以及远程权限。

## 功能及范围

| 功能 | 终端 | Telegram Bot |
| --- | --- | --- |
| VLESS RAW/REALITY/Vision、XHTTP/REALITY、XHTTP/TLS/CDN | 支持 | 节点查看/导出 |
| 独立创建/删除用户、改名、UUID 轮换 | 支持（共用 Go 管理程序） | 支持 |
| 暂停/恢复用户（保留原 UUID） | 通过已安装的 Bot 管理程序支持 | 支持，私聊确认 |
| 指定节点分配/移除用户 | 支持，默认不绑定 | 支持，默认不绑定 |
| 节点列表、二维码、用户链接 | 支持 | 支持 |
| 修改 XHTTP Path/Mode | 支持 | 支持，仅允许修改这两项并二次确认 |
| 流量统计/清零 | 支持 | 支持 |
| 入站累计流量 | 核心 StatsService 可查 | Bot 展示当前 Xray 运行周期的各入站总量 |
| Xray 状态/重启 | 支持 | 支持 |
| 更新 freedom.sh、Bot、Xray-core | 支持 | 支持（需授权） |
| 回滚上一版 Xray-core | 手动 | 支持（需授权） |
| 用户配额、有效期、Web 面板、公开订阅服务 | 暂不实现 | 暂不实现 |

不实现 3x-ui 的所有功能是有意的：个人、小规模 VPS 管理优先减少攻击面和维护复杂性。

## 管理脚本与 Bot 一次更新

新版 `freedom.sh` 的主菜单选择「8. 在线更新」→「1. 更新管理脚本并同步已安装 Bot」即可执行组合升级：先从 GitHub 官方 `main` 下载并检查 Bash 语法，通过文件内容比对决定是否替换 `freedom.sh`；若检测到服务器已经安装并配置 Bot，会直接询问是否同步安装最新 `bot-latest` 二进制、验证 SHA-256、重启 Bot 并在启动失败时尝试回滚。

首次使用新版脚本时，若服务器已有 Bot 且本版脚本尚未检查过，会直接在启动菜单前提供一次 Bot 同步提示；不需要专门进入「Telegram 机器人」子菜单。拒绝本次更新后，同一份脚本不会每次启动都重复询问。

如果只想升级 Bot，使用「8. 在线更新」→「5. 仅更新 Telegram Bot」。**管理组件升级不会自动升级 Xray-core**，核心仍在同一个更新中心以独立菜单操作。

注意：旧版已经安装在 VPS 上的 `freedom.sh` 可能依然使用固定版本字符串判断更新，不能可靠发现新版；因此从旧版过渡时仍建议先下载并安装一次最新脚本，再使用以上一体化菜单。

## 为什么 GitHub 更新了，Telegram 还是旧菜单？

GitHub 源码、`bot-latest` 二进制 Release 和 VPS 当前运行的 systemd 进程是三个不同状态。更改仓库不会自动替换 VPS 上正在运行的旧 Bot。旧版本不认识 `/update`，第一次必须在服务器上手动升级。

```bash
# 仅在 VPS 执行：下载最新管理脚本，先做语法检查和必要审阅
curl --proto "=https" -fsSLo /tmp/freedom-latest.sh https://raw.githubusercontent.com/u-fw/freedom-xray/main/freedom.sh
bash -n /tmp/freedom-latest.sh
less /tmp/freedom-latest.sh
sudo install -m 755 /tmp/freedom-latest.sh /usr/local/bin/freedom
sudo freedom
```

在主菜单选择「9. Telegram 机器人」→「2. 安装 / 升级 freedom-bot」，等待下载校验和自动重启完成；再进入「1. 配置机器人」启用 `allow_updates`。**不要为了更新 Bot 而选择升级 Xray-core**。

若仍显示旧菜单，请分别检查磁盘版本与 systemd 进程：

```bash
sudo /usr/local/bin/freedom-bot -v
sudo systemctl status freedom-bot --no-pager
sudo systemctl show freedom-bot -p ExecStart -p MainPID --no-pager
sudo journalctl -u freedom-bot -n 50 --no-pager
```

更新成功的 Bot 在 Telegram 发送 `/start` 应显示服务器主机名和 Bot Git 提交版本，还能识别 `/version`、`/update`，并向管理员私人聊天注册 Telegram 命令菜单。

每台 VPS 应优先使用**不同的 Telegram Bot Token**。同一个 Bot Token 被多台服务器的 `getUpdates` 进程同时使用，会发生冲突，可能造成某台旧程序接管回复。共享 Token 的多 VPS 控制需要独立的中心控制 Bot，而不是多个轮询实例。

Bot Release 构建现在先运行 Go 测试；发布前检查最新源码树，并限制并发构建。正常升级仍需要核实服务器运行的版本，不能仅凭 GitHub Actions 绿色判断服务器已部署。

## Telegram Bot

Bot 只响应管理员 User ID 在私人聊天中的操作。默认关闭远程配置修改、远程软件更新以及包含完整私钥的备份发送。

在终端配置的 bot.json 中有以下权限字段：

```json
{
  "allow_mutations": false,
  "allow_updates": false,
  "allow_core_updates": false,
  "allow_sensitive_backup": false
}
```

- allow_mutations：Telegram 添加、删除、改名、停用/恢复用户，轮换 UUID、修改节点分配，以及 XHTTP Path/Mode；二次确认。
- allow_updates：Telegram 升级 freedom.sh 和 freedom-bot；二次确认。
- allow_core_updates：独立授权 Xray-core 升级与回滚；还需 allow_updates。
- allow_sensitive_backup：允许把包含 REALITY/VLESS 私钥的完整 JSON 发往 Telegram；建议保持关闭。

在 VPS 上首次启用更新权限后，正常情况下后续可直接在 Telegram 中发送 /update，不必为每一次更新登录 VPS。但如果 Bot 自更新失败、服务器失联或管理员凭据丢失，仍必须通过 SSH/Tailscale 恢复。

### 常用指令

```text
/start                      打开按钮主菜单
/users                      用户管理（含新增用户引导）
/adduser phone              创建用户但不自动绑定节点
/adduser phone cdn          创建用户并绑定节点标签 cdn
/renameuser old new         改名
/rotateuser phone           轮换 UUID
/removeuser phone           删除用户
/disableuser phone          停用用户，保留原节点与 UUID
/enableuser phone           恢复停用用户
/assignuser phone cdn       给用户分配节点
/unassignuser phone cdn     从节点移除用户
/nodes                      节点列表
/traffic                    用户流量（当前核心运行周期）
/inboundtraffic             按入站汇总流量
/status                     系统状态
/update                     更新检查、确认、回滚菜单
/restart                    重启 Xray（确认）
/cancel                     取消文本输入流程
```

也可以直接在用户列表点击「新增用户」，按提示输入名称，选择「仅创建」或某个节点，再二次确认。已停用的用户可在用户列表中的「停用用户」恢复。

停用时会把用户从活跃的 Xray 入站配置移除，并将原始 UUID、各入站客户端配置保存在 VPS 本地的 `freedom-disabled-users.json`（文件权限 0600，**不是加密文件**）。为防止管理员意外失联，不能停用某入站的最后一个用户。若删除原入站，恢复会拒绝不完整的操作。终端用户菜单也提供停用/恢复入口，但需要先安装和配置 `freedom-bot` 二进制程序。

Telegram 节点详情允许修改现有 XHTTP 入站的 Path 与 Mode，不直接操作 REALITY 私钥、证书、端口、防火墙或其他任意 JSON 字段。**更改 Path/Mode 会使已导入的客户端需要同步配置**；请优先在备用节点测试。

### 远程更新

Bot 可以分别更新：

1. freedom.sh：读取 GitHub main 的提交 SHA，固定到该提交下载脚本，校验 Bash 语法，保留 previous 备份。
2. freedom-bot：下载本仓库 bot-latest Release 的架构匹配程序和 SHA-256 文件，验证后替换，由 systemd 重启。
3. Xray-core：下载 XTLS/Xray-core 官方最新稳定版 Release，验证 .dgst 中的 SHA-256，用新二进制测试现有配置，替换后重启；启动检查失败时尝试回滚。Telegram 另有「回滚上一版核心」。
   
注意：Xray 能启动不等于新版本能与 Shadowrocket、v2rayN、v2rayNG 完成 REALITY/ML-KEM 握手。升级核心前保留备用节点及 Tailscale/SSH 管理通道。更新不会自动修改 VLESS 密钥、REALITY 目标、路由资产或 TLS 证书。

### Telegram 权限风险

Bot 当前需要较高本地权限来管理 systemd 与安装核心。Bot Token、Telegram 管理员会话与 GitHub 发布源都属于高价值安全边界。不要向其他 Telegram 用户开放管理权限；建议启用 Telegram 两步验证，仅在需要时开启核心远程更新。

## 独立用户与节点绑定（新版）

管理模型已改为用户与入站解耦：用户的名称/UUID 由 `freedom-users.json` 保存（0600），节点配置保留在 Xray `config.json`。用户与节点通过显式的 `assign`/`unassign` 绑定。新节点默认 `clients: []`，**不再自动绑定 admin，也不会将已有用户复制到新节点**。新用户默认仅创建身份；需要在用户详情绑定一个或多个节点才能获得连接权限。

首次读取用户目录时，会自动从现有 Xray 入站扫描原有客户端名称/UUID，保留现有 admin 和连接关系。如果同名用户在不同入站使用了冲突 UUID，则拒绝自动合并，以免误改凭证；需先人工修复冲突。升级**不会删除或轮换任何已有 admin UUID**。

终端创建/删除用户、改名、轮换 UUID、绑定/解绑节点与 Telegram 使用同一 Go 管理引擎和文件锁。独立用户功能要求先安装 `freedom-bot` 二进制；只用终端时不需要启用或配置 Telegram Token。创建的用户即使被解绑所有节点，身份仍保留。

### 导出链路与二维码容量

Bot 的用户导出操作逐个处理已绑定节点。先发送不含 Markdown 解释的原始 `vless://` URI，再尝试发二维码；如果文本超过 Telegram 消息限制，改为发送完整 `.txt` 文件；二维码超过容量或上传失败将明确说明并继续处理其他节点。对于未绑定节点的用户，Bot 会提示先授权，而不是导出空 UUID 或静默跳过。

### Vision 支持矩阵

| 传输安全/加密 | 使用 xtls-rprx-vision |
| --- | --- |
| RAW/TCP + TLS 或 REALITY | 可以（正常 Splice 适用条件下） |
| XHTTP + TLS/REALITY 且 VLESS Encryption=none | 不应启用 |
| XHTTP + TLS/REALITY 且 VLESS Encryption 已启用 | 可以，用于穿透 Encryption；不意味着支持底层 TCP Splice |

Flow 必须与用户在对应入站的配置一致。CLI 与 Bot 导出器均不再自动给空 `flow` 补 Vision；新用户绑定时根据入站传输与 Encryption 配置选择合法 Flow，避免跨节点复制首个用户的 Flow。

## v2rayN/v2rayNG 导入

- 客户端 encryption 和服务端 decryption 必须成对；只有用户实际启用 Vision 才导出 flow。
- XHTTP Host 默认留空，不强制复制 TLS SNI；必要时可以针对反代自定义。
- XHTTP Padding 通常使用 100–1000 默认值；新版分享链接可携带 extra JSON，但客户端支持情况仍应实测。
- 不默认附加 ECH。仅在 DNS/CDN/客户端支持时使用。
- CDN 方案推荐有效源站证书 + Cloudflare Full (strict)，不要把 Full + 自签名证书误认为同等级验证。
- 不保证 XHTTP 比 RAW 延迟更低，也不保证迁移裸核能解除被封锁 IP。

## 安全和性能

- StatsService 保持 127.0.0.1 监听；管理建议优先通过 Tailscale。
- 更新包经 HTTPS 下载，Bot 与 Xray-core 发布包执行 SHA-256 校验；脚本执行 Bash 语法校验。
- Bot 用户修改及 XHTTP Path/Mode 修改在重启前验证配置，并在启动失败时尝试恢复旧配置；异常时需检查 systemd 和本地备份。
- 统计按入站和按用户分别独立提供，**不能据此推算每个用户在每个入站的流量**。入站流量显示当前 Xray 运行周期的值，核心重启会重置；并非持久化配额统计。
- Bot 与终端写入 Xray 配置的操作目前尚未完全共享一个覆盖整个读-改-写事务的跨进程锁，避免两个界面同时修改用户或节点，尤其不要在 Telegram 操作时使用终端执行配置变更。
- 内核升级/重启会影响现有连接：不承诺零停机。
- 系统网络调优是手动高级功能，不应无基准测试就认为 BBR、TCP Fast Open、缓冲区增大等一定提高吞吐。
- GitHub Actions 验证 Shell 语法、Go 单元测试并编译 amd64/arm64 Bot；真实 CDN 和客户端连接仍须实际验证。

## 1stream 路由资产

1stream.sh 可生成 1stream.dat geosite/domain 规则文件。该文件不包含 GeoIP 数据，与代理软件更新、用户管理相互独立。
