// Command fakeharness is the fake harness slop-bot's end-to-end tests run in
// place of a real one.
package main

import (
	"os"

	"github.com/lowaa/slop-factory/slop-bot/internal/harness/fake"
)

func main() {
	os.Exit(fake.Main(os.Args[1:], os.Stdin, os.Stdout, os.Getenv))
}
