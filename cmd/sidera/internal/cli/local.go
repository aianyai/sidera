package cli

import (
	"context"
	"flag"
	"fmt"

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
		_, err = fmt.Fprintf(c.Stdout, "Back up your self-revocation outside this device.\nnode %s\nhome %s\nRun `sidera identity export-revocation` to export it.\n", r.Node.String(), r.Home)
		return err
	}
	host, err := adapterhost.New(adapterhost.Config{Launcher: adapterhost.ExecLauncher{}, Clock: clk})
	if err != nil {
		return err
	}
	cfg.AdapterHost = host
	// cfg has no Relauncher, so the daemon accepts no restart and the ending is
	// always a stop.
	if _, err := daemon.Run(ctx, cfg); err != nil {
		return &daemonError{err}
	}
	return nil
}
