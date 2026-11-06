package adapterkit_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aianyai/sidera/sdk/go/adapterkit"
)

type build struct {
	Version string `json:"version"`
	Commit  string `json:"commit,omitempty"`
}

func (b build) Describe(program string) string { return program + " " + b.Version }

func run(f func(adapterkit.Streams) int) (code int, stdout, stderr string) {
	var out, errOut bytes.Buffer
	code = f(adapterkit.Streams{Stdout: &out, Stderr: &errOut})
	return code, out.String(), errOut.String()
}

// testProgram is a program whose serve, package and own command say that they ran.
func testProgram() adapterkit.Program {
	return adapterkit.Program{
		Name:  "prog",
		Usage: "usage text\n",
		Serve: func(s adapterkit.Streams) int {
			io.WriteString(s.Stdout, "served\n")
			return 7
		},
		Package: func(string, string, string, string) error { return nil },
		Build:   build{Version: "1.2.3"},
		Commands: map[string]adapterkit.Command{
			"own": func(args []string, s adapterkit.Streams) int {
				fmt.Fprintf(s.Stdout, "own %v\n", args)
				return 5
			},
		},
	}
}

// A program run by a person with nothing to do, or with a command it does not
// have, says so and ends: only the name `serve`, alone, reaches the adapter role,
// which would wait on its input.
func TestProgramRun(t *testing.T) {
	const usage = "usage text\n"
	for _, c := range []struct {
		args   []string
		code   int
		stdout string
		stderr string
	}{
		{nil, adapterkit.ExitUsage, "", usage},
		{[]string{"-h"}, adapterkit.ExitOK, usage, ""},
		{[]string{"--help"}, adapterkit.ExitOK, usage, ""},
		{[]string{"help"}, adapterkit.ExitOK, usage, ""},
		{[]string{"serve"}, 7, "served\n", ""},
		{[]string{"serve", "--help"}, adapterkit.ExitUsage, "", "prog: serve takes no arguments\n"},
		{[]string{"serve", "extra"}, adapterkit.ExitUsage, "", "prog: serve takes no arguments\n"},
		{[]string{"bogus"}, adapterkit.ExitUsage, "", "prog: unknown command \"bogus\"\n" + usage},
		{[]string{"--bogus"}, adapterkit.ExitUsage, "", "prog: unknown command \"--bogus\"\n" + usage},
		{[]string{"own", "a", "--b"}, 5, "own [a --b]\n", ""},
		{[]string{"version"}, 0, "prog 1.2.3\n", ""},
	} {
		code, stdout, stderr := run(func(s adapterkit.Streams) int { return testProgram().Run(c.args, s) })
		if code != c.code || stdout != c.stdout || stderr != c.stderr {
			t.Errorf("%v: code %d, stdout %q, stderr %q; want %d, %q, %q", c.args, code, stdout, stderr, c.code, c.stdout, c.stderr)
		}
	}
}

// A command of a program's own cannot take the place of one the kit runs.
func TestProgramRefusesAnOwnCommandTheKitRuns(t *testing.T) {
	for _, name := range []string{"serve", "package", "version", "help", "--help", "-h"} {
		p := testProgram()
		p.Commands[name] = func([]string, adapterkit.Streams) int { return 0 }
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: no panic", name)
				}
			}()
			p.Run([]string{"own"}, adapterkit.Streams{Stdout: io.Discard, Stderr: io.Discard})
		}()
	}
}

// Every command with a usage of its own answers --help with it, on standard output.
func TestHelpOfACommandIsNotAnError(t *testing.T) {
	for _, command := range []string{"package", "version"} {
		code, stdout, stderr := run(func(s adapterkit.Streams) int { return testProgram().Run([]string{command, "--help"}, s) })
		if code != adapterkit.ExitOK || !strings.HasPrefix(stdout, "usage: prog "+command) || stderr != "" {
			t.Errorf("%s --help: code %d, stdout %q, stderr %q", command, code, stdout, stderr)
		}
	}
}

func TestRunVersion(t *testing.T) {
	info := build{Version: "1.2.3", Commit: "abc"}
	for _, c := range []struct {
		args   []string
		code   int
		stdout string
	}{
		{nil, 0, "prog 1.2.3\n"},
		{[]string{"--json"}, 0, `{"version":"1.2.3","commit":"abc"}` + "\n"},
		{[]string{"--json=false"}, 0, "prog 1.2.3\n"},
		{[]string{"extra"}, adapterkit.ExitUsage, ""},
		{[]string{"--json", "extra"}, adapterkit.ExitUsage, ""},
		{[]string{"--unknown"}, adapterkit.ExitUsage, ""},
	} {
		code, stdout, stderr := run(func(s adapterkit.Streams) int { return adapterkit.RunVersion("prog", c.args, info, s) })
		if code != c.code || stdout != c.stdout {
			t.Errorf("version %v: code %d, stdout %q (stderr %q); want %d, %q", c.args, code, stdout, stderr, c.code, c.stdout)
		}
		if (c.code != 0) != (stderr != "") {
			t.Errorf("version %v: stderr %q", c.args, stderr)
		}
	}
}

func TestRunPackage(t *testing.T) {
	dir := t.TempDir()
	var written [][4]string
	write := func(goos, goarch, binary, out string) error {
		written = append(written, [4]string{goos, goarch, binary, out})
		return nil
	}
	pkg := func(args ...string) (int, string, string) {
		return run(func(s adapterkit.Streams) int { return adapterkit.RunPackage("prog", args, write, s) })
	}
	full := func(out string) []string {
		return []string{"--goos", "linux", "--goarch", "amd64", "--binary", "bin", "--out", out}
	}

	out := filepath.Join(dir, "new")
	if code, stdout, stderr := pkg(full(out)...); code != 0 || stdout != "package: "+out+"\n" || stderr != "" {
		t.Fatalf("code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if len(written) != 1 || written[0] != [4]string{"linux", "amd64", "bin", out} {
		t.Fatalf("written %v", written)
	}

	// A usage error writes nothing: every flag is needed and nothing else is taken.
	for name, args := range map[string][]string{
		"no arguments":    nil,
		"missing --out":   {"--goos", "linux", "--goarch", "amd64", "--binary", "bin"},
		"empty --binary":  {"--goos", "linux", "--goarch", "amd64", "--binary", "", "--out", out},
		"positional":      append(full(out), "extra"),
		"unknown flag":    append(full(out), "--nope"),
		"flag needs text": {"--goos"},
	} {
		written = nil
		if code, stdout, stderr := pkg(args...); code != adapterkit.ExitUsage || stdout != "" || !strings.Contains(stderr, "package needs") || len(written) != 0 {
			t.Errorf("%s: code %d, stdout %q, stderr %q, written %v", name, code, stdout, stderr, written)
		}
	}

	// A directory that holds something is refused; an empty one is fine.
	if err := os.WriteFile(filepath.Join(dir, "old"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	written = nil
	if code, _, stderr := pkg(full(dir)...); code != adapterkit.ExitFailed || !strings.Contains(stderr, "is not empty") || len(written) != 0 {
		t.Errorf("non-empty directory: code %d, stderr %q, written %v", code, stderr, written)
	}
	empty := t.TempDir()
	if code, _, stderr := pkg(full(empty)...); code != 0 || stderr != "" {
		t.Errorf("empty directory: code %d, stderr %q", code, stderr)
	}

	// A writer that fails is an ordinary failure and prints no success line.
	failing := func(string, string, string, string) error { return errors.New("no such binary") }
	code, stdout, stderr := run(func(s adapterkit.Streams) int {
		return adapterkit.RunPackage("prog", full(filepath.Join(dir, "other")), failing, s)
	})
	if code != adapterkit.ExitFailed || stdout != "" || stderr != "prog: no such binary\n" {
		t.Errorf("failing writer: code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}
