// Command quality measures a Go module and ratchets the numbers against
// its quality_baseline/. quality.sh, through quality_lib.sh, is how it's run;
// see dev-tools/quality/README.md.
package main

import "os"

func main() {
	app := &App{Out: os.Stdout, Err: os.Stderr}
	os.Exit(app.Run(os.Args[1:]))
}
