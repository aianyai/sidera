//go:build realcodex

package server_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/appserver"
	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/serverreq"
)

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return v
}

// methodsOf lists the method names of a request or notification union of the
// schema.
func methodsOf(t *testing.T, path string) []string {
	t.Helper()
	var out []string
	for _, alt := range readJSON(t, path)["oneOf"].([]any) {
		props := alt.(map[string]any)["properties"].(map[string]any)
		out = append(out, props["method"].(map[string]any)["enum"].([]any)[0].(string))
	}
	slices.Sort(out)
	return out
}

// The adapter is written against one release of an interface that is not stable.
// The schema the installed server generates must still hold what it relies on: the
// requests the server may send are exactly the ones answered, and every method and
// member used exists.
func TestSchemaGuard(t *testing.T) {
	program := requireRealCodex(t)
	dir := t.TempDir()
	if out, err := exec.Command(program, "app-server", "generate-json-schema", "--out", dir).CombinedOutput(); err != nil {
		t.Fatalf("generate-json-schema: %v\n%s", err, out)
	}

	got := methodsOf(t, filepath.Join(dir, "ServerRequest.json"))
	want := slices.Clone(serverreq.Known())
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("the server's requests are\n  %v\nthe adapter answers\n  %v\nA request the adapter does not know is refused: decide what it should be.", got, want)
	}

	requests := methodsOf(t, filepath.Join(dir, "ClientRequest.json"))
	for _, m := range []string{
		appserver.MethodInitialize, appserver.MethodAccountRead, appserver.MethodThreadStart, appserver.MethodThreadResume,
		appserver.MethodThreadSetName, appserver.MethodThreadArchive, appserver.MethodThreadUnsubscribe,
		appserver.MethodThreadTurnsList, appserver.MethodThreadShell, appserver.MethodTurnStart, appserver.MethodCommandExec,
	} {
		if !slices.Contains(requests, m) {
			t.Errorf("the server has no request %s", m)
		}
	}
	notifications := methodsOf(t, filepath.Join(dir, "ServerNotification.json"))
	for _, m := range []string{
		appserver.NotifyTurnStarted, appserver.NotifyTurnCompleted, appserver.NotifyItemCompleted,
		appserver.NotifyThreadStatus, appserver.NotifyMCPStatus,
	} {
		if !slices.Contains(notifications, m) {
			t.Errorf("the server has no notification %s", m)
		}
	}
	for _, m := range appserver.QuietNotifications {
		if !slices.Contains(notifications, m) {
			t.Errorf("the adapter opts out of %s, which the server does not send", m)
		}
	}

	for file, members := range map[string][]string{
		"TurnStartParams":          {"clientUserMessageId", "threadId", "input", "cwd", "approvalPolicy", "sandboxPolicy", "model", "effort"},
		"ThreadStartParams":        {"config", "ephemeral", "cwd", "approvalPolicy", "sandbox", "model", "developerInstructions"},
		"ThreadResumeParams":       {"config", "excludeTurns", "threadId", "cwd", "approvalPolicy", "sandbox", "model", "developerInstructions"},
		"ThreadTurnsListParams":    {"threadId", "itemsView", "sortDirection", "limit", "cursor"},
		"CommandExecParams":        {"command", "cwd", "env", "sandboxPolicy", "timeoutMs"},
		"ThreadShellCommandParams": {"threadId", "command", "timeoutMs"},
		"InitializeParams":         {"clientInfo", "capabilities"},
	} {
		path := filepath.Join(dir, "v2", file+".json")
		if file == "InitializeParams" {
			path = filepath.Join(dir, "v1", file+".json")
		}
		props, _ := readJSON(t, path)["properties"].(map[string]any)
		for _, m := range members {
			if _, ok := props[m]; !ok {
				t.Errorf("%s has no member %s", file, m)
			}
		}
	}
	// The permission profile a thread runs under is read from the answer to
	// thread/start and thread/resume. The server sends it to every client, but only the
	// experimental schema lists it, so that is the one the member is looked up in.
	experimental := t.TempDir()
	if out, err := exec.Command(program, "app-server", "generate-json-schema", "--experimental", "--out", experimental).CombinedOutput(); err != nil {
		t.Fatalf("generate-json-schema --experimental: %v\n%s", err, out)
	}
	// The queue of a thread's prompts is experimental: its methods and members are
	// in the experimental schema only.
	queueRequests := methodsOf(t, filepath.Join(experimental, "ClientRequest.json"))
	for _, m := range []string{appserver.MethodThreadRead, appserver.MethodThreadQueueAdd, appserver.MethodThreadQueueList, appserver.MethodThreadQueueDelete} {
		if !slices.Contains(queueRequests, m) {
			t.Errorf("the server has no request %s", m)
		}
	}
	for file, members := range map[string][]string{
		"ThreadQueueAddParams":    {"threadId", "clientUserMessageId", "input"},
		"ThreadQueueListParams":   {"threadId", "limit"},
		"ThreadQueueDeleteParams": {"threadId", "queuedSubmissionId"},
		"ThreadReadParams":        {"threadId", "includeTurns"},
	} {
		props, _ := readJSON(t, filepath.Join(experimental, "v2", file+".json"))["properties"].(map[string]any)
		for _, m := range members {
			if _, ok := props[m]; !ok {
				t.Errorf("%s has no member %s", file, m)
			}
		}
	}
	if props, _ := readJSON(t, filepath.Join(experimental, "v2", "ThreadQueueDeleteResponse.json"))["properties"].(map[string]any); props["deleted"] == nil {
		t.Error("ThreadQueueDeleteResponse has no member deleted, which says whether a withdrawn prompt was still queued")
	}
	for _, file := range []string{"ThreadQueueAddResponse", "ThreadQueueListResponse"} {
		raw, err := os.ReadFile(filepath.Join(experimental, "v2", file+".json"))
		if err != nil || !strings.Contains(string(raw), `"clientUserMessageId"`) {
			t.Errorf("%s no longer carries clientUserMessageId, which a queued prompt is found by (%v)", file, err)
		}
	}
	// The end of a turn another process runs is read from its completion time; a
	// thread that takes no queued prompt is told by these members.
	for file, members := range map[string][]string{"Turn": {"startedAt", "completedAt"}, "Thread": {"ephemeral", "parentThreadId", "source"}} {
		defs, _ := readJSON(t, filepath.Join(experimental, "v2", "ThreadReadResponse.json"))["definitions"].(map[string]any)
		if file == "Turn" {
			defs, _ = readJSON(t, filepath.Join(experimental, "v2", "ThreadTurnsListResponse.json"))["definitions"].(map[string]any)
		}
		def, _ := defs[file].(map[string]any)
		props, _ := def["properties"].(map[string]any)
		for _, m := range members {
			if _, ok := props[m]; !ok {
				t.Errorf("%s has no member %s", file, m)
			}
		}
	}
	for _, file := range []string{"ThreadStartResponse", "ThreadResumeResponse"} {
		props, _ := readJSON(t, filepath.Join(experimental, "v2", file+".json"))["properties"].(map[string]any)
		if _, ok := props["activePermissionProfile"]; !ok {
			t.Errorf("%s has no member activePermissionProfile, which the check that a thread is protected reads", file)
		}
	}
	// A prompt is found again by the client id the server keeps on its record.
	raw, err := os.ReadFile(filepath.Join(dir, "v2", "ItemCompletedNotification.json"))
	if err != nil || !strings.Contains(string(raw), `"clientId"`) {
		t.Errorf("a userMessage item no longer carries clientId (%v)", err)
	}
	raw, err = os.ReadFile(filepath.Join(dir, "v2", "ThreadTurnsListResponse.json"))
	if err != nil || !strings.Contains(string(raw), `"clientId"`) || !strings.Contains(string(raw), `"itemsView"`) {
		t.Errorf("the turns of a thread no longer carry clientId and itemsView, which the reading of the record requires (%v)", err)
	}
}
