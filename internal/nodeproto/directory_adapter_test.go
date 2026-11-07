package nodeproto

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/authz"
	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// Golden bytes of a directory answer of one runtime endpoint, written by an
// independent CBOR encoder as the other vectors are: with the full adapter,
// with the instance and the adapter only, and with none (the 2.0.0 form).
const (
	goldenAdapterFull    = "a201010281a601500102030405060708090a0b0c0d0e0f10026772756e74696d6503644e616d65046005816373656506a40164776f726b0265636f6465780366302e31372e3004a20169436f64657820434c490267302e3136332e30"
	goldenAdapterMinimal = "a201010281a601500102030405060708090a0b0c0d0e0f10026772756e74696d6503644e616d65046005816373656506a20164776f726b0265636f646578"
	goldenAdapterNone    = "a201010281a501500102030405060708090a0b0c0d0e0f10026772756e74696d6503644e616d650460058163736565"
)

func runtimeEntry(adapter *DirectoryAdapter) DirectoryEntry {
	return DirectoryEntry{
		Endpoint: ep1, Kind: adapterreg.KindRuntime, DisplayName: "Name",
		Actions: []authz.Action{authz.ActionSee}, Adapter: adapter,
	}
}

func TestDirectoryEntryAdapterGolden(t *testing.T) {
	full := &DirectoryAdapter{Instance: "work", ID: "codex", Version: "0.17.0", Runtime: &adapterproto.RuntimeInfo{Name: "Codex CLI", Version: "0.163.0"}}
	minimal := &DirectoryAdapter{Instance: "work", ID: "codex"}
	for _, row := range []struct {
		name    string
		golden  string
		adapter *DirectoryAdapter
	}{
		{"{1: instance, 2: id, 3: version, 4: {1: name, 2: version}}", goldenAdapterFull, full},
		{"{1: instance, 2: id}", goldenAdapterMinimal, minimal},
		{"no adapter, as in 2.0.0", goldenAdapterNone, nil},
	} {
		t.Run(row.name, func(t *testing.T) {
			answer := DirectoryAnswer{Outcome: OutcomeOK, Entries: []DirectoryEntry{runtimeEntry(row.adapter)}}
			want := unhex(t, row.golden)
			got, err := answer.Encode()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("encoding differs:\n got %x\nwant %x", got, want)
			}
			back, err := DecodeDirectoryAnswer(want)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(back, answer) {
				t.Errorf("decoded %+v, want %+v", back, answer)
			}
		})
	}
}

func TestDecodeDirectoryAdapterRefuses(t *testing.T) {
	text, key := w.tstr, w.uint
	runtime := func(name, version []byte) []byte { return w.mapOf(key(1), name, key(2), version) }
	// adapter builds the adapter map from its members; a row replaces what it breaks.
	adapter := func(instance, id, version, rt []byte) []byte {
		kv := [][]byte{key(1), instance, key(2), id}
		if version != nil {
			kv = append(kv, key(3), version)
		}
		if rt != nil {
			kv = append(kv, key(4), rt)
		}
		return w.mapOf(kv...)
	}
	good := func(adapterBytes []byte) []byte {
		entry := w.mapOf(key(1), ep1Bytes, key(2), text("runtime"), key(3), text("n"), key(4), text(""), key(5), w.arr(text("see")), key(6), adapterBytes)
		return w.mapOf(key(1), key(1), key(2), w.arr(entry))
	}
	okRuntime := runtime(text("Codex CLI"), text("0.163.0"))
	var rows []refusal
	add := func(rule string, adapterBytes []byte, is error) {
		rows = append(rows, refusal{rule, good(adapterBytes), is})
	}

	add("an empty instance", adapter(text(""), text("codex"), nil, nil), ErrInvalid)
	add("an instance with an upper-case letter", adapter(text("Work"), text("codex"), nil, nil), ErrInvalid)
	add("an instance with a space", adapter(text("my work"), text("codex"), nil, nil), ErrInvalid)
	add("an instance with an escape sequence", adapter(text("w\x1b[2J"), text("codex"), nil, nil), ErrInvalid)
	add("an instance of 33 characters", adapter(text(strings.Repeat("a", 33)), text("codex"), nil, nil), ErrInvalid)
	add("an instance that starts with a hyphen", adapter(text("-work"), text("codex"), nil, nil), ErrInvalid)
	add("an empty adapter id", adapter(text("work"), text(""), nil, nil), ErrInvalid)
	add("an adapter id in upper case", adapter(text("work"), text("Codex"), nil, nil), ErrInvalid)
	add("an adapter id of 33 characters", adapter(text("work"), text(strings.Repeat("a", 33)), nil, nil), ErrInvalid)
	add("an adapter id with a newline", adapter(text("work"), text("co\ndex"), nil, nil), ErrInvalid)
	add("a version that is not semantic", adapter(text("work"), text("codex"), text("1.0"), nil), ErrInvalid)
	add("a version with a v prefix", adapter(text("work"), text("codex"), text("v1.0.0"), nil), ErrInvalid)
	add("a version of 65 characters", adapter(text("work"), text("codex"), text("1.0.0-"+strings.Repeat("a", 59)), nil), ErrInvalid)
	add("a version with an escape sequence", adapter(text("work"), text("codex"), text("1.0.0\x1b[2J"), nil), ErrInvalid)
	add("an empty version written out", adapter(text("work"), text("codex"), text(""), nil), record.ErrNotCanonical)
	add("an empty runtime name", adapter(text("work"), text("codex"), nil, runtime(text(""), text("1"))), ErrInvalid)
	add("an empty runtime version", adapter(text("work"), text("codex"), nil, runtime(text("n"), text(""))), ErrInvalid)
	add("a runtime name of 65 characters", adapter(text("work"), text("codex"), nil, runtime(text(strings.Repeat("é", 65)), text("1"))), ErrInvalid)
	add("a runtime version of 65 characters", adapter(text("work"), text("codex"), nil, runtime(text("n"), text(strings.Repeat("é", 65)))), ErrInvalid)
	add("a runtime name that is not UTF-8", adapter(text("work"), text("codex"), nil, runtime([]byte{0x62, 0xff, 0xfe}, text("1"))), record.ErrMalformed)
	// Both runtime texts are shown to a person and obey the rule of a display
	// name: a class a terminal acts on or that reorders text refuses the answer. Line
	// feed and tab are refused too, since neither is a name of a product.
	for _, c := range personTextClasses {
		add("a runtime name with "+c.name, adapter(text("work"), text("codex"), nil, runtime(text(c.text), text("1"))), ErrInvalid)
		add("a runtime version with "+c.name, adapter(text("work"), text("codex"), nil, runtime(text("n"), text(c.text))), ErrInvalid)
	}
	add("a runtime with no version", adapter(text("work"), text("codex"), nil, w.mapOf(key(1), text("n"))), record.ErrNotCanonical)
	add("an empty runtime", adapter(text("work"), text("codex"), nil, w.mapOf()), record.ErrNotCanonical)
	add("an unknown field of the runtime", adapter(text("work"), text("codex"), nil, w.mapOf(key(1), text("n"), key(2), text("1"), key(3), key(0))), record.ErrMalformed)
	add("an unknown field of the adapter", w.mapOf(key(1), text("work"), key(2), text("codex"), key(5), key(0)), record.ErrMalformed)
	add("an adapter with no id", w.mapOf(key(1), text("work")), record.ErrNotCanonical)
	add("an adapter with no instance", w.mapOf(key(2), text("codex")), record.ErrNotCanonical)
	add("an empty adapter", w.mapOf(), record.ErrNotCanonical)
	add("keys out of canonical order", w.mapOf(key(2), text("codex"), key(1), text("work")), record.ErrNotCanonical)
	add("an adapter that is not a map", text("codex"), record.ErrMalformed)
	runRefusals(t, DecodeDirectoryAnswer, rows)

	// What is valid in the same places is accepted, at the bounds.
	for _, a := range [][]byte{
		adapter(text("w"), text("c"), nil, nil),
		adapter(text(strings.Repeat("a", 32)), text(strings.Repeat("a", 32)), text("1.0.0-"+strings.Repeat("a", 58)), nil),
		adapter(text("work"), text("codex"), text("1.0.0"), runtime(text(strings.Repeat("\U0001F600", 64)), text(strings.Repeat("\U0001F600", 64)))),
		adapter(text("work"), text("codex"), nil, okRuntime),
	} {
		if _, err := DecodeDirectoryAnswer(good(a)); err != nil {
			t.Errorf("a valid adapter %x is refused: %v", a, err)
		}
	}
}

// Encode refuses what the decoder refuses, so a node never sends an answer every
// peer rejects.
func TestEncodeRefusesAnInvalidAdapter(t *testing.T) {
	for _, row := range []struct {
		rule string
		a    DirectoryAdapter
	}{
		{"no instance", DirectoryAdapter{ID: "codex"}},
		{"no id", DirectoryAdapter{Instance: "work"}},
		{"a version that is not semantic", DirectoryAdapter{Instance: "work", ID: "codex", Version: "latest"}},
		{"a runtime with no name", DirectoryAdapter{Instance: "work", ID: "codex", Runtime: &adapterproto.RuntimeInfo{Version: "1"}}},
		{"a runtime name with an escape sequence", DirectoryAdapter{Instance: "work", ID: "codex", Runtime: &adapterproto.RuntimeInfo{Name: "n\x1b[2J", Version: "1"}}},
	} {
		t.Run(row.rule, func(t *testing.T) {
			a := row.a
			_, err := DirectoryAnswer{Outcome: OutcomeOK, Entries: []DirectoryEntry{runtimeEntry(&a)}}.Encode()
			if !errors.Is(err, ErrInvalid) {
				t.Errorf("got %v, want an error wrapping ErrInvalid", err)
			}
		})
	}
}

// The directory is spoken under three identifiers, the newest first. Only the
// newest has an adapter in an entry; the older ones are the answers of the nodes
// before it, so an answer in one of them carries none and one that does is
// refused, as such a node would refuse it.
func TestDirectoryProtocolVersions(t *testing.T) {
	if got, want := Offer(ProtocolDirectory), []string{"/sidera/directory/3.0.0", "/sidera/directory/2.0.0", "/sidera/directory/1.0.0"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("offered %v, want %v", got, want)
	}
	if IsLegacy(PreviousProtocolDirectory) || !IsLegacy(LegacyProtocolDirectory) {
		t.Error("only the 1.0.0 stream is unframed")
	}
	for _, p := range []string{ProtocolDirectory, PreviousProtocolDirectory, LegacyProtocolDirectory} {
		if limit(p) != limit(ProtocolDirectory) {
			t.Errorf("%s has the limits %+v, want those of %s", p, limit(p), ProtocolDirectory)
		}
	}
	adapter := &DirectoryAdapter{Instance: "work", ID: "codex", Version: "0.17.0", Runtime: &adapterproto.RuntimeInfo{Name: "Codex CLI", Version: "0.163.0"}}
	answer := DirectoryAnswer{Outcome: OutcomeOK, Entries: []DirectoryEntry{runtimeEntry(adapter)}}
	for _, p := range []string{PreviousProtocolDirectory, LegacyProtocolDirectory} {
		t.Run(p, func(t *testing.T) {
			b, err := answer.EncodeFor(p)
			if err != nil {
				t.Fatal(err)
			}
			if want := unhex(t, goldenAdapterNone); !bytes.Equal(b, want) {
				t.Errorf("the answer in %s differs from the one without an adapter:\n got %x\nwant %x", p, b, want)
			}
			got, err := DecodeDirectoryAnswerFor(p, b)
			if err != nil || got.Entries[0].Adapter != nil {
				t.Errorf("decoded %+v, %v; want an entry with no adapter", got, err)
			}
			if _, err := DecodeDirectoryAnswerFor(p, unhex(t, goldenAdapterFull)); !errors.Is(err, ErrInvalid) {
				t.Errorf("an adapter in %s: %v, want ErrInvalid", p, err)
			}
		})
	}
	if _, err := answer.EncodeFor("/sidera/directory/9.0.0"); err == nil {
		t.Error("an answer in an identifier that is no directory protocol is written")
	}
	if _, err := DecodeDirectoryAnswerFor(ProtocolSend, unhex(t, goldenAdapterNone)); err == nil {
		t.Error("an answer in the identifier of another protocol is read")
	}
}
