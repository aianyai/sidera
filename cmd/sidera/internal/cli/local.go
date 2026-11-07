package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/aianyai/sidera/cmd/sidera/internal/launch"
	"github.com/aianyai/sidera/internal/node/adapterhost"
	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/daemon"
)

// initFlags are the flags of init.
type initFlags struct {
	port int
}

func (o *initFlags) add(f *flag.FlagSet) {
	f.IntVar(&o.port, "port", config.DefaultListenPort, "port the node listens on, for TCP and QUIC")
}

type daemonError struct{ err error }

func (e *daemonError) Error() string { return e.err.Error() }
func (e *daemonError) Unwrap() error { return e.err }

func (c *command) local(ctx context.Context, args []string) error {
	p, err := parse(c.arguments.flags, args[1:])
	if err != nil {
		return err
	}
	if len(p) != 0 {
		return usage(args[0] + " takes no positional arguments")
	}
	clk, err := c.testClock()
	if err != nil {
		return err
	}
	home, err := c.dataDir()
	if err != nil {
		return err
	}
	cfg := daemon.Config{Home: home, Clock: clk, Rand: c.random, TempRand: c.tempRandom, Observer: c.observer, Version: c.Release.Version}
	if args[0] == "init" {
		if err := config.ValidatePort(c.arguments.init.port); err != nil {
			return usage("--port: " + err.Error())
		}
		cfg.InitPort = c.arguments.init.port
		r, err := daemon.Init(ctx, cfg)
		if err != nil {
			return &daemonError{err}
		}
		if c.json {
			return c.encode(struct {
				Node           string `json:"node"`
				Home           string `json:"home"`
				SelfRevocation string `json:"self_revocation"`
				BackupRequired bool   `json:"backup_required"`
			}{r.Node.String(), r.Home, r.SelfRevocationPath, true})
		}
		_, err = io.WriteString(c.out, renderInitialized(c.out, r.Node.String(), r.Home))
		return err
	}
	host, err := adapterhost.New(adapterhost.Config{Launcher: adapterhost.ExecLauncher{}, Clock: clk})
	if err != nil {
		return err
	}
	cfg.AdapterHost = host
	cfg.Relauncher = relauncher{c.Launch, c.LaunchErr}
	ending, err := daemon.Run(ctx, cfg)
	if err != nil {
		return &daemonError{err}
	}
	// Run starts the program again once this command has released what it holds.
	c.restart = ending == daemon.EndRestart
	return nil
}

// restartCheckLimit bounds the run of the program that a restart would start:
// `version` answers at once, and a program that does not is not one to start. The
// daemon's context ends only when the daemon does.
const restartCheckLimit = 20 * time.Second

// relauncher is how the daemon learns whether, and into what, it can restart:
// the way this process was started, or why that is not known.
type relauncher struct {
	launch    launch.Launch
	launchErr error
}

func (r relauncher) CheckRestart(ctx context.Context) (string, string, error) {
	if r.launchErr != nil {
		return "", "", fmt.Errorf("%w: how this program was started was not recorded: %v", daemon.ErrRestartUnavailable, r.launchErr)
	}
	if ok, why := launch.Restartable(); !ok {
		return "", "", fmt.Errorf("%w: %s", daemon.ErrRestartUnavailable, why)
	}
	ctx, cancel := context.WithTimeout(ctx, restartCheckLimit)
	defer cancel()
	info, err := r.launch.Probe(ctx)
	if err != nil {
		return "", "", err
	}
	return r.launch.Program, info.Version, nil
}
