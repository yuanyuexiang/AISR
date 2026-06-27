# AISR — AI Session Runtime

> **A unified session runtime for AI CLIs.**
> 一个运行在本机的 AI CLI 会话运行时:统一管理多个 AI CLI 的 Session 与上下文,
> 让其他本地程序通过统一接口调用大模型。

> ⚠️ **状态:早期开发(pre-alpha)。** 已可运行:`aisr ask`、`aisr session` 管理、
> daemon **`aisr serve`**(Unix socket 上的 `/v1` HTTP API,NDJSON 流式)、
> **Go SDK**([pkg/sdk](pkg/sdk/sdk.go))与 **Python 客户端**
> ([clients/python](clients/python/))——均已对真实 claude 验证通过(含按名
> resume、优雅退出)。**Provider:Claude + Cursor 均已接入**。尚未实现:Gemini
> provider、`aisr chat`、TCP 鉴权、常驻进程池。上手见下方 **使用方法**。

---

## 为什么需要它

让本机上的**其他 agent 应用,无需自备 API key、也无需按 token 付费,就能用上大模型。**

claude code / cursor / gemini 等 CLI 已经用各自的**订阅(subscription)登录**好了。
AISR 复用它们登录好的凭据,在 headless 模式下统一驱动它们,把模型能力以统一接口
(CLI / SDK / 本地 HTTP API)暴露给上层 agent。对上层而言:**零 API key、零 token
账单,却能用上大模型** —— 这就是 AISR 存在的理由。

```text
[前提] 本机装好并登录:claude / cursor / gemini
            │
            ▼
上层 Agent ──HTTP/SDK──> AISR daemon ──按需拉起+resume──> claude (-p --output-format stream-json)
            ▲                                                  │
            └──────────── NDJSON 结构化事件流 ◀────────────────┘
   (text / tool_use / tool_result / usage / done)
```

## ⚠️ 定位与边界(务必先读)

* **本地个人 runtime**:服务对象是「你自己的 agent + 你自己的订阅 + 你自己的机器」。
* **不是对外 / 共享的 LLM 网关**:不要包装成对多人或对外提供服务的 API 网关——
  订阅授权一般面向个人交互式使用,对外提供服务可能违反服务条款(ToS)并导致封号。
* **已知约束**:① 订阅有额度 / 限流,多 agent 共用比单人更易撞限,不像付费 API 可
  横向扩展;② 输出是 CLI 的 agent 行为(可用 `--model` / `--system-prompt` 调),
  与直连 API 的原始模型行为有差异。

它**不负责** Agent / Workflow / RAG / Web UI / 浏览器 —— 这些都是上层应用。

## 特性

* 统一管理多个 AI CLI 的 **Session 生命周期**与上下文持久化
* **结构化事件流**(NDJSON:text / tool_use / tool_result / usage / error / done),
  三种接入方式格式一致
* **按需拉起 + `--resume`** 的轻量会话模型(进程可丢弃,session-id 持久化)
* 统一接口:**本地 HTTP API(daemon)+ Go SDK + 薄 Python 客户端 + CLI**
* 优先对接各 CLI 的**无头 / 结构化模式**,而非脆弱的终端解析

## 支持的 Provider

| Provider | 集成模式 | 状态 |
|----------|---------|------|
| Claude Code (`claude`) | structured(`-p --output-format stream-json --verbose`) | ✅ 已实现 |
| Cursor (`cursor-agent`) | structured(`-p --output-format stream-json --force`) | ✅ 已实现 |
| Gemini CLI | 待验证(structured / pty) | spike 后加入 |

> Codex CLI 暂不纳入。集成可行性以 [技术方案.md](技术方案.md) §十 的 spike 结论为准。

## 使用方法

### 0. 前提

* 本机已安装并登录 `claude`(确认:`claude -p "hi"` 能正常回话)。
* 构建需 Go 1.22+;用 Python 客户端需 `python3`(标准库即可,无第三方依赖)。

### 1. 构建

```bash
go build -o ./bin/aisr ./cmd/aisr      # 或:make build
```

### 2. 启动守护进程(daemon)

daemon 是给**其他程序接入**用的。开一个终端挂着:

```bash
./bin/aisr serve                       # 监听 ~/.aisr/aisr.sock
# 自定义:--socket /path/aisr.sock   或   --listen 127.0.0.1:7878 (TCP)
```

看到 `aisr serve: listening on ...` 即成功。`Ctrl-C` 或 `kill`(SIGTERM)优雅退出
并清理 socket。

> Unix socket 路径有长度上限(macOS 约 104 字节),默认路径没问题,自定义 `--socket`
> 别用过长路径。

### 3. 命令行用法(CLI,不需要 daemon)

CLI 直连磁盘,自己在终端用很方便:

```bash
# 一次性提问(临时会话;session-id 打到 stderr)
./bin/aisr ask "用一句话回答:1+1等于几"
./bin/aisr ask --json "你好"                       # 输出 NDJSON 事件流

# 列出可用 provider 及能力
./bin/aisr providers

# 受管理的会话(会话名是位置参数;按名字 resume,首次自动创建)
./bin/aisr session create dev --provider claude --workspace ./demo
./bin/aisr ask --session dev "记住数字 7"
./bin/aisr ask --session dev "我让你记的数字?"     # 自动接上上下文
./bin/aisr session show dev
./bin/aisr session list
./bin/aisr session remove dev
```

> 约定:会话名在 `session create/show/remove` 里都是**位置参数**;`ask` 用 `--session`
> (因为位置参数留给 prompt)。`session create` 的 flags 要写在名字**前面**。

### 4. 用 Python 调用(daemon 需在跑)

最快——跑自带示例:

```bash
PYTHONPATH=clients/python python3 clients/python/example.py "用一句话回答:1+1等于几"
```

自己写几行(`PYTHONPATH` 指到 [clients/python](clients/python/) 即可 `import aisr`):

```python
from aisr import Client

c = Client()                       # 默认连 ~/.aisr/aisr.sock

# 流式一轮;session 不存在会自动创建
for ev in c.send("dev", "优化这个 Go 项目", workspace="./demo"):
    if ev.kind == "text":
        print(ev.text, end="", flush=True)
    elif ev.kind == "error":
        raise RuntimeError(ev.text)

# 同名再问一次 = 自动 resume,接上上下文
for ev in c.send("dev", "刚才你建议了什么?"):
    if ev.kind == "text":
        print(ev.text, end="", flush=True)

# 会话管理
c.providers()
c.list_sessions()
c.get_session("dev")
c.remove_session("dev")
```

事件 `kind` 取值:`text` / `tool_use` / `tool_result` / `usage` / `error` / `done`。
详见 [clients/python/README.md](clients/python/README.md)。

### 5. 用 Go 调用(daemon 需在跑)

```bash
go run ./examples/go -session dev "用一句话回答:1+1等于几"
```

```go
import "github.com/yuanyuexiang/aisr/pkg/sdk"

c := sdk.New()                     // 默认连 ~/.aisr/aisr.sock
events, err := c.Send(ctx, "dev", "优化这个 Go 项目", sdk.SendOptions{Workspace: "./demo"})
if err != nil { /* ... */ }
for ev := range events {
    if ev.Kind == sdk.EventText {
        fmt.Print(ev.Text)
    }
}
```

### 6. 用 curl / 任意语言(HTTP over Unix socket)

```bash
curl --unix-socket ~/.aisr/aisr.sock http://localhost/v1/providers
curl --unix-socket ~/.aisr/aisr.sock -N -X POST \
  http://localhost/v1/sessions/dev/messages \
  -H 'Content-Type: application/json' -d '{"prompt":"优化这个项目"}'   # NDJSON 流
```

完整端点、事件模型与错误码见 **[docs/接口使用文档.md](docs/接口使用文档.md)**。

### 7. 在容器 / 跨网络调用(TCP + token)

Unix socket 之外,daemon 也可监听 TCP 给其他机器 / 容器调用。**TCP 模式强制要
bearer token**(网络可达,不能裸奔)。自己定一个值即可(生产换强随机值):

```bash
AISR_TOKEN=123 ./bin/aisr serve --listen 0.0.0.0:7878    # 缺 token 会拒绝启动
```

客户端用环境变量 `AISR_BASE_URL` + `AISR_TOKEN`(SDK / Python 都读),或 curl 手带:

```bash
curl -H "Authorization: Bearer 123" http://127.0.0.1:7878/v1/providers
```

**在 Docker 容器里调用 AISR**(调用方在容器、daemon 在宿主机,已实测):见
**[docker/README.md](docker/README.md)** 与 [docker/docker-compose.yml](docker/docker-compose.yml)。

> 计划中(尚未实现):`aisr chat`(交互式 REPL)、`cancel` 端点。

## 路线图

**V1(最小闭环)**:Claude Provider(结构化)、Session 生命周期 + 持久化、按需 resume、
Streaming、Workspace、配置、CLI、对外 HTTP API、Go SDK、薄 Python 客户端;通过 spike
后再加 Cursor / Gemini Provider。

**V2**:MCP 生命周期管理、Provider 插件机制、常驻进程池、Session 快照 / 恢复、
多 Provider 协作、Prompt 模板、Hook、可观测性。

## 文档

* [技术方案.md](技术方案.md) —— 设计与架构(source of truth)
* [docs/接口使用文档.md](docs/接口使用文档.md) —— 面向应用层的接口契约
* [CLAUDE.md](CLAUDE.md) —— 给 Claude Code 的工作指南

## 许可证

待定(TBD)。
