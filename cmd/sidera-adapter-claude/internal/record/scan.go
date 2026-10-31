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
type Index struct {
	session string
	watched map[string][]byte
	seen    map[string]bool
	hasID   bool
	partial []byte
	read    int64
}

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

// Offset is the number of bytes fed so far: where the next chunk of the file
// starts.
func (ix *Index) Offset() int64 { return ix.read }

// SessionSeen reports whether a line of the file names the session.
func (ix *Index) SessionSeen() bool { return ix.hasID }

// UserSeen reports whether a user record with the watched uuid was fed.
func (ix *Index) UserSeen(uuid string) bool { return ix.seen[uuid] }

func (ix *Index) judge(line []byte) {
	interesting := !ix.hasID && bytes.Contains(line, []byte(ix.session))
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
	var rec struct {
		Type      string `json:"type"`
		UUID      string `json:"uuid"`
		SessionID string `json:"sessionId"`
	}
	if json.Unmarshal(line, &rec) != nil {
		return
	}
	if rec.SessionID == ix.session {
		ix.hasID = true
	}
	if _, ok := ix.watched[rec.UUID]; ok && rec.Type == "user" {
		ix.seen[rec.UUID] = true
		ix.hasID = true
	}
}

// Write feeds p, so that an Index can be the destination of io.Copy.
func (ix *Index) Write(p []byte) (int, error) {
	ix.Feed(p)
	return len(p), nil
}
