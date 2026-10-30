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
| 添加/删除用户、改名、UUID 轮换 | 支持 | 支持 |
| 指定节点分配用户 | 目前添加用户默认同步各节点 | 支持 |
| 节点列表、二维码、用户链接 | 支持 | 支持 |
| 流量统计/清零 | 支持 | 支持 |
| Xray 状态/重启 | 支持 | 支持 |
| 更新 freedom.sh、Bot、Xray-core | 支持 | 支持（需授权） |
| 回滚上一版 Xray-core | 手动 | 支持（需授权） |
| 用户配额、有效期、Web 面板、公开订阅服务 | 暂不实现 | 暂不实现 |

不实现 3x-ui 的所有功能是有意的：个人、小规模 VPS 管理优先减少攻击面和维护复杂性。

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

- allow_mutations：Telegram 添加、删除、改名用户，轮换 UUID、修改节点分配；二次确认。
- allow_updates：Telegram 升级 freedom.sh 和 freedom-bot；二次确认。
- allow_core_updates：独立授权 Xray-core 升级与回滚；还需 allow_updates。
- allow_sensitive_backup：允许把包含 REALITY/VLESS 私钥的完整 JSON 发往 Telegram；建议保持关闭。

在 VPS 上首次启用更新权限后，正常情况下后续可直接在 Telegram 中发送 /update，不必为每一次更新登录 VPS。但如果 Bot 自更新失败、服务器失联或管理员凭据丢失，仍必须通过 SSH/Tailscale 恢复。

### 常用指令

```text
/start                      打开按钮主菜单
/users                      用户管理（含新增用户引导）
/adduser phone              全部节点新增用户
/adduser phone cdn          只添加到节点标签 cdn
/renameuser old new         改名
/rotateuser phone           轮换 UUID
/removeuser phone           删除用户
/assignuser phone cdn       给用户分配节点
/unassignuser phone cdn     从节点移除用户
/nodes                      节点列表
/traffic                    流量
/status                     系统状态
/update                     更新检查、确认、回滚菜单
/restart                    重启 Xray（确认）
/cancel                     取消文本输入流程
```

也可以直接在用户列表点击「新增用户」，按提示输入名称、选择节点、二次确认。

### 远程更新

Bot 可以分别更新：

1. freedom.sh：读取 GitHub main 的提交 SHA，固定到该提交下载脚本，校验 Bash 语法，保留 previous 备份。
2. freedom-bot：下载本仓库 bot-latest Release 的架构匹配程序和 SHA-256 文件，验证后替换，由 systemd 重启。
3. Xray-core：下载 XTLS/Xray-core 官方最新稳定版 Release，验证 .dgst 中的 SHA-256，用新二进制测试现有配置，替换后重启；启动检查失败时尝试回滚。Telegram 另有「回滚上一版核心」。
   
注意：Xray 能启动不等于新版本能与 Shadowrocket、v2rayN、v2rayNG 完成 REALITY/ML-KEM 握手。升级核心前保留备用节点及 Tailscale/SSH 管理通道。更新不会自动修改 VLESS 密钥、REALITY 目标、路由资产或 TLS 证书。

### Telegram 权限风险

Bot 当前需要较高本地权限来管理 systemd 与安装核心。Bot Token、Telegram 管理员会话与 GitHub 发布源都属于高价值安全边界。不要向其他 Telegram 用户开放管理权限；建议启用 Telegram 两步验证，仅在需要时开启核心远程更新。

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
- Bot 用户修改在重启前验证配置，并在启动失败时尝试恢复旧配置；异常时需检查 systemd 和本地备份。
- 内核升级/重启会影响现有连接：不承诺零停机。
- 系统网络调优是手动高级功能，不应无基准测试就认为 BBR、TCP Fast Open、缓冲区增大等一定提高吞吐。
- GitHub Actions 验证 Shell 语法、Go 单元测试并编译 amd64/arm64 Bot；真实 CDN 和客户端连接仍须实际验证。

## 1stream 路由资产

1stream.sh 可生成 1stream.dat geosite/domain 规则文件。该文件不包含 GeoIP 数据，与代理软件更新、用户管理相互独立。
