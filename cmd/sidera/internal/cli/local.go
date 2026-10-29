package cli

import (
	"context"
	"fmt"

	"github.com/aianyai/sidera/internal/node/adapterhost"
	"github.com/aianyai/sidera/internal/node/daemon"
)

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
	cfg := daemon.Config{Home: c.home, Clock: clk, Rand: c.random, Observer: c.observer, Version: c.Version}
	if args[0] == "init" {
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
	if err := daemon.Run(ctx, cfg); err != nil {
		return &daemonError{err}
	}
	return nil
}
