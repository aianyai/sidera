package ledger

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/aianyai/sidera/sdk/go/strictjson"
)

// Version is the only format version.
const Version = 1

// Entry is one line of a ledger. Members are written in this order, which is the
// canonical form: Decode accepts exactly the line Encode writes for the entry
// it read.
type Entry struct {
	V int `json:"v"`
	// Writer names who wrote the entry: "adapter/<instance>", "runtime/<native
	// ref>", "agent/<endpoint>" or "fixture".
	Writer string `json:"writer"`
	// PID is the writing process; 0 for a writer that lives in the test process.
	PID int64 `json:"pid"`
	// Seq numbers the entries of one writer in one process, from 1 without gaps.
	Seq int64 `json:"seq"`
	// AtMS is the time on the shared test clock, in Unix milliseconds.
	AtMS int64 `json:"at_ms"`
	// Step is the script step the fixture had announced when the entry was
	// written; it may be empty.
	Step  string `json:"step"`
	Kind  string `json:"kind"`
	Attrs Attrs  `json:"attrs"`
}

// Attrs are the attributes of an entry. Which of them a kind takes, and which it
// requires, is the table in kinds.go. Message bodies never appear: only digests.
type Attrs struct {
	Instance          string  `json:"instance,omitempty"`
	LockPath          string  `json:"lock_path,omitempty"`
	Attempt           string  `json:"attempt,omitempty"`
	Preset            string  `json:"preset,omitempty"`
	Endpoint          string  `json:"endpoint,omitempty"`
	NativeRef         string  `json:"native_ref,omitempty"`
	RuntimeWriter     string  `json:"runtime_writer,omitempty"`
	RuntimeSeq        int64   `json:"runtime_seq,omitempty"`
	Outcome           string  `json:"outcome,omitempty"`
	TextDigest        string  `json:"text_digest,omitempty"`
	Lease             string  `json:"lease,omitempty"`
	Mode              string  `json:"mode,omitempty"`
	Verdict           string  `json:"verdict,omitempty"`
	Knob              string  `json:"knob,omitempty"`
	Detail            string  `json:"detail,omitempty"`
	CWD               string  `json:"cwd,omitempty"`
	PID               int64   `json:"pid,omitempty"`
	Turn              string  `json:"turn,omitempty"`
	MessageID         string  `json:"message_id,omitempty"`
	Part              int     `json:"part,omitempty"`
	Parts             int     `json:"parts,omitempty"`
	Count             int     `json:"count,omitempty"`
	PossiblyDuplicate *bool   `json:"possibly_duplicate,omitempty"`
	BodyDigest        string  `json:"body_digest,omitempty"`
	From              string  `json:"from,omitempty"`
	Feedback          string  `json:"feedback,omitempty"`
	Action            string  `json:"action,omitempty"`
	IdemKey           string  `json:"idem_key,omitempty"`
	Category          string  `json:"category,omitempty"`
	Code              string  `json:"code,omitempty"`
	Name              string  `json:"name,omitempty"`
	CrashPoint        string  `json:"crash_point,omitempty"`
	PIDs              []int64 `json:"pids,omitempty"`
}

var writerPattern = regexp.MustCompile(`^(fixture|(adapter|runtime|agent)/[A-Za-z0-9.:@-]{1,128})$`)

// ValidWriter reports whether name is a writer name.
func ValidWriter(name string) bool { return writerPattern.MatchString(name) }

// present returns the JSON names of the attributes that are set, and an error
// for a value that cannot be written back as it is (an empty list, which
// omitempty would drop, or text that is not UTF-8).
func (a Attrs) present() (map[string]bool, error) {
	out := map[string]bool{}
	v := reflect.ValueOf(a)
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		f := v.Field(i)
		switch f.Kind() {
		case reflect.String:
			if !utf8.ValidString(f.String()) {
				return nil, fmt.Errorf("attribute %s is not UTF-8", name)
			}
			if f.String() != "" {
				out[name] = true
			}
		case reflect.Int, reflect.Int64:
			if f.Int() < 0 {
				return nil, fmt.Errorf("attribute %s is negative", name)
			}
			if f.Int() != 0 {
				out[name] = true
			}
		case reflect.Pointer:
			if !f.IsNil() {
				out[name] = true
			}
		case reflect.Slice:
			if f.IsNil() {
				continue
			}
			if f.Len() == 0 {
				return nil, fmt.Errorf("attribute %s is an empty list: omit it", name)
			}
			for j := 0; j < f.Len(); j++ {
				if f.Index(j).Int() < 1 {
					return nil, fmt.Errorf("attribute %s holds %d: not a process id", name, f.Index(j).Int())
				}
			}
			out[name] = true
		default:
			return nil, fmt.Errorf("attribute %s has an unsupported type %s", name, f.Kind())
		}
	}
	return out, nil
}

// Validate checks every rule of an entry: the version, the writer name, the
// numbers, the kind, and that the attributes are exactly those the kind allows
// with all that it requires.
func (e Entry) Validate() error {
	if e.V != Version {
		return fmt.Errorf("version %d: only %d is defined", e.V, Version)
	}
	if !ValidWriter(e.Writer) {
		return fmt.Errorf("writer %q is not a writer name", e.Writer)
	}
	if e.PID < 0 {
		return fmt.Errorf("pid %d is negative", e.PID)
	}
	if e.Seq < 1 {
		return fmt.Errorf("seq %d: sequence numbers start at 1", e.Seq)
	}
	if e.AtMS < 0 {
		return fmt.Errorf("at_ms %d is negative", e.AtMS)
	}
	if !utf8.ValidString(e.Step) {
		return errors.New("step is not UTF-8")
	}
	spec, ok := kindSpecs[e.Kind]
	if !ok {
		return fmt.Errorf("unknown kind %q", e.Kind)
	}
	present, err := e.Attrs.present()
	if err != nil {
		return fmt.Errorf("kind %s: %w", e.Kind, err)
	}
	for _, name := range spec.required {
		if !present[name] {
			return fmt.Errorf("kind %s requires the attribute %s", e.Kind, name)
		}
	}
	for name := range present {
		if !spec.allows(name) {
			return fmt.Errorf("kind %s does not take the attribute %s", e.Kind, name)
		}
	}
	return e.validateRuntimeEntry(present)
}

// validateRuntimeEntry checks the one cross-writer reference the format has: a
// native.create.returned names the entry of the runtime that made the native
// session, by its writer and sequence number, exactly when it names a native
// session. A runtime entry that is never named could not be told from one that
// was lost, and a name without a session has nothing to point at.
func (e Entry) validateRuntimeEntry(present map[string]bool) error {
	if e.Kind != KindNativeCreateReturned {
		return nil
	}
	if present["native_ref"] != present["runtime_writer"] || present["native_ref"] != present["runtime_seq"] {
		return errors.New("kind native.create.returned carries native_ref, runtime_writer and runtime_seq together or none of them")
	}
	if present["runtime_writer"] && !strings.HasPrefix(e.Attrs.RuntimeWriter, "runtime/") {
		return fmt.Errorf("runtime_writer %q is not a runtime writer", e.Attrs.RuntimeWriter)
	}
	return nil
}

// Encode validates e and returns its canonical line, without a newline.
func Encode(e Entry) ([]byte, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(e); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// Decode reads one line, strictly: an unknown member, a missing one, a repeated
// one, null, and every rule of Validate are errors, and so is a line that is not
// the canonical form of the entry it holds.
func Decode(line []byte) (Entry, error) {
	var e Entry
	if err := strictjson.Decode(line, &e); err != nil {
		return Entry{}, err
	}
	if err := e.Validate(); err != nil {
		return Entry{}, err
	}
	canonical, err := Encode(e)
	if err != nil {
		return Entry{}, err
	}
	if !bytes.Equal(canonical, line) {
		return Entry{}, errors.New("not the canonical line of its entry")
	}
	return e, nil
}

// Kinds lists every kind, sorted.
func Kinds() []string {
	out := make([]string, 0, len(kindSpecs))
	for k := range kindSpecs {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
