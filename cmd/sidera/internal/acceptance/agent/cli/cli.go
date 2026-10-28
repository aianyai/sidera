// Package cli drives the real CLI through the layer-neutral Agent seam.
package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/aianyai/sidera/internal/node/agentapi"
	"github.com/aianyai/sidera/internal/node/apiwire"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/strictjson"
)

// Config identifies the built program and one endpoint's injected environment.
// Dir lets two sessions run in the same project directory with distinct tokens.
type Config struct{ Binary, Home, Dir, Token, ProbeToken string }

// Agent starts one CLI process per call. It never retries or changes credentials.
type Agent struct{ cfg Config }

var _ fakeagent.Agent = (*Agent)(nil)

func New(cfg Config) *Agent { return &Agent{cfg} }

func (a *Agent) call(ctx context.Context, args []string, body *string, out any) ([]byte, error) {
	cmd := exec.CommandContext(ctx, a.cfg.Binary, append([]string{"--json", "--home", a.cfg.Home}, args...)...)
	cmd.Dir = a.cfg.Dir
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if name != "SIDERA_HOME" && name != "SIDERA_TOKEN" && name != "SIDERA_PROBE_TOKEN" {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "SIDERA_HOME="+a.cfg.Home, "SIDERA_TOKEN="+a.cfg.Token, "SIDERA_PROBE_TOKEN="+a.cfg.ProbeToken)
	if body != nil {
		cmd.Stdin = strings.NewReader(*body)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	raw := stdout.Bytes()
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			return nil, fmt.Errorf("cliagent: start CLI: %w", err)
		}
		var failure apiwire.Body
		if decode := strictjson.Decode(raw, &failure); decode != nil {
			return nil, fmt.Errorf("cliagent: CLI exited %d without a wire error: %w", exit.ExitCode(), decode)
		}
		if exit.ExitCode() != failure.Error.Category.ExitCode() {
			return nil, fmt.Errorf("cliagent: exit %d disagrees with category %s", exit.ExitCode(), failure.Error.Category)
		}
		return nil, &fakeagent.Error{Category: fakeagent.Category(failure.Error.Category), Code: failure.Error.Code, Message: failure.Error.Message}
	}
	if err := strictjson.Decode(raw, out); err != nil {
		return nil, fmt.Errorf("cliagent: CLI response: %w", err)
	}
	return raw, nil
}

func (a *Agent) Me(ctx context.Context) (fakeagent.MeResult, error) {
	var r agentapi.MeResult
	_, err := a.call(ctx, []string{"me"}, nil, &r)
	frontends := make(map[adapterproto.Frontend]fakeagent.FrontendLimits, len(r.Frontends))
	for name, f := range r.Frontends {
		frontends[name] = fakeagent.FrontendLimits{RecvWaitMaxS: int(f.RecvWaitMaxS), Budget: fakeagent.Budget{Unit: f.Budget.Unit, Limit: f.Budget.Limit}}
	}
	return fakeagent.MeResult{Address: r.Address, Node: r.Node, Kind: r.Kind, DisplayName: r.DisplayName, Declaration: fakeagent.Declaration{Paused: r.Declaration.Paused, RefuseMultiRecipient: r.Declaration.RefuseMultiRecipient, RestrictSenders: r.Declaration.RestrictSenders, AcceptFrom: r.Declaration.AcceptFrom, HiddenFromDirectory: r.Declaration.HiddenFromDirectory}, Frontends: frontends}, err
}

func (a *Agent) Peers(ctx context.Context) (fakeagent.PeersResult, error) {
	var r agentapi.PeersResult
	_, err := a.call(ctx, []string{"peers"}, nil, &r)
	out := fakeagent.PeersResult{Nodes: make([]fakeagent.PeerNode, len(r.Nodes))}
	for i, n := range r.Nodes {
		out.Nodes[i] = fakeagent.PeerNode{Node: n.Node, Self: n.Self}
	}
	return out, err
}

func (a *Agent) Directory(ctx context.Context, in fakeagent.DirectoryInput) (fakeagent.DirectoryResult, error) {
	args := []string{"directory"}
	args = option(args, "node", in.Node)
	args = option(args, "cursor", in.Cursor)
	if in.Limit != 0 {
		args = append(args, "--limit", strconv.Itoa(in.Limit))
	}
	var r agentapi.DirectoryResult
	_, err := a.call(ctx, args, nil, &r)
	out := fakeagent.DirectoryResult{NextCursor: r.NextCursor, Endpoints: make([]fakeagent.DirectoryEndpoint, len(r.Endpoints))}
	for i, e := range r.Endpoints {
		out.Endpoints[i] = fakeagent.DirectoryEndpoint{Address: e.Address, Kind: e.Kind, DisplayName: e.DisplayName, Description: e.Description, Accepts: e.Accepts}
	}
	return out, err
}

func (a *Agent) Probe(ctx context.Context, in fakeagent.ProbeInput) (fakeagent.ProbeResult, error) {
	if in.Frontend != "" && in.Frontend != adapterproto.FrontendCLI {
		return fakeagent.ProbeResult{}, fmt.Errorf("cliagent: probe supports only the cli front end")
	}
	var r agentapi.ProbeResult
	_, err := a.call(ctx, []string{"probe"}, nil, &r)
	return fakeagent.ProbeResult{OK: r.OK, Preset: r.Preset, Frontend: r.Frontend}, err
}

func (a *Agent) Send(ctx context.Context, in fakeagent.SendInput) (fakeagent.SendResult, error) {
	if in.DiscloseRecipients {
		return fakeagent.SendResult{}, fmt.Errorf("cliagent: the CLI cannot request recipient disclosure")
	}
	args := []string{"send", "--to", strings.Join(in.To, ",")}
	args = option(args, "content-type", in.ContentType)
	args = option(args, "thread", in.Thread)
	args = option(args, "key", in.IdemKey)
	if in.AcceptBeforeS != 0 {
		args = append(args, "--accept-before", strconv.FormatInt(in.AcceptBeforeS, 10))
	}
	var r agentapi.SendResult
	_, err := a.call(ctx, args, &in.Body, &r)
	return sendResult(r), err
}

func (a *Agent) Reply(ctx context.Context, in fakeagent.ReplyInput) (fakeagent.SendResult, error) {
	args := []string{"reply", string(in.MessageID)}
	args = option(args, "content-type", in.ContentType)
	args = option(args, "thread", in.Thread)
	args = option(args, "key", in.IdemKey)
	var r agentapi.SendResult
	_, err := a.call(ctx, args, &in.Body, &r)
	return sendResult(r), err
}

func (a *Agent) Recv(ctx context.Context, in fakeagent.RecvInput) (fakeagent.RecvResult, error) {
	if in.Frontend != "" && in.Frontend != adapterproto.FrontendCLI {
		return fakeagent.RecvResult{}, fmt.Errorf("cliagent: recv supports only the cli front end")
	}
	args := []string{"recv"}
	if in.Msg != "" || in.Part != 0 {
		args = append(args, "--msg", string(in.Msg), "--part", strconv.Itoa(in.Part))
	}
	if in.WaitS != 0 || (in.Msg == "" && in.Part == 0) {
		args = append(args, "--wait", strconv.Itoa(in.WaitS))
	}
	args = option(args, "ack", string(in.AckLease))
	var page adapterproto.RecvPage
	raw, err := a.call(ctx, args, nil, &page)
	return fakeagent.RecvResult{Raw: raw, Page: page}, err
}

func (a *Agent) Ack(ctx context.Context, in fakeagent.AckInput) (fakeagent.AckResult, error) {
	args := []string{"ack", string(in.Lease)}
	for _, item := range in.Items {
		args = append(args, fmt.Sprintf("%s:%d", item.MessageID, item.Part))
	}
	var r agentapi.AckResult
	_, err := a.call(ctx, args, nil, &r)
	return fakeagent.AckResult{Items: r.Items}, err
}

func (a *Agent) Control(ctx context.Context, in fakeagent.ControlInput) (fakeagent.SendResult, error) {
	args := []string{"control", in.To, string(in.Signal)}
	args = option(args, "preset", in.Preset)
	args = option(args, "body", in.Body)
	args = option(args, "key", in.IdemKey)
	var r agentapi.ControlResult
	_, err := a.call(ctx, args, nil, &r)
	return fakeagent.SendResult{MessageID: r.MessageID, To: []string{r.To}, Replayed: r.Replayed}, err
}

func (a *Agent) Declare(ctx context.Context, in fakeagent.DeclareInput) error {
	args := []string{"declare"}
	for _, p := range []struct {
		name string
		v    *bool
	}{{"paused", in.Paused}, {"refuse-multi-recipient", in.RefuseMultiRecipient}, {"restrict-senders", in.RestrictSenders}, {"hidden", in.HiddenFromDirectory}} {
		if p.v != nil {
			args = append(args, "--"+p.name+"="+strconv.FormatBool(*p.v))
		}
	}
	if in.AcceptFrom != nil {
		args = append(args, "--accept-from="+strings.Join(*in.AcceptFrom, ","))
	}
	var out agentapi.DeclareResult
	_, err := a.call(ctx, args, nil, &out)
	return err
}

func (a *Agent) Status(ctx context.Context, in fakeagent.StatusInput) (fakeagent.StatusResult, error) {
	var out adapterproto.MessageStatusResult
	_, err := a.call(ctx, []string{"status", string(in.MessageID)}, nil, &out)
	return out, err
}

func option(args []string, name, value string) []string {
	if value != "" {
		return append(args, "--"+name, value)
	}
	return args
}
func sendResult(r agentapi.SendResult) fakeagent.SendResult {
	return fakeagent.SendResult{MessageID: r.MessageID, To: r.To, Replayed: r.Replayed}
}
