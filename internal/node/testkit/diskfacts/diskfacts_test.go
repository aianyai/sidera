package diskfacts_test

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/keys"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/testkit/diskfacts"
	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/internal/revocation"
)

var initPaths = []string{
	keys.IdentityKeyPath, keys.NetworkKeyPath, keys.SelfRevocationPath, keys.ClockPath,
	"config/node.json", "config/policy.json", "config/presets.json", "config/instances.json",
}

func TestInitPreservationKeepsTheFirstBytesOfEachProtectedFile(t *testing.T) {
	for _, file := range initPaths {
		t.Run(file, func(t *testing.T) {
			mem := newDisk(t)
			put(t, mem, file, []byte("original"))
			var kept diskfacts.InitPreservation
			if err := kept.Remember(mem); err != nil {
				t.Fatal(err)
			}
			put(t, mem, file, []byte("changed"))
			if err := kept.Remember(mem); err != nil {
				t.Fatal(err)
			}
			if err := kept.Check(mem); err == nil || !strings.Contains(err.Error(), file) {
				t.Fatalf("changed protected file was not named: %v", err)
			}
		})
	}
}

func TestInitPreservationRemembersOnlyFilesThatHaveAppeared(t *testing.T) {
	mem := newDisk(t)
	var kept diskfacts.InitPreservation
	if err := kept.Remember(mem); err != nil {
		t.Fatal(err)
	}
	if err := kept.Check(mem); err != nil {
		t.Fatal(err)
	}
	put(t, mem, keys.IdentityKeyPath, []byte("first actual bytes"))
	if err := kept.Check(mem); err != nil {
		t.Fatal(err)
	}
	if err := kept.Remember(mem); err != nil {
		t.Fatal(err)
	}
	if err := kept.Check(mem); err != nil {
		t.Fatal(err)
	}
	if err := mem.Remove(fsx.Tag{}, keys.IdentityKeyPath); err != nil {
		t.Fatal(err)
	}
	if err := kept.Check(mem); err == nil {
		t.Fatal("a remembered file disappeared without an error")
	}
}

func TestInitPreservationAllowsCertificateAndSerialReissue(t *testing.T) {
	mem := newDisk(t)
	for _, file := range append(append([]string(nil), initPaths...), keys.CertificatePath, keys.SerialPath) {
		put(t, mem, file, []byte("original"))
	}
	var kept diskfacts.InitPreservation
	if err := kept.Remember(mem); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{keys.CertificatePath, keys.SerialPath} {
		put(t, mem, file, []byte("reissued"))
	}
	if err := kept.Check(mem); err != nil {
		t.Fatal(err)
	}
}

func TestSigningSnapshotChecksAllFiveFiles(t *testing.T) {
	for _, file := range []string{keys.IdentityKeyPath, keys.NetworkKeyPath, keys.SelfRevocationPath, keys.CertificatePath, keys.SerialPath} {
		t.Run(file, func(t *testing.T) {
			mem := newDisk(t)
			for _, p := range []string{keys.IdentityKeyPath, keys.NetworkKeyPath, keys.SelfRevocationPath, keys.CertificatePath, keys.SerialPath} {
				put(t, mem, p, []byte("original"))
			}
			before, err := diskfacts.ReadSigning(mem)
			if err != nil {
				t.Fatal(err)
			}
			if same, err := before.Unchanged(mem); err != nil || !same {
				t.Fatalf("unchanged signing files: same=%v, error=%v", same, err)
			}
			put(t, mem, file, []byte("changed"))
			if same, err := before.Unchanged(mem); err != nil || same {
				t.Fatalf("changed signing file: same=%v, error=%v", same, err)
			}
			if err := mem.Remove(fsx.Tag{}, file); err != nil {
				t.Fatal(err)
			}
			if _, err := diskfacts.ReadSigning(mem); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("missing snapshot file: %v", err)
			}
			if _, err := before.Unchanged(mem); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("missing comparison file: %v", err)
			}
		})
	}
}

func TestSnapshotsNeverPrintTheirRetainedBytes(t *testing.T) {
	mem := newDisk(t)
	for _, file := range append(append([]string(nil), initPaths...), keys.CertificatePath, keys.SerialPath) {
		put(t, mem, file, []byte("private bytes"))
	}
	var init diskfacts.InitPreservation
	if err := init.Remember(mem); err != nil {
		t.Fatal(err)
	}
	signing, err := diskfacts.ReadSigning(mem)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		value any
		want  string
	}{
		{"init value", init, "diskfacts.InitPreservation"},
		{"init pointer", &init, "diskfacts.InitPreservation"},
		{"signing value", signing, "diskfacts.SigningSnapshot"},
		{"signing pointer", &signing, "diskfacts.SigningSnapshot"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%X", "%d"} {
				if fmt.Sprintf(format, tc.value) != tc.want {
					t.Errorf("format %s exposed more than the type name", format)
				}
			}
		})
	}
}

func TestTerminalKeepsOnlyTheLocalOwnersRawResult(t *testing.T) {
	id := admin.OperationID{1}
	local := admin.KeyFor(admin.LocalOwner(), id)
	otherOwner := local
	person, err := ident.PersonIDFromKey(testSigner(t, 2).Public())
	if err != nil {
		t.Fatal(err)
	}
	otherOwner.Person = person
	otherID := admin.KeyFor(admin.LocalOwner(), admin.OperationID{2})
	for _, tc := range []struct {
		name string
		rows []store.AdminRecord
		want []byte
	}{
		{name: "no record"},
		{name: "another owner", rows: []store.AdminRecord{{Key: otherOwner, Result: []byte(`{"ok":true}`)}}},
		{name: "another operation", rows: []store.AdminRecord{{Key: otherID, Result: []byte(`{"ok":true}`)}}},
		{name: "nil result", rows: []store.AdminRecord{{Key: local}}},
		{name: "raw result", rows: []store.AdminRecord{{Key: otherOwner, Result: []byte(`{"other":1}`)}, {Key: local, Result: []byte("{ \"ok\" : true }\n")}, {Key: otherID, Result: []byte(`{"other":2}`)}}, want: []byte("{ \"ok\" : true }\n")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scan := &store.Scan{Admin: tc.rows}
			got, err := diskfacts.InspectTerminal(newDisk(t), scan, ident.NodeID{}, id)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got.Persisted, tc.want) || (got.Persisted == nil) != (tc.want == nil) {
				t.Fatal("result lost its raw bytes or absence")
			}
			if tc.want != nil {
				// Neither the scan nor the returned facts may mutate the other.
				scan.Admin[1].Result[0] = '!'
				if !bytes.Equal(got.Persisted, tc.want) {
					t.Fatal("result aliases the scan")
				}
				got.Persisted[1] = '!'
				if scan.Admin[1].Result[1] != tc.want[1] {
					t.Fatal("scan aliases the result")
				}
			}
		})
	}
}

func TestTerminalVerifiesEveryRevocationAndBindsRetirementToTheNode(t *testing.T) {
	signer := testSigner(t, 1)
	node, err := ident.NodeIDFromKey(signer.Public())
	if err != nil {
		t.Fatal(err)
	}
	self, err := revocation.IssueNodeSelfRevocation(signer)
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := revocation.IssueNodeSelfRevocation(testSigner(t, 2))
	if err != nil {
		t.Fatal(err)
	}
	person, err := revocation.IssuePersonSelfRevocation(signer)
	if err != nil {
		t.Fatal(err)
	}
	badSignature, err := revocation.IssueNodeSelfRevocation(testSigner(t, 3))
	if err != nil {
		t.Fatal(err)
	}
	badSignature.Signatures[0].Sig[0] ^= 0xff
	readFailure := errors.New("revocation read failed")
	for _, tc := range []struct {
		name       string
		records    []record.Record
		extraName  string
		extraBytes []byte
		failRead   bool
		retired    bool
		fileReason config.FileReason
	}{
		{name: "none"},
		{name: "foreign node", records: []record.Record{foreign}},
		{name: "another revocation kind", records: []record.Record{person}},
		{name: "this node", records: []record.Record{self, foreign}, retired: true},
		{name: "invalid signature", records: []record.Record{badSignature}, fileReason: config.FileInvalid},
		{name: "wrong digest", extraName: strings.Repeat("0", 64) + ".rec", extraBytes: encode(t, foreign), fileReason: config.FileMisnamed},
		{name: "invalid record after valid self", records: []record.Record{self}, extraName: "zz-invalid.rec", extraBytes: []byte("invalid"), fileReason: config.FileMalformed},
		{name: "read error after valid self", records: []record.Record{self}, extraName: "zz-unreadable.rec", extraBytes: []byte("unreadable"), failRead: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mem := newDisk(t)
			for _, rec := range tc.records {
				put(t, mem, config.RevocationPath(rec.Digest()), encode(t, rec))
			}
			if tc.extraName != "" {
				put(t, mem, path.Join(config.RevocationsDir, tc.extraName), tc.extraBytes)
			}
			var reader fsx.Reader = mem
			if tc.failRead {
				reader = failedRead{Reader: mem, path: path.Join(config.RevocationsDir, tc.extraName), err: readFailure}
			}
			got, err := diskfacts.InspectTerminal(reader, &store.Scan{}, node, admin.OperationID{1})
			if tc.failRead {
				if !errors.Is(err, readFailure) {
					t.Fatalf("later read error was hidden: %v", err)
				}
			} else if tc.fileReason != 0 {
				var fileError *config.FileError
				if !errors.As(err, &fileError) || fileError.Reason != tc.fileReason {
					t.Fatalf("revocation error = %v, want %s", err, tc.fileReason)
				}
			} else if err != nil || got.Retired != tc.retired {
				t.Fatalf("retired=%v, error=%v; want retired=%v", got.Retired, err, tc.retired)
			}
		})
	}
}

type failedRead struct {
	fsx.Reader
	path string
	err  error
}

func (r failedRead) ReadFile(path string) ([]byte, error) {
	if path == r.path {
		return nil, r.err
	}
	return r.Reader.ReadFile(path)
}

func newDisk(t testing.TB) *fsxtest.Mem {
	t.Helper()
	mem := fsxtest.NewMem()
	for _, dir := range []string{keys.Dir, "config", config.RevocationsDir} {
		if err := fsx.MakeDir(mem, fsx.Tag{}, dir); err != nil {
			t.Fatal(err)
		}
	}
	return mem
}

func put(t testing.TB, mem *fsxtest.Mem, file string, data []byte) {
	t.Helper()
	if err := fsx.Replace(mem, fsx.Tag{}, file, data, fsx.ModePrivate); err != nil {
		t.Fatal(err)
	}
}

func testSigner(t testing.TB, seed byte) record.Signer {
	t.Helper()
	signer, err := record.NewEd25519Signer(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, ed25519.SeedSize)))
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

func encode(t testing.TB, rec record.Record) []byte {
	t.Helper()
	data, err := rec.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return data
}
