// Command aisr is the AISR CLI — a thin client over session.Manager.
//
// Implemented: `aisr ask` (driving the Claude provider end to end) and
// `aisr session create|list|remove`. The orchestration lives in the shared
// session.Manager so the upcoming daemon reuses it (see 技术方案.md V1).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"

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

	mgr, err := openManager()
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

	mgr, err := openManager()
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
	mgr, err := openManager()
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
	mgr, err := openManager()
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

// --- wiring ---

// openManager builds the shared Manager over the default file store and the
// provider resolver. The daemon will construct its Manager the same way.
func openManager() (*session.Manager, error) {
	dir, err := storage.DefaultDir()
	if err != nil {
		return nil, err
	}
	store, err := storage.New(dir)
	if err != nil {
		return nil, err
	}
	return session.NewManager(store, pickProvider), nil
}

// pickProvider is the ProviderResolver (the Router): name -> implementation.
func pickProvider(name string) (provider.Provider, error) {
	switch name {
	case "claude":
		return claude.New(), nil
	default:
		return nil, fmt.Errorf("unsupported provider %q (only \"claude\" in this slice)", name)
	}
}
