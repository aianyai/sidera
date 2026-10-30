package acceptance

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	cliagent "github.com/aianyai/sidera/cmd/sidera/internal/acceptance/agent/cli"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/fakectl"
	"github.com/aianyai/sidera/internal/node/testkit/fakeruntime"
	"github.com/aianyai/sidera/internal/node/testkit/scenario"
)

// sessionAgent retains only the call sequence. Native references and credentials
// are resolved again for every call, including calls after daemon replacement.
type sessionAgent struct {
	env      *environment
	endpoint string
	calls    uint64
}

var _ fakeagent.Agent = (*sessionAgent)(nil)

func sessionAgentFor(e scenario.Env, endpoint string) fakeagent.Agent {
	env := envFrom(e)
	if a, ok := env.sessions[endpoint]; ok {
		return a
	}
	w, err := env.fixture.files.Open("agent/"+endpoint, 0, env.fixture.Now)
	if err != nil {
		panic(err)
	}
	env.fixture.agentWriters = append(env.fixture.agentWriters, w)
	a := fakeagent.Recording(&sessionAgent{env: env, endpoint: endpoint}, w, endpoint, fakeagent.RecordingOptions{ModelSaw: true, Feedback: func(from, _ string) (string, bool) {
		address, err := ident.ParseAddress(from)
		return "notice", err == nil && address.Node == env.node && address.Endpoint == ident.SystemEndpoint()
	}})
	env.sessions[endpoint] = a
	return a
}

// sessionReference reads the fake runtime's published ProcessInfo for current
// control connections. Historical creation receipts cannot prove live ownership.
func (e *environment) sessionReference(endpoint string) (string, error) {
	var listed struct {
		Endpoints []struct{ Address, Kind, Instance, State string } `json:"endpoints"`
	}
	if err := managementRead(e, "endpoints.list", nil, &listed); err != nil {
		return "", err
	}
	var instance string
	for _, ep := range listed.Endpoints {
		address, err := ident.ParseAddress(ep.Address)
		if err != nil {
			return "", err
		}
		if address.Endpoint.String() == endpoint && ep.Kind == "session" && ep.State == "open" {
			if instance != "" {
				return "", fmt.Errorf("acceptance: multiple open session endpoints for %s", endpoint)
			}
			instance = ep.Instance
		}
	}
	if instance == "" {
		return "", fakeruntime.ErrUnknownSession
	}
	var dir string
	for _, input := range e.fixture.inputs {
		if input.process.InstanceID == instance {
			dir = input.process.RuntimeDir
		}
	}
	if dir == "" {
		return "", fmt.Errorf("acceptance: session instance %s has no runtime input", instance)
	}
	var ref string
	for _, conn := range e.fixture.control.Conns() {
		hello := conn.Hello()
		if hello.Role != fakectl.RoleRuntime {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, hello.ID+".json"))
		if err != nil {
			return "", err
		}
		info, err := fakeruntime.DecodeProcessInfo(raw)
		if err != nil {
			return "", err
		}
		if info.Ref != hello.ID || info.PID != hello.PID {
			return "", fmt.Errorf("acceptance: runtime process info does not match its live control connection")
		}
		if info.Endpoint != endpoint {
			continue
		}
		if ref != "" {
			return "", fmt.Errorf("acceptance: multiple live native owners for endpoint %s", endpoint)
		}
		ref = info.Ref
	}
	if ref == "" {
		return "", fakeruntime.ErrUnknownSession
	}
	return ref, nil
}

func (a *sessionAgent) cli() (*cliagent.Agent, error) {
	e := a.env
	if err := e.check(); err != nil {
		return nil, err
	}
	ref, err := e.sessionReference(a.endpoint)
	if err != nil {
		return nil, err
	}
	runtime, err := e.Runtime()
	if err != nil {
		return nil, err
	}
	actual, err := runtime.SessionEnv(ref)
	if err != nil {
		return nil, err
	}
	if actual["SIDERA_HOME"] == "" || actual["SIDERA_TOKEN"] == "" {
		return nil, fmt.Errorf("acceptance: live session lacks its tool environment")
	}
	return cliagent.New(cliagent.Config{Binary: e.fixture.binaries.Tagged, Home: actual["SIDERA_HOME"], Dir: e.fixture.root, Token: actual["SIDERA_TOKEN"], ProbeToken: actual["SIDERA_PROBE_TOKEN"], Env: func() []string {
		keys := make([]string, 0, len(actual))
		for key := range actual {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		values := make([]string, 0, len(actual)+5)
		for _, key := range keys {
			values = append(values, key+"="+actual[key])
		}
		a.calls++
		return append(values,
			"SIDERA_TEST_SEED="+fmt.Sprintf("%s/session-cli/%s/%d", e.fixture.opts.Seed, a.endpoint, a.calls),
			"SIDERA_TEST_CLOCK_START_MS="+strconv.FormatInt(e.fixture.Now().UnixMilli(), 10),
			"SIDERA_CRASH_RECORD=", "SIDERA_CRASH_AT=", "SIDERA_CRASH_GEN=")
	}}), nil
}

func (a *sessionAgent) Me(ctx context.Context) (fakeagent.MeResult, error) {
	cli, err := a.cli()
	if err != nil {
		return fakeagent.MeResult{}, err
	}
	return cli.Me(ctx)
}

func (a *sessionAgent) Peers(ctx context.Context) (fakeagent.PeersResult, error) {
	cli, err := a.cli()
	if err != nil {
		return fakeagent.PeersResult{}, err
	}
	return cli.Peers(ctx)
}

func (a *sessionAgent) Directory(ctx context.Context, in fakeagent.DirectoryInput) (fakeagent.DirectoryResult, error) {
	cli, err := a.cli()
	if err != nil {
		return fakeagent.DirectoryResult{}, err
	}
	return cli.Directory(ctx, in)
}

func (a *sessionAgent) Probe(ctx context.Context, in fakeagent.ProbeInput) (fakeagent.ProbeResult, error) {
	cli, err := a.cli()
	if err != nil {
		return fakeagent.ProbeResult{}, err
	}
	return cli.Probe(ctx, in)
}

func (a *sessionAgent) Send(ctx context.Context, in fakeagent.SendInput) (fakeagent.SendResult, error) {
	cli, err := a.cli()
	if err != nil {
		return fakeagent.SendResult{}, err
	}
	return cli.Send(ctx, in)
}

func (a *sessionAgent) Reply(ctx context.Context, in fakeagent.ReplyInput) (fakeagent.SendResult, error) {
	cli, err := a.cli()
	if err != nil {
		return fakeagent.SendResult{}, err
	}
	return cli.Reply(ctx, in)
}

func (a *sessionAgent) Recv(ctx context.Context, in fakeagent.RecvInput) (fakeagent.RecvResult, error) {
	cli, err := a.cli()
	if err != nil {
		return fakeagent.RecvResult{}, err
	}
	return cli.Recv(ctx, in)
}

func (a *sessionAgent) Ack(ctx context.Context, in fakeagent.AckInput) (fakeagent.AckResult, error) {
	cli, err := a.cli()
	if err != nil {
		return fakeagent.AckResult{}, err
	}
	return cli.Ack(ctx, in)
}

func (a *sessionAgent) Control(ctx context.Context, in fakeagent.ControlInput) (fakeagent.SendResult, error) {
	cli, err := a.cli()
	if err != nil {
		return fakeagent.SendResult{}, err
	}
	return cli.Control(ctx, in)
}

func (a *sessionAgent) Declare(ctx context.Context, in fakeagent.DeclareInput) error {
	cli, err := a.cli()
	if err != nil {
		return err
	}
	return cli.Declare(ctx, in)
}

func (a *sessionAgent) Status(ctx context.Context, in fakeagent.StatusInput) (fakeagent.StatusResult, error) {
	cli, err := a.cli()
	if err != nil {
		return fakeagent.StatusResult{}, err
	}
	return cli.Status(ctx, in)
}
