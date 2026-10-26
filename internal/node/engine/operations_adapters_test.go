package engine_test

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"path"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/testkit/fakeadapter"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// Installing an adapter package, enabling and disabling an instance of it, and the
// presets: each is an operation of the owner's through the one door. The package
// is copied outside the loop into a directory next to the versions, the copy is
// what is parsed, and only the rename that makes it current is a step of the loop.

// ---------------------------------------------------------------------------
// Packages on disks of their own

// relayOptions is what the adapter "relay" declares: a runtime with a command-line
// and an MCP front end, and direct endpoints.
func relayOptions(version string) fakeadapter.ManifestOptions {
	o := fakeadapter.DefaultManifestOptions()
	o.ID, o.Version = "relay", version
	return o
}

func manifestFile(t testing.TB, o fakeadapter.ManifestOptions) string {
	t.Helper()
	raw, err := o.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func mkdirs(t testing.TB, mem *fsxtest.Mem, dir string) {
	t.Helper()
	if dir == "." || dir == "" {
		return
	}
	if ok, err := fsx.Exists(mem, dir); err != nil {
		t.Fatal(err)
	} else if ok {
		return
	}
	mkdirs(t, mem, path.Dir(dir))
	if err := fsx.MakeDir(mem, setupTag, dir); err != nil {
		t.Fatal(err)
	}
}

// packageDisk is a disk that holds the files, rooted at a package directory.
func packageDisk(t testing.TB, files map[string]string) *fsxtest.Mem {
	t.Helper()
	mem := fsxtest.NewMem()
	for p, c := range files {
		mkdirs(t, mem, path.Dir(p))
		if err := fsx.Replace(mem, setupTag, p, []byte(c), fsx.ModePrivate); err != nil {
			t.Fatal(err)
		}
	}
	return mem
}

// relayPackage is the package of relay at the version.
func relayPackage(t testing.TB, version string) *fsxtest.Mem {
	t.Helper()
	o := relayOptions(version)
	return packageDisk(t, map[string]string{
		"sidera-adapter.json": manifestFile(t, o),
		o.Program:             "#!relay " + version + "\n",
		"lib/data.txt":        "data\n",
	})
}

// otherOS is an operating system of the manifest's vocabulary that is not this one.
func otherOS() string {
	if runtime.GOOS == "linux" {
		return "darwin"
	}
	return "linux"
}

// foreignManifest is the manifest of relay for a platform this one is not.
func foreignManifest(t testing.TB, version string) string {
	t.Helper()
	text := manifestFile(t, relayOptions(version))
	swapped := regexp.MustCompile(`"os":\s*"`+runtime.GOOS+`"`).ReplaceAllString(text, `"os": "`+otherOS()+`"`)
	if swapped == text {
		t.Fatalf("the manifest names no platform %q:\n%s", runtime.GOOS, text)
	}
	return swapped
}

// countingReader counts what is read from a package.
type countingReader struct {
	fsx.Reader
	n atomic.Int64
}

func (c *countingReader) ReadFile(p string) ([]byte, error) {
	c.n.Add(1)
	return c.Reader.ReadFile(p)
}

func (c *countingReader) ReadDir(p string) ([]fsx.Entry, error) {
	c.n.Add(1)
	return c.Reader.ReadDir(p)
}

func (c *countingReader) reads() int64 { return c.n.Load() }

// swappingReader is a package that is another package once it has been read: the
// manifest is `first` on its first read and `later` on every read after it, as when
// the owner, or someone else, replaces the file while it is being installed.
type swappingReader struct {
	fsx.Reader
	name         string
	first, later string
	mu           sync.Mutex
	reads        int
}

func (s *swappingReader) ReadFile(p string) ([]byte, error) {
	if p != s.name {
		return s.Reader.ReadFile(p)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads++
	if s.reads == 1 {
		return []byte(s.first), nil
	}
	return []byte(s.later), nil
}

// gatedReader holds the first read of a package until it is released, so that a
// test can have an installation in the middle of its copy.
type gatedReader struct {
	fsx.Reader
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func newGatedReader(r fsx.Reader) *gatedReader {
	return &gatedReader{Reader: r, entered: make(chan struct{}), release: make(chan struct{})}
}

func (g *gatedReader) ReadFile(p string) ([]byte, error) {
	g.once.Do(func() {
		close(g.entered)
		<-g.release
	})
	return g.Reader.ReadFile(p)
}

// ---------------------------------------------------------------------------
// The requests, as the management interface builds them

// idBase keeps the identifiers of these requests clear of the two that the fixture
// used to install and enable the adapter of the node.
const idBase = 128

func encodeInstalled(a engine.AdapterInstalled) ([]byte, error) {
	return json.Marshal(struct {
		Adapter struct {
			ID      string `json:"id"`
			Version string `json:"version"`
		} `json:"adapter"`
	}{Adapter: struct {
		ID      string `json:"id"`
		Version string `json:"version"`
	}{a.ID, a.Version}})
}

func installCommand(b byte, content, source string) engine.ManagementCommand {
	return engine.ManagementCommand{
		Key: requestKey(b + idBase), Fingerprint: fingerprintOf(content), Name: "adapters.install",
		Operation: engine.InstallAdapterOp(source, encodeInstalled),
	}
}

// installCommandSeeing is installCommand that tells what the installation did.
func installCommandSeeing(b byte, content, source string, did *engine.AdapterInstalled) engine.ManagementCommand {
	c := installCommand(b, content, source)
	c.Operation = engine.InstallAdapterOp(source, func(a engine.AdapterInstalled) ([]byte, error) {
		*did = a
		return encodeInstalled(a)
	})
	return c
}

func encodeInstance(c engine.InstanceChange) ([]byte, error) {
	return json.Marshal(struct {
		Instance string `json:"instance"`
		Enabled  bool   `json:"enabled"`
		Version  uint64 `json:"version"`
	}{c.Instance, c.Enabled, uint64(c.Version)})
}

func enableCommand(b byte, content string, expected admin.Version, adapter, instance, doc string) engine.ManagementCommand {
	var raw []byte
	if doc != "" {
		raw = []byte(doc)
	}
	return engine.ManagementCommand{
		Key: requestKey(b + idBase), Fingerprint: fingerprintOf(content), Expected: expected, Name: "adapters.enable",
		Operation: engine.EnableAdapterOp(adapter, instance, raw, encodeInstance),
	}
}

func disableCommand(b byte, content string, expected admin.Version, instance string) engine.ManagementCommand {
	return engine.ManagementCommand{
		Key: requestKey(b + idBase), Fingerprint: fingerprintOf(content), Expected: expected, Name: "adapters.disable",
		Operation: engine.DisableAdapterOp(instance, encodeInstance),
	}
}

func encodePreset(c engine.PresetChange) ([]byte, error) {
	return json.Marshal(struct {
		Name    string `json:"name"`
		Version uint64 `json:"version"`
	}{c.Name, uint64(c.Version)})
}

func addPresetCommand(b byte, content string, expected admin.Version, name, doc string) engine.ManagementCommand {
	return engine.ManagementCommand{
		Key: requestKey(b + idBase), Fingerprint: fingerprintOf(content), Expected: expected, Name: "presets.add",
		Operation: engine.AddPresetOp(name, []byte(doc), encodePreset),
	}
}

func removePresetCommand(b byte, content string, expected admin.Version, name string) engine.ManagementCommand {
	return engine.ManagementCommand{
		Key: requestKey(b + idBase), Fingerprint: fingerprintOf(content), Expected: expected, Name: "presets.remove",
		Operation: engine.RemovePresetOp(name, encodePreset),
	}
}

func changeReason(t testing.TB, err error) config.ChangeReason {
	t.Helper()
	var ce *config.ChangeError
	if !errors.As(err, &ce) {
		t.Fatalf("error %v is no refused change of the configuration", err)
	}
	return ce.Reason
}

func installedVersion(cfg *config.Config, id string) (string, bool) {
	for _, a := range cfg.Adapters() {
		if a.ID == id {
			return a.Version, true
		}
	}
	return "", false
}

// ---------------------------------------------------------------------------
// Installing

// A package is copied once into the directory next to the versions, parsed there,
// and made current by one rename; nothing of the staging is left behind. The same
// request sent again, to this process or to the next, is answered with the result
// that was stored, and the package is not read again: the copy is the expensive
// part, and the stored result says the request was done.
func TestInstallingACopiedPackageStoresItsResultAndAResendCopiesNothing(t *testing.T) {
	noLeaks(t)
	src := &countingReader{Reader: relayPackage(t, "1.0.0")}
	r := newRig(t, 10, withPackage("/src/relay", src))
	e := r.start()

	var did engine.AdapterInstalled
	first, err := e.Execute(ctx, installCommandSeeing(1, "install relay", "/src/relay", &did))
	if err != nil || first.Replayed {
		t.Fatalf("the installation: %+v, %v", first, err)
	}
	if want := `{"adapter":{"id":"relay","version":"1.0.0"}}`; string(first.Result) != want {
		t.Errorf("the result is %s, want %s", first.Result, want)
	}
	if did.ID != "relay" || did.Version != "1.0.0" || did.AlreadyCurrent {
		t.Errorf("the installation did %+v", did)
	}
	if v, ok := installedVersion(r.cfg, "relay"); !ok || v != "1.0.0" {
		t.Fatalf("the configuration holds relay %q (%v), want 1.0.0", v, ok)
	}
	// The files are where the version directory is, with the program of the
	// manifest, and the pointer names the version.
	dir := config.AdapterVersionDir("relay", "1.0.0")
	program := relayOptions("1.0.0").Program
	for p, want := range map[string]string{
		path.Join(dir, "sidera-adapter.json"): manifestFile(t, relayOptions("1.0.0")),
		path.Join(dir, program):               "#!relay 1.0.0\n",
		path.Join(dir, "lib", "data.txt"):     "data\n",
	} {
		got, err := r.mem.ReadFile(p)
		if err != nil || string(got) != want {
			t.Errorf("%s is %q (%v), want %q", p, got, err, want)
		}
	}
	if cur, err := r.mem.ReadFile(config.AdapterCurrentPath("relay")); err != nil {
		t.Errorf("the current pointer: %v", err)
	} else if v, err := config.DecodeAdapterCurrent("relay", cur); err != nil || v != "1.0.0" {
		t.Errorf("the pointer names %q (%v)", v, err)
	}
	if work, err := config.AdapterGCWork(r.mem); err != nil || len(work) != 0 {
		t.Errorf("garbage left in the adapter directory: %v (%v)", work, err)
	}
	if _, ok := r.adminResultOf(r.scan(), installCommand(1, "install relay", "/src/relay")); !ok {
		t.Error("the result is not stored under state/admin/")
	}

	// A resend is the stored result and reads nothing.
	reads, staged := src.reads(), r.commits("adapter-stage")
	if reads == 0 || staged < 1 {
		t.Fatalf("the package was read %d times and staged %d times: the first installation copied nothing", reads, staged)
	}
	again, err := e.Execute(ctx, installCommand(1, "install relay", "/src/relay"))
	if err != nil || !again.Replayed || string(again.Result) != string(first.Result) {
		t.Fatalf("the resend: %+v, %v", again, err)
	}
	if got := src.reads(); got != reads {
		t.Errorf("the resend read the package %d more times", got-reads)
	}
	if got := r.commits("adapter-stage"); got != staged {
		t.Errorf("the resend staged the package again (%d commits, was %d)", got, staged)
	}

	// The same holds after a restart, where the replay table is built from disk.
	r.restart()
	e = r.start()
	again, err = e.Execute(ctx, installCommand(1, "install relay", "/src/relay"))
	if err != nil || !again.Replayed || string(again.Result) != string(first.Result) {
		t.Fatalf("the resend after a restart: %+v, %v", again, err)
	}
	if got := src.reads(); got != reads {
		t.Errorf("the resend after a restart read the package %d more times", got-reads)
	}
	if got := r.commits("adapter-stage"); got >= 1 {
		t.Errorf("the resend after a restart staged the package (%d commits)", got)
	}

	// The identifier of another request is refused before anything is read.
	_, err = e.Execute(ctx, installCommand(1, "install something else", "/src/relay"))
	wantReason(t, err, engine.ReasonOperationIDReused)
	if got := src.reads(); got != reads {
		t.Errorf("the reused identifier read the package %d more times", got-reads)
	}
}

// What is judged is the copy that is installed, and not the source it was copied
// from: a package whose manifest is replaced while it is being read is installed
// as it was copied, and its platform is that of the copy. A check made on the
// source would pass the package that is then installed.
func TestThePlatformIsJudgedOnTheCopyThatWouldBeInstalledAndNotOnTheSource(t *testing.T) {
	noLeaks(t)
	good := manifestFile(t, relayOptions("1.0.0"))
	bad := foreignManifest(t, "1.0.0")
	src := &swappingReader{Reader: relayPackage(t, "1.0.0"), name: "sidera-adapter.json", first: good, later: bad}
	r := newRig(t, 10, withPackage("/src/relay", src))
	e := r.start()
	before := r.snapshot()

	_, err := e.Execute(ctx, installCommand(2, "install relay", "/src/relay"))
	wantReason(t, err, engine.ReasonPlatformUnsupported)
	if !reflect.DeepEqual(before, r.snapshot()) {
		t.Error("a package the platform does not run left something on the disk")
	}
	if _, ok := installedVersion(r.cfg, "relay"); ok {
		t.Error("the package is installed")
	}
	if r.commits("adapter-unstage") < 1 {
		t.Error("the copy was dropped without the transaction that drops it")
	}
}

// What is refused is refused with nothing left on the disk and nothing stored, so
// that the same identifier can be used again for a package that is right.
func TestAPackageThatIsRefusedLeavesNothingAndItsIdentifierFree(t *testing.T) {
	noLeaks(t)
	good := relayPackage(t, "1.0.0")
	o := relayOptions("1.0.0")
	for _, tc := range []struct {
		name string
		src  fsx.Reader
		want func(t *testing.T, err error)
	}{
		{"a manifest that does not parse", packageDisk(t, map[string]string{"sidera-adapter.json": "{}", o.Program: "x"}),
			func(t *testing.T, err error) {
				if got := changeReason(t, err); got != config.ChangeInvalid {
					t.Errorf("reason %v, want invalid", got)
				}
			}},
		{"a package with no manifest", packageDisk(t, map[string]string{o.Program: "x"}),
			func(t *testing.T, err error) {
				if got := changeReason(t, err); got != config.ChangeInvalid {
					t.Errorf("reason %v, want invalid", got)
				}
			}},
		{"a manifest for another platform", packageDisk(t, map[string]string{"sidera-adapter.json": foreignManifest(t, "1.0.0"), o.Program: "x"}),
			func(t *testing.T, err error) { wantReason(t, err, engine.ReasonPlatformUnsupported) }},
		{"a package without its program", packageDisk(t, map[string]string{"sidera-adapter.json": manifestFile(t, o)}),
			func(t *testing.T, err error) {
				if got := changeReason(t, err); got != config.ChangeInvalid {
					t.Errorf("reason %v, want invalid", got)
				}
			}},
		{"a name that the cleanup would take for debris", packageDisk(t, map[string]string{"sidera-adapter.json": manifestFile(t, o), o.Program: "x", "lib/.tmp-x": "x"}),
			func(t *testing.T, err error) {
				if got := changeReason(t, err); got != config.ChangeInvalid {
					t.Errorf("reason %v, want invalid", got)
				}
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, 10, withPackage("/src/bad", tc.src), withPackage("/src/good", good))
			e := r.start()
			before := r.snapshot()
			_, err := e.Execute(ctx, installCommand(3, "install", "/src/bad"))
			if err == nil {
				t.Fatal("the package was installed")
			}
			tc.want(t, err)
			if !reflect.DeepEqual(before, r.snapshot()) {
				t.Error("the refused package left something on the disk")
			}
			// the identifier was not used up: the same request, for a package that is
			// right, is carried out
			if _, err := e.Execute(ctx, installCommand(3, "install", "/src/good")); err != nil {
				t.Errorf("the identifier of a refused request, used again: %v", err)
			}
		})
	}
	t.Run("a directory that is not there", func(t *testing.T) {
		r := newRig(t, 10)
		e := r.start()
		before := r.snapshot()
		_, err := e.Execute(ctx, installCommand(4, "install", "/src/nothing"))
		wantInvalid(t, err, "source", engine.DetailSourceUnreadable)
		if !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("error %v, want the cause to be that the directory does not exist", err)
		}
		if !reflect.DeepEqual(before, r.snapshot()) {
			t.Error("a directory that is not there left something on the disk")
		}
	})
}

// A version that is already the current one is installed: the request is answered
// at once with its result, nothing is renamed and the copy that was made is dropped.
func TestInstallingTheVersionThatIsCurrentWritesOnlyItsResult(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10, withPackage("/src/relay", relayPackage(t, "1.0.0")))
	e := r.start()
	if _, err := e.Execute(ctx, installCommand(5, "install relay", "/src/relay")); err != nil {
		t.Fatal(err)
	}
	before := r.snapshot()

	var did engine.AdapterInstalled
	res, err := e.Execute(ctx, installCommandSeeing(6, "install relay once more", "/src/relay", &did))
	if err != nil || res.Replayed {
		t.Fatalf("the second installation: %+v, %v", res, err)
	}
	if !did.AlreadyCurrent || did.ID != "relay" || did.Version != "1.0.0" {
		t.Errorf("the installation did %+v, want the version found current", did)
	}
	after := r.snapshot()
	for p, want := range before {
		if after[p] != want {
			t.Errorf("%s changed", p)
		}
	}
	added := 0
	for p := range after {
		if _, ok := before[p]; !ok {
			added++
			if !strings.HasPrefix(p, "state/admin/") {
				t.Errorf("%s was written besides the result", p)
			}
		}
	}
	if added != 1 {
		t.Errorf("%d files were added, want the one result", added)
	}
	if work, err := config.AdapterGCWork(r.mem); err != nil || len(work) != 0 {
		t.Errorf("garbage left: %v (%v)", work, err)
	}
	// and the result is stored: a resend of this request is answered from it
	again, err := e.Execute(ctx, installCommand(6, "install relay once more", "/src/relay"))
	if err != nil || !again.Replayed || string(again.Result) != string(res.Result) {
		t.Errorf("the resend: %+v, %v", again, err)
	}
}

// A new version replaces the old one, which is then gone, while no instance runs
// the old one; while an instance of the adapter is enabled the upgrade is refused
// and nothing is left of the copy.
func TestUpgradingWhileAnInstanceIsEnabledIsRefusedAndOtherwiseReplacesTheVersion(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10, withPackage("/src/relay1", relayPackage(t, "1.0.0")), withPackage("/src/relay2", relayPackage(t, "2.0.0")))
	e := r.start()
	if _, err := e.Execute(ctx, installCommand(7, "install 1", "/src/relay1")); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Execute(ctx, enableCommand(8, "enable", r.cfg.Instances().Version, "relay", "relay", "")); err != nil {
		t.Fatal(err)
	}
	before := r.snapshot()
	_, err := e.Execute(ctx, installCommand(9, "install 2", "/src/relay2"))
	if got := changeReason(t, err); got != config.ChangeAdapterInUse {
		t.Fatalf("the upgrade of an adapter that runs: %v, want it in use", err)
	}
	if !reflect.DeepEqual(before, r.snapshot()) {
		t.Error("the refused upgrade left something on the disk")
	}
	if v, _ := installedVersion(r.cfg, "relay"); v != "1.0.0" {
		t.Errorf("version %s is installed after the refusal", v)
	}

	if _, err := e.Execute(ctx, disableCommand(10, "disable", r.cfg.Instances().Version, "relay")); err != nil {
		t.Fatal(err)
	}
	settle(t, e)
	if _, err := e.Execute(ctx, installCommand(11, "install 2 again", "/src/relay2")); err != nil {
		t.Fatalf("the upgrade of an adapter that does not run: %v", err)
	}
	if v, _ := installedVersion(r.cfg, "relay"); v != "2.0.0" {
		t.Errorf("version %s is installed, want 2.0.0", v)
	}
	if ok, _ := fsx.Exists(r.mem, config.AdapterVersionDir("relay", "1.0.0")); ok {
		t.Error("the old version is still on the disk")
	}
	if ok, _ := fsx.Exists(r.mem, config.AdapterVersionDir("relay", "2.0.0")); !ok {
		t.Error("the new version is not on the disk")
	}
	if work, err := config.AdapterGCWork(r.mem); err != nil || len(work) != 0 {
		t.Errorf("garbage left: %v (%v)", work, err)
	}
}

// One package is copied at a time: a second request that arrives while the first
// is being copied is told so and has changed nothing, and the engine is not quiet
// while the copy goes on.
func TestOnlyOneInstallationRunsAtATime(t *testing.T) {
	noLeaks(t)
	gate := newGatedReader(relayPackage(t, "1.0.0"))
	other := packageDisk(t, map[string]string{
		"sidera-adapter.json":         strings.Replace(manifestFile(t, relayOptions("1.0.0")), `"relay"`, `"other"`, 1),
		relayOptions("1.0.0").Program: "x",
	})
	r := newRig(t, 10, withPackage("/src/relay", gate), withPackage("/src/other", other))
	e := r.start()

	done := make(chan error, 1)
	go func() {
		_, err := e.Execute(ctx, installCommand(12, "install relay", "/src/relay"))
		done <- err
	}()
	<-gate.entered

	_, err := e.Execute(ctx, installCommand(13, "install other", "/src/other"))
	wantReason(t, err, engine.ReasonInstallInProgress)
	if _, ok := installedVersion(r.cfg, "other"); ok {
		t.Error("the refused installation went on")
	}
	qctx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	if rep, err := e.Quiesce(qctx, engine.Expect{}); err == nil || rep.Workers != 1 {
		t.Errorf("Quiesce = %+v, %v; want the install worker counted and the engine not quiet", rep, err)
	}

	close(gate.release)
	if err := <-done; err != nil {
		t.Fatalf("the installation that was running: %v", err)
	}
	settle(t, e)
	// the one that was refused stored nothing, so it is carried out when it is sent again
	if _, err := e.Execute(ctx, installCommand(13, "install other", "/src/other")); err != nil {
		t.Errorf("the refused request sent again: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Enabling and disabling

// Enabling an instance of an installed adapter makes it exist, enabled, and has the
// adapter host start it; what is refused is refused before the host is told.
func TestEnablingAnInstanceStartsItThroughTheHost(t *testing.T) {
	noLeaks(t)
	host := newHostDouble()
	r := newRig(t, 10, withAdapters(host), withPackage("/src/relay", relayPackage(t, "1.0.0")))
	e := r.start()
	if _, err := e.Execute(ctx, installCommand(20, "install", "/src/relay")); err != nil {
		t.Fatal(err)
	}
	if got := host.asks(); len(got) != 0 {
		t.Fatalf("installing asked the host for %v", got)
	}

	version := r.cfg.Instances().Version
	res, err := e.Execute(ctx, enableCommand(21, "enable", version, "relay", "relay", ""))
	if err != nil || res.Replayed {
		t.Fatalf("enabling: %+v, %v", res, err)
	}
	if want := `{"instance":"relay","enabled":true,"version":` + itoa(uint64(version)+1) + `}`; string(res.Result) != want {
		t.Errorf("the result is %s, want %s", res.Result, want)
	}
	if in, ok := r.cfg.Instances().Items["relay"]; !ok || !in.Enabled || in.Adapter != "relay" {
		t.Errorf("the instance is %+v (%v)", in, ok)
	}
	if got := host.asks(); !reflect.DeepEqual(got, []string{"run:relay"}) {
		t.Errorf("the host was asked for %v, want it to run relay", got)
	}
	// the resend is the stored result, and starts nothing
	again, err := e.Execute(ctx, enableCommand(21, "enable", version, "relay", "relay", ""))
	if err != nil || !again.Replayed || string(again.Result) != string(res.Result) {
		t.Errorf("the resend: %+v, %v", again, err)
	}
	if got := host.asks(); len(got) != 1 {
		t.Errorf("the resend asked the host again: %v", got)
	}

	// what is refused starts nothing
	next := r.cfg.Instances().Version
	for _, tc := range []struct {
		name string
		cmd  engine.ManagementCommand
		want config.ChangeReason
	}{
		{"an adapter that is not installed", enableCommand(22, "ghost", next, "ghost", "ghost", ""), config.ChangeAdapterNotInstalled},
		{"a version that is not current", enableCommand(23, "stale", next-1, "relay", "second", ""), config.ChangeVersionConflict},
		{"an instance of another adapter", enableCommand(24, "other adapter", next, "chat", "relay", ""), config.ChangeInstanceAdapterMismatch},
		{"a configuration for an instance that exists", enableCommand(25, "config", next, "relay", "relay", `{"x":1}`), config.ChangeInstanceExists},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := r.snapshot()
			_, err := e.Execute(ctx, tc.cmd)
			if got := changeReason(t, err); got != tc.want {
				t.Errorf("reason %v, want %v", got, tc.want)
			}
			if !reflect.DeepEqual(before, r.snapshot()) {
				t.Error("a refused enabling wrote to the disk")
			}
			if got := host.asks(); len(got) != 1 {
				t.Errorf("a refused enabling asked the host: %v", got)
			}
		})
	}
}

func itoa(n uint64) string { b, _ := json.Marshal(n); return string(b) }

// Disabling an instance stops it through the host, marks it offline in the
// engine, which the host does not report when it is stopped, closes its endpoints
// and rejects what is queued for them, and keeps the instance in the
// configuration with its stable ids.
func TestDisablingAnInstanceStopsItMarksItOfflineAndClosesItsEndpoints(t *testing.T) {
	noLeaks(t)
	host := newHostDouble()
	r := newRig(t, 10, withAdapters(host), withParams(engine.Params{ChunkReplaces: 2}))
	e := r.start()
	a, b := register(t, e, "a"), register(t, e, "b")
	r.restart()
	r.queue(1, 4, a)
	r.queue(5, 3, b)
	e = r.start()
	if err := e.AdapterStarted(ctx, "chat", adapterproto.StartResult{}); err != nil {
		t.Fatal(err)
	}
	if v, err := e.Adapter(ctx, "chat"); err != nil || !v.Online {
		t.Fatalf("the instance before: %+v, %v; want it online", v, err)
	}
	ids := r.cfg.EndpointIDs("chat")
	r.clk.Advance(3 * time.Minute)
	disabledAt := r.clk.Now()

	version := r.cfg.Instances().Version
	res, err := e.Execute(ctx, disableCommand(30, "disable", version, "chat"))
	if err != nil || res.Replayed {
		t.Fatalf("disabling: %+v, %v", res, err)
	}
	if want := `{"instance":"chat","enabled":false,"version":` + itoa(uint64(version)+1) + `}`; string(res.Result) != want {
		t.Errorf("the result is %s, want %s", res.Result, want)
	}
	if got := host.asks(); !reflect.DeepEqual(got, []string{"stop:chat"}) {
		t.Errorf("the host was asked for %v, want it to stop chat", got)
	}
	settle(t, e)
	if got := openEndpoints(t, e); got != 0 {
		t.Errorf("%d endpoints are open after the instance was disabled", got)
	}
	sc := r.scan()
	if len(sc.In) != 7 {
		t.Fatalf("%d target records, want 7", len(sc.In))
	}
	for _, in := range sc.In {
		if in.Record.State != delivery.StateRejected {
			t.Errorf("record %s is %v, want it rejected with its endpoint closed", in.Key.Message, in.Record.State)
		}
	}
	if len(sc.Bodies) != 0 {
		t.Errorf("%d bodies are kept for rejected records", len(sc.Bodies))
	}
	in, ok := r.cfg.Instances().Items["chat"]
	if !ok || in.Enabled {
		t.Errorf("the instance is %+v (%v), want it kept and disabled", in, ok)
	}
	if got := r.cfg.EndpointIDs("chat"); !reflect.DeepEqual(got, ids) {
		t.Errorf("the stable ids are %+v, want %+v kept", got, ids)
	}
	// a disabled instance is not known to the engine, and it is not told that it is
	// gone either: the host reported nothing, and the disabling marked it
	_, err = e.Adapter(ctx, "chat")
	wantReason(t, err, engine.ReasonInstanceUnknown)

	// enabled again, it is the instance that went offline when it was disabled
	r.clk.Advance(time.Minute)
	if _, err := e.Execute(ctx, enableCommand(31, "enable", r.cfg.Instances().Version, "chat", "chat", "")); err != nil {
		t.Fatal(err)
	}
	v, err := e.Adapter(ctx, "chat")
	if err != nil {
		t.Fatal(err)
	}
	if v.Online || !v.OfflineSince.Equal(disabledAt) {
		t.Errorf("after enabling again the instance is online=%v, offline since %v; want offline since the disabling, %v", v.Online, v.OfflineSince, disabledAt)
	}
	if got := host.asks(); !reflect.DeepEqual(got, []string{"stop:chat", "run:chat"}) {
		t.Errorf("the host was asked for %v", got)
	}
}

// An instance that is enabled again while the records of its endpoints are still
// being closed waits for the closing to end: the endpoints that were there when
// the owner disabled it are closed when it runs again, and the answer to the
// request that enables it comes after.
func TestEnablingAnInstanceWaitsForTheDrainOfItsDisabling(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10, withAdapters(newHostDouble()),
		withParams(engine.Params{ChunkReplaces: 1, RetryBackoff: 10 * time.Second}))
	e := r.start()
	a := register(t, e, "a")
	r.restart()
	r.queue(1, 3, a)
	e = r.start()
	rule := r.faulty.Fail(matchT{Tx: "endpoint-drain", Step: fsx.StepWrite, Hit: 1}, syscall.EIO)

	release := engine.StallLoop(t, e)
	version := r.cfg.Instances().Version
	disabled := make(chan error, 1)
	go func() {
		_, err := e.Execute(ctx, disableCommand(32, "disable", version, "chat"))
		disabled <- err
	}()
	engine.WaitInbox(t, e, 1)
	enabled := make(chan error, 1)
	go func() {
		_, err := e.Execute(ctx, enableCommand(33, "enable", version+1, "chat", "chat", ""))
		enabled <- err
	}()
	engine.WaitInbox(t, e, 2)
	release()
	if err := <-disabled; err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool { return rule.Hits() > 0 })
	qctx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	if rep, err := e.Quiesce(qctx, engine.Expect{}); err == nil || rep.Parked != 1 {
		t.Fatalf("Quiesce = %+v, %v; want the enabling parked until the closing is done", rep, err)
	}
	select {
	case err := <-enabled:
		t.Fatalf("the instance was enabled (%v) before the records of its endpoints were closed", err)
	default:
	}
	r.clk.Advance(10 * time.Second)
	settle(t, e)
	if err := <-enabled; err != nil {
		t.Fatalf("enabling after the closing: %v", err)
	}
	if in := r.cfg.Instances().Items["chat"]; !in.Enabled {
		t.Error("the instance is not enabled")
	}
	for _, in := range r.scan().In {
		if in.Record.State != delivery.StateRejected {
			t.Errorf("record %s is %v after the closing", in.Key.Message, in.Record.State)
		}
	}
}

// ---------------------------------------------------------------------------
// Presets

// A preset is added and removed through the door, each with the classification
// the administrative rules judge it by (adding loosens, removing narrows), the
// version of the whole collection, and the statement about local sources that no
// preset may be without.
func TestPresetsAreAddedAndRemovedWithTheirClassAndTheCollectionVersion(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	e := r.start()
	if op := engine.AddPresetOp("x", nil, encodePreset); op.Admin() != (admin.Operation{Kind: admin.OpChangePreset, PresetChange: admin.PresetChangeLoosening}) {
		t.Errorf("adding a preset is %+v", op.Admin())
	}
	if op := engine.RemovePresetOp("x", encodePreset); op.Admin() != (admin.Operation{Kind: admin.OpChangePreset, PresetChange: admin.PresetChangeNarrowing}) {
		t.Errorf("removing a preset is %+v", op.Admin())
	}

	version := r.cfg.Presets().Version
	res, err := e.Execute(ctx, addPresetCommand(40, "add", version, "tight", `{"local_sources":"deny","description":"closed"}`))
	if err != nil || res.Replayed {
		t.Fatalf("adding: %+v, %v", res, err)
	}
	if want := `{"name":"tight","version":` + itoa(uint64(version)+1) + `}`; string(res.Result) != want {
		t.Errorf("the result is %s, want %s", res.Result, want)
	}
	if p, ok := r.cfg.Presets().Items["tight"]; !ok || p.Description != "closed" {
		t.Errorf("the preset is %+v (%v)", p, ok)
	}
	again, err := e.Execute(ctx, addPresetCommand(40, "add", version, "tight", `{"local_sources":"deny","description":"closed"}`))
	if err != nil || !again.Replayed || string(again.Result) != string(res.Result) {
		t.Errorf("the resend: %+v, %v", again, err)
	}

	next := r.cfg.Presets().Version
	for _, tc := range []struct {
		name string
		cmd  engine.ManagementCommand
		want config.ChangeReason
	}{
		{"a name that is taken", addPresetCommand(41, "dup", next, "tight", `{"local_sources":"allow"}`), config.ChangePresetExists},
		{"no statement about local sources", addPresetCommand(42, "none", next, "loose", `{"description":"x"}`), config.ChangeInvalid},
		{"a statement that is none", addPresetCommand(43, "maybe", next, "loose", `{"local_sources":"maybe"}`), config.ChangeInvalid},
		{"a stale version", addPresetCommand(44, "stale", next-1, "loose", `{"local_sources":"allow"}`), config.ChangeVersionConflict},
		{"removing a preset that is not there", removePresetCommand(45, "unknown", next, "ghost"), config.ChangePresetUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := r.snapshot()
			_, err := e.Execute(ctx, tc.cmd)
			if got := changeReason(t, err); got != tc.want {
				t.Errorf("reason %v, want %v", got, tc.want)
			}
			if !reflect.DeepEqual(before, r.snapshot()) {
				t.Error("a refused change wrote to the disk")
			}
		})
	}

	res, err = e.Execute(ctx, removePresetCommand(46, "remove", r.cfg.Presets().Version, "tight"))
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"name":"tight","version":` + itoa(uint64(next)+1) + `}`; string(res.Result) != want {
		t.Errorf("the result is %s, want %s", res.Result, want)
	}
	if _, ok := r.cfg.Presets().Items["tight"]; ok {
		t.Error("the preset is still there")
	}
}

// ---------------------------------------------------------------------------
// Install, then enable, then the probe

// relayAdmission is what the host tells the engine when it has admitted relay.
func relayAdmission(t testing.TB) (adapterreg.Admission, adapterproto.InitializeResult) {
	t.Helper()
	o := relayOptions("1.0.0")
	adm, err := adapterreg.Admit(o.Capabilities(), o.Manifest())
	if err != nil {
		t.Fatal(err)
	}
	return adm, adapterproto.InitializeResult{
		ProtocolVersion: 1, Adapter: adapterproto.AdapterInfo{ID: "relay", Version: "1.0.0"}, Capabilities: adm.Capabilities(),
	}
}

// probeRan is what an adapter answers adapter.probe when each of its tool probes
// ran.
func probeRan(reqs []adapterproto.ToolProbeRequest) adapterproto.ProbeResult {
	res := adapterproto.ProbeResult{
		Runtime: &adapterproto.RuntimeInfo{Name: "fake-runtime", Version: "1.0.0"},
		Auth:    adapterproto.AuthValid, Dependencies: []adapterproto.Dependency{}, Presets: []adapterproto.PresetStatus{},
		ToolProbes: []adapterproto.ToolProbeResult{},
	}
	seen := map[string]bool{}
	for _, q := range reqs {
		if !seen[q.Preset] {
			seen[q.Preset] = true
			res.Presets = append(res.Presets, adapterproto.PresetStatus{Name: q.Preset, Usable: true})
		}
		res.ToolProbes = append(res.ToolProbes, adapterproto.ToolProbeResult{
			Preset: q.Preset, Frontend: q.Frontend, Outcome: adapterproto.ProbeRan, Method: adapterproto.ProbeUnsandboxed,
		})
	}
	return res
}

// The owner installs a package and enables an instance of it; the host starts it
// (here, the test does what the host does for a connection), the adapter says its
// tool probes ran, and only when the node has seen each probe token presented is
// the probe passed and the runtime endpoint part of what the node declares. The
// presenting of a token through the Agent socket is the Agent interface's: the
// method it calls is the one called here.
func TestAnInstalledAdapterIsDeclaredOnceItsProbeHasPassedAndTheTokensWerePresented(t *testing.T) {
	noLeaks(t)
	host := newHostDouble()
	r := newRig(t, 10, withAdapters(host), withPackage("/src/relay", relayPackage(t, "1.0.0")))
	e := r.start()

	// install
	if _, err := e.Execute(ctx, installCommand(50, "install", "/src/relay")); err != nil {
		t.Fatal(err)
	}
	list, err := e.AdapterList(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !slicesHave(list.Adapters, engine.InstalledAdapter{ID: "relay", Version: "1.0.0"}) {
		t.Errorf("the list of adapters is %+v, want relay 1.0.0 in it", list.Adapters)
	}
	for _, in := range list.Instances {
		if in.Name == "relay" {
			t.Errorf("an instance of relay exists before it was enabled: %+v", in)
		}
	}

	// enable
	version := r.cfg.Instances().Version
	if _, err := e.Execute(ctx, enableCommand(51, "enable", version, "relay", "relay", "")); err != nil {
		t.Fatal(err)
	}
	if got := host.asks(); !reflect.DeepEqual(got, []string{"run:relay"}) {
		t.Fatalf("the host was asked for %v", got)
	}
	probeOf := func() engine.ProbeView {
		t.Helper()
		list, err := e.AdapterList(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if list.Version != r.cfg.Instances().Version {
			t.Errorf("the list carries version %d, the instances are at %d", list.Version, r.cfg.Instances().Version)
		}
		for _, in := range list.Instances {
			if in.Name == "relay" {
				if !in.Enabled || in.Adapter != "relay" {
					t.Errorf("the instance is %+v", in)
				}
				return in.Probe
			}
		}
		t.Fatalf("no instance relay in %+v", list.Instances)
		return engine.ProbeView{}
	}
	if p := probeOf(); p.State != engine.ProbePending || !p.At.IsZero() || len(p.Reached) != 0 {
		t.Errorf("the probe of an instance that has not started is %+v, want it pending", p)
	}

	// the host takes the adapter on
	adm, info := relayAdmission(t)
	if _, err := e.AdapterAdmitted(ctx, "relay", adm, info); err != nil {
		t.Fatal(err)
	}
	if err := e.AdapterStarted(ctx, "relay", adapterproto.StartResult{}); err != nil {
		t.Fatal(err)
	}
	reqs, err := e.ProbeTokens(ctx, "relay")
	if err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 2 { // the default preset, the command line and MCP
		t.Fatalf("%d probe requests, want 2: %+v", len(reqs), reqs)
	}
	v, err := e.Adapter(ctx, "relay")
	if err != nil || v.RuntimeDeclared {
		t.Fatalf("before the probe: %+v, %v; want the runtime endpoint not declared", v, err)
	}
	probedAt := r.clk.Now()
	if err := e.AdapterProbed(ctx, "relay", probeRan(reqs)); err != nil {
		t.Fatal(err)
	}
	// ran is necessary, not enough: the node has seen no token
	if p := probeOf(); p.State != engine.ProbePending || !p.At.Equal(probedAt) {
		t.Errorf("a probe whose tokens were not presented is %+v, want it pending since %v", p, probedAt)
	}
	if v, _ := e.Adapter(ctx, "relay"); v.RuntimeDeclared {
		t.Error("the runtime endpoint is declared before a token was presented")
	}

	// a token presented for another front end is refused and is not consumed
	byFrontend := map[adapterproto.Frontend]adapterproto.Token{}
	for _, q := range reqs {
		byFrontend[q.Frontend] = q.Token
	}
	_, err = e.PresentProbeToken(ctx, byFrontend[adapterproto.FrontendCLI], adapterproto.FrontendMCP)
	var ref *engine.Refusal
	if !errors.As(err, &ref) || ref.Reason != engine.ReasonInvalidArgument || ref.Detail != engine.DetailProbeFrontendMismatch {
		t.Fatalf("a token of the command line presented as MCP: %v, want the front ends not to match", err)
	}
	target, err := e.PresentProbeToken(ctx, byFrontend[adapterproto.FrontendCLI], adapterproto.FrontendCLI)
	if err != nil {
		t.Fatalf("presenting the token of the command line: %v", err)
	}
	if want := (engine.ProbeTarget{Instance: "relay", Preset: "default", Frontend: adapterproto.FrontendCLI}); target != want {
		t.Errorf("the token was for %+v, want %+v", target, want)
	}
	// one use only
	_, err = e.PresentProbeToken(ctx, byFrontend[adapterproto.FrontendCLI], adapterproto.FrontendCLI)
	wantReason(t, err, engine.ReasonProbeTokenUsed)
	// a token nobody issued
	_, err = e.PresentProbeToken(ctx, adapterproto.Token(strings.Repeat("A", 43)), adapterproto.FrontendCLI)
	wantReason(t, err, engine.ReasonUnauthenticated)

	if p := probeOf(); p.State != engine.ProbePending || len(p.Reached) != 1 {
		t.Errorf("with one of two tokens presented the probe is %+v, want it pending with one reached", p)
	}
	if _, err := e.PresentProbeToken(ctx, byFrontend[adapterproto.FrontendMCP], adapterproto.FrontendMCP); err != nil {
		t.Fatalf("presenting the token of MCP: %v", err)
	}
	p := probeOf()
	if p.State != engine.ProbePassed {
		t.Fatalf("the probe is %+v, want it passed", p)
	}
	want := []engine.ProbeReached{{Preset: "default", Frontend: adapterproto.FrontendCLI}, {Preset: "default", Frontend: adapterproto.FrontendMCP}}
	if !reflect.DeepEqual(p.Reached, want) {
		t.Errorf("reached %+v, want %+v", p.Reached, want)
	}
	if v, _ := e.Adapter(ctx, "relay"); !v.RuntimeDeclared {
		t.Error("the runtime endpoint is not declared after the probe passed")
	}
}

func slicesHave[T comparable](list []T, v T) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// A token is valid for five minutes of the node's clock, and after that it is
// refused as expired; an adapter that says a probe did not run fails it, and a
// node that was never shown the token does not take the adapter's word.
func TestAProbeTokenExpiresAndAFailedToolProbeFailsTheProbe(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10, withAdapters(newHostDouble()), withPackage("/src/relay", relayPackage(t, "1.0.0")))
	e := r.start()
	if _, err := e.Execute(ctx, installCommand(60, "install", "/src/relay")); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Execute(ctx, enableCommand(61, "enable", r.cfg.Instances().Version, "relay", "relay", "")); err != nil {
		t.Fatal(err)
	}
	adm, info := relayAdmission(t)
	if _, err := e.AdapterAdmitted(ctx, "relay", adm, info); err != nil {
		t.Fatal(err)
	}
	if err := e.AdapterStarted(ctx, "relay", adapterproto.StartResult{}); err != nil {
		t.Fatal(err)
	}
	reqs, err := e.ProbeTokens(ctx, "relay")
	if err != nil {
		t.Fatal(err)
	}
	res := probeRan(reqs)
	res.ToolProbes[1].Outcome = adapterproto.ProbeFailed
	if err := e.AdapterProbed(ctx, "relay", res); err != nil {
		t.Fatal(err)
	}
	stateOf := func() engine.ProbeState {
		list, err := e.AdapterList(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, in := range list.Instances {
			if in.Name == "relay" {
				return in.Probe.State
			}
		}
		t.Fatal("no instance relay")
		return 0
	}
	if got := stateOf(); got != engine.ProbeFailed {
		t.Errorf("a probe one of whose tool probes failed is %v, want failed", got)
	}

	r.clk.Advance(engine.ProbeTokenLifetime - time.Second)
	if _, err := e.PresentProbeToken(ctx, reqs[0].Token, reqs[0].Frontend); err != nil {
		t.Errorf("a token a second before it expires: %v", err)
	}
	r.clk.Advance(2 * time.Second)
	_, err = e.PresentProbeToken(ctx, reqs[1].Token, reqs[1].Frontend)
	wantReason(t, err, engine.ReasonProbeTokenExpired)
	if v, _ := e.Adapter(ctx, "relay"); v.RuntimeDeclared {
		t.Error("the runtime endpoint is declared although the probe did not pass")
	}
}

// ---------------------------------------------------------------------------
// Crash points

// crashCoverage fails for a key of the declaration of any of the transactions that
// the run reached and the walk did not crash at under every kind of crash.
func crashCoverage(t *testing.T, r *rig, w *crashWalker, txs ...string) {
	t.Helper()
	names := map[string]bool{}
	for _, tx := range txs {
		names[tx] = true
		if r.commits(tx) < 1 {
			t.Errorf("transaction %s never reached its commit", tx)
		}
	}
	var missing []string
	for _, k := range r.tab.Keys() {
		if !names[k.Tag.Tx] {
			continue
		}
		reached := false
		for _, c := range r.w.Coverage() {
			if c.Key.Key == k.Key && c.Hits > 0 {
				reached = true
			}
		}
		if !reached {
			continue
		}
		for _, model := range crashKindNames {
			if w.taken[k.Key][model] == 0 {
				missing = append(missing, k.Key.String()+" under "+model)
			}
		}
	}
	if len(missing) != 0 {
		t.Errorf("crash points reached and never crashed at: %v", missing)
	}
}

// An upgrade is walked through every crash point of its copy and of its commit,
// in the process crash and the worst power loss. Wherever the node dies the next
// start finds the old version installed or the new one, never neither and never a
// half-copied directory; the request sent again is answered from its result when
// the result is stored and carried out otherwise, and ends with the new version
// installed and nothing left over.
func TestAnInstallationRecoversFromACrashAtEveryPoint(t *testing.T) {
	noLeaks(t)
	var r *rig
	checked, stored, notStored, oldSeen, newSeen := 0, 0, 0, 0, 0
	cmd := func() engine.ManagementCommand { return installCommand(70, "install 2", "/src/relay2") }
	walker := newCrashWalker(func(where string, disk *fsxtest.Mem) {
		checked++
		f := r.fork(disk)
		e, _, err := f.open()
		if err != nil {
			t.Errorf("%s: Open: %v", where, err)
			return
		}
		startForked(t, f, e)
		defer f.stop2(e)
		if work, err := config.AdapterGCWork(f.mem); err != nil || len(work) != 0 {
			t.Errorf("%s: garbage left in the adapter directory after the start: %v (%v)", where, work, err)
		}
		v, ok := installedVersion(f.cfg, "relay")
		switch {
		case !ok:
			t.Errorf("%s: relay is not installed, neither version is", where)
			return
		case v == "1.0.0":
			oldSeen++
		case v == "2.0.0":
			newSeen++
		default:
			t.Errorf("%s: version %s is installed", where, v)
			return
		}
		_, haveResult := f.adminResultOf(f.scan(), cmd())
		if haveResult && v != "2.0.0" {
			t.Errorf("%s: the result is stored and version %s is current", where, v)
		}
		got, err := e.Execute(ctx, cmd())
		if err != nil {
			t.Errorf("%s: the request sent again: %v", where, err)
			return
		}
		if got.Replayed != haveResult {
			t.Errorf("%s: replayed %v, want %v", where, got.Replayed, haveResult)
		}
		if haveResult {
			stored++
		} else {
			notStored++
		}
		if v, _ := installedVersion(f.cfg, "relay"); v != "2.0.0" {
			t.Errorf("%s: version %s is installed after the request was sent again", where, v)
		}
		settle(t, e)
		if work, err := config.AdapterGCWork(f.mem); err != nil || len(work) != 0 {
			t.Errorf("%s: garbage left after the request was sent again: %v (%v)", where, work, err)
		}
		if ok, _ := fsx.Exists(f.mem, config.AdapterVersionDir("relay", "1.0.0")); ok {
			t.Errorf("%s: the old version is still on the disk after the request was sent again", where)
		}
		second, err := e.Execute(ctx, cmd())
		if err != nil || !second.Replayed || string(second.Result) != string(got.Result) {
			t.Errorf("%s: the second resend: %+v, %v", where, second, err)
		}
	}, "adapter-stage", "adapter-install")
	r = newRig(t, 10, withObserver(walker), withPackage("/src/relay1", relayPackage(t, "1.0.0")), withPackage("/src/relay2", relayPackage(t, "2.0.0")))
	e := r.start()
	if _, err := e.Execute(ctx, installCommand(69, "install 1", "/src/relay1")); err != nil {
		t.Fatal(err)
	}
	r.restart() // the walk counts the points of the installation it walks, not of the setup
	e = r.start()
	walker.rig = r
	if _, err := e.Execute(ctx, cmd()); err != nil {
		t.Fatal(err)
	}
	settle(t, e)
	if checked == 0 || oldSeen == 0 || newSeen == 0 || stored == 0 || notStored == 0 {
		t.Fatalf("%d states judged: %d with the old version, %d with the new one, %d with the result, %d without; the walk must reach both sides of the commit", checked, oldSeen, newSeen, stored, notStored)
	}
	crashCoverage(t, r, walker, "adapter-stage", "adapter-install")
}

// Installing the version that is current stores only its result, and that write is
// walked through its crash points too: after any of them the request sent again is
// answered, once, with the same result.
func TestStoringTheResultOfAnInstallationThatFoundItDoneRecoversFromACrash(t *testing.T) {
	noLeaks(t)
	var r *rig
	checked := 0
	cmd := func() engine.ManagementCommand { return installCommand(72, "install again", "/src/relay1") }
	walker := newCrashWalker(func(where string, disk *fsxtest.Mem) {
		checked++
		f := r.fork(disk)
		e, _, err := f.open()
		if err != nil {
			t.Errorf("%s: Open: %v", where, err)
			return
		}
		startForked(t, f, e)
		defer f.stop2(e)
		if v, ok := installedVersion(f.cfg, "relay"); !ok || v != "1.0.0" {
			t.Errorf("%s: relay %q (%v) is installed", where, v, ok)
		}
		got, err := e.Execute(ctx, cmd())
		if err != nil {
			t.Errorf("%s: the request sent again: %v", where, err)
			return
		}
		second, err := e.Execute(ctx, cmd())
		if err != nil || !second.Replayed || string(second.Result) != string(got.Result) {
			t.Errorf("%s: the second resend: %+v, %v", where, second, err)
		}
		settle(t, e)
		if work, err := config.AdapterGCWork(f.mem); err != nil || len(work) != 0 {
			t.Errorf("%s: garbage left: %v (%v)", where, work, err)
		}
	}, "adapter-stage", "adapter-unstage", "adapter-install-current")
	r = newRig(t, 10, withObserver(walker), withPackage("/src/relay1", relayPackage(t, "1.0.0")))
	e := r.start()
	if _, err := e.Execute(ctx, installCommand(71, "install 1", "/src/relay1")); err != nil {
		t.Fatal(err)
	}
	r.restart()
	e = r.start()
	walker.rig = r
	if _, err := e.Execute(ctx, cmd()); err != nil {
		t.Fatal(err)
	}
	settle(t, e)
	if checked == 0 {
		t.Fatal("no crash state was judged")
	}
	crashCoverage(t, r, walker, "adapter-stage", "adapter-unstage", "adapter-install-current")
}

// Disabling an instance is walked through every crash point of the change of the
// configuration and of the closing of its endpoints. The next start finds the
// instance enabled with everything as it was, or disabled with every endpoint
// closed and every queued record rejected, which the recovery that closes the
// endpoints of a disabled instance finishes; the request sent again is answered
// from the result the configuration keeps, or carried out.
func TestDisablingAnInstanceRecoversFromACrashAtEveryPoint(t *testing.T) {
	noLeaks(t)
	var r *rig
	checked, enabledStates, disabledStates := 0, 0, 0
	var version admin.Version
	cmd := func() engine.ManagementCommand { return disableCommand(80, "disable", version, "chat") }
	walker := newCrashWalker(func(where string, disk *fsxtest.Mem) {
		checked++
		f := r.fork(disk)
		e, _, err := f.open()
		if err != nil {
			t.Errorf("%s: Open: %v", where, err)
			return
		}
		sc := f.scan()
		in := f.cfg.Instances().Items["chat"]
		open, rejected, accepted := 0, 0, 0
		for _, ep := range sc.Endpoints {
			if ep.Status != store.EndpointClosed {
				open++
			}
		}
		for _, rec := range sc.In {
			switch rec.Record.State {
			case delivery.StateRejected:
				rejected++
			case delivery.StateAccepted:
				accepted++
			}
		}
		if in.Enabled {
			enabledStates++
			if open != 2 || accepted != 5 || rejected != 0 {
				t.Errorf("%s: the instance is enabled with %d endpoints open, %d records queued and %d rejected; want 2, 5 and 0", where, open, accepted, rejected)
			}
		} else {
			disabledStates++
			if open != 0 || accepted != 0 || rejected != 5 {
				t.Errorf("%s: the instance is disabled with %d endpoints open, %d records queued and %d rejected; want 0, 0 and 5", where, open, accepted, rejected)
			}
		}
		if plan := engine.PlanRecovery(sc, f.clk.Now(), func(name string) bool { return f.cfg.Instances().Items[name].Enabled }); !plan.Empty() {
			t.Errorf("%s: work left after Open: %v", where, plan.Items())
		}
		// the instance may be disabled, which the host would not admit
		e.Start()
		if err := e.RecoverDelivery(ctx); err != nil {
			t.Errorf("%s: RecoverDelivery: %v", where, err)
		}
		e.Serve()
		defer f.stop2(e)
		_, err = e.Execute(ctx, cmd())
		if err != nil {
			// an instance that was disabled and whose startForked admission was refused
			// is still disabled; the resend is what is judged
			t.Errorf("%s: the request sent again: %v", where, err)
			return
		}
		settle(t, e)
		if got := f.cfg.Instances().Items["chat"]; got.Enabled {
			t.Errorf("%s: the instance is enabled after the request was sent again", where)
		}
		if openEndpoints(t, e) != 0 {
			t.Errorf("%s: endpoints are open after the request was sent again", where)
		}
		second, err := e.Execute(ctx, cmd())
		if err != nil || !second.Replayed {
			t.Errorf("%s: the second resend: %+v, %v", where, second, err)
		}
	}, "adapter-disable", "instance-drain", "endpoint-drain")
	r = newRig(t, 10, withParams(engine.Params{ChunkReplaces: 2}), withObserver(walker))
	e := r.start()
	a, b := register(t, e, "a"), register(t, e, "b")
	r.restart()
	r.queue(1, 3, a)
	r.queue(4, 2, b)
	e = r.start()
	version = r.cfg.Instances().Version
	walker.rig = r
	if _, err := e.Execute(ctx, cmd()); err != nil {
		t.Fatal(err)
	}
	settle(t, e)
	if checked == 0 || enabledStates == 0 || disabledStates == 0 {
		t.Fatalf("%d states judged: %d enabled, %d disabled; the walk must reach both sides of the change", checked, enabledStates, disabledStates)
	}
	crashCoverage(t, r, walker, "adapter-disable", "instance-drain", "endpoint-drain")
}

// Enabling an instance and the changes of the presets are single replacements of a
// file of config/, and are walked here through the door: wherever the node dies,
// the file is whole, and the request sent again is carried out or answered from the
// result that file keeps.
func TestEnablingAnInstanceAndChangingPresetsRecoverFromACrashAtEveryPoint(t *testing.T) {
	for _, tc := range []struct {
		name string
		tx   string
		cmd  func(r *rig) engine.ManagementCommand
		done func(r *rig) bool
	}{
		{"enabling an instance", "adapter-enable",
			func(r *rig) engine.ManagementCommand {
				return enableCommand(90, "enable", r.cfg.Instances().Version, "chat", "second", "")
			},
			func(r *rig) bool { return r.cfg.Instances().Items["second"].Enabled }},
		{"adding a preset", "presets-change",
			func(r *rig) engine.ManagementCommand {
				return addPresetCommand(91, "add", r.cfg.Presets().Version, "tight", `{"local_sources":"deny"}`)
			},
			func(r *rig) bool { _, ok := r.cfg.Presets().Items["tight"]; return ok }},
		{"removing a preset", "presets-change",
			func(r *rig) engine.ManagementCommand {
				return removePresetCommand(92, "remove", r.cfg.Presets().Version, "default")
			},
			func(r *rig) bool { _, ok := r.cfg.Presets().Items["default"]; return !ok }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			noLeaks(t)
			var r *rig
			checked, doneStates, notDone := 0, 0, 0
			var cmd engine.ManagementCommand
			walker := newCrashWalker(func(where string, disk *fsxtest.Mem) {
				checked++
				f := r.fork(disk)
				e, _, err := f.open()
				if err != nil {
					t.Errorf("%s: Open: %v", where, err)
					return
				}
				startForked(t, f, e)
				defer f.stop2(e)
				done := tc.done(f)
				replayed := false
				for _, o := range f.cfg.LastOperations() {
					if o.Key() == cmd.Key {
						replayed = true
					}
				}
				if done != replayed {
					t.Errorf("%s: the change is %v and its result is %v in the same file", where, done, replayed)
				}
				if done {
					doneStates++
				} else {
					notDone++
				}
				got, err := e.Execute(ctx, cmd)
				if err != nil {
					t.Errorf("%s: the request sent again: %v", where, err)
					return
				}
				if got.Replayed != replayed || !tc.done(f) {
					t.Errorf("%s: replayed %v, want %v, and the change must be in force", where, got.Replayed, replayed)
				}
			}, tc.tx)
			r = newRig(t, 10, withObserver(walker))
			e := r.start()
			cmd = tc.cmd(r)
			walker.rig = r
			if _, err := e.Execute(ctx, cmd); err != nil {
				t.Fatal(err)
			}
			settle(t, e)
			if checked == 0 || doneStates == 0 || notDone == 0 {
				t.Fatalf("%d states judged: %d with the change, %d without", checked, doneStates, notDone)
			}
			crashCoverage(t, r, walker, tc.tx)
		})
	}
}
