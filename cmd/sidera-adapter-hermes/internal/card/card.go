// Package card reads the card a Hermes agent wrote for the Sidera lane and checks
// it against what this node allows. A card is an untrusted request: any process of
// the owner's operating system user can write the board, so nothing in it is
// taken as given.
package card

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/aianyai/sidera/sdk/go/adapterproto"

	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/config"
)

// Version is the envelope version this adapter reads.
const Version = 1

// Modes of a card. ModeNewSession and ModeMessage are the alias modes; a reply
// answers an inbound request.
const (
	ModeNewSession = config.ModeNewSession
	ModeMessage    = config.ModeMessage
	ModeReply      = "reply"
)

// laneName is the assignee of a card for this lane.
const laneName = "sidera"

// InvalidError is the reason a card cannot be served. The adapter writes it in a
// block reason and, the second time, in the result of the completed card, where
// Hermes looks for the paths of local files to upload. So it says what to change and
// never repeats what the card holds: the card may have been written by an agent that
// read text from another machine.
type InvalidError struct{ Reason string }

func (e *InvalidError) Error() string { return e.Reason }

func invalid(format string, args ...any) error {
	return &InvalidError{Reason: fmt.Sprintf(format, args...)}
}

// MatchesAssignee reports whether a card with this assignee is for the lane:
// exactly "sidera", or "sidera:" and a label. The label is only shown; the body
// decides where the card goes. Hermes lowercases assignees, so the comparison
// ignores letter case. "sidera-inbox" is the assignee of the adapter's own
// request cards and is not the lane.
func MatchesAssignee(assignee string) bool {
	a := strings.ToLower(assignee)
	return a == laneName || strings.HasPrefix(a, laneName+":")
}

// Envelope is the JSON body of a card as written.
type Envelope struct {
	Version int    `json:"sidera"`
	To      string `json:"to,omitempty"`
	Mode    string `json:"mode"`
	Preset  string `json:"preset,omitempty"`
	// Model and Effort choose the model and the reasoning effort of a new session;
	// the target preset says which it allows.
	Model   string `json:"model,omitempty"`
	Effort  string `json:"effort,omitempty"`
	ReplyTo string `json:"reply_to,omitempty"`
	Task    string `json:"task"`
}

// Decode reads a card body strictly: one JSON object, no unknown member, the
// version this adapter reads.
func Decode(body string) (Envelope, error) {
	dec := json.NewDecoder(strings.NewReader(body))
	dec.DisallowUnknownFields()
	var e Envelope
	if err := dec.Decode(&e); err != nil {
		return Envelope{}, invalid("the card body is not one JSON object with the members sidera, to, mode, preset, model, effort, reply_to and task, of the types the Skill gives")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return Envelope{}, invalid("the card body has more than one JSON value")
	}
	if e.Version != Version {
		return Envelope{}, invalid(`the card body must be an object with "sidera": %d`, Version)
	}
	return e, nil
}

// Request is a validated card: what to do, and to whom.
type Request struct {
	Mode string
	// Alias is the alias the card names, lowercase. Empty when the card names none:
	// a follow-up that continues its parent's conversation, and a reply.
	Alias string
	// Address is the alias's address, empty with Alias.
	Address adapterproto.Address
	// Preset is the preset of a new session.
	Preset string
	// Model and Effort are the model and the effort the card chose for a new
	// session; empty takes the preset's own.
	Model  string
	Effort adapterproto.Effort
	// ReplyTo is the id of the request card a reply answers.
	ReplyTo string
	Task    string
}

// Validate checks an envelope against the alias table. hasParents says the card
// continues earlier cards, which is the only case where a message can leave out
// the alias.
func (e Envelope) Validate(aliases config.Aliases, hasParents bool) (Request, error) {
	if strings.TrimSpace(e.Task) == "" {
		return Request{}, invalid(`"task" is empty`)
	}
	r := Request{Mode: e.Mode, Preset: e.Preset, Model: e.Model, Effort: adapterproto.Effort(e.Effort), ReplyTo: e.ReplyTo, Task: e.Task}
	switch e.Mode {
	case ModeReply:
		return e.validateReply(r)
	case ModeNewSession, ModeMessage:
	default:
		return Request{}, invalid(`"mode" must be one of %v`, []string{ModeNewSession, ModeMessage, ModeReply})
	}
	if e.ReplyTo != "" {
		return Request{}, invalid(`"reply_to" belongs to mode "reply"`)
	}
	if e.Mode == ModeMessage && e.Preset != "" {
		return Request{}, invalid(`"preset" belongs to mode "new_session"`)
	}
	if err := e.checkChoice(); err != nil {
		return Request{}, err
	}
	if e.To == "" {
		if e.Mode == ModeNewSession || !hasParents {
			return Request{}, invalid(`"to" is required, except in a follow-up message that continues its parent card`)
		}
		return r, nil
	}
	alias, ok := aliases.Lookup(e.To)
	if !ok {
		return Request{}, invalid(`"to" is not an alias this node serves`)
	}
	if !alias.Allows(e.Mode) {
		return Request{}, invalid("alias %q does not allow mode %q", alias.Name, e.Mode)
	}
	if e.Mode == ModeNewSession && !alias.AllowsPreset(e.Preset) {
		return Request{}, invalid("alias %q does not allow the preset on the card (allowed: %s)", alias.Name, strings.Join(alias.Presets, ", "))
	}
	r.Alias, r.Address = alias.Name, alias.Address
	return r, nil
}

func (e Envelope) validateReply(r Request) (Request, error) {
	switch {
	case e.ReplyTo == "":
		return Request{}, invalid(`mode "reply" needs "reply_to", the id of the request card`)
	case e.To != "":
		return Request{}, invalid(`mode "reply" answers the sender of the request and takes no "to"`)
	case e.Preset != "":
		return Request{}, invalid(`"preset" belongs to mode "new_session"`)
	}
	if err := e.checkChoice(); err != nil {
		return Request{}, err
	}
	return r, nil
}

// checkChoice checks the model and the effort of the card. They choose the model
// of a new session, so only that mode takes them, and their syntax is the
// protocol's own, the one every requester is held to. The reason names the member
// and not its value, which a card the agent wrote from remote text may hold.
func (e Envelope) checkChoice() error {
	if e.Mode != ModeNewSession {
		switch {
		case e.Model != "":
			return invalid(`"model" belongs to mode "new_session"`)
		case e.Effort != "":
			return invalid(`"effort" belongs to mode "new_session"`)
		}
		return nil
	}
	if e.Model != "" && adapterproto.ValidateModelName(e.Model) != nil {
		return invalid(`"model" is not a model name: it is 1 to %d letters, digits and . _ : / @ + [ ] -, starting with a letter or digit`, adapterproto.MaxModelNameLen)
	}
	if e.Effort != "" && !adapterproto.Effort(e.Effort).Valid() {
		return invalid(`"effort" must be one of %v`, adapterproto.Efforts())
	}
	return nil
}

// CheckCreator applies the optional list of profiles whose cards are served; an
// empty list serves every creator. It applies to replies as well, since a reply
// makes the node send a message. The creator is only as reliable as the way the
// card was made: a Hermes agent's tool fills it in itself, while the command line
// accepts any value, so the list narrows who the agents are and does not stop a
// process that writes the board.
func CheckCreator(allowed []string, createdBy string) error {
	if len(allowed) == 0 || slices.Contains(allowed, strings.ToLower(createdBy)) {
		return nil
	}
	return invalid("cards created by this profile are not served by this node")
}
