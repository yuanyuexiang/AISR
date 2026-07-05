# AISR 守护进程部署(Windows 10 / 11)

把 AISR daemon 装成「登录即自动后台运行」的服务。**以当前登录用户身份跑**
(才能用到你 claude / cursor 的登录态),**不需要管理员权限**,不用装任何额外
工具(只用系统自带的任务计划程序)。

目标机型:Windows 10/11,x64(AMD/Intel)。ARM 机器把 `dist\aisr-windows-arm64.exe`
改名 `aisr.exe` 放进本文件夹即可。

## 包内容

| 文件 | 作用 |
|---|---|
| `aisr.exe` | AISR 二进制(Windows amd64) |
| `install-aisr.ps1` | 安装:注册登录自启 + 立即启动 |
| `uninstall-aisr.ps1` | 卸载:删任务 + 杀进程 + 删安装目录 |

## 前提(每台机器做一次)

装好并**登录**目标 CLI —— AISR 复用它们的登录态(零 API key):

```powershell
claude -p "hi"          # 能正常回话 = 已登录
cursor-agent status
```

> 命令名不是 `claude` / `cursor-agent`?装完后在
> `%LOCALAPPDATA%\AISR\run-aisr.ps1` 里设 `AISR_CLAUDE_BIN` / `AISR_CURSOR_BIN`
> 指到全路径。

## 安装

把**整个文件夹**拷到目标机器,在该文件夹里开 PowerShell:

```powershell
powershell -ExecutionPolicy Bypass -File .\install-aisr.ps1
```

装完会打印 **token** 和监听地址。默认监听 `0.0.0.0:7878`(局域网可达,靠 token
保护)。常用变体:

```powershell
# 只想本机可用(最安全):
powershell -ExecutionPolicy Bypass -File .\install-aisr.ps1 -Listen 127.0.0.1:7878

# 固定一个自己的 token:
powershell -ExecutionPolicy Bypass -File .\install-aisr.ps1 -Token 你的强随机值
```

## 验证

```powershell
curl -H "Authorization: Bearer <token>" http://127.0.0.1:7878/v1/providers
```

返回 `providers` 列表即成功。

## 客户端连接

```
AISR_BASE_URL = http://<这台机器的IP>:7878
AISR_TOKEN    = <token>
```

容器里调用 → `http://host.docker.internal:7878` + token(见
[../../docker/README.md](../../docker/README.md))。接口/事件模型见
[../../docs/接口使用文档.md](../../docs/接口使用文档.md)。

## 卸载

```powershell
powershell -ExecutionPolicy Bypass -File .\uninstall-aisr.ps1
```

## 说明 / 排错

- 守护进程随**登录**启动(不是开机就起 —— 它需要你的登录态)。锁屏不影响运行。
- 崩溃会自动重启(计划任务设了 `RestartCount`)。
- 想看日志排错:临时手动跑 `%LOCALAPPDATA%\AISR\run-aisr.ps1`,前台就能看到输出。
- 调用报 `PROVIDER_UNAVAILABLE` = 那台机器的 `claude` / `cursor-agent` 没登录、
  或不在 PATH(见「前提」)。
- 这是**本地个人 runtime**,不要包成对外多用户服务(订阅 ToS)。TCP 的 token
  是唯一防线,`0.0.0.0` 会暴露到局域网 —— 不需要就用 `127.0.0.1`。

---

维护者:这个包里的 `aisr.exe` 用 `make windows`(在仓库根)重编后从
`dist/aisr-windows-amd64.exe` 拷过来。
