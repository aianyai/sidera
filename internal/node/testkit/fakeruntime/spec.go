package fakeruntime

import (
	"errors"

	"github.com/aianyai/sidera/sdk/go/strictjson"
)

// ProcessSpec is everything a runtime process is told when it is started, as the
// JSON the adapter that starts it hands over in the environment.
type ProcessSpec struct {
	// Create is the session the process hosts.
	Create CreateRequest `json:"create"`
	// Dir is the directory of the session records and of the info files.
	Dir string `json:"dir"`
	// LedgerDir is the directory of the file ledger.
	LedgerDir string `json:"ledger_dir"`
	// Seed makes the native reference and the process identity deterministic.
	Seed uint64 `json:"seed"`
	// ClockStartMS is the test time the process starts at, in Unix milliseconds.
	ClockStartMS int64 `json:"clock_start_ms"`
	// Control is the address of the fixture's control server; absent when none.
	Control string `json:"control,omitempty"`
}

func (s ProcessSpec) validate() error {
	if err := s.Create.validate(); err != nil {
		return err
	}
	if s.Dir == "" || s.LedgerDir == "" {
		return errors.New("fakeruntime: a process spec needs its directories")
	}
	if s.ClockStartMS < 0 {
		return errors.New("fakeruntime: the clock cannot start before 1970")
	}
	return nil
}

// DecodeProcessSpec reads a process spec strictly.
func DecodeProcessSpec(data []byte) (ProcessSpec, error) {
	var s ProcessSpec
	if err := strictjson.Decode(data, &s); err != nil {
		return ProcessSpec{}, err
	}
	return s, s.validate()
}
