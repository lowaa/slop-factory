// Command genprices regenerates pricing/prices_gen.go from OpenRouter's
// model list. Run it with `go generate ./pricing` and commit the result:
// each price change is then a reviewed diff, and no server needs network
// access to price usage.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/lowaa/slop-factory/go-llm-usage/internal/pricegen"
)

const modelsURL = "https://openrouter.ai/api/v1/models"

func main() {
	out := flag.String("out", "prices_gen.go", "file to write")
	in := flag.String("in", "", "read a saved models response from this file instead of fetching -url")
	url := flag.String("url", modelsURL, "where to fetch the models response from")
	flag.Parse()
	if err := run(&source{file: *in, url: *url}, *out); err != nil {
		fmt.Fprintln(os.Stderr, "genprices:", err)
		os.Exit(1)
	}
}

// source is where the models response comes from: a saved file if set,
// else url.
type source struct {
	file string
	url  string
}

func run(src *source, out string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	body, err := src.open(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = body.Close() }()
	entries, err := pricegen.Parse(body)
	if err != nil {
		return err
	}
	gen, err := pricegen.Render(entries, time.Now())
	if err != nil {
		return err
	}
	return os.WriteFile(out, gen, 0o644)
}

func (s *source) open(ctx context.Context) (io.ReadCloser, error) {
	if s.file != "" {
		return os.Open(s.file)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch models: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("fetch models: %s", resp.Status)
	}
	return resp.Body, nil
}
