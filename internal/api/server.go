// Package api exposes the session.Manager over a local HTTP/JSON API.
//
// It is the daemon's outward contract (see docs/接口使用文档.md): /v1 endpoints,
// with the message stream delivered as NDJSON (one provider.Event per line) so
// HTTP, SDK, and CLI all emit the byte-identical event shape.
package api

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/yuanyuexiang/aisr/internal/provider"
	"github.com/yuanyuexiang/aisr/internal/session"
)

// Server wires the Manager and provider listing to HTTP handlers.
type Server struct {
	mgr       *session.Manager
	providers []provider.Provider
	token     string // if non-empty, a Bearer token is required (TCP mode)
}

// NewServer builds an API server. When token is non-empty, every request must
// carry "Authorization: Bearer <token>" (used for TCP listeners; the Unix socket
// relies on file permissions instead). Logging uses the slog default logger,
// configured by the daemon (see cmd/aisr setupLogging).
func NewServer(mgr *session.Manager, providers []provider.Provider, token string) *Server {
	return &Server{mgr: mgr, providers: providers, token: token}
}

// Handler returns the routed http.Handler (Go 1.22+ method+wildcard patterns),
// wrapped with bearer-token auth when a token is configured, and an access log.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/providers", s.handleProviders)
	mux.HandleFunc("POST /v1/sessions", s.handleCreate)
	mux.HandleFunc("GET /v1/sessions", s.handleList)
	mux.HandleFunc("GET /v1/sessions/{name}", s.handleGet)
	mux.HandleFunc("DELETE /v1/sessions/{name}", s.handleDelete)
	mux.HandleFunc("POST /v1/sessions/{name}/messages", s.handleMessages)
	mux.HandleFunc("POST /v1/sessions/{name}/cancel", s.handleCancel)
	var h http.Handler = mux
	if s.token != "" {
		h = s.requireToken(h)
	}
	return logRequests(h)
}

// logRequests is an access-log middleware: one info line per request with method,
// path, status, and duration (for a streaming /messages turn, the duration is the
// whole turn). Outermost so even 401s are logged.
func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		slog.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", sw.status,
			"dur", time.Since(start).Round(time.Millisecond).String())
	})
}

// statusWriter captures the response status and forwards Flush so NDJSON
// streaming (handleMessages) still flushes through the middleware wrapper.
type statusWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.wroteHeader {
		w.status = code
		w.wroteHeader = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// requireToken enforces "Authorization: Bearer <token>" (constant-time compare).
func (s *Server) requireToken(next http.Handler) http.Handler {
	want := []byte(s.token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(got), want) != 1 {
			writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing or invalid bearer token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// --- handlers ---

func (s *Server) handleProviders(w http.ResponseWriter, _ *http.Request) {
	type info struct {
		Name         string                `json:"name"`
		Capabilities provider.Capabilities `json:"capabilities"`
	}
	out := struct {
		Providers []info `json:"providers"`
	}{Providers: []info{}}
	for _, p := range s.providers {
		out.Providers = append(out.Providers, info{p.Name(), p.Capabilities()})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Provider  string `json:"provider"`
		Workspace string `json:"workspace"`
		Name      string `json:"name"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
		return
	}
	if req.Provider == "" {
		req.Provider = provider.DefaultName
	}
	rec, err := s.mgr.Create(req.Provider, req.Name, req.Workspace)
	if err != nil {
		status, code := classify(err)
		writeError(w, status, code, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, rec)
}

func (s *Server) handleList(w http.ResponseWriter, _ *http.Request) {
	recs, err := s.mgr.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	if recs == nil {
		recs = []*session.Session{}
	}
	writeJSON(w, http.StatusOK, struct {
		Sessions []*session.Session `json:"sessions"`
	}{recs})
}

func (s *Server) handleGet(w http.ResponseWriter, r *http.Request) {
	rec, err := s.mgr.Get(r.PathValue("name"))
	if err != nil {
		status, code := classify(err)
		writeError(w, status, code, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.mgr.Remove(r.PathValue("name")); err != nil {
		status, code := classify(err)
		writeError(w, status, code, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var req struct {
		Prompt    string                 `json:"prompt"`
		Provider  string                 `json:"provider"`
		Workspace string                 `json:"workspace"`
		Model     string                 `json:"model"`
		Agent     *provider.AgentOptions `json:"agent"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
		return
	}
	if req.Prompt == "" {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "missing prompt")
		return
	}
	if req.Provider == "" {
		req.Provider = provider.DefaultName
	}

	slog.Info("turn", "session", name, "provider", req.Provider, "agent", req.Agent != nil)

	turn, err := s.mgr.Ask(r.Context(), session.AskRequest{
		SessionName: name,
		Provider:    req.Provider,
		Workspace:   req.Workspace,
		Model:       req.Model,
		Prompt:      req.Prompt,
		Agent:       req.Agent,
	})
	if err != nil {
		// Pre-stream errors get a proper status; mid-stream errors arrive as
		// `error` events once streaming has begun.
		status, code := classify(err)
		slog.Warn("turn failed", "session", name, "provider", req.Provider, "code", code, "err", err.Error())
		writeError(w, status, code, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	enc := json.NewEncoder(w)
	for ev := range turn.Events {
		slog.Debug("event", "session", name, "kind", string(ev.Kind))
		if err := enc.Encode(ev); err != nil {
			break // client disconnected
		}
		if flusher != nil {
			flusher.Flush()
		}
	}
	if turn.SaveErr != nil {
		slog.Error("session save failed", "session", name, "err", turn.SaveErr.Error())
	}
}

func (s *Server) handleCancel(w http.ResponseWriter, r *http.Request) {
	if err := s.mgr.Cancel(r.PathValue("name")); err != nil {
		status, code := classify(err)
		writeError(w, status, code, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- helpers ---

func decodeJSON(r *http.Request, v any) error {
	if r.Body == nil {
		return nil
	}
	err := json.NewDecoder(r.Body).Decode(v)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil // empty body is allowed (zero-value request)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

type errorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	var b errorBody
	b.Error.Code = code
	b.Error.Message = msg
	writeJSON(w, status, b)
}

// classify maps Manager/provider errors to HTTP status + API error code
// (see docs/接口使用文档.md §7).
func classify(err error) (int, string) {
	switch {
	case errors.Is(err, session.ErrNotFound):
		return http.StatusNotFound, "SESSION_NOT_FOUND"
	case errors.Is(err, session.ErrExists):
		return http.StatusConflict, "SESSION_EXISTS"
	case errors.Is(err, session.ErrInvalidName):
		return http.StatusBadRequest, "INVALID_NAME"
	case errors.Is(err, session.ErrWorkspaceInvalid):
		return http.StatusBadRequest, "WORKSPACE_INVALID"
	case errors.Is(err, session.ErrNoActiveTurn):
		return http.StatusConflict, "NO_ACTIVE_TURN"
	case errors.Is(err, provider.ErrUnknown):
		return http.StatusBadRequest, "PROVIDER_NOT_FOUND"
	default:
		return http.StatusInternalServerError, "INTERNAL"
	}
}
