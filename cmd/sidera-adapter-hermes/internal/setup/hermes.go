package setup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/config"
	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/kanban"
	"github.com/aianyai/sidera/sdk/go/adaptersetup"
)

// hermesTimeout bounds one Hermes command: a hung Hermes must not hold a setup, which
// `sidera update` runs, for good.
const hermesTimeout = time.Minute

// Hermes is what setup asks of Hermes. Every method reads: setup changes no Hermes
// configuration and runs no Hermes command that does (see ToolsetGap.Advice), and the
// interface has nothing that could.
type Hermes interface {
	// ConfigFile is the config.yaml of a profile; the profile's directory is around it.
	ConfigFile(ctx context.Context, profile string) (string, error)
	// Toolsets reads which toolsets a profile has on a platform, as the gate does.
	Toolsets(ctx context.Context, profile, platform string) (kanban.ToolsetReading, error)
	// NotifierDisabled names the profiles whose gateway does not run the Kanban
	// notifier, as the gate reads it.
	NotifierDisabled(ctx context.Context, profiles []string) ([]string, error)
}

// installation is the Hermes the instance configuration names.
type installation struct {
	cfg   config.Config
	board *kanban.Client
}

// NewHermes is the Hermes of an instance configuration. The reads of toolsets and of the
// notifier go through the adapter's own client, so that what setup reports and what the
// adapter's availability says are one answer.
func NewHermes(cfg config.Config) Hermes {
	board, err := kanban.New(kanban.Config{Executable: cfg.HermesPath, Home: cfg.HermesHome, KanbanHome: cfg.KanbanHome, Board: cfg.Board})
	if err != nil {
		return unusable{err}
	}
	return installation{cfg: cfg, board: board}
}

// unusable is the Hermes of a configuration the client cannot be made for: every read
// fails with the reason.
type unusable struct{ err error }

func (u unusable) ConfigFile(context.Context, string) (string, error) { return "", u.err }
func (u unusable) Toolsets(context.Context, string, string) (kanban.ToolsetReading, error) {
	return kanban.ToolsetReading{}, u.err
}
func (u unusable) NotifierDisabled(context.Context, []string) ([]string, error) { return nil, u.err }

// ConfigFile asks Hermes where a profile lives: `config path` names the file, and
// Hermes keeps the profile's directory around it. Asking is the one way to learn that
// without repeating how Hermes lays profiles out. The command gets the environment of the
// adapter's own Hermes commands (the owner's shell may point at another home or pin a
// profile), and runs as every client command of a setup does: the absolute program, no
// standard input, a time limit, in a directory of no project's.
func (h installation) ConfigFile(ctx context.Context, profile string) (string, error) {
	program, err := exec.LookPath(h.cfg.HermesPath)
	if err == nil {
		err = kanban.CheckExecutable(runtime.GOOS, program)
	}
	if err == nil {
		program, err = filepath.Abs(program)
	}
	if err != nil {
		return "", fmt.Errorf("hermes is not installed or cannot be run: %w", err)
	}
	cmd := adaptersetup.ClientCommand{
		Program: program,
		Args:    []string{"-p", profile, "config", "path"},
		Dir:     os.TempDir(),
		Env:     kanban.ChildEnv(os.Environ(), kanban.Config{Home: h.cfg.HermesHome, KanbanHome: h.cfg.KanbanHome}),
		Timeout: hermesTimeout,
	}
	out, err := cmd.Run(ctx)
	var notStarted *adaptersetup.NotStartedError
	switch {
	case errors.As(err, &notStarted) && errors.Is(err, os.ErrNotExist):
		return "", fmt.Errorf("hermes is not installed: %w", err)
	case err != nil:
		return "", err
	case out.ExitCode != 0:
		return "", cmd.Failure(out)
	}
	words := strings.Fields(strings.TrimSpace(string(out.Stdout)))
	if len(words) == 0 || !filepath.IsAbs(words[len(words)-1]) {
		return "", fmt.Errorf("hermes -p %s config path printed %q, want the path of config.yaml", profile, strings.TrimSpace(string(out.Stdout)))
	}
	return words[len(words)-1], nil
}

func (h installation) Toolsets(ctx context.Context, profile, platform string) (kanban.ToolsetReading, error) {
	ctx, cancel := context.WithTimeout(ctx, hermesTimeout)
	defer cancel()
	return h.board.Toolsets(ctx, profile, platform)
}

func (h installation) NotifierDisabled(ctx context.Context, profiles []string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, hermesTimeout*2)
	defer cancel()
	return h.board.NotifierDisabled(ctx, profiles)
}
