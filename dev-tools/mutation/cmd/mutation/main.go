package main

import "os"

func main() {
	os.Exit((&App{Out: os.Stdout, Err: os.Stderr}).Run(os.Args[1:]))
}
