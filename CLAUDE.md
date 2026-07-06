# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Current state

**Early development (pre-alpha) — CLI + daemon + Go SDK + Python client.**
Core orchestration lives in `session.Manager` (shared by every entry point). The CLI
(`aisr ask`, `aisr session create|list|remove`) and the daemon (`aisr serve`,
exposing the `/v1` HTTP API over a Unix socket with NDJSON streaming) are thin layers
over it; the **Go SDK** ([pkg/sdk](pkg/sdk/sdk.go)) and the stdlib-only **Python
client** ([clients/python/aisr.py](clients/python/aisr.py)) are clients of the daemon.
Sessions persist under `~/.aisr/sessions` (one JSON each); `--session <name>` / `POST
/v1/sessions/{name}/messages` resume by friendly name (lazy-create on first use).
**Providers: Claude (`claude`) and Cursor (`cursor-agent`) are both implemented**
(structured stream-json, resume, zero-API-key via subscription/login). Verified
against the real CLIs: CLI, daemon, Go SDK, and Python client all do create → ask →
resume → list → remove for both providers; daemon does graceful shutdown (SIGTERM →
exit 0, socket unlinked). Adding Cursor needed **zero** changes to CLI/daemon/SDK —
only registering `cursor.New()` in the registry — confirming the abstraction holds.
The daemon also serves over **TCP** (`serve --listen ADDR`), which **requires a
bearer token** (`--token` / `AISR_TOKEN`); the Unix socket relies on file perms.
Clients (Go SDK, Python) read `AISR_BASE_URL` / `AISR_SOCKET` / `AISR_TOKEN` from the
env. Calling AISR from a Docker container (caller in container, daemon on host via
`host.docker.internal` + token) is set up and verified — see [docker/](docker/README.md).
**Agent mode (Claude + Cursor):** `messages` accepts an optional `agent` block
that passes the autonomous-agent knobs to the CLI — `system_prompt` /
`append_system_prompt`, `allowed_tools` / `disallowed_tools`, `mcp_config`
(external MCP, inline JSON), `add_dirs`, `env`, `max_turns`, `permission_mode`.
The tool whitelist is the real gate, so `bypassPermissions` isn't required.
`cancel` is implemented (`POST /v1/sessions/{name}/cancel`). The event model has
a `thinking` kind. These let an upper layer drive a full tool-using agent (e.g.
inline ask_user via a blocking external MCP tool) — verified by spike for BOTH
providers. **Cursor** honors the same block via a different mechanism (its knobs
differ): the `mcp_config` is written to `<workspace>/.cursor/mcp.json` +
`--approve-mcps`, the system prompt is prepended to the prompt (no flag), and
`tool_call` events (mcpToolCall / readToolCall / …) are mapped to the unified
tool_use/tool_result. Cursor drops `add_dirs` (single `--workspace`) and the
tool whitelist (coarse `--force` gate). **Not yet built:** Gemini provider,
resident process pool.
Module: `github.com/yuanyuexiang/aisr` (zero external deps; the SDK exposes its
own public types, not internal ones). Git repo (branch `main`).

Build, test & run (also see [Makefile](Makefile): `make build|vet|test`):

```bash
go build -o ./bin/aisr ./cmd/aisr           # build
go vet ./... && go test ./...               # vet + unit tests (parser, manager, api)
./bin/aisr ask "你好"                        # ephemeral one-shot; session id -> stderr
./bin/aisr providers                         # list providers + capabilities
./bin/aisr session create dev --workspace ./demo   # name is a positional arg
./bin/aisr ask --session dev "继续上文"       # resume by name; persists provider session
./bin/aisr serve                            # daemon on ~/.aisr/aisr.sock
curl --unix-socket ~/.aisr/aisr.sock -N -X POST \
  http://localhost/v1/sessions/dev/messages -d '{"prompt":"hi"}'   # NDJSON stream
go run ./examples/go -session demo "你好"     # Go SDK example (needs daemon up)
PYTHONPATH=clients/python python3 clients/python/example.py "你好"  # Python client
```

Notes for implementers:
- Each provider has its own parser + fixture test (claude and cursor share the
  Anthropic-style content-block shape but differ — see below); when a provider's
  event mapping changes, update its `*_test.go`.
- **Cursor** ([internal/provider/cursor/cursor.go](internal/provider/cursor/cursor.go))
  needs `--force` (clears the workspace-trust gate + allows tools in headless), and
  **must ignore `{"type":"user"}`** events (Cursor echoes the prompt there, unlike
  Claude where `user` is a tool result).
- The daemon installs its signal handler **before** binding (avoids a startup race);
  keep it that way when editing `cmdServe`.
- **Logging** is the `log/slog` default logger, configured once in `cmdServe`
  (`setupLogging`): stderr by default, or a file via `--log-file` / `AISR_LOG_FILE`;
  level `info` (default) or `debug` via `--log-level` / `AISR_LOG_LEVEL`. The API
  layer logs an access line per request + one line per turn (`internal/api`); debug
  adds the CLI spawn argv (`provider.StreamCommand`) and per-event kinds. Use
  `slog.Info/Warn/Error/Debug` for new logs — don't reintroduce `log.Default()` or a
  per-Server logger. No prompt/output content is logged.
- **Unix socket path length limit (~104 chars on macOS)**: `~/.aisr/aisr.sock` is
  fine, but a long custom `--socket` path fails with `bind: invalid argument`.
- **Windows**: code cross-compiles (amd64/arm64). Provider binary names are
  overridable via `AISR_CLAUDE_BIN` / `AISR_CURSOR_BIN` (e.g. `claude.cmd`). Native
  Windows should use **TCP** — Python's `socket.AF_UNIX` is typically absent there
  (the client raises a clear error). WSL2 behaves like Linux. See
  [docs/windows.md](docs/windows.md).

Treat [技术方案.md](技术方案.md) as the source of truth for design decisions; this
file summarizes it for quick orientation. If the spec and an instruction conflict,
surface the conflict rather than silently picking one.

## What this project is (and is not)

**AISR (AI Session Runtime)** — *a unified session runtime for AI CLIs*, written in **Go**.
The CLI binary is `aisr`.

Its single responsibility: **manage the sessions of multiple AI CLIs and expose one
unified interface to call them.** Target CLIs: Claude Code, Cursor CLI, Gemini CLI
(Aider and others later). **Codex CLI is out of scope** for now.

**Core motivation (the reason this project exists):** let other local agent apps use
large models **without their own API key and without per-token billing**, by reusing
the AI CLIs that are already logged in via their *subscriptions*. AISR drives those
CLIs in headless mode and exposes the model capability through one unified interface —
downstream apps need zero API key.

**Positioning boundary:** this is a **local, personal runtime** (your own agents +
your own subscription + your own machine), **not a shared/public LLM gateway**.
Subscription terms generally cover individual interactive use; turning it into an
outward service for many users risks ToS violation and account bans — keep it local.
Known constraints: subscription rate/usage limits don't scale like a paid API, and
output reflects each CLI's agent behavior (tunable via `--model` / `--system-prompt`)
rather than a raw model endpoint.

It **owns**: session lifecycle, a session index (+ optional process pool), context
persistence, streaming, provider management, workspace management, optional MCP
lifecycle (V2), and a unified outward API (CLI + SDK).

It explicitly **does NOT do**: Agent logic, Workflows, LangGraph, RAG, Web UI, or a
browser. Those are upper-layer applications built *on top of* AISR. This scope
boundary is the project's core identity — when a request would pull AISR toward
becoming an agent framework, flag it against this boundary before implementing.

## Design principles (read before implementing)

These four principles, not the module list, decide whether the project works. They
come from [技术方案.md](技术方案.md) §二:

1. **Structured integration over terminal scraping.** Target CLIs are TUI programs;
   parsing their stdout (ANSI/spinners/redraws) is fragile and breaks on upgrades,
   and many detect isatty and degrade under a plain pipe. So integrate via each
   CLI's **non-interactive / headless / structured (JSON) mode** first; fall back to
   **PTY + text parsing** only when no structured mode exists, marked best-effort.
   E.g. Claude Code: `claude -p "<prompt>" --output-format stream-json --verbose`
   (`--verbose` is required with `stream-json` — verified, see 技术方案.md §十).
2. **Interfaces express capability differences — no lowest-common-denominator.** A
   Provider declares its `Capabilities` and emits **typed events** (text / tool_use /
   tool_result / usage / error / done), keeping the provider's raw payload so no
   information is lost. Do not collapse everything to "send text / receive text".
3. **A session is a persisted session-id, not a live process.** The session's
   identity = persisted `session-id + workspace + metadata`; processes are
   disposable execution carriers. Default model is **on-demand spawn + `--resume`**;
   a resident hot-process pool is an optional V2 optimization, not the default.
4. **Minimal loop first.** V1 ships **one provider (Claude)** end to end
   (`create → ask → resume → list → remove`) to prove the abstraction before adding
   Cursor / Gemini.

## Planned architecture

Layered: a user application (agent / script / IDE plugin) calls AISR through a
**Go SDK, a thin Python client, or the CLI**. AISR's core is three cooperating
subsystems:

- **Session Manager** — owns lifecycle and a *persisted session index* (e.g.
  `session-001`); each session maps to one Workspace and its persisted Context. (An
  index, not a resident process pool — pooling is V2.)
- **Provider Router** — resolves a provider name to its concrete implementation.
- **Stream Manager** — normalizes each provider's events into one uniform `Event`
  stream flowing back through the SDK to the application.

Each backing CLI (Claude, Cursor, Gemini) is wrapped by a **Provider** that emits
structured events and declares its capabilities:

```go
type EventKind string // text | tool_use | tool_result | usage | error | done

type Event struct {
    Kind EventKind
    Text string
    Raw  json.RawMessage // provider's original payload — never discard it
}

type Capabilities struct {
    StructuredOutput bool // has JSON/headless mode -> decides integration mode
    Streaming        bool
    Resume           bool // can restore context by session-id
    ToolUse          bool
    MCP              bool
}

type Provider interface {
    Name() string
    Capabilities() Capabilities
    Start(ctx context.Context, opts SessionOpts) (sessionID string, err error)
    Send(ctx context.Context, prompt string) (<-chan Event, error)
    Close() error
}
```

Integration mode is chosen by `Capabilities.StructuredOutput`: `structured`
(preferred; Claude) vs `pty` (best-effort fallback; Cursor/Gemini pending the spike).

**Session lifecycle (persisted):** `Create → Idle → Active → Idle → Expired →
Destroy`, driven by `Create / Acquire / Release / Destroy / Recover`. **Process
state (ephemeral):** `Cold → Warm → Cold`. Default cycle: `Acquire()` spawns a
process on demand and `--resume`s context; `Release()` lets it exit (context is
already persisted). Sessions/contexts persist under `~/.aisr/sessions`.

## Planned package layout (Go)

```
cmd/aisr/                  # CLI entrypoint
internal/
    api/                   # outward unified API
    session/               # Session Manager + lifecycle + index
    provider/
        provider.go        # Provider interface + Event/Capabilities
        claude/            # ClaudeProvider (structured mode — the V1 reference)
        cursor/  gemini/   # pending the spike
    router/                # Provider Router
    stream/                # Stream Manager (normalize structured events)
    workspace/             # working-directory management
    storage/               # session/context persistence
    config/  logger/
pkg/sdk/                   # Go SDK (public)
clients/python/            # thin Python client (calls the local API)
configs/  docs/  scripts/  Makefile  go.mod
```

## Planned CLI surface (git-style subcommands)

```bash
aisr session create --provider claude --workspace ./demo   # -> prints a session id
aisr session list                                          # id / provider / status
aisr session remove dev-001
aisr chat --session dev-001                                # interactive REPL
aisr ask --session dev-001 "优化这个 Go 项目"              # one-shot call (--json -> NDJSON events)
aisr serve                                                 # local daemon on a Unix socket
```

Config is YAML (see the spec for the full shape): `runtime` (`max_sessions`,
`idle_timeout`, `execution: on-demand|resident`), `providers` (per-provider
`command` + `mode: structured|pty`), `server` (`socket`, optional TCP `listen`/
`token`), `storage` (`session_dir`), `workspace` (`default`).

## Outward API contract (for upper-layer apps)

The outward interface is a **local daemon (`aisr serve`) exposing an HTTP/JSON API
over a Unix socket** (`~/.aisr/aisr.sock`); the Go SDK, the thin Python client, and
the CLI are all clients of it. Streaming uses **NDJSON** (one JSON `Event` per line)
so HTTP, SDK, and CLI emit the byte-identical event shape. The full contract —
`/v1` endpoints, the event model (`text` / `tool_use` / `tool_result` / `usage` /
`error` / `done`), error codes, and per-language examples — lives in
[docs/接口使用文档.md](docs/接口使用文档.md). Keep that doc, this file, and the spec
in sync when the API changes.

## Integration spike status

Per [技术方案.md](技术方案.md) §十, the whole approach hinges on whether each target
CLI can be driven structurally.

- **Claude: ✅ verified** (CLI 2.1.193). `claude -p --output-format stream-json
  --verbose` emits NDJSON (`system`/`init`, `rate_limit_event`, `assistant`,
  `result`); `session_id` comes from `init`/`result` and is **stable across
  `--resume`**; `--resume` continues context; `apiKeySource:"none"` confirms the
  no-API-key/subscription premise. Caveat: each headless call reloads the full CC
  system prompt (~6–9k cache-creation tokens), so resident mode and `--model`/tool
  trimming matter for cost. Full findings in 技术方案.md §十.
- **Cursor / Gemini: not yet verified** — check for an equivalent structured mode
  before implementing their providers; fall back to PTY otherwise.

Gate to proceed to V1: Claude `create → ask → resume → list` working — the ask +
resume + session-id legs are now proven at the CLI level.

## Scope of V1 vs V2 (from the spec)

- **V1 (minimal loop):** Claude provider (structured), session lifecycle +
  persistence (session-id/workspace/metadata), on-demand resume execution,
  streaming (normalized events), workspace management, config, CLI, Go SDK, thin
  Python client. Cursor/Gemini providers added *after* the spike passes.
- **V2 (do not pull forward without reason):** MCP lifecycle, provider plugin
  mechanism, **resident process pool**, session snapshot/restore, multi-provider
  collaboration, prompt templates, before/after-send hooks, observability.
