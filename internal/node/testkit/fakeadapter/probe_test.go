package fakeadapter_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/testkit/fakeadapter"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/peer"
)

const probeSecret = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

// The child is the test program so argv and environment are exercised without a
// shell or platform-specific executable. Its output deliberately contains secrets.
func TestToolProbeCommand(t *testing.T) {
	if os.Getenv("SIDERA_TEST_PROBE_CHILD") != "yes" {
		return
	}
	args := os.Args
	frontend := args[len(args)-1]
	if args[len(args)-2] != "literal $() ; * argument" {
		os.Exit(11)
	}
	if os.Getenv("SIDERA_PROBE_TOKEN") != probeSecret || os.Getenv("SIDERA_HOME") != "request-home" || os.Getenv("OPAQUE_PROBE_VALUE") != "request-value" || os.Getenv("SIDERA_TEST_PROBE_FRONTEND") != frontend || os.Getenv("SIDERA_TEST_PROBE_INHERITED") != "inherited-value" {
		os.Exit(12)
	}
	if path := os.Getenv("SIDERA_TEST_PROBE_RECORD"); path != "" {
		if err := os.WriteFile(path, []byte(frontend+"/"+os.Getenv("OPAQUE_PROBE_VALUE")), 0o600); err != nil {
			os.Exit(15)
		}
	}
	_, _ = io.WriteString(os.Stdout, probeSecret)
	_, _ = io.WriteString(os.Stderr, probeSecret)
	if address := os.Getenv("SIDERA_TEST_PROBE_HOLD"); address != "" {
		conn, err := net.Dial("tcp", address)
		if err != nil {
			os.Exit(13)
		}
		defer conn.Close()
		_, _ = io.Copy(io.Discard, conn)
	}
	if os.Getenv("SIDERA_TEST_PROBE_FAIL") == "yes" {
		os.Exit(14)
	}
	os.Exit(0)
}

func probeCommand(t *testing.T, frontend adapterproto.Frontend) []string {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return []string{binary, "-test.run=^TestToolProbeCommand$", "--", "literal $() ; * argument", string(frontend)}
}

func probeRequest(frontend adapterproto.Frontend, preset string) adapterproto.ToolProbeRequest {
	return adapterproto.ToolProbeRequest{Preset: preset, Frontend: frontend, Token: probeSecret, Env: map[string]string{
		"SIDERA_PROBE_TOKEN": probeSecret, "SIDERA_HOME": "request-home", "OPAQUE_PROBE_VALUE": "request-value",
		"SIDERA_TEST_PROBE_CHILD": "yes", "SIDERA_TEST_PROBE_FRONTEND": string(frontend),
	}}
}

type probePeer struct {
	peer   *peer.Peer
	stop   func()
	log    func() *ledger.Log
	output func() []byte
}

func newProbePeer(t *testing.T, process bool, config *fakeadapter.ToolProbeConfig) probePeer {
	t.Helper()
	if !process {
		r := newRig(t, rigOptions{toolProbe: config})
		r.initialize()
		return probePeer{peer: r.daemon, stop: func() { _ = r.disconnect() }, log: r.ledgerLog, output: func() []byte {
			var out []byte
			for _, line := range r.log.snapshot() {
				if !line.toAdapter {
					out = append(out, line.line...)
				}
			}
			return out
		}}
	}
	f := newProcFixture(t)
	f.cfg.ToolProbe = config
	var stderr bytes.Buffer
	f.stderr = &stderr
	a := f.startAdapter()
	a.initialize()
	return probePeer{peer: a.daemon, stop: func() {
		_ = a.daemon.Close()
		select {
		case <-a.exited:
		case <-time.After(waitLimit):
			a.stop()
			t.Fatal("adapter did not finish after its protocol connection closed")
		}
	}, log: func() *ledger.Log {
		log, err := ledger.ReadDir(f.cfg.LedgerDir)
		if err != nil {
			t.Fatal(err)
		}
		return log
	}, output: func() []byte { return stderr.Bytes() }}
}

func TestToolProbeCommandsUseEachFrontendAndPresetEnvironment(t *testing.T) {
	// Request values must override stale inherited credentials, while unrelated
	// inherited values remain available to the command.
	t.Setenv("SIDERA_PROBE_TOKEN", "stale-token")
	t.Setenv("SIDERA_HOME", "stale-home")
	t.Setenv("OPAQUE_PROBE_VALUE", "stale-value")
	t.Setenv("SIDERA_TEST_PROBE_INHERITED", "inherited-value")
	cli := probeCommand(t, adapterproto.FrontendCLI)
	mcp := probeCommand(t, adapterproto.FrontendMCP)
	for _, process := range []bool{false, true} {
		layer := "local"
		if process {
			layer = "process"
		}
		t.Run(layer, func(t *testing.T) {
			for _, tc := range []struct {
				name             string
				config           *fakeadapter.ToolProbeConfig
				fail             bool
				wantCLI, wantMCP adapterproto.ProbeOutcome
			}{
				{"nil report only", nil, false, adapterproto.ProbeRan, adapterproto.ProbeRan},
				{"missing frontend", &fakeadapter.ToolProbeConfig{Commands: map[adapterproto.Frontend][]string{adapterproto.FrontendCLI: cli}}, false, adapterproto.ProbeRan, adapterproto.ProbeNotRun},
				{"empty command", &fakeadapter.ToolProbeConfig{Commands: map[adapterproto.Frontend][]string{adapterproto.FrontendCLI: {}, adapterproto.FrontendMCP: {""}}}, false, adapterproto.ProbeNotRun, adapterproto.ProbeNotRun},
				{"missing program", &fakeadapter.ToolProbeConfig{Commands: map[adapterproto.Frontend][]string{adapterproto.FrontendCLI: {filepath.Join(t.TempDir(), "absent")}, adapterproto.FrontendMCP: {filepath.Join(t.TempDir(), "absent")}}}, false, adapterproto.ProbeFailed, adapterproto.ProbeFailed},
				{"failed command", &fakeadapter.ToolProbeConfig{Commands: map[adapterproto.Frontend][]string{adapterproto.FrontendCLI: cli, adapterproto.FrontendMCP: mcp}}, true, adapterproto.ProbeFailed, adapterproto.ProbeFailed},
				{"successful commands", &fakeadapter.ToolProbeConfig{Commands: map[adapterproto.Frontend][]string{adapterproto.FrontendCLI: cli, adapterproto.FrontendMCP: mcp}}, false, adapterproto.ProbeRan, adapterproto.ProbeRan},
			} {
				t.Run(tc.name, func(t *testing.T) {
					a := newProbePeer(t, process, tc.config)
					ctx, cancel := context.WithTimeout(context.Background(), waitLimit)
					defer cancel()
					requests := []adapterproto.ToolProbeRequest{}
					for _, preset := range []string{"default", "second"} {
						for _, frontend := range []adapterproto.Frontend{adapterproto.FrontendCLI, adapterproto.FrontendMCP} {
							request := probeRequest(frontend, preset)
							request.Env["SIDERA_TEST_PROBE_RECORD"] = filepath.Join(t.TempDir(), preset+"-"+string(frontend))
							if tc.fail {
								request.Env["SIDERA_TEST_PROBE_FAIL"] = "yes"
							}
							requests = append(requests, request)
						}
					}
					var result adapterproto.ProbeResult
					if err := a.peer.Call(ctx, adapterproto.MethodAdapterProbe, adapterproto.ProbeParams{ToolProbes: requests}, &result); err != nil {
						t.Fatal(err)
					}
					if len(result.ToolProbes) != len(requests) || len(result.Presets) != 2 {
						t.Fatalf("probe count: %+v", result)
					}
					for i, got := range result.ToolProbes {
						want := tc.wantCLI
						if requests[i].Frontend == adapterproto.FrontendMCP {
							want = tc.wantMCP
						}
						if got.Preset != requests[i].Preset || got.Frontend != requests[i].Frontend || got.Outcome != want {
							t.Fatalf("probe %d: %+v", i, got)
						}
					}
					for _, request := range requests {
						want := tc.wantCLI
						if request.Frontend == adapterproto.FrontendMCP {
							want = tc.wantMCP
						}
						if want == adapterproto.ProbeRan && tc.config != nil || tc.fail {
							got, err := os.ReadFile(request.Env["SIDERA_TEST_PROBE_RECORD"])
							if err != nil || string(got) != string(request.Frontend)+"/request-value" {
								t.Fatalf("preset %s frontend %s environment did not reach its command: %q %v", request.Preset, request.Frontend, got, err)
							}
						}
					}
					// Stop before inspecting captured process output: its writer owns
					// the buffer until Wait has completed.
					a.stop()
					raw, err := json.Marshal(result)
					if err != nil {
						t.Fatal(err)
					}
					entries, err := json.Marshal(a.log().Entries())
					if err != nil {
						t.Fatal(err)
					}
					for _, captured := range [][]byte{raw, entries, a.output()} {
						if bytes.Contains(captured, []byte(probeSecret)) {
							t.Fatal("probe credential leaked")
						}
					}
				})
			}
		})
	}
}

func TestToolProbeCancellationReapsItsCommand(t *testing.T) {
	t.Setenv("SIDERA_TEST_PROBE_INHERITED", "inherited-value")
	for _, process := range []bool{false, true} {
		for _, end := range []string{"request", "shutdown", "connection"} {
			name := "local/" + end
			if process {
				name = "process/" + end
			}
			t.Run(name, func(t *testing.T) {
				listener, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				defer listener.Close()
				accepted := make(chan net.Conn, 1)
				go func() { conn, _ := listener.Accept(); accepted <- conn }()
				a := newProbePeer(t, process, &fakeadapter.ToolProbeConfig{Commands: map[adapterproto.Frontend][]string{adapterproto.FrontendCLI: probeCommand(t, adapterproto.FrontendCLI)}})
				ctx, cancel := context.WithTimeout(context.Background(), waitLimit)
				defer cancel()
				requestCtx, cancelRequest := context.WithCancel(ctx)
				defer cancelRequest()
				request := probeRequest(adapterproto.FrontendCLI, "default")
				request.Env["SIDERA_TEST_PROBE_HOLD"] = listener.Addr().String()
				finished := make(chan error, 1)
				go func() {
					finished <- a.peer.Call(requestCtx, adapterproto.MethodAdapterProbe, adapterproto.ProbeParams{ToolProbes: []adapterproto.ToolProbeRequest{request}}, nil)
				}()
				var conn net.Conn
				select {
				case conn = <-accepted:
					if conn == nil {
						t.Fatal("probe listener closed before command connected")
					}
				case err := <-finished:
					t.Fatalf("probe ended before starting command: %v", err)
				case <-ctx.Done():
					t.Fatal("command did not start")
				}
				defer conn.Close()
				switch end {
				case "request":
					cancelRequest()
				case "shutdown":
					if err := a.peer.Call(ctx, adapterproto.MethodShutdown, adapterproto.Empty{}, nil); err != nil {
						t.Fatal(err)
					}
				case "connection":
					a.stop()
				}
				_ = conn.SetReadDeadline(time.Now().Add(waitLimit))
				// A killed command can close its socket with EOF or a TCP reset.
				if n, err := conn.Read(make([]byte, 1)); n != 0 || !(errors.Is(err, io.EOF) || errors.Is(err, probeConnectionReset)) {
					t.Fatalf("owned command survived cancellation: %d %v", n, err)
				}
				select {
				case <-finished:
				case <-ctx.Done():
					t.Fatal("probe request did not finish")
				}
				if end != "connection" {
					a.stop()
				}
			})
		}
	}
}

func TestToolProbeConfigContainsOnlyStaticCommands(t *testing.T) {
	f := newProcFixture(t)
	raw, err := json.Marshal(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	withCommands := strings.TrimSuffix(string(raw), "}") + `,"tool_probe":{"commands":{"cli":["sidera","probe","--json"]}}}`
	cfg, err := fakeadapter.DecodeProcessConfig([]byte(withCommands))
	if err != nil || len(cfg.ToolProbe.Commands[adapterproto.FrontendCLI]) != 3 {
		t.Fatalf("static commands: %v", err)
	}
	for _, extra := range []string{`"env":{"SIDERA_PROBE_TOKEN":"secret"}`, `"token":"secret"`} {
		bad := strings.Replace(withCommands, `"tool_probe":{`, `"tool_probe":{`+extra+`,`, 1)
		if _, err := fakeadapter.DecodeProcessConfig([]byte(bad)); err == nil {
			t.Fatal("request credential field accepted in static config")
		}
	}
}
