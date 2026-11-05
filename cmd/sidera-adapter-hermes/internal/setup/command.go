package setup

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/config"
	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/kanban"
	"github.com/aianyai/sidera/internal/datahome"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// Exit codes, as the console client's.
const (
	exitOK     = 0
	exitFailed = 1
	exitUsage  = 2
)

// DefaultInstance is the instance name `sidera adapter enable hermes` gives the first
// instance.
const DefaultInstance = "hermes"

// Usage is the text of `setup --help`.
const Usage = `usage: sidera-adapter-hermes setup --config <file|-> [--home <dir>] [--instance <name>]
                                   [--profile <name>]... [--platform <name>]... [--apply]

Writes the Sidera Skill for the instance's alias table, prints the steps that let
Hermes use it and says which toolsets (kanban, skills) a platform still lacks, with the
change to make by hand. With --apply it places the Skill in each profile, asking before
each. It changes no Hermes configuration and restarts no gateway.

  --config    the instance configuration, the document given to 'sidera adapter
              enable' or 'sidera adapter config' ('-' reads standard input and is
              refused with --apply, which asks its questions there)
  --home      the Sidera data directory (default: SIDERA_HOME, then the platform's)
  --instance  the instance name (default: hermes)
  --profile   a Hermes profile whose agents delegate; repeatable (default: home_profile)
  --platform  a platform whose agents delegate; repeatable (default: home_platform).
              cron is always included
  --apply     place the Skill in each profile, each after a y/N question
`

// Env is what the command is given by its process.
type Env struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	Getenv func(string) string
}

type options struct {
	configFile string
	home       string
	instance   string
	profiles   multi
	platforms  multi
	apply      bool
}

// multi is a repeatable string flag.
type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

// Run is `setup`: args are the arguments after the command name. It returns the
// exit code.
func Run(args []string, env Env) int {
	opts, err := parse(args)
	if errors.Is(err, flag.ErrHelp) {
		fmt.Fprint(env.Stdout, Usage)
		return exitOK
	}
	if err != nil {
		fmt.Fprintln(env.Stderr, "sidera-adapter-hermes: setup:", err)
		fmt.Fprint(env.Stderr, Usage)
		return exitUsage
	}
	if err := run(opts, env); err != nil {
		fmt.Fprintln(env.Stderr, "sidera-adapter-hermes: setup:", err)
		return exitFailed
	}
	return exitOK
}

func parse(args []string) (options, error) {
	var o options
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.configFile, "config", "", "")
	fs.StringVar(&o.home, "home", "", "")
	fs.StringVar(&o.instance, "instance", DefaultInstance, "")
	fs.Var(&o.profiles, "profile", "")
	fs.Var(&o.platforms, "platform", "")
	fs.BoolVar(&o.apply, "apply", false, "")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	for _, p := range o.profiles {
		if err := config.ValidateProfileName(p); err != nil {
			return o, fmt.Errorf("--profile: %w", err)
		}
	}
	switch {
	case fs.NArg() != 0:
		return o, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	case o.configFile == "":
		return o, errors.New("--config is required")
	case o.configFile == "-" && o.apply:
		return o, errors.New("--config - reads standard input, which --apply needs for its questions")
	}
	return o, nil
}

func run(o options, env Env) error {
	cfg, err := readConfig(o.configFile, env.Stdin)
	if err != nil {
		return err
	}
	home, err := datahome.Resolve(o.home, datahome.Host{OS: runtime.GOOS, Getenv: env.Getenv, UserHomeDir: os.UserHomeDir, Lstat: os.Lstat})
	if err != nil {
		return err
	}
	dir := StateDir(home, o.instance)
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return fmt.Errorf("%s does not exist: enable the instance first ('sidera adapter enable hermes --instance %s --config <file>'), or check --home and --instance", dir, o.instance)
	}

	skill, err := Skill(cfg.Board, cfg.Aliases)
	if err != nil {
		return err
	}
	skillFile, err := writeSkill(dir, skill)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	profiles := orDefault(o.profiles, cfg.Home.Profile)
	platforms := orDefault(o.platforms, cfg.Home.Platform)
	configFiles := map[string]string{}
	for _, p := range profiles {
		if configFiles[p], err = profileConfigFile(ctx, cfg, p); err != nil {
			return err
		}
	}
	steps := Plan(profiles, skillFile, func(p string) string { return profileSkillDir(configFiles[p]) })
	board := newBoard(cfg, env.Stderr)
	printPlan(env.Stdout, cfg, skillFile, steps, notifierOff(ctx, board, profiles, env.Stderr),
		toolsetGaps(ctx, board, profiles, Platforms(platforms), configFiles, env.Stderr))
	if !o.apply {
		return nil
	}
	return apply(steps, env)
}

// profileConfigFile is the config.yaml of a profile. Hermes keeps the profile's
// directory around its configuration file, and `config path` names the file; asking
// Hermes is the one way to learn where a profile lives without repeating how Hermes
// lays profiles out.
func profileConfigFile(ctx context.Context, cfg config.Config, profile string) (string, error) {
	cmd := exec.CommandContext(ctx, cfg.HermesPath, "-p", profile, "config", "path")
	cmd.Env = hermesChildEnv(cfg)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("hermes -p %s config path: %w", profile, err)
	}
	lines := strings.Fields(strings.TrimSpace(string(out)))
	if len(lines) == 0 || !filepath.IsAbs(lines[len(lines)-1]) {
		return "", fmt.Errorf("hermes -p %s config path printed %q, want the path of config.yaml", profile, strings.TrimSpace(string(out)))
	}
	return lines[len(lines)-1], nil
}

// profileSkillDir is where a profile keeps the Skill, given its config.yaml. Hermes
// finds a skill in any directory under the profile's skills directory that holds a
// SKILL.md.
func profileSkillDir(configFile string) string {
	return filepath.Join(filepath.Dir(configFile), "skills", SkillName)
}

// newBoard is the client through which setup asks Hermes what it reads of the adapter's
// own configuration (the notifier, the toolsets), so that the adapter and setup read
// the same way. It is nil, with a warning on w, when the configuration cannot make one:
// the owner's setup does not depend on the reading.
func newBoard(cfg config.Config, w io.Writer) *kanban.Client {
	board, err := kanban.New(kanban.Config{Executable: cfg.HermesPath, Home: cfg.HermesHome, KanbanHome: cfg.KanbanHome, Board: cfg.Board})
	if err != nil {
		fmt.Fprintln(w, "sidera-adapter-hermes: setup: warning:", err)
		return nil
	}
	return board
}

// notifierOff names the profiles whose gateway does not run the Kanban notifier,
// asking Hermes as the adapter does (the client is the one home of that question). A
// profile that cannot be read is said so on w and left out, while the profiles that
// could be read are still named.
func notifierOff(ctx context.Context, board *kanban.Client, profiles []string, w io.Writer) []string {
	if board == nil {
		return nil
	}
	off, err := board.NotifierDisabled(ctx, profiles)
	if err != nil {
		fmt.Fprintf(w, "sidera-adapter-hermes: setup: warning: could not read whether the Kanban notifier is on in every profile: %v\n", err)
	}
	return off
}

// toolsetGaps names the platforms of the profiles whose agents lack a toolset they
// delegate with, asking Hermes as the gate does. A platform that cannot be read is said
// so on w and left out, while the others are still named.
func toolsetGaps(ctx context.Context, board *kanban.Client, profiles, platforms []string, configFiles map[string]string, w io.Writer) []ToolsetGap {
	if board == nil {
		return nil
	}
	var gaps []ToolsetGap
	for _, profile := range profiles {
		for _, platform := range platforms {
			reading, err := board.Toolsets(ctx, profile, platform)
			if err != nil {
				fmt.Fprintf(w, "sidera-adapter-hermes: setup: warning: could not read which toolsets profile %s has on platform %s: %v\n", profile, platform, err)
				continue
			}
			if len(reading.Missing) > 0 {
				gaps = append(gaps, ToolsetGap{Profile: profile, Platform: platform, ConfigFile: configFiles[profile],
					Enabled: reading.Enabled, Missing: reading.Missing})
			}
		}
	}
	return gaps
}

// StateDir is where the daemon keeps an instance's private state: adapter-state/<instance>
// in the data directory, which the adapter-host reference defines. The adapter is told
// the directory in initialize; setup runs without the daemon and so derives the same path.
func StateDir(home, instance string) string {
	return filepath.Join(home, "adapter-state", instance)
}

// readConfig reads the instance configuration document and decodes it with the
// adapter's own decoder. The daemon has no method that returns an instance's
// configuration and its data files are not an interface, so the document the owner
// gave 'sidera adapter enable' or 'config' is the input; reading it here repeats no
// parsing of the daemon's.
func readConfig(file string, stdin io.Reader) (config.Config, error) {
	var data []byte
	var err error
	if file == "-" {
		data, err = io.ReadAll(stdin)
	} else {
		data, err = os.ReadFile(file)
	}
	if err != nil {
		return config.Config{}, err
	}
	var values adapterproto.ConfigValues
	if err := json.Unmarshal(data, &values); err != nil {
		return config.Config{}, fmt.Errorf("%s: want one JSON object of settings: %w", file, err)
	}
	cfg, err := config.Decode(values)
	if err != nil {
		return config.Config{}, fmt.Errorf("%s: %w", file, err)
	}
	return cfg, nil
}

// writeSkill writes the Skill into the instance's state directory and returns its path.
// The daemon starts an adapter only when its whole state directory is private to the
// owner, so the directory and the file are owner-only; one that is wider, as an earlier
// version of this command left it, is narrowed, or the instance would not start again.
func writeSkill(stateDir string, skill []byte) (string, error) {
	file := filepath.Join(stateDir, "skill", "SKILL.md")
	if err := writeFile(file, skill, 0o700, 0o600); err != nil {
		return "", err
	}
	if err := os.Chmod(filepath.Dir(file), 0o700); err != nil {
		return "", err
	}
	return file, nil
}

// writeFile replaces the file with a temporary file and a rename, so that a reader
// (Hermes scanning its skills) never sees half of it. The directory is made with
// dirMode when it is missing; the file has fileMode.
func writeFile(path string, data []byte, dirMode, fileMode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(data)
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		os.Remove(tmp.Name())
		return werr
	}
	if err := os.Chmod(tmp.Name(), fileMode); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func orDefault(given multi, fallback string) []string {
	if len(given) > 0 {
		return slices.Compact(slices.Sorted(slices.Values(given)))
	}
	return []string{fallback}
}

func printPlan(w io.Writer, cfg config.Config, skillFile string, steps []Step, notifierOff []string, gaps []ToolsetGap) {
	fmt.Fprintf(w, "Wrote the Skill for %d alias(es): %s\n", len(cfg.Aliases), skillFile)
	fmt.Fprintf(w, "The Skill names the board %q, so the agent and the adapter use the same one.\n\n", cfg.Board)
	fmt.Fprintln(w, "Steps (HERMES_HOME is "+cfg.HermesHome+envKanban(cfg)+"):")
	for i, s := range steps {
		fmt.Fprintf(w, "  %d. %s\n     %s\n", i+1, s.Why, s.Command(cfg.HermesPath))
	}
	for _, s := range NotifierSteps(notifierOff) {
		fmt.Fprintf(w, "\nWarning: %s\n  %s\n", s.Why, s.Command(cfg.HermesPath))
	}
	fmt.Fprint(w, ToolsetAdvice(gaps))
	fmt.Fprintln(w, `
Then restart the gateway that serves each profile: Hermes reads its toolsets and Skills
when the gateway starts, and this command restarts nothing. By default one gateway, the
host, serves every profile (restart that one); a profile that runs its own gateway
(gateway.standalone) has to be restarted on its own.

To find the address behind an alias, list what another node shows:
  sidera-adapter-console directory --node <node id>
and add the entry to the instance's 'aliases' setting ('sidera adapter config').
When the aliases change, run setup again: the Skill lists them.`)
}

func envKanban(cfg config.Config) string {
	if cfg.KanbanHome == "" {
		return ""
	}
	return ", HERMES_KANBAN_HOME is " + cfg.KanbanHome
}

// apply places the Skill, one profile at a time, each after the owner said yes. A step
// that fails stops the run.
func apply(steps []Step, env Env) error {
	in := bufio.NewReader(env.Stdin)
	for i, s := range steps {
		fmt.Fprintf(env.Stdout, "\n[%d/%d] %s\n", i+1, len(steps), s.Command(""))
		fmt.Fprint(env.Stdout, "Run it? [y/N] ")
		answer, err := in.ReadString('\n')
		if err != nil && answer == "" {
			return fmt.Errorf("no answer on standard input; stopped before step %d", i+1)
		}
		if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
			fmt.Fprintln(env.Stdout, "skipped")
			continue
		}
		data, err := os.ReadFile(s.CopyFrom)
		if err == nil {
			err = writeFile(s.CopyTo, data, 0o755, 0o644)
		}
		if err != nil {
			return fmt.Errorf("step %d failed: %w", i+1, err)
		}
	}
	return nil
}

// hermesChildEnv is the environment of every Hermes process `setup` starts: the
// adapter's own (kanban.ChildEnv, the one home of that rule), so that the owner's
// shell, which may point at another home or pin a profile, never decides which
// installation is changed.
func hermesChildEnv(cfg config.Config) []string {
	return kanban.ChildEnv(os.Environ(), kanban.Config{Home: cfg.HermesHome, KanbanHome: cfg.KanbanHome})
}
