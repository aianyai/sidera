package nodeproto

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/envelope"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
)

type refusal struct {
	rule  string
	input []byte
	is    error // the sentinel the refusal wraps
}

func runRefusals[T any](t *testing.T, decode func([]byte) (T, error), rows []refusal) {
	t.Helper()
	for _, row := range rows {
		t.Run(row.rule, func(t *testing.T) {
			if _, err := decode(row.input); err == nil {
				t.Fatal("accepted")
			} else if !errors.Is(err, row.is) {
				t.Errorf("error %v does not wrap %v", err, row.is)
			}
		})
	}
}

func TestDecodeSendRequestRefuses(t *testing.T) {
	env := w.bstr([]byte{1})
	sender := w.tstr(ep1Text)
	runRefusals(t, DecodeSendRequest, []refusal{
		{"empty input", nil, record.ErrMalformed},
		{"input over the request limit is refused before decoding", bytes.Repeat([]byte{0}, limit(ProtocolSend).Request+1), record.ErrOversize},
		{"trailing bytes", append(w.mapOf(w.uint(1), sender, w.uint(2), env), 0), record.ErrMalformed},
		{"a duplicate key", w.mapOf(w.uint(1), sender, w.uint(1), sender, w.uint(2), env), record.ErrMalformed},
		{"an unknown field", w.mapOf(w.uint(1), sender, w.uint(2), env, w.uint(3), w.uint(0)), record.ErrMalformed},
		{"keys out of canonical order", w.mapOf(w.uint(2), env, w.uint(1), sender), record.ErrNotCanonical},
		{"a missing sender", w.mapOf(w.uint(2), env), record.ErrNotCanonical},
		{"a sender that is not an endpoint text", w.mapOf(w.uint(1), w.tstr("ep1nonsense"), w.uint(2), env), ErrInvalid},
		{"a node text in place of the endpoint text", w.mapOf(w.uint(1), w.tstr("node1qqqqqqqqqqqqqq"), w.uint(2), env), ErrInvalid},
		{"a sender in upper case, a second spelling of one endpoint", w.mapOf(w.uint(1), w.tstr(strings.ToUpper(ep1Text)), w.uint(2), env), ErrInvalid},
		{"an empty envelope", w.mapOf(w.uint(1), sender, w.uint(2), w.bstr(nil)), ErrInvalid},
		{"an envelope one byte over the longest the protocol allows", w.mapOf(w.uint(1), sender, w.uint(2), w.bstr(make([]byte, envelope.MaxEncoded()+1))), ErrInvalid},
		{"an envelope that is not a byte string", w.mapOf(w.uint(1), sender, w.uint(2), w.tstr("x")), record.ErrMalformed},
	})
}

func TestDecodeSendAnswerRefuses(t *testing.T) {
	reports := func(n int) []byte { return w.arr(repeatItem(recBytes, n)...) }
	ok := w.uint(1)
	runRefusals(t, DecodeSendAnswer, []refusal{
		{"empty input", nil, record.ErrMalformed},
		{"input over the answer limit", bytes.Repeat([]byte{0}, limit(ProtocolSend).Answer+1), record.ErrOversize},
		{"outcome zero", w.mapOf(w.uint(1), w.uint(0), w.uint(2), reports(0)), ErrInvalid},
		{"an outcome above failed", w.mapOf(w.uint(1), w.uint(4), w.uint(2), reports(0)), ErrInvalid},
		{"a refusal with no reason", w.mapOf(w.uint(1), w.uint(2), w.uint(2), reports(0)), ErrInvalid},
		{"a reason on an ok answer", w.mapOf(w.uint(1), ok, w.uint(2), reports(0), w.uint(4), w.tstr("x")), ErrInvalid},
		{"a reason on a failed answer", w.mapOf(w.uint(1), w.uint(3), w.uint(2), reports(0), w.uint(4), w.tstr("x")), ErrInvalid},
		{"a reason with a character outside a-z, 0-9 and underscore", w.mapOf(w.uint(1), w.uint(2), w.uint(2), reports(0), w.uint(4), w.tstr("Bad reason")), ErrInvalid},
		{"a reason over the longest name", w.mapOf(w.uint(1), w.uint(2), w.uint(2), reports(0), w.uint(4), w.tstr(strings.Repeat("a", MaxReasonLen+1))), ErrInvalid},
		{"reports on a refused answer", w.mapOf(w.uint(1), w.uint(2), w.uint(2), reports(1), w.uint(4), w.tstr("x")), ErrInvalid},
		{"a control reply on a failed answer", w.mapOf(w.uint(1), w.uint(3), w.uint(2), reports(0), w.uint(3), recBytes), ErrInvalid},
		{"more reports than an envelope has recipients", w.mapOf(w.uint(1), ok, w.uint(2), reports(MaxRecipients+1)), ErrInvalid},
		{"a report that is not a well-formed record", w.mapOf(w.uint(1), ok, w.uint(2), w.arr(w.bstr([]byte{0xa0}))), record.ErrNotCanonical},
		{"a control reply that is not a well-formed record", w.mapOf(w.uint(1), ok, w.uint(2), reports(0), w.uint(3), w.bstr([]byte{0xa0})), record.ErrNotCanonical},
		{"an empty control reply written out", w.mapOf(w.uint(1), ok, w.uint(2), reports(0), w.uint(3), w.bstr(nil)), record.ErrNotCanonical},
		{"a missing report list", w.mapOf(w.uint(1), ok), record.ErrNotCanonical},
		{"keys out of canonical order", w.mapOf(w.uint(2), reports(0), w.uint(1), ok), record.ErrNotCanonical},
		{"an unknown field", w.mapOf(w.uint(1), ok, w.uint(2), reports(0), w.uint(5), w.uint(0)), record.ErrMalformed},
	})
}

func TestDecodePullRequestRefuses(t *testing.T) {
	recipients := func(items ...[]byte) []byte { return w.arr(items...) }
	msg := w.tstr("m-1")
	runRefusals(t, DecodePullRequest, []refusal{
		{"empty input", nil, record.ErrMalformed},
		{"input over the request limit", bytes.Repeat([]byte{0}, limit(ProtocolPull).Request+1), record.ErrOversize},
		{"an empty message identifier", w.mapOf(w.uint(1), w.tstr(""), w.uint(2), recipients(ep1Bytes)), envelope.ErrID},
		{"a message identifier with a space", w.mapOf(w.uint(1), w.tstr("a b"), w.uint(2), recipients(ep1Bytes)), envelope.ErrID},
		{"a message identifier over the longest", w.mapOf(w.uint(1), w.tstr(strings.Repeat("a", envelope.MaxIDLen+1)), w.uint(2), recipients(ep1Bytes)), envelope.ErrID},
		{"no recipient", w.mapOf(w.uint(1), msg, w.uint(2), recipients()), ErrInvalid},
		{"more recipients than an envelope has", w.mapOf(w.uint(1), msg, w.uint(2), recipients(distinctEndpoints(MaxRecipients+1)...)), ErrInvalid},
		{"a recipient listed twice", w.mapOf(w.uint(1), msg, w.uint(2), recipients(ep1Bytes, ep1Bytes)), ErrInvalid},
		{"the zero endpoint", w.mapOf(w.uint(1), msg, w.uint(2), recipients(zeroBytes)), ErrInvalid},
		{"an endpoint of the wrong length", w.mapOf(w.uint(1), msg, w.uint(2), recipients(w.bstr([]byte{1, 2, 3}))), ErrInvalid},
		{"a missing recipient list", w.mapOf(w.uint(1), msg), record.ErrNotCanonical},
		{"an unknown field", w.mapOf(w.uint(1), msg, w.uint(2), recipients(ep1Bytes), w.uint(3), w.uint(0)), record.ErrMalformed},
	})
}

func TestDecodePullAnswerRefuses(t *testing.T) {
	list := func(items ...[]byte) []byte { return w.arr(items...) }
	ok := w.uint(1)
	runRefusals(t, DecodePullAnswer, []refusal{
		{"empty input", nil, record.ErrMalformed},
		{"input over the answer limit", bytes.Repeat([]byte{0}, limit(ProtocolPull).Answer+1), record.ErrOversize},
		{"an outcome above failed", w.mapOf(w.uint(1), w.uint(4), w.uint(2), list(), w.uint(3), list()), ErrInvalid},
		{"a refusal with no reason", w.mapOf(w.uint(1), w.uint(2), w.uint(2), list(), w.uint(3), list()), ErrInvalid},
		{"a no-record entry on a failed answer", w.mapOf(w.uint(1), w.uint(3), w.uint(2), list(), w.uint(3), list(ep1Bytes)), ErrInvalid},
		{"a report on a refused answer", w.mapOf(w.uint(1), w.uint(2), w.uint(2), list(recBytes), w.uint(3), list(), w.uint(5), w.tstr("x")), ErrInvalid},
		{"reports and no-record entries over what a request names", w.mapOf(w.uint(1), ok, w.uint(2), list(repeatItem(recBytes, 30)...), w.uint(3), list(distinctEndpoints(30)...)), ErrInvalid},
		{"a no-record endpoint listed twice", w.mapOf(w.uint(1), ok, w.uint(2), list(), w.uint(3), list(ep1Bytes, ep1Bytes)), ErrInvalid},
		{"the zero endpoint as no-record", w.mapOf(w.uint(1), ok, w.uint(2), list(), w.uint(3), list(zeroBytes)), ErrInvalid},
		{"a report that is not a well-formed record", w.mapOf(w.uint(1), ok, w.uint(2), list(w.bstr([]byte{0xa0})), w.uint(3), list()), record.ErrNotCanonical},
		{"a missing no-record list", w.mapOf(w.uint(1), ok, w.uint(2), list()), record.ErrNotCanonical},
	})
}

func TestDecodeDirectoryRequestRefuses(t *testing.T) {
	sender := w.tstr(ep1Text)
	runRefusals(t, DecodeDirectoryRequest, []refusal{
		{"empty input", nil, record.ErrMalformed},
		{"input over the request limit", bytes.Repeat([]byte{0}, limit(ProtocolDirectory).Request+1), record.ErrOversize},
		{"a missing sender", w.mapOf(w.uint(3), w.uint(5)), record.ErrNotCanonical},
		{"a sender that is not an endpoint text", w.mapOf(w.uint(1), w.tstr("x")), ErrInvalid},
		{"a limit of zero written out", w.mapOf(w.uint(1), sender, w.uint(3), w.uint(0)), record.ErrNotCanonical},
		{"a limit above the endpoints a node has", w.mapOf(w.uint(1), sender, w.uint(3), w.uint(MaxDirectoryPage+1)), ErrInvalid},
		{"a limit that is not a number", w.mapOf(w.uint(1), sender, w.uint(3), w.tstr("5")), record.ErrMalformed},
		{"an empty cursor written out", w.mapOf(w.uint(1), sender, w.uint(2), w.tstr("")), record.ErrNotCanonical},
		{"a cursor with a space", w.mapOf(w.uint(1), sender, w.uint(2), w.tstr("a b")), ErrInvalid},
		{"a cursor over the longest", w.mapOf(w.uint(1), sender, w.uint(2), w.tstr(strings.Repeat("a", MaxCursorLen+1))), ErrInvalid},
		{"an unknown field", w.mapOf(w.uint(1), sender, w.uint(4), w.uint(0)), record.ErrMalformed},
	})
}

func TestDecodeDirectoryAnswerRefuses(t *testing.T) {
	entry := func(fields ...[]byte) []byte { return w.arr(fields...) }
	good := func(over ...[2][]byte) []byte { // an entry whose fields the rows replace
		f := map[uint64][]byte{
			1: ep1Bytes, 2: w.tstr("direct"), 3: w.tstr("n"), 4: w.tstr(""), 5: w.arr(w.tstr("see")),
		}
		for _, o := range over {
			f[uint64(o[0][0])] = o[1]
		}
		var kv [][]byte
		for k := uint64(1); k <= 5; k++ {
			kv = append(kv, w.uint(k), f[k])
		}
		return w.mapOf(kv...)
	}
	with := func(n uint64, v []byte) [2][]byte { return [2][]byte{{byte(n)}, v} }
	answer := func(entries ...[]byte) []byte { return w.mapOf(w.uint(1), w.uint(1), w.uint(2), entry(entries...)) }
	runRefusals(t, DecodeDirectoryAnswer, []refusal{
		{"empty input", nil, record.ErrMalformed},
		{"input over the answer limit", bytes.Repeat([]byte{0}, limit(ProtocolDirectory).Answer+1), record.ErrOversize},
		{"a refusal with no reason", w.mapOf(w.uint(1), w.uint(2), w.uint(2), entry()), ErrInvalid},
		{"an entry on a failed answer", w.mapOf(w.uint(1), w.uint(3), w.uint(2), entry(good())), ErrInvalid},
		{"a next cursor on a refused answer", w.mapOf(w.uint(1), w.uint(2), w.uint(2), entry(), w.uint(3), w.tstr("c"), w.uint(4), w.tstr("x")), ErrInvalid},
		{"a next cursor with a space", w.mapOf(w.uint(1), w.uint(1), w.uint(2), entry(), w.uint(3), w.tstr("a b")), ErrInvalid},
		{"more entries than a node has endpoints", answer(repeatItem(good(), MaxDirectoryPage+1)...), ErrInvalid},
		{"an unknown kind", answer(good(with(2, w.tstr("pigeon")))), ErrInvalid},
		{"an unknown action", answer(good(with(5, w.arr(w.tstr("fly"))))), ErrInvalid},
		{"an action listed twice", answer(good(with(5, w.arr(w.tstr("see"), w.tstr("see"))))), ErrInvalid},
		{"the zero endpoint", answer(good(with(1, zeroBytes))), ErrInvalid},
		{"a display name of 129 characters", answer(good(with(3, w.tstr(strings.Repeat("é", 129))))), ErrInvalid},
		{"a description of 1025 characters", answer(good(with(4, w.tstr(strings.Repeat("é", 1025))))), ErrInvalid},
		{"a display name that is not UTF-8", answer(good(with(3, []byte{0x62, 0xff, 0xfe}))), record.ErrMalformed},
		{"an entry missing a field", answer(w.mapOf(w.uint(1), ep1Bytes, w.uint(2), w.tstr("direct"))), record.ErrNotCanonical},
		{"an entry with an unknown field", answer(w.mapOf(w.uint(1), ep1Bytes, w.uint(2), w.tstr("direct"), w.uint(3), w.tstr("n"), w.uint(4), w.tstr(""), w.uint(5), w.arr(), w.uint(6), w.uint(0))), record.ErrMalformed},
	})
}

// A message the package writes is a message the package reads: Encode refuses
// what the decoder would, so a node never sends a message every peer refuses.
func TestEncodeRefusesWhatDecodeRefuses(t *testing.T) {
	rec := mustRecord(t)
	for _, row := range []struct {
		rule string
		fn   func() ([]byte, error)
		is   error
	}{
		{"a send request with no sender", SendRequest{Envelope: []byte{1}}.Encode, ErrInvalid},
		{"a send request with no envelope", SendRequest{Sender: ep1}.Encode, ErrInvalid},
		{"a send answer with an unset outcome", SendAnswer{}.Encode, ErrInvalid},
		{"a refused send answer with no reason", SendAnswer{Outcome: OutcomeRefused}.Encode, ErrInvalid},
		{"a failed send answer with a report", SendAnswer{Outcome: OutcomeFailed, Reports: []record.Record{rec}}.Encode, ErrInvalid},
		{"a record that does not encode", SendAnswer{Outcome: OutcomeOK, Reports: []record.Record{{}}}.Encode, record.ErrType},
		{"a pull request with no recipient", PullRequest{Message: "m"}.Encode, ErrInvalid},
		{"a pull answer that lists a recipient twice", PullAnswer{Outcome: OutcomeOK, NoRecord: []ident.EndpointID{ep1, ep1}}.Encode, ErrInvalid},
		{"a directory request with a negative limit", DirectoryRequest{Sender: ep1, Limit: -1}.Encode, ErrInvalid},
		{"a directory entry of an unset kind", DirectoryAnswer{Outcome: OutcomeOK, Entries: []DirectoryEntry{{Endpoint: ep1}}}.Encode, ErrInvalid},
	} {
		t.Run(row.rule, func(t *testing.T) {
			if _, err := row.fn(); !errors.Is(err, row.is) {
				t.Errorf("got %v, want an error wrapping %v", err, row.is)
			}
		})
	}
}
