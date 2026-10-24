package keys_test

import (
	"bytes"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/node/keys"
)

// Key generation takes its bytes from the injected reader and from nothing
// else: the same reader gives the same keys, byte for byte, and everything
// derived from them, so a test can fix a node's identity.
func TestKeyGenerationWithAnInjectedReaderIsReproducible(t *testing.T) {
	a, b, c := newRig(t), newRig(t), newRig(t)
	a.mustInit(1, t0)
	b.mustInit(1, t0)
	c.mustInit(2, t0)
	for _, p := range []string{keys.IdentityKeyPath, keys.NetworkKeyPath, keys.CertificatePath, keys.SelfRevocationPath, keys.SerialPath, keys.ClockPath, keys.InitMarkerPath} {
		if !bytes.Equal(a.read(p), b.read(p)) {
			t.Errorf("%s differs between two inits with the same reader and time", p)
		}
	}
	for _, p := range []string{keys.IdentityKeyPath, keys.NetworkKeyPath, keys.CertificatePath, keys.SelfRevocationPath} {
		if bytes.Equal(a.read(p), c.read(p)) {
			t.Errorf("%s is the same for different readers", p)
		}
	}
}

// A reader that fails or runs dry fails the init: there is no fallback to
// another source of randomness, which would have made the key unpredictable to
// the test and silently untestable.
func TestKeyGenerationFailsWithTheReaderAndNeverFallsBack(t *testing.T) {
	for name, rd := range map[string]interface {
		Read([]byte) (int, error)
	}{
		"failing": &failingReader{},
		"dry":     &shortReader{left: 40},
		"empty":   &shortReader{},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			before := snapshot(t, r.mem)
			_, err := keys.PlanInit(r.w.Reader(), rd, t0)
			if !errors.Is(err, keys.ErrRandom) {
				t.Fatalf("PlanInit = %v, want ErrRandom", err)
			}
			if r.obs.count() != 0 || !reflect.DeepEqual(snapshot(t, r.mem), before) {
				t.Error("a failed generation wrote")
			}
		})
	}
	var fr failingReader
	r := newRig(t)
	if _, err := keys.PlanInit(r.w.Reader(), &fr, t0); err == nil || fr.n != 1 {
		t.Errorf("the failing reader was asked %d times (want 1: the first key fails the plan): %v", fr.n, err)
	}
}

// By construction: nothing in this package can reach a random source of its own.
// The scan of the module forbids the imports of crypto/rand and math/rand; it
// cannot see ed25519.GenerateKey(nil), which reads the system source without
// importing anything, nor the key generators that ignore the reader they are
// given. Those are forbidden here by name, in every non-test file of the package.
func TestKeysSourceNeverReachesAGlobalRandomSource(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	forbiddenImports := map[string]bool{"crypto/rand": true, "math/rand": true, "math/rand/v2": true, "crypto/ecdh": true, "crypto/ecdsa": true, "crypto/rsa": true}
	scanned := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		scanned++
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(token.NewFileSet(), f, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range file.Imports {
			if path, _ := strconv.Unquote(imp.Path.Value); forbiddenImports[path] {
				t.Errorf("%s imports %s", f, path)
			}
		}
		ast.Inspect(file, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok && (sel.Sel.Name == "GenerateKey" || sel.Sel.Name == "GenerateKeyPair") {
				t.Errorf("%s uses %s: key generation reads the injected reader itself and builds the key with NewKeyFromSeed", f, sel.Sel.Name)
			}
			return true
		})
	}
	if scanned < 5 {
		t.Errorf("scanned %d source files, the scan did not find the package", scanned)
	}
}

// A key never prints itself, nor does anything that holds one: no verb of fmt
// shows a seed, in hex, decimal or text, whether the holder is printed by
// pointer or by value.
func TestPrivateKeysNeverPrintThemselves(t *testing.T) {
	r := newRig(t)
	plan := r.mustInit(1, t0)
	k := r.mustLoad()
	seed := stream(1, 64)
	for _, key := range []any{k, *k, plan, *plan, k.NetworkSigner(), k.Certificate()} {
		for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%x", "%X", "%d", "%q"} {
			out := sprintf(verb, key)
			for _, s := range [][]byte{seed[:32], seed[32:]} {
				if strings.Contains(out, hexString(s)) || strings.Contains(out, decimalString(s)) || strings.Contains(out, string(s)) {
					t.Errorf("%s of the keys shows key material", verb)
				}
			}
		}
	}
}
