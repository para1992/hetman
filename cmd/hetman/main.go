// Command hetman is a config-driven flow engine.
package main

import (
	"os"

	"hetman/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:], os.Stdout, os.Stderr))
}
