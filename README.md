# AISR — AI Session Runtime

> **A unified session runtime for AI CLIs.**
> 一个运行在本机的 AI CLI 会话运行时:统一管理多个 AI CLI 的 Session 与上下文,
> 让其他本地程序通过统一接口调用大模型。

> ⚠️ **状态:早期开发(pre-alpha)。** 已可运行:`aisr ask`、`aisr session` 管理、
> daemon **`aisr serve`**(Unix socket 上的 `/v1` HTTP API,NDJSON 流式)、
> **Go SDK**([pkg/sdk](pkg/sdk/sdk.go))与 **Python 客户端**
> ([clients/python](clients/python/))——均已对真实 claude 验证通过(含按名
> resume、优雅退出)。尚未实现:Cursor/Gemini provider、TCP 鉴权、常驻进程池。
> 下文 **快速开始** 中的 `chat` 仍为**目标接口**;当前能跑的见「现在能跑什么」。

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
| Claude Code | structured(`-p --output-format stream-json --verbose`) | ✅ spike 已验证,待实现 |
| Cursor CLI | 待验证(structured / pty) | spike 后加入 |
| Gemini CLI | 待验证(structured / pty) | spike 后加入 |

> Codex CLI 暂不纳入。集成可行性以 [技术方案.md](技术方案.md) §十 的 spike 结论为准。

## 现在能跑什么

最薄垂直切片:`aisr ask` 端到端驱动 Claude(前提:本机已安装并登录 `claude`)。

```bash
go build -o ./bin/aisr ./cmd/aisr

# 一次性提问(session-id 打印到 stderr,便于复用)
./bin/aisr ask "用一句话回答:1+1等于几"

# 输出归一化的 NDJSON 事件流(text / tool_use / usage / done ...)
./bin/aisr ask --json "你好"

# 用会话名续接上下文(首次自动创建)
./bin/aisr ask --session dev "我刚才问了什么?"
```

**daemon(给其他程序接入):**

```bash
./bin/aisr serve                      # 监听 ~/.aisr/aisr.sock

# 另一个终端 / 任意语言的程序:
curl --unix-socket ~/.aisr/aisr.sock http://localhost/v1/providers
curl --unix-socket ~/.aisr/aisr.sock -N -X POST \
  http://localhost/v1/sessions/dev/messages \
  -H 'Content-Type: application/json' -d '{"prompt":"优化这个项目"}'   # NDJSON 流
```

端点与事件模型见 [docs/接口使用文档.md](docs/接口使用文档.md)。

**Go SDK / Python 客户端**(daemon 在跑即可,几行代码接入):

```bash
go run ./examples/go -session demo "用一句话回答:1+1等于几"        # Go SDK
PYTHONPATH=clients/python python3 clients/python/example.py "你好"  # Python
```

代码见 [pkg/sdk](pkg/sdk/sdk.go) 与 [clients/python](clients/python/)。

## 快速开始(目标接口,规划中)

```bash
# 前提:本机已安装并登录 claude / cursor / gemini

# 启动本地 daemon(监听 ~/.aisr/aisr.sock)
aisr serve

# 创建一个绑定到某 workspace 的会话
aisr session create --provider claude --workspace ./demo   # -> Session: dev-001

# 一次性调用(--json 输出 NDJSON 事件,便于脚本解析)
aisr ask --session dev-001 "优化这个 Go 项目"

# 进入交互模式
aisr chat --session dev-001

# 查看 / 删除
aisr session list
aisr session remove dev-001
```

### 在你的程序里调用

Go:

```go
client := sdk.NewClient()                 // 默认连 ~/.aisr/aisr.sock
sess, _ := client.CreateSession(ctx, sdk.SessionOpts{Provider: "claude", Workspace: "./demo"})
events, _ := client.Send(ctx, sess.ID, "优化这个 Go 项目")
for ev := range events {
    if ev.Kind == sdk.EventText { fmt.Print(ev.Text) }
}
```

Python:

```python
from aisr import Client
client = Client()
session = client.create_session(provider="claude", workspace="./demo")
for event in client.send(session.id, "优化这个 Go 项目"):
    if event.kind == "text":
        print(event.text, end="", flush=True)
```

完整端点、事件模型与错误码见 **[docs/接口使用文档.md](docs/接口使用文档.md)**。

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
