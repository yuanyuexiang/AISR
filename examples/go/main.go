// Command go-demo is a minimal example of the AISR Go SDK.
//
// Prerequisite: the daemon is running (`aisr serve`).
//
//	go run ./examples/go -session demo -- "用一句话回答:1+1等于几"
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/yuanyuexiang/aisr/pkg/sdk"
)

func main() {
	session := flag.String("session", "go-demo", "session name")
	model := flag.String("model", "", "model override")
	flag.Parse()

	prompt := strings.TrimSpace(strings.Join(flag.Args(), " "))
	if prompt == "" {
		prompt = "用一句话介绍你自己"
	}

	c := sdk.New() // default: ~/.aisr/aisr.sock
	ctx := context.Background()

	if ps, err := c.Providers(ctx); err == nil {
		names := make([]string, 0, len(ps))
		for _, p := range ps {
			names = append(names, p.Name)
		}
		fmt.Fprintf(os.Stderr, "providers: %s\n", strings.Join(names, ", "))
	}

	events, err := c.Send(ctx, *session, prompt, sdk.SendOptions{Model: *model})
	if err != nil {
		fmt.Fprintln(os.Stderr, "send:", err)
		os.Exit(1)
	}
	for ev := range events {
		switch ev.Kind {
		case sdk.EventText:
			fmt.Print(ev.Text)
		case sdk.EventError:
			fmt.Fprintln(os.Stderr, "\n[error]", ev.Text)
		}
	}
	fmt.Println()

	if s, err := c.GetSession(ctx, *session); err == nil {
		fmt.Fprintf(os.Stderr, "session %q -> provider session %s\n", s.Name, s.ProviderSession)
	}
}
