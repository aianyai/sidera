package fakeruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
)

// ExecRunner carries out ActionExec actions as real commands: the way the model
// of a real runtime reaches the channel's tools, by starting the command-line
// client with the environment of its session. The argument vector is Args["argv"],
// a JSON array of strings; the command's standard output is what the model saw.
// Any other kind of action is an error: what a command line to run means for a
// receive or an acknowledgement is the business of the layer that builds the
// client.
type ExecRunner struct {
	// Dir is the working directory of the commands; empty for the current one.
	Dir string
}

var _ ActionRunner = ExecRunner{}

// RunAction runs the command. A command that fails keeps the output it printed,
// so that what the model saw of an error is still on record.
func (r ExecRunner) RunAction(ctx context.Context, session SessionInfo, a TurnAction) (ActionResult, error) {
	if a.Kind != ActionExec {
		return ActionResult{}, fmt.Errorf("fakeruntime: ExecRunner runs %q actions, not %q", ActionExec, a.Kind)
	}
	var argv []string
	if err := json.Unmarshal([]byte(a.Args["argv"]), &argv); err != nil || len(argv) == 0 {
		return ActionResult{}, errors.New(`fakeruntime: an exec action needs Args["argv"], a JSON array of strings`)
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = r.Dir
	cmd.Env = os.Environ()
	keys := make([]string, 0, len(session.Env))
	for k := range session.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		cmd.Env = append(cmd.Env, k+"="+session.Env[k])
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	return ActionResult{Output: out.Bytes()}, err
}
