# 在 Windows 上运行 AISR

代码已交叉编译验证(windows/amd64 + arm64)。下面是原生 Windows 的注意事项与步骤。

> **WSL2**:如果在 WSL2(Linux)里跑,一切与 Linux 相同(unix socket、`make` 等都可用),
> 按主 README 的用法即可,本文主要针对**原生 Windows**(PowerShell)。

## 关键差异(原生 Windows)

| 项 | 说明 / 处理 |
|---|---|
| **provider 二进制名** | Windows 上可能是 `claude.cmd` / `cursor-agent.cmd` 或全路径。用环境变量覆盖:`AISR_CLAUDE_BIN`、`AISR_CURSOR_BIN`。 |
| **Unix socket** | Win10+ 的 Go 端能监听,但 **Python 的 `socket.AF_UNIX` 原生 Windows 通常没有**,Python 客户端走 socket 会直接报错。→ 跨语言 / 容器**一律用 TCP**。 |
| **可执行文件** | 产物是 `aisr.exe`。 |
| **没有 `make`** | 直接用 `go build`(或装 make:scoop/choco)。 |
| **信号** | `Ctrl+C` 可优雅退出;`taskkill` 不触发优雅退出(socket 残留会在下次启动时被清理)。 |
| **文件权限** | `chmod 0600` 在 Windows 基本是空操作,socket/token 的"文件权限"防护弱于 Unix;TCP 用 token 保护即可。 |

## 构建

```powershell
go build -o bin\aisr.exe ./cmd/aisr
```

## 前提:CLI 已装好并登录

```powershell
claude -p "hi"            # 或你的实际命令名
cursor-agent status
```
若命令名不是 `claude` / `cursor-agent`,设置覆盖(本会话内有效):
```powershell
$env:AISR_CLAUDE_BIN = "claude.cmd"
$env:AISR_CURSOR_BIN = "cursor-agent.cmd"
```

## 启动 daemon(推荐 TCP)

PowerShell 不支持 `VAR=值 命令` 的内联写法,先设再跑:

```powershell
$env:AISR_TOKEN = "123"                       # 自己定一个值
.\bin\aisr.exe serve --listen 0.0.0.0:7878
```

> 想用默认 unix socket(`.\bin\aisr.exe serve`)也可以在 Win10+ 上跑,但**仅 Go SDK /
> CLI 能连**;Python 客户端连不了 unix socket,所以建议直接 TCP。

## 命令行用法

```powershell
.\bin\aisr.exe ask --provider cursor "用一句话回答:1+1等于几"
.\bin\aisr.exe providers
.\bin\aisr.exe session create dev --workspace .
.\bin\aisr.exe ask --session dev "继续上文"
```

## 客户端调用(TCP)

```powershell
# curl(Windows 10+ 自带 curl.exe)
curl -H "Authorization: Bearer 123" http://127.0.0.1:7878/v1/providers

# Python(用 base_url / 环境变量,不要走 unix socket)
$env:AISR_BASE_URL = "http://127.0.0.1:7878"
$env:AISR_TOKEN = "123"
python clients\python\example.py "你好"
```

Go SDK 同理:`sdk.New()` 读 `AISR_BASE_URL` / `AISR_TOKEN`,或显式
`sdk.New(sdk.WithBaseURL("http://127.0.0.1:7878"), sdk.WithToken("123"))`。

## Docker(Docker Desktop for Windows)

与 macOS 相同:调用方在容器、daemon 在宿主机(Windows),容器经
`host.docker.internal` 走 TCP。`host.docker.internal` 在 Docker Desktop 默认可用。

```powershell
# 终端 1(宿主机)
$env:AISR_TOKEN = "123"
.\bin\aisr.exe serve --listen 0.0.0.0:7878

# 终端 2(容器,同一个 token)
$env:AISR_TOKEN = "123"
docker compose -f docker/docker-compose.yml run --rm caller "用一句话回答:1+1等于几"
```

## 排错

- **provider 报 "not found / 无法启动"**:命令名不对,用 `AISR_CLAUDE_BIN` /
  `AISR_CURSOR_BIN` 指到 `.cmd` 或全路径。
- **Python 报 AF_UNIX / Unix socket 不可用**:改用 TCP(设 `AISR_BASE_URL`)。
- **容器连不上**:确认宿主 daemon 绑的是 `0.0.0.0`(不是 `127.0.0.1`);看防火墙是否放行。
- **端口占用**:`Get-NetTCPConnection -LocalPort 7878`(PowerShell)查谁在用。
```
