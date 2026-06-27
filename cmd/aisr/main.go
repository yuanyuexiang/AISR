// Command aisr is the AISR CLI and daemon — both thin layers over session.Manager.
//
// Implemented: `aisr ask`, `aisr session create|list|remove`, and `aisr serve`
// (the daemon exposing the /v1 HTTP API over a Unix socket). See 技术方案.md V1
// and docs/接口使用文档.md.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/yuanyuexiang/aisr/internal/api"
	"github.com/yuanyuexiang/aisr/internal/provider"
	"github.com/yuanyuexiang/aisr/internal/provider/claude"
	"github.com/yuanyuexiang/aisr/internal/provider/cursor"
	"github.com/yuanyuexiang/aisr/internal/session"
	"github.com/yuanyuexiang/aisr/internal/storage"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "ask":
		os.Exit(cmdAsk(os.Args[2:]))
	case "session":
		os.Exit(cmdSession(os.Args[2:]))
	case "serve":
		os.Exit(cmdServe(os.Args[2:]))
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "aisr: unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `aisr — AI Session Runtime (pre-alpha)

Usage:
  aisr ask [flags] "<prompt>"      one-shot prompt (optionally within a session)
  aisr session create [flags]      create a managed session
  aisr session list                list managed sessions
  aisr session remove <name>       delete a managed session
  aisr serve [flags]               run the daemon (HTTP API over a Unix socket)

Run "aisr <command> -h" for flags.
`)
}

// --- ask ---

func cmdAsk(argv []string) int {
	fs := flag.NewFlagSet("ask", flag.ExitOnError)
	provName := fs.String("provider", "claude", "provider name (ignored if --session names an existing session)")
	sessName := fs.String("session", "", "managed session name (resumes context; lazily created if new)")
	workspace := fs.String("workspace", "", "working directory")
	model := fs.String("model", "", "model override (e.g. haiku, sonnet, opus)")
	jsonOut := fs.Bool("json", false, "emit normalized NDJSON events")
	_ = fs.Parse(argv)

	prompt := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if prompt == "" {
		fmt.Fprintln(os.Stderr, "aisr ask: missing prompt")
		return 2
	}

	mgr, _, err := buildManager()
	if err != nil {
		fmt.Fprintln(os.Stderr, "aisr:", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	turn, err := mgr.Ask(ctx, session.AskRequest{
		SessionName: *sessName,
		Provider:    *provName,
		Workspace:   *workspace,
		Model:       *model,
		Prompt:      prompt,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "aisr:", err)
		return 1
	}

	enc := json.NewEncoder(os.Stdout)
	exit := 0
	for ev := range turn.Events {
		switch {
		case *jsonOut:
			_ = enc.Encode(ev)
		case ev.Kind == provider.EventText:
			fmt.Print(ev.Text)
		case ev.Kind == provider.EventToolUse:
			fmt.Fprintf(os.Stderr, "\n[tool_use] %s\n", ev.Raw)
		case ev.Kind == provider.EventError:
			fmt.Fprintf(os.Stderr, "\n[error] %s\n", ev.Text)
			exit = 1
		}
	}
	if !*jsonOut {
		fmt.Println()
	}

	// turn.Session / SaveErr are valid only now that the stream is drained.
	if turn.SaveErr != nil {
		fmt.Fprintln(os.Stderr, "aisr: warning: could not save session:", turn.SaveErr)
	}
	if s := turn.Session; s != nil && s.ProviderSession != "" {
		if turn.Managed {
			fmt.Fprintf(os.Stderr, "session: %s (%s)\n", s.Name, s.ProviderSession)
		} else {
			fmt.Fprintf(os.Stderr, "session: %s\n", s.ProviderSession)
		}
	}
	return exit
}

// --- session ---

func cmdSession(argv []string) int {
	if len(argv) < 1 {
		fmt.Fprintln(os.Stderr, "usage: aisr session <create|list|remove>")
		return 2
	}
	switch argv[0] {
	case "create":
		return cmdSessionCreate(argv[1:])
	case "list", "ls":
		return cmdSessionList(argv[1:])
	case "remove", "rm":
		return cmdSessionRemove(argv[1:])
	default:
		fmt.Fprintf(os.Stderr, "aisr session: unknown subcommand %q\n", argv[0])
		return 2
	}
}

func cmdSessionCreate(argv []string) int {
	fs := flag.NewFlagSet("session create", flag.ExitOnError)
	provName := fs.String("provider", "claude", "provider name")
	workspace := fs.String("workspace", "", "working directory")
	name := fs.String("name", "", "session name (auto-generated if empty)")
	_ = fs.Parse(argv)

	mgr, _, err := buildManager()
	if err != nil {
		fmt.Fprintln(os.Stderr, "aisr:", err)
		return 1
	}
	rec, err := mgr.Create(*provName, *name, *workspace)
	if err != nil {
		fmt.Fprintln(os.Stderr, "aisr:", err)
		return 1
	}
	fmt.Printf("Session: %s\n", rec.Name)
	return 0
}

func cmdSessionList(argv []string) int {
	mgr, _, err := buildManager()
	if err != nil {
		fmt.Fprintln(os.Stderr, "aisr:", err)
		return 1
	}
	recs, err := mgr.List()
	if err != nil {
		fmt.Fprintln(os.Stderr, "aisr:", err)
		return 1
	}
	if len(recs) == 0 {
		fmt.Println("(no sessions)")
		return 0
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tPROVIDER\tWORKSPACE\tRESUMABLE\tUPDATED")
	for _, r := range recs {
		resumable := "no"
		if r.ProviderSession != "" {
			resumable = "yes"
		}
		ws := r.Workspace
		if ws == "" {
			ws = "-"
		}
		updated := "-"
		if !r.UpdatedAt.IsZero() {
			updated = r.UpdatedAt.Format("2006-01-02 15:04")
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", r.Name, r.Provider, ws, resumable, updated)
	}
	if err := tw.Flush(); err != nil {
		fmt.Fprintln(os.Stderr, "aisr:", err)
		return 1
	}
	return 0
}

func cmdSessionRemove(argv []string) int {
	if len(argv) < 1 {
		fmt.Fprintln(os.Stderr, "usage: aisr session remove <name>")
		return 2
	}
	name := argv[0]
	mgr, _, err := buildManager()
	if err != nil {
		fmt.Fprintln(os.Stderr, "aisr:", err)
		return 1
	}
	if err := mgr.Remove(name); err != nil {
		if errors.Is(err, session.ErrNotFound) {
			fmt.Fprintf(os.Stderr, "aisr: no such session %q\n", name)
			return 1
		}
		fmt.Fprintln(os.Stderr, "aisr:", err)
		return 1
	}
	fmt.Printf("Removed: %s\n", name)
	return 0
}

// --- serve (daemon) ---

func cmdServe(argv []string) int {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	socket := fs.String("socket", "", "unix socket path (default ~/.aisr/aisr.sock)")
	listen := fs.String("listen", "", "TCP address, e.g. 127.0.0.1:7878 (overrides --socket)")
	_ = fs.Parse(argv)

	// Install the signal handler before binding, so a SIGTERM that arrives the
	// instant the socket appears is caught (graceful shutdown), not fatal.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	mgr, reg, err := buildManager()
	if err != nil {
		fmt.Fprintln(os.Stderr, "aisr:", err)
		return 1
	}
	srv := api.NewServer(mgr, reg.List(), nil)

	ln, cleanup, err := listenFor(*listen, *socket)
	if err != nil {
		fmt.Fprintln(os.Stderr, "aisr:", err)
		return 1
	}
	defer cleanup()

	httpSrv := &http.Server{Handler: srv.Handler()}
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutCtx)
	}()

	fmt.Fprintf(os.Stderr, "aisr serve: listening on %s\n", ln.Addr())
	if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintln(os.Stderr, "aisr:", err)
		return 1
	}
	return 0
}

// listenFor opens a TCP or Unix listener and returns a cleanup func.
func listenFor(tcpAddr, socketPath string) (net.Listener, func(), error) {
	if tcpAddr != "" {
		ln, err := net.Listen("tcp", tcpAddr)
		return ln, func() {}, err
	}
	path := socketPath
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, nil, err
		}
		path = filepath.Join(home, ".aisr", "aisr.sock")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, nil, err
	}
	// Clean up a stale socket left by a previous unclean exit.
	if fi, err := os.Stat(path); err == nil && fi.Mode()&os.ModeSocket != 0 {
		_ = os.Remove(path)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, nil, err
	}
	_ = os.Chmod(path, 0o600)
	return ln, func() { _ = os.Remove(path) }, nil
}

// --- wiring ---

// buildManager constructs the shared Manager (over the default file store) and
// the provider registry. The CLI and the daemon both build it this way.
func buildManager() (*session.Manager, *provider.Registry, error) {
	dir, err := storage.DefaultDir()
	if err != nil {
		return nil, nil, err
	}
	store, err := storage.New(dir)
	if err != nil {
		return nil, nil, err
	}
	reg := provider.NewRegistry(claude.New(), cursor.New())
	return session.NewManager(store, reg.Resolve), reg, nil
}
