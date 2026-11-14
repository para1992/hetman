// Command hetman-stub-adapter is an executor adapter that runs no model. It
// does what a plan file tells it, so that flows with agent nodes can be
// tried without any real client. See package stubadapter for the plan
// format.
package main

import (
	"os"

	"hetman/internal/stubadapter"
)

func main() {
	os.Exit(stubadapter.Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
