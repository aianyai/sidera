package kanban

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Status is the status of a card.
type Status string

// The statuses `hermes kanban list --status` accepts.
const (
	StatusTriage    Status = "triage"
	StatusTodo      Status = "todo"
	StatusScheduled Status = "scheduled"
	StatusReady     Status = "ready"
	StatusRunning   Status = "running"
	StatusReview    Status = "review"
	StatusBlocked   Status = "blocked"
	StatusDone      Status = "done"
	StatusArchived  Status = "archived"
)

// BlockKind is the typed reason of a block. It is the one home of Hermes'
// vocabulary (VALID_BLOCK_KINDS): `block --kind` accepts exactly these four, and
// a second block of the same kind on a card sends it to triage.
type BlockKind string

const (
	// BlockDependency waits in todo until the card's parents finish.
	BlockDependency BlockKind = "dependency"
	// BlockNeedsInput waits for a person.
	BlockNeedsInput BlockKind = "needs_input"
	// BlockCapability says the card cannot be done as written.
	BlockCapability BlockKind = "capability"
	// BlockTransient marks a failure that may be gone on the next try.
	BlockTransient BlockKind = "transient"
)

// Valid reports whether k is one of the four kinds Hermes accepts.
func (k BlockKind) Valid() bool {
	switch k {
	case BlockDependency, BlockNeedsInput, BlockCapability, BlockTransient:
		return true
	}
	return false
}

// Delivery modes of a notification subscription.
const (
	DeliveryNotify     = "notify"
	DeliveryNotifyWake = "notify+wake"
	DeliveryWake       = "wake"
)

// Card is a task as `list --json` and `create --json` print it. Fields Hermes
// prints that the adapter does not read (workspace, model overrides, ...) are
// ignored.
type Card struct {
	ID               string
	Title            string
	Body             string
	Assignee         string
	Status           Status
	Priority         int
	Tenant           string
	CreatedBy        string
	SessionID        string // the chat or agent session that created the card, empty when none
	Result           string
	LastFailureError string
	CreatedAt        time.Time
	StartedAt        time.Time // zero until a claim
	CompletedAt      time.Time // zero until done
}

// Comment is a comment on a card.
type Comment struct {
	Author    string
	Body      string
	CreatedAt time.Time
}

// Run is one attempt at a card. A claim opens a run; block and complete close it,
// and the summary of a completed run is where Hermes keeps a lane's answer.
type Run struct {
	ID        int64
	Profile   string
	Status    string
	Outcome   string // empty while the run is open
	Summary   string
	Error     string
	Metadata  json.RawMessage // a JSON object or null
	StartedAt time.Time
	EndedAt   time.Time // zero while the run is open
}

// Detail is a card with its graph, comments and runs, as `show --json` prints it.
// The event log is not decoded.
type Detail struct {
	Card          Card
	LatestSummary string
	Parents       []string
	Children      []string
	Comments      []Comment
	Runs          []Run
}

// Attachment is a file attached to a card. StoredPath is where Hermes keeps it
// on this machine; it is the path an agent reads the file from.
type Attachment struct {
	ID          int64
	Filename    string // as stored: Hermes renames a duplicate to "name (1).ext"
	ContentType string
	Size        int64
	UploadedBy  string
	StoredPath  string
	CreatedAt   time.Time
}

// NotifySub is one row of a card's notification subscriptions: where Hermes sends
// the card's terminal events and how. Every routing and delivery column is kept,
// because a subscription copied to another card must reproduce the original
// destination exactly.
type NotifySub struct {
	TaskID          string
	Platform        string
	ChatID          string
	ThreadID        string
	UserID          string
	UserIDAlt       string
	ChatType        string
	NotifierProfile string
	DeliveryMode    string
	// DeliveryMetadata holds routing anchors with no command-line flag of their own
	// (such as a scope id) next to parent_chat_id and guild_id.
	DeliveryMetadata map[string]any
	CreatedAt        time.Time
	LastEventID      int64
}

// Assignee is a name that is a profile on disk or the assignee of a card.
type Assignee struct {
	Name   string
	OnDisk bool
}

func unixTime(sec *int64) time.Time {
	if sec == nil {
		return time.Time{}
	}
	return time.Unix(*sec, 0)
}

// decodeObject unmarshals the JSON object b into v, which tolerates unknown
// fields, and fails when any key in required is absent. A key that is present
// with a null value counts as present: it is the field that is gone that signals
// a changed shape.
func decodeObject(b []byte, what string, v any, required ...string) error {
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(b, &keys); err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	var missing []string
	for _, k := range required {
		if _, ok := keys[k]; !ok {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%s: missing field(s) %s", what, strings.Join(missing, ", "))
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	return nil
}

func (c *Card) UnmarshalJSON(b []byte) error {
	var w struct {
		ID               string `json:"id"`
		Title            string `json:"title"`
		Body             string `json:"body"`
		Assignee         string `json:"assignee"`
		Status           Status `json:"status"`
		Priority         int    `json:"priority"`
		Tenant           string `json:"tenant"`
		CreatedBy        string `json:"created_by"`
		SessionID        string `json:"session_id"`
		Result           string `json:"result"`
		LastFailureError string `json:"last_failure_error"`
		CreatedAt        *int64 `json:"created_at"`
		StartedAt        *int64 `json:"started_at"`
		CompletedAt      *int64 `json:"completed_at"`
	}
	if err := decodeObject(b, "card", &w, "id", "title", "body", "assignee", "status",
		"created_by", "session_id", "created_at"); err != nil {
		return err
	}
	if w.ID == "" || w.Status == "" {
		return fmt.Errorf("card: empty id or status")
	}
	*c = Card{ID: w.ID, Title: w.Title, Body: w.Body, Assignee: w.Assignee, Status: w.Status,
		Priority: w.Priority, Tenant: w.Tenant, CreatedBy: w.CreatedBy, SessionID: w.SessionID,
		Result: w.Result, LastFailureError: w.LastFailureError, CreatedAt: unixTime(w.CreatedAt),
		StartedAt: unixTime(w.StartedAt), CompletedAt: unixTime(w.CompletedAt)}
	return nil
}

func (c *Comment) UnmarshalJSON(b []byte) error {
	var w struct {
		Author    string `json:"author"`
		Body      string `json:"body"`
		CreatedAt *int64 `json:"created_at"`
	}
	if err := decodeObject(b, "comment", &w, "author", "body", "created_at"); err != nil {
		return err
	}
	*c = Comment{Author: w.Author, Body: w.Body, CreatedAt: unixTime(w.CreatedAt)}
	return nil
}

func (r *Run) UnmarshalJSON(b []byte) error {
	var w struct {
		ID        int64           `json:"id"`
		Profile   string          `json:"profile"`
		Status    string          `json:"status"`
		Outcome   string          `json:"outcome"`
		Summary   string          `json:"summary"`
		Error     string          `json:"error"`
		Metadata  json.RawMessage `json:"metadata"`
		StartedAt *int64          `json:"started_at"`
		EndedAt   *int64          `json:"ended_at"`
	}
	if err := decodeObject(b, "run", &w, "id", "status", "outcome", "summary", "started_at"); err != nil {
		return err
	}
	*r = Run{ID: w.ID, Profile: w.Profile, Status: w.Status, Outcome: w.Outcome, Summary: w.Summary,
		Error: w.Error, Metadata: w.Metadata, StartedAt: unixTime(w.StartedAt), EndedAt: unixTime(w.EndedAt)}
	return nil
}

func (d *Detail) UnmarshalJSON(b []byte) error {
	var w struct {
		Task          Card      `json:"task"`
		LatestSummary string    `json:"latest_summary"`
		Parents       []string  `json:"parents"`
		Children      []string  `json:"children"`
		Comments      []Comment `json:"comments"`
		Runs          []Run     `json:"runs"`
	}
	if err := decodeObject(b, "show", &w, "task", "latest_summary", "parents", "children",
		"comments", "runs"); err != nil {
		return err
	}
	*d = Detail{Card: w.Task, LatestSummary: w.LatestSummary, Parents: w.Parents,
		Children: w.Children, Comments: w.Comments, Runs: w.Runs}
	return nil
}

func (a *Attachment) UnmarshalJSON(b []byte) error {
	var w struct {
		ID          int64  `json:"id"`
		Filename    string `json:"filename"`
		ContentType string `json:"content_type"`
		Size        int64  `json:"size"`
		UploadedBy  string `json:"uploaded_by"`
		StoredPath  string `json:"stored_path"`
		CreatedAt   *int64 `json:"created_at"`
	}
	if err := decodeObject(b, "attachment", &w, "id", "filename", "size", "stored_path"); err != nil {
		return err
	}
	*a = Attachment{ID: w.ID, Filename: w.Filename, ContentType: w.ContentType, Size: w.Size,
		UploadedBy: w.UploadedBy, StoredPath: w.StoredPath, CreatedAt: unixTime(w.CreatedAt)}
	return nil
}

func (s *NotifySub) UnmarshalJSON(b []byte) error {
	var w struct {
		TaskID           string         `json:"task_id"`
		Platform         string         `json:"platform"`
		ChatID           string         `json:"chat_id"`
		ThreadID         string         `json:"thread_id"`
		UserID           string         `json:"user_id"`
		UserIDAlt        string         `json:"user_id_alt"`
		ChatType         string         `json:"chat_type"`
		NotifierProfile  string         `json:"notifier_profile"`
		DeliveryMode     string         `json:"delivery_mode"`
		DeliveryMetadata map[string]any `json:"delivery_metadata"`
		CreatedAt        *int64         `json:"created_at"`
		LastEventID      int64          `json:"last_event_id"`
	}
	if err := decodeObject(b, "notify subscription", &w, "task_id", "platform", "chat_id",
		"thread_id", "user_id", "user_id_alt", "chat_type", "notifier_profile", "delivery_mode",
		"delivery_metadata"); err != nil {
		return err
	}
	*s = NotifySub{TaskID: w.TaskID, Platform: w.Platform, ChatID: w.ChatID, ThreadID: w.ThreadID,
		UserID: w.UserID, UserIDAlt: w.UserIDAlt, ChatType: w.ChatType, NotifierProfile: w.NotifierProfile,
		DeliveryMode: w.DeliveryMode, DeliveryMetadata: w.DeliveryMetadata,
		CreatedAt: unixTime(w.CreatedAt), LastEventID: w.LastEventID}
	return nil
}

func (a *Assignee) UnmarshalJSON(b []byte) error {
	var w struct {
		Name   string `json:"name"`
		OnDisk bool   `json:"on_disk"`
	}
	if err := decodeObject(b, "assignee", &w, "name", "on_disk"); err != nil {
		return err
	}
	*a = Assignee(w)
	return nil
}
