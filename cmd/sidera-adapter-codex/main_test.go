package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/aianyai/sidera/sdk/go/adapterkit"
)

// The manifest names `setup` as the program's command, so the program must have it.
func TestTheProgramHasTheSetupCommandTheManifestNames(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := program.Run([]string{"setup"}, adapterkit.Streams{Stdin: strings.NewReader("not a request"), Stdout: &stdout, Stderr: &stderr})
	if code != adapterkit.ExitUsage || !strings.Contains(stderr.String(), "setup:") || strings.Contains(stderr.String(), "unknown command") {
		t.Errorf("exit %d, stderr %q", code, stderr.String())
	}
	stdout.Reset()
	if program.Run([]string{"help"}, adapterkit.Streams{Stdout: &stdout, Stderr: &stderr}); !strings.Contains(stdout.String(), "\n  setup ") {
		t.Errorf("the help does not list setup: %q", stdout.String())
	}
}
