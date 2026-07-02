// Package provider defines the unified abstraction over AI CLIs.
//
// Each backing CLI (claude / cursor / gemini) is wrapped by a Provider that
// drives it in headless mode and normalizes its output into a single stream of
// structured Events. See 技术方案.md §五 for the design.
package provider

import (
	"context"
	"encoding/json"
)

// EventKind is the type of a normalized event emitted by a Provider.
type EventKind string

const (
	EventText       EventKind = "text"        // model text output (incremental)
	EventThinking   EventKind = "thinking"    // model reasoning (extended thinking)
	EventToolUse    EventKind = "tool_use"    // the model invoked a tool
	EventToolResult EventKind = "tool_result" // a tool returned a result
	EventUsage      EventKind = "usage"       // token / cost usage
	EventError      EventKind = "error"       // the turn errored
	EventDone       EventKind = "done"        // the turn finished
)

// Event is the uniform unit streamed to callers, identical across providers.
// Raw preserves the provider's original payload so nothing is lost.
type Event struct {
	Kind EventKind       `json:"kind"`
	Text string          `json:"text,omitempty"`
	Raw  json.RawMessage `json:"raw,omitempty"`
}

// Capabilities declares what a provider supports; callers degrade accordingly.
type Capabilities struct {
	StructuredOutput bool `json:"structured_output"` // has JSON/headless mode
	Streaming        bool `json:"streaming"`
	Resume           bool `json:"resume"` // can restore context by session id
	ToolUse          bool `json:"tool_use"`
	MCP              bool `json:"mcp"`
}

// AgentOptions carries the richer agent-control knobs that a structured provider
// (Claude) can pass to its CLI. They let an upper layer drive an autonomous,
// tool-using agent — register external tools (MCP), shape the system prompt,
// gate which tools may run, and expose extra directories — instead of a bare
// "prompt in / text out" call. Providers that can't honor a field ignore it
// (declare support via Capabilities). All fields are optional.
type AgentOptions struct {
	// SystemPrompt REPLACES the CLI's default system prompt (--system-prompt).
	// Mutually exclusive in practice with AppendSystemPrompt.
	SystemPrompt string `json:"system_prompt,omitempty"`
	// AppendSystemPrompt is appended to the CLI's default system prompt.
	AppendSystemPrompt string `json:"append_system_prompt,omitempty"`
	// Env are extra environment variables for the spawned CLI process (e.g. data
	// roots, a venv PATH). Merged over the daemon's own environment.
	Env map[string]string `json:"env,omitempty"`
	// AllowedTools / DisallowedTools whitelist or blacklist tool names
	// (e.g. "Read", "Bash", "mcp__auric__ask_user"). The whitelist is the real
	// permission gate in headless mode, so bypassing permissions isn't required.
	AllowedTools    []string `json:"allowed_tools,omitempty"`
	DisallowedTools []string `json:"disallowed_tools,omitempty"`
	// MCPConfig is inline MCP-server JSON (the CLI's --mcp-config accepts a JSON
	// string), e.g. {"mcpServers":{"auric":{"type":"http","url":"..."}}}.
	MCPConfig json.RawMessage `json:"mcp_config,omitempty"`
	// AddDirs are extra directories the agent's tools may access (--add-dir).
	AddDirs []string `json:"add_dirs,omitempty"`
	// MaxTurns caps the agent's internal tool-use loop (0 = CLI default).
	MaxTurns int `json:"max_turns,omitempty"`
	// PermissionMode overrides the CLI permission mode (e.g. "bypassPermissions").
	// Usually leave empty and rely on AllowedTools.
	PermissionMode string `json:"permission_mode,omitempty"`
}

// SessionOpts parameterizes one turn. An empty SessionID starts a new session;
// a non-empty one resumes that session's context.
type SessionOpts struct {
	SessionID string
	Workspace string
	Model     string
	// Agent, when non-nil, enables agent-mode controls for providers that
	// support them (Claude). Nil keeps the plain prompt-in/text-out behavior.
	Agent *AgentOptions
}

// Turn is the handle for one in-flight exchange.
//
// Consume Events to completion; SessionID is valid only after Events is fully
// drained (the producing goroutine sets it then closes the channel, so reading
// it after the range loop ends is race-free).
type Turn struct {
	Events    <-chan Event
	SessionID string
}

// Provider drives one AI CLI.
type Provider interface {
	Name() string
	Capabilities() Capabilities
	// Send runs a single turn (on-demand: spawn, stream, exit).
	Send(ctx context.Context, opts SessionOpts, prompt string) (*Turn, error)
}
