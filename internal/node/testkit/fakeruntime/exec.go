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
	"sync"
)

// ExecRunner carries out ActionExec actions as real commands: the way the model
// of a real runtime reaches the channel's tools, by starting the command-line
// client with the environment of its session. The argument vector is Args["argv"],
// a JSON array of strings; the command's standard output is what the model saw.
// Any other kind of action is an error: what a command line to run means for a
// receive or an acknowledgement is the business of the layer that builds the
// client.
//
// With a Seed, every command gets the environment variable SIDERA_TEST_SEED set to
// "<seed>/<native ref>/<n>", n counting the commands of the session from 1: a
// client that draws its message numbers from a source derived from it makes the
// same numbers in a rerun of a crash walk, and two commands of one session do not
// draw the same ones.
type ExecRunner struct {
	// Dir is the working directory of the commands; empty for the current one.
	Dir string
	// Seed is the base of the seeds of the commands; empty sets none.
	Seed string

	mu    sync.Mutex
	count map[string]int
}

// TestSeedEnv is the variable that carries the seed of a command.
const TestSeedEnv = "SIDERA_TEST_SEED"

var _ ActionRunner = (*ExecRunner)(nil)

// nextSeed returns the seed of the next command of a session.
func (r *ExecRunner) nextSeed(ref string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.count == nil {
		r.count = map[string]int{}
	}
	r.count[ref]++
	return fmt.Sprintf("%s/%s/%d", r.Seed, ref, r.count[ref])
}

// RunAction runs the command. A command that fails keeps the output it printed,
// so that what the model saw of an error is still on record.
func (r *ExecRunner) RunAction(ctx context.Context, session SessionInfo, a TurnAction) (ActionResult, error) {
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
	if r.Seed != "" {
		cmd.Env = append(cmd.Env, TestSeedEnv+"="+r.nextSeed(session.Ref))
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	return ActionResult{Output: out.Bytes()}, err
}
