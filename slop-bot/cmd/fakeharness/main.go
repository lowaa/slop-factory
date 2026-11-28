// Command fakeharness is the fake harness slop-bot's end-to-end tests run in
// place of a real one.
package main

import (
	"io"
	"os"
	"strings"

	"github.com/lowaa/slop-factory/slop-bot/internal/harness/fake"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Getenv))
}

// run is main without the process globals, so a test can drive it. A nil
// stdin or getenv means an empty one, so a caller that has neither isn't a
// panic inside the fake.
func run(args []string, stdin io.Reader, stdout io.Writer, getenv func(string) string) int {
	if stdin == nil {
		stdin = strings.NewReader("")
	}
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	return fake.Main(args, stdin, stdout, getenv)
}
