// Command slop-bot is the daemon, the CLI that talks to it, and the TUI.
package main

import (
	"fmt"
	"os"

	"github.com/lowaa/slop-factory/slop-bot/internal/version"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "version" {
		fmt.Println(version.String())
		return
	}
	fmt.Fprintln(os.Stderr, "usage: slop-bot version")
	os.Exit(2)
}
