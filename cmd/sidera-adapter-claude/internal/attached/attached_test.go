package attached

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/cmd/sidera-adapter-claude/internal/record"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

const session = "6ee62271-42c1-4558-89cf-0e32c50e0c1b"

func read(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseRegistry(t *testing.T) {
	busy, err := ParseRegistry(read(t, "registry-busy.json"))
	if err != nil {
		t.Fatal(err)
	}
	if busy.PID != 4242 || busy.SessionID != session || busy.Cwd != "/work/dir" || !busy.Interactive ||
		busy.Status != StatusBusy || !busy.Status.Active() || busy.StatusAt != 1793617545691 || !busy.Messaging {
		t.Fatalf("parsed %+v", busy)
	}
	shell, err := ParseRegistry(read(t, "registry-shell.json"))
	if err != nil || shell.Status != StatusShell || shell.Status.Active() {
		t.Fatalf("a session with a background shell has ended its turn: %+v, %v", shell, err)
	}
	old, err := ParseRegistry(read(t, "registry-old.json"))
	if err != nil || old.Messaging {
		t.Fatalf("a runtime that lists no messaging: %+v, %v", old, err)
	}

	// The runtime writes the process start in UTC, as `ps -o lstart` prints it.
	start := time.Date(2026, 11, 2, 5, 34, 31, 0, time.UTC).Unix()
	for _, tc := range []struct {
		name string
		unix int64
		want bool
	}{{"the same second", start, true}, {"a second off", start + 1, true}, {"hours later: the number was reused", start + 7200, false}} {
		if got := busy.SameProcess(tc.unix); got != tc.want {
			t.Errorf("%s: SameProcess = %v", tc.name, got)
		}
	}
}

func TestParseRegistryRefusesAFormItDoesNotRead(t *testing.T) {
	good := string(read(t, "registry-busy.json"))
	for name, doc := range map[string]string{
		"not JSON":                        "pid=4242",
		"no status":                       strings.Replace(good, `"status":"busy",`, "", 1),
		"no session id":                   strings.Replace(good, `"sessionId"`, `"session"`, 1),
		"a process start in another form": strings.Replace(good, "Mon Nov  2 05:34:31 2026", "2026-11-02T05:34:31Z", 1),
		"no time of the status":           strings.Replace(good, `"statusUpdatedAt"`, `"since"`, 1),
	} {
		if _, err := ParseRegistry([]byte(doc)); !errors.Is(err, ErrRegistryForm) {
			t.Errorf("%s: err = %v, want ErrRegistryForm", name, err)
		}
	}
	// A status the adapter does not know is read, not refused: it counts as no turn running.
	r, err := ParseRegistry([]byte(strings.Replace(good, `"busy"`, `"compacting"`, 1)))
	if err != nil || r.Status != StatusUnknown || r.Status.Active() || r.StatusName != "compacting" {
		t.Fatalf("an unknown status: %+v, %v", r, err)
	}
}

func TestParseWake(t *testing.T) {
	w, err := ParseWake(adapterproto.WakeInfo{WakeSocket: "/s", WakeToken: "tok", WakePID: "4242", "OTHER": "x"})
	if err != nil || w != (Wake{Socket: "/s", Token: "tok", PID: 4242}) {
		t.Fatalf("%+v, %v", w, err)
	}
	if w, err := ParseWake(nil); err != nil || w != (Wake{}) {
		t.Fatalf("no wake: %+v, %v", w, err)
	}
	_, err = ParseWake(adapterproto.WakeInfo{WakePID: "not-a-number-secret"})
	if err == nil || !strings.Contains(err.Error(), WakePID) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("an invalid key is named by the key, not the value: %v", err)
	}
}

func TestWakeLines(t *testing.T) {
	const uuid = "11111111-1111-4111-8111-111111111111"
	got, err := Wake{Socket: "/s", Token: "tok-1"}.Lines(uuid, "line one\n\"two\"")
	if err != nil {
		t.Fatal(err)
	}
	want := `{"type":"auth","token":"tok-1"}` + "\n" +
		`{"type":"user","message":{"role":"user","content":"line one\n\"two\""},"uuid":"` + uuid + `"}` + "\n"
	if string(got) != want {
		t.Fatalf("lines\n got %q\nwant %q", got, want)
	}
	got, _ = Wake{Socket: "/s"}.Lines(uuid, "hi")
	if strings.Contains(string(got), "auth") || bytes.Count(got, []byte("\n")) != 1 {
		t.Fatalf("without a token only the message is written: %q", got)
	}
}

// A wake never prints its secrets, whatever the verb or the log handler.
func TestWakeNeverPrintsItsSecrets(t *testing.T) {
	w := Wake{Socket: "/run/secret-socket", Token: "secret-token", PID: 4242}
	var logs bytes.Buffer
	for _, h := range []slog.Handler{slog.NewTextHandler(&logs, nil), slog.NewJSONHandler(&logs, nil)} {
		slog.New(h).Info("wake", "wake", w)
	}
	out := logs.String() + fmt.Sprintf("%v %+v %#v %s", w, w, w, w) + fmt.Sprint(w, &w, []Wake{w}, map[string]Wake{"k": w})
	if strings.Contains(out, "secret") || strings.Contains(out, "4242") {
		t.Fatalf("a wake leaked: %s", out)
	}
}

// The recorded sample: the markers a follower gets from the transcript.
func TestTranscriptSample(t *testing.T) {
	ix := record.NewIndex(session, "11111111-1111-4111-8111-111111111111")
	ix.Track()
	ix.Feed(read(t, "transcript.jsonl"))
	got := ix.Markers()
	want := []record.Marker{
		{Kind: record.MarkTurnStart, UUID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"},
		{Kind: record.MarkTurnEnd},
		{Kind: record.MarkPrompt, UUID: "11111111-1111-4111-8111-111111111111"},
		{Kind: record.MarkTurnEnd},
	}
	if len(got) != len(want) {
		t.Fatalf("markers %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("markers %v, want %v", got, want)
		}
	}
	if !ix.Recognized() {
		t.Fatal("the sample's user lines are in the form the lookup reads")
	}
}
