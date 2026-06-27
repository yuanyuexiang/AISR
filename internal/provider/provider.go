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

// SessionOpts parameterizes one turn. An empty SessionID starts a new session;
// a non-empty one resumes that session's context.
type SessionOpts struct {
	SessionID string
	Workspace string
	Model     string
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
