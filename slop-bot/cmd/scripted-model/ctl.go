package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/lowaa/slop-factory/slop-bot/internal/scriptedmodel"
)

const ctlUsage = "usage: scripted-model ctl [-url URL] load FILE | reset | release GATE | held GATE | requests [-conversation] | errors | remaining"

var errCtlUsage = errors.New(ctlUsage)

// runCtl drives a running server through its control endpoints, for a
// person poking at a tier-2 container or a shell script driving one. JSON
// results go to out.
func runCtl(ctx context.Context, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("scripted-model ctl", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	url := flags.String("url", "http://127.0.0.1:8000", "the server's base URL")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("%w: %w", errCtlUsage, err)
	}
	if flags.NArg() == 0 {
		return errCtlUsage
	}
	client := scriptedmodel.NewClient(*url, nil)
	command, rest := flags.Arg(0), flags.Args()[1:]
	switch command {
	case "load", "release", "held":
		if len(rest) != 1 {
			return errCtlUsage
		}
		return ctlWithArg(ctx, client, command, rest[0])
	case "reset":
		return client.Reset(ctx)
	case "requests":
		return ctlRequests(ctx, client, rest, out)
	case "errors":
		errs, err := client.Errors(ctx)
		return printJSON(out, errs, err)
	case "remaining":
		n, err := client.Remaining(ctx)
		return printJSON(out, map[string]int{"remaining": n}, err)
	default:
		return errCtlUsage
	}
}

func ctlWithArg(ctx context.Context, client *scriptedmodel.Client, command, arg string) error {
	switch command {
	case "load":
		scenario, err := readScenario(arg)
		if err != nil {
			return err
		}
		return client.Load(ctx, scenario)
	case "release":
		return client.Release(ctx, arg)
	default:
		return client.HeldGate(ctx, arg)
	}
}

func ctlRequests(ctx context.Context, client *scriptedmodel.Client, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("requests", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	conversation := flags.Bool("conversation", false, "leave out the harness's own housekeeping requests")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("%w: %w", errCtlUsage, err)
	}
	requests, err := client.Requests(ctx)
	if err == nil && *conversation {
		requests = (&scriptedmodel.Recording{Requests: requests}).Conversation()
	}
	return printJSON(out, requests, err)
}

func printJSON(out io.Writer, v any, err error) error {
	if err != nil {
		return err
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
