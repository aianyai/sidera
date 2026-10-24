package config_test

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/handoff"
	"github.com/aianyai/sidera/internal/node/config"
)

// The limits of a setting live in the package that owns the thing limited, and
// config asks that package: the error a setting gets is the owner's error, so
// the rule has one home. A value one step inside each limit is accepted, so the
// delegation is to the owner's bounds and not to bounds copied from them.
func TestSettingsValidationDelegatesToOwningContract(t *testing.T) {
	good := config.DefaultNodeSettings()
	rows := []struct {
		name  string
		patch func(s *config.NodeSettings)
		want  error // nil: the settings are valid
	}{
		{"prompt turns zero", func(s *config.NodeSettings) { s.NodePromptTurns = 0 }, handoff.ErrInvalidParams},
		{"prompt turns negative", func(s *config.NodeSettings) { s.NodePromptTurns = -1 }, handoff.ErrInvalidParams},
		{"prompt turns one", func(s *config.NodeSettings) { s.NodePromptTurns = 1 }, nil},
		{"endpoint backlog zero", func(s *config.NodeSettings) { s.EndpointDefaults.BacklogLimit = 0 }, delivery.ErrBacklogLimit},
		{"endpoint backlog negative", func(s *config.NodeSettings) { s.EndpointDefaults.BacklogLimit = -3 }, delivery.ErrBacklogLimit},
		{"endpoint backlog one", func(s *config.NodeSettings) { s.EndpointDefaults.BacklogLimit = 1 }, nil},
		{"endpoint queue time zero", func(s *config.NodeSettings) { s.EndpointDefaults.MaxQueue = 0 }, delivery.ErrMaxQueue},
		{"endpoint queue time negative", func(s *config.NodeSettings) { s.EndpointDefaults.MaxQueue = -time.Second }, delivery.ErrMaxQueue},
		{"endpoint queue time past the protocol cap", func(s *config.NodeSettings) { s.EndpointDefaults.MaxQueue = delivery.MaxQueueCap + time.Second }, delivery.ErrMaxQueue},
		{"endpoint queue time at the protocol cap", func(s *config.NodeSettings) { s.EndpointDefaults.MaxQueue = delivery.MaxQueueCap }, nil},
		{"endpoint queue time one second", func(s *config.NodeSettings) { s.EndpointDefaults.MaxQueue = time.Second }, nil},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			s := good
			r.patch(&s)
			err := s.Validate()
			switch {
			case r.want == nil && err != nil:
				t.Errorf("Validate = %v, want valid", err)
			case r.want != nil && !errors.Is(err, r.want):
				t.Errorf("Validate = %v, want an error wrapping %v", err, r.want)
			}
		})
	}
}

// The same delegation holds for the settings of one endpoint.
func TestEndpointSettingsValidationDelegatesToOwningContract(t *testing.T) {
	ok := config.EndpointSettings{MaxQueue: time.Minute, BacklogLimit: 10}
	if err := ok.Validate(); err != nil {
		t.Fatalf("Validate = %v", err)
	}
	if err := (config.EndpointSettings{MaxQueue: 0, BacklogLimit: 10}).Validate(); !errors.Is(err, delivery.ErrMaxQueue) {
		t.Errorf("zero queue time: %v", err)
	}
	if err := (config.EndpointSettings{MaxQueue: time.Minute, BacklogLimit: 0}).Validate(); !errors.Is(err, delivery.ErrBacklogLimit) {
		t.Errorf("zero backlog: %v", err)
	}
}

// The values `init` writes come from the packages that decided them; only what
// no other package owns is a number here, with its reason in the README.
func TestDefaultsComeFromTheirOwners(t *testing.T) {
	d := config.DefaultNodeSettings()
	if d.NodePromptTurns != handoff.DefaultMaxConcurrentTurns {
		t.Errorf("node prompt turns = %d, want the handoff default %d", d.NodePromptTurns, handoff.DefaultMaxConcurrentTurns)
	}
	if d.EndpointDefaults.BacklogLimit != delivery.DefaultEndpointBacklogLimit {
		t.Errorf("endpoint backlog = %d, want the delivery default %d", d.EndpointDefaults.BacklogLimit, delivery.DefaultEndpointBacklogLimit)
	}
	if d.EndpointDefaults.MaxQueue != delivery.DefaultEndpointMaxQueue {
		t.Errorf("endpoint queue time = %v, want the delivery default %v", d.EndpointDefaults.MaxQueue, delivery.DefaultEndpointMaxQueue)
	}
	// The values config decides itself, written out.
	if !d.ReplyPermission {
		t.Error("reply permission is off by default: the zero value of the evidence rule is off, so the owner's switch has to be on")
	}
	if d.NodeQueueLimit != 10000 {
		t.Errorf("node queue limit = %d, want 10000", d.NodeQueueLimit)
	}
	if d.DiskReserveBytes != 256<<20 {
		t.Errorf("disk reserve = %d, want 256 MiB", d.DiskReserveBytes)
	}
	if d.LogTotalBytes != 150<<20 {
		t.Errorf("log total = %d, want 150 MiB", d.LogTotalBytes)
	}
	if d.ArchiveRetentionDays != 30 {
		t.Errorf("archive retention = %d days, want 30", d.ArchiveRetentionDays)
	}
	if err := d.Validate(); err != nil {
		t.Errorf("the defaults are not valid: %v", err)
	}
}

// The settings that no other package limits have ranges of their own.
func TestNodeSettingRanges(t *testing.T) {
	good := config.DefaultNodeSettings()
	rows := []struct {
		name  string
		patch func(s *config.NodeSettings)
	}{
		{"queue limit zero", func(s *config.NodeSettings) { s.NodeQueueLimit = 0 }},
		{"queue limit negative", func(s *config.NodeSettings) { s.NodeQueueLimit = -1 }},
		{"disk reserve zero", func(s *config.NodeSettings) { s.DiskReserveBytes = 0 }},
		{"disk reserve negative", func(s *config.NodeSettings) { s.DiskReserveBytes = -1 }},
		{"retention zero", func(s *config.NodeSettings) { s.ArchiveRetentionDays = 0 }},
		{"retention negative", func(s *config.NodeSettings) { s.ArchiveRetentionDays = -30 }},
		{"log total zero", func(s *config.NodeSettings) { s.LogTotalBytes = 0 }},
		{"log total negative", func(s *config.NodeSettings) { s.LogTotalBytes = -1 }},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			s := good
			r.patch(&s)
			if err := s.Validate(); !errors.Is(err, config.ErrOutOfRange) && !errors.Is(err, config.ErrDiskReserveTooLow) {
				t.Errorf("Validate = %v, want a range error", err)
			}
		})
	}
}

// The disk reserve must cover what the unfinished deliveries and the logs can
// grow by while the node still takes work: the log total plus 4 KiB per
// delivery the queue holds. The arithmetic is written out here: with the
// default 150 MiB of logs and a reserve of 256 MiB, 27,136 deliveries are the
// most that fit ((268,435,456 - 157,286,400) / 4,096).
func TestDiskReserveCoversTheQueueAndTheLogs(t *testing.T) {
	good := config.DefaultNodeSettings()
	for _, r := range []struct {
		limit int
		ok    bool
	}{{27136, true}, {27137, false}, {1, true}, {100000, false}} {
		s := good
		s.NodeQueueLimit = r.limit
		err := s.Validate()
		if r.ok && err != nil {
			t.Errorf("queue limit %d: %v, want valid", r.limit, err)
		}
		if !r.ok && !errors.Is(err, config.ErrDiskReserveTooLow) {
			t.Errorf("queue limit %d: %v, want the reserve to be too low", r.limit, err)
		}
	}
	s := good
	s.LogTotalBytes = 268435456 - 4096*10000 + 1 // one byte too many for the default queue
	if err := s.Validate(); !errors.Is(err, config.ErrDiskReserveTooLow) {
		t.Errorf("log total one byte beyond what the reserve covers: %v", err)
	}
	s.LogTotalBytes--
	if err := s.Validate(); err != nil {
		t.Errorf("log total exactly what the reserve covers: %v", err)
	}
	// A limit large enough to overflow the arithmetic is refused, not wrapped.
	s = good
	s.NodeQueueLimit = int(^uint(0) >> 1)
	if err := s.Validate(); !errors.Is(err, config.ErrDiskReserveTooLow) {
		t.Errorf("a queue limit of the largest int: %v", err)
	}
}

// A patch changes the settings it names and no others, atomically: a patch with
// one bad value changes nothing.
func TestNodeSettingsPatch(t *testing.T) {
	t.Run("named settings change, the rest stay", func(t *testing.T) {
		_, c := loaded(t)
		before := c.Node().Settings
		var seen []config.Node
		n, err := c.SetNodeSettings(request(1, 3), []byte(`{"reply_permission": false, "node_queue_limit": 20000, "endpoint_defaults": {"backlog_limit": 99}}`), result(&seen, `{}`))
		if err != nil {
			t.Fatalf("SetNodeSettings: %v", err)
		}
		want := before
		want.ReplyPermission = false
		want.NodeQueueLimit = 20000
		want.EndpointDefaults.BacklogLimit = 99
		if !reflect.DeepEqual(n.Settings, want) {
			t.Errorf("settings = %+v, want %+v", n.Settings, want)
		}
		if !reflect.DeepEqual(c.Node().Settings, want) {
			t.Error("memory does not hold the result")
		}
	})
	t.Run("a patch that names the endpoint queue time", func(t *testing.T) {
		_, c := loaded(t)
		var seen []config.Node
		n, err := c.SetNodeSettings(request(1, 3), []byte(`{"endpoint_defaults": {"max_queue_s": 600}}`), result(&seen, `{}`))
		if err != nil || n.Settings.EndpointDefaults.MaxQueue != 10*time.Minute || n.Settings.EndpointDefaults.BacklogLimit != 200 {
			t.Errorf("settings = %+v, %v", n.Settings.EndpointDefaults, err)
		}
	})
	t.Run("one bad value changes nothing", func(t *testing.T) {
		rg, c := loaded(t)
		before := snapshot(t, rg.mem)
		var seen []config.Node
		_, err := c.SetNodeSettings(request(1, 3), []byte(`{"reply_permission": false, "node_prompt_turns": 0}`), result(&seen, `{}`))
		ce := changeReason(t, err)
		if ce.Reason != config.ChangeInvalid || ce.Field != "node_prompt_turns" || !errors.Is(err, handoff.ErrInvalidParams) {
			t.Errorf("error = %+v, want ChangeInvalid at node_prompt_turns wrapping the handoff error", ce)
		}
		if !reflect.DeepEqual(before, snapshot(t, rg.mem)) || !c.Node().Settings.ReplyPermission {
			t.Error("a refused patch changed something")
		}
	})
	t.Run("the nested field is named with its path", func(t *testing.T) {
		_, c := loaded(t)
		var seen []config.Node
		_, err := c.SetNodeSettings(request(1, 3), []byte(`{"endpoint_defaults": {"backlog_limit": 0}}`), result(&seen, `{}`))
		if ce := changeReason(t, err); ce.Field != "endpoint_defaults.backlog_limit" || !errors.Is(err, delivery.ErrBacklogLimit) {
			t.Errorf("error = %+v", ce)
		}
	})
	t.Run("the reserve rule sees the merged settings", func(t *testing.T) {
		_, c := loaded(t)
		var seen []config.Node
		_, err := c.SetNodeSettings(request(1, 3), []byte(`{"node_queue_limit": 100000}`), result(&seen, `{}`))
		if ce := changeReason(t, err); ce.Reason != config.ChangeInvalid || !errors.Is(err, config.ErrDiskReserveTooLow) {
			t.Errorf("raising the queue limit alone: %+v", ce)
		}
		n, err := c.SetNodeSettings(request(2, 3), []byte(`{"node_queue_limit": 100000, "disk_reserve_bytes": 1073741824}`), result(&seen, `{}`))
		if err != nil || n.Settings.NodeQueueLimit != 100000 {
			t.Errorf("raising the reserve with it: %v", err)
		}
	})
	for _, r := range []struct {
		name, patch string
		reason      config.ChangeReason
		field       string
	}{
		{"empty patch", `{}`, config.ChangeNothingToChange, ""},
		{"empty nested patch", `{"endpoint_defaults": {}}`, config.ChangeNothingToChange, ""},
		{"unknown setting", `{"log_bytes": 1}`, config.ChangeInvalid, "log_bytes"},
		{"wrong type", `{"reply_permission": "no"}`, config.ChangeInvalid, "reply_permission"},
		{"null", `{"reply_permission": null}`, config.ChangeInvalid, "reply_permission"},
		{"a float", `{"node_queue_limit": 1.5}`, config.ChangeInvalid, "node_queue_limit"},
		{"not an object", `[1]`, config.ChangeInvalid, ""},
		{"not json", `{`, config.ChangeInvalid, ""},
		{"a setting twice", `{"reply_permission": true, "reply_permission": false}`, config.ChangeInvalid, "reply_permission"},
	} {
		t.Run(r.name, func(t *testing.T) {
			_, c := loaded(t)
			var seen []config.Node
			_, err := c.SetNodeSettings(request(1, 3), []byte(r.patch), result(&seen, `{}`))
			ce := changeReason(t, err)
			if ce.Reason != r.reason || (r.field != "" && ce.Field != r.field) {
				t.Errorf("error = %+v, want %v at %q", ce, r.reason, r.field)
			}
			if len(seen) != 0 || c.Node().Version != admin.Version(3) {
				t.Error("a refused patch built a result or changed the version")
			}
		})
	}
}

// The patch decoders on their own: what the management layer hands over is
// decoded here and nowhere else.
func TestPatchDecoders(t *testing.T) {
	p, err := config.DecodeNodePatch([]byte(`{"node_prompt_turns": 4, "endpoint_defaults": {"max_queue_s": 60}}`))
	if err != nil {
		t.Fatal(err)
	}
	if p.NodePromptTurns == nil || *p.NodePromptTurns != 4 || p.ReplyPermission != nil || p.EndpointDefaults == nil ||
		p.EndpointDefaults.MaxQueue == nil || *p.EndpointDefaults.MaxQueue != time.Minute || p.EndpointDefaults.BacklogLimit != nil {
		t.Errorf("patch = %+v", p)
	}
	// An explicit false and an explicit zero are values, not absence.
	p, err = config.DecodeNodePatch([]byte(`{"reply_permission": false, "node_prompt_turns": 0}`))
	if err != nil || p.ReplyPermission == nil || *p.ReplyPermission || p.NodePromptTurns == nil || *p.NodePromptTurns != 0 {
		t.Errorf("patch = %+v, %v", p, err)
	}
	e, err := config.DecodeEndpointPatch([]byte(`{"backlog_limit": 5}`))
	if err != nil || e.BacklogLimit == nil || *e.BacklogLimit != 5 || e.MaxQueue != nil {
		t.Errorf("endpoint patch = %+v, %v", e, err)
	}
	for _, bad := range []string{`{"max_queue": 5}`, `{"backlog_limit": "5"}`, `{"backlog_limit": null}`, `5`} {
		if _, err := config.DecodeEndpointPatch([]byte(bad)); err == nil {
			t.Errorf("DecodeEndpointPatch(%s) accepted it", bad)
		}
	}
}
