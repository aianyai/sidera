// Package verify decides whether a release may be believed and whether this
// installation may take it: the chain from the public key built into the program,
// through the signature of checksums.txt and the trusted comment it covers, to the
// SHA-256 of one archive. Nothing here touches the network or the disk; callers
// hand over bytes.
package verify

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/aianyai/sidera/internal/minisign"
	"github.com/aianyai/sidera/internal/release"
	"github.com/aianyai/sidera/sdk/go/semver"
)

var (
	// ErrNotTrusted means the bytes cannot be believed to come from the release
	// they claim to be: the signature does not verify, it is for another version,
	// the signed text is not in the exact form the pipeline writes, or an archive
	// does not match its digest. Nothing from such a release may be installed.
	ErrNotTrusted = errors.New("release is not trustworthy")
	// ErrNotAllowed means the release is genuine but this installation may not take
	// it: it is older than the oldest version the release can be installed over.
	ErrNotAllowed = errors.New("update is not allowed")
	// ErrNoArchive means the release lists no archive for the platform.
	ErrNoArchive = errors.New("release has no archive for this platform")
	// ErrInvalidVersion means a version the caller gave is not a plain release
	// version: MAJOR.MINOR.PATCH with an optional pre-release, no "v", no build
	// metadata. The version asked for may come from the network (fetch.Latest), which
	// checks the tag with the same grammar before it gets here; a caller therefore
	// classifies this error by where the version came from: a mistake of the caller
	// when it was typed or compiled in, a bad answer of the server when it was fetched.
	ErrInvalidVersion = errors.New("not a release version")
)

// Release is what a verified checksums.txt says about one release. Only Verify
// makes a Release that means anything; its fields are not exported so that no
// caller can lower the floor or add an archive. The zero value was never verified
// and admits no installation.
type Release struct {
	version  string
	floor    string
	archives map[string][sha256.Size]byte // by archive name
}

// Version is the version the signature was made for, equal to the one asked for.
func (r Release) Version() string { return r.version }

// Floor is the oldest version, a plain MAJOR.MINOR.PATCH, that may update to this release.
func (r Release) Floor() string { return r.floor }

// EmbeddedKeys returns the keys releases are trusted by: the one built into the
// program. It is the only source of keys outside tests.
func EmbeddedKeys() ([]minisign.PublicKey, error) {
	key, err := minisign.ParsePublicKey(release.SigningKey())
	if err != nil {
		return nil, fmt.Errorf("the built-in signing key: %w", err)
	}
	return []minisign.PublicKey{key}, nil
}

// Verify checks that checksums and its signature minisig were made by one of keys
// for exactly the release want, and reads the archives checksums lists. Production
// callers pass EmbeddedKeys() and nothing else; keys is a parameter only so that a
// test can sign with a throwaway key.
//
// The trusted comment the signature covers must be `sidera <version> update-from
// <floor>` (the form scripts/release-sign.sh writes), and its version must be want:
// a signature of another release is refused even though it is valid. Every line of
// checksums.txt must be `<64 lowercase hex digits>  <archive name>` for an archive
// of want, as scripts/release.sh writes them, and none may repeat. Anything else is
// refused whole rather than skipped, so no line is read by a rule other than the
// signer's. A failure is ErrNotTrusted, except that want itself must be a plain
// release version (ErrInvalidVersion).
func Verify(keys []minisign.PublicKey, checksums, minisig []byte, want string) (Release, error) {
	if _, err := parseVersion(want); err != nil {
		return Release{}, fmt.Errorf("requested version: %w", err)
	}
	sig, err := minisign.ParseSignature(minisig)
	if err != nil {
		return Release{}, fmt.Errorf("%w: signature file: %w", ErrNotTrusted, err)
	}
	comment, err := minisign.Verify(keys, checksums, sig)
	if err != nil {
		return Release{}, fmt.Errorf("%w: signature: %w", ErrNotTrusted, err)
	}
	version, floor, err := parseTrustedComment(comment)
	if err != nil {
		return Release{}, fmt.Errorf("%w: trusted comment %q: %w", ErrNotTrusted, comment, err)
	}
	if version != want {
		return Release{}, fmt.Errorf("%w: the signature is for version %s, not the requested %s", ErrNotTrusted, version, want)
	}
	archives, err := parseChecksums(checksums, version)
	if err != nil {
		return Release{}, fmt.Errorf("%w: checksums.txt: %w", ErrNotTrusted, err)
	}
	return Release{version: version, floor: floor, archives: archives}, nil
}

// Admits says whether an installation at version installed may take this release:
// installed must not be below Floor by SemVer precedence, so a pre-release of the
// floor is below it. It does not compare installed with Version; whether the
// release is newer than what is installed is for the caller to decide. A failure is
// ErrNotAllowed, ErrInvalidVersion for text that is not a release version, or
// ErrNotTrusted for a Release that Verify did not make.
func (r Release) Admits(installed string) error {
	if r.version == "" {
		return fmt.Errorf("%w: the release was never verified", ErrNotTrusted)
	}
	have, err := parseVersion(installed)
	if err != nil {
		return fmt.Errorf("installed version: %w", err)
	}
	floor, err := parseVersion(r.floor)
	if err != nil {
		return fmt.Errorf("floor of the release: %w", err)
	}
	if semver.Compare(have, floor) < 0 {
		return fmt.Errorf("%w: release %s can be installed over %s or later, this is %s", ErrNotAllowed, r.version, r.floor, installed)
	}
	return nil
}

// Archive returns the name of the archive of this release for the platform and the
// SHA-256 checksums.txt gives for it, or ErrNoArchive.
func (r Release) Archive(goos, goarch string) (name string, sum [sha256.Size]byte, err error) {
	name, ok := archiveName(r.version, goos, goarch)
	if ok {
		sum, ok = r.archives[name]
	}
	if !ok {
		return "", sum, fmt.Errorf("%w: %s/%s in release %s", ErrNoArchive, goos, goarch, r.version)
	}
	return name, sum, nil
}

// VerifyDigest reads content to its end and checks that its SHA-256 is want. A
// different digest is ErrNotTrusted; an error from reading is returned as it is.
func VerifyDigest(content io.Reader, want [sha256.Size]byte) error {
	h := sha256.New()
	if _, err := io.Copy(h, content); err != nil {
		return err
	}
	var got [sha256.Size]byte
	h.Sum(got[:0])
	if got != want {
		return fmt.Errorf("%w: archive has SHA-256 %x, checksums.txt says %x", ErrNotTrusted, got, want)
	}
	return nil
}

// parseVersion accepts a release version in canonical form. Build metadata is
// refused here because semver.Parse would drop it silently, and a version with a
// suffix that is ignored is not the version that was signed.
func parseVersion(text string) (semver.Version, error) {
	if strings.Contains(text, "+") {
		return semver.Version{}, fmt.Errorf("%w: %q has build metadata", ErrInvalidVersion, text)
	}
	v, err := semver.Parse(text)
	if err != nil {
		return semver.Version{}, fmt.Errorf("%w: %q", ErrInvalidVersion, text)
	}
	return v, nil
}

// parseTrustedComment reads `sidera <version> update-from <floor>`: four fields
// separated by single spaces, the version without a "v" and the floor a plain
// MAJOR.MINOR.PATCH.
func parseTrustedComment(comment string) (version, floor string, err error) {
	fields := strings.Split(comment, " ")
	if len(fields) != 4 || fields[0] != "sidera" || fields[2] != "update-from" {
		return "", "", errors.New("want `sidera <version> update-from <floor>` with single spaces")
	}
	version, floor = fields[1], fields[3]
	if _, err := parseVersion(version); err != nil {
		return "", "", err
	}
	f, err := parseVersion(floor)
	if err != nil {
		return "", "", err
	}
	if len(f.Pre) != 0 {
		return "", "", fmt.Errorf("floor %q has a pre-release part", floor)
	}
	return version, floor, nil
}

// The platforms of a release and the archive each gets, as scripts/release.sh builds them.
var (
	platformOS   = []string{"darwin", "linux", "windows"}
	platformArch = []string{"amd64", "arm64"}
)

// archiveName is sidera_<version>_<os>_<arch>.tar.gz, or .zip for Windows. ok is
// false for a platform the releases are not built for.
func archiveName(version, goos, goarch string) (name string, ok bool) {
	if !contains(platformOS, goos) || !contains(platformArch, goarch) {
		return "", false
	}
	ext := ".tar.gz"
	if goos == "windows" {
		ext = ".zip"
	}
	return "sidera_" + version + "_" + goos + "_" + goarch + ext, true
}

func contains(list []string, s string) bool {
	for _, e := range list {
		if e == s {
			return true
		}
	}
	return false
}

// parseChecksums reads the sha256sum text of one release.
func parseChecksums(text []byte, version string) (map[string][sha256.Size]byte, error) {
	if len(text) == 0 {
		return nil, errors.New("empty")
	}
	if strings.ContainsRune(string(text), '\r') {
		return nil, errors.New("carriage return; lines end with a newline only")
	}
	body, ok := strings.CutSuffix(string(text), "\n")
	if !ok {
		return nil, errors.New("the last line does not end with a newline")
	}
	allowed := make(map[string]bool)
	for _, goos := range platformOS {
		for _, goarch := range platformArch {
			name, _ := archiveName(version, goos, goarch)
			allowed[name] = true
		}
	}
	archives := make(map[string][sha256.Size]byte)
	for i, line := range strings.Split(body, "\n") {
		sum, name, err := parseChecksumLine(line)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", i+1, err)
		}
		if !allowed[name] {
			return nil, fmt.Errorf("line %d: %q is not an archive of version %s", i+1, name, version)
		}
		if _, dup := archives[name]; dup {
			return nil, fmt.Errorf("line %d: %s is listed twice", i+1, name)
		}
		archives[name] = sum
	}
	return archives, nil
}

// parseChecksumLine splits `<64 lowercase hex digits>  <name>`, the two spaces
// being text mode of sha256sum. It does not judge the name.
func parseChecksumLine(line string) (sum [sha256.Size]byte, name string, err error) {
	const hexLen = 2 * sha256.Size
	digest, name, ok := strings.Cut(line, "  ")
	if !ok || len(digest) != hexLen || name == "" || strings.ContainsAny(name, " \t") {
		return sum, "", fmt.Errorf("%q is not `<%d lowercase hex digits>  <name>`", line, hexLen)
	}
	for _, c := range digest {
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f') {
			return sum, "", fmt.Errorf("%q is not `<%d lowercase hex digits>  <name>`", line, hexLen)
		}
	}
	if _, err := hex.Decode(sum[:], []byte(digest)); err != nil {
		return sum, "", err
	}
	return sum, name, nil
}
