// Command aisr is the AISR CLI.
//
// Implemented so far: `aisr ask` (driving the Claude provider end to end) and
// `aisr session create|list|remove` (persisted session management). daemon /
// HTTP API / Go SDK come next (see 技术方案.md V1).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/yuanyuexiang/aisr/internal/provider"
	"github.com/yuanyuexiang/aisr/internal/provider/claude"
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

	var (
		st      *storage.Store
		rec     *session.Session
		managed = *sessName != ""
		prv     provider.Provider
		opts    provider.SessionOpts
	)

	if managed {
		if err := session.ValidateName(*sessName); err != nil {
			fmt.Fprintln(os.Stderr, "aisr:", err)
			return 2
		}
		var err error
		if st, err = openStore(); err != nil {
			fmt.Fprintln(os.Stderr, "aisr:", err)
			return 1
		}
		rec, err = st.Load(*sessName)
		if errors.Is(err, storage.ErrNotFound) {
			rec = &session.Session{ // lazy-create on first use
				Name:      *sessName,
				Provider:  *provName,
				Workspace: absPath(*workspace),
				CreatedAt: time.Now(),
			}
		} else if err != nil {
			fmt.Fprintln(os.Stderr, "aisr:", err)
			return 1
		}
		if prv, err = pickProvider(rec.Provider); err != nil {
			fmt.Fprintln(os.Stderr, "aisr:", err)
			return 2
		}
		opts = provider.SessionOpts{SessionID: rec.ProviderSession, Workspace: rec.Workspace, Model: *model}
	} else {
		var err error
		if prv, err = pickProvider(*provName); err != nil {
			fmt.Fprintln(os.Stderr, "aisr:", err)
			return 2
		}
		opts = provider.SessionOpts{Workspace: absPath(*workspace), Model: *model}
	}

	if err := checkWorkspace(opts.Workspace); err != nil {
		fmt.Fprintln(os.Stderr, "aisr:", err)
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	turn, err := prv.Send(ctx, opts, prompt)
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

	// turn.SessionID is valid only now that the stream is fully drained.
	switch {
	case managed && turn.SessionID != "":
		rec.ProviderSession = turn.SessionID
		rec.UpdatedAt = time.Now()
		if err := st.Save(rec); err != nil {
			fmt.Fprintln(os.Stderr, "aisr: warning: could not save session:", err)
		}
		fmt.Fprintf(os.Stderr, "session: %s (%s)\n", rec.Name, rec.ProviderSession)
	case turn.SessionID != "":
		fmt.Fprintf(os.Stderr, "session: %s\n", turn.SessionID)
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

	if _, err := pickProvider(*provName); err != nil {
		fmt.Fprintln(os.Stderr, "aisr:", err)
		return 2
	}
	sname := *name
	if sname == "" {
		sname = session.GenName(*provName)
	}
	if err := session.ValidateName(sname); err != nil {
		fmt.Fprintln(os.Stderr, "aisr:", err)
		return 2
	}
	if err := checkWorkspace(absPath(*workspace)); err != nil {
		fmt.Fprintln(os.Stderr, "aisr:", err)
		return 2
	}
	st, err := openStore()
	if err != nil {
		fmt.Fprintln(os.Stderr, "aisr:", err)
		return 1
	}
	if st.Exists(sname) {
		fmt.Fprintf(os.Stderr, "aisr: session %q already exists\n", sname)
		return 1
	}
	now := time.Now()
	rec := &session.Session{
		Name: sname, Provider: *provName, Workspace: absPath(*workspace),
		CreatedAt: now, UpdatedAt: now,
	}
	if err := st.Save(rec); err != nil {
		fmt.Fprintln(os.Stderr, "aisr:", err)
		return 1
	}
	fmt.Printf("Session: %s\n", sname)
	return 0
}

func cmdSessionList(argv []string) int {
	st, err := openStore()
	if err != nil {
		fmt.Fprintln(os.Stderr, "aisr:", err)
		return 1
	}
	recs, err := st.List()
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
	st, err := openStore()
	if err != nil {
		fmt.Fprintln(os.Stderr, "aisr:", err)
		return 1
	}
	if err := st.Remove(name); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			fmt.Fprintf(os.Stderr, "aisr: no such session %q\n", name)
			return 1
		}
		fmt.Fprintln(os.Stderr, "aisr:", err)
		return 1
	}
	fmt.Printf("Removed: %s\n", name)
	return 0
}

// --- helpers ---

func openStore() (*storage.Store, error) {
	dir, err := storage.DefaultDir()
	if err != nil {
		return nil, err
	}
	return storage.New(dir)
}

func absPath(p string) string {
	if p == "" {
		return ""
	}
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}

// checkWorkspace validates that a non-empty workspace path is an existing dir,
// turning the exec-layer chdir failure into a clear up-front error.
func checkWorkspace(path string) error {
	if path == "" {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("workspace does not exist: %s", path)
		}
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("workspace is not a directory: %s", path)
	}
	return nil
}

func pickProvider(name string) (provider.Provider, error) {
	switch name {
	case "claude":
		return claude.New(), nil
	default:
		return nil, fmt.Errorf("unsupported provider %q (only \"claude\" in this slice)", name)
	}
}
