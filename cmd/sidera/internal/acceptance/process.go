package acceptance

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

// process owns exactly one Wait, installed as soon as Start succeeds. Output
// goes to files: a surviving runtime cannot keep an output-copy pipe open.
type process struct {
	cmd  *exec.Cmd
	done chan struct{}
	err  error // published by closing done
}

func startProcess(cmd *exec.Cmd) (*process, error) {
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &process{cmd: cmd, done: make(chan struct{})}
	go func() { p.err = cmd.Wait(); close(p.done) }()
	return p, nil
}

func (p *process) wait(ctx context.Context) error {
	select {
	case <-p.done:
		return p.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *process) killAndWait() (bool, error) {
	err := p.cmd.Process.Kill()
	killed := err == nil
	if errors.Is(err, os.ErrProcessDone) {
		err = nil
	}
	<-p.done
	return killed, err
}

func (p *process) exited() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

// launch gives every node command the same file-output and single-Wait ownership.
// Empty values remove inherited controls rather than adding empty variables.
func (f *Fixture) launch(command, stdout, stderr string, values map[string]string) (*process, error) {
	out, err := os.OpenFile(stdout, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	defer out.Close()
	errout, err := os.OpenFile(stderr, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	defer errout.Close()
	cmd := exec.Command(f.binaries.Tagged, "--home", f.home, command)
	cmd.Dir, cmd.Stdout, cmd.Stderr = f.root, out, errout
	cmd.Env = processEnv(values)
	cmd.WaitDelay = 5 * time.Second
	return startProcess(cmd)
}

// prepare initializes a normal script's home before observed generation 1.
// It has no observer environment or generation entry: Init remains the observed
// initialization runner, with its positive, unique generation contract unchanged.
func (f *Fixture) prepare(ctx context.Context) error {
	if f.current != nil || len(f.gens) != 0 {
		return fmt.Errorf("acceptance: preparation must precede observed generations")
	}
	stdout, stderr := filepath.Join(f.root, "prepare.out"), filepath.Join(f.root, "prepare.err")
	p, err := f.launch("init", stdout, stderr, map[string]string{
		"SIDERA_TEST_SEED":           f.opts.Seed + "/prepare-init",
		"SIDERA_TEST_CLOCK_START_MS": strconv.FormatInt(f.Now().UnixMilli(), 10),
		"SIDERA_CRASH_RECORD":        "", "SIDERA_CRASH_AT": "", "SIDERA_CRASH_GEN": "",
	})
	if err != nil {
		return err
	}
	err = p.wait(ctx)
	if ctx.Err() != nil {
		_, killErr := p.killAndWait()
		return errors.Join(ctx.Err(), killErr)
	}
	if err != nil {
		out, _ := os.ReadFile(stdout)
		diagnostic, _ := os.ReadFile(stderr)
		return fmt.Errorf("acceptance: preparation: %w\nstdout:\n%s\nstderr:\n%s", err, out, diagnostic)
	}
	return nil
}
