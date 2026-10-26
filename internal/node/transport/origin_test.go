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
	if _, err := l.Pull(context.Background(), r.self, transport.PullRequest{Message: "m1", Recipients: pulled}); err != nil {
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
// that nothing outside the loopback makes a local origin. The rules are listed
// at originViolations; TestOriginScanCatchesWhatItNames shows that each of them
// catches what it names. The scan reads source text: it does not police unsafe
// or reflection, and a reviewer of the package's local interface has to keep
// that in mind.
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
	for _, v := range originViolations(files) {
		t.Error(v)
	}
	// The scan must have looked at something: the constructor and its calls exist.
	if !declares(files["origin.go"], "localOrigin") {
		t.Error("origin.go does not declare localOrigin: the scan above no longer checks the rule it names")
	}
	if !callsIn(files["loopback.go"], "localOrigin") {
		t.Error("loopback.go does not call localOrigin: the loopback no longer makes the local origin")
	}
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
