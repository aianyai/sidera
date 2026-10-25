package transport_test

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/transport"
)

// TestLoopbackMakesTheLocalOrigin is the positive half of the local-origin rule:
// every request the loopback hands to the engine's side, an envelope or a pull,
// is from a local origin of this node.
func TestLoopbackMakesTheLocalOrigin(t *testing.T) {
	r := newRig(t)
	var seen []transport.Origin
	h := &originRecorder{target: r.target, seen: &seen}
	l := transport.NewLoopback(r.self, h, nil)
	if _, err := l.Send(context.Background(), r.self, ep(9), r.message(t, "m1", "hello")); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Pull(context.Background(), r.self, transport.PullRequest{Message: "m1"}); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 {
		t.Fatalf("handler saw %d requests, want 2", len(seen))
	}
	for i, o := range seen {
		if !o.Local() || o.Node() != r.self {
			t.Errorf("request %d: local=%v node=%v, want a local origin of this node", i, o.Local(), o.Node())
		}
	}
}

type originRecorder struct {
	target *target
	seen   *[]transport.Origin
}

func (o *originRecorder) Inbound(ctx context.Context, from transport.Origin, sender ident.EndpointID, data []byte) (transport.Receipt, error) {
	*o.seen = append(*o.seen, from)
	return o.target.Inbound(ctx, from, sender, data)
}

func (o *originRecorder) Pull(ctx context.Context, from transport.Origin, req transport.PullRequest) (transport.PullAnswer, error) {
	*o.seen = append(*o.seen, from)
	return o.target.Pull(ctx, from, req)
}

// TestRemoteOriginIsNeverLocal is the negative half for every other transport:
// the one constructor outside the package makes a remote origin, whatever node
// it names, this node included. A connection that shows this node's own id (a
// second instance or a stolen key) is therefore not a local source. The zero
// Origin is not local either.
func TestRemoteOriginIsNeverLocal(t *testing.T) {
	self, other := nodeOf(t, 1), nodeOf(t, 2)
	for name, o := range map[string]transport.Origin{
		"a remote origin that names another node": transport.RemoteOrigin(other),
		"a remote origin that names this node":    transport.RemoteOrigin(self),
		"the zero origin":                         {},
		"a remote origin that names no node":      transport.RemoteOrigin(ident.NodeID{}),
	} {
		t.Run(name, func(t *testing.T) {
			if o.Local() {
				t.Fatal("origin is local")
			}
		})
	}
	if got := transport.RemoteOrigin(other).Node(); got != other {
		t.Errorf("RemoteOrigin(other).Node() = %v, want %v", got, other)
	}
}

// TestNoCallerCanClaimTheLocalOrigin reads the package's own source to check
// that nothing outside the loopback makes a local origin:
//
//   - Origin has no exported field and no exported method with a pointer
//     receiver, and the package exports no function, method or variable that
//     returns or holds an Origin other than RemoteOrigin, so a caller of the
//     package cannot build or change one;
//   - the unexported constructor is called from loopback.go only, and the field
//     that says "local" is set to anything but false in origin.go only.
//
// Importing code cannot reach unexported fields, so the first list is the
// whole outside surface; the second keeps the inside to the one place the plan
// names.
func TestNoCallerCanClaimTheLocalOrigin(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]*ast.File{}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		files[name] = f
	}
	if len(files) == 0 {
		t.Fatal("no source files found")
	}
	mentionsOrigin := func(e ast.Expr) bool {
		found := false
		ast.Inspect(e, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && id.Name == "Origin" {
				found = true
			}
			return !found
		})
		return found
	}
	for name, f := range files {
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				if !d.Name.IsExported() {
					continue
				}
				if d.Recv != nil && len(d.Recv.List) == 1 {
					if star, ok := d.Recv.List[0].Type.(*ast.StarExpr); ok {
						if id, ok := star.X.(*ast.Ident); ok && id.Name == "Origin" {
							t.Errorf("%s: %s is an exported method of *Origin that could change it", name, d.Name.Name)
						}
					}
				}
				if d.Type.Results == nil || (d.Name.Name == "RemoteOrigin" && d.Recv == nil) {
					continue
				}
				for _, res := range d.Type.Results.List {
					if mentionsOrigin(res.Type) {
						t.Errorf("%s: exported %s returns an Origin; only RemoteOrigin may", name, d.Name.Name)
					}
				}
			case *ast.GenDecl:
				for _, s := range d.Specs {
					switch s := s.(type) {
					case *ast.ValueSpec:
						for _, n := range s.Names {
							if n.IsExported() && (s.Type != nil && mentionsOrigin(s.Type) || anyMentions(s.Values, mentionsOrigin)) {
								t.Errorf("%s: exported %s holds an Origin", name, n.Name)
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
								t.Errorf("%s: Origin has an exported field", name)
							case mentionsOrigin(fld.Type):
								t.Errorf("%s: exported type %s has an exported field of type Origin that a caller could set", name, s.Name.Name)
							}
						}
					}
				}
			}
		}
	}
	for name, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.CallExpr:
				if id, ok := n.Fun.(*ast.Ident); ok && id.Name == "localOrigin" && name != "loopback.go" {
					t.Errorf("%s: calls localOrigin; only loopback.go may make a local origin", name)
				}
			case *ast.KeyValueExpr:
				if id, ok := n.Key.(*ast.Ident); ok && id.Name == "local" && name != "origin.go" {
					if v, ok := n.Value.(*ast.Ident); !ok || v.Name != "false" {
						t.Errorf("%s: sets the local field outside origin.go", name)
					}
				}
			case *ast.AssignStmt:
				for _, l := range n.Lhs {
					if sel, ok := l.(*ast.SelectorExpr); ok && sel.Sel.Name == "local" {
						t.Errorf("%s: assigns a local field", name)
					}
				}
			}
			return true
		})
	}
	// The scan must have looked at something: the constructor and its single call exist.
	if !declares(files["origin.go"], "localOrigin") {
		t.Error("origin.go does not declare localOrigin: the scan above no longer checks the rule it names")
	}
	if !callsIn(files["loopback.go"], "localOrigin") {
		t.Error("loopback.go does not call localOrigin: the loopback no longer makes the local origin")
	}
}

func anyMentions(exprs []ast.Expr, f func(ast.Expr) bool) bool {
	for _, e := range exprs {
		if f(e) {
			return true
		}
	}
	return false
}

func declares(f *ast.File, name string) bool {
	if f == nil {
		return false
	}
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil && fd.Name.Name == name {
			return true
		}
	}
	return false
}

func callsIn(f *ast.File, name string) bool {
	if f == nil {
		return false
	}
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok {
			if id, ok := c.Fun.(*ast.Ident); ok && id.Name == name {
				found = true
			}
		}
		return !found
	})
	return found
}
