package fakeadapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"

	"github.com/aianyai/sidera/internal/node/testkit/fakectl"
	"github.com/aianyai/sidera/internal/node/testkit/fakeruntime"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
	"github.com/aianyai/sidera/sdk/go/peer"
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
	// RuntimeSeed is the seed of native references; RuntimeTestSeed is the base of
	// the seeds the runtime processes derive for the commands their models run
	// (empty derives none).
	RuntimeSeed     uint64 `json:"runtime_seed"`
	RuntimeTestSeed string `json:"runtime_test_seed,omitempty"`
	// RuntimeFirstTurn is the plan of the first turn of the sessions the adapter
	// creates; the fixture can change it later with the first-turn knob.
	RuntimeFirstTurn fakeruntime.PlanKnob `json:"runtime_first_turn"`
	// ClockStartMS is the test time the process starts at, in Unix milliseconds. It
	// is required: a field that was left out must not read as 1970.
	ClockStartMS int64 `json:"clock_start_ms"`
	// Control is the address of the fixture's control server. It is required: a
	// process that nobody can end is left behind when its test ends.
	Control string `json:"control"`
}

func (c ProcessConfig) validate() error {
	switch {
	case c.InstanceID == "":
		return errors.New("fakeadapter: a process config needs an instance id")
	case c.LedgerDir == "" || c.RuntimeDir == "":
		return errors.New("fakeadapter: a process config needs its directories")
	case c.Manifest.Runtime && c.RuntimeBinary == "":
		return errors.New("fakeadapter: an adapter that declares a runtime needs the runtime program")
	case c.ClockStartMS <= 0:
		return errors.New("fakeadapter: a process config needs the time the clock starts at, after 1970: zero would be a missing field read as 1970")
	case c.Control == "":
		return errors.New("fakeadapter: a process config needs the address of the control server: a process nobody can end is left behind")
	}
	if err := c.RuntimeFirstTurn.Validate(); err != nil {
		return err
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

// ConfigEnv is the environment variable that carries the ProcessConfig, and
// ConfigFlag the argument that names a file holding it instead. The file wins:
// several fake adapters of one daemon share an environment but not their entry
// arguments.
const (
	ConfigEnv  = "SIDERA_FAKE_ADAPTER_CONFIG"
	ConfigFlag = "--config"
)

// ReadProcessConfig finds the ProcessConfig of a program started with args (without
// the program name): the file named by ConfigFlag if the arguments have it, else
// the value of ConfigEnv. Any other argument is refused.
func ReadProcessConfig(args []string, getenv func(string) string) (ProcessConfig, error) {
	switch {
	case len(args) == 0:
		cfg, err := DecodeProcessConfig([]byte(getenv(ConfigEnv)))
		if err != nil {
			return ProcessConfig{}, fmt.Errorf("%s: %w", ConfigEnv, err)
		}
		return cfg, nil
	case len(args) == 2 && args[0] == ConfigFlag:
		raw, err := os.ReadFile(args[1])
		if err != nil {
			return ProcessConfig{}, err
		}
		cfg, err := DecodeProcessConfig(raw)
		if err != nil {
			return ProcessConfig{}, fmt.Errorf("%s: %w", args[1], err)
		}
		return cfg, nil
	}
	return ProcessConfig{}, fmt.Errorf("fakeadapter: the arguments are %v: want none, or %s <file>", args, ConfigFlag)
}

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
		Seed: cfg.RuntimeSeed, TestSeed: cfg.RuntimeTestSeed, Now: clock.Now,
	}
	if err := launcher.SetFirstTurn(cfg.RuntimeFirstTurn.Plan()); err != nil {
		return err
	}
	defer launcher.Close()
	knobs := NewKnobs()

	var wg sync.WaitGroup
	defer wg.Wait()
	client, err := fakectl.Dial(ctx, cfg.Control)
	if err != nil {
		return err
	}
	defer client.Close()
	client.HandleClock(clock.SetMS)
	client.HandleStep(files.SetStep)
	client.HandleKnob(knobsHandler(knobs, launcher))
	knobs.OnPause(func(point string) { _ = client.Report(ctx, "paused", point) })
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
	err = Serve(ctx, stdin, stdout, Config{
		InstanceID: cfg.InstanceID, Now: clock.Now, Ledger: led, Runtime: launcher,
		Manifest: cfg.Manifest, Knobs: knobs, Logger: logger,
	})
	if errors.Is(err, context.Canceled) {
		return nil // the fixture ended the control connection, or a signal came
	}
	return err
}

// knobsHandler serves the knobs of the fixture: the adapter's own, and the one that
// belongs to the runtime processes it starts, which only the adapter can reach.
func knobsHandler(k *Knobs, launcher *fakeruntime.ProcessLauncher) func(context.Context, string, json.RawMessage) (any, error) {
	return func(ctx context.Context, name string, args json.RawMessage) (any, error) {
		if name != fakeruntime.KnobFirstTurn {
			return k.Dispatch(ctx, name, args)
		}
		plan, err := fakeruntime.DecodePlanKnob(name, args)
		if err != nil {
			return nil, peer.NewError(peer.CodeInvalidParams, err.Error())
		}
		return nil, launcher.SetFirstTurn(plan.Plan())
	}
}
