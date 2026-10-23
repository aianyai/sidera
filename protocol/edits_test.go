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
	lengths map[int]bool   // string lengths, item counts and property counts
	numbers map[int64]bool // numeric limits
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
	c := constraints{lengths: map[int]bool{}, numbers: map[int64]bool{}}
	for _, n := range nodes {
		var all []node
		x.expand(n, nil, &all)
		for _, e := range all {
			m := e.body.(map[string]any)
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
	idx := newSchemaIndex(t)
	var c tally
	for _, tg := range targets {
		file, frag, _ := strings.Cut(tg.SchemaRef, "#")
		root := []node{idx.resolve(file, "#"+frag)}
		if frag == "" {
			root = []node{{file, idx.docs[file]}}
		}
		for _, raw := range seeds[tg.Name] {
			if len(raw) > 3000 {
				continue // the large documents are boundary cases themselves
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
