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
	if !strings.Contains(usageText, "\n  setup ") {
		t.Error("the usage does not list setup")
	}
}
