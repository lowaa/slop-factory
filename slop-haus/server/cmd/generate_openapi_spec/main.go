// Command generate_openapi_spec writes slop-haus's real route registration
// (internal/apiserver) as an OpenAPI 3.1 JSON document, so it can never
// describe an API surface the server doesn't actually serve. No server is
// started and no database is touched -- only operation/schema metadata is
// needed.
//
// The frontend's TypeScript types are generated from this file's output by
// openapi-typescript, a separate (Node) step -- see
// frontend/generate_api_client.sh. This command only owns the Go side:
// producing the spec itself.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/apiserver"
)

func main() {
	out := flag.String("o", "", "output file path for the generated OpenAPI JSON document")
	check := flag.Bool("check", false, "verify -o already matches the generated output instead of writing it")
	flag.Parse()

	if *out == "" {
		fmt.Fprintln(os.Stderr, "generate_openapi_spec: -o is required")
		os.Exit(1)
	}

	spec, err := generate()
	if err != nil {
		fmt.Fprintln(os.Stderr, "generate_openapi_spec:", err)
		os.Exit(1)
	}

	if *check {
		existing, err := os.ReadFile(*out)
		if err != nil || string(existing) != string(spec) {
			fmt.Fprintln(os.Stderr, "generate_openapi_spec: openapi.json is out of date or unreadable")
			fmt.Fprintln(os.Stderr, "run slop-haus/frontend/generate_api_client.sh and commit the result")
			os.Exit(1)
		}
		return
	}

	if err := os.WriteFile(*out, spec, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "generate_openapi_spec:", err)
		os.Exit(1)
	}
}

// generate marshals apiserver's OpenAPI document as indented JSON (for
// readable diffs in code review), with a trailing newline so committing it
// doesn't trip a "no newline at end of file" warning.
func generate() ([]byte, error) {
	spec, err := json.MarshalIndent(apiserver.BuildOpenAPI(), "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal openapi spec: %w", err)
	}
	return append(spec, '\n'), nil
}
