// Package sdk is the Go client for the AISR daemon (`aisr serve`).
//
// It talks to the daemon's /v1 HTTP API — by default over the local Unix socket
// ~/.aisr/aisr.sock — and exposes self-contained public types so callers need no
// internal packages. Streaming replies arrive as a channel of Event (NDJSON).
//
//	c := sdk.New()
//	events, err := c.Send(ctx, "dev", "优化这个项目", sdk.SendOptions{})
//	for ev := range events {
//	    if ev.Kind == sdk.EventText { fmt.Print(ev.Text) }
//	}
package sdk

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// EventKind mirrors the daemon's normalized event kinds.
type EventKind string

const (
	EventText       EventKind = "text"
	EventToolUse    EventKind = "tool_use"
	EventToolResult EventKind = "tool_result"
	EventUsage      EventKind = "usage"
	EventError      EventKind = "error"
	EventDone       EventKind = "done"
)

// Event is one normalized event from a streamed turn.
type Event struct {
	Kind EventKind       `json:"kind"`
	Text string          `json:"text,omitempty"`
	Raw  json.RawMessage `json:"raw,omitempty"`
}

// Session is a managed session record.
type Session struct {
	Name            string    `json:"name"`
	Provider        string    `json:"provider"`
	Workspace       string    `json:"workspace"`
	ProviderSession string    `json:"provider_session"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// Capabilities declares what a provider supports.
type Capabilities struct {
	StructuredOutput bool `json:"structured_output"`
	Streaming        bool `json:"streaming"`
	Resume           bool `json:"resume"`
	ToolUse          bool `json:"tool_use"`
	MCP              bool `json:"mcp"`
}

// ProviderInfo is a provider and its capabilities.
type ProviderInfo struct {
	Name         string       `json:"name"`
	Capabilities Capabilities `json:"capabilities"`
}

// APIError is returned for non-2xx responses.
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("aisr api: %s (%s, http %d)", e.Message, e.Code, e.Status)
}

// Client talks to the AISR daemon.
type Client struct {
	hc   *http.Client
	base string
}

type config struct {
	socket  string
	baseURL string
}

// Option configures a Client.
type Option func(*config)

// WithSocket connects over a specific Unix socket path.
func WithSocket(path string) Option { return func(c *config) { c.socket = path } }

// WithBaseURL connects over TCP/HTTP instead of a Unix socket (e.g. for a daemon
// started with `aisr serve --listen`). Takes precedence over WithSocket.
func WithBaseURL(rawURL string) Option { return func(c *config) { c.baseURL = rawURL } }

// New builds a Client. With no options it connects to ~/.aisr/aisr.sock.
//
// No overall HTTP timeout is set so streaming turns aren't cut off; pass a
// context with a deadline to bound a call.
func New(opts ...Option) *Client {
	var cfg config
	for _, o := range opts {
		o(&cfg)
	}

	if cfg.baseURL != "" {
		return &Client{hc: &http.Client{}, base: strings.TrimRight(cfg.baseURL, "/")}
	}

	socketPath := cfg.socket
	if socketPath == "" {
		home, _ := os.UserHomeDir()
		socketPath = filepath.Join(home, ".aisr", "aisr.sock")
	}
	hc := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socketPath)
			},
		},
	}
	return &Client{hc: hc, base: "http://unix"}
}

// Providers lists the daemon's providers and their capabilities.
func (c *Client) Providers(ctx context.Context) ([]ProviderInfo, error) {
	var out struct {
		Providers []ProviderInfo `json:"providers"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/v1/providers", nil, &out); err != nil {
		return nil, err
	}
	return out.Providers, nil
}

// CreateRequest parameterizes CreateSession.
type CreateRequest struct {
	Provider  string `json:"provider,omitempty"`
	Workspace string `json:"workspace,omitempty"`
	Name      string `json:"name,omitempty"`
}

// CreateSession registers a new managed session.
func (c *Client) CreateSession(ctx context.Context, req CreateRequest) (*Session, error) {
	var s Session
	if err := c.doJSON(ctx, http.MethodPost, "/v1/sessions", req, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// ListSessions returns all managed sessions.
func (c *Client) ListSessions(ctx context.Context) ([]Session, error) {
	var out struct {
		Sessions []Session `json:"sessions"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/v1/sessions", nil, &out); err != nil {
		return nil, err
	}
	return out.Sessions, nil
}

// GetSession returns one session by name.
func (c *Client) GetSession(ctx context.Context, name string) (*Session, error) {
	var s Session
	if err := c.doJSON(ctx, http.MethodGet, "/v1/sessions/"+url.PathEscape(name), nil, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// RemoveSession deletes a session.
func (c *Client) RemoveSession(ctx context.Context, name string) error {
	return c.doJSON(ctx, http.MethodDelete, "/v1/sessions/"+url.PathEscape(name), nil, nil)
}

// SendOptions are optional per-turn settings.
type SendOptions struct {
	Provider  string // for ephemeral/lazy-created sessions (default "claude" server-side)
	Workspace string
	Model     string
}

// Send runs one turn against the named session (lazily created if new) and
// returns a channel of normalized events. Drain it to completion; the channel
// closes when the turn ends (or the context is cancelled).
func (c *Client) Send(ctx context.Context, session, prompt string, opt SendOptions) (<-chan Event, error) {
	body := map[string]string{"prompt": prompt}
	if opt.Provider != "" {
		body["provider"] = opt.Provider
	}
	if opt.Workspace != "" {
		body["workspace"] = opt.Workspace
	}
	if opt.Model != "" {
		body["model"] = opt.Model
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.base+"/v1/sessions/"+url.PathEscape(session)+"/messages", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, parseAPIError(resp)
	}

	ch := make(chan Event)
	go func() {
		defer close(ch)
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
		for sc.Scan() {
			var ev Event
			if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
				continue
			}
			select {
			case ch <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, nil
}

// --- internal ---

func (c *Client) doJSON(ctx context.Context, method, path string, body, out any) error {
	var rdr *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return parseAPIError(resp)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func parseAPIError(resp *http.Response) error {
	var e struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&e)
	return &APIError{Status: resp.StatusCode, Code: e.Error.Code, Message: e.Error.Message}
}
