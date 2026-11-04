package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/blake2b"

	"github.com/aianyai/sidera/cmd/sidera/internal/update/fetch"
	"github.com/aianyai/sidera/internal/minisign"
	"github.com/aianyai/sidera/internal/release"
)

const (
	testGOOS   = "linux"
	testGOARCH = "amd64"
	repoPath   = "/owner/repo"
)

// signer is a throwaway minisign key; real releases are signed with a key that is
// never in a test.
type signer struct {
	id   [8]byte
	priv ed25519.PrivateKey
	pub  ed25519.PublicKey
}

func newSigner(t *testing.T) *signer {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s := &signer{priv: priv, pub: pub}
	if _, err := rand.Read(s.id[:]); err != nil {
		t.Fatal(err)
	}
	return s
}

func (s *signer) keys(t *testing.T) []minisign.PublicKey {
	t.Helper()
	line := base64.StdEncoding.EncodeToString(append(append([]byte("Ed"), s.id[:]...), s.pub...))
	key, err := minisign.ParsePublicKey([]byte("untrusted comment: test key\n" + line + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	return []minisign.PublicKey{key}
}

// sign makes the signature file minisign writes for message with a trusted comment.
func (s *signer) sign(message []byte, comment string) []byte {
	digest := blake2b.Sum512(message)
	sig := ed25519.Sign(s.priv, digest[:])
	global := ed25519.Sign(s.priv, append(append([]byte{}, sig...), comment...))
	b64 := base64.StdEncoding.EncodeToString
	return []byte("untrusted comment: signature\n" +
		b64(append(append([]byte("ED"), s.id[:]...), sig...)) + "\n" +
		"trusted comment: " + comment + "\n" +
		b64(global) + "\n")
}

// published is one release as the server has it.
type published struct {
	checksums, signature, archive []byte
}

// releaseOptions bend a release away from the honest one.
type releaseOptions struct {
	floor      string // default 0.8.0
	signer     *signer
	signedFor  string            // the version in the trusted comment; default the release's
	listedSum  []byte            // the archive bytes the checksum is made of; default the archive
	files      map[string]string // replaces the default contents of the archive
	noCodexPkg bool
}

// archiveBytes builds a .tar.gz with the layout of a release: the two programs and
// the packages of the console and claude adapters.
func archiveBytes(t *testing.T, version string, files map[string]string) []byte {
	t.Helper()
	if files == nil {
		files = map[string]string{
			"sidera":                               "new sidera " + version,
			"sidera-adapter-console":               "new console " + version,
			"adapters/claude/sidera-adapter.json":  `{"version":"` + version + `"}`,
			"adapters/claude/bin/claude-adapter":   "claude " + version,
			"adapters/console/sidera-adapter.json": `{"version":"` + version + `"}`,
			"adapters/console/bin/console-adapter": "console " + version,
		}
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func publish(t *testing.T, sg *signer, version string, opt releaseOptions) published {
	t.Helper()
	if opt.floor == "" {
		opt.floor = "0.8.0"
	}
	if opt.signer == nil {
		opt.signer = sg
	}
	if opt.signedFor == "" {
		opt.signedFor = version
	}
	archive := archiveBytes(t, version, opt.files)
	listed := archive
	if opt.listedSum != nil {
		listed = opt.listedSum
	}
	sum := sha256.Sum256(listed)
	checksums := []byte(hex.EncodeToString(sum[:]) + "  sidera_" + version + "_" + testGOOS + "_" + testGOARCH + ".tar.gz\n")
	return published{
		checksums: checksums,
		signature: opt.signer.sign(checksums, "sidera "+opt.signedFor+" update-from "+opt.floor),
		archive:   archive,
	}
}

// repo serves releases over TLS like the GitHub repository does and counts what is
// asked of it.
type repo struct {
	mu       sync.Mutex
	releases map[string]published
	latest   string
	hits     map[string]int
	client   *fetch.Client
}

func newRepo(t *testing.T) *repo {
	t.Helper()
	r := &repo{releases: map[string]published{}, hits: map[string]int{}}
	srv := httptest.NewTLSServer(http.HandlerFunc(r.serve))
	t.Cleanup(srv.Close)
	base, err := url.Parse(srv.URL + repoPath)
	if err != nil {
		t.Fatal(err)
	}
	r.client = fetch.NewAt(base, srv.Client().Transport, "sidera/test")
	return r
}

func (r *repo) add(version string, p published) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.releases[version] = p
	r.latest = version
}

func (r *repo) hit(kind string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.hits[kind]
}

func (r *repo) serve(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if req.URL.Path == repoPath+"/releases/latest" {
		r.hits["latest"]++
		http.Redirect(w, req, "https://"+req.Host+repoPath+"/releases/tag/v"+r.latest, http.StatusFound)
		return
	}
	rest, ok := strings.CutPrefix(req.URL.Path, repoPath+"/releases/download/v")
	version, asset, _ := strings.Cut(rest, "/")
	p, found := r.releases[version]
	if !ok || !found {
		http.NotFound(w, req)
		return
	}
	switch {
	case asset == "checksums.txt":
		r.hits["checksums"]++
		w.Write(p.checksums)
	case asset == "checksums.txt.minisig":
		r.hits["signature"]++
		w.Write(p.signature)
	case strings.HasSuffix(asset, ".tar.gz"):
		r.hits["archive"]++
		w.Write(p.archive)
	default:
		http.NotFound(w, req)
	}
}

// fakeNode is a daemon that restarts to whatever the program it is given reports.
type fakeNode struct {
	running    bool
	version    string
	adapters   []Adapter
	restartTo  string // the version it runs after a restart; default the one expected
	restartErr error
	installErr map[string]error

	restarts []string // expected versions asked
	installs []string // directories installed
	t        *testing.T
}

func (n *fakeNode) Status(context.Context) (NodeStatus, error) {
	if !n.running {
		return NodeStatus{}, ErrNotRunning
	}
	return NodeStatus{Version: n.version}, nil
}

func (n *fakeNode) Restart(_ context.Context, expect string) error {
	n.restarts = append(n.restarts, expect)
	if n.restartErr != nil {
		return n.restartErr
	}
	n.version = expect
	if n.restartTo != "" {
		n.version = n.restartTo
	}
	return nil
}

func (n *fakeNode) Adapters(context.Context) ([]Adapter, error) {
	return append([]Adapter(nil), n.adapters...), nil
}

func (n *fakeNode) Install(_ context.Context, dir string) (Installed, error) {
	id := filepath.Base(dir)
	n.installs = append(n.installs, dir)
	if !filepath.IsAbs(dir) {
		n.t.Errorf("install of %s: not an absolute path", dir)
	}
	if _, err := os.Stat(filepath.Join(dir, "sidera-adapter.json")); err != nil {
		n.t.Errorf("install of %s: %v", dir, err)
	}
	if err := n.installErr[id]; err != nil {
		return Installed{}, err
	}
	version := ""
	for i, a := range n.adapters {
		if a.ID == id {
			version = a.Version
			n.adapters[i].Version = n.version
		}
	}
	return Installed{ID: id, Version: n.version, Replaced: version != ""}, nil
}

// rig is a program directory, a cache and a repository wired into a Config.
type rig struct {
	t       *testing.T
	repo    *repo
	signer  *signer
	node    *fakeNode
	dir     string // the directory of the programs
	cache   string
	cfg     Config
	plans   []Plan
	answer  bool
	smoked  []string
	smokeAs func(target string) release.Info
}

const oldVersion = "0.11.0"

func newRig(t *testing.T) *rig {
	t.Helper()
	sg := newSigner(t)
	r := &rig{t: t, repo: newRepo(t), signer: sg, answer: true}
	r.dir = t.TempDir()
	r.cache = filepath.Join(t.TempDir(), "sidera-update")
	r.write("sidera", "old sidera")
	r.write("sidera-adapter-console", "old console")
	r.node = &fakeNode{
		t: t, running: true, version: oldVersion,
		adapters: []Adapter{{ID: "claude", Version: oldVersion}, {ID: "console", Version: oldVersion}, {ID: "codex", Version: oldVersion}},
	}
	r.cfg = Config{
		Installed: release.Info{Version: oldVersion, Distribution: release.DistributionArchive},
		Program:   filepath.Join(r.dir, "sidera"),
		GOOS:      testGOOS, GOARCH: testGOARCH,
		Fetch: r.repo.client, Keys: sg.keys(t),
		Node:      r.node,
		CacheRoot: r.cache, Random: rand.Reader,
		Confirm: func(p Plan) (bool, error) { r.plans = append(r.plans, p); return r.answer, nil },
	}
	r.cfg.Smoke = func(_ context.Context, program string) (release.Info, error) {
		r.smoked = append(r.smoked, program)
		if _, err := os.Stat(program); err != nil {
			return release.Info{}, err
		}
		if r.smokeAs != nil {
			return r.smokeAs(r.latestTarget()), nil
		}
		return release.Info{Version: r.latestTarget(), Distribution: release.DistributionArchive}, nil
	}
	return r
}

// latestTarget is the version the smoke run reports by default: the newest release.
func (r *rig) latestTarget() string { return r.repo.latest }

func (r *rig) write(name, content string) {
	r.t.Helper()
	if err := os.WriteFile(filepath.Join(r.dir, name), []byte(content), 0o755); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) read(name string) string {
	r.t.Helper()
	b, err := os.ReadFile(filepath.Join(r.dir, name))
	if err != nil {
		return "<" + err.Error() + ">"
	}
	return string(b)
}

func (r *rig) exists(name string) bool {
	_, err := os.Lstat(filepath.Join(r.dir, name))
	return err == nil
}

// release publishes an honest release and makes it the latest.
func (r *rig) release(version string) {
	r.repo.add(version, publish(r.t, r.signer, version, releaseOptions{}))
}

// unchanged fails the test when anything next to the programs has changed.
func (r *rig) unchanged() {
	r.t.Helper()
	if got := r.read("sidera"); got != "old sidera" {
		r.t.Errorf("sidera = %q, want the old program", got)
	}
	if got := r.read("sidera-adapter-console"); got != "old console" {
		r.t.Errorf("console = %q, want the old program", got)
	}
	entries, _ := os.ReadDir(r.dir)
	if len(entries) != 2 {
		r.t.Errorf("the program directory holds %d entries, want 2", len(entries))
	}
}
