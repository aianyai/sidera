package record

import (
	"bytes"
	"encoding/json"
)

// Index finds lines of a session file by feeding it the file's bytes in order,
// in any chunking. It looks at every line of the raw file, not only at the
// current branch of the conversation, because the line it looks for is on disk
// whether or not a later turn branched off it.
//
// Two facts are of interest: whether the file holds the session at all (a line
// with its session id), and whether a user record with a given uuid is there.
// The runtime writes the uuid of a stream-json user message as the uuid of its
// user record; only a line of type "user" counts, because other lines (queue
// operations, attachments) carry uuids or none and are not prompts.
//
// A third fact says whether the first two can be trusted: whether every user
// line carries a uuid and a session id, as the lookup assumes. A prompt that is
// not found proves nothing about a file whose user lines are not in that form.
type Index struct {
	session      string
	watched      map[string][]byte
	seen         map[string]bool
	hasID        bool
	unrecognized bool
	partial      []byte
	read         int64
	track        bool
	markers      []Marker
}

// MarkerKind says what a Marker marks in the session file.
type MarkerKind int

const (
	// MarkPrompt: the user record of a watched uuid.
	MarkPrompt MarkerKind = iota + 1
	// MarkTurnEnd: the system record Claude Code writes when a turn ends
	// (subtype turn_duration).
	MarkTurnEnd
	// MarkTurnStart: a user record that begins a turn and is not the prompt of a
	// watched uuid: what the person typed, a message of another session or the
	// notice of a finished task (origin kinds human, peer and task-notification). A
	// record of one of these kinds that arrives in the middle of a turn is marked
	// too: whether it begins a turn is the follower's to say.
	MarkTurnStart
)

// Marker is a line of the session file a follower of its turns cares about, in
// the order of the file.
type Marker struct {
	Kind MarkerKind
	// UUID is the record's, for MarkPrompt and MarkTurnStart.
	UUID string
}

// userMarker is in every line of type "user", whatever spaces the writer puts
// around the type's colon.
var userMarker = []byte(`"user"`)

// turnEndMarker is in the line that ends a turn.
var turnEndMarker = []byte(`"turn_duration"`)

// NewIndex returns an Index for a session that watches the given user uuids; more
// can be added with Watch.
func NewIndex(session string, watch ...string) *Index {
	ix := &Index{session: session, watched: map[string][]byte{}, seen: map[string]bool{}}
	for _, u := range watch {
		ix.Watch(u)
	}
	return ix
}

// Watch adds a user uuid to look for in the bytes fed from now on.
func (ix *Index) Watch(uuid string) {
	if _, ok := ix.watched[uuid]; !ok {
		ix.watched[uuid] = []byte(uuid)
	}
}

// Unwatch forgets a watched uuid and whether it was seen, for a caller that will
// not ask about it again; a long-lived index would otherwise hold every uuid it
// was ever given.
func (ix *Index) Unwatch(uuid string) {
	delete(ix.watched, uuid)
	delete(ix.seen, uuid)
}

// Feed adds the next bytes of the file. A line is judged when its newline
// arrives; a last line without one is not judged, since it may be half written.
func (ix *Index) Feed(chunk []byte) {
	ix.read += int64(len(chunk))
	for len(chunk) > 0 {
		i := bytes.IndexByte(chunk, '\n')
		if i < 0 {
			ix.partial = append(ix.partial, chunk...)
			return
		}
		line := chunk[:i]
		if len(ix.partial) > 0 {
			ix.partial = append(ix.partial, line...)
			line = ix.partial
		}
		ix.judge(line)
		ix.partial = ix.partial[:0]
		chunk = chunk[i+1:]
	}
}

// Track makes the index keep the order of what it finds from now on: the user
// records of watched uuids and the ends of turns, for Markers to hand out. What
// was fed before is not counted, so a caller that joins a file in progress feeds
// the history first and tracks after it.
func (ix *Index) Track() { ix.track = true }

// Markers returns the markers found since the last call, in the order of the
// file, and forgets them.
func (ix *Index) Markers() []Marker {
	out := ix.markers
	ix.markers = nil
	return out
}

// Offset is the number of bytes fed so far: where the next chunk of the file
// starts.
func (ix *Index) Offset() int64 { return ix.read }

// SessionSeen reports whether a line of the file names the session.
func (ix *Index) SessionSeen() bool { return ix.hasID }

// UserSeen reports whether a user record with the watched uuid was fed.
func (ix *Index) UserSeen(uuid string) bool { return ix.seen[uuid] }

// Recognized reports whether every user line fed carried a uuid and a session id,
// both strings. When it is false the record is in a form this package does not
// read, and no prompt may be called absent from it.
func (ix *Index) Recognized() bool { return !ix.unrecognized }

func (ix *Index) judge(line []byte) {
	interesting := bytes.Contains(line, userMarker) || (ix.track && bytes.Contains(line, turnEndMarker)) ||
		(!ix.hasID && bytes.Contains(line, []byte(ix.session)))
	if !interesting {
		for _, u := range ix.watched {
			if bytes.Contains(line, u) {
				interesting = true
				break
			}
		}
	}
	if !interesting {
		return
	}
	var raw struct {
		Type      string          `json:"type"`
		Subtype   string          `json:"subtype"`
		UUID      json.RawMessage `json:"uuid"`
		SessionID json.RawMessage `json:"sessionId"`
		Origin    struct {
			Kind string `json:"kind"`
		} `json:"origin"`
	}
	if json.Unmarshal(line, &raw) != nil {
		return
	}
	rec := struct{ Type, UUID, SessionID string }{raw.Type, text(raw.UUID), text(raw.SessionID)}
	if rec.Type == "user" && (rec.UUID == "" || rec.SessionID == "") {
		ix.unrecognized = true
	}
	if rec.SessionID == ix.session {
		ix.hasID = true
	}
	if _, ok := ix.watched[rec.UUID]; ok && rec.Type == "user" {
		ix.seen[rec.UUID] = true
		ix.hasID = true
		if ix.track {
			ix.markers = append(ix.markers, Marker{Kind: MarkPrompt, UUID: rec.UUID})
		}
	} else if ix.track && rec.Type == "user" && startsTurn(raw.Origin.Kind) {
		ix.markers = append(ix.markers, Marker{Kind: MarkTurnStart, UUID: rec.UUID})
	}
	if ix.track && rec.Type == "system" && raw.Subtype == "turn_duration" {
		ix.markers = append(ix.markers, Marker{Kind: MarkTurnEnd})
	}
}

// startsTurn reports whether a user record of this origin kind is a message that
// the runtime answers with a turn; the other user records (results of tools,
// attachments) carry no origin.
func startsTurn(kind string) bool {
	return kind == "human" || kind == "peer" || kind == "task-notification"
}

// text is the value of a JSON string; anything else, absent included, is "".
func text(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

// Write feeds p, so that an Index can be the destination of io.Copy.
func (ix *Index) Write(p []byte) (int, error) {
	ix.Feed(p)
	return len(p), nil
}
