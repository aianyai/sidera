package localhttp

import (
	"net/http"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/ipc"
)

// These are the transport configuration, so the check stays with its owner.
func TestTheServerTimeouts(t *testing.T) {
	s, err := New(Config{Kind: ipc.KindManage, PathPrefix: "/v1/manage/", Lookup: func(string) http.HandlerFunc { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	h := s.http
	if h.ReadHeaderTimeout != 10*time.Second || h.ReadTimeout != 30*time.Second || h.IdleTimeout != 60*time.Second || h.MaxHeaderBytes != 8<<10 {
		t.Errorf("limits: header %v, read %v, idle %v, header bytes %d", h.ReadHeaderTimeout, h.ReadTimeout, h.IdleTimeout, h.MaxHeaderBytes)
	}
	if h.WriteTimeout != 0 {
		t.Errorf("a write timeout of %v would cut a request that waits", h.WriteTimeout)
	}
}
