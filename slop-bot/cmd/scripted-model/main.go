// Command scripted-model serves the scripted model (internal/scriptedmodel)
// for harnesses in another process or container. Tests drive it through its
// /_scripted control endpoints.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/lowaa/slop-factory/slop-bot/internal/scriptedmodel"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	var err error
	if len(os.Args) > 1 && os.Args[1] == "ctl" {
		err = runCtl(ctx, os.Args[2:], os.Stdout)
	} else {
		err = run(ctx, os.Args[1:], os.Stderr)
	}
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "scripted-model:", err)
		os.Exit(1)
	}
}

// run serves until ctx ends.
func run(ctx context.Context, args []string, logs io.Writer) error {
	flags := flag.NewFlagSet("scripted-model", flag.ContinueOnError)
	flags.SetOutput(logs)
	listen := flags.String("listen", "127.0.0.1:8000", "the address to listen on")
	scenarioPath := flags.String("scenario", "", "a scenario JSON file to load at start (optional)")
	gateTimeout := flags.Duration("gate-timeout", scriptedmodel.DefaultGateTimeout, "how long a turn waits at an unopened gate")
	models := flags.String("models", strings.Join(scriptedmodel.DefaultModels, ","), "comma-separated model ids the model lists serve")
	if err := flags.Parse(args); err != nil {
		return err
	}
	scenario, err := readScenario(*scenarioPath)
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewTextHandler(logs, nil))
	server, err := scriptedmodel.New(&scriptedmodel.Options{
		Addr:        *listen,
		Logger:      logger,
		GateTimeout: *gateTimeout,
		Models:      strings.Split(*models, ","),
	})
	if err != nil {
		return err
	}
	if scenario != nil {
		if err := server.Load(scenario); err != nil {
			return errors.Join(err, server.Close())
		}
	}
	logger.Info("serving", "url", server.URL())
	<-ctx.Done()
	return server.Close()
}

func readScenario(path string) (*scriptedmodel.Scenario, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path) // #nosec G304 -- the operator names the file
	if err != nil {
		return nil, err
	}
	scenario, err := scriptedmodel.ParseScenario(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return scenario, nil
}
