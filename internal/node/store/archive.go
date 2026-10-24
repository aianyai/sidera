package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/aianyai/sidera/internal/node/recfile"
	"github.com/aianyai/sidera/sdk/go/strictjson"
)

// ArchiveSummary is the summary of a record that left the live files.
type ArchiveSummary struct {
	At          time.Time
	Kind        Kind
	Key         string // the decoded text of the key
	State       string
	Reason      string
	AcceptedAt  time.Time
	ClosedAt    time.Time
	DeliveredAt time.Time
	Size        int
	Digest      string
	Quarantined bool
}

// ArchiveContents is what an archive file holds that can be read.
type ArchiveContents struct {
	Summaries []ArchiveSummary
	Events    []ArchiveEvent
	// Dropped counts the lines that are neither empty nor intact.
	Dropped int
}

// A line of the archive is `{"crc":"<8 hex>","rec":<compact JSON>}` and a
// newline. The checksum is the CRC32C of the bytes of the record, so a line
// delimits itself, checks itself, and is still a JSON value.
const (
	linePrefix = `{"crc":"`
	lineMid    = `","rec":`
)

type summaryRec struct {
	K           string `json:"k"`
	At          wtime  `json:"at"`
	Kind        string `json:"kind"`
	Key         string `json:"key"`
	State       string `json:"state"`
	Reason      string `json:"reason,omitempty"`
	AcceptedAt  *wtime `json:"accepted_at,omitempty"`
	ClosedAt    *wtime `json:"closed_at,omitempty"`
	DeliveredAt *wtime `json:"delivered_at,omitempty"`
	Size        int    `json:"size"`
	Digest      string `json:"digest,omitempty"`
	Quarantined bool   `json:"quarantined"`
}

type eventRec struct {
	K  string `json:"k"`
	At wtime  `json:"at"`
	F  string `json:"f"`
	E  string `json:"e"`
	N  *int   `json:"n,omitempty"`
}

func archiveLine(rec any) ([]byte, error) {
	b, err := marshal(rec)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(b)+32)
	out = append(out, linePrefix...)
	out = append(out, recfile.ChecksumText(recfile.Checksum(b))...)
	out = append(out, lineMid...)
	out = append(out, b...)
	return append(out, '}', '\n'), nil
}

func summaryLine(s ArchiveSummary) ([]byte, error) {
	at, err := wreq(s.At, "the time of a summary")
	if err != nil {
		return nil, err
	}
	if !slices.Contains([]Kind{KindOut, KindOutCtl, KindIn, KindCtl}, s.Kind) || s.Key == "" || s.State == "" || s.Size < 0 {
		return nil, fmt.Errorf("store: a summary of kind %q, key %q, state %q, size %d", s.Kind, s.Key, s.State, s.Size)
	}
	return archiveLine(summaryRec{K: "summary", At: at, Kind: string(s.Kind), Key: s.Key, State: s.State, Reason: s.Reason,
		AcceptedAt: tptr(s.AcceptedAt), ClosedAt: tptr(s.ClosedAt), DeliveredAt: tptr(s.DeliveredAt), Size: s.Size, Digest: s.Digest, Quarantined: s.Quarantined})
}

func eventLine(e ArchiveEvent) ([]byte, error) {
	at, err := wreq(e.At, "the time of an event")
	if err != nil {
		return nil, err
	}
	if !isHashName(e.File) || e.Event == "" || (e.N != nil && *e.N < 0) {
		return nil, fmt.Errorf("store: an event %q of file %q", e.Event, e.File)
	}
	return archiveLine(eventRec{K: "ev", At: at, F: e.File, E: e.Event, N: e.N})
}

// ParseArchive reads the contents of an archive file. A newline is a point
// where reading resumes, an empty line is skipped, a line that is not intact
// is dropped and counted, and a last line without a newline is a torn tail and
// is ignored.
func ParseArchive(data []byte) ArchiveContents {
	var out ArchiveContents
	lines := bytes.Split(data, []byte("\n"))
	lines = lines[:len(lines)-1] // what follows the last newline is a torn tail, or nothing
	for _, line := range lines {
		if len(line) == 0 {
			continue
		}
		rec, ok := checkLine(line)
		if !ok {
			out.Dropped++
			continue
		}
		var k struct {
			K string `json:"k"`
		}
		_ = json.Unmarshal(rec, &k)
		switch k.K {
		case "summary":
			var s summaryRec
			if strictjson.Decode(rec, &s) == nil && s.K == "summary" {
				if sm, ok := fromSummary(s); ok {
					out.Summaries = append(out.Summaries, sm)
					continue
				}
			}
		case "ev":
			var e eventRec
			if strictjson.Decode(rec, &e) == nil && e.K == "ev" {
				if ev, ok := fromEvent(e); ok {
					out.Events = append(out.Events, ev)
					continue
				}
			}
		}
		out.Dropped++
	}
	return out
}

// checkLine returns the record of a line that has the prefix, a checksum that
// matches and the closing brace.
func checkLine(line []byte) ([]byte, bool) {
	rest, ok := bytes.CutPrefix(line, []byte(linePrefix))
	if !ok || len(rest) < 8 || !bytes.HasPrefix(rest[8:], []byte(lineMid)) || line[len(line)-1] != '}' {
		return nil, false
	}
	crc := string(rest[:8])
	if !recfile.ValidChecksumText(crc) {
		return nil, false
	}
	rec := rest[8+len(lineMid) : len(rest)-1]
	if recfile.ChecksumText(recfile.Checksum(rec)) != crc {
		return nil, false
	}
	return rec, true
}

func fromSummary(s summaryRec) (ArchiveSummary, bool) {
	sm := ArchiveSummary{At: time.Time(s.At), Kind: Kind(s.Kind), Key: s.Key, State: s.State, Reason: s.Reason,
		AcceptedAt: tval(s.AcceptedAt), ClosedAt: tval(s.ClosedAt), DeliveredAt: tval(s.DeliveredAt), Size: s.Size, Digest: s.Digest, Quarantined: s.Quarantined}
	ok := slices.Contains([]Kind{KindOut, KindOutCtl, KindIn, KindCtl}, sm.Kind) && sm.Key != "" && sm.State != "" && sm.Size >= 0
	return sm, ok
}

func fromEvent(e eventRec) (ArchiveEvent, bool) {
	ev := ArchiveEvent{At: time.Time(e.At), File: e.F, Event: e.E, N: e.N}
	return ev, isHashName(ev.File) && ev.Event != "" && (ev.N == nil || *ev.N >= 0)
}

// ReadArchive reads the archive file of the given name.
func (s *Store) ReadArchive(name string) (ArchiveContents, error) {
	if _, ok := archiveDate(name); !ok {
		return ArchiveContents{}, fmt.Errorf("store: %q is not the name of an archive file", name)
	}
	data, err := s.r.ReadFile(path.Join(ArchiveDir, name))
	if err != nil {
		return ArchiveContents{}, err
	}
	return ParseArchive(data), nil
}

const archiveLayout = "2006-01-02"

var archiveNameRE = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}\.jsonl$`)

// ArchiveDay is the name of the archive file of the UTC date of t.
func ArchiveDay(t time.Time) string { return t.UTC().Format(archiveLayout) + ".jsonl" }

// archiveDate is the date an archive file name stands for.
func archiveDate(name string) (time.Time, bool) {
	if !archiveNameRE.MatchString(name) {
		return time.Time{}, false
	}
	d, err := time.Parse(archiveLayout, strings.TrimSuffix(name, ".jsonl"))
	return d, err == nil
}

// ExpiredArchiveFiles returns the names of the archive files whose date is
// more than retentionDays days before the date of now, from the names of the
// directory. Names that are not archive file names are not returned.
func ExpiredArchiveFiles(names []string, now time.Time, retentionDays int) []string {
	today, _ := time.Parse(archiveLayout, now.UTC().Format(archiveLayout))
	cutoff := today.AddDate(0, 0, -retentionDays)
	var out []string
	for _, n := range names {
		if d, ok := archiveDate(n); ok && d.Before(cutoff) {
			out = append(out, n)
		}
	}
	return out
}
