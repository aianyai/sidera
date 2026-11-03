// Package fetch is how `sidera update` reaches GitHub: it finds the latest
// release and downloads one asset of a release, and does nothing else. It checks
// no signature and knows nothing of what a file contains; whatever it returns is
// untrusted until the caller has verified it.
package fetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/aianyai/sidera/sdk/go/semver"
)

// The most a download of each kind may deliver. A longer body is refused.
const (
	MaxChecksums int64 = 64 << 10
	MaxSignature int64 = 4 << 10
	MaxArchive   int64 = 256 << 20
)

const (
	repository = "https://github.com/aianyai/sidera"

	// maxRedirects is how many redirects one download follows.
	maxRedirects = 5

	// smallTimeout bounds finding the latest release and downloading a file of at
	// most MaxChecksums; archiveTimeout bounds a larger one, which at 20 KB/s is
	// still done in time for a package of the size the releases have.
	smallTimeout   = time.Minute
	archiveTimeout = 15 * time.Minute

	responseHeaderTimeout = 30 * time.Second
)

// ErrNotFound is the answer for a release or an asset GitHub does not have.
var ErrNotFound = errors.New("no such release or asset")

// ErrTooLarge is the answer for a body longer than the limit of its download.
var ErrTooLarge = errors.New("larger than the limit")

// isReleaseVersion reports whether s is a release version: a semantic version
// without the leading v and without build metadata, which a release tag never
// carries. It is also the only text that reaches a URL path from a response.
// scripts/lib/release-format.sh makes the release scripts accept exactly these.
func isReleaseVersion(s string) bool {
	if strings.Contains(s, "+") {
		return false
	}
	_, err := semver.Parse(s)
	return err == nil
}

// An asset is a file name of the release: no separator, no way up the path.
var assetPattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._-]*$`)

// Client talks to the GitHub repository of the releases. It sends no credential
// and pins no host: what a download delivers is authentic because the caller
// verifies its signature, not because of where it came from.
type Client struct {
	base      *url.URL
	userAgent string
	discover  *http.Client // does not follow redirects: the redirect is the answer
	download  *http.Client // follows redirects, every hop over HTTPS

	// The timeouts are fields so that a test can shorten them.
	smallTimeout   time.Duration
	archiveTimeout time.Duration
}

// UserAgent is the text `sidera update` identifies itself with: the version and
// the platform, which the package name states anyway.
func UserAgent(version string) string {
	return fmt.Sprintf("sidera/%s (%s/%s)", version, runtime.GOOS, runtime.GOARCH)
}

// New returns the client for the real repository. It reads the proxy from the
// environment (HTTP_PROXY, HTTPS_PROXY, NO_PROXY and their lower-case forms).
func New(userAgent string) *Client {
	base, err := url.Parse(repository)
	if err != nil {
		panic(err)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyFromEnvironment
	transport.ResponseHeaderTimeout = responseHeaderTimeout
	return NewAt(base, transport, userAgent)
}

// NewAt returns a client for a repository served at base over transport. It
// exists for tests, which serve a repository from a local server whose
// certificate they trust; nothing the user can set reaches it.
func NewAt(base *url.URL, transport http.RoundTripper, userAgent string) *Client {
	return &Client{
		base:      base,
		userAgent: userAgent,
		discover: &http.Client{
			Transport: transport,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		download: &http.Client{
			Transport:     transport,
			CheckRedirect: checkRedirect,
		},
		smallTimeout:   smallTimeout,
		archiveTimeout: archiveTimeout,
	}
}

// checkRedirect lets a download follow at most maxRedirects redirects, each to an
// HTTPS URL. The message names the host and not the URL, whose query may carry a
// signed token.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) > maxRedirects {
		return fmt.Errorf("stopped after %d redirects", maxRedirects)
	}
	if req.URL.Scheme != "https" {
		return fmt.Errorf("refusing a redirect to %s://%s: only HTTPS is followed", req.URL.Scheme, req.URL.Host)
	}
	return nil
}

// Latest returns the version of the latest release, without the leading v. It
// reads the redirect of <repository>/releases/latest and does not follow it.
func (c *Client) Latest(ctx context.Context) (string, error) {
	u := c.base.JoinPath("releases", "latest")
	ctx, cancel := context.WithTimeout(ctx, c.smallTimeout)
	defer cancel()
	resp, err := c.get(ctx, c.discover, u)
	if err != nil {
		return "", fmt.Errorf("find the latest release: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		return "", fmt.Errorf("find the latest release: %s answered %s, not a 302 redirect to a release", u.Host+u.Path, resp.Status)
	}
	location, err := resp.Location()
	if err != nil {
		return "", fmt.Errorf("find the latest release: the redirect of %s has no usable Location: %w", u.Host+u.Path, err)
	}
	version, ok := strings.CutPrefix(location.Path, c.base.JoinPath("releases", "tag").Path+"/v")
	if !ok || !isReleaseVersion(version) || location.String() != c.tagURL(version) {
		return "", fmt.Errorf("find the latest release: the redirect of %s leads to %q, not to a release tag of this repository", u.Host+u.Path, location.Redacted())
	}
	return version, nil
}

// Download writes the asset of the release v<version> to w, which receives at
// most limit bytes. A failure may leave a prefix in w; the caller discards it. The
// body is untrusted: nothing here has checked it.
func (c *Client) Download(ctx context.Context, version, asset string, w io.Writer, limit int64) error {
	what := fmt.Sprintf("download %s of v%s", asset, version)
	if !isReleaseVersion(version) {
		return fmt.Errorf("%s: %q is not a release version", what, version)
	}
	if !assetPattern.MatchString(asset) {
		return fmt.Errorf("%s: %q is not an asset name", what, asset)
	}
	timeout := c.smallTimeout
	if limit > MaxChecksums {
		timeout = c.archiveTimeout
	}
	// The deadline covers the body too: a stalled transfer ends as a slow one.
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	u := c.base.JoinPath("releases", "download", "v"+version, asset)
	resp, err := c.get(ctx, c.download, u)
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return fmt.Errorf("%s: %s answered 404: %w", what, resp.Request.URL.Host, ErrNotFound)
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("%s: %s answered %s", what, resp.Request.URL.Host, resp.Status)
	case resp.ContentLength > limit:
		return fmt.Errorf("%s: %d bytes announced, the limit is %d: %w", what, resp.ContentLength, limit, ErrTooLarge)
	}
	n, err := io.Copy(w, io.LimitReader(resp.Body, limit))
	if err != nil {
		return fmt.Errorf("%s: after %d bytes: %w", what, n, err)
	}
	if n == limit {
		var extra [1]byte
		switch _, err := io.ReadFull(resp.Body, extra[:]); {
		case err == nil:
			return fmt.Errorf("%s: more than %d bytes: %w", what, limit, ErrTooLarge)
		case !errors.Is(err, io.EOF):
			return fmt.Errorf("%s: after %d bytes: %w", what, n, err)
		}
	}
	return nil
}

// tagURL is where the redirect of the latest release leads.
func (c *Client) tagURL(version string) string {
	return c.base.JoinPath("releases", "tag", "v"+version).String()
}

// get sends one GET with the user agent. The error it returns is the cause
// without the URL the http package puts in front of it, which for a redirect is
// a signed address.
func (c *Client) get(ctx context.Context, client *http.Client, u *url.URL) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.userAgent)
	resp, err := client.Do(req)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return nil, fmt.Errorf("%s: %w", u.Host, err)
	}
	return resp, nil
}
