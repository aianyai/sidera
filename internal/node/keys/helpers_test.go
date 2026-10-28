package keys_test

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"io/fs"
	"math/rand/v2"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/keys"
	"github.com/aianyai/sidera/internal/node/txn"
)

var t0 = time.Date(2026, 10, 24, 6, 0, 0, 0, time.UTC)

// hook is the Observer of a test: it keeps every point and can run a callback
// on each one before its step runs.
type hook struct {
	mu     sync.Mutex
	points []fsx.Point
	on     func(p fsx.Point)
}

func (h *hook) Reached(observation fsx.Observation, _ txn.Kind) {
	p := observation.Point

	h.mu.Lock()
	h.points = append(h.points, p)
	on := h.on
	h.mu.Unlock()
	if on != nil {
		on(p)
	}
}

func (h *hook) names() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, len(h.points))
	for i, p := range h.points {
		out[i] = p.String()
	}
	return out
}

func (h *hook) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.points)
}

// The stand-ins of the composing packages. The cross-owner transactions belong
// to their composers; the roles of this package are the ones they list.
var (
	// initDecl is the init transaction as its composer declares it: the layout
	// directories, the writes of this package, a write of another owner, and the
	// marker last.
	initDecl = &txn.Decl{Name: "init", Kind: txn.Normal, Writes: slices.Concat(
		[]txn.Write{{Role: "dir", Op: txn.OpMakeDir, Many: true, Optional: true}},
		keys.InitWrites(),
		[]txn.Write{{Role: "config", Op: txn.OpReplace, Optional: true}},
		[]txn.Write{keys.InitMarkerWrite()},
	)}
	// initKeysDecl stops before the marker: the state a crash of init leaves.
	initKeysDecl = &txn.Decl{Name: "init-keys", Kind: txn.Normal, Writes: slices.Concat(
		[]txn.Write{{Role: "dir", Op: txn.OpMakeDir, Many: true, Optional: true}},
		keys.InitWrites(),
	)}
	// clearDecl is the clock-clear transaction of the engine: this package's
	// write, then a result in the directory of another owner.
	clearDecl = &txn.Decl{Name: "clock-clear", Kind: txn.Normal, Writes: []txn.Write{
		{Role: keys.RoleClock, Op: txn.OpReplace},
		{Role: "result", Op: txn.OpReplace, Optional: true},
	}}
)

func newTable(tb testing.TB) *txn.Table {
	tb.Helper()
	tab, err := txn.NewTable(slices.Concat(txn.Internal(), keys.Transactions(), []*txn.Decl{initDecl, initKeysDecl, clearDecl})...)
	if err != nil {
		tb.Fatalf("NewTable: %v", err)
	}
	return tab
}

// rig is one process: a Writer over a file system, as after a start.
type rig struct {
	t   testing.TB
	mem *fsxtest.Mem
	w   *txn.Writer
	obs *hook
}

// open starts a process over mem: a Writer, and the cleanup of leftovers that
// every start does first.
func open(tb testing.TB, mem *fsxtest.Mem) *rig { return openOver(tb, mem, mem) }

func openOver(tb testing.TB, mem *fsxtest.Mem, fsys fsx.FS) *rig {
	tb.Helper()
	obs := &hook{}
	w := txn.New(fsys, newTable(tb), obs)
	if err := w.Recover(); err != nil {
		tb.Fatalf("Recover: %v", err)
	}
	obs.points = nil
	return &rig{t: tb, mem: mem, w: w, obs: obs}
}

func newRig(tb testing.TB) *rig { return open(tb, fsxtest.NewMem()) }

// restart is the process dying and starting again over the disk it left.
func (r *rig) restart() *rig { return open(r.t, r.mem.Clone()) }

// runInit does what the composer of init does, with the given declaration for
// the write of the marker or without it.
func (r *rig) runInit(rd io.Reader, now time.Time) (*keys.InitPlan, error) {
	return r.runInitWith(initDecl, true, rd, now)
}

func (r *rig) runInitKeysOnly(rd io.Reader, now time.Time) (*keys.InitPlan, error) {
	return r.runInitWith(initKeysDecl, false, rd, now)
}

func (r *rig) runInitWith(d *txn.Decl, marker bool, rd io.Reader, now time.Time) (*keys.InitPlan, error) {
	r.t.Helper()
	plan, err := keys.PlanInit(r.w.Reader(), rd, now)
	if err != nil {
		return nil, err
	}
	tx := r.w.Begin(d)
	defer tx.Close()
	for _, dir := range []string{"keys", "config"} {
		if d == initKeysDecl && dir == "config" {
			continue
		}
		if _, err := r.w.Reader().Stat(dir); errors.Is(err, fs.ErrNotExist) {
			if err := tx.MakeDir("dir", dir); err != nil {
				return nil, err
			}
		}
	}
	if err := plan.WriteKeys(tx); err != nil {
		return nil, err
	}
	if marker {
		if err := tx.Replace("config", "config/stand-in", []byte("config"), fsx.ModePrivate); err != nil {
			return nil, err
		}
		if err := plan.WriteMarker(tx); err != nil {
			return nil, err
		}
	}
	return plan, tx.Commit()
}

func (r *rig) mustInit(seed byte, now time.Time) *keys.InitPlan {
	r.t.Helper()
	plan, err := r.runInit(newSeeded(seed), now)
	if err != nil {
		r.t.Fatalf("init: %v", err)
	}
	return plan
}

func (r *rig) mustLoad() *keys.Keys {
	r.t.Helper()
	k, err := keys.Load(r.w)
	if err != nil {
		r.t.Fatalf("Load: %v", err)
	}
	return k
}

func (r *rig) read(p string) []byte {
	r.t.Helper()
	b, err := r.mem.ReadFile(p)
	if err != nil {
		r.t.Fatalf("ReadFile(%s): %v", p, err)
	}
	return b
}

// put writes a file durably outside any transaction, as an owner or a damaged
// disk would leave it.
func put(tb testing.TB, mem *fsxtest.Mem, p string, data []byte) {
	tb.Helper()
	if _, err := mem.Stat(path.Dir(p)); errors.Is(err, fs.ErrNotExist) {
		if err := fsx.MakeDir(mem, fsx.Tag{Tx: "setup", Role: "dir"}, path.Dir(p)); err != nil {
			tb.Fatal(err)
		}
	}
	if err := fsx.Replace(mem, fsx.Tag{Tx: "setup", Role: "f"}, p, data, fsx.ModePrivate); err != nil {
		tb.Fatalf("setup Replace(%s): %v", p, err)
	}
}

func remove(tb testing.TB, mem *fsxtest.Mem, p string) {
	tb.Helper()
	if err := fsx.Remove(mem, fsx.Tag{Tx: "setup", Role: "f"}, p); err != nil {
		tb.Fatal(err)
	}
}

// snapshot lists every file by path with its content and every directory as
// "path/", skipping leftovers.
func snapshot(tb testing.TB, r fsx.Reader) map[string]string {
	tb.Helper()
	got := map[string]string{}
	var walk func(dir string)
	walk = func(dir string) {
		entries, err := r.ReadDir(dir)
		if err != nil {
			tb.Fatalf("ReadDir(%q): %v", dir, err)
		}
		for _, e := range entries {
			if fsx.IsLeftover(e.Name) {
				continue
			}
			p := path.Join(dir, e.Name)
			if e.IsDir {
				got[p+"/"] = ""
				walk(p)
				continue
			}
			b, err := r.ReadFile(p)
			if err != nil {
				tb.Fatalf("ReadFile(%q): %v", p, err)
			}
			got[p] = string(b)
		}
	}
	walk(".")
	return got
}

type crashState struct {
	name string
	mem  *fsxtest.Mem
}

// crashStates are the disks a crash at this instant can leave: the process
// dying, the worst power loss, and some seeded ones.
func crashStates(m *fsxtest.Mem, rng *rand.Rand, seeded int) []crashState {
	out := []crashState{{"process-crash", m.Clone()}, {"worst-power-loss", m.CrashCloneWorst()}}
	for i := 0; i < seeded; i++ {
		out = append(out, crashState{fmt.Sprintf("power-loss-%d", i), m.CrashClone(rng)})
	}
	return out
}

// seeded is a deterministic random source that counts what was drawn from it.
type seeded struct {
	c *rand.ChaCha8
	n int
}

func chachaSeed(seed byte) [32]byte {
	var s [32]byte
	s[0] = seed
	return s
}

func newSeeded(seed byte) *seeded { return &seeded{c: rand.NewChaCha8(chachaSeed(seed))} }

func (s *seeded) Read(p []byte) (int, error) {
	s.n += len(p)
	return s.c.Read(p)
}

// stream returns the first n bytes a reader seeded with seed yields.
func stream(seed byte, n int) []byte {
	b := make([]byte, n)
	if _, err := rand.NewChaCha8(chachaSeed(seed)).Read(b); err != nil {
		panic(err)
	}
	return b
}

// ---- oracles: the formats written out again, from their description ----

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

// oracleBody splits a file of the record envelope into its body and checks the
// first line field by field: magic, version, kind, CRC32C, length, key.
func oracleBody(tb testing.TB, kind string, data []byte) []byte {
	tb.Helper()
	nl := bytes.IndexByte(data, '\n')
	if nl < 0 {
		tb.Fatalf("no first line in %q", data)
	}
	f := strings.Split(string(data[:nl]), " ")
	body := data[nl+1:]
	if len(f) != 6 || f[0] != "sidera-record" || f[1] != "1" || f[2] != kind {
		tb.Fatalf("first line %q is not a version 1 %s record", data[:nl], kind)
	}
	if f[3] != fmt.Sprintf("%08x", crc32.Checksum(body, castagnoli)) {
		tb.Fatalf("crc field %s does not match the body", f[3])
	}
	if f[4] != strconv.Itoa(len(body)) {
		tb.Fatalf("length field %s, body is %d bytes", f[4], len(body))
	}
	if f[5] != base64.RawURLEncoding.EncodeToString([]byte(kind)) {
		tb.Fatalf("key field %s is not the kind name", f[5])
	}
	return body
}

func oracleSeed(tb testing.TB, kind string, data []byte) []byte {
	tb.Helper()
	var b struct {
		Algorithm string `json:"algorithm"`
		Seed      string `json:"seed"`
	}
	if err := json.Unmarshal(oracleBody(tb, kind, data), &b); err != nil {
		tb.Fatal(err)
	}
	if b.Algorithm != "ed25519" {
		tb.Fatalf("algorithm %q", b.Algorithm)
	}
	seed, err := base64.RawURLEncoding.DecodeString(b.Seed)
	if err != nil || len(seed) != 32 {
		tb.Fatalf("seed %q: %v", b.Seed, err)
	}
	return seed
}

func oracleSerial(tb testing.TB, data []byte) (node string, serial uint64, digest string) {
	tb.Helper()
	var b struct {
		Node   string `json:"node"`
		Serial uint64 `json:"serial"`
		Digest string `json:"digest"`
	}
	if err := json.Unmarshal(oracleBody(tb, "serial", data), &b); err != nil {
		tb.Fatal(err)
	}
	return b.Node, b.Serial, b.Digest
}

func oracleClock(tb testing.TB, data []byte) (highWater, pausedSince string) {
	tb.Helper()
	var b struct {
		HighWater string `json:"high_water"`
		Paused    string `json:"rollback_paused_since"`
	}
	if err := json.Unmarshal(oracleBody(tb, "clock", data), &b); err != nil {
		tb.Fatal(err)
	}
	return b.HighWater, b.Paused
}

func rfc3339(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05Z") }

// asFileError returns err as a *keys.FileError naming want, or fails.
func asFileError(tb testing.TB, err error, want string) *keys.FileError {
	tb.Helper()
	var fe *keys.FileError
	if !errors.As(err, &fe) {
		tb.Fatalf("error %v is not a *keys.FileError", err)
	}
	if fe.Path != want {
		tb.Fatalf("error names %q, want %q", fe.Path, want)
	}
	if !strings.Contains(err.Error(), want) {
		tb.Fatalf("error text %q does not name %s", err.Error(), want)
	}
	return fe
}

var errBoom = errors.New("boom")

// failingReader is a random source that fails.
type failingReader struct{ n int }

func (f *failingReader) Read([]byte) (int, error) { f.n++; return 0, errBoom }

// shortReader gives n bytes and then ends.
type shortReader struct{ left int }

func (s *shortReader) Read(p []byte) (int, error) {
	if s.left == 0 {
		return 0, io.EOF
	}
	n := min(len(p), s.left)
	s.left -= n
	for i := 0; i < n; i++ {
		p[i] = byte(i + 1)
	}
	return n, nil
}

// goldenNodeOf is the node text of a loaded set of keys.
func goldenNodeOf(k *keys.Keys) string { return k.Node().String() }

// oracleRecordFile is a file of the record envelope for kind and body, written
// from the description of the format.
func oracleRecordFile(kind string, body []byte) []byte {
	head := fmt.Sprintf("sidera-record 1 %s %08x %d %s\n", kind, crc32.Checksum(body, castagnoli), len(body), base64.RawURLEncoding.EncodeToString([]byte(kind)))
	return append([]byte(head), body...)
}

// putInitMarker puts the marker that says init finished, for a test that loads
// the keys of a disk on which only the key side of init was run.
func putInitMarker(tb testing.TB, mem *fsxtest.Mem) {
	tb.Helper()
	put(tb, mem, keys.InitMarkerPath, oracleRecordFile("init", []byte(`{"completed_at":"2026-10-24T06:00:00Z"}`)))
}

func oracleSerialFile(node string, serial uint64, digest string) []byte {
	return oracleRecordFile("serial", []byte(fmt.Sprintf(`{"node":%q,"serial":%d,"digest":%q}`, node, serial, digest)))
}

func decodeStdBase64(s string) ([]byte, error) { return base64.StdEncoding.DecodeString(s) }

func sprintf(verb string, v any) string { return fmt.Sprintf(verb, v) }

func hexString(b []byte) string { return hex.EncodeToString(b) }

// decimalString is how fmt's %v prints a byte slice: [1 2 3].
func decimalString(b []byte) string { return fmt.Sprint(b) }
