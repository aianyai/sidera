package kanban

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// opt formats a valued option as --name=value. The joined form keeps a value that
// begins with "-" from being read as another option.
func opt(name, value string) string { return name + "=" + value }

// decodeJSON decodes the --json output of op into v. A failure is an
// ErrUnexpectedOutput, never a guess.
func decodeJSON(op string, out []byte, v any) error {
	if err := json.Unmarshal(out, v); err != nil {
		return &Error{Op: op, Class: Transient, ExitCode: -1,
			Err: fmt.Errorf("%w: %v", ErrUnexpectedOutput, err)}
	}
	return nil
}

// List returns the cards with the given status.
func (c *Client) List(ctx context.Context, status Status) ([]Card, error) {
	out, err := c.kanban(ctx, "list", nil, "list", opt("--status", string(status)), "--json")
	if err != nil {
		return nil, err
	}
	var cards []Card
	if err := decodeJSON("list", out, &cards); err != nil {
		return nil, err
	}
	return cards, nil
}

// Show returns a card with its parents, comments and runs.
func (c *Client) Show(ctx context.Context, id string) (*Detail, error) {
	out, err := c.kanban(ctx, "show", nil, "show", id, "--json")
	if err != nil {
		return nil, err
	}
	var d Detail
	if err := decodeJSON("show", out, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// NewCard describes a card to create.
type NewCard struct {
	Title string // required; bounded by MaxArgBytes
	// Body is the opening post. It goes through standard input and is not bounded.
	Body     string
	Assignee string
	// Parents are the ids of the cards this one depends on. A card whose parents
	// are all done is created ready; otherwise it is todo.
	Parents []string
	// IdempotencyKey makes a second Create with the same key return the card the
	// first one made, as it was then, instead of a duplicate.
	IdempotencyKey string
	// CreatedBy is recorded on the card; Hermes' default is "user".
	CreatedBy string
}

// Create makes a card and returns it.
func (c *Client) Create(ctx context.Context, n NewCard) (*Card, error) {
	// Options first, then the title after "--": a title may begin with "-".
	args := []string{"create", "--json"}
	var stdin []byte
	if n.Body != "" {
		args = append(args, "--body-file", "-")
		stdin = []byte(n.Body)
	}
	if n.Assignee != "" {
		args = append(args, opt("--assignee", n.Assignee))
	}
	for _, p := range n.Parents {
		args = append(args, opt("--parent", p))
	}
	if n.IdempotencyKey != "" {
		args = append(args, opt("--idempotency-key", n.IdempotencyKey))
	}
	if n.CreatedBy != "" {
		args = append(args, opt("--created-by", n.CreatedBy))
	}
	args = append(args, "--", n.Title)
	out, err := c.kanban(ctx, "create", stdin, args...)
	if err != nil {
		return nil, err
	}
	var card Card
	if err := decodeJSON("create", out, &card); err != nil {
		return nil, err
	}
	return &card, nil
}

// Claim takes a ready card for ttl. The command line has no renewable lease: a
// claim that expires returns the card to ready, and a second expiry blocks it, so
// a caller that waits long claims with a TTL far above any wait.
func (c *Client) Claim(ctx context.Context, id string, ttl time.Duration) error {
	secs := int64(ttl / time.Second)
	if secs < 1 {
		return fmt.Errorf("kanban: claim ttl %v is below one second", ttl)
	}
	_, err := c.kanban(ctx, "claim", nil, "claim", id, opt("--ttl", strconv.FormatInt(secs, 10)))
	return err
}

// Block marks a card blocked with a typed reason. Hermes also records the reason
// as a comment before it tries the block, so a refused block (the card is not
// ready or running, or already blocked) can still leave a "BLOCKED: ..." comment.
// A block with the kind the card was last blocked with, which Hermes keeps through
// an unblock, sends the card to triage.
func (c *Client) Block(ctx context.Context, id string, kind BlockKind, reason string) error {
	if !kind.Valid() {
		return fmt.Errorf("kanban: %q is not a block kind Hermes accepts", kind)
	}
	args := []string{"block", id, opt("--kind", string(kind))}
	if reason != "" {
		args = append(args, "--", reason)
	}
	_, err := c.kanban(ctx, "block", nil, args...)
	return err
}

// Comment appends a comment to a card. The text is bounded by MaxArgBytes.
func (c *Client) Comment(ctx context.Context, id, text string) error {
	_, err := c.kanban(ctx, "comment", nil, "comment", id, opt("--author", c.cfg.Author), "--", text)
	return err
}

// Completion is what a card is completed with. Hermes refuses a completion with
// neither a summary nor a result. The summary is the text a waking agent reads
// first; metadata is structured data stored on the closing run. All three are
// bounded by MaxArgBytes: longer text is attached to the card before completing.
type Completion struct {
	Summary  string
	Result   string
	Metadata map[string]any
}

// Complete marks a card done. A card that is already done, archived or waiting on
// open parents is refused with a Permanent error.
func (c *Client) Complete(ctx context.Context, id string, done Completion) error {
	args := []string{"complete", id}
	if done.Summary != "" {
		args = append(args, opt("--summary", done.Summary))
	}
	if done.Result != "" {
		args = append(args, opt("--result", done.Result))
	}
	if done.Metadata != nil {
		meta, err := json.Marshal(done.Metadata)
		if err != nil {
			return fmt.Errorf("kanban: metadata: %w", err)
		}
		args = append(args, opt("--metadata", string(meta)))
	}
	_, err := c.kanban(ctx, "complete", nil, args...)
	return err
}

// attachFilePrefix begins the name of the temporary file Attach writes.
const attachFilePrefix = "attach-"

// RemoveLeftoverAttachFiles removes the temporary files of Attach that a process
// which died between writing and removing them left in dir; they hold the text of an
// attachment. Only regular files whose name begins like Attach's are removed, and
// only directly in dir. It must not run while an Attach into dir may be under way.
func RemoveLeftoverAttachFiles(dir string) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range entries {
		if e.Type().IsRegular() && strings.HasPrefix(e.Name(), attachFilePrefix) {
			errs = append(errs, os.Remove(filepath.Join(dir, e.Name())))
		}
	}
	return errors.Join(errs...)
}

// Attach stores content as an attachment of the card under the given name. It
// writes the content to a temporary file in dir, which the caller names, hands
// Hermes the path (Hermes copies the bytes and keeps its own copy), and removes
// the file. Hermes caps an attachment at 25 MB. Attaching is not idempotent: a
// second attachment with the same name is stored as "name (1).ext", so a caller
// that may repeat the call lists Attachments first.
func (c *Client) Attach(ctx context.Context, id, dir, name string, content []byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return &Error{Op: "attach", Class: Transient, ExitCode: -1, Err: err}
	}
	f, err := os.CreateTemp(dir, attachFilePrefix+"*")
	if err != nil {
		return &Error{Op: "attach", Class: Transient, ExitCode: -1, Err: err}
	}
	path, err := filepath.Abs(f.Name())
	defer os.Remove(f.Name())
	if err == nil {
		_, err = f.Write(content)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return &Error{Op: "attach", Class: Transient, ExitCode: -1, Err: err}
	}
	_, err = c.kanban(ctx, "attach", nil, "attach", id, path, opt("--name", name), opt("--author", c.cfg.Author))
	return err
}

// Attachments lists the attachments of a card.
func (c *Client) Attachments(ctx context.Context, id string) ([]Attachment, error) {
	out, err := c.kanban(ctx, "attachments", nil, "attachments", id, "--json")
	if err != nil {
		return nil, err
	}
	var atts []Attachment
	if err := decodeJSON("attachments", out, &atts); err != nil {
		return nil, err
	}
	return atts, nil
}

// NotifyList lists the notification subscriptions of a card; a card without
// subscriptions, or an unknown card, gives none.
func (c *Client) NotifyList(ctx context.Context, id string) ([]NotifySub, error) {
	out, err := c.kanban(ctx, "notify-list", nil, "notify-list", id, "--json")
	if err != nil {
		return nil, err
	}
	var subs []NotifySub
	if err := decodeJSON("notify-list", out, &subs); err != nil {
		return nil, err
	}
	return subs, nil
}

// Subscription is a destination for a card's terminal events: every routing and
// delivery field `notify-subscribe` accepts.
type Subscription struct {
	Platform string // required
	ChatID   string // required
	ThreadID string
	UserID   string
	// UserIDAlt is the platform's alternative user id (a Feishu union id, a Signal
	// UUID); Hermes derives the session key of a wake from it when it is set.
	UserIDAlt string
	// ChatType is dm, group, channel or thread; empty leaves Hermes' default, dm.
	ChatType string
	// ParentChatID and GuildID are routing anchors kept in the delivery metadata.
	ParentChatID string
	GuildID      string
	// NotifierProfile names the profile whose gateway owns and delivers the
	// subscription. It is required: Hermes' default is the profile of HERMES_HOME,
	// which says nothing about where a person's chat is served.
	NotifierProfile string
	// DeliveryMode is one of the Delivery constants; empty leaves Hermes' default,
	// notify.
	DeliveryMode string
}

// NotifySubscribe subscribes a destination to the card's terminal events. It
// starts caught up: events the card already has are not delivered, so subscribe
// before the event that should wake the destination.
func (c *Client) NotifySubscribe(ctx context.Context, id string, s Subscription) error {
	if s.Platform == "" || s.ChatID == "" {
		return errors.New("kanban: a subscription needs a platform and a chat id")
	}
	if s.NotifierProfile == "" {
		return errors.New("kanban: a subscription needs a notifier profile")
	}
	args := []string{"notify-subscribe", id,
		opt("--platform", s.Platform), opt("--chat-id", s.ChatID),
		opt("--notifier-profile", s.NotifierProfile)}
	for _, o := range [...]struct{ name, value string }{
		{"--thread-id", s.ThreadID},
		{"--user-id", s.UserID},
		{"--user-id-alt", s.UserIDAlt},
		{"--chat-type", s.ChatType},
		{"--parent-chat-id", s.ParentChatID},
		{"--guild-id", s.GuildID},
		{"--delivery-mode", s.DeliveryMode},
	} {
		if o.value != "" {
			args = append(args, opt(o.name, o.value))
		}
	}
	_, err := c.kanban(ctx, "notify-subscribe", nil, args...)
	return err
}
