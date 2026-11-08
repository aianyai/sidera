package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/aianyai/sidera/cmd/sidera-adapter-claude/internal/create"
	"github.com/aianyai/sidera/cmd/sidera-adapter-claude/internal/turn"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// checkpointVersion is the format version of the checkpoints this adapter writes.
// The daemon keeps a checkpoint across an upgrade of the adapter in place, and a
// session survives it only if the new version reads what the earlier ones wrote:
// when a format changes, the decoders below keep reading every older format. A
// checkpoint of a version newer than this adapter knows, which only a later adapter
// wrote, is refused rather than guessed at.
const checkpointVersion = 1

// choiceCheckpoint is the model and effort a session runs with because its
// requester chose them: the pair the preset's allowlist let through when the
// session was created. A session whose requester chose nothing has none and
// follows the preset. A member left out is left to Claude Code, as it is when the
// preset does not set it.
type choiceCheckpoint struct {
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
}

// validate refuses a stored choice that is empty or holds a value that could not
// have been resolved: it ends up in an argument of a turn process.
func (c choiceCheckpoint) validate() error {
	if c.Model == "" && c.Effort == "" {
		return fmt.Errorf("a choice that chooses nothing")
	}
	if c.Model != "" {
		if err := adapterproto.ValidateModelName(c.Model); err != nil {
			return fmt.Errorf("model: %w", err)
		}
	}
	if c.Effort != "" && !adapterproto.Effort(c.Effort).Valid() {
		return fmt.Errorf("effort %q is not an effort", c.Effort)
	}
	return nil
}

// attemptCheckpoint is what a creation attempt stores with the daemon: the state
// of the attempt, one field for each of create.State's, and what the attempt
// was asked, so that a repeat with other parameters is told from a retry. It
// holds no token: tool access is sent with every request that needs it.
type attemptCheckpoint struct {
	V               int    `json:"v"`
	Attempt         string `json:"attempt"`
	Session         string `json:"session"`
	FirstPrompt     string `json:"first_prompt"`
	FirstDigest     string `json:"first_digest"`
	RecordDir       string `json:"record_dir"`
	Cwd             string `json:"cwd"`
	Preset          string `json:"preset"`
	SessionEndpoint string `json:"session_endpoint"`
	// Model and Effort are what the requester asked for, as asked, so that a repeat
	// that asks for other values is told from a retry; Choice is what that came to
	// under the preset of the time, nil when the requester asked for neither.
	Model  string            `json:"model,omitempty"`
	Effort string            `json:"effort,omitempty"`
	Choice *choiceCheckpoint `json:"choice,omitempty"`
	// The numbers of Stage and FailReason are persisted values and never change.
	Stage     int    `json:"stage"`
	PID       int    `json:"pid,omitempty"`
	StartUnix int64  `json:"start_unix,omitempty"`
	StartKey  string `json:"start_key,omitempty"`
	// ContainerKind and ContainerName say where the recorded process runs; empty
	// for one the adapter found and did not start.
	ContainerKind string `json:"container_kind,omitempty"`
	ContainerName string `json:"container_name,omitempty"`
	Starts        int    `json:"starts"`
	FailReason    int    `json:"fail_reason"`
}

// state returns the create.State the checkpoint holds.
func (c attemptCheckpoint) state() create.State {
	st := create.State{
		AttemptID: create.AttemptID(c.Attempt), SessionID: create.SessionID(c.Session),
		FirstPromptID: create.PromptID(c.FirstPrompt), RecordDir: c.RecordDir,
		Stage: create.Stage(c.Stage), Starts: c.Starts, FailReason: create.Reason(c.FailReason),
	}
	if c.PID != 0 {
		st.Process = create.Process{PID: c.PID, StartUnix: c.StartUnix}
	}
	return st
}

// withState returns the checkpoint with the state's fields; the start key and the
// container stay as they were, for the caller to set when the process changes.
func (c attemptCheckpoint) withState(st create.State) attemptCheckpoint {
	c.Stage, c.Starts, c.FailReason = int(st.Stage), st.Starts, int(st.FailReason)
	c.PID, c.StartUnix = st.Process.PID, st.Process.StartUnix
	return c
}

// process is the turn process the checkpoint records.
func (c attemptCheckpoint) process() turn.Process {
	return turn.Process{
		PID: c.PID, StartUnix: c.StartUnix, StartKey: c.StartKey,
		ContainerKind: c.ContainerKind, ContainerName: c.ContainerName,
	}
}

func decodeAttemptCheckpoint(raw json.RawMessage) (attemptCheckpoint, error) {
	var c attemptCheckpoint
	if err := json.Unmarshal(raw, &c); err != nil {
		return c, fmt.Errorf("attempt checkpoint: %w", err)
	}
	if c.V != checkpointVersion { // the one format so far; see checkpointVersion
		return c, fmt.Errorf("attempt checkpoint: version %d is not %d", c.V, checkpointVersion)
	}
	if c.Choice != nil {
		if err := c.Choice.validate(); err != nil {
			return c, fmt.Errorf("attempt checkpoint: choice: %w", err)
		}
	}
	return c, nil
}

// dispatchCheckpoint is what a prompt attempt stores before its turn process is
// started. Its existence is the proof that nothing was started without it.
type dispatchCheckpoint struct {
	V       int    `json:"v"`
	Stage   string `json:"stage"`
	Session string `json:"session"`
}

// sessionCheckpoint is what an open session stores: where its files are, the
// model and effort its requester chose, if it did, and the last turn.
type sessionCheckpoint struct {
	V         int               `json:"v"`
	Session   string            `json:"session"`
	Cwd       string            `json:"cwd"`
	RecordDir string            `json:"record_dir"`
	Choice    *choiceCheckpoint `json:"choice,omitempty"`
	Turn      *turn.State       `json:"turn,omitempty"`
}

func decodeSessionCheckpoint(raw json.RawMessage) (sessionCheckpoint, error) {
	var c sessionCheckpoint
	if err := json.Unmarshal(raw, &c); err != nil {
		return c, fmt.Errorf("session checkpoint: %w", err)
	}
	if c.V != checkpointVersion { // the one format so far; see checkpointVersion
		return c, fmt.Errorf("session checkpoint: version %d is not %d", c.V, checkpointVersion)
	}
	if c.Session == "" || c.RecordDir == "" || c.Cwd == "" {
		return c, fmt.Errorf("session checkpoint: incomplete")
	}
	if c.Choice != nil {
		if err := c.Choice.validate(); err != nil {
			return c, fmt.Errorf("session checkpoint: choice: %w", err)
		}
	}
	return c, nil
}

// digest is the SHA-256 of a prompt's text in hex.
func digest(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func (a *App) putCheckpoint(ctx context.Context, scope adapterproto.CheckpointScope, id string, state any) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return a.conn().CheckpointPut(ctx, adapterproto.CheckpointPutParams{Scope: scope, ID: id, State: raw})
}
