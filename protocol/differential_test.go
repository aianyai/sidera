package protocol_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// The differential tests feed the same document to the schema and to the Go
// decoder and require the same verdict. The schema cannot state a few rules (a
// range with min above max, a checkpoint that is too big, ...); for those Go may
// reject what the schema accepts, and says so with a rule beyond the schema. In
// every other case the two must agree, in both directions.

// plainIntegers reports whether every number in the document is written as a
// plain integer. The schema counts 1.0 and 1e2 as integers; the Go types, like
// the wire format, take only plain integers, so such documents are outside the
// comparison.
func plainIntegers(raw []byte) bool {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var walk func(v any) bool
	walk = func(v any) bool {
		switch x := v.(type) {
		case json.Number:
			return numberRE.MatchString(x.String())
		case []any:
			for _, e := range x {
				if !walk(e) {
					return false
				}
			}
		case map[string]any:
			for _, e := range x {
				if !walk(e) {
					return false
				}
			}
		}
		return true
	}
	var v any
	if err := dec.Decode(&v); err != nil {
		return false
	}
	return walk(v)
}

// duplicateFree reports whether no object of the document repeats a key. Parsers
// disagree about duplicates; the SDK rejects them and the schema validator
// takes the last value, so documents with duplicates are outside the comparison.
func duplicateFree(raw []byte) bool {
	dec := json.NewDecoder(bytes.NewReader(raw))
	var scan func() bool
	scan = func() bool {
		tok, err := dec.Token()
		if err != nil {
			return false
		}
		d, ok := tok.(json.Delim)
		if !ok {
			return true
		}
		switch d {
		case '{':
			seen := map[string]bool{}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil || seen[kt.(string)] {
					return false
				}
				seen[kt.(string)] = true
				if !scan() {
					return false
				}
			}
		case '[':
			for dec.More() {
				if !scan() {
					return false
				}
			}
		}
		_, err = dec.Token()
		return err == nil
	}
	return scan()
}

// inDomain reports whether raw is in the domain of the comparison: one valid
// UTF-8 JSON value with plain integers only and no repeated keys.
func inDomain(raw []byte) bool {
	if !utf8.Valid(raw) || !json.Valid(raw) {
		return false
	}
	return plainIntegers(raw) && duplicateFree(raw)
}

type verdict int

const (
	bothAccept verdict = iota
	bothReject
	goOnlyRejects // a rule beyond the schema
)

// differ compares the verdicts on raw. It returns an error describing a
// disagreement, or the kind of agreement.
func differ(tb testing.TB, tg target, raw []byte) (verdict, error) {
	tb.Helper()
	doc, err := parse(raw)
	if err != nil {
		return 0, nil // not in the domain
	}
	schemaErr := schemas(tb).accepts(tb, tg.SchemaRef, doc)
	_, goErr := tg.Go(raw)
	switch {
	case schemaErr == nil && goErr == nil:
		return bothAccept, nil
	case schemaErr != nil && goErr != nil:
		return bothReject, nil
	case schemaErr == nil && goErr != nil:
		if errors.Is(goErr, adapterproto.ErrBeyondSchema) {
			return goOnlyRejects, nil
		}
		return 0, fmt.Errorf("%s: Go rejects what the schema accepts, for a rule the schema could state\n  document: %s\n  Go: %v", tg.Name, raw, goErr)
	default:
		return 0, fmt.Errorf("%s: Go accepts what the schema rejects\n  document: %s\n  schema: %v", tg.Name, raw, schemaErr)
	}
}

// seedsFor collects, for every target, the documents the golden messages hold for
// it: the params or result of each example, and the parts of them that have a
// target of their own.
func seedsFor(tb testing.TB) map[string][][]byte {
	tb.Helper()
	seeds := map[string][][]byte{}
	methods := methodByName(tb)
	add := func(name string, raw []byte) {
		if len(raw) > 0 {
			seeds[name] = append(seeds[name], append([]byte(nil), raw...))
		}
	}
	for _, ex := range loadExamples(tb) {
		switch ex.Kind {
		case "manifest":
			add("manifest", ex.Raw)
			continue
		case "invalid":
			var w struct {
				Kind     string          `json:"kind"`
				Method   string          `json:"method"`
				Document json.RawMessage `json:"document"`
			}
			if err := json.Unmarshal(ex.Raw, &w); err != nil {
				tb.Fatal(err)
			}
			collectSeeds(tb, w.Kind, w.Method, w.Document, methods, add)
			continue
		}
		var f frame
		if err := json.Unmarshal(ex.Raw, &f); err != nil {
			tb.Fatal(err)
		}
		method := f.Method
		if ex.Kind == "result" {
			method = methodOfDir(tb, ex.Dir)
		}
		collectSeeds(tb, ex.Kind, method, ex.Raw, methods, add)
	}
	return seeds
}

func collectSeeds(tb testing.TB, kind, method string, doc []byte, methods map[string]schemaMethod, add func(string, []byte)) {
	var f frame
	if err := json.Unmarshal(doc, &f); err != nil {
		return // frame-level cases are not documents of any target
	}
	switch kind {
	case "request", "notification":
		if _, ok := methods[f.Method]; ok {
			add(f.Method+" params", f.Params)
		}
		if f.Method == adapterproto.MethodInitialize {
			return
		}
	case "result":
		if _, ok := methods[method]; ok {
			add(method+" result", f.Result)
		}
		var r struct {
			Runtime      json.RawMessage `json:"runtime"`
			Page         json.RawMessage `json:"page"`
			ControlReply json.RawMessage `json:"control_reply"`
			Capabilities json.RawMessage `json:"capabilities"`
			Process      json.RawMessage `json:"process"`
		}
		_ = json.Unmarshal(f.Result, &r)
		add("RecvPage", r.Page)
		add("ControlReply", r.ControlReply)
		add("Capabilities", r.Capabilities)
		add("ProcessIdentity", r.Process)
	case "error":
		add("ErrorObject", f.Error)
	}
	if kind == "request" || kind == "notification" {
		var p struct {
			Process  json.RawMessage `json:"process"`
			Sessions []struct {
				Process json.RawMessage `json:"process"`
			} `json:"sessions"`
		}
		_ = json.Unmarshal(f.Params, &p)
		add("ProcessIdentity", p.Process)
		for _, s := range p.Sessions {
			add("ProcessIdentity", s.Process)
		}
	}
}

// mutator rewrites a parsed document in small ways.
type mutator struct {
	rng  *rand.Rand
	pool []any // values to splice in
}

func newMutator(seed int64, strs []string) *mutator {
	m := &mutator{rng: rand.New(rand.NewSource(seed))}
	m.pool = []any{
		json.Number("0"), json.Number("1"), json.Number("-1"), json.Number("2"), json.Number("30"), json.Number("31"), json.Number("128"),
		json.Number("65535"), json.Number("65536"), json.Number("131072"), json.Number("131073"), json.Number("2147483647"), json.Number("2147483648"),
		json.Number("4294967295"), json.Number("4294967296"), json.Number("9007199254740991"), json.Number("9007199254740992"), json.Number("-9007199254740992"),
		"", "x", "X", " ", "a b", strings.Repeat("a", 64), strings.Repeat("a", 65), strings.Repeat("a", 128), strings.Repeat("a", 129),
		strings.Repeat("a", 256), strings.Repeat("a", 257), strings.Repeat("a", 1024), strings.Repeat("a", 1025), strings.Repeat("é", 129), "00000000000000000000000000000000",
		"0.0.0", "1.0", "../x", "/x", "é",
		true, false, nil, []any{}, map[string]any{}, []any{"cli"}, []any{"cli", "cli"}, []any{json.Number("1")}, map[string]any{"kind": "none"},
	}
	for _, s := range strs {
		m.pool = append(m.pool, s)
	}
	return m
}

// locations lists every container slot of the document.
type slot struct {
	parent any
	key    string
	index  int
}

func collect(v any, out *[]slot) {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			*out = append(*out, slot{parent: x, key: k, index: -1})
			collect(e, out)
		}
	case []any:
		for i, e := range x {
			*out = append(*out, slot{parent: x, index: i})
			collect(e, out)
		}
	}
}

func deepCopy(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = deepCopy(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = deepCopy(e)
		}
		return out
	}
	return v
}

func swapCase(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	if strings.ToUpper(string(r[0])) != string(r[0]) {
		r[0] = []rune(strings.ToUpper(string(r[0])))[0]
	} else {
		r[0] = []rune(strings.ToLower(string(r[0])))[0]
	}
	return string(r)
}

// mutate applies up to three edits to a copy of tree.
func (m *mutator) mutate(tree any) any {
	root := deepCopy(tree)
	holder := map[string]any{"": root}
	for n := 1 + m.rng.Intn(3); n > 0; n-- {
		var slots []slot
		collect(holder[""], &slots)
		if len(slots) == 0 {
			holder[""] = m.pool[m.rng.Intn(len(m.pool))]
			continue
		}
		s := slots[m.rng.Intn(len(slots))]
		set := func(v any) {
			switch p := s.parent.(type) {
			case map[string]any:
				p[s.key] = v
			case []any:
				p[s.index] = v
			}
		}
		get := func() any {
			switch p := s.parent.(type) {
			case map[string]any:
				return p[s.key]
			case []any:
				return p[s.index]
			}
			return nil
		}
		switch op := m.rng.Intn(9); op {
		case 0: // delete
			if p, ok := s.parent.(map[string]any); ok {
				delete(p, s.key)
			} else {
				set([]any{}) // arrays are edited through their parent below
			}
		case 1: // unknown key next to it
			if p, ok := s.parent.(map[string]any); ok {
				p["zz_unknown"] = json.Number("1")
			}
		case 2: // key differs in case
			if p, ok := s.parent.(map[string]any); ok {
				v := p[s.key]
				delete(p, s.key)
				p[swapCase(s.key)] = v
			}
		case 3:
			set(nil)
		case 4, 5:
			set(m.pool[m.rng.Intn(len(m.pool))])
		case 6: // tweak a string or number
			switch x := get().(type) {
			case string:
				switch m.rng.Intn(4) {
				case 0:
					set(strings.ToUpper(x))
				case 1:
					set(x + "x")
				case 2:
					if len(x) > 0 {
						set(x[:len(x)-1])
					}
				case 3:
					set(swapCase(x))
				}
			case json.Number:
				if i, err := x.Int64(); err == nil {
					set(json.Number(fmt.Sprint(i + int64(m.rng.Intn(3)) - 1)))
				}
			case bool:
				set(!x)
			}
		case 7: // repeat an array element
			if p, ok := s.parent.([]any); ok {
				p = append(p, p[s.index])
				// the parent slice header is held by its own parent; edit through the holder
				replaceSlice(holder, s.parent.([]any), p)
			}
		case 8: // empty an array or an object
			switch get().(type) {
			case []any:
				set([]any{})
			case map[string]any:
				set(map[string]any{})
			}
		}
	}
	return holder[""]
}

// replaceSlice swaps old for updated wherever old is held.
func replaceSlice(root map[string]any, old, updated []any) {
	var walk func(v any) any
	walk = func(v any) any {
		switch x := v.(type) {
		case map[string]any:
			for k, e := range x {
				x[k] = walk(e)
			}
		case []any:
			if len(x) > 0 && len(old) > 0 && &x[0] == &old[0] {
				return updated
			}
			for i, e := range x {
				x[i] = walk(e)
			}
		}
		return v
	}
	root[""] = walk(root[""])
}

func stringsIn(v any, out *[]string) {
	switch x := v.(type) {
	case string:
		if len(x) < 40 {
			*out = append(*out, x)
		}
	case []any:
		for _, e := range x {
			stringsIn(e, out)
		}
	case map[string]any:
		for k, e := range x {
			*out = append(*out, k)
			stringsIn(e, out)
		}
	}
}

// TestDifferentialMutants mutates every golden document many times and requires
// the schema and Go to agree on all of them. The generator is seeded, and the
// test counts how often each kind of agreement happened, so it cannot pass by
// never reaching the interesting cases.
func TestDifferentialMutants(t *testing.T) {
	targets := buildTargets(t)
	seeds := seedsFor(t)
	var pool []string
	for _, docs := range seeds {
		for _, d := range docs {
			var v any
			if dec := json.NewDecoder(bytes.NewReader(d)); dec != nil {
				dec.UseNumber()
				if dec.Decode(&v) == nil {
					stringsIn(v, &pool)
				}
			}
		}
	}
	m := newMutator(20261023, pool)
	const perSeed = 60
	var total, acc, rej, beyond int
	for _, tg := range targets {
		docs := seeds[tg.Name]
		if len(docs) == 0 {
			t.Errorf("no seed document for %s", tg.Name)
			continue
		}
		var tAcc, tRej int
		for _, raw := range docs {
			if v, err := differ(t, tg, raw); err != nil {
				t.Error(err)
			} else if v == bothAccept {
				tAcc++
			}
			var tree any
			dec := json.NewDecoder(bytes.NewReader(raw))
			dec.UseNumber()
			if err := dec.Decode(&tree); err != nil {
				t.Fatalf("seed for %s: %v", tg.Name, err)
			}
			for i := 0; i < perSeed; i++ {
				mut, err := json.Marshal(m.mutate(tree))
				if err != nil || !inDomain(mut) {
					continue
				}
				total++
				v, err := differ(t, tg, mut)
				if err != nil {
					t.Error(err)
					continue
				}
				switch v {
				case bothAccept:
					acc++
					tAcc++
				case bothReject:
					rej++
					tRej++
				case goOnlyRejects:
					beyond++
					tRej++
				}
			}
		}
		if tAcc == 0 || tRej == 0 {
			t.Errorf("%s: %d documents accepted and %d rejected; the comparison needs both", tg.Name, tAcc, tRej)
		}
	}
	t.Logf("%d mutants: %d accepted by both, %d rejected by both, %d rejected by Go for a rule beyond the schema", total, acc, rej, beyond)
	if total < 20000 || acc < 500 || rej < 10000 || beyond < 5 {
		t.Errorf("the mutation run did not reach the cases it exists for: %d total, %d accepted, %d rejected, %d beyond", total, acc, rej, beyond)
	}
}

// FuzzDifferential lets the fuzzer mutate the golden documents. Bytes outside the
// domain of the comparison (not UTF-8, not one JSON value, a number written as
// 1.0 or 1e2) are skipped.
func FuzzDifferential(f *testing.F) {
	targets := buildTargets(f)
	seeds := seedsFor(f)
	for i, tg := range targets {
		for _, raw := range seeds[tg.Name] {
			f.Add(uint8(i), raw)
		}
	}
	f.Fuzz(func(t *testing.T, sel uint8, raw []byte) {
		if !inDomain(raw) {
			t.Skip()
		}
		tg := targets[int(sel)%len(targets)]
		if _, err := differ(t, tg, raw); err != nil {
			t.Fatal(err)
		}
	})
}
