// Command slop-bot is the daemon, the CLI that talks to it, and the TUI.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/lowaa/slop-factory/slop-bot/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	deps, err := cli.FromOS()
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "slop-bot:", err)
		stop()
		os.Exit(1)
	}
	code := cli.Execute(ctx, deps, os.Args[1:])
	stop()
	os.Exit(code)
}
