//go:build acceptance

package main_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	mcpagent "github.com/aianyai/sidera/cmd/sidera/internal/acceptance/agent/mcp"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

func (n *cliNode) mcp(s session, era string, options ...string) *mcpagent.Client {
	n.t.Helper()
	return startMCP(n.t, n.h.Ctx(), n.command(s, append(options, "mcp")...), era)
}

func startMCP(t *testing.T, ctx context.Context, cmd *exec.Cmd, era string) *mcpagent.Client {
	t.Helper()
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var diag bytes.Buffer
	cmd.Stderr = &diag
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var client *mcpagent.Client
	t.Cleanup(func() {
		input.Close()
		if client != nil {
			client.Close()
		} else {
			output.Close()
		}
		ended := make(chan error, 1)
		go func() { ended <- cmd.Wait() }()
		select {
		case err := <-ended:
			if err != nil {
				t.Errorf("MCP exit: %v; stderr %s", err, diag.String())
			}
		case <-time.After(20 * time.Second):
			cmd.Process.Kill()
			<-ended
			t.Error("MCP did not exit after stdin closed")
		}
	})
	client, err = mcpagent.New(ctx, input, output, era)
	if err != nil {
		t.Fatalf("MCP handshake %s: %v", era, err)
	}
	return client
}

func TestMCPCommandUsesTheRealProgramInBothEras(t *testing.T) {
	for _, era := range []string{"2025-06-18", "2026-07-28"} {
		t.Run(era, func(t *testing.T) {
			n := newNode(t)
			a := n.mcp(n.first, era, "--json")
			me, err := a.Me(n.h.Ctx())
			if err != nil || me.Address != n.first.address || me.Frontends[adapterproto.FrontendMCP].RecvWaitMaxS != 45 {
				t.Fatalf("MCP me: %+v %v", me, err)
			}
		})
	}
}

func TestMCPUnknownMethodsValidateEraThroughTheRealProgram(t *testing.T) {
	program := binary(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	home, err := os.MkdirTemp("", "mu-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	cmd := exec.CommandContext(ctx, program, "--home", home, "mcp")
	cmd.Env = append(os.Environ(), "SIDERA_TOKEN=", "SIDERA_PROBE_TOKEN=")
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var diag bytes.Buffer
	cmd.Stderr = &diag
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		input.Close()
		if err := cmd.Wait(); err != nil {
			t.Errorf("MCP exit: %v; stderr %s", err, diag.String())
		}
	})
	reader := bufio.NewReader(output)
	for _, tc := range []struct{ request, response string }{
		{`{"jsonrpc":"2.0","id":1,"method":"unknown","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2099-01-01","io.modelcontextprotocol/clientCapabilities":{}}}}`, `{"jsonrpc":"2.0","id":1,"error":{"code":-32022,"message":"unsupported protocol version","data":{"supported":["2026-07-28"],"requested":"2099-01-01"}}}`},
		{`{"jsonrpc":"2.0","id":2,"method":"unknown"}`, `{"jsonrpc":"2.0","id":2,"error":{"code":-32602,"message":"send initialize first, or carry io.modelcontextprotocol/protocolVersion in _meta"}}`},
	} {
		if _, err := fmt.Fprintln(input, tc.request); err != nil {
			t.Fatal(err)
		}
		line, err := reader.ReadBytes('\n')
		if err != nil {
			t.Fatal(err)
		}
		var got, want any
		if err := json.Unmarshal(line, &got); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(tc.response), &want); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("response:\n%s\nwant:\n%s", line, tc.response)
		}
	}
}

func TestMCPProcessErrorsStayOnStderrInJSONMode(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		exit        int
		args        []string
	}{
		{"EOF", "", 0, nil},
		{"truncated frame", `{"jsonrpc":"2.0"`, 1, nil},
		{"unexpected positional", "", 2, []string{"unexpected"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := newNode(t)
			cmd := n.command(n.first, append([]string{"--json", "mcp"}, tc.args...)...)
			cmd.Stdin = strings.NewReader(tc.input)
			var out, diag bytes.Buffer
			cmd.Stdout = &out
			cmd.Stderr = &diag
			err := cmd.Run()
			code := 0
			if err != nil {
				e, ok := err.(*exec.ExitError)
				if !ok {
					t.Fatal(err)
				}
				code = e.ExitCode()
			}
			if code != tc.exit || out.Len() != 0 || (code != 0 && !strings.Contains(diag.String(), "error:")) {
				t.Fatalf("exit %d stdout=%q stderr=%q; want exit %d with no stdout", code, out.String(), diag.String(), tc.exit)
			}
		})
	}
}

func TestLargeMessagesViaMCPInChineseAndEnglish(t *testing.T) {
	for _, era := range []string{"2025-06-18", "2026-07-28"} {
		for _, language := range []struct{ name, body string }{{"English", strings.Repeat("E", 60*1024)}, {"Chinese", strings.Repeat("中", 20*1024)}} {
			t.Run(era+"/"+language.name, func(t *testing.T) {
				n := newNode(t)
				a := n.mcp(n.first, era)
				want := map[adapterproto.MessageID]string{}
				for i := range 5 {
					sent, err := a.Send(n.h.Ctx(), fakeagent.SendInput{To: []string{n.first.address}, Body: language.body})
					if err != nil {
						t.Fatal(err)
					}
					want[sent.MessageID] = language.body
					if len(want) != i+1 {
						t.Fatal("separate sends reused an identifier")
					}
				}
				if err := n.h.Quiesce(); err != nil {
					t.Fatal(err)
				}
				got := map[adapterproto.MessageID]string{}
				var next *adapterproto.PageNext
				for pages := 0; pages < 100; pages++ {
					in := fakeagent.RecvInput{Frontend: adapterproto.FrontendMCP}
					if next != nil {
						in.Msg, in.Part = next.Msg, next.Part
					}
					result, err := a.Recv(n.h.Ctx(), in)
					if err != nil {
						t.Fatal(err)
					}
					if len(result.Raw) == 0 || result.Raw[len(result.Raw)-1] != '\n' {
						t.Fatal("MCP receive lost the page terminator")
					}
					if len(result.Page.Messages) == 0 {
						break
					}
					var items []adapterproto.PartRef
					next = nil
					for _, message := range result.Page.Messages {
						if _, ok := want[message.ID]; !ok {
							t.Fatalf("unexpected message %s", message.ID)
						}
						part := 1
						if message.Part != nil {
							part = message.Part.Index
							next = message.Part.Next
						}
						got[message.ID] += message.Body
						items = append(items, adapterproto.PartRef{MessageID: message.ID, Part: part})
					}
					ack, err := a.Ack(n.h.Ctx(), fakeagent.AckInput{Lease: result.Page.Lease, Items: items})
					if err != nil || len(ack.Items) != len(items) {
						t.Fatalf("ack count differs from visible items: %+v %v", ack, err)
					}
				}
				if len(got) != 5 {
					t.Fatalf("received %d messages, want 5", len(got))
				}
				for id, body := range want {
					if got[id] != body {
						t.Fatalf("body %s has %d bytes, want %d", id, len(got[id]), len(body))
					}
				}
				if err := n.h.Quiesce(); err != nil {
					t.Fatal(err)
				}
				// The node collects delivery reports on its injected periodic pull clock.
				for range 4 {
					n.h.Advance(engine.DefaultParams().PullInterval)
					if err := n.h.Quiesce(); err != nil {
						t.Fatal(err)
					}
				}
				for id := range want {
					status, err := a.Status(n.h.Ctx(), fakeagent.StatusInput{MessageID: id})
					if err != nil || len(status.Recipients) != 1 || status.Recipients[0].State != adapterproto.DeliveryDelivered {
						t.Fatalf("acknowledged message %s: %+v %v", id, status, err)
					}
				}
			})
		}
	}
}

func TestMCPAgentDriverExchangesAllToolsThroughTheRealProgram(t *testing.T) {
	for _, era := range []string{"2025-06-18", "2026-07-28"} {
		t.Run(era, func(t *testing.T) {
			n := newNode(t)
			a := n.mcp(n.first, era)
			me, err := a.Me(n.h.Ctx())
			if err != nil || me.Address != n.first.address {
				t.Fatalf("me: %+v %v", me, err)
			}
			paused := false
			senders := []string{}
			if err := a.Declare(n.h.Ctx(), fakeagent.DeclareInput{Paused: &paused, AcceptFrom: &senders}); err != nil {
				t.Fatal(err)
			}
			peers, err := a.Peers(n.h.Ctx())
			if err != nil || len(peers.Nodes) != 1 || !peers.Nodes[0].Self {
				t.Fatalf("peers: %+v %v", peers, err)
			}
			directory, err := a.Directory(n.h.Ctx(), fakeagent.DirectoryInput{Node: me.Node, Limit: 1})
			if err != nil || len(directory.Endpoints) != 1 || directory.NextCursor == "" {
				t.Fatalf("directory: %+v %v", directory, err)
			}
			input := fakeagent.SendInput{To: []string{n.first.address}, Body: "driver message", IdemKey: "driver-send"}
			sent, err := a.Send(n.h.Ctx(), input)
			if err != nil || sent.Replay != fakeagent.ReplayFresh {
				t.Fatalf("fresh send: %+v %v", sent, err)
			}
			again, err := a.Send(n.h.Ctx(), input)
			if err != nil || again.Replay != fakeagent.ReplayRepeated || again.MessageID != sent.MessageID {
				t.Fatalf("repeated send: %+v %v", again, err)
			}
			status, err := a.Status(n.h.Ctx(), fakeagent.StatusInput{MessageID: sent.MessageID})
			if err != nil || len(status.Recipients) != 1 || string(status.Recipients[0].To) != n.first.address {
				t.Fatalf("status: %+v %v", status, err)
			}
			if err := n.h.Quiesce(); err != nil {
				t.Fatal(err)
			}
			page, err := a.Recv(n.h.Ctx(), fakeagent.RecvInput{Frontend: adapterproto.FrontendMCP})
			if err != nil || len(page.Page.Messages) != 1 || page.Page.Messages[0].ID != sent.MessageID {
				t.Fatalf("recv: %+v %v", page, err)
			}
			if _, err := a.Recv(n.h.Ctx(), fakeagent.RecvInput{Msg: sent.MessageID, Part: 1, WaitS: 1}); fakeagent.CategoryOf(err) != fakeagent.CategoryInvalidArgument {
				t.Fatalf("part receive discarded its incompatible wait: %v", err)
			}
			if _, err := a.Recv(n.h.Ctx(), fakeagent.RecvInput{Frontend: adapterproto.FrontendCLI}); err == nil || err.Error() != "mcp test client: recv supports only the mcp front end" {
				t.Fatalf("CLI receive was silently sent as MCP: %v", err)
			}
			if _, err := a.Probe(n.h.Ctx(), fakeagent.ProbeInput{Frontend: adapterproto.FrontendCLI}); err == nil || err.Error() != "mcp test client: probe supports only the mcp front end" {
				t.Fatalf("CLI probe was silently sent as MCP: %v", err)
			}
			if _, err := a.Ack(n.h.Ctx(), fakeagent.AckInput{Lease: page.Page.Lease, Items: []adapterproto.PartRef{}}); fakeagent.CategoryOf(err) != fakeagent.CategoryInvalidArgument {
				t.Fatalf("empty acknowledgement list became whole-page confirmation: %v", err)
			}
			page, err = a.Recv(n.h.Ctx(), fakeagent.RecvInput{})
			if err != nil || len(page.Page.Messages) != 1 || !page.Page.Messages[0].PossiblyDuplicate {
				t.Fatalf("invalid ack lost the message: %+v %v", page, err)
			}
			ack, err := a.Ack(n.h.Ctx(), fakeagent.AckInput{Lease: page.Page.Lease, Items: []adapterproto.PartRef{{MessageID: sent.MessageID, Part: 1}}})
			if err != nil || len(ack.Items) != 1 {
				t.Fatalf("ack: %+v %v", ack, err)
			}
			original, err := n.h.Agent("creator").Send(n.h.Ctx(), fakeagent.SendInput{To: []string{n.first.address}, Body: "answer this"})
			if err != nil {
				t.Fatal(err)
			}
			if err := n.h.Quiesce(); err != nil {
				t.Fatal(err)
			}
			if _, err := a.Recv(n.h.Ctx(), fakeagent.RecvInput{}); err != nil {
				t.Fatal(err)
			}
			reply, err := a.Reply(n.h.Ctx(), fakeagent.ReplyInput{MessageID: original.MessageID, Body: "the answer"})
			if err != nil || len(reply.To) != 1 || reply.To[0] != n.h.Addr("creator") {
				t.Fatalf("reply: %+v %v", reply, err)
			}
			control, err := a.Control(n.h.Ctx(), fakeagent.ControlInput{To: n.h.RuntimeAddr(), Signal: adapterproto.SignalSessionCreate, Preset: "default"})
			if err != nil || control.MessageID == "" || len(control.To) != 1 || control.To[0] != n.h.RuntimeAddr() {
				t.Fatalf("control: %+v %v", control, err)
			}

			probes, err := n.h.Engine().ProbeTokens(n.h.Ctx(), harness.Instance)
			if err != nil {
				t.Fatal(err)
			}
			var probeToken adapterproto.Token
			for _, probe := range probes {
				if probe.Frontend == adapterproto.FrontendMCP {
					probeToken = probe.Token
				}
			}
			if probeToken == "" {
				t.Fatal("the adapter issued no MCP probe")
			}
			// A probe-only process must not acquire the session credential.
			cmd := n.command(session{}, "mcp", "--json")
			cmd.Env = append(cmd.Env, "SIDERA_PROBE_TOKEN="+string(probeToken))
			probeAgent := startMCP(t, n.h.Ctx(), cmd, era)
			if _, err := probeAgent.Me(n.h.Ctx()); fakeagent.CodeOf(err) != "token_missing" {
				t.Fatalf("probe credential authenticated me: %v", err)
			}
			probe, err := probeAgent.Probe(n.h.Ctx(), fakeagent.ProbeInput{Frontend: adapterproto.FrontendMCP})
			// The automatic handshake probe and explicit tool share the one-use token.
			if err != nil && fakeagent.CodeOf(err) != "probe_token_used" {
				t.Fatal(err)
			}
			if err == nil && (!probe.OK || probe.Frontend != adapterproto.FrontendMCP) {
				t.Fatalf("probe: %+v", probe)
			}
			adapter, err := n.h.Engine().Adapter(n.h.Ctx(), harness.Instance)
			if err != nil {
				t.Fatal(err)
			}
			reached := false
			for _, item := range adapter.Probe.Reached {
				reached = reached || item.Frontend == adapterproto.FrontendMCP
			}
			if !reached {
				t.Fatal("the real MCP process did not present its probe credential")
			}
		})
	}
}
