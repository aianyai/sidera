package keys_test

import (
	"bytes"
	"crypto/ed25519"
	"encoding/pem"
	"errors"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/node/keys"
	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/internal/revocation"
)

func TestParseRevocationExportReturnsTheVerifiedStatement(t *testing.T) {
	for name, text := range map[string]string{
		"canonical":             goldenExport,
		"without final newline": strings.TrimSuffix(goldenExport, "\n"),
	} {
		t.Run(name, func(t *testing.T) {
			sr, err := keys.ParseRevocationExport([]byte(text))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(sr.Wire(), mustHex(goldenSelfRevocationHex)) ||
				sr.Digest().String() != goldenSelfRevocationDigest || sr.Node().String() != goldenNode {
				t.Fatal("the parsed statement does not match the independent wire, digest and node vector")
			}
			formatted, digest := keys.FormatRevocationExport(sr)
			if string(formatted) != goldenExport || digest != sr.Digest() {
				t.Fatal("the parsed statement does not format as the canonical export")
			}
		})
	}
}

func malformedRevocationExports() map[string]string {
	lines := strings.Split(goldenExport, "\n")
	body := strings.Join(lines[1:len(lines)-2], "")
	return map[string]string{
		"empty":                "",
		"raw record":           string(mustHex(goldenSelfRevocationHex)),
		"prefix":               "backup\n" + goldenExport,
		"suffix":               goldenExport + "backup\n",
		"multiple blocks":      goldenExport + goldenExport,
		"leading newline":      "\n" + goldenExport,
		"extra final newline":  goldenExport + "\n",
		"headers":              strings.Replace(goldenExport, "\n", "\nComment: backup\n\n", 1),
		"wrong block type":     strings.ReplaceAll(goldenExport, "SELF-REVOCATION", "CERTIFICATE"),
		"CRLF":                 strings.ReplaceAll(goldenExport, "\n", "\r\n"),
		"CR":                   strings.ReplaceAll(goldenExport, "\n", "\r"),
		"space in base64":      strings.Replace(goldenExport, "owF4", "ow F4", 1),
		"tab in base64":        strings.Replace(goldenExport, "owF4", "ow\tF4", 1),
		"unwrapped base64":     lines[0] + "\n" + body + "\n" + lines[len(lines)-2] + "\n",
		"base64url":            strings.NewReplacer("/", "_", "+", "-").Replace(goldenExport),
		"missing padding":      strings.Replace(goldenExport, "==", "", 1),
		"nonzero padding bits": strings.Replace(goldenExport, "BA==", "BB==", 1),
		"invalid base64":       strings.Replace(goldenExport, "owF4", "!wF4", 1),
		"missing end marker":   strings.Join(lines[:len(lines)-2], "\n") + "\n",
		"truncated marker":     strings.TrimSuffix(goldenExport, "-----\n"),
	}
}

func TestParseRevocationExportRefusesMalformedBackups(t *testing.T) {
	rows := malformedRevocationExports()
	good := mustHex(goldenSelfRevocationHex)
	damaged := bytes.Clone(good)
	damaged[len(damaged)-3] ^= 1
	rec, err := record.Decode(good, record.DefaultMaxInput)
	if err != nil {
		t.Fatal(err)
	}
	otherSigner, err := record.NewEd25519Signer(ed25519.NewKeyFromSeed(mustHex(goldenNetworkSeedHex)))
	if err != nil {
		t.Fatal(err)
	}
	wrongIdentity, err := record.Sign(rec.Type, rec.Payload, otherSigner)
	if err != nil {
		t.Fatal(err)
	}
	wrongIdentityWire, err := wrongIdentity.Encode()
	if err != nil {
		t.Fatal(err)
	}
	r := newRig(t)
	r.mustInit(1, t0)
	for name, wire := range map[string][]byte{
		"empty record":                        nil,
		"damaged signature":                   damaged,
		"truncated record":                    good[:len(good)-1],
		"valid signature of another identity": wrongIdentityWire,
		"a certificate":                       goldenCertificate,
		"a certificate revocation":            certRevocationWire(t, r),
	} {
		rows[name] = string(pem.EncodeToMemory(&pem.Block{Type: "SIDERA SELF-REVOCATION", Bytes: wire}))
	}
	for name, text := range rows {
		t.Run(name, func(t *testing.T) {
			sr, err := keys.ParseRevocationExport([]byte(text))
			if !errors.Is(err, keys.ErrCorrupt) {
				t.Fatalf("ParseRevocationExport error = %v, want ErrCorrupt", err)
			}
			if len(sr.Wire()) != 0 || !sr.Digest().IsZero() || !sr.Node().IsZero() {
				t.Fatal("a refused backup returned a usable statement")
			}
		})
	}
}

func FuzzParseRevocationExport(f *testing.F) {
	f.Add([]byte(goldenExport))
	f.Add([]byte(strings.TrimSuffix(goldenExport, "\n")))
	for _, text := range malformedRevocationExports() {
		f.Add([]byte(text))
	}
	wire := mustHex(goldenSelfRevocationHex)
	damaged := bytes.Clone(wire)
	damaged[len(damaged)-3] ^= 1
	for _, invalid := range [][]byte{nil, damaged, wire[:len(wire)-1], goldenCertificate} {
		f.Add(pem.EncodeToMemory(&pem.Block{Type: "SIDERA SELF-REVOCATION", Bytes: invalid}))
	}
	f.Fuzz(func(t *testing.T, text []byte) {
		sr, err := keys.ParseRevocationExport(text)
		if err != nil {
			if !errors.Is(err, keys.ErrCorrupt) || len(sr.Wire()) != 0 || !sr.Digest().IsZero() || !sr.Node().IsZero() {
				t.Fatal("a refused backup returned a statement or an unexpected error kind")
			}
			return
		}
		rec, err := record.Decode(sr.Wire(), record.DefaultMaxInput)
		if err != nil {
			t.Fatal(err)
		}
		rv, err := revocation.Verify(rec)
		if err != nil || rv.Kind() != revocation.KindNodeSelf || rv.Digest() != sr.Digest() || rv.Node() != sr.Node() {
			t.Fatal("accepted backup is not the verified statement returned")
		}
		canonical, digest := keys.FormatRevocationExport(sr)
		if !bytes.Equal(bytes.TrimSuffix(text, []byte{'\n'}), bytes.TrimSuffix(canonical, []byte{'\n'})) || digest != sr.Digest() {
			t.Fatal("accepted backup is not canonical apart from its optional final newline")
		}
		again, err := keys.ParseRevocationExport(canonical)
		if err != nil || !bytes.Equal(again.Wire(), sr.Wire()) || again.Digest() != sr.Digest() || again.Node() != sr.Node() {
			t.Fatal("accepted backup does not round trip")
		}
	})
}
