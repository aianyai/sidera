package mcp_test

import (
	"context"
	"io"
	"testing"

	mcpagent "github.com/aianyai/sidera/cmd/sidera/internal/acceptance/agent/mcp"
	"github.com/aianyai/sidera/cmd/sidera/internal/mcp"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
)

func TestIndependentDriverUsesBothProtocolEras(t *testing.T) {
	for _, era := range []string{"2025-06-18", "2026-07-28"} {
		t.Run(era, func(t *testing.T) {
			h, c, address := agentNode(t)
			in, input := io.Pipe()
			output, out := io.Pipe()
			done := make(chan error, 1)
			go func() {
				done <- mcp.Run(h.Ctx(), mcp.Config{Stdin: in, Stdout: out, Stderr: io.Discard, Agent: c, Version: "test"})
			}()
			a, err := mcpagent.New(h.Ctx(), input, output, era)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				a.Close()
				if err := <-done; err != nil {
					t.Error(err)
				}
			})
			me, err := a.Me(context.Background())
			if err != nil || me.Address != address || me.Frontends["mcp"].RecvWaitMaxS != 45 {
				t.Fatalf("me: %+v %v", me, err)
			}
			_, err = h.Agent("creator").Send(h.Ctx(), fakeagent.SendInput{To: []string{address}, Body: "driver message"})
			if err != nil {
				t.Fatal(err)
			}
			h.Quiesce()
			page, err := a.Recv(h.Ctx(), fakeagent.RecvInput{})
			if err != nil || len(page.Page.Messages) != 1 || len(page.Raw) == 0 {
				t.Fatalf("receive: %+v %v", page, err)
			}
			ack, err := a.Ack(h.Ctx(), fakeagent.AckInput{Lease: page.Page.Lease})
			if err != nil || len(ack.Items) != 1 {
				t.Fatalf("ack: %+v %v", ack, err)
			}
		})
	}
}
