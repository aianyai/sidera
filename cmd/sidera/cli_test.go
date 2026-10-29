package main_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	cliagent "github.com/aianyai/sidera/cmd/sidera/internal/acceptance/agent/cli"
	"github.com/aianyai/sidera/internal/node/agentapi"
	"github.com/aianyai/sidera/internal/node/apiwire"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/ipc"
	"github.com/aianyai/sidera/internal/node/manageapi"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

var buildOnce sync.Once
var cliBinary string
var buildError error

func binary(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "cli-bin-")
		if err != nil {
			buildError = err
			return
		}
		cliBinary = filepath.Join(dir, "sidera")
		if runtime.GOOS == "windows" {
			cliBinary += ".exe"
		}
		args := []string{"build", "-o", cliBinary}
		if raceEnabled {
			args = append(args, "-race")
		}
		if out, err := exec.Command("go", append(args, ".")...).CombinedOutput(); err != nil {
			buildError = fmt.Errorf("build CLI: %w: %s", err, out)
		}
	})
	if buildError != nil {
		t.Fatal(buildError)
	}
	return cliBinary
}

func TestMain(m *testing.M) {
	code := m.Run()
	if cliBinary != "" {
		_ = os.RemoveAll(filepath.Dir(cliBinary))
	}
	os.Exit(code)
}

type session struct{ token, address string }
type cliNode struct {
	t     *testing.T
	h     *harness.H
	home  string
	first session
}
type held struct{}

func (held) Held() bool { return true }

func newNode(t *testing.T, cut ...chan struct{}) *cliNode {
	t.Helper()
	binary(t)
	h := harness.New(t, harness.Options{Adapter: &harness.AdapterOptions{Runtime: true}})
	if err := h.Quiesce(); err != nil {
		t.Fatal(err)
	}
	h.MustDirect("creator")
	created, err := h.Agent("creator").Control(h.Ctx(), fakeagent.ControlInput{To: h.RuntimeAddr(), Signal: adapterproto.SignalSessionCreate, Preset: "default"})
	if err != nil {
		t.Fatal(err)
	}
	n := &cliNode{t: t, h: h}
	n.first = n.created(created.MessageID)
	home, err := os.MkdirTemp("", "cl-")
	if err != nil {
		t.Fatal(err)
	}
	n.home = home
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	if err := os.Mkdir(filepath.Join(home, "run"), 0700); err != nil {
		t.Fatal(err)
	}
	addr, err := ipc.Locate(home, ipc.KindAgent)
	if err != nil {
		t.Fatal(err)
	}
	l, err := ipc.Listen(addr, held{}, ipc.Options{})
	if err != nil {
		t.Fatal(err)
	}
	s, err := agentapi.New(agentapi.Config{Service: h.Engine()})
	if err != nil {
		t.Fatal(err)
	}
	s.EnableMethods()
	var handler http.Handler = s
	if len(cut) > 0 {
		var once sync.Once
		handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/recv") {
				pause := false
				once.Do(func() { pause = true })
				if pause {
					s.ServeHTTP(&cutResponse{ResponseWriter: w, request: r, started: cut[0]}, r)
					return
				}
			}
			s.ServeHTTP(w, r)
		})
	}
	server := &http.Server{Handler: handler}
	ended := make(chan error, 1)
	go func() { ended <- server.Serve(l) }()
	t.Cleanup(func() {
		server.Close()
		s.Close()
		if err := <-ended; err != http.ErrServerClosed {
			t.Error(err)
		}
	})
	return n
}

// cutResponse holds an actual Agent response after its first bytes reached the
// CLI. Killing that process cancels the request and releases the handler.
type cutResponse struct {
	http.ResponseWriter
	request *http.Request
	started chan struct{}
}

func (w *cutResponse) Write(body []byte) (int, error) {
	n, err := w.ResponseWriter.Write(body[:1])
	if err != nil {
		return n, err
	}
	w.ResponseWriter.(http.Flusher).Flush()
	close(w.started)
	<-w.request.Context().Done()
	return n, w.request.Context().Err()
}

func (n *cliNode) created(id adapterproto.MessageID) session {
	n.t.Helper()
	for range 4 {
		if err := n.h.Quiesce(); err != nil {
			n.t.Fatal(err)
		}
		n.h.Advance(engine.DefaultParams().PullInterval)
	}
	if err := n.h.Quiesce(); err != nil {
		n.t.Fatal(err)
	}
	status, err := n.h.Agent("creator").Status(n.h.Ctx(), fakeagent.StatusInput{MessageID: id})
	if err != nil || status.ControlReply == nil || status.ControlReply.Create == nil {
		n.t.Fatalf("creation: %+v, %v", status, err)
	}
	return n.session(status.ControlReply.Create.SessionEndpoint)
}

func (n *cliNode) session(id adapterproto.EndpointRef) session {
	n.t.Helper()
	scan, err := n.h.Records()
	if err != nil {
		n.t.Fatal(err)
	}
	for _, ep := range scan.Endpoints {
		if ep.ID.String() == string(id) && ep.Status == store.EndpointOpen {
			return session{base64.RawURLEncoding.EncodeToString(ep.Token), harness.NodeID().String() + "/" + ep.ID.String()}
		}
	}
	n.t.Fatal("created session has no open endpoint")
	return session{}
}

func (n *cliNode) command(s session, args ...string) *exec.Cmd {
	cmd := exec.Command(binary(n.t), args...)
	cmd.Dir = n.home
	cmd.Env = append(os.Environ(), "SIDERA_HOME="+n.home, "SIDERA_TOKEN="+s.token, "SIDERA_PROBE_TOKEN=")
	return cmd
}

func (n *cliNode) run(s session, args ...string) (int, string, string) {
	n.t.Helper()
	cmd := n.command(s, args...)
	var out, diag bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &diag
	err := cmd.Run()
	if err == nil {
		return 0, out.String(), diag.String()
	}
	if e, ok := err.(*exec.ExitError); ok {
		return e.ExitCode(), out.String(), diag.String()
	}
	n.t.Fatal(err)
	return -1, "", ""
}

func (n *cliNode) page(s session, args ...string) adapterproto.RecvPage {
	n.t.Helper()
	code, out, diag := n.run(s, append([]string{"recv"}, args...)...)
	var p adapterproto.RecvPage
	if err := json.Unmarshal([]byte(out), &p); err != nil || code != 0 || diag != "" {
		n.t.Fatalf("recv: exit %d, stdout %d bytes beginning %.200q, stderr %q, decode %v", code, len(out), out, diag, err)
	}
	return p
}

func TestRecvHandingOutExitsZero(t *testing.T) {
	n := newNode(t)
	sent, err := n.h.Agent("creator").Send(n.h.Ctx(), fakeagent.SendInput{To: []string{n.first.address}, Body: "read this exact text <&>"})
	if err != nil {
		t.Fatal(err)
	}
	if err := n.h.Quiesce(); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{nil, {"--json"}} {
		p := n.page(n.first, args...)
		if len(p.Messages) != 1 || p.Messages[0].ID != sent.MessageID || p.Messages[0].Body != "read this exact text <&>" {
			t.Fatalf("received %+v", p)
		}
	}
	code, out, diag := n.run(n.first, "ack", string(n.page(n.first).Lease))
	if code != 0 || out == "" || diag != "" {
		t.Fatalf("ack: %d %q %q", code, out, diag)
	}
	if p := n.page(n.first); len(p.Messages) != 0 {
		t.Fatalf("acknowledged message returned: %+v", p)
	}
}

func TestRecvPrintsTheExactPageBytes(t *testing.T) {
	home, err := os.MkdirTemp("", "cp-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	if err := os.Mkdir(filepath.Join(home, "run"), 0700); err != nil {
		t.Fatal(err)
	}
	addr, err := ipc.Locate(home, ipc.KindAgent)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := ipc.Listen(addr, held{}, ipc.Options{})
	if err != nil {
		t.Fatal(err)
	}
	// The spaces, tab and final newline belong to the received page.
	page := "{ \"lease\": \"0123456789abcdef0123456789abcdef\",\t\"messages\" : [], \"more\":0 }\n"
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/agent/recv" {
			t.Errorf("unexpected Agent route %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, page)
	})}
	ended := make(chan error, 1)
	go func() { ended <- server.Serve(listener) }()
	t.Cleanup(func() {
		server.Close()
		if err := <-ended; err != http.ErrServerClosed {
			t.Error(err)
		}
	})
	for _, mode := range []struct {
		name string
		args []string
	}{{"human", nil}, {"json", []string{"--json"}}} {
		t.Run(mode.name, func(t *testing.T) {
			code, out, diag := runHome(t, home, append([]string{"recv"}, mode.args...)...)
			if code != 0 || diag != "" || out != page {
				t.Fatalf("receive changed page bytes: exit %d, stdout %q, stderr %q; want %q", code, out, diag, page)
			}
		})
	}
}

func TestSendBodySourcesAndStableHumanOutput(t *testing.T) {
	n := newNode(t)
	file := filepath.Join(n.home, "body.txt")
	if err := os.WriteFile(file, []byte("from a file <&>"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name        string
		args        []string
		stdin, body string
	}{
		{"argument", []string{"--body", "--json"}, "", "--json"},
		{"file", []string{"--body-file", file}, "", "from a file <&>"},
		{"stdin", nil, "from standard input\nsecond line\n", "from standard input\nsecond line\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"--home", n.home, "send", "--to", n.h.Addr("creator")}, tc.args...)
			cmd := n.command(n.first, args...)
			cmd.Stdin = strings.NewReader(tc.stdin)
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("send: %v", err)
			}
			prefix := "sent "
			suffix := " to " + n.h.Addr("creator") + "\n"
			if !strings.HasPrefix(string(out), prefix) || !strings.HasSuffix(string(out), suffix) {
				t.Fatalf("send output %q", out)
			}
			if err := n.h.Quiesce(); err != nil {
				t.Fatal(err)
			}
			page, err := n.h.Agent("creator").Recv(n.h.Ctx(), fakeagent.RecvInput{})
			if err != nil || len(page.Page.Messages) != 1 || page.Page.Messages[0].Body != tc.body {
				t.Fatalf("body: %+v, %v", page, err)
			}
			if _, err := n.h.Agent("creator").Ack(n.h.Ctx(), fakeagent.AckInput{Lease: page.Page.Lease}); err != nil {
				t.Fatal(err)
			}
		})
	}
	// Windows process arguments replace invalid UTF-8; a file preserves the bytes.
	invalidFile := filepath.Join(n.home, "invalid-body.txt")
	if err := os.WriteFile(invalidFile, []byte("invalid\xff"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"empty argument", []string{"--body", ""}},
		{"invalid UTF-8 file", []string{"--body-file", invalidFile}},
	} {
		args := append([]string{"send", "--json", "--to", n.h.Addr("creator")}, tc.args...)
		code, out, diag := n.run(n.first, args...)
		if code != 3 || !strings.Contains(out, `"category":"invalid_argument"`) || diag != "" {
			t.Fatalf("%s: %d %q %q", tc.name, code, out, diag)
		}
	}
	code, _, _ := n.run(n.first, "send", "--to", n.h.Addr("creator"), "--body", "one", "--body-file", file)
	if code != 2 {
		t.Fatalf("conflicting body sources exit %d, want 2", code)
	}
	code, out, diag := n.run(n.first, "send", "--to", n.h.Addr("creator")+","+n.first.address, "--body", "one", "--json")
	if code != 3 || !strings.Contains(out, `"code":"single_recipient_only"`) || diag != "" {
		t.Fatalf("recipients: %d %q %q", code, out, diag)
	}
}

func (n *cliNode) createSession(key string) session {
	n.t.Helper()
	code, out, diag := n.run(n.first, "control", n.h.RuntimeAddr(), "session.create", "--preset", "default", "--key", key, "--json")
	var sent agentapi.ControlResult
	if err := json.Unmarshal([]byte(out), &sent); code != 0 || err != nil {
		n.t.Fatalf("create: %d %q %q %v", code, out, diag, err)
	}
	for range 4 {
		if err := n.h.Quiesce(); err != nil {
			n.t.Fatal(err)
		}
		n.h.Advance(engine.DefaultParams().PullInterval)
	}
	if err := n.h.Quiesce(); err != nil {
		n.t.Fatal(err)
	}
	code, out, diag = n.run(n.first, "status", string(sent.MessageID), "--json")
	var status adapterproto.MessageStatusResult
	if err := json.Unmarshal([]byte(out), &status); code != 0 || err != nil || status.ControlReply == nil || status.ControlReply.Create == nil || status.ControlReply.Create.Outcome != adapterproto.CreateReplyCreated {
		n.t.Fatalf("create status: %d %q %q %v", code, out, diag, err)
	}
	return n.session(status.ControlReply.Create.SessionEndpoint)
}

func TestTwoSessionsSameDirectoryDoNotCrossDeliver(t *testing.T) {
	n := newNode(t)
	a, b := n.createSession("session-a"), n.createSession("session-b")
	if a.address == b.address || a.token == b.token {
		t.Fatal("two creations reused session identity")
	}
	for _, tc := range []struct {
		s    session
		body string
	}{{a, "only session A"}, {b, "only session B"}} {
		code, out, diag := n.run(n.first, "send", "--to", tc.s.address, "--body", tc.body, "--json")
		if code != 0 {
			t.Fatalf("send: %d %q %q", code, out, diag)
		}
	}
	if err := n.h.Quiesce(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		s    session
		body string
	}{{a, "only session A"}, {b, "only session B"}} {
		p := n.page(tc.s)
		if len(p.Messages) != 1 || p.Messages[0].Body != tc.body {
			t.Fatalf("session %s received %+v", tc.s.address, p)
		}
	}
	code, out, _ := n.run(a, "me", "--json")
	var me agentapi.MeResult
	if err := json.Unmarshal([]byte(out), &me); err != nil || code != 0 || me.Address != a.address {
		t.Fatalf("me: %d %q %v", code, out, err)
	}
}

func TestCLIKilledDuringRecvRetryReturnsPossibleDuplicate(t *testing.T) {
	started := make(chan struct{})
	n := newNode(t, started)
	body := strings.Repeat("large message ", 4500)
	sent, err := n.h.Agent("creator").Send(n.h.Ctx(), fakeagent.SendInput{To: []string{n.first.address}, Body: body})
	if err != nil {
		t.Fatal(err)
	}
	if err := n.h.Quiesce(); err != nil {
		t.Fatal(err)
	}
	cmd := n.command(n.first, "recv")
	var out, diag bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &diag
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-started:
	case err := <-done:
		t.Fatalf("receive ended before its response was interrupted: %v, %s", err, &diag)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err == nil {
		t.Fatal("killed receive exited successfully")
	}
	p := n.page(n.first)
	if len(p.Messages) != 1 {
		t.Fatalf("retry returned %d messages, want 1", len(p.Messages))
	}
	found := false
	for _, m := range p.Messages {
		if m.ID == sent.MessageID && m.PossiblyDuplicate {
			found = true
		}
	}
	if !found {
		t.Fatal("retry did not return the unacknowledged message as possibly duplicate")
	}
}

func TestCommandUsageAndUnavailableDiagnostics(t *testing.T) {
	n := newNode(t)
	for _, args := range [][]string{{"me", "unexpected"}, {"recv", "--no-such-flag"}, {"identity"}} {
		for _, suffix := range [][]string{nil, {"--json"}} {
			code, out, diag := n.run(n.first, append(args, suffix...)...)
			if code != 2 || out != "" || !strings.HasPrefix(diag, "error: ") {
				t.Fatalf("usage %v: %d %q %q", args, code, out, diag)
			}
		}
	}
	code, out, diag := n.run(n.first, "recv", "--help")
	if code != 0 || diag != "" || !strings.Contains(out, "next.msg and next.part become --msg and --part") || !strings.Contains(out, "Example: sidera recv --wait 8") {
		t.Fatalf("help: %d %q %q", code, out, diag)
	}
	missing := filepath.Join(n.home, "absent")
	code, out, diag = n.run(n.first, "me", "--home", missing)
	if code != 9 || out != "" || !strings.Contains(diag, "sidera daemon") {
		t.Fatalf("missing daemon: %d %q %q", code, out, diag)
	}
	code, out, diag = n.run(n.first, "me", "--home", missing, "--json")
	if code != 9 || diag != "" || !strings.Contains(out, `"code":"daemon_not_running"`) {
		t.Fatalf("missing daemon json: %d %q %q", code, out, diag)
	}
}

func TestExplicitAgentFlagValuesReachValidation(t *testing.T) {
	n := newNode(t)
	for _, args := range [][]string{
		{"control", n.h.RuntimeAddr(), "session.create", "--preset", "default", "--body", ""},
		{"send", "--to", n.first.address, "--body", "text", "--content-type="},
		{"send", "--to", n.first.address, "--body", "text", "--key="},
		{"directory", "--limit", "0"},
		{"recv", "--msg=", "--part", "0"},
	} {
		code, out, diag := n.run(n.first, append(args, "--json")...)
		if code != 3 || diag != "" || !strings.Contains(out, `"category":"invalid_argument"`) {
			t.Fatalf("explicit values %v: %d %q %q", args, code, out, diag)
		}
	}
	code, out, diag := n.run(n.first, "control", n.h.RuntimeAddr(), "session.create", "--preset", "default", "--body", "initial message", "--json")
	var accepted agentapi.ControlResult
	if err := json.Unmarshal([]byte(out), &accepted); code != 0 || err != nil {
		t.Fatalf("initial control: %d %q %q %v", code, out, diag, err)
	}
}

func TestLargeMessagesViaCLIInChineseAndEnglish(t *testing.T) {
	for _, tc := range []struct{ name, body string }{{"English", strings.Repeat("E", 60*1024)}, {"Chinese", strings.Repeat("中", 20*1024)}} {
		t.Run(tc.name, func(t *testing.T) {
			n := newNode(t)
			want := map[adapterproto.MessageID]string{}
			for i := range 5 {
				cmd := n.command(n.first, "send", "--to", n.first.address, "--json")
				cmd.Stdin = strings.NewReader(tc.body)
				out, err := cmd.Output()
				if err != nil {
					t.Fatal(err)
				}
				var sent agentapi.SendResult
				if err := json.Unmarshal(out, &sent); err != nil {
					t.Fatal(err)
				}
				want[sent.MessageID] = tc.body
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
				var args []string
				if next != nil {
					args = []string{"--msg", string(next.Msg), "--part", fmt.Sprint(next.Part)}
				}
				p := n.page(n.first, args...)
				if len(p.Messages) == 0 {
					break
				}
				var items []string
				next = nil
				for _, m := range p.Messages {
					if _, ok := want[m.ID]; !ok {
						t.Fatalf("unexpected message %s", m.ID)
					}
					part := 1
					if m.Part != nil {
						part = m.Part.Index
						next = m.Part.Next
					}
					got[m.ID] += m.Body
					items = append(items, fmt.Sprintf("%s:%d", m.ID, part))
				}
				code, out, diag := n.run(n.first, append([]string{"ack", string(p.Lease), "--json"}, items...)...)
				var ack agentapi.AckResult
				if err := json.Unmarshal([]byte(out), &ack); code != 0 || err != nil || len(ack.Items) != len(items) {
					t.Fatalf("ack count differs from visible parts: %d %q %q %v", code, out, diag, err)
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
		})
	}
}

func TestAgentCommandsPreservePatchesAndFormatHumanResults(t *testing.T) {
	n := newNode(t)
	code, out, diag := n.run(n.first, "declare", "--paused=true", "--restrict-senders=true", "--accept-from", n.h.Addr("creator"), "--json")
	if code != 0 || diag != "" {
		t.Fatalf("declare: %d %q %q", code, out, diag)
	}
	code, out, diag = n.run(n.first, "declare", "--paused=false", "--hidden=true", "--json")
	var declaration agentapi.DeclareResult
	if err := json.Unmarshal([]byte(out), &declaration); err != nil || code != 0 || declaration.Declaration.Paused || !declaration.Declaration.RestrictSenders || !declaration.Declaration.HiddenFromDirectory || len(declaration.Declaration.AcceptFrom) != 1 {
		t.Fatalf("patch lost omitted fields: %d %q %q %v", code, out, diag, err)
	}
	code, out, _ = n.run(n.first, "me")
	if code != 0 || !strings.HasPrefix(out, "address "+n.first.address+"\nnode "+harness.NodeID().String()+"\nkind session\n") || !strings.Contains(out, "frontend cli wait_max_s=20 budget=27000 chars\n") {
		t.Fatalf("me: %d %q", code, out)
	}
	code, out, diag = n.run(n.first, "peers")
	if code != 0 || diag != "" || !strings.Contains(out, harness.NodeID().String()+"  true") {
		t.Fatalf("peers: %d %q %q", code, out, diag)
	}
	code, out, diag = n.run(n.first, "directory", "--limit", "1", "--json")
	var directory agentapi.DirectoryResult
	if err := json.Unmarshal([]byte(out), &directory); err != nil || code != 0 || len(directory.Endpoints) != 1 || directory.NextCursor == "" {
		t.Fatalf("directory: %d %q %q %v", code, out, diag, err)
	}
	code, out, diag = n.run(n.first, "declare", "--accept-from=", "--json")
	if err := json.Unmarshal([]byte(out), &declaration); err != nil || code != 0 || len(declaration.Declaration.AcceptFrom) != 0 {
		t.Fatalf("clear senders: %d %q %q %v", code, out, diag, err)
	}
}

func TestInitPrintsIdentityAndBackupInstructions(t *testing.T) {
	parent, err := os.MkdirTemp("", "ci-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(parent) })
	home := filepath.Join(parent, "node")
	cmd := exec.Command(binary(t), "init", "--home", home)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("init: %v %s", err, out)
	}
	if !strings.HasPrefix(string(out), "Back up your self-revocation outside this device.") || !strings.Contains(string(out), "home "+home+"\n") || !strings.Contains(string(out), "sidera identity export-revocation") {
		t.Fatalf("init output: %q", out)
	}
	for _, jsonMode := range []bool{false, true} {
		for _, tc := range []struct {
			command string
			home    string
			code    string
			action  string
			human   string
		}{
			{"init", home, "data_directory_already_initialized", "start it with `sidera daemon`", "daemon: this data directory is already initialized"},
			{"daemon", parent, "data_directory_not_initialized", "run `sidera init`", "daemon: this data directory is not initialized; run `sidera init`"},
		} {
			args := []string{"--home", tc.home, tc.command}
			if jsonMode {
				args = append(args, "--json")
			}
			cmd = exec.Command(binary(t), args...)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			if err := cmd.Run(); err == nil || cmd.ProcessState.ExitCode() != 1 {
				t.Fatalf("%v: exit = %v; want 1", args, err)
			}
			if jsonMode {
				var body apiwire.Body
				if err := json.Unmarshal(stdout.Bytes(), &body); err != nil || body.Error.Code != tc.code || !strings.Contains(body.Error.Message, tc.action) || stderr.Len() != 0 {
					t.Fatalf("%v: output %q %q: %v", args, &stdout, &stderr, err)
				}
			} else if stdout.Len() != 0 || !strings.HasPrefix(stderr.String(), "error: ") || !strings.Contains(stderr.String(), tc.human) {
				t.Fatalf("%v: output %q %q", args, &stdout, &stderr)
			}
		}
	}
	if runtime.GOOS != "windows" {
		longHome := filepath.Join(parent, strings.Repeat("x", 110))
		cmd = exec.Command(binary(t), "--json", "--home", longHome, "init")
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err == nil || cmd.ProcessState.ExitCode() != 1 {
			t.Fatalf("long home: exit = %v; want 1", err)
		}
		var body apiwire.Body
		if err := json.Unmarshal(stdout.Bytes(), &body); err != nil || body.Error.Code != "socket_path_too_long" || !strings.Contains(body.Error.Message, longHome) || !strings.Contains(body.Error.Message, "use a shorter data directory (--home or SIDERA_HOME)") || stderr.Len() != 0 {
			t.Fatalf("long home: output %q %q: %v", &stdout, &stderr, err)
		}
	}
}

func TestCLIAgentDriverExchangesMessagesThroughTheRealProgram(t *testing.T) {
	n := newNode(t)
	probes, err := n.h.Engine().ProbeTokens(n.h.Ctx(), harness.Instance)
	if err != nil {
		t.Fatal(err)
	}
	var probeToken string
	for _, p := range probes {
		if p.Frontend == adapterproto.FrontendCLI {
			probeToken = string(p.Token)
		}
	}
	if probeToken == "" {
		t.Fatal("the adapter issued no CLI probe")
	}
	a := cliagent.New(cliagent.Config{Binary: binary(t), Home: n.home, Dir: n.home, Token: n.first.token, ProbeToken: probeToken})
	probe, err := a.Probe(n.h.Ctx(), fakeagent.ProbeInput{Frontend: adapterproto.FrontendCLI})
	if err != nil || !probe.OK || probe.Frontend != adapterproto.FrontendCLI {
		t.Fatalf("probe: %+v %v", probe, err)
	}
	me, err := a.Me(n.h.Ctx())
	if err != nil || me.Address != n.first.address || me.Frontends[adapterproto.FrontendCLI].Budget.Limit != 27000 {
		t.Fatalf("me: %+v %v", me, err)
	}
	sent, err := a.Send(n.h.Ctx(), fakeagent.SendInput{To: []string{n.first.address}, Body: "driver message", IdemKey: "driver-send"})
	if err != nil {
		t.Fatal(err)
	}
	if err := n.h.Quiesce(); err != nil {
		t.Fatal(err)
	}
	p, err := a.Recv(n.h.Ctx(), fakeagent.RecvInput{})
	if err != nil || len(p.Page.Messages) != 1 || p.Page.Messages[0].ID != sent.MessageID || !bytes.HasSuffix(p.Raw, []byte("\n")) {
		t.Fatalf("recv: %+v %v", p, err)
	}
	if _, err := a.Recv(n.h.Ctx(), fakeagent.RecvInput{Msg: sent.MessageID, Part: 1, WaitS: 1}); fakeagent.CategoryOf(err) != fakeagent.CategoryInvalidArgument {
		t.Fatalf("part receive discarded its incompatible wait instead of returning the Agent error: %v", err)
	}
	ack, err := a.Ack(n.h.Ctx(), fakeagent.AckInput{Lease: p.Page.Lease})
	if err != nil || len(ack.Items) != 1 {
		t.Fatalf("ack: %+v %v", ack, err)
	}
	_, err = a.Send(n.h.Ctx(), fakeagent.SendInput{To: []string{n.first.address}, Body: ""})
	if fakeagent.CategoryOf(err) != fakeagent.CategoryInvalidArgument {
		t.Fatalf("empty send: %v", err)
	}
	if _, err := a.Send(n.h.Ctx(), fakeagent.SendInput{To: []string{n.first.address}, Body: "no", DiscloseRecipients: true}); err == nil {
		t.Fatal("unsupported disclosure was silently discarded")
	}
	if _, err := a.Recv(n.h.Ctx(), fakeagent.RecvInput{Frontend: adapterproto.FrontendMCP}); err == nil {
		t.Fatal("MCP receive was silently sent as CLI")
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
	if err := n.h.Quiesce(); err != nil {
		t.Fatal(err)
	}
	answer, err := n.h.Agent("creator").Recv(n.h.Ctx(), fakeagent.RecvInput{})
	if err != nil || len(answer.Page.Messages) != 1 || answer.Page.Messages[0].Body != "the answer" {
		t.Fatalf("reply body: %+v %v", answer, err)
	}
}

func (n *cliNode) management() {
	n.t.Helper()
	if err := os.Mkdir(filepath.Join(n.home, "manage"), 0700); err != nil {
		n.t.Fatal(err)
	}
	addr, err := ipc.Locate(n.home, ipc.KindManage)
	if err != nil {
		n.t.Fatal(err)
	}
	l, err := manageapi.Listen(addr, held{}, ipc.Options{})
	if err != nil {
		n.t.Fatal(err)
	}
	s, err := manageapi.New(manageapi.Config{Service: n.h.Engine(), Node: harness.NodeID(), Clock: n.h.Clock()})
	if err != nil {
		n.t.Fatal(err)
	}
	ended := make(chan error, 1)
	go func() { ended <- s.Serve(l) }()
	n.t.Cleanup(func() {
		s.Close()
		if err := <-ended; err != nil {
			n.t.Error(err)
		}
	})
}

func TestManagementVersionedSettingsAndPresets(t *testing.T) {
	n := newNode(t)
	n.management()
	code, out, diag := n.run(session{}, "node", "show", "--json")
	var before struct {
		Result struct {
			Version uint64 `json:"version"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &before); code != 0 || err != nil {
		t.Fatalf("node show: %d %q %q %v", code, out, diag, err)
	}
	code, out, diag = n.run(session{}, "node", "set", "node_queue_limit=20001", "reply_permission=false", "--json")
	var changed struct {
		OperationID string `json:"operation_id"`
		Replayed    bool   `json:"replayed"`
		Result      struct {
			Version  uint64         `json:"version"`
			Settings map[string]any `json:"settings"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &changed); code != 0 || err != nil || len(changed.OperationID) != 32 || changed.Replayed || changed.Result.Version != before.Result.Version+1 || changed.Result.Settings["node_queue_limit"] != float64(20001) || changed.Result.Settings["reply_permission"] != false {
		t.Fatalf("node set: %d %q %q %v", code, out, diag, err)
	}
	code, out, diag = n.run(session{}, "node", "set", "node_queue_limit=30000", "--expected-version", fmt.Sprint(before.Result.Version), "--json")
	if code != 6 || diag != "" || !strings.Contains(out, `"current_version"`) {
		t.Fatalf("stale version: %d %q %q", code, out, diag)
	}
	code, out, diag = n.run(session{}, "node", "set", "node_queue_limit=30000", "--expected-version=0", "--json")
	if code != 3 || !strings.Contains(out, "missing_expected_version") {
		t.Fatalf("explicit zero version: %d %q %q", code, out, diag)
	}
	preset := filepath.Join(n.home, "preset.json")
	if err := os.WriteFile(preset, []byte(`{"local_sources":"allow","description":"CLI preset"}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"preset", "add", "cli-preset", "--file", preset}, {"preset", "list"}, {"preset", "remove", "cli-preset"}} {
		code, out, diag = n.run(session{}, append(args, "--json")...)
		if code != 0 || diag != "" {
			t.Fatalf("preset %v: %d %q %q", args, code, out, diag)
		}
	}
}

func TestManagementUnknownOutcomeResendsOnlyTheFrozenRequest(t *testing.T) {
	for _, mode := range []struct {
		name      string
		json      bool
		loseAgain bool
	}{{"json replay", true, false}, {"json unknown", true, true}, {"human replay", false, false}, {"human unknown", false, true}} {
		t.Run(mode.name, func(t *testing.T) {
			n := newNode(t)
			if err := os.Mkdir(filepath.Join(n.home, "manage"), 0700); err != nil {
				t.Fatal(err)
			}
			addr, err := ipc.Locate(n.home, ipc.KindManage)
			if err != nil {
				t.Fatal(err)
			}
			l, err := ipc.Listen(addr, held{}, ipc.Options{})
			if err != nil {
				t.Fatal(err)
			}
			var mu sync.Mutex
			var changes [][]byte
			reads := 0
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/node.show") {
					mu.Lock()
					reads++
					mu.Unlock()
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprintln(w, `{"result":{"version":41}}`)
					return
				}
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				changes = append(changes, body)
				attempt := len(changes)
				mu.Unlock()
				if attempt == 1 || mode.loseAgain {
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					conn.Close()
					return
				}
				var request struct {
					OperationID string `json:"operation_id"`
				}
				if err := json.Unmarshal(body, &request); err != nil {
					t.Error(err)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, "{\"operation_id\":%q,\"replayed\":true,\"result\":{\"version\":42}}\n", request.OperationID)
			})
			server := &http.Server{Handler: handler}
			done := make(chan error, 1)
			go func() { done <- server.Serve(l) }()
			t.Cleanup(func() {
				server.Close()
				if err := <-done; err != http.ErrServerClosed {
					t.Error(err)
				}
			})
			args := []string{"node", "set", "node_queue_limit=20001"}
			if mode.json {
				args = append(args, "--json")
			}
			code, out, diag := n.run(session{}, args...)
			if mode.json && diag != "" {
				t.Fatalf("JSON diagnostics: %q", diag)
			}
			mu.Lock()
			defer mu.Unlock()
			if reads != 1 || len(changes) != 2 || !bytes.Equal(changes[0], changes[1]) {
				t.Fatalf("read %d versions, sent %d changes; request changed: %v", reads, len(changes), len(changes) == 2 && !bytes.Equal(changes[0], changes[1]))
			}
			var request struct {
				OperationID     string `json:"operation_id"`
				ExpectedVersion uint64 `json:"expected_version"`
			}
			if err := json.Unmarshal(changes[0], &request); err != nil || len(request.OperationID) != 32 || request.ExpectedVersion != 41 {
				t.Fatalf("request envelope: %v", err)
			}
			if mode.loseAgain {
				failure, category := diag, "outcome_unknown/"
				if mode.json {
					failure, category = out, `"category":"outcome_unknown"`
				} else if out != "" {
					t.Fatalf("human failure wrote stdout: %q", out)
				}
				if code != 10 || !strings.Contains(failure, request.OperationID) || !strings.Contains(failure, category) {
					t.Fatalf("unknown again: %d %q %q", code, out, diag)
				}
			} else {
				replayed := "replayed true"
				if mode.json {
					replayed = `"replayed":true`
				}
				if code != 0 || !strings.Contains(out, replayed) || !strings.Contains(out, request.OperationID) {
					t.Fatalf("replayed response metadata: %d %q", code, out)
				}
			}
		})
	}
}

func TestManagementEndpointSettingsAndClosing(t *testing.T) {
	n := newNode(t)
	n.management()
	code, out, diag := n.run(session{}, "endpoint", "set", n.h.Addr("creator"), "max_queue_s=1800", "backlog_limit=200", "--json")
	if code != 0 || diag != "" || !strings.Contains(out, `"max_queue_s":1800`) || !strings.Contains(out, `"backlog_limit":200`) {
		t.Fatalf("endpoint settings: %d %q %q", code, out, diag)
	}
	code, out, diag = n.run(session{}, "endpoint", "list", "--instance", harness.Instance, "--json")
	if code != 0 || !strings.Contains(out, n.first.address) {
		t.Fatalf("endpoint list: %d %q %q", code, out, diag)
	}
	code, out, diag = n.run(session{}, "endpoint", "close", n.first.address, "--json")
	if code != 0 || !strings.Contains(out, `"operation_id"`) {
		t.Fatalf("endpoint close: %d %q %q", code, out, diag)
	}
	if err := n.h.Quiesce(); err != nil {
		t.Fatal(err)
	}
	code, out, diag = n.run(n.first, "me", "--json")
	if code != 4 {
		t.Fatalf("closed credential: %d %q %q", code, out, diag)
	}
	code, out, diag = n.run(session{}, "alerts", "ack", strings.Repeat("a", 32), "--json")
	if code != 5 || !strings.Contains(out, `"code":"alert_unknown"`) {
		t.Fatalf("unknown alert: %d %q %q", code, out, diag)
	}
}

func runHome(t *testing.T, home string, args ...string) (int, string, string) {
	t.Helper()
	cmd := exec.Command(binary(t), append([]string{"--home", home}, args...)...)
	cmd.Dir = home
	var out, diag bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &diag
	err := cmd.Run()
	if err == nil {
		return 0, out.String(), diag.String()
	}
	if exit, ok := err.(*exec.ExitError); ok {
		return exit.ExitCode(), out.String(), diag.String()
	}
	t.Fatal(err)
	return -1, "", ""
}

func startDaemon(t *testing.T) (string, <-chan error) {
	t.Helper()
	home, err := os.MkdirTemp("", "cd-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	code, out, diag := runHome(t, home, "init", "--json")
	if code != 0 {
		t.Fatalf("init: %d %q %q", code, out, diag)
	}
	cmd := exec.Command(binary(t), "--home", home, "daemon")
	cmd.Dir = home
	var log bytes.Buffer
	cmd.Stderr = &log
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	ended := make(chan error, 1)
	done := make(chan error, 1)
	go func() { err := cmd.Wait(); ended <- err; done <- err; close(done) }()
	t.Cleanup(func() { _ = cmd.Process.Kill(); <-ended })
	client, err := manageapi.NewClient(manageapi.ClientConfig{Home: home})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		raw, err := client.Call(ctx, "status", manageapi.Request{})
		if err == nil && strings.Contains(string(raw), `"state":"running"`) {
			return home, done
		}
		select {
		case err := <-done:
			t.Fatalf("daemon ended before serving: %v %s", err, &log)
		case <-ctx.Done():
			t.Fatalf("daemon did not serve: %v", ctx.Err())
		case <-tick.C:
		}
	}
}

func TestManagementDaemonLifecycleAndAdapterCommands(t *testing.T) {
	home, done := startDaemon(t)
	for _, args := range [][]string{{"daemon"}, {"daemon", "--json"}} {
		code, out, diag := runHome(t, home, args...)
		if code != 1 {
			t.Fatalf("second daemon: %d %q %q", code, out, diag)
		}
		if len(args) == 2 {
			var body apiwire.Body
			if err := json.Unmarshal([]byte(out), &body); err != nil || body.Error.Code != "daemon_already_running" || !strings.Contains(body.Error.Message, "wait for it to finish or use another data directory") || diag != "" {
				t.Fatalf("second daemon: %d %q %q: %v", code, out, diag, err)
			}
		} else if out != "" || !strings.Contains(diag, "daemon: a daemon is already running on this data directory") {
			t.Fatalf("second daemon: %d %q %q", code, out, diag)
		}
	}
	code, out, diag := runHome(t, home, "status")
	if code != 0 || diag != "" || !strings.Contains(out, "daemon.state running") || !strings.HasPrefix(out, "The statement that can retire this node") {
		t.Fatalf("status: %d %q %q", code, out, diag)
	}
	// This executable is deliberately not an adapter: enabling it leaves an
	// offline instance, while its installation and persisted settings are real.
	manifest := harness.AdapterManifest(10)
	manifest.ID = "cli-adapter"
	if err := manifest.WritePackage(filepath.Join(home, "package"), binary(t)); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"adapter", "install", "package"},
		{"adapter", "list"},
		{"adapter", "enable", "cli-adapter", "--instance", "cli-instance"},
		{"adapter", "disable", "cli-instance"},
		{"endpoint", "list"},
		{"alerts", "list", "--all"},
		{"clock", "clear-rollback"},
	} {
		code, out, diag = runHome(t, home, append(args, "--json")...)
		if code != 0 || diag != "" || !strings.Contains(out, `"result"`) {
			t.Fatalf("management %v: %d %q %q", args, code, out, diag)
		}
	}
	code, out, diag = runHome(t, home, "adapter", "enable", "cli-adapter", "--instance", "cli-instance")
	if code != 0 || diag != "" || !strings.Contains(out, "enabled, probing in progress; see sidera adapter list") || !strings.Contains(out, "operation_id ") {
		t.Fatalf("human adapter enable: %d %q %q", code, out, diag)
	}
	code, out, diag = runHome(t, home, "daemon", "stop", "--json")
	if code != 0 || diag != "" || !strings.Contains(out, `"operation_id"`) || !strings.Contains(out, `"stopping":true`) {
		t.Fatalf("daemon stop: %d %q %q", code, out, diag)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("orderly daemon exit: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("daemon did not finish its orderly stop")
	}
}

func TestIdentityExportBackupAndRetirement(t *testing.T) {
	home, done := startDaemon(t)
	code, armor, diag := runHome(t, home, "identity", "export-revocation")
	if code != 0 || !strings.HasPrefix(armor, "-----BEGIN SIDERA SELF-REVOCATION-----\n") || !strings.Contains(diag, "identity confirm-backup") {
		t.Fatalf("human export: %d %q %q", code, armor, diag)
	}
	code, out, diag := runHome(t, home, "identity", "export-revocation", "--json")
	var export struct {
		Result struct {
			Armored string `json:"armored"`
			Digest  string `json:"digest"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &export); code != 0 || err != nil || export.Result.Armored != armor || len(export.Result.Digest) != 64 || !strings.Contains(diag, export.Result.Digest) {
		t.Fatalf("JSON export: %d %q %q %v", code, out, diag, err)
	}
	code, out, diag = runHome(t, home, "identity", "confirm-backup", export.Result.Digest, "--json")
	if code != 0 || diag != "" || !strings.Contains(out, `"confirmed_at"`) {
		t.Fatalf("confirm backup: %d %q %q", code, out, diag)
	}
	code, out, diag = runHome(t, home, "status", "--json")
	var status struct {
		Result struct {
			Node struct {
				ID string `json:"id"`
			} `json:"node"`
			Reminders []any `json:"reminders"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &status); code != 0 || err != nil || len(status.Result.Reminders) != 0 {
		t.Fatalf("confirmed status: %d %q %q %v", code, out, diag, err)
	}
	code, out, diag = runHome(t, home, "identity", "retire", "--node", status.Result.Node.ID, "--json")
	if code != 2 || out != "" || !strings.Contains(diag, "--yes") {
		t.Fatalf("retirement without confirmation: %d %q %q", code, out, diag)
	}
	code, out, diag = runHome(t, home, "identity", "retire", "--node", status.Result.Node.ID, "--yes", "--json")
	if code != 0 || diag != "" || !strings.Contains(out, `"retired":true`) || !strings.Contains(out, `"operation_id"`) {
		t.Fatalf("retirement: %d %q %q", code, out, diag)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("retired daemon exit: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("retired daemon did not stop")
	}
	code, offline, diag := runHome(t, home, "identity", "export-revocation", "--json")
	var stopped struct {
		Result struct {
			Armored string `json:"armored"`
			Digest  string `json:"digest"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(offline), &stopped); code != 0 || err != nil || stopped != export || !strings.Contains(diag, export.Result.Digest) {
		t.Fatalf("offline export: %d %q %q %v", code, offline, diag, err)
	}
}

func TestIdentityExportDoesNotFallBackAfterPeerCheckFailure(t *testing.T) {
	home, done := startDaemon(t)
	if code, out, diag := runHome(t, home, "daemon", "stop", "--json"); code != 0 {
		t.Fatalf("stop: %d %q %q", code, out, diag)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("daemon did not stop")
	}
	addr, err := ipc.Locate(home, ipc.KindManage)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := ipc.Listen(addr, held{}, ipc.Options{})
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(503)
		fmt.Fprintln(w, `{"error":{"category":"unavailable","code":"peer_check_failed","message":"the peer could not be verified"}}`)
	})}
	ended := make(chan error, 1)
	go func() { ended <- server.Serve(listener) }()
	t.Cleanup(func() {
		server.Close()
		if err := <-ended; err != http.ErrServerClosed {
			t.Error(err)
		}
	})
	code, out, diag := runHome(t, home, "identity", "export-revocation", "--json")
	if code != 9 || diag != "" || !strings.Contains(out, `"code":"peer_check_failed"`) || strings.Contains(out, "SELF-REVOCATION") {
		t.Fatalf("unverified peer export: %d %q %q", code, out, diag)
	}
}

func TestRevocationExportWritesVerifiedRecords(t *testing.T) {
	home, done := startDaemon(t)
	source, err := os.ReadFile(filepath.Join(home, "keys", "self-revocation.rec"))
	if err != nil {
		t.Fatal(err)
	}
	code, full, diag := runHome(t, home, "identity", "export-revocation", "--json")
	var exported struct {
		Result manageapi.RevocationExportResult `json:"result"`
	}
	if err := json.Unmarshal([]byte(full), &exported); code != 0 || err != nil || exported.Result.Armored == "" || len(exported.Result.Digest) != 64 {
		t.Fatalf("complete export: exit %d, decode %v, stderr %q", code, err, diag)
	}
	online := filepath.Join(home, "online-json.rec")
	for _, phase := range []string{"online", "offline"} {
		if phase == "offline" {
			if code, out, diag := runHome(t, home, "daemon", "stop", "--json"); code != 0 {
				t.Fatalf("stop: %d %q %q", code, out, diag)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(30 * time.Second):
				t.Fatal("daemon did not stop")
			}
		}
		for _, mode := range []string{"json", "human"} {
			t.Run(phase+"/"+mode, func(t *testing.T) {
				path := filepath.Join(home, phase+"-"+mode+".rec")
				args := []string{"identity", "export-revocation", "--out", path}
				if mode == "json" {
					args = append(args, "--json")
				}
				code, out, diag := runHome(t, home, args...)
				if code != 0 || !strings.Contains(diag, exported.Result.Digest) {
					t.Fatalf("file export: exit %d, stderr %q", code, diag)
				}
				if mode == "json" {
					var response struct {
						Result map[string]string `json:"result"`
					}
					if err := json.Unmarshal([]byte(out), &response); err != nil {
						t.Fatal(err)
					}
					if _, present := response.Result["armored"]; present || strings.Contains(out, `"armored"`) || strings.Contains(out, "SELF-REVOCATION") || strings.Contains(out, exported.Result.Armored) {
						t.Fatal("file export leaked the armored statement to stdout")
					}
					if len(response.Result) != 2 || response.Result["digest"] != exported.Result.Digest || response.Result["file"] != path {
						t.Fatalf("file export must report only its digest and file path: %v", response.Result)
					}
				} else if out != exported.Result.Digest+"\n" {
					t.Fatalf("human file export did not print only its digest: %q", out)
				}
				data, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(data, source) {
					t.Fatalf("export differs from the source record: %v", err)
				}
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
					t.Fatalf("export mode %o, want 600", info.Mode().Perm())
				}
			})
		}
	}
	code, out, diag := runHome(t, home, "identity", "export-revocation", "--out", online, "--json")
	after, err := os.ReadFile(online)
	if code != 1 || err != nil || !bytes.Equal(source, after) || strings.Contains(out, `"result"`) {
		t.Fatalf("existing file protection: exit %d, read %v, changed %v, stdout %q, stderr %q", code, err, !bytes.Equal(source, after), out, diag)
	}
	if !strings.Contains(out, filepath.Base(online)) {
		t.Fatalf("file failure lost its cause: %q", out)
	}
	code, out, diag = runHome(t, home, "identity", "export-revocation", "--out=", "--json")
	if code != 1 || strings.Contains(out, `"result"`) {
		t.Fatalf("empty output path disappeared: %d %q %q", code, out, diag)
	}
}

func TestRevocationExportRejectsAnInvalidBackupBeforeCreatingAFile(t *testing.T) {
	for _, mismatch := range []bool{false, true} {
		t.Run(fmt.Sprint(mismatch), func(t *testing.T) {
			home, err := os.MkdirTemp("", "ce-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.RemoveAll(home) })
			if code, out, diag := runHome(t, home, "init", "--json"); code != 0 {
				t.Fatalf("init: %d %q %q", code, out, diag)
			}
			code, out, diag := runHome(t, home, "identity", "export-revocation", "--json")
			if code != 0 {
				t.Fatalf("seed export: %d %q %q", code, out, diag)
			}
			var response struct {
				Result struct {
					Armored string `json:"armored"`
					Digest  string `json:"digest"`
				} `json:"result"`
			}
			if err := json.Unmarshal([]byte(out), &response); err != nil {
				t.Fatal(err)
			}
			if mismatch {
				response.Result.Digest = strings.Repeat("0", 64)
			} else {
				response.Result.Armored = "not a verified self-revocation"
			}
			addr, err := ipc.Locate(home, ipc.KindManage)
			if err != nil {
				t.Fatal(err)
			}
			l, err := ipc.Listen(addr, held{}, ipc.Options{})
			if err != nil {
				t.Fatal(err)
			}
			server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(w).Encode(response); err != nil {
					t.Error(err)
				}
			})}
			ended := make(chan error, 1)
			go func() { ended <- server.Serve(l) }()
			t.Cleanup(func() {
				server.Close()
				if err := <-ended; err != http.ErrServerClosed {
					t.Error(err)
				}
			})
			target := filepath.Join(home, "invalid.rec")
			code, out, diag = runHome(t, home, "identity", "export-revocation", "--out", target, "--json")
			if code != 10 || !strings.Contains(out, `"category":"outcome_unknown"`) || strings.Contains(out, `"result"`) {
				t.Fatalf("invalid export: %d %q %q", code, out, diag)
			}
			if _, err := os.Stat(target); !os.IsNotExist(err) {
				t.Fatalf("invalid backup created a file: %v", err)
			}
		})
	}
}

func TestRevocationExportCleansItsNewFileWhenOutputReturnsAnError(t *testing.T) {
	for _, stream := range []string{"stdout", "stderr"} {
		t.Run(stream, func(t *testing.T) {
			home, err := os.MkdirTemp("", "co-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.RemoveAll(home) })
			if code, out, diag := runHome(t, home, "init", "--json"); code != 0 {
				t.Fatalf("init: %d %q %q", code, out, diag)
			}
			path := filepath.Join(home, "read-only-stream")
			if err := os.WriteFile(path, nil, 0600); err != nil {
				t.Fatal(err)
			}
			readOnly, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer readOnly.Close()
			target := filepath.Join(home, "incomplete.rec")
			cmd := exec.Command(binary(t), "--home", home, "identity", "export-revocation", "--out", target, "--json")
			var out, diag bytes.Buffer
			cmd.Stdout = &out
			cmd.Stderr = &diag
			// Passing the read-only handle to the child makes its own Write return
			// an error, rather than a parent-side pipe copy or a SIGPIPE race.
			if stream == "stdout" {
				cmd.Stdout = readOnly
			} else {
				cmd.Stderr = readOnly
			}
			err = cmd.Run()
			exit, ok := err.(*exec.ExitError)
			if !ok || exit.ExitCode() != 1 {
				t.Fatalf("output failure: %v, stdout %q, stderr %q", err, &out, &diag)
			}
			if _, err := os.Stat(target); !os.IsNotExist(err) {
				t.Fatalf("failed export left its new file: %v", err)
			}
			if _, err := os.Stat(filepath.Join(home, "keys", "self-revocation.rec")); err != nil {
				t.Fatalf("source statement changed: %v", err)
			}
		})
	}
}
