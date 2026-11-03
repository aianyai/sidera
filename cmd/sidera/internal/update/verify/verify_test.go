package verify_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/aianyai/sidera/cmd/sidera/internal/update/verify"
	"github.com/aianyai/sidera/internal/minisign"
)

func readFile(t *testing.T, elem ...string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(append([]string{"testdata"}, elem...)...))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func embeddedKeys(t *testing.T) []minisign.PublicKey {
	t.Helper()
	keys, err := verify.EmbeddedKeys()
	if err != nil {
		t.Fatal(err)
	}
	return keys
}

func throwawayKeys(t *testing.T) []minisign.PublicKey {
	t.Helper()
	key, err := minisign.ParsePublicKey(readFile(t, "throwaway", "throwaway.pub"))
	if err != nil {
		t.Fatal(err)
	}
	return []minisign.PublicKey{key}
}

// fixture is a throwaway-signed checksums.txt and its signature; testdata/throwaway/generate.sh makes them.
func fixture(t *testing.T, name string) (checksums, minisig []byte) {
	t.Helper()
	return readFile(t, "throwaway", name+".txt"), readFile(t, "throwaway", name+".txt.minisig")
}

// fakeSum is the hash generate.sh gives an archive name.
func fakeSum(name string) [32]byte { return sha256.Sum256([]byte(name)) }

// The release 0.11.0 files are what the pipeline really produced and signed with the
// release key. This pins the pipeline's output, the embedded key and this verifier together.
func TestRealRelease(t *testing.T) {
	checksums := readFile(t, "release-0.11.0", "checksums.txt")
	minisig := readFile(t, "release-0.11.0", "checksums.txt.minisig")

	rel, err := verify.Verify(embeddedKeys(t), checksums, minisig, "0.11.0")
	if err != nil {
		t.Fatal(err)
	}
	if rel.Version() != "0.11.0" || rel.Floor() != "0.8.0" {
		t.Fatalf("Version %q Floor %q, want 0.11.0 and 0.8.0", rel.Version(), rel.Floor())
	}

	// Each platform gets the line of the file that names it.
	for _, p := range []struct{ goos, goarch, name string }{
		{"darwin", "amd64", "sidera_0.11.0_darwin_amd64.tar.gz"},
		{"darwin", "arm64", "sidera_0.11.0_darwin_arm64.tar.gz"},
		{"linux", "amd64", "sidera_0.11.0_linux_amd64.tar.gz"},
		{"linux", "arm64", "sidera_0.11.0_linux_arm64.tar.gz"},
		{"windows", "amd64", "sidera_0.11.0_windows_amd64.zip"},
		{"windows", "arm64", "sidera_0.11.0_windows_arm64.zip"},
	} {
		name, sum, err := rel.Archive(p.goos, p.goarch)
		if err != nil || name != p.name {
			t.Fatalf("Archive(%s, %s) = %q, %v; want %q", p.goos, p.goarch, name, err, p.name)
		}
		var line string
		for _, l := range strings.Split(string(checksums), "\n") {
			if strings.HasSuffix(l, "  "+p.name) {
				line = l
			}
		}
		if hex.EncodeToString(sum[:]) != strings.TrimSuffix(line, "  "+p.name) {
			t.Errorf("Archive(%s, %s) sum %x, file says %q", p.goos, p.goarch, sum, line)
		}
	}

	if err := rel.Admits("0.8.0"); err != nil {
		t.Errorf("Admits(0.8.0) = %v, want nil", err)
	}
	if err := rel.Admits("0.7.9"); !errors.Is(err, verify.ErrNotAllowed) {
		t.Errorf("Admits(0.7.9) = %v, want ErrNotAllowed", err)
	}
}

// Tampering with the signature itself is internal/minisign's tests; one row here
// shows that a refusal from it reaches the caller as ErrNotTrusted, and the others are
// this package's own: the version binding.
func TestRealReleaseTampered(t *testing.T) {
	checksums := readFile(t, "release-0.11.0", "checksums.txt")
	minisig := readFile(t, "release-0.11.0", "checksums.txt.minisig")
	tampered := bytes.Clone(checksums)
	tampered[10] ^= 1

	tests := []struct {
		name      string
		checksums []byte
		want      string
		msg       string
	}{
		{"a byte of the checksums: a signature failure is not trusted", tampered, "0.11.0", "signature"},
		{"another version asked for: a signature of an older release replayed", checksums, "0.12.0", "0.11.0"},
		{"another version asked for, older", checksums, "0.10.0", "0.11.0"},
		{"a pre-release of the version asked for", checksums, "0.11.0-rc.1", "0.11.0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := verify.Verify(embeddedKeys(t), tt.checksums, minisig, tt.want)
			if !errors.Is(err, verify.ErrNotTrusted) {
				t.Fatalf("err = %v, want ErrNotTrusted", err)
			}
			if !strings.Contains(err.Error(), tt.msg) {
				t.Errorf("err = %q, want it to mention %q", err, tt.msg)
			}
		})
	}
}

// A version asked for that is not a plain release version is the caller's mistake,
// reported before any signature is looked at.
func TestVerifyRejectsAskedVersion(t *testing.T) {
	checksums := readFile(t, "release-0.11.0", "checksums.txt")
	minisig := readFile(t, "release-0.11.0", "checksums.txt.minisig")
	for _, want := range []string{"", "v0.11.0", "0.11.0+build", "0.11.0+", "0.11", "0.11.0 ", "00.11.0", "latest"} {
		t.Run(want, func(t *testing.T) {
			_, err := verify.Verify(embeddedKeys(t), checksums, minisig, want)
			if !errors.Is(err, verify.ErrInvalidVersion) {
				t.Fatalf("err = %v, want ErrInvalidVersion", err)
			}
		})
	}
}

func TestVerifyThrowaway(t *testing.T) {
	good := func(t *testing.T) verify.Release {
		t.Helper()
		checksums, minisig := fixture(t, "good")
		rel, err := verify.Verify(throwawayKeys(t), checksums, minisig, "0.12.0")
		if err != nil {
			t.Fatal(err)
		}
		return rel
	}

	t.Run("good", func(t *testing.T) {
		rel := good(t)
		if rel.Version() != "0.12.0" || rel.Floor() != "0.8.0" {
			t.Fatalf("Version %q Floor %q", rel.Version(), rel.Floor())
		}
		for _, p := range [][2]string{{"darwin", "amd64"}, {"darwin", "arm64"}, {"linux", "amd64"}, {"linux", "arm64"}, {"windows", "amd64"}, {"windows", "arm64"}} {
			name, sum, err := rel.Archive(p[0], p[1])
			ext := ".tar.gz"
			if p[0] == "windows" {
				ext = ".zip"
			}
			wantName := "sidera_0.12.0_" + p[0] + "_" + p[1] + ext
			if err != nil || name != wantName || sum != fakeSum(wantName) {
				t.Errorf("Archive(%s, %s) = %q, %x, %v; want %q with its own sum", p[0], p[1], name, sum, err, wantName)
			}
		}
	})

	t.Run("pre-release", func(t *testing.T) {
		checksums, minisig := fixture(t, "prerelease")
		rel, err := verify.Verify(throwawayKeys(t), checksums, minisig, "0.12.0-rc.1")
		if err != nil {
			t.Fatal(err)
		}
		if name, _, err := rel.Archive("linux", "amd64"); err != nil || name != "sidera_0.12.0-rc.1_linux_amd64.tar.gz" {
			t.Errorf("Archive = %q, %v", name, err)
		}
		// The signature of the pre-release does not stand for the release of the same numbers.
		if _, err := verify.Verify(throwawayKeys(t), checksums, minisig, "0.12.0"); !errors.Is(err, verify.ErrNotTrusted) {
			t.Errorf("asked for 0.12.0: err = %v, want ErrNotTrusted", err)
		}
	})

	t.Run("a floor above the release admits no installation below it", func(t *testing.T) {
		checksums, minisig := fixture(t, "comment-floor-above")
		rel, err := verify.Verify(throwawayKeys(t), checksums, minisig, "0.12.0")
		if err != nil {
			t.Fatal(err)
		}
		if err := rel.Admits("0.12.0"); !errors.Is(err, verify.ErrNotAllowed) {
			t.Errorf("Admits(0.12.0) = %v, want ErrNotAllowed", err)
		}
	})

	t.Run("a platform the release does not list", func(t *testing.T) {
		checksums, minisig := fixture(t, "sums-missing-platform")
		rel, err := verify.Verify(throwawayKeys(t), checksums, minisig, "0.12.0")
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := rel.Archive("linux", "arm64"); !errors.Is(err, verify.ErrNoArchive) {
			t.Errorf("Archive(linux, arm64) = %v, want ErrNoArchive", err)
		}
		if _, _, err := rel.Archive("linux", "amd64"); err != nil {
			t.Errorf("Archive(linux, amd64) = %v, want nil", err)
		}
	})

	t.Run("a platform that is none of the release's", func(t *testing.T) {
		rel := good(t)
		for _, p := range [][2]string{{"freebsd", "amd64"}, {"linux", "riscv64"}, {"", ""}, {"linux", ""}, {"Linux", "amd64"}, {"linux", "amd64/../x"}} {
			if _, _, err := rel.Archive(p[0], p[1]); !errors.Is(err, verify.ErrNoArchive) {
				t.Errorf("Archive(%q, %q) = %v, want ErrNoArchive", p[0], p[1], err)
			}
		}
	})

	t.Run("the zero Release has no archive and admits nothing", func(t *testing.T) {
		if _, _, err := (verify.Release{}).Archive("linux", "amd64"); !errors.Is(err, verify.ErrNoArchive) {
			t.Errorf("Archive: err = %v, want ErrNoArchive", err)
		}
		if err := (verify.Release{}).Admits("0.9.0"); !errors.Is(err, verify.ErrNotTrusted) {
			t.Errorf("Admits: err = %v, want ErrNotTrusted", err)
		}
	})

	// Every case is signed correctly by the throwaway key; what is wrong is what
	// was signed, and the refusal must say which part.
	refused := []struct{ name, msg string }{
		// The trusted comment must be exactly `sidera <version> update-from <floor>`.
		{"comment-double-space", "trusted comment"},
		{"comment-leading-space", "trusted comment"},
		{"comment-trailing-space", "trusted comment"},
		{"comment-tab-separated", "trusted comment"},
		{"comment-missing-floor", "trusted comment"},
		{"comment-missing-keyword-and-floor", "trusted comment"},
		{"comment-extra-field", "trusted comment"},
		{"comment-v-version", "trusted comment"},
		{"comment-v-floor", "trusted comment"},
		{"comment-floor-prerelease", "trusted comment"},
		{"comment-floor-leading-zero", "trusted comment"},
		{"comment-floor-two-parts", "trusted comment"},
		{"comment-floor-build", "trusted comment"},
		{"comment-version-build", "trusted comment"},
		{"comment-wrong-name", "trusted comment"},
		{"comment-capitalised", "trusted comment"},
		{"comment-wrong-keyword", "trusted comment"},
		// A valid signature of another version.
		{"comment-other-version", "0.12.1"},
		// The lines of checksums.txt: `<64 lowercase hex>  <archive name of this version>`, once each.
		{"sums-duplicate", "twice"},
		{"sums-duplicate-other-hash", "twice"},
		{"sums-one-space", "line 3"},
		{"sums-three-spaces", "line 3"},
		{"sums-tab", "line 3"},
		{"sums-binary-mode", "line 3"},
		{"sums-uppercase-hex", "line 3"},
		{"sums-short-hash", "line 3"},
		{"sums-long-hash", "line 3"},
		{"sums-non-hex", "line 3"},
		{"sums-foreign-version", "line 3"},
		{"sums-v-version", "line 3"},
		{"sums-foreign-name", "line 3"},
		{"sums-dot-path", "line 3"},
		{"sums-parent-path", "line 3"},
		{"sums-unknown-os", "line 3"},
		{"sums-unknown-arch", "line 3"},
		{"sums-wrong-extension", "line 3"},
		{"sums-windows-tar", "line 3"},
		{"sums-name-suffix", "line 3"},
		{"sums-extra-column", "line 3"},
		{"sums-trailing-space", "line 3"},
		{"sums-leading-space", "line 3"},
		{"sums-comment-line", "line 1"},
		{"sums-blank-line", "line 3"},
		{"sums-trailing-blank-line", "line 7"},
		{"sums-crlf", "carriage return"},
		{"sums-no-final-newline", "newline"},
		{"sums-empty", "empty"},
	}
	for _, tt := range refused {
		t.Run(tt.name, func(t *testing.T) {
			checksums, minisig := fixture(t, tt.name)
			_, err := verify.Verify(throwawayKeys(t), checksums, minisig, "0.12.0")
			if !errors.Is(err, verify.ErrNotTrusted) {
				t.Fatalf("err = %v, want ErrNotTrusted", err)
			}
			if !strings.Contains(err.Error(), tt.msg) {
				t.Errorf("err = %q, want it to mention %q", err, tt.msg)
			}
		})
	}

	// The embedded key is not the throwaway key.
	t.Run("signed by a key that is not trusted", func(t *testing.T) {
		checksums, minisig := fixture(t, "good")
		if _, err := verify.Verify(embeddedKeys(t), checksums, minisig, "0.12.0"); !errors.Is(err, verify.ErrNotTrusted) {
			t.Fatalf("err = %v, want ErrNotTrusted", err)
		}
	})
}

func TestVerifyDigest(t *testing.T) {
	content := bytes.Repeat([]byte("sidera archive "), 100000)
	sum := sha256.Sum256(content)

	if err := verify.VerifyDigest(bytes.NewReader(content), sum); err != nil {
		t.Fatalf("matching content: %v", err)
	}

	flipped := bytes.Clone(content)
	flipped[len(flipped)/2] ^= 1
	tests := []struct {
		name    string
		content []byte
		want    [32]byte
	}{
		{"a byte changed", flipped, sum},
		{"truncated", content[:len(content)-1], sum},
		{"extended", append(bytes.Clone(content), 0), sum},
		{"empty", nil, sum},
		{"another digest", content, sha256.Sum256([]byte("other"))},
		{"zero digest", content, [32]byte{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := verify.VerifyDigest(bytes.NewReader(tt.content), tt.want); !errors.Is(err, verify.ErrNotTrusted) {
				t.Fatalf("err = %v, want ErrNotTrusted", err)
			}
		})
	}

	// The content is read in pieces, never held whole.
	if err := verify.VerifyDigest(iotest.OneByteReader(bytes.NewReader(content[:1000])), sha256.Sum256(content[:1000])); err != nil {
		t.Errorf("one byte at a time: %v", err)
	}

	// A read that fails says so; it is not a digest mismatch.
	boom := errors.New("disk gone")
	err := verify.VerifyDigest(io.MultiReader(bytes.NewReader(content[:10]), iotest.ErrReader(boom)), sum)
	if !errors.Is(err, boom) || errors.Is(err, verify.ErrNotTrusted) {
		t.Errorf("err = %v, want the read error and not ErrNotTrusted", err)
	}
}
