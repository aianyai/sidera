package cli

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
)

// nopWriteCloser is a standard output the tests can read and that closes to nothing.
// It is in no platform's file: the tests of every platform use it.
type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// runCLI runs `sidera <args>` with the given environment and returns its exit
// code, standard output and standard error.
func runCLI(t *testing.T, env map[string]string, args ...string) (int, string, string) {
	t.Helper()
	var out, errs bytes.Buffer
	code := Run(context.Background(), append([]string{"sidera"}, args...), Config{
		Stdin: io.NopCloser(strings.NewReader("")), Stdout: nopWriteCloser{&out}, Stderr: &errs,
		Getenv: func(name string) string { return env[name] },
	})
	return code, out.String(), errs.String()
}
