package update

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/aianyai/sidera/cmd/sidera/internal/update/fetch"
	"github.com/aianyai/sidera/cmd/sidera/internal/update/unpack"
	"github.com/aianyai/sidera/cmd/sidera/internal/update/verify"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/release"
)

// lock takes the update lock for the whole run, so that two runs cannot download
// into the same directory or replace the same programs at once. The cache root is
// made owner-only, and one that is not is refused.
func (s *session) lock() (unlock func(), err error) {
	if err := os.MkdirAll(s.cfg.CacheRoot, 0o700); err != nil {
		return nil, fmt.Errorf("make the cache directory: %w", err)
	}
	if err := checkCacheRoot(s.cfg.CacheRoot); err != nil {
		return nil, err
	}
	cache, err := fsx.OpenOS(s.cfg.CacheRoot, s.cfg.Random)
	if err != nil {
		return nil, err
	}
	release, err := cache.Lock(lockName)
	if errors.Is(err, fsx.ErrLocked) {
		return nil, fmt.Errorf("another sidera update is running (it holds %s)", filepath.Join(s.cfg.CacheRoot, lockName))
	}
	if err != nil {
		return nil, fmt.Errorf("lock %s: %w", filepath.Join(s.cfg.CacheRoot, lockName), err)
	}
	return func() { _ = release() }, nil
}

// checkCacheRoot refuses a cache directory that belongs to another user or that other
// users can enter, such as one under a cache directory that several users share:
// what is downloaded there is checked once and used later, and the lock is only as
// good as the directory it is in.
func checkCacheRoot(root string) error {
	fi, err := os.Lstat(root)
	if err != nil {
		return fmt.Errorf("look at the cache directory %s: %w", root, err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("the cache directory %s is not a directory (a symbolic link is not followed); remove it", root)
	}
	owned, owner, err := ownedByCaller(root, fi)
	if err != nil {
		return fmt.Errorf("find the owner of the cache directory %s: %w", root, err)
	}
	if !owned {
		return fmt.Errorf("the cache directory %s is owned by %s, not by the user running update; remove it, or run update as its owner", root, owner)
	}
	if reachableByOthers(fi) {
		return fmt.Errorf("the cache directory %s is open to other users (mode %v); run chmod 700 on it, or remove it", root, fi.Mode().Perm())
	}
	return nil
}

// releaseDir is where one release is downloaded and unpacked.
func (s *session) releaseDir() string { return filepath.Join(s.cfg.CacheRoot, s.target) }

// dropCache removes what the run downloaded. The next run starts from nothing in any
// case, so a failure to remove it costs only disk space.
func (s *session) dropCache() {
	if s.target != "" {
		_ = os.RemoveAll(s.releaseDir())
	}
}

// obtainArchive downloads the archive of the platform into a fresh owner-only
// directory, checks that file against the signed digest and unpacks that same file,
// then runs the unpacked program to see that it is the release asked for. It returns
// the directory the archive was unpacked into.
func (s *session) obtainArchive(ctx context.Context) (string, error) {
	name, sum, err := s.rel.Archive(s.cfg.GOOS, s.cfg.GOARCH)
	if err != nil {
		return "", err
	}
	dir := s.releaseDir()
	// Nothing left by an earlier run is trusted.
	if err := os.RemoveAll(dir); err != nil {
		return "", fmt.Errorf("clear %s: %w", dir, err)
	}
	if err := fsx.MakeRoot(dir); err != nil {
		return "", fmt.Errorf("make %s: %w", dir, err)
	}
	archive := filepath.Join(dir, name)
	s.step("downloading " + name)
	if err := s.download(ctx, name, archive); err != nil {
		return "", err
	}
	s.step("checking and unpacking " + name)
	if err := checkDigest(archive, sum); err != nil {
		return "", err
	}
	files := filepath.Join(dir, "files")
	if err := os.Mkdir(files, 0o700); err != nil {
		return "", err
	}
	if err := unpack.Extract(archive, files, unpack.DefaultLimits()); err != nil {
		return "", fmt.Errorf("release %s: %w", s.target, err)
	}
	if err := s.smoke(ctx, files); err != nil {
		return "", err
	}
	return files, nil
}

func (s *session) download(ctx context.Context, name, path string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	err = s.cfg.Fetch.Download(ctx, s.target, name, f, fetch.MaxArchive)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// checkDigest reads the file that was written, not the stream that wrote it, so that
// what is verified is what is unpacked.
func checkDigest(path string, sum [32]byte) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := verify.VerifyDigest(f, sum); err != nil {
		return fmt.Errorf("the downloaded %s does not match the signed checksum: %w", filepath.Base(path), err)
	}
	return nil
}

// smoke runs the unpacked program: it must be this release, from an archive.
func (s *session) smoke(ctx context.Context, files string) error {
	name := programName
	if s.cfg.GOOS == "windows" {
		name += ".exe"
	}
	run := s.cfg.Smoke
	if run == nil {
		run = launchSmoke
	}
	info, err := run(ctx, filepath.Join(files, name))
	if err != nil {
		return fmt.Errorf("the unpacked %s does not run: %w", name, err)
	}
	if info.Version != s.target || info.Distribution != release.DistributionArchive {
		return fmt.Errorf("the unpacked %s reports version %q, distribution %q; want %s from an archive", name, info.Version, info.Distribution, s.target)
	}
	return nil
}
