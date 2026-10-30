package acceptance

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/testkit/fakectl"
	"github.com/aianyai/sidera/sdk/go/peer"
)

func TestSyncConnectionsRequiresOnlyLiveMembers(t *testing.T) {
	for _, method := range []string{fakectl.MethodClockSet, fakectl.MethodStepSet} {
		for _, closes := range []bool{true, false} {
			outcome := "remote-error"
			if closes {
				outcome = "closed"
			}
			t.Run(method+"/"+outcome, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				server, err := fakectl.Listen(ctx)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = server.Close() })
				connect := func(id string, setup func(*fakectl.Client)) *fakectl.Conn {
					t.Helper()
					client, err := fakectl.Dial(ctx, server.Addr())
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = client.Close() })
					client.HandleClock(func(int64) {})
					client.HandleStep(func(string) {})
					setup(client)
					if err := client.Start(ctx, fakectl.HelloParams{Role: fakectl.RoleAdapter, ID: id, PID: 1}); err != nil {
						t.Fatal(err)
					}
					conn, err := server.Wait(ctx, id)
					if err != nil {
						t.Fatal(err)
					}
					return conn
				}
				departing := connect("first", func(client *fakectl.Client) {
					client.Handle(method, func(context.Context, json.RawMessage) (any, error) {
						if closes {
							return nil, client.Close()
						}
						return nil, peer.NewError(1042, "synchronization refused")
					})
				})
				clock := fakectl.NewClock(1000)
				steps := make(chan string, 1)
				connect("live", func(client *fakectl.Client) {
					client.HandleClock(clock.SetMS)
					client.HandleStep(func(step string) { steps <- step })
				})
				f := &Fixture{control: server, now: time.Unix(1800000000, 0), step: "current"}
				err = f.syncConnections(ctx)
				if !closes {
					var remote *peer.Error
					if !errors.As(err, &remote) || remote.Code != 1042 {
						t.Fatalf("syncConnections = %v, want remote refusal", err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				select {
				case <-departing.Done():
				default:
					t.Fatal("synchronization accepted an unconfirmed departure")
				}
				if got := clock.Now(); !got.Equal(f.Now()) {
					t.Fatalf("live member clock = %s, want %s", got, f.Now())
				}
				select {
				case got := <-steps:
					if got != f.step {
						t.Fatalf("live member step = %q, want %q", got, f.step)
					}
				default:
					t.Fatal("live member did not acknowledge the current step")
				}
			})
		}
	}
}
