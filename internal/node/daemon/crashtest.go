//go:build crashtest

package daemon

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/txn"
)

// RecvAfterResponse is a process point, outside the transaction declaration
// table: a complete receive page arrived but has not reached the model's stdout.
const RecvAfterResponse = "proc/cli.recv-after-response"

// CrashConfig supplies an independently owned append-only record and one process
// generation. The caller opens and closes Writer; this package reads no environment.
type CrashConfig struct {
	Writer io.Writer
	At     string
	Gen    int
}

// CrashObserver records each complete observation before its primitive runs.
// Reached and RecvResponse are safe for concurrent use.
type CrashObserver struct {
	mu  sync.Mutex
	cfg CrashConfig
}

// NewCrashObserver validates a crash-test process's explicit configuration.
// A target requires its record, so a killed process always leaves its evidence.
func NewCrashObserver(cfg CrashConfig) (*CrashObserver, error) {
	if cfg.Writer == nil {
		return nil, fmt.Errorf("daemon: crash record writer is required")
	}
	if cfg.Gen < 1 {
		return nil, fmt.Errorf("daemon: crash generation must be positive")
	}
	if cfg.At != "" && cfg.At != RecvAfterResponse {
		if _, err := fsx.ParsePoint(cfg.At); err != nil {
			return nil, err
		}
	}
	return &CrashObserver{cfg: cfg}, nil
}

type crashRecord struct {
	Gen   int    `json:"gen"`
	Point string `json:"point"`
	Path  string `json:"path"`
	From  string `json:"from"`
	Kind  string `json:"kind"`
	Crash bool   `json:"crash,omitempty"`
}

// Reached retains the raw filesystem operands; the trace consumer alone
// normalizes temporary names for comparison with another run.
func (o *CrashObserver) Reached(ob fsx.Observation, kind txn.Kind) {
	name := "normal"
	if kind == txn.Recovery {
		name = "recovery"
	}
	o.record(crashRecord{Gen: o.cfg.Gen, Point: ob.Point.String(), Path: ob.Path, From: ob.From, Kind: name})
}

// RecvResponse records the CLI-only point after the complete HTTP response and
// before the first stdout write. It is never a transaction declaration.
func (o *CrashObserver) RecvResponse() {
	o.record(crashRecord{Gen: o.cfg.Gen, Point: RecvAfterResponse, Kind: "proc"})
}

func (o *CrashObserver) record(record crashRecord) {
	o.mu.Lock()
	defer o.mu.Unlock()
	write := func() {
		line, err := json.Marshal(record)
		if err != nil {
			panic(err)
		}
		line = append(line, '\n')
		n, err := o.cfg.Writer.Write(line)
		if err != nil {
			panic(fmt.Errorf("daemon: write crash record: %w", err))
		}
		if n != len(line) {
			panic(fmt.Errorf("daemon: write crash record: %w", io.ErrShortWrite))
		}
	}
	write()
	if record.Point == o.cfg.At {
		record.Crash = true
		write()
		killCrashProcess()
	}
}
