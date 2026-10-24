package keys_test

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/node/keys"
)

// The self-revocation is a declaration that anyone holding it can use to retire
// the identity irreversibly, so it is as secret as a key: it prints as its node
// and its digest and nothing else, under every verb and wherever it is held.
func TestSelfRevocationPrintsOnlyItsNodeAndDigest(t *testing.T) {
	r := newRig(t)
	r.mustInit(1, t0)
	k := r.mustLoad()
	sr := k.SelfRevocation()
	wire := sr.Wire()

	// every spelling a printer might give the bytes, whole or in part
	spellings := map[string]string{
		"hex":         hex.EncodeToString(wire),
		"hex prefix":  hex.EncodeToString(wire[:16]),
		"HEX prefix":  strings.ToUpper(hex.EncodeToString(wire[:16])),
		"decimal":     fmt.Sprint(wire),
		"base64":      base64.StdEncoding.EncodeToString(wire),
		"base64 url":  base64.RawURLEncoding.EncodeToString(wire),
		"go syntax":   fmt.Sprintf("%#v", wire),
		"raw text":    string(wire),
		"decimal run": fmt.Sprint(wire[:16]),
	}
	holder := struct{ Statement keys.SelfRevocation }{sr}

	renderings := map[string]func(verb string) string{
		"value":         func(verb string) string { return fmt.Sprintf(verb, sr) },
		"pointer":       func(verb string) string { return fmt.Sprintf(verb, &sr) },
		"struct field":  func(verb string) string { return fmt.Sprintf(verb, holder) },
		"field pointer": func(verb string) string { return fmt.Sprintf(verb, &holder) },
	}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%x", "%X", "%d", "%q"} {
		for how, render := range renderings {
			out := render(verb)
			for name, spelling := range spellings {
				if strings.Contains(out, spelling) {
					t.Errorf("%s of the statement (%s) shows its bytes as %s: %.200s", verb, how, name, out)
				}
			}
		}
	}

	outs := map[string]string{
		"fmt.Sprint":   fmt.Sprint(sr),
		"fmt.Sprintln": fmt.Sprintln(sr),
		"String":       methodOutput(t, sr, func(s fmt.Stringer) string { return s.String() }),
		"GoString":     methodOutput(t, sr, func(s fmt.GoStringer) string { return s.GoString() }),
		"slog text":    slogText(sr),
		"slog struct":  slogText(holder),
	}
	for how, out := range outs {
		for name, spelling := range spellings {
			if strings.Contains(out, spelling) {
				t.Errorf("%s of the statement shows its bytes as %s: %.200s", how, name, out)
			}
		}
	}

	// what it does print names the statement
	for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
		out := fmt.Sprintf(verb, sr)
		if !strings.Contains(out, sr.Node().String()) || !strings.Contains(out, sr.Digest().String()) {
			t.Errorf("%s prints %q, want it to name the node %s and the digest %s", verb, out, sr.Node(), sr.Digest())
		}
	}
}

func slogText(v any) string {
	var buf bytes.Buffer
	slog.New(slog.NewTextHandler(&buf, nil)).Info("loaded", "statement", v)
	return buf.String()
}

// methodOutput calls a printing method through its interface, failing the test
// when the statement does not implement it.
func methodOutput[I any](t *testing.T, v any, call func(I) string) string {
	t.Helper()
	i, ok := v.(I)
	if !ok {
		t.Errorf("%T does not implement %T", v, *new(I))
		return ""
	}
	return call(i)
}
