package fakectl

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"unicode/utf8"

	"github.com/aianyai/sidera/sdk/go/strictjson"
)

// The methods of the control channel. Requests: hello (client to server), clock.set,
// step.set and knob (server to client). Notification: report (client to server).
const (
	MethodHello    = "hello"
	MethodClockSet = "clock.set"
	MethodStepSet  = "step.set"
	MethodKnob     = "knob"
	MethodReport   = "report"
)

// The roles of a connecting process.
const (
	RoleAdapter = "adapter"
	RoleRuntime = "runtime"
)

var (
	identPattern = regexp.MustCompile(`^[A-Za-z0-9._:@-]{1,128}$`)
	namePattern  = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,63}$`)
)

const (
	maxStepLen   = 256
	maxDetailLen = 1024
)

// HelloParams is the first request of a connection: who is calling.
type HelloParams struct {
	// Role is RoleAdapter or RoleRuntime.
	Role string `json:"role"`
	// ID is the instance name of an adapter or the native reference of a runtime
	// session.
	ID string `json:"id"`
	// PID is the calling process.
	PID int64 `json:"pid"`
}

func (p HelloParams) validate() error {
	if p.Role != RoleAdapter && p.Role != RoleRuntime {
		return fmt.Errorf("role %q is neither %q nor %q", p.Role, RoleAdapter, RoleRuntime)
	}
	if !identPattern.MatchString(p.ID) {
		return fmt.Errorf("id %q is not 1 to 128 characters of letters, digits and ._:@-", p.ID)
	}
	if p.PID < 0 {
		return fmt.Errorf("pid %d is negative", p.PID)
	}
	return nil
}

// ClockSetParams moves the test clock of a fake process to an absolute time. An
// absolute value, not a step, so that a replay and a log say what time it was,
// and so that setting the clock back is expressible.
type ClockSetParams struct {
	ToMS int64 `json:"to_ms"`
}

func (p ClockSetParams) validate() error {
	if p.ToMS < 0 {
		return fmt.Errorf("to_ms %d is negative", p.ToMS)
	}
	return nil
}

// StepSetParams announces the script step that is about to run.
type StepSetParams struct {
	Step string `json:"step"`
}

func (p StepSetParams) validate() error {
	if utf8.RuneCountInString(p.Step) > maxStepLen {
		return fmt.Errorf("step is longer than %d characters", maxStepLen)
	}
	return nil
}

// KnobParams turns a named knob of a fake process. The names and arguments of
// the knobs belong to the fake that has them.
type KnobParams struct {
	Name string `json:"name"`
	// Args is a non-empty JSON object, or absent.
	Args json.RawMessage `json:"args,omitempty"`
}

func (p KnobParams) validate() error {
	if !namePattern.MatchString(p.Name) {
		return fmt.Errorf("knob name %q is not a lower-case name", p.Name)
	}
	if p.Args == nil {
		return nil
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(p.Args, &members); err != nil || members == nil {
		return errors.New("args is not a JSON object")
	}
	if len(members) == 0 {
		return errors.New("args is an empty object: omit it")
	}
	if err := strictjson.NoDuplicateTopLevelKeys(p.Args); err != nil {
		return fmt.Errorf("args: %w", err)
	}
	return nil
}

// ReportParams tells the fixture that something happened in a fake process, for
// instance that it stopped at a pause point.
type ReportParams struct {
	Name   string `json:"name"`
	Detail string `json:"detail,omitempty"`
}

func (p ReportParams) validate() error {
	if !namePattern.MatchString(p.Name) {
		return fmt.Errorf("report name %q is not a lower-case name", p.Name)
	}
	if utf8.RuneCountInString(p.Detail) > maxDetailLen {
		return fmt.Errorf("detail is longer than %d characters", maxDetailLen)
	}
	return nil
}

// DecodeParams strictly decodes the params of a method of the control channel
// and validates them. The value is the params type itself, not a pointer.
func DecodeParams(method string, data []byte) (any, error) {
	switch method {
	case MethodHello:
		return decode[HelloParams](data)
	case MethodClockSet:
		return decode[ClockSetParams](data)
	case MethodStepSet:
		return decode[StepSetParams](data)
	case MethodKnob:
		p, err := decode[KnobParams](data)
		if err != nil {
			return nil, err
		}
		k := p.(KnobParams)
		if k.Args != nil {
			// The raw arguments are kept as the encoder will write them, so that
			// encoding and decoding again gives the same value.
			norm, err := json.Marshal(k.Args)
			if err != nil {
				return nil, err
			}
			k.Args = norm
		}
		return k, nil
	case MethodReport:
		return decode[ReportParams](data)
	}
	return nil, fmt.Errorf("fakectl: unknown method %q", method)
}

type validated interface{ validate() error }

func decode[T validated](data []byte) (any, error) {
	var v T
	if err := strictjson.Decode(data, &v); err != nil {
		return nil, err
	}
	if err := v.validate(); err != nil {
		return nil, err
	}
	return v, nil
}
