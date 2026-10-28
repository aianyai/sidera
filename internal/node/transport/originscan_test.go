package transport_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"sort"
	"strings"
	"testing"
)

// originViolations reads the parsed, non-test sources of the package, by file
// name, and returns one line for each place that breaks a rule of
// TestNoCallerCanClaimTheLocalOrigin. It reads source text only: it does not
// see what the type checker, unsafe or reflection could do with an Origin.
//
// The rules are:
//
//   - Origin has no exported field and no exported method with a pointer
//     receiver, and no exported function, method, variable or field of an
//     exported type returns or holds an Origin, except RemoteOrigin. Code that
//     imports the package cannot reach an unexported field, so this list is the
//     whole outside surface.
//   - Outside loopback.go no code refers to localOrigin at all, called or not.
//     Inside loopback.go it appears only as a direct argument of a call of the
//     handler's Inbound, Pull or QueryDirectory, so it is never kept, returned or passed on.
//   - Outside origin.go no composite literal of type Origin has elements, keyed
//     or not, nothing converts to Origin and no type is declared as another name
//     for it. The only literals that remain are the empty Origin{} and the ones
//     in origin.go.
//   - The field that says "local" is set to anything but false in origin.go only
//     and is assigned nowhere.
func originViolations(files map[string]*ast.File) []string {
	var out []string
	report := func(format string, args ...any) { out = append(out, fmt.Sprintf(format, args...)) }
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		for _, d := range files[name].Decls {
			surfaceViolations(name, d, report)
		}
	}

	// The declaration of localOrigin is the one reference that is not a use.
	declared := map[*ast.Ident]bool{}
	if f := files["origin.go"]; f != nil {
		for _, d := range f.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil && fd.Name.Name == "localOrigin" {
				declared[fd.Name] = true
			}
		}
	}
	for _, name := range names {
		f := files[name]
		allowed := map[*ast.Ident]bool{}
		if name == "loopback.go" {
			ast.Inspect(f, func(n ast.Node) bool {
				if c, ok := n.(*ast.CallExpr); ok && callsHandler(c) {
					for _, a := range c.Args {
						if ac, ok := a.(*ast.CallExpr); ok {
							if id, ok := ac.Fun.(*ast.Ident); ok && id.Name == "localOrigin" {
								allowed[id] = true
							}
						}
					}
				}
				return true
			})
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.Ident:
				if n.Name != "localOrigin" || declared[n] || allowed[n] {
					break
				}
				if name == "loopback.go" {
					report("%s: localOrigin is used other than as a direct argument of the handler's Inbound, Pull or QueryDirectory call", name)
				} else {
					report("%s: refers to localOrigin; only loopback.go may, as a direct argument of the handler's Inbound, Pull or QueryDirectory call", name)
				}
			case *ast.CompositeLit:
				if name == "origin.go" {
					break
				}
				if isOriginIdent(n.Type) && len(n.Elts) > 0 {
					report("%s: a composite literal of Origin with elements outside origin.go", name)
				}
				for _, e := range elidedElements(n) {
					if e.Type == nil && len(e.Elts) > 0 {
						report("%s: a composite literal of Origin, with its type left out, with elements outside origin.go", name)
					}
				}
			case *ast.CallExpr:
				if isOriginIdent(n.Fun) && name != "origin.go" {
					report("%s: converts a value to Origin outside origin.go", name)
				}
			case *ast.TypeSpec:
				if isOriginIdent(n.Type) && name != "origin.go" {
					report("%s: type %s is declared as another name for Origin", name, n.Name.Name)
				}
			case *ast.KeyValueExpr:
				if id, ok := n.Key.(*ast.Ident); ok && id.Name == "local" && name != "origin.go" {
					if v, ok := n.Value.(*ast.Ident); !ok || v.Name != "false" {
						report("%s: sets the local field outside origin.go", name)
					}
				}
			case *ast.AssignStmt:
				for _, l := range n.Lhs {
					if sel, ok := l.(*ast.SelectorExpr); ok && sel.Sel.Name == "local" {
						report("%s: assigns a local field", name)
					}
				}
			}
			return true
		})
	}
	return out
}

// surfaceViolations checks one declaration against the outside surface of the
// package: what a caller that imports it could build, hold or change.
func surfaceViolations(name string, d ast.Decl, report func(string, ...any)) {
	switch d := d.(type) {
	case *ast.FuncDecl:
		if !d.Name.IsExported() {
			return
		}
		if d.Recv != nil && len(d.Recv.List) == 1 {
			if star, ok := d.Recv.List[0].Type.(*ast.StarExpr); ok {
				if id, ok := star.X.(*ast.Ident); ok && id.Name == "Origin" {
					report("%s: %s is an exported method of *Origin that could change it", name, d.Name.Name)
				}
			}
		}
		if d.Type.Results == nil || (d.Name.Name == "RemoteOrigin" && d.Recv == nil) {
			return
		}
		for _, res := range d.Type.Results.List {
			if mentionsOrigin(res.Type) {
				report("%s: exported %s returns an Origin; only RemoteOrigin may", name, d.Name.Name)
			}
		}
	case *ast.GenDecl:
		for _, s := range d.Specs {
			switch s := s.(type) {
			case *ast.ValueSpec:
				for _, n := range s.Names {
					if n.IsExported() && (s.Type != nil && mentionsOrigin(s.Type) || anyMentions(s.Values, mentionsOrigin)) {
						report("%s: exported %s holds an Origin", name, n.Name)
					}
				}
			case *ast.TypeSpec:
				st, ok := s.Type.(*ast.StructType)
				if !ok || !s.Name.IsExported() {
					continue
				}
				for _, fld := range st.Fields.List {
					exported := len(fld.Names) == 0
					for _, n := range fld.Names {
						exported = exported || n.IsExported()
					}
					switch {
					case !exported:
					case s.Name.Name == "Origin":
						report("%s: Origin has an exported field", name)
					case mentionsOrigin(fld.Type):
						report("%s: exported type %s has an exported field of type Origin that a caller could set", name, s.Name.Name)
					}
				}
			}
		}
	}
}

// callsHandler reports whether c is a call of the Inbound, Pull or QueryDirectory of the
// loopback's handler field, l.h.
func callsHandler(c *ast.CallExpr) bool {
	sel, ok := c.Fun.(*ast.SelectorExpr)
	if !ok || (sel.Sel.Name != "Inbound" && sel.Sel.Name != "Pull" && sel.Sel.Name != "QueryDirectory") {
		return false
	}
	recv, ok := sel.X.(*ast.SelectorExpr)
	return ok && recv.Sel.Name == "h"
}

func isOriginIdent(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == "Origin"
}

// elidedElements returns the elements of a slice, array or map literal of
// Origin that are composite literals with their type left out, as in
// []Origin{{n, true}}.
func elidedElements(lit *ast.CompositeLit) []*ast.CompositeLit {
	var elem ast.Expr
	switch t := lit.Type.(type) {
	case *ast.ArrayType:
		elem = t.Elt
	case *ast.MapType:
		elem = t.Value
	default:
		return nil
	}
	if !mentionsOrigin(elem) {
		return nil
	}
	var out []*ast.CompositeLit
	for _, e := range lit.Elts {
		if kv, ok := e.(*ast.KeyValueExpr); ok {
			e = kv.Value
		}
		if c, ok := e.(*ast.CompositeLit); ok {
			out = append(out, c)
		}
	}
	return out
}

func mentionsOrigin(e ast.Expr) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && id.Name == "Origin" {
			found = true
		}
		return !found
	})
	return found
}

func anyMentions(exprs []ast.Expr, f func(ast.Expr) bool) bool {
	return slices.ContainsFunc(exprs, f)
}

// The sources the scan is tried on below: the shape of the real origin.go and
// loopback.go, reduced to what the rules look at.
const (
	legitOrigin = `package transport
type Origin struct {
	node  ident.NodeID
	local bool
}
func RemoteOrigin(node ident.NodeID) Origin { return Origin{node: node} }
func localOrigin(node ident.NodeID) Origin { return Origin{node: node, local: true} }
`
	legitLoopback = `package transport
func (l *Loopback) Send(ctx context.Context) {
	l.h.Inbound(ctx, localOrigin(l.self), s, d)
}
func (l *Loopback) Pull(ctx context.Context) {
	l.h.Pull(ctx, localOrigin(l.self), r)
}
func (l *Loopback) QueryDirectory(ctx context.Context) {
	l.h.QueryDirectory(ctx, localOrigin(l.self), s, r)
}
`
)

func parseSources(t *testing.T, sources map[string]string) map[string]*ast.File {
	t.Helper()
	fset := token.NewFileSet()
	files := map[string]*ast.File{}
	for name, src := range sources {
		f, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		files[name] = f
	}
	return files
}

// TestOriginScanCatchesWhatItNames runs the scan over synthetic sources, one
// for each way found to get past an earlier, weaker scan or that the rules
// name, and expects each to be reported. It also expects the sources of the
// legitimate shape to pass, so that a scan that reports everything would fail
// here and not only the real one.
func TestOriginScanCatchesWhatItNames(t *testing.T) {
	legit := map[string]string{"origin.go": legitOrigin, "loopback.go": legitLoopback}
	with := func(name, src string) map[string]string {
		m := map[string]string{}
		for k, v := range legit {
			m[k] = v
		}
		m[name] = src
		return m
	}
	cases := []struct {
		rule    string
		sources map[string]string
		want    string // a part of the report that says why
	}{
		{
			rule:    "a function of origin.go used as a value in another file",
			sources: with("extra.go", "package transport\nvar MakeLocal = localOrigin\n"),
			want:    "refers to localOrigin",
		},
		{
			rule:    "a call outside loopback.go",
			sources: with("extra.go", "package transport\nfunc f(n ident.NodeID) Origin { return localOrigin(n) }\n"),
			want:    "refers to localOrigin",
		},
		{
			rule:    "an unkeyed literal outside origin.go",
			sources: with("extra.go", "package transport\nfunc f(n ident.NodeID) Origin { return Origin{n, true} }\n"),
			want:    "composite literal of Origin",
		},
		{
			rule:    "a keyed literal that sets local outside origin.go",
			sources: with("extra.go", "package transport\nfunc f(n ident.NodeID) Origin { return Origin{node: n, local: true} }\n"),
			want:    "composite literal of Origin",
		},
		{
			rule:    "an unkeyed literal of Origin with its type left out, in a slice",
			sources: with("extra.go", "package transport\nvar xs = []Origin{{n, true}}\n"),
			want:    "type left out",
		},
		{
			rule:    "an unkeyed literal of Origin with its type left out, in a map",
			sources: with("extra.go", "package transport\nvar xs = map[string]Origin{\"a\": {n, true}}\n"),
			want:    "type left out",
		},
		{
			rule:    "a conversion to Origin from a struct of the same shape",
			sources: with("extra.go", "package transport\ntype raw struct{ node ident.NodeID; local bool }\nfunc f(r raw) Origin { return Origin(r) }\n"),
			want:    "converts a value to Origin",
		},
		{
			rule:    "another name for Origin",
			sources: with("extra.go", "package transport\ntype O = Origin\n"),
			want:    "another name for Origin",
		},
		{
			rule:    "an assignment to the local field",
			sources: with("extra.go", "package transport\nfunc f(o *Origin) { o.local = true }\n"),
			want:    "assigns a local field",
		},
		{
			rule: "a new exported method of the loopback that returns the local origin as any",
			sources: with("loopback.go", legitLoopback+
				"func (l *Loopback) Spy() any { return localOrigin(l.self) }\n"),
			want: "other than as a direct argument",
		},
		{
			rule: "the local origin kept in a variable of the loopback before it is passed",
			sources: with("loopback.go", legitLoopback+
				"func (l *Loopback) Spy(ctx context.Context) { o := localOrigin(l.self); l.h.Inbound(ctx, o, s, d) }\n"),
			want: "other than as a direct argument",
		},
		{
			rule: "the local origin handed to a handler that is not the loopback's own",
			sources: with("loopback.go", legitLoopback+
				"func (l *Loopback) Spy(ctx context.Context, h Handler) { h.Inbound(ctx, localOrigin(l.self), s, d) }\n"),
			want: "other than as a direct argument",
		},
		{
			rule: "the local origin handed to a call that is not the handler's",
			sources: with("loopback.go", legitLoopback+
				"func (l *Loopback) Spy() { leak(localOrigin(l.self)) }\n"),
			want: "other than as a direct argument",
		},
		{
			rule:    "an exported function that returns an Origin",
			sources: with("extra.go", "package transport\nfunc Make(n ident.NodeID) Origin { return RemoteOrigin(n) }\n"),
			want:    "only RemoteOrigin may",
		},
		{
			rule:    "an exported variable that holds an Origin",
			sources: with("extra.go", "package transport\nvar Zero Origin\n"),
			want:    "holds an Origin",
		},
		{
			rule:    "an exported field of Origin that a caller could set",
			sources: with("extra.go", "package transport\ntype Request struct{ From Origin }\n"),
			want:    "a caller could set",
		},
		{
			rule:    "an exported method of *Origin",
			sources: with("extra.go", "package transport\nfunc (o *Origin) Reset() {}\n"),
			want:    "could change it",
		},
	}
	if got := originViolations(parseSources(t, legit)); len(got) != 0 {
		t.Fatalf("the legitimate shape is reported: %v", got)
	}
	for _, c := range cases {
		t.Run(c.rule, func(t *testing.T) {
			got := originViolations(parseSources(t, c.sources))
			if len(got) == 0 {
				t.Fatal("the scan reports nothing")
			}
			if !strings.Contains(strings.Join(got, "\n"), c.want) {
				t.Errorf("the scan reports %q, want a report that says %q", got, c.want)
			}
		})
	}
}
