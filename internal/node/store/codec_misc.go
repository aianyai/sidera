package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"
	"unicode/utf8"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/handoff"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// ---- endpoints

var statusNames = names[EndpointStatus]{{EndpointCreating, "creating"}, {EndpointOpen, "open"}, {EndpointClosed, "closed"}}

type sessionJSON struct {
	Preset     string                        `json:"preset,omitempty"`
	NativeRef  string                        `json:"native_ref,omitempty"`
	Process    *adapterproto.ProcessIdentity `json:"process,omitempty"`
	ToolAccess []string                      `json:"tool_access,omitempty"`
	Released   bool                          `json:"released,omitempty"`
}

type endpointJSON struct {
	Kind        string          `json:"kind"`
	Instance    string          `json:"instance"`
	DisplayName string          `json:"display_name,omitempty"`
	Description string          `json:"description,omitempty"`
	Status      string          `json:"status"`
	Token       string          `json:"token,omitempty"`
	Declaration json.RawMessage `json:"declaration,omitempty"`
	Session     *sessionJSON    `json:"session,omitempty"`
	CreatedBy   string          `json:"created_by,omitempty"`
}

var errEmptySession = errors.New("an empty session mapping is not written")

// checkReleased holds the fact that a native session was released to the endpoint
// it can be true of: one that is closed and has a native session to release.
func checkReleased(status EndpointStatus, s SessionInfo) error {
	if s.Released && (status != EndpointClosed || s.NativeRef == "") {
		return errors.New("only a closed session endpoint with a native session has it released")
	}
	return nil
}

// TokenLen is the length of an endpoint token in bytes.
const TokenLen = 32

// checkEndpointText holds the display name and the description of an endpoint
// to the bounds of the protocol that registers them. Both are optional: an empty
// one is not written. Text that is not valid UTF-8 would not come back from the
// encoder as it went in, so the encoder refuses it rather than altering it. The
// decoder never sees such text: encoding/json replaces an invalid byte with
// U+FFFD, and a body that held one is refused by the canonical re-encoding check
// of DecodeRecord, which is where a body the encoder never writes is caught; the
// test is kept on that path only so that this function states the whole rule and
// the encoder and the decoder cannot drift apart. The length bound is checked on
// both paths.
func checkEndpointText(displayName, description string) error {
	for _, f := range []struct {
		what, text string
		max        int
	}{
		{"display name", displayName, adapterproto.MaxDisplayName},
		{"description", description, adapterproto.MaxDescription},
	} {
		if !utf8.ValidString(f.text) {
			return fmt.Errorf("an endpoint's %s is not valid UTF-8", f.what)
		}
		if n := utf8.RuneCountInString(f.text); n > f.max {
			return fmt.Errorf("an endpoint's %s is %d characters, at most %d", f.what, n, f.max)
		}
	}
	return nil
}

func encodeEndpoint(r EndpointRecord) ([]byte, error) {
	if r.ID.IsZero() {
		return nil, errors.New("an endpoint record without an endpoint")
	}
	var j endpointJSON
	var err error
	if j.Kind, err = enumName(r.Kind, adapterreg.EndpointKind.String, "endpoint kind"); err != nil || !r.Kind.Valid() {
		return nil, fmt.Errorf("endpoint kind %d has no name", r.Kind)
	}
	if r.Instance == "" || len(r.Instance) > 128 {
		return nil, errors.New("an endpoint's instance name is 1 to 128 bytes")
	}
	j.Instance = r.Instance
	if err := checkEndpointText(r.DisplayName, r.Description); err != nil {
		return nil, err
	}
	j.DisplayName, j.Description = r.DisplayName, r.Description
	if j.Status, err = statusNames.name(r.Status, "endpoint status"); err != nil {
		return nil, err
	}
	switch r.Status {
	case EndpointOpen, EndpointCreating:
		if len(r.Token) != TokenLen {
			return nil, fmt.Errorf("an endpoint that is %s has a token of %d bytes, got %d", j.Status, TokenLen, len(r.Token))
		}
		j.Token = b64(r.Token)
	default:
		if len(r.Token) != 0 {
			return nil, errors.New("a closed endpoint has no token")
		}
	}
	if len(r.Declaration) != 0 {
		if err := compactObject(r.Declaration, "the declaration", 0); err != nil {
			return nil, err
		}
		j.Declaration = json.RawMessage(r.Declaration)
	}
	if r.Session != nil {
		if r.Kind != adapterreg.KindSession {
			return nil, errors.New("a session mapping belongs to a session endpoint")
		}
		s := r.Session
		if s.empty() {
			return nil, errEmptySession
		}
		if s.Process != nil {
			if err := s.Process.Validate(); err != nil {
				return nil, fmt.Errorf("session process: %w", err)
			}
		}
		if err := checkReleased(r.Status, *s); err != nil {
			return nil, err
		}
		j.Session = &sessionJSON{Preset: s.Preset, NativeRef: s.NativeRef, Process: s.Process, ToolAccess: s.ToolAccess, Released: s.Released}
	}
	if r.CreatedBy != "" {
		if r.Kind != adapterreg.KindSession {
			return nil, errors.New("created_by belongs to a session endpoint")
		}
		if _, err := hexN(r.CreatedBy, 32); err != nil {
			return nil, fmt.Errorf("created_by: %w", err)
		}
		j.CreatedBy = r.CreatedBy
	}
	return marshal(j)
}

func decodeEndpoint(id ident.EndpointID, body []byte) (EndpointRecord, error) {
	var j endpointJSON
	if err := unmarshal(body, &j); err != nil {
		return EndpointRecord{}, err
	}
	r := EndpointRecord{ID: id, Instance: j.Instance, DisplayName: j.DisplayName, Description: j.Description}
	var err error
	if r.Kind, err = enumValue(j.Kind, adapterreg.EndpointKind.String, "endpoint kind"); err != nil {
		return r, err
	}
	if r.Instance == "" || len(r.Instance) > 128 {
		return r, errors.New("an endpoint's instance name is 1 to 128 bytes")
	}
	if err := checkEndpointText(r.DisplayName, r.Description); err != nil {
		return r, err
	}
	if r.Status, err = statusNames.value(j.Status, "endpoint status"); err != nil {
		return r, err
	}
	if r.Status == EndpointClosed {
		if j.Token != "" {
			return r, errors.New("a closed endpoint has no token")
		}
	} else {
		if r.Token, err = unb64(j.Token); err != nil || len(r.Token) != TokenLen {
			return r, fmt.Errorf("an endpoint that is %s has a token of %d bytes", j.Status, TokenLen)
		}
	}
	if len(j.Declaration) != 0 {
		if err := compactObject(j.Declaration, "the declaration", 0); err != nil {
			return r, err
		}
		r.Declaration = []byte(j.Declaration)
	}
	if j.Session != nil {
		if r.Kind != adapterreg.KindSession {
			return r, errors.New("a session mapping belongs to a session endpoint")
		}
		s := &SessionInfo{Preset: j.Session.Preset, NativeRef: j.Session.NativeRef, Process: j.Session.Process, ToolAccess: j.Session.ToolAccess, Released: j.Session.Released}
		if s.empty() {
			return r, errEmptySession
		}
		if err := checkReleased(r.Status, *s); err != nil {
			return r, err
		}
		if s.Process != nil {
			if err := s.Process.Validate(); err != nil {
				return r, fmt.Errorf("session process: %w", err)
			}
		}
		r.Session = s
	}
	if j.CreatedBy != "" {
		if r.Kind != adapterreg.KindSession {
			return r, errors.New("created_by belongs to a session endpoint")
		}
		if _, err := hexN(j.CreatedBy, 32); err != nil {
			return r, fmt.Errorf("created_by: %w", err)
		}
		r.CreatedBy = j.CreatedBy
	}
	return r, nil
}

// ---- attempts

var (
	outcomeNames = names[handoff.AttemptOutcome]{{handoff.AttemptFailed, "failed"}, {handoff.AttemptUnknown, "unknown"}, {handoff.AttemptDelivered, "delivered"}}
	promptNames  = names[handoff.PromptKind]{{handoff.PromptKindFirst, "first"}, {handoff.PromptKindReprompt, "reprompt"}}
	blockedNames = names[handoff.Cause]{
		{handoff.CauseAdapterOffline, "adapter_offline"}, {handoff.CauseAuthInvalid, "auth_invalid"}, {handoff.CauseServiceUnavailable, "service_unavailable"},
	}
)

type attemptJSON struct {
	At      wtime  `json:"at"`
	Outcome string `json:"outcome"`
	Kind    string `json:"kind"`
}

type blockedJSON struct {
	Cause string `json:"cause"`
	Since wtime  `json:"since"`
}

type inFlightJSON struct {
	Attempt string   `json:"attempt"`
	Kind    string   `json:"kind"`
	Covers  []string `json:"covers,omitempty"`
}

type attemptsJSON struct {
	Attempts []attemptJSON `json:"attempts,omitempty"`
	Blocked  *blockedJSON  `json:"blocked,omitempty"`
	InFlight *inFlightJSON `json:"in_flight,omitempty"`
}

func encodeAttempts(r AttemptsRecord) ([]byte, error) {
	if r.Endpoint.IsZero() {
		return nil, errors.New("an attempts record without an endpoint")
	}
	var j attemptsJSON
	for _, a := range r.Attempts {
		at, err := wreq(a.At, "the time of an attempt")
		if err != nil {
			return nil, err
		}
		on, err := outcomeNames.name(a.Outcome, "attempt outcome")
		if err != nil {
			return nil, err
		}
		kn, err := promptNames.name(a.Kind, "prompt kind")
		if err != nil {
			return nil, err
		}
		j.Attempts = append(j.Attempts, attemptJSON{At: at, Outcome: on, Kind: kn})
	}
	if r.Blocked.Cause != 0 {
		cn, err := blockedNames.name(r.Blocked.Cause, "blocked cause")
		if err != nil {
			return nil, err
		}
		since, err := wreq(r.Blocked.Since, "the time a block began")
		if err != nil {
			return nil, err
		}
		j.Blocked = &blockedJSON{Cause: cn, Since: since}
	} else if !r.Blocked.Since.IsZero() {
		return nil, errors.New("a block time without a cause")
	}
	if f := r.InFlight; f != nil {
		if err := f.Attempt.Validate(); err != nil {
			return nil, fmt.Errorf("in-flight attempt: %w", err)
		}
		kn, err := promptNames.name(f.Kind, "prompt kind")
		if err != nil {
			return nil, err
		}
		j.InFlight = &inFlightJSON{Attempt: string(f.Attempt), Kind: kn}
		for _, k := range f.Covers {
			if _, err := k.Bytes(); err != nil {
				return nil, err
			}
			j.InFlight.Covers = append(j.InFlight.Covers, k.String())
		}
	}
	return marshal(j)
}

func decodeAttempts(id ident.EndpointID, body []byte) (AttemptsRecord, error) {
	var j attemptsJSON
	if err := unmarshal(body, &j); err != nil {
		return AttemptsRecord{}, err
	}
	r := AttemptsRecord{Endpoint: id}
	for _, a := range j.Attempts {
		on, err := outcomeNames.value(a.Outcome, "attempt outcome")
		if err != nil {
			return r, err
		}
		kn, err := promptNames.value(a.Kind, "prompt kind")
		if err != nil {
			return r, err
		}
		r.Attempts = append(r.Attempts, handoff.Attempt{At: time.Time(a.At), Outcome: on, Kind: kn})
	}
	if j.Blocked != nil {
		cn, err := blockedNames.value(j.Blocked.Cause, "blocked cause")
		if err != nil {
			return r, err
		}
		r.Blocked = handoff.BlockedReport{Cause: cn, Since: time.Time(j.Blocked.Since)}
	}
	if f := j.InFlight; f != nil {
		in := &InFlightPrompt{Attempt: adapterproto.AttemptID(f.Attempt)}
		if err := in.Attempt.Validate(); err != nil {
			return r, fmt.Errorf("in-flight attempt: %w", err)
		}
		var err error
		if in.Kind, err = promptNames.value(f.Kind, "prompt kind"); err != nil {
			return r, err
		}
		for _, s := range f.Covers {
			k, err := ParseTargetKey(s)
			if err != nil {
				return r, err
			}
			in.Covers = append(in.Covers, k)
		}
		r.InFlight = in
	}
	return r, nil
}

// ---- checkpoints, admin results, alerts, floors

type checkpointJSON struct {
	State json.RawMessage `json:"state"`
}

func encodeCheckpoint(r CheckpointRecord) ([]byte, error) {
	if _, err := CheckpointKeyBytes(r.Key); err != nil {
		return nil, err
	}
	if err := compactObject(r.State, "the checkpoint state", adapterproto.MaxCheckpointBytes); err != nil {
		return nil, err
	}
	return marshal(checkpointJSON{State: json.RawMessage(r.State)})
}

func decodeCheckpoint(key CheckpointKey, body []byte) (CheckpointRecord, error) {
	var j checkpointJSON
	if err := unmarshal(body, &j); err != nil {
		return CheckpointRecord{}, err
	}
	if err := compactObject(j.State, "the checkpoint state", adapterproto.MaxCheckpointBytes); err != nil {
		return CheckpointRecord{}, err
	}
	return CheckpointRecord{Key: key, State: []byte(j.State)}, nil
}

var opRE = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,63}$`)

type adminJSON struct {
	Fingerprint string          `json:"fingerprint"`
	Op          string          `json:"op"`
	Result      json.RawMessage `json:"result"`
	At          wtime           `json:"at"`
	RetainUntil wtime           `json:"retain_until"`
}

func encodeAdmin(r AdminRecord) ([]byte, error) {
	if r.Key.ID.IsZero() {
		return nil, errors.New("an admin result without an operation id")
	}
	if !opRE.MatchString(r.Op) {
		return nil, fmt.Errorf("operation name %q", r.Op)
	}
	if err := compactObject(r.Result, "the result", 0); err != nil {
		return nil, err
	}
	at, err := wreq(r.At, "the time of the result")
	if err != nil {
		return nil, err
	}
	ru, err := wreq(r.RetainUntil, "the retention of the result")
	if err != nil {
		return nil, err
	}
	if time.Time(ru).Before(time.Time(at)) {
		return nil, errors.New("a result is kept until before it was made")
	}
	return marshal(adminJSON{Fingerprint: hexStr(r.Fingerprint[:]), Op: r.Op, Result: json.RawMessage(r.Result), At: at, RetainUntil: ru})
}

func decodeAdmin(key admin.RequestKey, body []byte) (AdminRecord, error) {
	var j adminJSON
	if err := unmarshal(body, &j); err != nil {
		return AdminRecord{}, err
	}
	r := AdminRecord{Key: key, Op: j.Op, At: time.Time(j.At), RetainUntil: time.Time(j.RetainUntil)}
	fp, err := hexN(j.Fingerprint, 32)
	if err != nil {
		return r, fmt.Errorf("fingerprint: %w", err)
	}
	copy(r.Fingerprint[:], fp)
	if !opRE.MatchString(r.Op) {
		return r, fmt.Errorf("operation name %q", r.Op)
	}
	if err := compactObject(j.Result, "the result", 0); err != nil {
		return r, err
	}
	r.Result = []byte(j.Result)
	if r.RetainUntil.Before(r.At) {
		return r, errors.New("a result is kept until before it was made")
	}
	return r, nil
}

var alertKindRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

type ackedJSON struct {
	At        wtime  `json:"at"`
	Operation string `json:"operation"`
}

type alertJSON struct {
	Kind    string     `json:"kind"`
	At      wtime      `json:"at"`
	Detail  string     `json:"detail"`
	Subject string     `json:"subject,omitempty"`
	Acked   *ackedJSON `json:"acked,omitempty"`
}

func encodeAlert(r AlertRecord) ([]byte, error) {
	if r.ID == (AlertID{}) {
		return nil, errors.New("an alert without an id")
	}
	if !alertKindRE.MatchString(r.Kind) {
		return nil, fmt.Errorf("alert kind %q", r.Kind)
	}
	at, err := wreq(r.At, "the time of an alert")
	if err != nil {
		return nil, err
	}
	j := alertJSON{Kind: r.Kind, At: at, Detail: r.Detail, Subject: r.Subject}
	if r.Acked != nil {
		if r.Acked.Operation.IsZero() {
			return nil, errors.New("an acknowledgement without an operation")
		}
		aat, err := wreq(r.Acked.At, "the time of an acknowledgement")
		if err != nil {
			return nil, err
		}
		j.Acked = &ackedJSON{At: aat, Operation: hexStr(r.Acked.Operation[:])}
	}
	return marshal(j)
}

func decodeAlert(id AlertID, body []byte) (AlertRecord, error) {
	var j alertJSON
	if err := unmarshal(body, &j); err != nil {
		return AlertRecord{}, err
	}
	r := AlertRecord{ID: id, Kind: j.Kind, At: time.Time(j.At), Detail: j.Detail, Subject: j.Subject}
	if !alertKindRE.MatchString(r.Kind) {
		return r, fmt.Errorf("alert kind %q", r.Kind)
	}
	if j.Acked != nil {
		op, err := hexN(j.Acked.Operation, 16)
		if err != nil {
			return r, fmt.Errorf("operation: %w", err)
		}
		a := &AlertAcked{At: time.Time(j.Acked.At)}
		copy(a.Operation[:], op)
		if a.Operation.IsZero() {
			return r, errors.New("an acknowledgement without an operation")
		}
		r.Acked = a
	}
	return r, nil
}

type seqJSON struct {
	Floors map[string]uint64 `json:"floors"`
}

func encodeSeq(r SeqRecord) ([]byte, error) {
	if r.Sender.IsZero() {
		return nil, errors.New("a floor record without a sender")
	}
	j := seqJSON{Floors: map[string]uint64{}}
	for a, n := range r.Floors {
		if a.IsZero() || n == 0 {
			return nil, errors.New("a floor is for an address and at least 1")
		}
		j.Floors[a.String()] = n
	}
	return marshal(j)
}

func decodeSeq(id ident.EndpointID, body []byte) (SeqRecord, error) {
	var j seqJSON
	if err := unmarshal(body, &j); err != nil {
		return SeqRecord{}, err
	}
	r := SeqRecord{Sender: id, Floors: map[ident.Address]uint64{}}
	for s, n := range j.Floors {
		a, err := ident.ParseAddress(s)
		if err != nil {
			return r, err
		}
		if n == 0 {
			return r, errors.New("a floor is at least 1")
		}
		r.Floors[a] = n
	}
	return r, nil
}

func hexStr(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, 2*len(b))
	for _, x := range b {
		out = append(out, digits[x>>4], digits[x&15])
	}
	return string(out)
}
