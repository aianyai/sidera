// Command sidera-adapter-console is the console bridge adapter and its client in
// one program: `serve` is the adapter the daemon starts, every other command is
// the client the node's owner runs in a terminal. See internal/console.
package main

import (
	"os"

	"github.com/aianyai/sidera/cmd/sidera-adapter-console/internal/console"
)

func main() {
	os.Exit(console.Main(os.Args[1:], console.Env{
		Getenv: os.Getenv,
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	}))
}
