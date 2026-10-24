package config_test

import (
	"bytes"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/config"
)

// Enabling creates the instance when it is new (named after the adapter when
// no name is given), and turns an existing one on.
func TestEnableAdapterCreatesOrEnables(t *testing.T) {
	t.Run("a new instance", func(t *testing.T) {
		_, c := loaded(t)
		var seen []config.Instances
		after, err := c.EnableAdapter(request(1, 4), "second", "example", []byte(`{"workspace": "/tmp/w"}`), nil, result(&seen, `{}`))
		if err != nil {
			t.Fatalf("EnableAdapter: %v", err)
		}
		in := after.Items["second"]
		if in.Adapter != "example" || !in.Enabled || string(in.Config["workspace"]) != `"/tmp/w"` {
			t.Errorf("instance = %+v", in)
		}
		if !after.Items["example"].Enabled || after.Version != 5 {
			t.Errorf("the other instance or the version moved: %+v", after)
		}
	})
	t.Run("a disabled instance is enabled again with its settings", func(t *testing.T) {
		_, c := loaded(t)
		var seen []config.Instances
		if _, err := c.DisableAdapter(request(1, 4), "example", nil, result(&seen, `{}`)); err != nil {
			t.Fatal(err)
		}
		if c.Instances().Items["example"].Enabled {
			t.Fatal("the instance is still enabled")
		}
		after, err := c.EnableAdapter(request(2, 5), "example", "example", nil, nil, result(&seen, `{}`))
		if err != nil {
			t.Fatalf("EnableAdapter: %v", err)
		}
		in := after.Items["example"]
		if !in.Enabled || string(in.Config["workspace"]) != `"/Users/me/work"` || len(in.Endpoints) != 2 {
			t.Errorf("instance = %+v: enabling again has to keep what the owner set", in)
		}
	})
	for _, r := range []struct {
		name     string
		instance string
		adapter  string
		cfg      string
		want     config.ChangeReason
	}{
		{"an adapter that is not installed", "x", "ghost", "", config.ChangeAdapterNotInstalled},
		{"an existing instance of another adapter", "example", "other", "", config.ChangeInstanceAdapterMismatch},
		{"an existing instance with a configuration", "example", "example", `{"a": 1}`, config.ChangeInstanceExists},
		{"an instance name that is not a name", "Bad Name", "example", "", config.ChangeInvalid},
		{"a configuration that is not an object", "x", "example", `[1]`, config.ChangeInvalid},
		{"a configuration key that is not a key", "x", "example", `{"Bad Key": 1}`, config.ChangeInvalid},
		{"a configuration value that is null", "x", "example", `{"k": null}`, config.ChangeInvalid},
	} {
		t.Run(r.name, func(t *testing.T) {
			rg, c := loaded(t)
			before := snapshot(t, rg.mem)
			var cfg []byte
			if r.cfg != "" {
				cfg = []byte(r.cfg)
			}
			var seen []config.Instances
			_, err := c.EnableAdapter(request(1, 4), r.instance, r.adapter, cfg, nil, result(&seen, `{}`))
			if ce := changeReason(t, err); ce.Reason != r.want {
				t.Errorf("reason = %v, want %v (%v)", ce.Reason, r.want, err)
			}
			if !reflect.DeepEqual(before, snapshot(t, rg.mem)) {
				t.Error("a refused change wrote to the disk")
			}
		})
	}
}

func TestDisableAdapterKeepsTheInstance(t *testing.T) {
	rg, c := loaded(t)
	var seen []config.Instances
	after, err := c.DisableAdapter(request(1, 4), "example", nil, result(&seen, `{}`))
	if err != nil {
		t.Fatalf("DisableAdapter: %v", err)
	}
	in, ok := after.Items["example"]
	if !ok || in.Enabled || len(in.Endpoints) != 2 {
		t.Errorf("instance = %+v, %v: a disabled instance stays, with its settings", in, ok)
	}
	if !exists(rg.mem, config.EndpointIDsPath("example")) {
		t.Error("the endpoint ids were removed with the instance being disabled")
	}
	if _, err := c.DisableAdapter(request(2, 5), "nobody", nil, result(&seen, `{}`)); changeReason(t, err).Reason != config.ChangeInstanceUnknown {
		t.Errorf("disabling an unknown instance: %v", err)
	}
}

// The settings of an endpoint are patched in place: what a patch does not name
// stays, and a setting for an endpoint nobody can keep is refused, since the
// next write would drop it.
func TestSetEndpointSettings(t *testing.T) {
	ep1, ep4, ep5 := mustEndpoint(t, ep1Text), mustEndpoint(t, ep4Text), mustEndpoint(t, ep5Text)
	var seen []config.Instances

	t.Run("a stable endpoint keeps the setting the patch does not name", func(t *testing.T) {
		_, c := loaded(t)
		after, err := c.SetEndpointSettings(request(1, 4), "example", ep1, []byte(`{"max_queue_s": 600}`), isOpen(t), result(&seen, `{}`))
		if err != nil {
			t.Fatal(err)
		}
		if got := after.Items["example"].Endpoints[ep1]; got != (config.EndpointOverride{MaxQueue: 10 * time.Minute, BacklogLimit: 50}) {
			t.Errorf("override = %+v", got)
		}
	})
	t.Run("an open session endpoint gets a setting", func(t *testing.T) {
		_, c := loaded(t)
		after, err := c.SetEndpointSettings(request(1, 4), "example", ep4, []byte(`{"backlog_limit": 7}`), isOpen(t), result(&seen, `{}`))
		if err != nil {
			t.Fatal(err)
		}
		if got := after.Items["example"].Endpoints[ep4]; got != (config.EndpointOverride{BacklogLimit: 7}) {
			t.Errorf("override = %+v", got)
		}
		eff := c.EndpointSettings("example", ep4)
		if eff.BacklogLimit != 7 || eff.MaxQueue != c.Node().Settings.EndpointDefaults.MaxQueue {
			t.Errorf("effective settings = %+v: the field that is not set takes the node default", eff)
		}
	})
	t.Run("an endpoint with no setting takes the node defaults", func(t *testing.T) {
		_, c := loaded(t)
		eff := c.EndpointSettings("example", ep5)
		if eff != c.Node().Settings.EndpointDefaults {
			t.Errorf("effective = %+v, want the node defaults", eff)
		}
	})
	t.Run("an endpoint that is neither stable nor open is refused", func(t *testing.T) {
		rg, c := loaded(t)
		before := snapshot(t, rg.mem)
		_, err := c.SetEndpointSettings(request(1, 4), "example", ep5, []byte(`{"backlog_limit": 7}`), isOpen(t), result(&seen, `{}`))
		if ce := changeReason(t, err); ce.Reason != config.ChangeEndpointUnknown {
			t.Errorf("reason = %v", ce.Reason)
		}
		if !reflect.DeepEqual(before, snapshot(t, rg.mem)) {
			t.Error("a refused change wrote to the disk")
		}
	})
	t.Run("an instance that does not exist", func(t *testing.T) {
		_, c := loaded(t)
		_, err := c.SetEndpointSettings(request(1, 4), "nobody", ep1, []byte(`{"backlog_limit": 7}`), isOpen(t), result(&seen, `{}`))
		if ce := changeReason(t, err); ce.Reason != config.ChangeInstanceUnknown {
			t.Errorf("reason = %v", ce.Reason)
		}
	})
	for _, r := range []struct {
		name, patch string
		want        error
		reason      config.ChangeReason
	}{
		{"backlog limit zero", `{"backlog_limit": 0}`, delivery.ErrBacklogLimit, config.ChangeInvalid},
		{"queue time beyond the protocol cap", `{"max_queue_s": 7201}`, delivery.ErrMaxQueue, config.ChangeInvalid},
		{"queue time negative", `{"max_queue_s": -1}`, delivery.ErrMaxQueue, config.ChangeInvalid},
		{"nothing named", `{}`, nil, config.ChangeNothingToChange},
		{"unknown setting", `{"queue": 1}`, nil, config.ChangeInvalid},
	} {
		t.Run(r.name, func(t *testing.T) {
			_, c := loaded(t)
			_, err := c.SetEndpointSettings(request(1, 4), "example", ep1, []byte(r.patch), isOpen(t), result(&seen, `{}`))
			ce := changeReason(t, err)
			if ce.Reason != r.reason || (r.want != nil && !errors.Is(err, r.want)) {
				t.Errorf("error = %v, want %v wrapping %v", err, r.reason, r.want)
			}
		})
	}
}

// The settings of a session endpoint that has closed are not removed when it
// closes, which would make the engine change the owner's version; they go with
// the owner's next write to instances.json, and only those of session
// endpoints: a stable endpoint's id comes back, so its settings stay. A write
// that is not told which endpoints are open drops nothing.
func TestSettingsOfClosedSessionEndpointsGoWithTheNextWrite(t *testing.T) {
	ep1, ep3 := mustEndpoint(t, ep1Text), mustEndpoint(t, ep3Text)
	var seen []config.Instances

	for _, w := range []struct {
		name string
		do   func(c *config.Config, open config.OpenEndpoints) (config.Instances, error)
	}{
		{"disable", func(c *config.Config, open config.OpenEndpoints) (config.Instances, error) {
			return c.DisableAdapter(request(1, 4), "example", open, result(&seen, `{}`))
		}},
		{"enable", func(c *config.Config, open config.OpenEndpoints) (config.Instances, error) {
			return c.EnableAdapter(request(1, 4), "second", "example", nil, open, result(&seen, `{}`))
		}},
		{"endpoint set", func(c *config.Config, open config.OpenEndpoints) (config.Instances, error) {
			return c.SetEndpointSettings(request(1, 4), "example", ep1, []byte(`{"backlog_limit": 60}`), open, result(&seen, `{}`))
		}},
	} {
		t.Run(w.name+": the closed session endpoint's settings are dropped", func(t *testing.T) {
			rg, c := loaded(t)
			after, err := w.do(c, func(id ident.EndpointID) bool { return false })
			if err != nil {
				t.Fatal(err)
			}
			eps := after.Items["example"].Endpoints
			if _, ok := eps[ep3]; ok {
				t.Error("the settings of a closed session endpoint were kept")
			}
			if _, ok := eps[ep1]; !ok {
				t.Error("the settings of a stable endpoint were dropped")
			}
			if again := rg.load().Instances(); !reflect.DeepEqual(again, c.Instances()) {
				t.Error("what is on disk differs from what is in memory")
			}
		})
		t.Run(w.name+": an open session endpoint keeps its settings", func(t *testing.T) {
			_, c := loaded(t)
			after, err := w.do(c, func(id ident.EndpointID) bool { return id == ep3 })
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := after.Items["example"].Endpoints[ep3]; !ok {
				t.Error("the settings of an open session endpoint were dropped")
			}
		})
		t.Run(w.name+": no information about open endpoints drops nothing", func(t *testing.T) {
			_, c := loaded(t)
			after, err := w.do(c, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := after.Items["example"].Endpoints[ep3]; !ok {
				t.Error("settings were dropped without knowing which endpoints are open")
			}
		})
	}
}

// The ids of an instance are minted once and kept: asking again for the same
// key gives the same id, and nothing is written for it.
func TestStableEndpointIDsAreMintedOnce(t *testing.T) {
	rnd := bytes.NewReader(bytes.Repeat([]byte{0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0x37, 0x38, 0x39, 0x3a, 0x3b, 0x3c, 0x3d, 0x3e, 0x3f, 0x40}, 8))

	t.Run("an existing key", func(t *testing.T) {
		_, c := loaded(t)
		ch, err := c.DirectID("example", "notes", rnd)
		if err != nil {
			t.Fatal(err)
		}
		if ch.New || ch.ID != mustEndpoint(t, ep2Text) {
			t.Errorf("change = %+v, want the existing id and nothing new", ch)
		}
		if rnd.Len() != 128 {
			t.Error("randomness was drawn for an id that exists")
		}
		rt, err := c.RuntimeID("example", rnd)
		if err != nil || rt.New || rt.ID != mustEndpoint(t, ep1Text) {
			t.Errorf("runtime = %+v, %v", rt, err)
		}
	})
	t.Run("a new key is minted, written and kept", func(t *testing.T) {
		rg, c := loaded(t)
		ch, err := c.DirectID("example", "fresh", rnd)
		if err != nil {
			t.Fatal(err)
		}
		if !ch.New || ch.ID.IsZero() {
			t.Fatalf("change = %+v", ch)
		}
		if got := c.EndpointIDs("example").Direct["fresh"]; !got.IsZero() {
			t.Error("memory took the id over before the transaction committed")
		}
		tx := rg.w.Begin(testDirectRegister)
		defer tx.Close()
		done, err := c.PutEndpointIDs(tx, ch)
		if err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		done()
		if got := c.EndpointIDs("example").Direct["fresh"]; got != ch.ID {
			t.Errorf("after the commit memory holds %v, want %v", got, ch.ID)
		}
		again := rg.load().EndpointIDs("example")
		if again.Direct["fresh"] != ch.ID || again.Direct["notes"] != mustEndpoint(t, ep2Text) || again.Runtime != mustEndpoint(t, ep1Text) {
			t.Errorf("a restart reads %+v", again)
		}
		same, err := c.DirectID("example", "fresh", rnd)
		if err != nil || same.New || same.ID != ch.ID {
			t.Errorf("the same key again = %+v, %v", same, err)
		}
	})
	t.Run("an id that is not new writes nothing", func(t *testing.T) {
		rg, c := loaded(t)
		ch, _ := c.DirectID("example", "notes", rnd)
		before := snapshot(t, rg.mem)
		tx := rg.w.Begin(testDirectRegister)
		defer tx.Close()
		done, err := c.PutEndpointIDs(tx, ch)
		if err != nil || tx.Commit() != nil {
			t.Fatal(err)
		}
		done()
		if !reflect.DeepEqual(before, snapshot(t, rg.mem)) {
			t.Error("writing an existing id changed the disk")
		}
	})
	t.Run("a runtime id for an instance that has none", func(t *testing.T) {
		rg, c := loaded(t)
		var seen []config.Instances
		if _, err := c.EnableAdapter(request(1, 4), "second", "example", nil, nil, result(&seen, `{}`)); err != nil {
			t.Fatal(err)
		}
		ch, err := c.RuntimeID("second", rnd)
		if err != nil || !ch.New {
			t.Fatalf("RuntimeID = %+v, %v", ch, err)
		}
		tx := rg.w.Begin(testRuntimeAdmit)
		defer tx.Close()
		done, err := c.PutEndpointIDs(tx, ch)
		if err != nil || tx.Commit() != nil {
			t.Fatal(err)
		}
		done()
		if got := rg.load().EndpointIDs("second").Runtime; got != ch.ID {
			t.Errorf("runtime id after a restart = %v, want %v", got, ch.ID)
		}
	})
	t.Run("an instance that does not exist gets no ids", func(t *testing.T) {
		_, c := loaded(t)
		if _, err := c.DirectID("nobody", "k", rnd); changeReason(t, err).Reason != config.ChangeInstanceUnknown {
			t.Errorf("DirectID for an unknown instance: %v", err)
		}
		if _, err := c.RuntimeID("nobody", rnd); changeReason(t, err).Reason != config.ChangeInstanceUnknown {
			t.Errorf("RuntimeID for an unknown instance: %v", err)
		}
	})
	t.Run("a key beyond the length bound", func(t *testing.T) {
		_, c := loaded(t)
		long := string(bytes.Repeat([]byte("k"), 129))
		if _, err := c.DirectID("example", long, rnd); changeReason(t, err).Reason != config.ChangeInvalid {
			t.Errorf("DirectID with a key of 129 characters: %v", err)
		}
		if _, err := c.DirectID("example", "", rnd); changeReason(t, err).Reason != config.ChangeInvalid {
			t.Errorf("DirectID with an empty key: %v", err)
		}
	})
	t.Run("a failing source of randomness shows", func(t *testing.T) {
		_, c := loaded(t)
		if _, err := c.DirectID("example", "fresh", bytes.NewReader(nil)); err == nil {
			t.Error("DirectID minted an id from no randomness")
		}
	})
}

// A change that is made while the Tx of another package runs is taken over only
// after its commit: a Tx that is abandoned after its commit point is a defect
// the transaction layer reports by panicking, and memory stays as it was.
func TestEndpointIDsAreTakenOverOnlyAfterCommit(t *testing.T) {
	rg, c := loaded(t)
	rnd := bytes.NewReader(bytes.Repeat([]byte{0x51}, 16))
	ch, err := c.DirectID("example", "fresh", rnd)
	if err != nil {
		t.Fatal(err)
	}
	tx := rg.w.Begin(testDirectRegister)
	done, err := c.PutEndpointIDs(tx, ch)
	if err != nil {
		t.Fatal(err)
	}
	_ = done
	func() {
		defer func() {
			if recover() == nil {
				t.Error("a Tx abandoned after its commit point was closed without a panic")
			}
		}()
		tx.Close() // abandoned: the file was replaced but nobody committed
	}()
	if !c.EndpointIDs("example").Direct["fresh"].IsZero() {
		t.Error("memory took an id over without a commit")
	}
}
