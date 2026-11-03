package minisign_test

import (
	"bytes"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/minisign"
)

// The files in testdata are made by the real minisign tool; testdata/README.md
// says how.

const (
	testKeyID  = "57478AD34CBC6FF4"
	otherKeyID = "BAC0B87022F9F43C"
	comment    = "sidera 0.12.0 update-from 0.8.0"
)

func read(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func parseKey(t *testing.T, name string) minisign.PublicKey {
	t.Helper()
	key, err := minisign.ParsePublicKey(read(t, name))
	if err != nil {
		t.Fatalf("ParsePublicKey(%s): %v", name, err)
	}
	return key
}

// editLine returns file with the n-th line (0-based) replaced by f(line).
func editLine(file []byte, n int, f func(string) string) []byte {
	lines := strings.Split(string(file), "\n")
	lines[n] = f(lines[n])
	return []byte(strings.Join(lines, "\n"))
}

// editDecoded returns file with the base64 payload on line n decoded, changed
// by f and encoded again.
func editDecoded(t *testing.T, file []byte, n int, f func([]byte) []byte) []byte {
	t.Helper()
	return editLine(file, n, func(line string) string {
		raw, err := base64.StdEncoding.DecodeString(line)
		if err != nil {
			t.Fatal(err)
		}
		return base64.StdEncoding.EncodeToString(f(raw))
	})
}

func flip(at int) func([]byte) []byte {
	return func(b []byte) []byte {
		b = bytes.Clone(b)
		b[at] ^= 1
		return b
	}
}

func replaceString(from, to string) func(string) string {
	return func(s string) string { return strings.Replace(s, from, to, 1) }
}

func TestFormatKeyID(t *testing.T) {
	// The display form is the id bytes in reverse order, as the tool prints it.
	if got := minisign.FormatKeyID(parseKey(t, "test.pub").ID); got != testKeyID {
		t.Errorf("FormatKeyID = %s, want %s", got, testKeyID)
	}
	if got := minisign.FormatKeyID(parseKey(t, "other.pub").ID); got != otherKeyID {
		t.Errorf("FormatKeyID = %s, want %s", got, otherKeyID)
	}
}

func TestParsePublicKey(t *testing.T) {
	pub := read(t, "test.pub")
	keyLine := strings.Split(string(pub), "\n")[1]

	valid := []struct {
		name string
		file []byte
	}{
		{"pub file", pub},
		{"pub file without final newline", bytes.TrimSuffix(pub, []byte("\n"))},
		{"bare key line", []byte(keyLine)},
		{"bare key line with newline", []byte(keyLine + "\n")},
	}
	for _, c := range valid {
		t.Run("valid/"+c.name, func(t *testing.T) {
			key, err := minisign.ParsePublicKey(c.file)
			if err != nil {
				t.Fatal(err)
			}
			if got := minisign.FormatKeyID(key.ID); got != testKeyID {
				t.Errorf("key id = %s, want %s", got, testKeyID)
			}
			if len(key.Key) != 32 {
				t.Errorf("key length = %d, want 32", len(key.Key))
			}
		})
	}

	invalid := map[string][]byte{
		"empty file":                    nil,
		"only a newline":                []byte("\n"),
		"algorithm is the prehashed ED": editDecoded(t, pub, 1, func(b []byte) []byte { return append([]byte("ED"), b[2:]...) }),
		"algorithm unknown":             editDecoded(t, pub, 1, func(b []byte) []byte { return append([]byte("xx"), b[2:]...) }),
		"one byte short":                editDecoded(t, pub, 1, func(b []byte) []byte { return b[:len(b)-1] }),
		"one byte long":                 editDecoded(t, pub, 1, func(b []byte) []byte { return append(bytes.Clone(b), 0) }),
		"base64 corrupt":                editLine(pub, 1, replaceString("RW", "R!")),
		"base64 padding removed":        editLine(pub, 1, func(s string) string { return strings.TrimRight(s, "=") + "A" }),
		"three lines":                   append(bytes.Clone(pub), []byte("extra\n")...),
		"blank line after the key":      append(bytes.Clone(pub), '\n'),
		"comment prefix wrong":          editLine(pub, 0, replaceString("untrusted comment:", "untrusted_comment:")),
		"comment without space":         editLine(pub, 0, replaceString("untrusted comment: ", "untrusted comment:")),
		"CRLF":                          bytes.ReplaceAll(pub, []byte("\n"), []byte("\r\n")),
		"CR inside the key line":        editLine(pub, 1, func(s string) string { return s + "\r" }),
	}
	for name, file := range invalid {
		t.Run("invalid/"+name, func(t *testing.T) {
			if _, err := minisign.ParsePublicKey(file); !errors.Is(err, minisign.ErrMalformed) {
				t.Errorf("err = %v, want ErrMalformed", err)
			}
		})
	}
}

func TestParseSignature(t *testing.T) {
	sig := read(t, "message.txt.minisig")

	t.Run("valid", func(t *testing.T) {
		for name, file := range map[string][]byte{
			"with final newline":    sig,
			"without final newline": bytes.TrimSuffix(sig, []byte("\n")),
		} {
			got, err := minisign.ParseSignature(file)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if got.TrustedComment != comment {
				t.Errorf("%s: TrustedComment = %q, want %q", name, got.TrustedComment, comment)
			}
			if id := minisign.FormatKeyID(got.KeyID); id != testKeyID {
				t.Errorf("%s: KeyID = %s, want %s", name, id, testKeyID)
			}
		}
	})

	t.Run("legacy format", func(t *testing.T) {
		_, err := minisign.ParseSignature(read(t, "legacy.minisig"))
		if !errors.Is(err, minisign.ErrLegacyFormat) {
			t.Errorf("err = %v, want ErrLegacyFormat", err)
		}
	})

	lines := strings.Split(strings.TrimSuffix(string(sig), "\n"), "\n")
	invalid := map[string][]byte{
		"empty file":                   nil,
		"three lines":                  []byte(strings.Join(lines[:3], "\n") + "\n"),
		"five lines":                   append(bytes.Clone(sig), []byte("extra\n")...),
		"blank fifth line":             append(bytes.Clone(sig), '\n'),
		"signature line missing":       []byte(strings.Join([]string{lines[0], lines[2], lines[3]}, "\n") + "\n"),
		"untrusted prefix wrong":       editLine(sig, 0, replaceString("untrusted comment:", "comment:")),
		"trusted prefix of the spec":   editLine(sig, 2, replaceString("trusted comment:", "trusted_comment:")),
		"trusted prefix without space": editLine(sig, 2, replaceString("trusted comment: ", "trusted comment:")),
		"trusted prefix capitalised":   editLine(sig, 2, replaceString("trusted", "Trusted")),
		"algorithm unknown":            editDecoded(t, sig, 1, func(b []byte) []byte { return append([]byte("XX"), b[2:]...) }),
		"signature one byte short":     editDecoded(t, sig, 1, func(b []byte) []byte { return b[:len(b)-1] }),
		"signature one byte long":      editDecoded(t, sig, 1, func(b []byte) []byte { return append(bytes.Clone(b), 0) }),
		"signature base64 corrupt":     editLine(sig, 1, replaceString("RU", "R!")),
		"global one byte short":        editDecoded(t, sig, 3, func(b []byte) []byte { return b[:len(b)-1] }),
		"global one byte long":         editDecoded(t, sig, 3, func(b []byte) []byte { return append(bytes.Clone(b), 0) }),
		"global base64 corrupt":        editLine(sig, 3, func(s string) string { return "!" + s[1:] }),
		"global line empty":            editLine(sig, 3, func(string) string { return "" }),
		"CRLF":                         bytes.ReplaceAll(sig, []byte("\n"), []byte("\r\n")),
		"CR in the trusted comment":    editLine(sig, 2, func(s string) string { return s + "\r" }),
	}
	for name, file := range invalid {
		t.Run("invalid/"+name, func(t *testing.T) {
			if _, err := minisign.ParseSignature(file); !errors.Is(err, minisign.ErrMalformed) {
				t.Errorf("err = %v, want ErrMalformed", err)
			}
		})
	}
}

// verify parses the signature file and checks it, the way a caller does.
func verify(keys []minisign.PublicKey, message, file []byte) (string, error) {
	sig, err := minisign.ParseSignature(file)
	if err != nil {
		return "", err
	}
	return minisign.Verify(keys, message, sig)
}

func TestVerify(t *testing.T) {
	test, other := parseKey(t, "test.pub"), parseKey(t, "other.pub")
	message := read(t, "message.txt")
	sig := read(t, "message.txt.minisig")

	t.Run("valid returns the trusted comment", func(t *testing.T) {
		for name, keys := range map[string][]minisign.PublicKey{
			"one key":                 {test},
			"the key among others":    {other, test},
			"the signing key is last": {other, other, test},
		} {
			got, err := verify(keys, message, sig)
			if err != nil || got != comment {
				t.Errorf("%s: got (%q, %v), want (%q, nil)", name, got, err, comment)
			}
		}
	})

	t.Run("the key the id names is the one used", func(t *testing.T) {
		got, err := verify([]minisign.PublicKey{test, other}, message, read(t, "otherkey.minisig"))
		if err != nil || got != comment {
			t.Errorf("got (%q, %v), want (%q, nil)", got, err, comment)
		}
	})

	tampered := []struct {
		name    string
		keys    []minisign.PublicKey
		message []byte
		sig     []byte
		want    error
	}{
		{"message: one byte changed", []minisign.PublicKey{test}, flip(0)(message), sig, minisign.ErrBadSignature},
		{"message: last byte changed", []minisign.PublicKey{test}, flip(len(message) - 1)(message), sig, minisign.ErrBadSignature},
		{"message: truncated", []minisign.PublicKey{test}, message[:len(message)-1], sig, minisign.ErrBadSignature},
		{"message: extended", []minisign.PublicKey{test}, append(bytes.Clone(message), '\n'), sig, minisign.ErrBadSignature},
		{"message: empty", []minisign.PublicKey{test}, nil, sig, minisign.ErrBadSignature},
		{"trusted comment: one byte changed", []minisign.PublicKey{test}, message, editLine(sig, 2, replaceString("0.12.0", "0.12.1")), minisign.ErrBadSignature},
		{"trusted comment: appended", []minisign.PublicKey{test}, message, editLine(sig, 2, func(s string) string { return s + " " }), minisign.ErrBadSignature},
		{"trusted comment: emptied", []minisign.PublicKey{test}, message, editLine(sig, 2, func(string) string { return "trusted comment: " }), minisign.ErrBadSignature},
		{"signature: first byte changed", []minisign.PublicKey{test}, message, editDecoded(t, sig, 1, flip(10)), minisign.ErrBadSignature},
		{"signature: last byte changed", []minisign.PublicKey{test}, message, editDecoded(t, sig, 1, flip(73)), minisign.ErrBadSignature},
		{"global signature: first byte changed", []minisign.PublicKey{test}, message, editDecoded(t, sig, 3, flip(0)), minisign.ErrBadSignature},
		{"global signature: last byte changed", []minisign.PublicKey{test}, message, editDecoded(t, sig, 3, flip(63)), minisign.ErrBadSignature},
		{"key id: one byte changed", []minisign.PublicKey{test}, message, editDecoded(t, sig, 1, flip(2)), minisign.ErrUnknownKey},
		{"key id: signed by another key", []minisign.PublicKey{test}, message, read(t, "otherkey.minisig"), minisign.ErrUnknownKey},
		{"key id: no key given", nil, message, sig, minisign.ErrUnknownKey},
		{"key id: id of the key changed", []minisign.PublicKey{{ID: other.ID, Key: test.Key}}, message, sig, minisign.ErrUnknownKey},
		{"key: right id, other key", []minisign.PublicKey{{ID: test.ID, Key: other.Key}}, message, sig, minisign.ErrBadSignature},
		{"key: wrong length", []minisign.PublicKey{{ID: test.ID, Key: test.Key[:31]}}, message, sig, minisign.ErrMalformed},
		{"algorithm: changed to the legacy Ed", []minisign.PublicKey{test}, message, editDecoded(t, sig, 1, func(b []byte) []byte { return append([]byte("Ed"), b[2:]...) }), minisign.ErrLegacyFormat},
		{"algorithm: a real legacy signature", []minisign.PublicKey{test}, message, read(t, "legacy.minisig"), minisign.ErrLegacyFormat},
		{"signature file: trusted prefix of the spec", []minisign.PublicKey{test}, message, editLine(sig, 2, replaceString("trusted comment:", "trusted_comment:")), minisign.ErrMalformed},
		{"signature file: empty", []minisign.PublicKey{test}, message, nil, minisign.ErrMalformed},
	}
	for _, c := range tampered {
		t.Run("rejects/"+c.name, func(t *testing.T) {
			got, err := verify(c.keys, c.message, c.sig)
			if !errors.Is(err, c.want) {
				t.Errorf("err = %v, want %v", err, c.want)
			}
			if got != "" {
				t.Errorf("returned the trusted comment %q with an error", got)
			}
		})
	}
}
