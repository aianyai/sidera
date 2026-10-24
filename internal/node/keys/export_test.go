package keys_test

import (
	"bytes"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/keys"
	"github.com/aianyai/sidera/internal/record"
)

// goldenDisk is a disk that holds the golden files: keys 1 and 2, the
// certificate that key 1 signed for key 2 at serial 1, the serial file that
// names it, and the self-revocation of node 1.
func goldenDisk(t *testing.T) *fsxtest.Mem {
	t.Helper()
	mem := fsxtest.NewMem()
	put(t, mem, keys.IdentityKeyPath, []byte(goldenIdentityKeyFile))
	put(t, mem, keys.NetworkKeyPath, []byte(goldenNetworkKeyFile))
	put(t, mem, keys.CertificatePath, goldenCertificate)
	put(t, mem, keys.SerialPath, []byte(goldenSerialFile))
	put(t, mem, keys.SelfRevocationPath, mustHex(goldenSelfRevocationHex))
	put(t, mem, keys.ClockPath, []byte(goldenClockFile))
	put(t, mem, keys.InitMarkerPath, []byte(goldenInitFile))
	return mem
}

// The export format and its digest, against a vector made by an independent
// implementation: the armor lines, the 64-column base64 of the record bytes,
// and the digest as the record package defines it. The digest is also computed
// here, by hand, from its definition, so the vector and the definition agree.
func TestExportFormatGolden(t *testing.T) {
	mem := goldenDisk(t)
	sr, err := keys.ReadSelfRevocation(mem)
	if err != nil {
		t.Fatalf("ReadSelfRevocation: %v", err)
	}
	if !bytes.Equal(sr.Wire(), mustHex(goldenSelfRevocationHex)) {
		t.Error("the record bytes read are not the file's")
	}
	if sr.Node().String() != goldenNode {
		t.Errorf("Node = %s, want %s", sr.Node(), goldenNode)
	}
	if sr.Digest().String() != goldenSelfRevocationDigest {
		t.Errorf("Digest = %s, want %s", sr.Digest(), goldenSelfRevocationDigest)
	}
	rec, err := record.Decode(sr.Wire(), record.DefaultMaxInput)
	if err != nil {
		t.Fatal(err)
	}
	if got := oracleDigest(rec.Type, rec.Payload); got != goldenSelfRevocationDigest {
		t.Errorf("the digest computed by hand = %s, the vector says %s", got, goldenSelfRevocationDigest)
	}

	text, digest := keys.FormatRevocationExport(sr)
	if string(text) != goldenExport {
		t.Errorf("export text:\n%s\nwant:\n%s", text, goldenExport)
	}
	if digest.String() != goldenSelfRevocationDigest {
		t.Errorf("export digest = %s, want %s", digest, goldenSelfRevocationDigest)
	}
	for i, line := range strings.Split(strings.TrimSuffix(string(text), "\n"), "\n") {
		if i > 0 && !strings.HasPrefix(line, "-----") && len(line) > 64 {
			t.Errorf("line %d is %d columns, want at most 64", i, len(line))
		}
	}
}

// The export of a node that init wrote verifies and round trips: the base64
// body decodes to the file's bytes.
func TestExportOfAFreshNodeDecodesToTheFile(t *testing.T) {
	r := newRig(t)
	r.mustInit(1, t0)
	sr, err := keys.ReadSelfRevocation(r.w.Reader())
	if err != nil {
		t.Fatal(err)
	}
	text, digest := keys.FormatRevocationExport(sr)
	lines := strings.Split(strings.TrimSuffix(string(text), "\n"), "\n")
	if lines[0] != "-----BEGIN SIDERA SELF-REVOCATION-----" || lines[len(lines)-1] != "-----END SIDERA SELF-REVOCATION-----" {
		t.Fatalf("armor lines: %q ... %q", lines[0], lines[len(lines)-1])
	}
	if !strings.HasSuffix(string(text), "\n") {
		t.Error("the export does not end in a newline")
	}
	body := strings.Join(lines[1:len(lines)-1], "")
	raw, err := decodeStdBase64(body)
	if err != nil || !bytes.Equal(raw, r.read(keys.SelfRevocationPath)) {
		t.Errorf("the body does not decode to the file's bytes: %v", err)
	}
	if digest != sr.Digest() || len(digest) != 32 {
		t.Error("the digest returned is not the digest of the statement")
	}
	if _, err := hex.DecodeString(digest.String()); err != nil {
		t.Error(err)
	}
}

// ReadSelfRevocation is the one reader behind both paths of the export. It
// verifies the statement by itself and refuses anything else, naming the file.
func TestReadSelfRevocationRefusals(t *testing.T) {
	base := newRig(t)
	base.mustInit(1, t0)
	good := base.read(keys.SelfRevocationPath)
	flipped := append([]byte(nil), good...)
	flipped[len(flipped)-3] ^= 1
	rows := []struct {
		name string
		mem  func() *fsxtest.Mem
		is   error
	}{
		{"missing", func() *fsxtest.Mem { m := base.mem.Clone(); remove(t, m, keys.SelfRevocationPath); return m }, keys.ErrMissing},
		{"signature damaged", func() *fsxtest.Mem { m := base.mem.Clone(); put(t, m, keys.SelfRevocationPath, flipped); return m }, keys.ErrCorrupt},
		{"a certificate", func() *fsxtest.Mem {
			m := base.mem.Clone()
			put(t, m, keys.SelfRevocationPath, base.read(keys.CertificatePath))
			return m
		}, keys.ErrCorrupt},
		{"a revocation of a certificate", func() *fsxtest.Mem {
			m := base.mem.Clone()
			put(t, m, keys.SelfRevocationPath, certRevocationWire(t, base))
			return m
		}, keys.ErrCorrupt},
		{"empty", func() *fsxtest.Mem { m := base.mem.Clone(); put(t, m, keys.SelfRevocationPath, nil); return m }, keys.ErrCorrupt},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			_, err := keys.ReadSelfRevocation(row.mem())
			if !errors.Is(err, row.is) {
				t.Fatalf("ReadSelfRevocation = %v, want %v", err, row.is)
			}
			asFileError(t, err, keys.SelfRevocationPath)
		})
	}
}

// The zero SelfRevocation is not a statement: exporting it would hand the owner
// an empty backup that looks like a success. Only this package makes a
// statement, so a zero one is a programming error and the formatter panics.
func TestFormatRevocationExportPanicsOnAZeroSelfRevocation(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("FormatRevocationExport of the zero SelfRevocation did not panic")
		}
	}()
	keys.FormatRevocationExport(keys.SelfRevocation{})
}
