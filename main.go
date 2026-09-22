package main

import (
	"os"

	"github.com/HaikeiLabs/kei-cli/internal/app"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		app.PrintUsage(os.Stderr)
		os.Exit(2)
	}
	os.Exit(app.Main(version, os.Args[1], os.Args[2:], os.Stdout, os.Stderr, os.Stdin))
}
