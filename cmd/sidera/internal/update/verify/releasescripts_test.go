package verify_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aianyai/sidera/cmd/sidera/internal/update/verify"
)

// The release scripts decide what they build and sign with scripts/lib/release-format.sh;
// this package decides what a node believes. They are two implementations of one grammar
// (shell has no way to share the Go code), so these tests run the shell functions and
// require, row by row, the answer Verify gives.

// shellAccepts runs function of scripts/lib/release-format.sh on args and reports whether
// it succeeded. Bash being absent skips the test: it is part of the Windows toolchain
// only through Git for Windows, and a machine without it cannot run the release scripts
// either.
func shellAccepts(t *testing.T, function string, args ...string) bool {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not available")
	}
	lib, err := filepath.Abs(filepath.Join("..", "..", "..", "..", "..", "scripts", "lib", "release-format.sh"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bash, append([]string{"-c", `. "$1"; shift; "$@"`, "bash", filepath.ToSlash(lib), function}, args...)...)
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return true
	case errors.As(err, &exit) && exit.ExitCode() == 1:
		return false
	}
	t.Fatalf("%s %q: %v\n%s", function, args, err, out)
	return false
}

// verifierAcceptsVersion asks Verify for a release of version. It reads the version
// before the signature or the checksums, so nothing else needs to be valid.
func verifierAcceptsVersion(version string) bool {
	_, err := verify.Verify(nil, nil, nil, version)
	return !errors.Is(err, verify.ErrInvalidVersion)
}

func TestScriptsAgreeOnReleaseVersion(t *testing.T) {
	versions := []string{
		"0.0.0", "0.12.0", "1.2.3", "10.20.30", "999999999.999999999.999999999",
		"0.12.0-rc.1", "0.12.0-rc.0", "0.0.0-dev", "0.12.0-0", "0.12.0-0a", "0.12.0-a-b", "0.12.0--",
		"0.12.0-0A", "0.12.0-rc.1.2.3", "0.12.0-1-2", "0.12.0-" + strings.Repeat("a", 64-7),
		// Not release versions.
		"", "v0.12.0", "latest", "0.12", "0.12.0.1", "-1.2.3", " 0.12.0", "0.12.0 ", "0.12.0\n", "0.12.0\n0.12.1",
		"01.2.3", "0.01.2", "0.1.02", "1000000000.0.0", "0.1000000000.0", "0.0.1000000000",
		"0.12.0-rc.01", "0.12.0-00", "0.12.0-rc..1", "0.12.0-.", "0.12.0-", "0.12.0-rc.", "0.12.0-.rc",
		"0.12.0+build", "0.12.0+", "0.12.0-rc.1+build", "0.12.0-rc_1", "0.12.0-é", "0.12.٠",
		"0.12.0-" + strings.Repeat("a", 64-6),
	}
	for _, v := range versions {
		t.Run(v, func(t *testing.T) {
			if got, want := shellAccepts(t, "is_release_version", v), verifierAcceptsVersion(v); got != want {
				t.Errorf("is_release_version(%q) = %v, Verify accepts the version: %v", v, got, want)
			}
		})
	}
}

// The floor is read from scripts/update-from by the signer and from the trusted comment
// by the verifier. Each refused floor is a throwaway fixture that differs from the good
// one in the floor alone.
func TestScriptsAgreeOnFloor(t *testing.T) {
	floors := []struct{ fixture, floor string }{
		{"good", "0.8.0"},
		{"comment-floor-prerelease", "0.8.0-rc.1"},
		{"comment-floor-leading-zero", "0.08.0"},
		{"comment-floor-two-parts", "0.8"},
		{"comment-floor-build", "0.8.0+1"},
		{"comment-v-floor", "v0.8.0"},
	}
	for _, tt := range floors {
		t.Run(tt.fixture, func(t *testing.T) {
			checksums, minisig := fixture(t, tt.fixture)
			_, err := verify.Verify(throwawayKeys(t), checksums, minisig, "0.12.0")
			if got, want := shellAccepts(t, "is_release_floor", tt.floor), err == nil; got != want {
				t.Errorf("is_release_floor(%q) = %v, Verify accepts the release: %v (%v)", tt.floor, got, want, err)
			}
		})
	}
}

// Every checksums.txt of the throwaway fixtures that differs from the good one in its
// lines. The comment fixtures have good lines, so they say nothing about this check.
func TestScriptsAgreeOnChecksums(t *testing.T) {
	names, err := filepath.Glob(filepath.Join("testdata", "throwaway", "*.txt"))
	if err != nil || len(names) == 0 {
		t.Fatalf("no fixtures: %v", err)
	}
	var tried int
	for _, path := range names {
		name := strings.TrimSuffix(filepath.Base(path), ".txt")
		if strings.HasPrefix(name, "comment-") {
			continue
		}
		version := "0.12.0"
		if name == "prerelease" {
			version = "0.12.0-rc.1"
		}
		t.Run(name, func(t *testing.T) {
			tried++
			checksums, minisig := fixture(t, name)
			_, err := verify.Verify(throwawayKeys(t), checksums, minisig, version)
			file := filepath.Join(t.TempDir(), "checksums.txt")
			if err := os.WriteFile(file, checksums, 0o600); err != nil {
				t.Fatal(err)
			}
			if got, want := shellAccepts(t, "check_checksums", filepath.ToSlash(file), version), err == nil; got != want {
				t.Errorf("check_checksums = %v, Verify accepts the release: %v (%v)", got, want, err)
			}
		})
	}
	if tried < 30 {
		t.Errorf("only %d checksums fixtures were tried", tried)
	}
}
