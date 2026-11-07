package cli

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/aianyai/sidera/sdk/go/termout"
)

// plain is a stream that writes to w with no decoration and no color, as a pipe gets.
func plain(w io.Writer) *termout.Stream { return termout.New(w, false, false, 80) }

// commandOn is a command whose standard output is out, for the tests of the layouts that
// a command writes.
func commandOn(out io.Writer) *command {
	return &command{Config: Config{Stdout: nopWriteCloser{out}}, out: plain(out)}
}

// nopWriteCloser is a standard output the tests can read and that closes to nothing.
// It is in no platform's file: the tests of every platform use it.
type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// runCLI runs `sidera <args>` with the given environment and returns its exit
// code, standard output and standard error.
func runCLI(t *testing.T, env map[string]string, args ...string) (int, string, string) {
	t.Helper()
	return runCLIAs(t, nil, env, args...)
}

// runCLIAs is runCLI for a program whose path is exe (os.Executable); nil is a
// program whose path the test does not use.
func runCLIAs(t *testing.T, exe func() (string, error), env map[string]string, args ...string) (int, string, string) {
	t.Helper()
	var out, errs bytes.Buffer
	code := Run(context.Background(), append([]string{"sidera"}, args...), Config{
		Stdin: io.NopCloser(strings.NewReader("")), Stdout: nopWriteCloser{&out}, Stderr: &errs,
		Getenv: func(name string) string { return env[name] }, Executable: exe,
	})
	return code, out.String(), errs.String()
}
