package create

import (
	"encoding/json"
	"fmt"

	"github.com/aianyai/sidera/sdk/go/adapterkit/modelchoice"
)

// SessionCheckpointVersion is the format version of the session checkpoint, with the
// same rule as CheckpointVersion: a later format keeps reading this one, and a
// version newer than this adapter knows is refused.
const SessionCheckpointVersion = 1

// sessionCheckpoint is what is stored of a session. Only a session whose requester
// chose a model or an effort stores one; every other follows its preset, so a
// session that stored nothing is a session without a choice.
type sessionCheckpoint struct {
	V      int                 `json:"v"`
	Choice *modelchoice.Choice `json:"choice,omitempty"`
}

// SessionCheckpoint is the checkpoint of a session created with a choice. The pair is
// stored whole, not the requester's own words: the allowlist is checked once, when the
// session is created, so the session must keep running with the combination that was
// checked, even if the owner later edits the preset.
func SessionCheckpoint(c modelchoice.Choice) json.RawMessage {
	raw, _ := json.Marshal(sessionCheckpoint{
		V: SessionCheckpointVersion, Choice: &c,
	})
	return raw
}

// ParseSessionCheckpoint reads the checkpoint the daemon handed back for a session.
// chosen is false for a session that stored nothing. A checkpoint that cannot be read
// is an error: running the session on the preset would use values nobody chose.
func ParseSessionCheckpoint(raw json.RawMessage) (c modelchoice.Choice, chosen bool, err error) {
	if len(raw) == 0 {
		return modelchoice.Choice{}, false, nil
	}
	var cp sessionCheckpoint
	if err := json.Unmarshal(raw, &cp); err != nil {
		return modelchoice.Choice{}, false, fmt.Errorf("create: unreadable session checkpoint: %w", err)
	}
	if cp.V != SessionCheckpointVersion { // the one format so far; see SessionCheckpointVersion
		return modelchoice.Choice{}, false, fmt.Errorf("create: session checkpoint version %d is not %d", cp.V, SessionCheckpointVersion)
	}
	if cp.Choice == nil {
		return modelchoice.Choice{}, false, nil
	}
	if err := cp.Choice.Validate(); err != nil {
		return modelchoice.Choice{}, false, fmt.Errorf("create: session checkpoint: choice: %w", err)
	}
	return *cp.Choice, true, nil
}
