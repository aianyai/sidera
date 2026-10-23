package protocol_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
)

// The two tests here are exhaustive where the mutation test is random. They work
// on the golden documents only, and one position of a document at a time.

// pos is a position in a document: object keys (strings) and array indexes (ints).
type pos []any

func (p pos) String() string {
	var b strings.Builder
	for _, s := range p {
		switch x := s.(type) {
		case string:
			b.WriteString("." + x)
		case int:
			fmt.Fprintf(&b, "[%d]", x)
		}
	}
	return b.String()
}

// positions lists every position that holds a value, parents before children.
func positions(v any, at pos, out *[]pos) {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			p := append(append(pos{}, at...), k)
			*out = append(*out, p)
			positions(x[k], p, out)
		}
	case []any:
		for i, e := range x {
			p := append(append(pos{}, at...), i)
			*out = append(*out, p)
			positions(e, p, out)
		}
	}
}

// parentOf walks to the container that holds the last step of p.
func parentOf(root any, p pos) any {
	cur := root
	for _, s := range p[:len(p)-1] {
		switch x := cur.(type) {
		case map[string]any:
			cur = x[s.(string)]
		case []any:
			cur = x[s.(int)]
		}
	}
	return cur
}

func at(root any, p pos) any {
	switch x := parentOf(root, p).(type) {
	case map[string]any:
		return x[p[len(p)-1].(string)]
	case []any:
		return x[p[len(p)-1].(int)]
	}
	return nil
}

func put(root any, p pos, v any) {
	switch x := parentOf(root, p).(type) {
	case map[string]any:
		x[p[len(p)-1].(string)] = v
	case []any:
		x[p[len(p)-1].(int)] = v
	}
}

func contains(ps []pos, p pos) bool {
	for _, q := range ps {
		if q.String() == p.String() {
			return true
		}
	}
	return false
}

func parseTree(tb testing.TB, raw []byte) (any, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var tree any
	if err := dec.Decode(&tree); err != nil {
		return nil, false
	}
	return tree, true
}

type tally struct{ total, accept, reject, beyond int }

func (c *tally) record(v verdict) {
	c.total++
	switch v {
	case bothAccept:
		c.accept++
	case bothReject:
		c.reject++
	case goOnlyRejects:
		c.beyond++
	}
}

// TestDifferentialSingleEdits tries every single edit at every position of every
// golden document: it leaves out, nulls and retypes each member, repeats a key in
// another case and adds an unknown key to each object. A member the Go side
// requires and the schema does not, or the other way round, shows here.
func TestDifferentialSingleEdits(t *testing.T) {
	targets := buildTargets(t)
	seeds := seedsFor(t, false)
	replacements := []any{nil, json.Number("0"), json.Number("-1"), json.Number("1"), "", "x", "zzz", true, false, []any{}, map[string]any{}}
	var c tally
	for _, tg := range targets {
		for _, raw := range seeds[tg.Name] {
			tree, ok := parseTree(t, raw)
			if !ok {
				continue
			}
			var ps []pos
			positions(tree, nil, &ps)
			try := func(what string, edit func(root any, p pos)) {
				for _, p := range ps {
					root := deepCopy(tree)
					edit(root, p)
					mut, err := json.Marshal(root)
					if err != nil || !inDomain(mut) {
						continue
					}
					v, err := differ(t, tg, mut)
					if err != nil {
						t.Errorf("%s: %s at %s: %v", tg.Name, what, p, err)
						continue
					}
					c.record(v)
				}
			}
			try("delete", func(root any, p pos) {
				if m, ok := parentOf(root, p).(map[string]any); ok {
					delete(m, p[len(p)-1].(string))
				}
			})
			for _, r := range replacements {
				r := r
				try(fmt.Sprintf("replace with %v", r), func(root any, p pos) { put(root, p, deepCopy(r)) })
			}
			try("rename in another case", func(root any, p pos) {
				if m, ok := parentOf(root, p).(map[string]any); ok {
					k := p[len(p)-1].(string)
					v := m[k]
					delete(m, k)
					m[swapCase(k)] = v
				}
			})
			try("add an unknown key", func(root any, p pos) {
				if m, ok := at(root, p).(map[string]any); ok {
					m["zz_unknown"] = json.Number("1")
				}
			})
			// Graft: every member another document of the same target has at the same
			// place and this one lacks, so that a member that belongs to one variant
			// of a message is tried in the others.
			for _, otherRaw := range seeds[tg.Name] {
				other, ok := parseTree(t, otherRaw)
				if !ok {
					continue
				}
				var ops []pos
				positions(other, nil, &ops)
				ops = append(ops, nil)
				for _, p := range ops {
					var dst, src map[string]any
					if len(p) == 0 {
						dst, _ = tree.(map[string]any)
						src, _ = other.(map[string]any)
					} else if !contains(ps, p) {
						continue
					} else {
						dst, _ = at(tree, p).(map[string]any)
						src, _ = at(other, p).(map[string]any)
					}
					if dst == nil || src == nil {
						continue
					}
					for k, v := range src {
						if _, has := dst[k]; has {
							continue
						}
						root := deepCopy(tree)
						var target map[string]any
						if len(p) == 0 {
							target = root.(map[string]any)
						} else {
							target = at(root, p).(map[string]any)
						}
						target[k] = deepCopy(v)
						mut, err := json.Marshal(root)
						if err != nil || !inDomain(mut) {
							continue
						}
						verdict, err := differ(t, tg, mut)
						if err != nil {
							t.Errorf("%s: grafting %q at %s: %v", tg.Name, k, p, err)
							continue
						}
						c.record(verdict)
					}
				}
			}
			// The document itself, too: an unknown key at the top.
			root := deepCopy(tree)
			if m, ok := root.(map[string]any); ok {
				m["zz_unknown"] = json.Number("1")
				if mut, err := json.Marshal(m); err == nil {
					if v, err := differ(t, tg, mut); err != nil {
						t.Errorf("%s: unknown key at the top: %v", tg.Name, err)
					} else {
						c.record(v)
					}
				}
			}
		}
	}
	t.Logf("%d single edits: %d accepted by both, %d rejected by both, %d rejected by Go for a rule beyond the schema", c.total, c.accept, c.reject, c.beyond)
	if c.total < 15000 || c.accept < 2000 || c.reject < 10000 || c.beyond < 10 {
		t.Errorf("too few edits reached: %+v", c)
	}
}

// constraints are the limits the schemas state for a position of a document.
type constraints struct {
	lengths map[int]bool    // string lengths, item counts and property counts
	numbers map[int64]bool  // numeric limits
	strings map[string]bool // the strings the schema names as members of an enum or as a const
}

// schemaIndex resolves references among the four schema files and finds the
// limits that apply at a position.
type schemaIndex struct {
	docs map[string]map[string]any
}

func newSchemaIndex(tb testing.TB) *schemaIndex {
	idx := &schemaIndex{docs: map[string]map[string]any{}}
	for _, n := range []string{"message", "types", "methods", "manifest"} {
		idx.docs[n+".schema.json"] = rawSchema(tb, n)
	}
	return idx
}

type node struct {
	file string
	body any
}

// resolve returns the object a reference names.
func (x *schemaIndex) resolve(file, ref string) node {
	target := file
	frag := ref
	if i := strings.Index(ref, "#"); i >= 0 {
		if i > 0 {
			target = ref[:i]
		}
		frag = ref[i+1:]
	}
	cur := any(x.docs[target])
	for _, part := range strings.Split(strings.TrimPrefix(frag, "/"), "/") {
		if part == "" {
			continue
		}
		cur = cur.(map[string]any)[part]
	}
	return node{target, cur}
}

// expand follows references and combinators to every schema object that applies
// to the same value.
func (x *schemaIndex) expand(n node, seen map[any]bool, out *[]node) {
	m, ok := n.body.(map[string]any)
	if !ok {
		return
	}
	*out = append(*out, n)
	if r, ok := m["$ref"].(string); ok {
		x.expand(x.resolve(n.file, r), seen, out)
	}
	for _, k := range []string{"allOf", "anyOf", "oneOf"} {
		if list, ok := m[k].([]any); ok {
			for _, e := range list {
				x.expand(node{n.file, e}, seen, out)
			}
		}
	}
	for _, k := range []string{"then", "else"} {
		if e, ok := m[k]; ok {
			x.expand(node{n.file, e}, seen, out)
		}
	}
}

// descend finds the schema objects for the child of the nodes at a step.
func (x *schemaIndex) descend(nodes []node, step any) []node {
	var out []node
	for _, n := range nodes {
		var all []node
		x.expand(n, nil, &all)
		for _, e := range all {
			m := e.body.(map[string]any)
			switch s := step.(type) {
			case string:
				if props, ok := m["properties"].(map[string]any); ok {
					if c, ok := props[s]; ok {
						out = append(out, node{e.file, c})
					}
				}
				if ap, ok := m["additionalProperties"].(map[string]any); ok {
					out = append(out, node{e.file, ap})
				}
			case int:
				if it, ok := m["items"].(map[string]any); ok {
					out = append(out, node{e.file, it})
				}
			}
		}
	}
	return out
}

func (x *schemaIndex) limits(nodes []node) constraints {
	c := constraints{lengths: map[int]bool{}, numbers: map[int64]bool{}, strings: map[string]bool{}}
	for _, n := range nodes {
		var all []node
		x.expand(n, nil, &all)
		for _, e := range all {
			m := e.body.(map[string]any)
			if list, ok := m["enum"].([]any); ok {
				for _, v := range list {
					if str, ok := v.(string); ok {
						c.strings[str] = true
					}
				}
			}
			if str, ok := m["const"].(string); ok {
				c.strings[str] = true
			}
			for k, v := range m {
				f, ok := v.(float64)
				if !ok {
					continue
				}
				switch k {
				case "maxLength", "minLength", "maxItems", "minItems", "maxProperties", "minProperties":
					c.lengths[int(f)] = true
				case "maximum", "minimum":
					c.numbers[int64(f)] = true
				}
			}
		}
	}
	return c
}

// TestDifferentialBoundaries tries, at every position of every golden document,
// the lengths, counts and numbers one below, at and one above each limit the
// schema states for that position. A limit that is a little different in Go than
// in the schema shows here.
func TestDifferentialBoundaries(t *testing.T) {
	targets := buildTargets(t)
	seeds := seedsFor(t, true)
	positive := map[string]bool{}
	for target, docs := range seedsFor(t, false) {
		for _, d := range docs {
			positive[target+"\x00"+string(d)] = true
		}
	}
	idx := newSchemaIndex(t)
	var c tally
	for _, tg := range targets {
		file, frag, _ := strings.Cut(tg.SchemaRef, "#")
		root := []node{idx.resolve(file, "#"+frag)}
		if frag == "" {
			root = []node{{file, idx.docs[file]}}
		}
		for _, raw := range seeds[tg.Name] {
			if len(raw) > 3000 && !positive[tg.Name+"\x00"+string(raw)] {
				continue // a large invalid document is a boundary case itself
			}
			tree, ok := parseTree(t, raw)
			if !ok {
				continue
			}
			var ps []pos
			positions(tree, nil, &ps)
			for _, p := range ps {
				nodes := root
				for _, step := range p {
					nodes = idx.descend(nodes, step)
				}
				if len(nodes) == 0 {
					continue
				}
				lim := idx.limits(nodes)
				var values []any
				switch old := at(tree, p).(type) {
				case string:
					for member := range lim.strings {
						values = append(values, member)
					}
					for l := range lim.lengths {
						for d := -1; d <= 1; d++ {
							if n := l + d; n >= 0 && n <= 9000 {
								values = append(values, strings.Repeat("a", n))
								if len(old) > 0 && n > 0 {
									values = append(values, (old + strings.Repeat("a", n))[:n])
								}
							}
						}
					}
				case json.Number:
					for b := range lim.numbers {
						for d := int64(-1); d <= 1; d++ {
							values = append(values, json.Number(fmt.Sprint(b+d)))
						}
					}
				case []any:
					if len(old) == 0 {
						continue
					}
					for l := range lim.lengths {
						for d := -1; d <= 1; d++ {
							if n := l + d; n >= 0 && n <= 1100 {
								arr := make([]any, n)
								for k := range arr {
									arr[k] = deepCopy(old[k%len(old)])
								}
								values = append(values, arr)
							}
						}
					}
				case map[string]any:
					if len(old) == 0 {
						continue
					}
					keys := make([]string, 0, len(old))
					for k := range old {
						keys = append(keys, k)
					}
					sort.Strings(keys)
					for l := range lim.lengths {
						for d := -1; d <= 1; d++ {
							if n := l + d; n >= 0 && n <= 1100 {
								m := map[string]any{}
								for _, k := range keys {
									if len(m) < n {
										m[k] = deepCopy(old[k])
									}
								}
								for k := 0; len(m) < n; k++ {
									m[fmt.Sprintf("%s_%d", keys[0], k)] = deepCopy(old[keys[0]])
								}
								values = append(values, m)
							}
						}
					}
				}
				values = append(values, discovered(tg, tree, p, at(tree, p))...)
				for _, v := range values {
					doc := deepCopy(tree)
					put(doc, p, v)
					mut, err := json.Marshal(doc)
					if err != nil || !inDomain(mut) {
						continue
					}
					verdict, err := differ(t, tg, mut)
					if err != nil {
						t.Errorf("%s at %s: %v", tg.Name, p, err)
						continue
					}
					c.record(verdict)
				}
			}
		}
	}
	t.Logf("%d boundary documents: %d accepted by both, %d rejected by both, %d rejected by Go for a rule beyond the schema", c.total, c.accept, c.reject, c.beyond)
	if c.total < 8000 || c.accept < 3000 || c.reject < 2000 || c.beyond < 100 {
		t.Errorf("too few boundary documents reached: %+v", c)
	}
}

// edge finds where acceptance of a family of values, indexed by an integer, starts
// and stops, by bisection: from is an index whose value is accepted, [min, max]
// the range to search. It returns the smallest and the largest accepted index
// found, and whether the family has a lower and an upper edge inside the range.
// It assumes acceptance is an interval, which holds for limits.
func edge(from, min, max int, accept func(int) bool) (lo, hi int, hasLo, hasHi bool) {
	lo, hi = from, from
	if !accept(max) {
		l, h := from, max // accept(l), !accept(h)
		for h-l > 1 {
			m := l + (h-l)/2
			if accept(m) {
				l = m
			} else {
				h = m
			}
		}
		hi, hasHi = l, true
	}
	if !accept(min) {
		l, h := min, from // !accept(l), accept(h)
		for h-l > 1 {
			m := l + (h-l)/2
			if accept(m) {
				h = m
			} else {
				l = m
			}
		}
		lo, hasLo = h, true
	}
	return
}

// discovered returns the values one below, at and one above the edges of what the
// Go side accepts at a position, found by bisection, so that a limit the schema
// lacks or states differently is probed even though the schema does not mention
// it.
func discovered(tg target, tree any, p pos, old any) []any {
	goAccepts := func(v any) bool {
		doc := deepCopy(tree)
		put(doc, p, v)
		raw, err := json.Marshal(doc)
		if err != nil || !inDomain(raw) {
			return false
		}
		_, err = tg.Go(raw)
		return err == nil
	}
	var out []any
	var variant func(n int) any
	var from, min, max int
	switch x := old.(type) {
	case string:
		from, min, max = len([]rune(x)), 0, 9000
		runes := []rune(x)
		variant = func(n int) any {
			if n <= len(runes) {
				return string(runes[:n])
			}
			return x + strings.Repeat("a", n-len(runes))
		}
	case []any:
		if len(x) == 0 {
			return nil
		}
		from, min, max = len(x), 0, 1100
		variant = func(n int) any {
			arr := make2(n)
			for k := range arr {
				arr[k] = deepCopy(x[k%len(x)])
			}
			return arr
		}
	case json.Number:
		n, err := x.Int64()
		if err != nil {
			return nil
		}
		// The numbers are searched as offsets from the original value, in two scales.
		return discoveredNumbers(goAccepts, n)
	case map[string]any:
		if len(x) == 0 {
			return nil
		}
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		from, min, max = len(x), 0, 1100
		variant = func(n int) any {
			m := map[string]any{}
			for _, k := range keys {
				if len(m) < n {
					m[k] = deepCopy(x[k])
				}
			}
			for k := 0; len(m) < n; k++ {
				m[fmt.Sprintf("%s_%d", keys[0], k)] = deepCopy(x[keys[0]])
			}
			return m
		}
	default:
		return nil
	}
	if m, ok := old.(map[string]any); ok && len(m) > 0 {
		out = append(out, discoveredKeys(goAccepts, m)...)
	}
	if !goAccepts(variant(from)) {
		return out
	}
	lo, hi, hasLo, hasHi := edge(from, min, max, func(n int) bool { return goAccepts(variant(n)) })
	if hasHi {
		out = append(out, variant(hi), variant(hi+1))
	}
	if hasLo {
		out = append(out, variant(lo), variant(lo-1))
	}
	return out
}

func make2(n int) []any {
	if n < 0 {
		n = 0
	}
	return make([]any, n)
}

// discoveredNumbers bisects the edges of an integer position on both sides.
func discoveredNumbers(goAccepts func(any) bool, anchor int64) []any {
	const bound = int64(1) << 54
	accept := func(n int64) bool { return goAccepts(json.Number(fmt.Sprint(n))) }
	if !accept(anchor) {
		return nil
	}
	var out []any
	if !accept(bound) {
		l, h := anchor, bound
		for h-l > 1 {
			m := l + (h-l)/2
			if accept(m) {
				l = m
			} else {
				h = m
			}
		}
		out = append(out, json.Number(fmt.Sprint(l)), json.Number(fmt.Sprint(l+1)))
	}
	if !accept(-bound) {
		l, h := -bound, anchor
		for h-l > 1 {
			m := l + (h-l)/2
			if accept(m) {
				h = m
			} else {
				l = m
			}
		}
		out = append(out, json.Number(fmt.Sprint(h)), json.Number(fmt.Sprint(h-1)))
	}
	return out
}

// discoveredKeys bisects how long the first key of an object may be, by padding it
// with letters, for objects whose keys are names with a length limit.
func discoveredKeys(goAccepts func(any) bool, m map[string]any) []any {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	key := keys[0]
	rekey := func(n int) any {
		name := key
		if r := []rune(key); n <= len(r) {
			name = string(r[:n])
		} else {
			name = key + strings.Repeat("a", n-len(r))
		}
		out := map[string]any{}
		for _, k := range keys {
			if k != key {
				out[k] = deepCopy(m[k])
			}
		}
		out[name] = deepCopy(m[key])
		return out
	}
	from := len([]rune(key))
	if !goAccepts(rekey(from)) {
		return nil
	}
	lo, hi, hasLo, hasHi := edge(from, 1, 600, func(n int) bool { return n >= 1 && goAccepts(rekey(n)) })
	var out []any
	if hasHi {
		out = append(out, rekey(hi), rekey(hi+1))
	}
	if hasLo {
		out = append(out, rekey(lo), rekey(lo-1))
	}
	return out
}
