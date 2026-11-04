// Command sidera-adapter-hermes is the Hermes adapter: it makes a Hermes Agent
// Kanban board a lane to other nodes. `serve` is the adapter the daemon starts;
// `package` and `version` are for the person who builds and installs it, and `setup`
// prepares the Hermes it serves (the Skill and the steps). See internal/adapter.
package main

import (
	"os"

	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/adapter"
)

func main() {
	os.Exit(adapter.Main(os.Args[1:], adapter.ProgramEnv{
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	}))
}
