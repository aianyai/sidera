package fakeadapter

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"sync"

	"github.com/aianyai/sidera/internal/node/testkit/fakectl"
	"github.com/aianyai/sidera/internal/node/testkit/fakeruntime"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
	"github.com/aianyai/sidera/sdk/go/strictjson"
)

// ProcessConfig is everything a fake adapter program is told when it is started,
// as the JSON the fixture hands over in the environment.
type ProcessConfig struct {
	InstanceID string          `json:"instance_id"`
	Manifest   ManifestOptions `json:"manifest"`
	// LedgerDir is the directory of the file ledger, shared with the fixture and
	// with the runtime processes.
	LedgerDir string `json:"ledger_dir"`
	// RuntimeDir is where the runtime processes keep their records; RuntimeBinary
	// is the program of a runtime process.
	RuntimeDir    string `json:"runtime_dir"`
	RuntimeBinary string `json:"runtime_binary"`
	// RuntimeSeed is the seed of native references.
	RuntimeSeed uint64 `json:"runtime_seed"`
	// ClockStartMS is the test time the process starts at, in Unix milliseconds.
	ClockStartMS int64 `json:"clock_start_ms"`
	// Control is the address of the fixture's control server; absent when none.
	Control string `json:"control,omitempty"`
}

func (c ProcessConfig) validate() error {
	switch {
	case c.InstanceID == "":
		return errors.New("fakeadapter: a process config needs an instance id")
	case c.LedgerDir == "" || c.RuntimeDir == "":
		return errors.New("fakeadapter: a process config needs its directories")
	case c.Manifest.Runtime && c.RuntimeBinary == "":
		return errors.New("fakeadapter: an adapter that declares a runtime needs the runtime program")
	case c.ClockStartMS < 0:
		return errors.New("fakeadapter: the clock cannot start before 1970")
	}
	return c.Manifest.Validate()
}

// DecodeProcessConfig reads a process config strictly.
func DecodeProcessConfig(data []byte) (ProcessConfig, error) {
	var c ProcessConfig
	if err := strictjson.Decode(data, &c); err != nil {
		return ProcessConfig{}, err
	}
	return c, c.validate()
}

// ConfigEnv is the environment variable that carries the ProcessConfig.
const ConfigEnv = "SIDERA_FAKE_ADAPTER_CONFIG"

// RunProcess is the body of a fake adapter program: Serve over stdin and stdout,
// with a file ledger, native sessions that are runtime processes, and the test
// clock, the script step and the knobs under the control of the fixture over the
// control channel. It returns when the protocol stream ends, or when the control
// connection ends: a fake adapter whose fixture is gone ends itself.
func RunProcess(ctx context.Context, cfg ProcessConfig, stdin io.Reader, stdout io.WriteCloser, logger *slog.Logger) error {
	if err := cfg.validate(); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	clock := fakectl.NewClock(cfg.ClockStartMS)
	files := ledger.NewDir(cfg.LedgerDir)
	led, err := files.Open("adapter/"+cfg.InstanceID, os.Getpid(), clock.Now)
	if err != nil {
		return err
	}
	defer led.Close()
	launcher := &fakeruntime.ProcessLauncher{
		Binary: cfg.RuntimeBinary, Dir: cfg.RuntimeDir, LedgerDir: cfg.LedgerDir, Control: cfg.Control,
		Seed: cfg.RuntimeSeed, Now: clock.Now,
	}
	defer launcher.Close()
	knobs := NewKnobs()

	var wg sync.WaitGroup
	defer wg.Wait()
	if cfg.Control != "" {
		client, err := fakectl.Dial(ctx, cfg.Control)
		if err != nil {
			return err
		}
		defer client.Close()
		client.HandleClock(clock.SetMS)
		client.HandleStep(files.SetStep)
		client.HandleKnob(knobsHandler(knobs))
		if err := client.Start(ctx, fakectl.HelloParams{Role: fakectl.RoleAdapter, ID: cfg.InstanceID, PID: int64(os.Getpid())}); err != nil {
			return err
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case <-client.Done():
				cancel()
			case <-ctx.Done():
			}
		}()
	}
	err = Serve(ctx, stdin, stdout, Config{
		InstanceID: cfg.InstanceID, Now: clock.Now, Ledger: led, Runtime: launcher,
		Manifest: cfg.Manifest, Knobs: knobs, Logger: logger,
	})
	if errors.Is(err, context.Canceled) {
		return nil // the fixture ended the control connection, or a signal came
	}
	return err
}

func knobsHandler(k *Knobs) func(context.Context, string, json.RawMessage) (any, error) {
	return k.Dispatch
}
