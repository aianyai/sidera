package adapterkit

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/aianyai/sidera/sdk/go/termout"
)

// Exit codes of an adapter program. They are the codes of the `sidera` program
// that mean the same: 0 done, 1 an ordinary failure, 2 a usage error and 10 an
// outcome that is not known yet. A program that has to say more takes the code
// from the table of the command-line reference and does not invent one.
const (
	ExitOK             = 0
	ExitFailed         = 1
	ExitUsage          = 2
	ExitUnknownOutcome = 10
)

// Streams are the standard streams of an adapter program, given by its process.
type Streams struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	// Getenv reads the environment variables that decide whether the output is
	// decorated and colored (NO_COLOR and the like). A program passes os.Getenv; nil
	// reads nothing, which leaves the output plain unless it is written to a terminal.
	Getenv func(string) string
}

// Out is Stdout as a stream of human-readable output: whether it is decorated, colored
// and how wide it is are decided once from what it is and from Getenv.
func (s Streams) Out() *termout.Stream { return s.open(s.Stdout) }

// Err is Stderr as a stream of human-readable output; see [Streams.Out]. The two are
// decided separately, so a terminal on one and a file on the other each get their own.
func (s Streams) Err() *termout.Stream { return s.open(s.Stderr) }

func (s Streams) open(w io.Writer) *termout.Stream {
	getenv := s.Getenv
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	return termout.Open(w, termout.ColorAuto, getenv)
}

// BuildInfo is what a program was built from, as `version` reports it. The value
// is written as JSON by `version --json`, so its members are the report; the
// release type every Sidera program uses has the members `sidera version --json`
// writes.
type BuildInfo interface {
	// Describe is the one line `version` prints for the program called program.
	Describe(program string) string
}

// PackageWriter writes the adapter package of a program for one platform from a
// binary built for it into the directory out, which is empty or does not exist.
type PackageWriter func(goos, goarch, binary, out string) error

// Command is a command of a program that the kit does not provide. args are the
// arguments after the command's name.
type Command func(args []string, s Streams) int

// Program is the command line of an adapter program: the commands every adapter
// program has the same way, which the kit runs, and the commands of its own.
//
// Of the commands the kit runs, `serve` is the adapter role, the one the daemon
// starts (the manifest's entry says so): a program run with nothing to do must
// not wait on its input as if the daemon had started it, so nothing serves unless
// `serve` is named, and `serve` takes no argument. `package` and `version [--json]`
// are written once, with one output and one usage error for a missing flag or an
// extra argument. `-h`, `--help` and `help` print the usage on standard output and
// are done (0), and so does `--help` after `package` or `version`; no argument and
// an unknown command are usage errors (2). The conventions are in the command-line
// reference.
//
// The commands of a program's own go in Commands: the console's client commands,
// and a `setup` that reads a request on Streams.Stdin and writes its result on
// Streams.Stdout. A name the kit runs cannot be one of them.
type Program struct {
	// Name is the program as its messages call it, such as "sidera-adapter-claude".
	Name string
	// Help describes the commands of the program's own; the kit adds the ones it runs.
	// It is what help prints.
	Help Help
	// Serve is the adapter role; its result is the exit code.
	Serve func(s Streams) int
	// Package writes the program's adapter package.
	Package PackageWriter
	// Build is what `version` reports.
	Build BuildInfo
	// Commands are the program's own commands by name.
	Commands map[string]Command
}

// kitCommands are the names Run answers itself.
var kitCommands = []string{"-h", "--help", "help", "serve", "package", "version"}

// Run runs the command line args, the arguments after the program's name, and
// returns the exit code. It panics when Commands names a command the kit runs.
func (p Program) Run(args []string, s Streams) int {
	for _, name := range kitCommands {
		if _, clash := p.Commands[name]; clash {
			panic("adapterkit: the command " + name + " of " + p.Name + " is one the kit runs")
		}
	}
	if len(args) == 0 {
		fmt.Fprint(s.Stderr, p.Text(s.Err()))
		return ExitUsage
	}
	command, rest := args[0], args[1:]
	switch command {
	case "-h", "--help", "help":
		fmt.Fprint(s.Stdout, p.Text(s.Out()))
		return ExitOK
	case "serve":
		if len(rest) > 0 {
			fmt.Fprintf(s.Stderr, "%s: serve takes no arguments\n", p.Name)
			return ExitUsage
		}
		return p.Serve(s)
	case "package":
		return RunPackage(p.Name, rest, p.Package, s)
	case "version":
		return RunVersion(p.Name, rest, p.Build, s)
	}
	if run, ok := p.Commands[command]; ok {
		return run(rest, s)
	}
	fmt.Fprintf(s.Stderr, "%s: unknown command %q\n\n%s", p.Name, command, p.Text(s.Err()))
	return ExitUsage
}

// RunPackage is the `package` command of an adapter program: it writes the adapter
// package for one platform from a binary built for it, ready for `sidera adapter
// install`. args are the arguments after `package`. A missing flag, an unknown flag
// or a positional argument is a usage error (2); a directory that is not empty, or
// a package that cannot be written, is an ordinary failure (1). On success it prints
// the directory and platform written; `--help` prints the usage and is done (0).
func RunPackage(program string, args []string, write PackageWriter, s Streams) int {
	fs := flag.NewFlagSet("package", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	// The description of a flag is its usage text, written once: the help lists the flags from it,
	// and the back-quoted word is the placeholder of the value.
	goos := fs.String("goos", "", "The `os` the binary was built for (GOOS).")
	goarch := fs.String("goarch", "", "The CPU `arch` the binary was built for (GOARCH).")
	binary := fs.String("binary", "", "The `file` that holds the binary built for that platform.")
	out := fs.String("out", "", "The `directory` to write the package into; it must be new or empty.")
	err := fs.Parse(args)
	if errors.Is(err, flag.ErrHelp) {
		return printHelp(Help{
			Usage:  program + " package --goos <os> --goarch <arch> --binary <file> --out <directory>",
			Groups: []HelpGroup{{Title: "Flags", Entries: flagEntries(fs, "goos", "goarch", "binary", "out")}},
		}, s)
	}
	if err != nil || fs.NArg() != 0 || *goos == "" || *goarch == "" || *binary == "" || *out == "" {
		fmt.Fprintf(s.Stderr, "%s: package needs --goos, --goarch, --binary and --out, and no other argument\n", program)
		return ExitUsage
	}
	// The package is written beside whatever is in the directory, so a directory
	// that holds something is refused rather than mixed with an older package.
	if entries, err := os.ReadDir(*out); err == nil && len(entries) > 0 {
		fmt.Fprintf(s.Stderr, "%s: %s is not empty\n", program, *out)
		return ExitFailed
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(s.Stderr, "%s: %v\n", program, err)
		return ExitFailed
	}
	if err := write(*goos, *goarch, *binary, *out); err != nil {
		fmt.Fprintf(s.Stderr, "%s: %v\n", program, err)
		return ExitFailed
	}
	term := s.Out()
	fmt.Fprint(term, term.OK("Wrote the adapter package.\n"+term.Block([]termout.Field{
		{Label: "Platform", Value: *goos + "/" + *goarch},
		{Label: "Directory", Value: *out},
	})))
	return ExitOK
}

// flagEntries lists the named flags of fs as help entries, in the order given: the name
// with the placeholder of its value, and its usage text as the purpose.
func flagEntries(fs *flag.FlagSet, names ...string) []HelpEntry {
	entries := make([]HelpEntry, len(names))
	for i, name := range names {
		placeholder, usage := flag.UnquoteUsage(fs.Lookup(name))
		entries[i] = HelpEntry{Name: "--" + name + " <" + placeholder + ">", Purpose: usage}
	}
	return entries
}

// RunVersion is the `version` command of an adapter program: the one line that
// names the program, its version, the commit and the Go version, or with `--json`
// the object info is encoded as. args are the arguments after `version`; anything
// but `--json` is a usage error (2), and `--help` prints the usage and is done (0).
func RunVersion(program string, args []string, info BuildInfo, s Streams) int {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	asJSON := fs.Bool("json", false, "write the report as JSON")
	err := fs.Parse(args)
	if errors.Is(err, flag.ErrHelp) {
		return printHelp(Help{
			Usage: program + " version [--json]",
			Groups: []HelpGroup{{Title: "Flags", Entries: []HelpEntry{
				{Name: "--json", Purpose: "Write the report as JSON."},
			}}},
		}, s)
	}
	if err != nil || fs.NArg() != 0 {
		fmt.Fprintf(s.Stderr, "%s: version takes no argument but --json\n", program)
		return ExitUsage
	}
	if !*asJSON {
		fmt.Fprintln(s.Stdout, info.Describe(program))
		return ExitOK
	}
	enc := json.NewEncoder(s.Stdout)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(info); err != nil {
		fmt.Fprintf(s.Stderr, "%s: %v\n", program, err)
		return ExitFailed
	}
	return ExitOK
}

// printHelp answers `--help` of a command: its usage and its flags, on standard output.
func printHelp(h Help, s Streams) int {
	fmt.Fprint(s.Stdout, h.render(s.Out()))
	return ExitOK
}
