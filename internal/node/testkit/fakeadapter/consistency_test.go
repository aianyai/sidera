package fakeadapter_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/node/clock/clocktest"
	"github.com/aianyai/sidera/internal/node/testkit/fakeadapter"
	"github.com/aianyai/sidera/internal/node/testkit/fakeruntime"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/manifest"
	"github.com/aianyai/sidera/sdk/go/peer"
)

// These are the consistency tests of the fake adapter. An integration test is
// only as good as the adapter it is written against: if the fake spoke a
// dialect of the protocol that the schema rejects, every test built on it would
// prove the daemon against that dialect. So the oracle is the part of the
// repository the fake did not write: the JSON Schema and the golden messages
// under protocol/, and the daemon's own admission rules.

const probeToken = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

// scenario drives one adapter through every method of the protocol, in both
// directions, and returns the rig for inspection.
func runFullScenario(t *testing.T, serverRequests bool) *rig {
	t.Helper()
	r := newRig(t, rigOptions{manifest: func(o *fakeadapter.ManifestOptions) { o.ServerRequests = serverRequests }})
	ctx := r.ctx

	r.initialize()
	r.mustCall(adapterproto.MethodPing, nil, nil)
	if err := r.knobs.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	r.start(nil, []adapterproto.DirectAttach{{Endpoint: "ep-chat:alice", Key: "chat:alice"}})
	var probe adapterproto.ProbeResult
	r.mustCall(adapterproto.MethodAdapterProbe, adapterproto.ProbeParams{ToolProbes: []adapterproto.ToolProbeRequest{
		{Preset: "default", Frontend: adapterproto.FrontendCLI, Token: probeToken, Env: map[string]string{"SIDERA_TOKEN": probeToken}},
	}}, &probe)

	// Availability: a condition, then the empty snapshot.
	if err := r.knobs.Availability(ctx, []adapterproto.Availability{{Condition: adapterproto.ConditionRuntimeAuth, SinceMS: testStart.UnixMilli()}}); err != nil {
		t.Fatalf("Availability: %v", err)
	}
	if err := r.knobs.Availability(ctx, []adapterproto.Availability{}); err != nil {
		t.Fatalf("Availability: %v", err)
	}
	r.waitNotifications(adapterproto.MethodAdapterAvailability, 2)

	// A request that is cancelled while the adapter holds it.
	r.knobs.QueueCreate(fakeadapter.Outcome{Kind: fakeadapter.OutcomeHold})
	cancelCtx, cancel := context.WithCancel(ctx)
	held := make(chan error, 1)
	go func() {
		var res json.RawMessage
		held <- r.daemon.Call(cancelCtx, adapterproto.MethodSessionCreate, r.createParams(90, "sess-held"), &res)
	}()
	r.waitFor("a held create", func() bool { return len(r.knobs.Held()) == 1 })
	cancel()
	if err := <-held; err == nil {
		t.Fatal("a cancelled create returned a result to its caller")
	}
	r.waitFor("the held create to end", func() bool { return len(r.knobs.Held()) == 0 })

	// A session, a prompt that starts a turn in which the agent reads.
	created := r.createSession(1, "sess-1")
	if created.Outcome != adapterproto.CreateCreated {
		t.Fatalf("create outcome %q", created.Outcome)
	}
	ref := string(created.NativeRef)
	if err := r.runtime.SetPlan(ref, fakeruntime.TurnPlan{Actions: []fakeruntime.TurnAction{{Kind: fakeruntime.ActionRecv, Args: map[string]string{"frontend": "cli"}}}}); err != nil {
		t.Fatal(err)
	}
	var delivered adapterproto.PromptDeliverResult
	r.mustCall(adapterproto.MethodPromptDeliver, adapterproto.PromptDeliverParams{Endpoint: "sess-1", Attempt: attemptID(2), Text: "work on it"}, &delivered)
	r.waitNotifications(adapterproto.MethodTurnStarted, 1)
	r.waitFor("recv.observed", func() bool { return len(r.observedCalls()) == 1 })
	r.waitFor("an idle runtime", r.runtime.Idle)
	if err := r.runtime.EndTurn(ref, fakeruntime.FinalCompleted); err != nil {
		t.Fatal(err)
	}
	r.waitNotifications(adapterproto.MethodTurnEnded, 1)

	// A prompt that is only accepted, and made durable afterwards.
	if err := r.runtime.SetPlan(ref, fakeruntime.TurnPlan{ManualStart: true}); err != nil {
		t.Fatal(err)
	}
	r.knobs.QueuePrompt(fakeadapter.Outcome{Kind: fakeadapter.OutcomeAccepted})
	r.mustCall(adapterproto.MethodPromptDeliver, adapterproto.PromptDeliverParams{Endpoint: "sess-1", Attempt: attemptID(3), Text: "and this"}, &delivered)
	if delivered.Stage != adapterproto.PromptAccepted {
		t.Fatalf("stage %q, want accepted", delivered.Stage)
	}
	if err := r.knobs.Persist(ctx, "sess-1", attemptID(3)); err != nil {
		t.Fatal(err)
	}
	r.waitNotifications(adapterproto.MethodPromptPersisted, 1)

	// Reconciliation: an attempt that is in the record and one that never was.
	var rec adapterproto.ReconcileResult
	r.mustCall(adapterproto.MethodPromptReconcile, adapterproto.ReconcileParams{Endpoint: "sess-1", Attempt: attemptID(3)}, &rec)
	var absent adapterproto.ReconcileResult
	r.mustCall(adapterproto.MethodPromptReconcile, adapterproto.ReconcileParams{Endpoint: "sess-1", Attempt: attemptID(99)}, &absent)

	// Interrupt a running turn, then interrupt again with none running.
	if err := r.runtime.StartTurn(ref); err != nil {
		t.Fatal(err)
	}
	r.waitNotifications(adapterproto.MethodTurnStarted, 2)
	var intr adapterproto.InterruptResult
	r.mustCall(adapterproto.MethodSessionInterrupt, adapterproto.InterruptParams{Endpoint: "sess-1"}, &intr)
	r.waitNotifications(adapterproto.MethodTurnEnded, 2)
	r.mustCall(adapterproto.MethodSessionInterrupt, adapterproto.InterruptParams{Endpoint: "sess-1"}, &intr)

	if serverRequests {
		if err := r.knobs.NativeRequest(ctx, "sess-1", "item/tool/call", adapterproto.DispositionPreset); err != nil {
			t.Fatal(err)
		}
		r.waitNotifications(adapterproto.MethodNativeRequestHandled, 1)
	}

	var closed adapterproto.CloseResult
	r.mustCall(adapterproto.MethodSessionClose, adapterproto.CloseParams{Endpoint: "sess-1"}, &closed)

	// Direct endpoints: everything the chat bridge does.
	reg, err := r.knobs.Register(ctx, adapterproto.RegisterParams{Key: "chat:alice", DisplayName: "Alice"})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := r.daemon.Notify(ctx, adapterproto.MethodEndpointPending, adapterproto.PendingParams{Endpoint: reg.Endpoint, Count: 1}); err != nil {
		t.Fatal(err)
	}
	page, err := r.knobs.Recv(ctx, adapterproto.RecvParams{Endpoint: reg.Endpoint})
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if _, err := r.knobs.Ack(ctx, adapterproto.AckParams{Endpoint: reg.Endpoint, Lease: page.Page.Lease, Items: []adapterproto.PartRef{{MessageID: "m2", Part: 1}}}); err != nil {
		t.Fatalf("Ack: %v", err)
	}
	sent, err := r.knobs.Send(ctx, adapterproto.SendParams{Endpoint: reg.Endpoint, To: []adapterproto.Address{"bob/chat"}, ContentType: "text/plain", Body: "hi"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if _, err := r.knobs.MessageStatus(ctx, adapterproto.MessageStatusParams{Endpoint: reg.Endpoint, MessageID: sent.MessageID}); err != nil {
		t.Fatalf("MessageStatus: %v", err)
	}
	if err := r.knobs.Unregister(ctx, adapterproto.UnregisterParams{Endpoint: reg.Endpoint}); err != nil {
		t.Fatalf("Unregister: %v", err)
	}

	r.mustCall(adapterproto.MethodShutdown, nil, nil)
	return r
}

// validateLog checks every line of the rig's log and returns the methods seen,
// by direction, together with the number of lines checked.
func validateLog(t *testing.T, r *rig) (toAdapter, fromAdapter map[string]int, lines int) {
	t.Helper()
	return validateFrames(t, r, false)
}

// validateFrames is validateLog; with adapterOnly it checks only what the adapter
// wrote, which is what the fake is answerable for: a test may send the adapter a
// request that is wrong on purpose.
func validateFrames(t *testing.T, r *rig, adapterOnly bool) (toAdapter, fromAdapter map[string]int, lines int) {
	t.Helper()
	toAdapter, fromAdapter = map[string]int{}, map[string]int{}
	pending := map[bool]map[string]string{true: {}, false: {}} // by direction of the request, id -> method
	for _, ll := range r.log.snapshot() {
		var f wireFrame
		if err := json.Unmarshal(ll.line, &f); err != nil {
			t.Fatalf("unparsable line %q: %v", ll.line, err)
		}
		method := ""
		switch {
		case f.isRequest() || f.isNotification():
			seen := fromAdapter
			if ll.toAdapter {
				seen = toAdapter
			}
			seen[f.Method]++
			if f.isRequest() {
				pending[ll.toAdapter][string(f.ID)] = f.Method
			}
		default:
			// A response travels the opposite way to its request.
			method = pending[!ll.toAdapter][string(f.ID)]
		}
		if adapterOnly && ll.toAdapter {
			continue
		}
		if err := checkFrame(t, ll.line, method); err != nil {
			t.Errorf("%s line %s is not valid protocol: %v\n%s", direction(ll.toAdapter), f.Method, err, ll.line)
		}
		lines++
	}
	return toAdapter, fromAdapter, lines
}

func direction(toAdapter bool) string {
	if toAdapter {
		return "daemon to adapter"
	}
	return "adapter to daemon"
}

// expectedExchange is what runFullScenario puts on the wire, counted from its
// script, line by line, and not from what the adapter happened to write: the
// requests and notifications each way, by method. Responses carry no method.
func expectedExchange(serverRequests bool) (toAdapter, fromAdapter map[string]int) {
	toAdapter = map[string]int{
		adapterproto.MethodInitialize:       1, // the handshake
		adapterproto.MethodPing:             1, // the daemon's ping
		adapterproto.MethodAdapterStart:     1,
		adapterproto.MethodAdapterProbe:     1,
		adapterproto.MethodSessionCreate:    2, // the one that is cancelled while held, and sess-1
		adapterproto.MethodCancelRequest:    1, // the cancel of the held create
		adapterproto.MethodPromptDeliver:    2, // attempt 2 starts a turn, attempt 3 is only accepted
		adapterproto.MethodPromptReconcile:  2, // an attempt in the record and one that never was
		adapterproto.MethodSessionInterrupt: 2, // a running turn, then none
		adapterproto.MethodSessionClose:     1,
		adapterproto.MethodEndpointPending:  1,
		adapterproto.MethodShutdown:         1,
	}
	fromAdapter = map[string]int{
		adapterproto.MethodPing:                  1, // the Ping knob
		adapterproto.MethodAdapterAvailability:   2, // a condition, then the empty snapshot
		adapterproto.MethodCheckpointPut:         2, // the attempt's and the session's, both of sess-1
		adapterproto.MethodTurnStarted:           2, // the prompt's turn and the scripted one
		adapterproto.MethodTurnEnded:             2, // ended by the script, ended by the interrupt
		adapterproto.MethodRecvObserved:          1, // the page the first turn read
		adapterproto.MethodPromptPersisted:       1,
		adapterproto.MethodEndpointRegister:      1,
		adapterproto.MethodEndpointRecv:          1,
		adapterproto.MethodEndpointAck:           1,
		adapterproto.MethodEndpointSend:          1,
		adapterproto.MethodEndpointMessageStatus: 1,
		adapterproto.MethodEndpointUnregister:    1,
	}
	if serverRequests {
		fromAdapter[adapterproto.MethodNativeRequestHandled] = 1
	}
	return toAdapter, fromAdapter
}

// TestFakeAdapterOutputMatchesProtocolSchemas is the consistency test of the
// fake adapter: every line it writes, answer or request or notification, is
// accepted by the JSON Schema and by the Go types of the protocol, and across
// the scenario it speaks every method it must, in both directions.
func TestFakeAdapterOutputMatchesProtocolSchemas(t *testing.T) {
	for _, serverRequests := range []bool{false, true} {
		name := "server requests not declared"
		if serverRequests {
			name = "server requests declared"
		}
		t.Run(name, func(t *testing.T) {
			r := runFullScenario(t, serverRequests)
			toAdapter, fromAdapter, _ := validateLog(t, r)
			wantTo, wantFrom := expectedExchange(serverRequests)
			if !reflect.DeepEqual(toAdapter, wantTo) {
				t.Errorf("requests and notifications to the adapter, by method:\n got %v\nwant %v", toAdapter, wantTo)
			}
			if !reflect.DeepEqual(fromAdapter, wantFrom) {
				t.Errorf("requests and notifications from the adapter, by method:\n got %v\nwant %v", fromAdapter, wantFrom)
			}

			// Every method the daemon may call, the daemon called, and the fake
			// answered; the cancel notification is both directions' method.
			for _, m := range adapterproto.Methods() {
				if m.Direction.CalledBy(true) && toAdapter[m.Name] == 0 {
					t.Errorf("the scenario never sent %s to the adapter", m.Name)
				}
			}
			// Every method an adapter of the kinds it declares must be able to
			// call, the fake called. recv.observed and native_request.handled
			// are the two gated ones.
			for _, m := range adapterproto.Methods() {
				if !m.Direction.CalledBy(false) {
					continue
				}
				required := adapterreg.RequirementOf(adapterreg.KindRuntime, m.Name) == adapterreg.Required ||
					adapterreg.RequirementOf(adapterreg.KindDirect, m.Name) == adapterreg.Required
				switch m.Name {
				case adapterproto.MethodNativeRequestHandled:
					required = serverRequests
				case adapterproto.MethodCancelRequest:
					required = false // an adapter that never calls the daemon slowly has nothing to cancel
				case adapterproto.MethodRecvObserved, adapterproto.MethodCheckpointPut,
					adapterproto.MethodEndpointUnregister, adapterproto.MethodEndpointMessageStatus:
					required = true // declared and optional: the fake uses them all
				}
				if required && fromAdapter[m.Name] == 0 {
					t.Errorf("the adapter never sent %s", m.Name)
				}
			}
			if !serverRequests && fromAdapter[adapterproto.MethodNativeRequestHandled] != 0 {
				t.Errorf("an adapter that did not declare server requests sent native_request.handled")
			}
		})
	}
}

// TestFakeAdapterDeclarationIsAdmittedByTheDaemonRules runs what the daemon
// runs when it meets an adapter: the manifest against its schema and parser, the
// declared capabilities and the manifest against Admit, and the handshake
// against the manifest. It also pins the declaration the fake makes.
func TestFakeAdapterDeclarationIsAdmittedByTheDaemonRules(t *testing.T) {
	r := newRig(t, rigOptions{})
	res := r.initialize()

	raw, err := r.opts.Encode()
	if err != nil {
		t.Fatalf("encode manifest: %v", err)
	}
	doc, err := parseJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := schemas(t).accepts(t, "manifest.schema.json", doc); err != nil {
		t.Fatalf("the manifest schema rejects the fake adapter's manifest: %v\n%s", err, raw)
	}
	mf, err := manifest.Parse(raw)
	if err != nil {
		t.Fatalf("manifest.Parse: %v", err)
	}
	adm, err := adapterreg.Admit(res.Capabilities, mf)
	if err != nil {
		t.Fatalf("adapterreg.Admit refuses the fake adapter: %v", err)
	}
	if err := adapterreg.CheckHandshake(adapterproto.SupportedRange(), res, mf); err != nil {
		t.Fatalf("the handshake does not match the manifest: %v", err)
	}

	rows := []struct {
		name string
		got  any
		want any
	}{
		{"it declares a runtime and direct endpoints", [2]bool{adm.Declares(adapterreg.KindRuntime), adm.Declares(adapterreg.KindDirect)}, [2]bool{true, true}},
		{"the runtime declares both front ends", res.Capabilities.Runtime.Frontends, []adapterproto.Frontend{adapterproto.FrontendCLI, adapterproto.FrontendMCP}},
		{"the runtime declares interrupt", res.Capabilities.Runtime.Interrupt, true},
		{"the runtime declares acknowledging on behalf", res.Capabilities.Runtime.AckOnBehalf, true},
		{"the runtime declares no server requests by default", res.Capabilities.Runtime.ServerRequests, false},
		{"the cli wait limit differs from the default and from the protocol cap", adm.RecvWait(adapterproto.FrontendCLI), 20 * time.Second},
		{"the mcp wait limit differs from the default and from the protocol cap", adm.RecvWait(adapterproto.FrontendMCP), 45 * time.Second},
		{"the limits are neither the default nor the cap", adm.RecvWait(adapterproto.FrontendCLI) != adapterreg.DefaultRecvWait &&
			adm.RecvWait(adapterproto.FrontendMCP) != adapterreg.DefaultRecvWait &&
			adm.RecvWait(adapterproto.FrontendCLI) != manifest.MaxRecvWaitSeconds*time.Second &&
			adm.RecvWait(adapterproto.FrontendMCP) != manifest.MaxRecvWaitSeconds*time.Second, true},
		{"the direct endpoint limit is the manifest's option", res.Capabilities.Direct.MaxEndpoints, r.opts.MaxDirectEndpoints},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			g, _ := json.Marshal(row.got)
			w, _ := json.Marshal(row.want)
			if !bytes.Equal(g, w) {
				t.Fatalf("got %s, want %s", g, w)
			}
		})
	}
}

// goldenRequests returns every request of the golden messages that the daemon
// sends to an adapter, with its path.
func goldenRequests(t *testing.T) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	root := filepath.Join(schemaDir, "examples")
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasPrefix(d.Name(), "request-") {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var f wireFrame
		if err := json.Unmarshal(raw, &f); err != nil {
			return err
		}
		info, ok := adapterproto.Lookup(f.Method)
		if !ok || !info.Direction.CalledBy(true) || info.Notification {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		out[filepath.ToSlash(rel)] = raw
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestFakeAdapterAnswersEveryGoldenRequest sends each golden request of the
// daemon to a fresh fake adapter, after a handshake, and requires an answer the
// schema accepts. The golden requests come from the repository's own examples,
// so they are the daemon's side of the conversation as the protocol authors
// wrote it. Only the lock path of the handshake is replaced: the golden one
// names a directory of another machine.
func TestFakeAdapterAnswersEveryGoldenRequest(t *testing.T) {
	golden := goldenRequests(t)
	if len(golden) < 14 {
		t.Fatalf("only %d golden requests found: the examples moved or the filter is wrong", len(golden))
	}
	paths := make([]string, 0, len(golden))
	for p := range golden {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	outcomes := map[string]int{}
	methods := map[string]bool{}
	for _, p := range paths {
		raw := golden[p]
		t.Run(p, func(t *testing.T) {
			var f wireFrame
			_ = json.Unmarshal(raw, &f)
			// The golden files are indented; a line of the protocol is one line.
			line := compact(raw)
			if f.Method == adapterproto.MethodInitialize {
				line = withLockPath(t, raw, filepath.Join(t.TempDir(), "instance.lock"))
			}
			s := startRaw(t)
			if f.Method != adapterproto.MethodInitialize {
				s.handshake(t)
			}
			switch f.Method {
			case adapterproto.MethodPromptDeliver, adapterproto.MethodPromptReconcile,
				adapterproto.MethodSessionInterrupt, adapterproto.MethodSessionClose:
				// These name the session the golden create request makes.
				create := compact(golden["session.create/request-basic.json"])
				resp := s.exchange(t, create)
				if err := checkFrame(t, resp, adapterproto.MethodSessionCreate); err != nil || !bytes.Contains(resp, []byte(`"created"`)) {
					t.Fatalf("the golden create that precedes %s did not create a session: %v\n%s", f.Method, err, resp)
				}
			}
			resp := s.exchange(t, line)
			var rf wireFrame
			if err := json.Unmarshal(resp, &rf); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(compact(rf.ID), compact(f.ID)) {
				t.Fatalf("the answer carries id %s, the request id %s", rf.ID, f.ID)
			}
			if err := checkFrame(t, resp, f.Method); err != nil {
				t.Fatalf("the answer to the golden request is not valid protocol: %v\n%s", err, resp)
			}
			if len(rf.Error) > 0 {
				outcomes["error"]++
			} else {
				outcomes["result"]++
			}
			methods[f.Method] = true
		})
	}
	// Coverage: every request method of the daemon was answered at least once,
	// and the fake produced real results, not only errors.
	for _, m := range adapterproto.Methods() {
		if m.Direction.CalledBy(true) && !m.Notification && !methods[m.Name] {
			t.Errorf("no golden request of %s was answered", m.Name)
		}
	}
	if outcomes["result"] < 12 {
		t.Errorf("only %d golden requests got a result (%d got an error): the fake refuses too much", outcomes["result"], outcomes["error"])
	}
	t.Logf("golden requests: %d results, %d errors", outcomes["result"], outcomes["error"])
}

func compact(b json.RawMessage) []byte {
	var buf bytes.Buffer
	if err := json.Compact(&buf, b); err != nil {
		return b
	}
	return buf.Bytes()
}

func withLockPath(t *testing.T, raw []byte, path string) []byte {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	doc["params"].(map[string]any)["instance"].(map[string]any)["lock_path"] = path
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// rawAdapter is a fake adapter served over a pair of pipes and driven line by
// line, with no peer on the daemon side, so the golden bytes reach it unchanged.
type rawAdapter struct {
	in    io.WriteCloser
	lines chan rawLine // every line the adapter writes, in order, until its output ends
}

type rawLine struct {
	line []byte
	err  error
}

func startRaw(t *testing.T) *rawAdapter {
	t.Helper()
	leakGuard(t)
	clk := clocktest.NewFake(testStart)
	mem := ledger.NewMemory()
	rt, err := fakeruntime.New(fakeruntime.Config{Now: clk.Now, Ledgers: mem, Dir: t.TempDir(), Seed: 3})
	if err != nil {
		t.Fatal(err)
	}
	w, err := mem.Open("adapter/fake", 0, clk.Now)
	if err != nil {
		t.Fatal(err)
	}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- fakeadapter.Serve(ctx, inR, outW, fakeadapter.Config{
			InstanceID: "fake", Now: clk.Now, Ledger: w, Runtime: rt,
			Manifest: fakeadapter.DefaultManifestOptions(), Knobs: fakeadapter.NewKnobs(),
		})
	}()
	t.Cleanup(func() {
		cancel()
		_ = inW.Close()
		_ = outW.Close()
		select {
		case <-done:
		case <-time.After(waitLimit):
			t.Errorf("Serve did not return")
		}
		_ = rt.Close()
	})
	a := &rawAdapter{in: inW, lines: make(chan rawLine, 64)}
	go func() {
		br := bufio.NewReader(outR)
		for {
			l, err := readLine(br)
			a.lines <- rawLine{l, err}
			if err != nil {
				return
			}
		}
	}()
	return a
}

// exchange writes one line and returns the line that answers it. Lines the adapter
// writes meanwhile are what a daemon would receive: each is validated, a
// notification is dropped, and a request is answered with an empty result, which
// is all the requests of a creation need (checkpoint.put).
func (a *rawAdapter) exchange(t *testing.T, line []byte) []byte {
	t.Helper()
	var sent wireFrame
	if err := json.Unmarshal(line, &sent); err != nil {
		t.Fatalf("the line to send is not JSON: %v", err)
	}
	if _, err := a.in.Write(append(bytes.Clone(line), '\n')); err != nil {
		t.Fatalf("write: %v", err)
	}
	deadline := time.After(waitLimit)
	for {
		select {
		case rd := <-a.lines:
			if rd.err != nil {
				t.Fatalf("read: %v", rd.err)
			}
			var f wireFrame
			if err := json.Unmarshal(rd.line, &f); err != nil {
				t.Fatalf("the adapter wrote a line that is not JSON: %v", err)
			}
			switch {
			case f.isNotification():
				if err := checkFrame(t, rd.line, ""); err != nil {
					t.Fatalf("a notification of the adapter is not valid protocol: %v\n%s", err, rd.line)
				}
			case f.isRequest():
				if err := checkFrame(t, rd.line, ""); err != nil {
					t.Fatalf("a request of the adapter is not valid protocol: %v\n%s", err, rd.line)
				}
				reply := []byte(`{"jsonrpc":"2.0","id":` + string(f.ID) + `,"result":{}}`)
				if _, err := a.in.Write(append(reply, '\n')); err != nil {
					t.Fatalf("write: %v", err)
				}
			default:
				return rd.line
			}
		case <-deadline:
			t.Fatalf("no answer within %v to %s", waitLimit, line)
		}
	}
}

func (a *rawAdapter) handshake(t *testing.T) {
	t.Helper()
	req, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": "handshake", "method": "initialize",
		"params": map[string]any{
			"protocol": map[string]int{"min": 1, "max": 1}, "daemon": map[string]string{"version": "0.1.0"},
			"instance":     map[string]string{"id": "fake", "lock_path": filepath.Join(t.TempDir(), "instance.lock")},
			"capabilities": map[string]any{},
		},
	})
	resp := a.exchange(t, req)
	var f wireFrame
	if err := json.Unmarshal(resp, &f); err != nil || len(f.Result) == 0 {
		t.Fatalf("handshake failed: %s", resp)
	}
}

var _ = peer.CodeInternalError
