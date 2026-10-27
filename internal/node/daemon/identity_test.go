package daemon_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/daemon"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/keys"
	"github.com/aianyai/sidera/internal/record"
)

// opID is the operation identifier of the nth request of a test: 32 lower-case
// hexadecimal digits that are not all zero.
func opID(n int) string {
	const digits = "0123456789abcdef"
	return strings.Repeat("0", 29) + string(digits[(n>>8)&15]) + string(digits[(n>>4)&15]) + string(digits[n&15])
}

// certificateOnDisk reads the certificate of the node from keys/network.cert with
// the record package alone, so that what a test expects of a renewal does not
// come from the code that renews.
func certificateOnDisk(t testing.TB, home string) (serial uint64, notAfter time.Time) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, "keys", "network.cert"))
	if err != nil {
		t.Fatalf("reading the certificate: %v", err)
	}
	return certificateOf(t, data)
}

// certificateOf reads the certificate from its record bytes.
func certificateOf(t testing.TB, data []byte) (serial uint64, notAfter time.Time) {
	t.Helper()
	rec, err := record.Decode(data, record.DefaultMaxInput)
	if err != nil {
		t.Fatalf("decoding the certificate: %v", err)
	}
	v, err := record.Verify(record.NetworkKeyCertificateDescriptor, rec)
	if err != nil {
		t.Fatalf("verifying the certificate: %v", err)
	}
	return v.Payload().Serial, v.Payload().NotAfter
}

// settled waits until the engine has nothing left to do at the current time of the
// test clock.
func settled(t testing.TB, d *daemon.Daemon) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := d.Engine().Quiesce(ctx, engine.Expect{}); err != nil {
		t.Fatalf("the engine did not settle: %v", err)
	}
}

// answerOf reads the body of a successful management answer.
func answerOf(t testing.TB, status int, body []byte) manageAnswer {
	t.Helper()
	var a manageAnswer
	if status != 200 {
		t.Fatalf("answered %d: %s", status, body)
	}
	if err := json.Unmarshal(body, &a); err != nil {
		t.Fatalf("the answer is not JSON: %v\n%s", err, body)
	}
	return a
}

// A node whose identity the owner has retired refuses to run for good: the
// retirement is made through the management interface, the daemon answers and
// stops, and the start that follows is refused at the step that loads the keys and
// the revocations, before the clock is judged or anything is signed. The data
// directory is left as it was: the certificate is not renewed, and the lock is
// free, so that the refusal is the same on every later try.
func TestRefusesToRunUnderRetiredIdentity(t *testing.T) {
	noLeaks(t)
	c, _ := initialized(t)
	node := nodeOf(t, c)
	certBefore, err := os.ReadFile(filepath.Join(c.Home, "keys", "network.cert"))
	if err != nil {
		t.Fatal(err)
	}
	d := started(t, c)

	// The owner names another node (the data directory of another node, or a typing
	// mistake): nothing is retired and the daemon runs on.
	status, body := manage(t, c.Home, "identity.retire", map[string]any{
		"operation_id": opID(1), "params": map[string]any{"node": nodeOfOther(t).String()},
	})
	if status != 400 || !strings.Contains(string(body), `"node_mismatch"`) {
		t.Errorf("retiring another node: %d %s; want 400 node_mismatch", status, body)
	}
	if got := tryManage(c.Home, "status"); got != 200 {
		t.Fatalf("the daemon does not answer after a retirement that was refused: %d", got)
	}
	revocations, err := os.ReadDir(filepath.Join(c.Home, "config", "revocations"))
	if err != nil || len(revocations) != 0 {
		t.Fatalf("config/revocations after a refused retirement: %v, %v", revocations, err)
	}

	status, body = manage(t, c.Home, "identity.retire", map[string]any{
		"operation_id": opID(2), "params": map[string]any{"node": node.String()},
	})
	got := answerOf(t, status, body)
	var result struct {
		Node     string `json:"node"`
		Retired  bool   `json:"retired"`
		Stopping bool   `json:"stopping"`
	}
	if err := json.Unmarshal(got.Result, &result); err != nil || result.Node != node.String() || !result.Retired || !result.Stopping {
		t.Fatalf("the result of the retirement is %s (%v)", got.Result, err)
	}

	// The daemon was asked to stop by the answer, and ends in an orderly way.
	select {
	case <-d.Done():
	case <-time.After(30 * time.Second):
		t.Fatal("the daemon did not stop after the retirement")
	}
	if err := d.Wait(); err != nil {
		t.Fatalf("the daemon ended with %v after the retirement; want an orderly stop", err)
	}

	// The retirement is on disk as a verified revocation of this node, under the
	// directory only a revocation goes to.
	revocations, err = os.ReadDir(filepath.Join(c.Home, "config", "revocations"))
	if err != nil || len(revocations) != 1 {
		t.Fatalf("config/revocations after the retirement: %v, %v; want the one statement", revocations, err)
	}

	for try := 1; try <= 2; try++ {
		_, err := daemon.Start(context.Background(), c)
		if !errors.Is(err, keys.ErrIdentityRetired) {
			t.Fatalf("start %d over a retired identity: %v; want keys.ErrIdentityRetired", try, err)
		}
		if errors.Is(err, daemon.ErrAlreadyRunning) {
			t.Errorf("start %d: the lock of the stopped daemon is still held", try)
		}
		if daemon.ExitCode(err) == 0 || daemon.ExitCode(err) == daemon.ExitFatal {
			t.Errorf("start %d: exit code %d, want a refusal that is neither success nor a failure of the disk", try, daemon.ExitCode(err))
		}
	}
	st := steps(logLines(t, c.Home))
	var begun []string
	for _, s := range st {
		if strings.HasPrefix(s, "startup ") && strings.HasSuffix(s, " begin") {
			begun = append(begun, s)
		}
	}
	// The last start that was refused began step 3 and never step 4.
	if n := len(begun); n < 3 || begun[n-1] != "startup 3 begin" {
		t.Errorf("the last start began %v, want it to stop at step 3", begun)
	}
	certAfter, err := os.ReadFile(filepath.Join(c.Home, "keys", "network.cert"))
	if err != nil {
		t.Fatal(err)
	}
	if string(certBefore) != string(certAfter) {
		t.Error("the certificate changed although the identity was retired before it could be renewed")
	}
}

// The loop runs until the daemon is down, so between the retirement of the identity
// and the end of the daemon the hourly look at the certificate still comes round.
// A certificate that is due then is not signed: the node never starts again, and a
// certificate signed in the name of a retired identity is what the retirement says
// no longer happens. The shutdown is held in its second step, with the loop running,
// so that the look is made after the commit of the retirement and before the stop.
func TestACertificateThatFallsDueAfterTheRetirementIsNotSigned(t *testing.T) {
	noLeaks(t)
	c, clk := initialized(t)
	node := nodeOf(t, c)
	serial, _ := certificateOnDisk(t, c.Home)

	holding, release := make(chan struct{}), make(chan struct{})
	var holdOnce, releaseOnce sync.Once
	c.Attach.AgentServe = func(context.Context, *daemon.Env) error { return nil }
	c.Attach.StopInterfaces = func(context.Context) error {
		holdOnce.Do(func() { close(holding) })
		<-release
		return nil
	}
	// The probe runs after the certificate check that falls due at the same time:
	// the schedule runs what is due in the order of the due times.
	probed := make(chan struct{}, 1)
	c.Attach.Periodic = []daemon.PeriodicTask{{Name: "probe", Every: 90 * time.Minute, Run: func(engine.TaskInput) error {
		select {
		case probed <- struct{}{}:
		default:
		}
		return nil
	}}}
	started(t, c)
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })

	status, body := manage(t, c.Home, "identity.retire", map[string]any{
		"operation_id": opID(1), "params": map[string]any{"node": node.String()},
	})
	answerOf(t, status, body)
	select {
	case <-holding:
	case <-time.After(30 * time.Second):
		t.Fatal("the shutdown after the retirement did not begin")
	}

	clk.Advance(25 * 24 * time.Hour) // the certificate has 5 days left
	select {
	case <-probed:
	case <-time.After(30 * time.Second):
		t.Fatal("the periodic tasks did not run while the shutdown was held")
	}
	if got, _ := certificateOnDisk(t, c.Home); got != serial {
		t.Errorf("serial %d after the retirement, want %d: nothing is signed in the name of a retired identity", got, serial)
	}
	var refused bool
	for _, l := range logLines(t, c.Home) {
		if strings.Contains(l, "the certificate is due and is not signed") && strings.Contains(l, "identity_retired") {
			refused = true
		}
	}
	if !refused {
		t.Error("the look at the certificate did not say that it was refused for the identity: the check was not made")
	}
}

// What the owner is shown of the standing facts of the node is derived from where
// the authority for each lives, and the backup of the self-revocation is the one
// that does not go away by itself: status reminds on every call, in the reminders
// that come first, until the owner confirms with the digest of the statement that
// the copy exists outside this device. A digest of another statement confirms
// nothing, and the confirmation is kept across a restart.
func TestStatusRemindsUntilRevocationBackupConfirmed(t *testing.T) {
	noLeaks(t)
	c, _ := initialized(t)
	d := started(t, c)

	reminders := func() []string {
		t.Helper()
		status, body := manage(t, c.Home, "status", map[string]any{})
		if status != 200 {
			t.Fatalf("status answered %d: %s", status, body)
		}
		var env struct {
			Result struct {
				Reminders []struct {
					Kind    string `json:"kind"`
					Message string `json:"message"`
				} `json:"reminders"`
			} `json:"result"`
		}
		if err := json.Unmarshal(body, &env); err != nil {
			t.Fatalf("status is not JSON: %v\n%s", err, body)
		}
		var kinds []string
		for _, r := range env.Result.Reminders {
			if r.Message == "" {
				t.Errorf("a reminder of kind %q says nothing", r.Kind)
			}
			kinds = append(kinds, r.Kind)
		}
		return kinds
	}
	want := []string{"revocation_backup_unconfirmed"}
	if got := reminders(); !reflect.DeepEqual(got, want) {
		t.Fatalf("reminders of a node that was just made: %v, want %v", got, want)
	}

	// The export is the statement as it is on disk, in armor, with the digest that
	// the confirmation is made with. Both are derived here from the record package
	// and the file, not from the code that exports.
	onDisk, err := os.ReadFile(filepath.Join(c.Home, "keys", "self-revocation.rec"))
	if err != nil {
		t.Fatal(err)
	}
	rec, err := record.Decode(onDisk, record.DefaultMaxInput)
	if err != nil {
		t.Fatal(err)
	}
	digest := rec.Digest().String()
	status, body := manage(t, c.Home, "identity.export_revocation", map[string]any{})
	if status != 200 {
		t.Fatalf("export answered %d: %s", status, body)
	}
	var export struct {
		Result struct {
			Armored string `json:"armored"`
			Digest  string `json:"digest"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &export); err != nil {
		t.Fatal(err)
	}
	if export.Result.Digest != digest {
		t.Errorf("the export names the digest %s, want %s", export.Result.Digest, digest)
	}
	lines := strings.Split(strings.TrimRight(export.Result.Armored, "\n"), "\n")
	if len(lines) < 3 || lines[0] != "-----BEGIN SIDERA SELF-REVOCATION-----" || lines[len(lines)-1] != "-----END SIDERA SELF-REVOCATION-----" {
		t.Fatalf("the export is not armored: %q", export.Result.Armored)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.Join(lines[1:len(lines)-1], ""))
	if err != nil || string(raw) != string(onDisk) {
		t.Errorf("the armor does not hold the statement on disk (%v)", err)
	}
	// An export is not an operation that is stored: the statement that can retire
	// the node is not copied under state/.
	if admin, _ := filepath.Glob(filepath.Join(c.Home, "state", "admin", "*")); len(admin) != 0 {
		t.Errorf("the export left %d stored results under state/admin", len(admin))
	}
	// Exporting is not confirming.
	if got := reminders(); !reflect.DeepEqual(got, want) {
		t.Errorf("reminders after an export: %v, want %v", got, want)
	}

	// A digest of another statement confirms nothing.
	other := strings.Repeat("ab", 32)
	status, body = manage(t, c.Home, "identity.confirm_backup", map[string]any{
		"operation_id": opID(1), "params": map[string]any{"digest": other},
	})
	if status != 400 || !strings.Contains(string(body), `"digest_mismatch"`) {
		t.Errorf("confirming another digest: %d %s; want 400 digest_mismatch", status, body)
	}
	if got := reminders(); !reflect.DeepEqual(got, want) {
		t.Errorf("reminders after a confirmation that was refused: %v, want %v", got, want)
	}

	status, body = manage(t, c.Home, "identity.confirm_backup", map[string]any{
		"operation_id": opID(2), "params": map[string]any{"digest": digest},
	})
	first := answerOf(t, status, body)
	var confirmed struct {
		ConfirmedAt string `json:"confirmed_at"`
	}
	if err := json.Unmarshal(first.Result, &confirmed); err != nil || confirmed.ConfirmedAt == "" {
		t.Fatalf("the result of the confirmation is %s (%v)", first.Result, err)
	}
	if got := reminders(); len(got) != 0 {
		t.Errorf("reminders after the owner confirmed the backup: %v, want none", got)
	}
	// The same request again is the stored result, and still no reminder.
	status, body = manage(t, c.Home, "identity.confirm_backup", map[string]any{
		"operation_id": opID(2), "params": map[string]any{"digest": digest},
	})
	if again := answerOf(t, status, body); !again.Replayed || string(again.Result) != string(first.Result) {
		t.Errorf("the confirmation sent again: %s; want the stored result", body)
	}

	// What was confirmed is kept across a restart.
	d.RequestStop("restart")
	if err := d.Wait(); err != nil {
		t.Fatal(err)
	}
	started(t, c)
	if got := reminders(); len(got) != 0 {
		t.Errorf("reminders after a restart: %v, want none", got)
	}
}

// The wall clock that was set back is not trusted: the engine stops signing and
// taking in what depends on a deadline, and the pause is on disk, in the one place
// that deleting state/ cannot clear, until the owner clears it with
// `clock clear-rollback`, which resets the mark to the clock as it reads now. No
// certificate is signed while the pause lasts, even when one is due.
func TestARollbackPauseIsKeptOnDiskAcrossARestartUntilTheOwnerClearsIt(t *testing.T) {
	noLeaks(t)
	c, clk := initialized(t)
	d := started(t, c)

	clk.Advance(2 * time.Hour)
	settled(t, d)
	clk.Set(t0.Add(30 * time.Minute)) // an hour and a half behind the mark
	status, body := manage(t, c.Home, "status", map[string]any{})
	if status != 200 || !strings.Contains(string(body), `"rollback_paused":true`) {
		t.Fatalf("status after the clock was set back: %d %s; want the pause shown", status, body)
	}
	settled(t, d)
	paused := readClockFile(t, c.Home)
	if paused.RollbackPausedSince.IsZero() {
		t.Fatal("the pause was not made durable")
	}

	// The pause outlives the process.
	d.RequestStop("restart")
	if err := d.Wait(); err != nil {
		t.Fatal(err)
	}
	clk.Set(t0.Add(3 * time.Hour)) // the clock is ahead of the mark again, which does not clear it
	d = started(t, c)
	status, body = manage(t, c.Home, "status", map[string]any{})
	if status != 200 || !strings.Contains(string(body), `"rollback_paused":true`) {
		t.Fatalf("status after the restart: %d %s; want the pause kept", status, body)
	}
	// and it is shown with the time it began, which is the one on disk
	if want := `"rollback_paused_since":"` + paused.RollbackPausedSince.UTC().Format(time.RFC3339) + `"`; !strings.Contains(string(body), want) {
		t.Errorf("status after the restart: %s; want it to say %s", body, want)
	}

	// A certificate that is due is not signed while the pause lasts.
	serial, _ := certificateOnDisk(t, c.Home)
	clk.Advance(25 * 24 * time.Hour)
	settled(t, d)
	if got, _ := certificateOnDisk(t, c.Home); got != serial {
		t.Errorf("the certificate was signed while the clock was in doubt: serial %d, was %d", got, serial)
	}

	// The owner clears the pause: the mark is the clock as it reads now, and the
	// flag is gone, in the same replacement.
	status, body = manage(t, c.Home, "clock.clear_rollback", map[string]any{"operation_id": opID(1), "params": map[string]any{}})
	cleared := answerOf(t, status, body)
	var result struct {
		Cleared   bool   `json:"cleared"`
		HighWater string `json:"high_water"`
	}
	if err := json.Unmarshal(cleared.Result, &result); err != nil || !result.Cleared {
		t.Fatalf("the result of the clear is %s (%v)", cleared.Result, err)
	}
	st := readClockFile(t, c.Home)
	if !st.RollbackPausedSince.IsZero() || !st.HighWater.Equal(clk.Now().Truncate(time.Second)) {
		t.Errorf("clock.rec after the clear: %+v; want no pause and the mark at %v", st, clk.Now())
	}
	status, body = manage(t, c.Home, "status", map[string]any{})
	if status != 200 || !strings.Contains(string(body), `"rollback_paused":false`) {
		t.Errorf("status after the clear: %d %s", status, body)
	}
	// The same clear sent again is the stored result; another one finds nothing to
	// clear and says so.
	status, body = manage(t, c.Home, "clock.clear_rollback", map[string]any{"operation_id": opID(1), "params": map[string]any{}})
	if again := answerOf(t, status, body); !again.Replayed || string(again.Result) != string(cleared.Result) {
		t.Errorf("the clear sent again: %s; want the stored result", body)
	}
	status, body = manage(t, c.Home, "clock.clear_rollback", map[string]any{"operation_id": opID(2), "params": map[string]any{}})
	if second := answerOf(t, status, body); !strings.Contains(string(second.Result), `"cleared":false`) {
		t.Errorf("a clear when no pause is in force: %s; want cleared false", body)
	}

	// With the clock trusted again the check, due at once by the clear, signs the
	// certificate; the hour that passes then finds nothing more to do.
	clk.Advance(time.Hour)
	settled(t, d)
	if got, _ := certificateOnDisk(t, c.Home); got != serial+1 {
		t.Errorf("serial %d after the pause was cleared and an hour passed, want %d", got, serial+1)
	}
}

func readClockFile(t testing.TB, home string) keys.ClockState {
	t.Helper()
	st, err := keys.ReadClock(osReader(t, home))
	if err != nil {
		t.Fatalf("reading clock.rec: %v", err)
	}
	return st
}

// The network key's certificate is renewed by the hourly check, before it
// expires: with ten days or less of its thirty left (the boundary is inclusive)
// the next check issues a certificate at the next serial, valid for thirty days
// from now, and the daemon that loads the keys after a restart accepts it.
func TestNetworkKeyCertificateRenewedBeforeExpiry(t *testing.T) {
	noLeaks(t)
	c, clk := initialized(t)
	serial, notAfter := certificateOnDisk(t, c.Home)
	if want := t0.Add(30 * 24 * time.Hour); !notAfter.Equal(want) {
		t.Fatalf("a new certificate is valid until %v, want %v (30 days)", notAfter, want)
	}
	d := started(t, c)

	// More than ten days left: nothing is signed.
	clk.Advance(20*24*time.Hour - time.Hour)
	settled(t, d)
	if got, _ := certificateOnDisk(t, c.Home); got != serial {
		t.Fatalf("serial %d with 10 days and an hour left, want %d (not due)", got, serial)
	}

	// Exactly ten days left: due, and signed by the hourly check that falls there.
	clk.Advance(time.Hour)
	settled(t, d)
	got, gotAfter := certificateOnDisk(t, c.Home)
	if got != serial+1 {
		t.Fatalf("serial %d with exactly 10 days left, want %d (renewed)", got, serial+1)
	}
	if want := clk.Now().Add(30 * 24 * time.Hour); !gotAfter.Equal(want) {
		t.Errorf("the renewed certificate is valid until %v, want %v", gotAfter, want)
	}
	if !clk.Now().Before(notAfter) {
		t.Error("the certificate was renewed only after the old one had expired")
	}

	// A certificate that is current is left alone by the checks that follow.
	clk.Advance(5 * time.Hour)
	settled(t, d)
	if again, _ := certificateOnDisk(t, c.Home); again != serial+1 {
		t.Errorf("serial %d after the renewal, want it unchanged (%d)", again, serial+1)
	}

	// The keys on disk are consistent: the next start loads them.
	d.RequestStop("restart")
	if err := d.Wait(); err != nil {
		t.Fatal(err)
	}
	started(t, c)
}

// A node that was down while its certificate fell due renews it at the step
// after the clock was judged and before anything else signs, so that the first
// report it signs carries a certificate that is current.
func TestACertificateThatFellDueWhileTheNodeWasDownIsRenewedAtTheStep(t *testing.T) {
	noLeaks(t)
	c, clk := initialized(t)
	serial, _ := certificateOnDisk(t, c.Home)
	clk.Advance(25 * 24 * time.Hour)
	started(t, c)
	if got, _ := certificateOnDisk(t, c.Home); got != serial+1 {
		t.Fatalf("serial %d after a start with 5 days left, want %d", got, serial+1)
	}
	st := steps(logLines(t, c.Home))
	four, five := indexOf(st, "startup 4 end"), indexOf(st, "startup 5 begin")
	if four < 0 || five < four || indexOf(st, "startup 5 end") < five {
		t.Errorf("the steps %v: the clock must be judged (4) before the certificate is signed (5)", st)
	}
}

// osReader reads the data directory of a home, as a tool that is not the daemon
// does.
func osReader(t testing.TB, home string) fsx.Reader {
	t.Helper()
	fsys, err := fsx.OpenOS(home, seeded(3))
	if err != nil {
		t.Fatal(err)
	}
	return fsys
}
