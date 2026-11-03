package fetch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	testAgent = "sidera/9.9.9 (test/test)"
	repoPath  = "/owner/repo"
)

// repo serves a repository at repoPath over TLS and returns a client that trusts it.
func repo(t *testing.T, handler http.Handler) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewTLSServer(handler)
	t.Cleanup(srv.Close)
	base, err := url.Parse(srv.URL + repoPath)
	if err != nil {
		t.Fatal(err)
	}
	return NewAt(base, srv.Client().Transport, testAgent), srv
}

func TestUserAgent(t *testing.T) {
	want := fmt.Sprintf("sidera/0.11.0 (%s/%s)", runtime.GOOS, runtime.GOARCH)
	if got := UserAgent("0.11.0"); got != want {
		t.Fatalf("UserAgent = %q, want %q", got, want)
	}
}

func TestLatest(t *testing.T) {
	tag := repoPath + "/releases/tag/v"
	tests := []struct {
		name     string
		status   int
		location func(host string) string // nil: no Location header
		want     string
		wantErr  string
	}{
		{name: "release", status: 302, location: func(h string) string { return "https://" + h + tag + "0.11.0" }, want: "0.11.0"},
		{name: "pre-release", status: 302, location: func(h string) string { return "https://" + h + tag + "0.12.0-rc.1" }, want: "0.12.0-rc.1"},
		{name: "relative location", status: 302, location: func(string) string { return tag + "1.2.3" }, want: "1.2.3"},

		{name: "other repository", status: 302, location: func(h string) string { return "https://" + h + "/other/repo/releases/tag/v1.2.3" }, wantErr: "not to a release tag"},
		{name: "other host", status: 302, location: func(string) string { return "https://example.com" + tag + "1.2.3" }, wantErr: "not to a release tag"},
		{name: "plain HTTP", status: 302, location: func(h string) string { return "http://" + h + tag + "1.2.3" }, wantErr: "not to a release tag"},
		{name: "not a tag page", status: 302, location: func(h string) string { return "https://" + h + repoPath + "/releases" }, wantErr: "not to a release tag"},
		{name: "no leading v", status: 302, location: func(h string) string { return "https://" + h + repoPath + "/releases/tag/1.2.3" }, wantErr: "not to a release tag"},
		{name: "two components", status: 302, location: func(h string) string { return "https://" + h + tag + "1.2" }, wantErr: "not to a release tag"},
		{name: "build metadata", status: 302, location: func(h string) string { return "https://" + h + tag + "1.2.3+build.5" }, wantErr: "not to a release tag"},
		{name: "extra path", status: 302, location: func(h string) string { return "https://" + h + tag + "1.2.3/x" }, wantErr: "not to a release tag"},
		{name: "query", status: 302, location: func(h string) string { return "https://" + h + tag + "1.2.3?x=1" }, wantErr: "not to a release tag"},
		{name: "user info", status: 302, location: func(h string) string { return "https://u:p@" + h + tag + "1.2.3" }, wantErr: "not to a release tag"},
		{name: "unparsable location", status: 302, location: func(string) string { return "https://%zz" }, wantErr: "failed to parse Location"},
		{name: "no location", status: 302, wantErr: "no usable Location"},

		{name: "permanent redirect", status: 301, location: func(h string) string { return "https://" + h + tag + "1.2.3" }, wantErr: "not a 302"},
		{name: "ok page", status: 200, wantErr: "200 OK"},
		{name: "not found", status: 404, wantErr: "404 Not Found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var tagHits atomic.Int32
			client, _ := repo(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == repoPath+"/releases/latest":
					if r.Method != http.MethodGet || r.Header.Get("User-Agent") != testAgent {
						t.Errorf("request %s with User-Agent %q", r.Method, r.Header.Get("User-Agent"))
					}
					if tt.location != nil {
						w.Header().Set("Location", tt.location(r.Host))
					}
					w.WriteHeader(tt.status)
				case strings.HasPrefix(r.URL.Path, tag):
					tagHits.Add(1)
				}
			}))
			got, err := client.Latest(context.Background())
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Latest = %q, %v; want an error containing %q", got, err, tt.wantErr)
				}
			} else if err != nil || got != tt.want {
				t.Fatalf("Latest = %q, %v; want %q", got, err, tt.want)
			}
			if tagHits.Load() != 0 {
				t.Fatal("Latest followed the redirect")
			}
		})
	}
}

func TestLatestTimesOut(t *testing.T) {
	client, _ := repo(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	client.smallTimeout = 50 * time.Millisecond
	if _, err := client.Latest(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Latest = %v, want a deadline error", err)
	}
}

// assetHandler serves body for the asset of v1.0.0 and checks the request.
func assetHandler(t *testing.T, body func(w http.ResponseWriter, r *http.Request)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != repoPath+"/releases/download/v1.0.0/a.tar.gz" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("User-Agent") != testAgent {
			t.Errorf("User-Agent %q", r.Header.Get("User-Agent"))
		}
		body(w, r)
	})
}

func TestDownload(t *testing.T) {
	client, _ := repo(t, assetHandler(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "payload") }))
	var buf bytes.Buffer
	if err := client.Download(context.Background(), "1.0.0", "a.tar.gz", &buf, 7); err != nil {
		t.Fatalf("Download: %v", err)
	}
	if buf.String() != "payload" {
		t.Fatalf("body = %q", buf.String())
	}
}

func TestDownloadRefusals(t *testing.T) {
	long := strings.Repeat("x", 100)
	tests := []struct {
		name    string
		handle  func(w http.ResponseWriter, r *http.Request)
		limit   int64
		wantIs  error
		wantMsg string
		nothing bool // the refusal comes before a byte is written
	}{
		{name: "announced too long", limit: 10, wantIs: ErrTooLarge, nothing: true,
			handle: func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, long) }},
		{name: "unannounced too long", limit: 10, wantIs: ErrTooLarge,
			handle: func(w http.ResponseWriter, r *http.Request) {
				io.WriteString(w, long[:50])
				w.(http.Flusher).Flush() // chunked: no Content-Length
				io.WriteString(w, long[50:])
			}},
		{name: "one byte over", limit: 99, wantIs: ErrTooLarge,
			handle: func(w http.ResponseWriter, r *http.Request) {
				io.WriteString(w, long[:50])
				w.(http.Flusher).Flush()
				io.WriteString(w, long[50:])
			}},
		{name: "server error", limit: 10, wantMsg: "503 Service Unavailable",
			handle: func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }},
		{name: "cut short", limit: 1000, wantIs: io.ErrUnexpectedEOF,
			handle: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Length", "100")
				io.WriteString(w, long[:10])
				w.(http.Flusher).Flush()
				panic(http.ErrAbortHandler)
			}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, _ := repo(t, assetHandler(t, tt.handle))
			var buf bytes.Buffer
			err := client.Download(context.Background(), "1.0.0", "a.tar.gz", &buf, tt.limit)
			switch {
			case err == nil:
				t.Fatal("Download succeeded")
			case tt.wantIs != nil && !errors.Is(err, tt.wantIs):
				t.Fatalf("Download = %v, want %v", err, tt.wantIs)
			case !strings.Contains(err.Error(), tt.wantMsg):
				t.Fatalf("Download = %v, want a message containing %q", err, tt.wantMsg)
			}
			if tt.nothing && buf.Len() != 0 {
				t.Fatalf("%d bytes written before the refusal", buf.Len())
			}
			if int64(buf.Len()) > tt.limit {
				t.Fatalf("%d bytes written, limit %d", buf.Len(), tt.limit)
			}
		})
	}
}

func TestDownloadBodyAtTheLimit(t *testing.T) {
	client, _ := repo(t, assetHandler(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "01234")
		w.(http.Flusher).Flush()
		io.WriteString(w, "56789")
	}))
	var buf bytes.Buffer
	if err := client.Download(context.Background(), "1.0.0", "a.tar.gz", &buf, 10); err != nil || buf.Len() != 10 {
		t.Fatalf("Download = %v with %d bytes", err, buf.Len())
	}
}

func TestDownloadMissing(t *testing.T) {
	client, _ := repo(t, http.NotFoundHandler())
	err := client.Download(context.Background(), "1.0.0", "checksums.txt", io.Discard, 10)
	if !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "checksums.txt of v1.0.0") {
		t.Fatalf("Download = %v, want ErrNotFound naming the asset and release", err)
	}
}

func TestDownloadTimesOut(t *testing.T) {
	client, _ := repo(t, assetHandler(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "first")
		w.(http.Flusher).Flush()
		<-r.Context().Done() // released when the client gives up
	}))
	client.smallTimeout = 100 * time.Millisecond
	err := client.Download(context.Background(), "1.0.0", "a.tar.gz", io.Discard, 1000)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Download = %v, want a deadline error", err)
	}
}

func TestDownloadLargeObjectsGetTheLongTimeout(t *testing.T) {
	client, _ := repo(t, assetHandler(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		io.WriteString(w, "late")
	}))
	client.smallTimeout = 50 * time.Millisecond
	if err := client.Download(context.Background(), "1.0.0", "a.tar.gz", io.Discard, MaxChecksums+1); err != nil {
		t.Fatalf("a download above MaxChecksums took the short timeout: %v", err)
	}
	if err := client.Download(context.Background(), "1.0.0", "a.tar.gz", io.Discard, MaxChecksums); err == nil {
		t.Fatal("a download of MaxChecksums took the long timeout")
	}
}

func TestDownloadRejectsNamesBeforeAnyRequest(t *testing.T) {
	var hits atomic.Int32
	client, _ := repo(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	for _, c := range []struct{ version, asset string }{
		{"v1.0.0", "a"}, {"1.0", "a"}, {"1.0.0/../../x", "a"}, {"", "a"},
		{"1.0.0", ""}, {"1.0.0", "../a"}, {"1.0.0", "a/b"}, {"1.0.0", ".hidden"}, {"1.0.0", "a?b"},
	} {
		if err := client.Download(context.Background(), c.version, c.asset, io.Discard, 10); err == nil {
			t.Errorf("Download(%q, %q) succeeded", c.version, c.asset)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("%d requests were sent", hits.Load())
	}
}

// TestDownloadRedirects follows a chain of redirects that alternates between two
// servers, so a redirect to another host is followed, and stops past the limit.
func TestDownloadRedirects(t *testing.T) {
	for _, tt := range []struct {
		hops    int
		wantErr string
	}{
		{hops: 0}, {hops: 1}, {hops: maxRedirects}, {hops: maxRedirects + 1, wantErr: "stopped after 5 redirects"},
	} {
		t.Run(fmt.Sprint(tt.hops), func(t *testing.T) {
			var servers [2]*httptest.Server
			handler := func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("User-Agent") != testAgent {
					t.Errorf("%s: User-Agent %q", r.URL.Path, r.Header.Get("User-Agent"))
				}
				hop := 0 // the asset URL is the first request
				if _, err := fmt.Sscanf(r.URL.Path, "/hop/%d", &hop); err != nil && !strings.HasPrefix(r.URL.Path, repoPath) {
					t.Errorf("unexpected path %s", r.URL.Path)
				}
				if hop < tt.hops {
					http.Redirect(w, r, fmt.Sprintf("%s/hop/%d", servers[(hop+1)%2].URL, hop+1), http.StatusFound)
					return
				}
				io.WriteString(w, "payload")
			}
			var client *Client
			client, servers[0] = repo(t, http.HandlerFunc(handler))
			servers[1] = httptest.NewTLSServer(http.HandlerFunc(handler))
			t.Cleanup(servers[1].Close)

			var buf bytes.Buffer
			err := client.Download(context.Background(), "1.0.0", "a.tar.gz", &buf, 100)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Download = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || buf.String() != "payload" {
				t.Fatalf("Download = %v, body %q", err, buf.String())
			}
		})
	}
}

func TestDownloadRefusesRedirectToPlainHTTP(t *testing.T) {
	var plainHits atomic.Int32
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		plainHits.Add(1)
		io.WriteString(w, "payload")
	}))
	t.Cleanup(plain.Close)
	client, _ := repo(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+"/asset?sig=SECRET", http.StatusFound)
	}))
	var buf bytes.Buffer
	err := client.Download(context.Background(), "1.0.0", "a.tar.gz", &buf, 100)
	if err == nil || !strings.Contains(err.Error(), "only HTTPS is followed") {
		t.Fatalf("Download = %v, want a refusal of the plain HTTP hop", err)
	}
	if strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("the error shows the query of the redirect: %v", err)
	}
	if plainHits.Load() != 0 || buf.Len() != 0 {
		t.Fatalf("the plain HTTP server was reached (%d requests, %d bytes)", plainHits.Load(), buf.Len())
	}
}
