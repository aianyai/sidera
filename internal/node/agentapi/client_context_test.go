package agentapi_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/agentapi"
	"github.com/aianyai/sidera/internal/node/apiwire"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

func TestClientMapsContextsThatEndBeforeWriting(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		newContext func() (context.Context, context.CancelFunc)
	}{
		{"canceled", "request_canceled", func() (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx, cancel
		}},
		{"expired", "request_timeout", func() (context.Context, context.CancelFunc) {
			return context.WithDeadline(context.Background(), time.Unix(1, 0))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// No listener is needed: the context has already ended before dialing.
			c, err := agentapi.NewClient(agentapi.ClientConfig{Home: socketHome(t), ProbeToken: adapterproto.Token(strings.Repeat("A", 43))})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			ctx, cancel := tc.newContext()
			defer cancel()
			_, err = c.Probe(ctx, adapterproto.FrontendCLI)
			e := apiwire.FromError(err)
			if e == nil || e.Category != apiwire.CategoryUnavailable || e.Code != tc.code {
				t.Fatalf("Probe returned %v; want unavailable/%s", err, tc.code)
			}
		})
	}
}
