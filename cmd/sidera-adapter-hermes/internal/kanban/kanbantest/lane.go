package kanbantest

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// ModeLane is a board that remembers: the cards the test put in the control
// directory (Update), changed by the commands the adapter runs the way Hermes
// 0.22.2 changes them. It answers list, show, claim, block, comment, complete,
// attach, attachments, notify-list, notify-subscribe and create (a second create
// with one idempotency key returns the first card; a card with a parent that is
// not done is todo; the subscriptions of the parents are copied), and everything
// else like ModeBoard. Every command it receives is recorded (Commands). A command
// named in a file `fail-<command>` of the control directory fails: with the file
// holding "transient" like a locked database, with "permanent" like an unknown
// or finished card.
const ModeLane = "lane"

const (
	boardFile = "board.json"
	callsFile = "calls.jsonl"
	lockFile  = "board.lock"
)

// Card is a card of the fake board.
type Card struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Body      string   `json:"body"`
	Assignee  string   `json:"assignee"`
	Status    string   `json:"status"`
	CreatedBy string   `json:"created_by"`
	SessionID string   `json:"session_id"`
	Parents   []string `json:"parents"`
	// IdempotencyKey is the key the card was created with.
	IdempotencyKey string `json:"idempotency_key"`
	Result         string `json:"result"`
	// BlockKind is the kind of the block in force.
	BlockKind string `json:"block_kind"`
	// Blocks counts how often the card was blocked, as Hermes does per kind.
	Blocks      map[string]int `json:"blocks"`
	Subs        []Sub          `json:"subs"`
	Comments    []Comment      `json:"comments"`
	Attachments []Attachment   `json:"attachments"`
	Runs        []Run          `json:"runs"`
}

// Sub is a notify subscription.
type Sub struct {
	Platform        string         `json:"platform"`
	ChatID          string         `json:"chat_id"`
	ThreadID        string         `json:"thread_id"`
	UserID          string         `json:"user_id"`
	UserIDAlt       string         `json:"user_id_alt"`
	ChatType        string         `json:"chat_type"`
	NotifierProfile string         `json:"notifier_profile"`
	DeliveryMode    string         `json:"delivery_mode"`
	Metadata        map[string]any `json:"metadata"`
}

// Comment is a comment on a card.
type Comment struct {
	Author string `json:"author"`
	Body   string `json:"body"`
}

// Attachment is a stored attachment: Content is what the file held.
type Attachment struct {
	Filename string `json:"filename"`
	Content  string `json:"content"`
	Path     string `json:"path"`
}

// Run is a closed run of a card, as a completion leaves it.
type Run struct {
	Outcome  string         `json:"outcome"`
	Summary  string         `json:"summary"`
	Metadata map[string]any `json:"metadata"`
}

// Board is everything the fake keeps.
type Board struct {
	Cards map[string]*Card `json:"cards"`
}

// Call is one command the fake received.
type Call struct {
	// Cmd is the subcommand, Args the arguments after it, Stdin what was piped in.
	Cmd   string   `json:"cmd"`
	Args  []string `json:"args"`
	Stdin string   `json:"stdin"`
}

// Update changes the board in the control directory under the board's lock, so
// that it can be done while an adapter is running.
func Update(t testing.TB, dir string, fn func(b *Board)) {
	t.Helper()
	unlock := lock(dir)
	defer unlock()
	b := load(dir)
	fn(&b)
	save(dir, b)
}

// Read returns a copy of the board.
func Read(dir string) Board {
	unlock := lock(dir)
	defer unlock()
	return load(dir)
}

// Commands returns the commands received so far, oldest first.
func Commands(dir string) []Call {
	unlock := lock(dir)
	defer unlock()
	raw, _ := os.ReadFile(filepath.Join(dir, callsFile))
	var calls []Call
	for _, line := range strings.Split(string(raw), "\n") {
		if line == "" {
			continue
		}
		var c Call
		if err := json.Unmarshal([]byte(line), &c); err == nil {
			calls = append(calls, c)
		}
	}
	return calls
}

// Fail makes the command fail from now on; class is "transient" or "permanent",
// and "" lifts it.
func Fail(t testing.TB, dir, cmd, class string) {
	t.Helper()
	path := filepath.Join(dir, "fail-"+cmd)
	if class == "" {
		_ = os.Remove(path)
		return
	}
	if err := os.WriteFile(path, []byte(class), 0o600); err != nil {
		t.Fatal(err)
	}
}

func lock(dir string) func() {
	path := filepath.Join(dir, lockFile)
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			f.Close()
			return func() { _ = os.Remove(path) }
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func load(dir string) Board {
	b := Board{Cards: map[string]*Card{}}
	if raw, err := os.ReadFile(filepath.Join(dir, boardFile)); err == nil {
		_ = json.Unmarshal(raw, &b)
	}
	if b.Cards == nil {
		b.Cards = map[string]*Card{}
	}
	return b
}

func save(dir string, b Board) {
	raw, _ := json.Marshal(b)
	if err := os.WriteFile(filepath.Join(dir, boardFile), raw, 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// lane runs one command of ModeLane.
func lane(args []string, dir string) int {
	if len(args) < 4 || args[0] != "kanban" || args[1] != "--board" {
		return recorded(args, dir)
	}
	cmd, rest := args[3], args[4:]
	switch cmd {
	case "list", "show", "claim", "block", "comment", "complete", "attach", "attachments", "notify-list", "notify-subscribe", "create":
	default:
		return recorded(args, dir)
	}
	if _, err := os.Stat(filepath.Join(dir, DownFile)); err == nil {
		fmt.Fprintln(os.Stderr, "sqlite3.OperationalError: database is locked")
		return 1
	}
	unlock := lock(dir)
	defer unlock()
	var stdin string
	if slices.Contains(rest, "-") {
		b, _ := io.ReadAll(os.Stdin)
		stdin = string(b)
	}
	call, _ := json.Marshal(Call{Cmd: cmd, Args: rest, Stdin: stdin})
	f, err := os.OpenFile(filepath.Join(dir, callsFile), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err == nil {
		_, _ = f.Write(append(call, '\n'))
		f.Close()
	}
	if class, err := os.ReadFile(filepath.Join(dir, "fail-"+cmd)); err == nil {
		if strings.TrimSpace(string(class)) == "permanent" {
			fmt.Fprintf(os.Stderr, "cannot %s %s (unknown id or terminal state)\n", cmd, firstPositional(rest))
		} else {
			fmt.Fprintln(os.Stderr, "sqlite3.OperationalError: database is locked")
		}
		return 1
	}
	b := load(dir)
	code := runLane(&b, dir, cmd, rest, stdin)
	save(dir, b)
	return code
}

func firstPositional(args []string) string {
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			return a
		}
	}
	return ""
}

// option returns the value of --name=value.
func option(args []string, name string) (string, bool) {
	prefix := name + "="
	for _, a := range args {
		if a == "--" {
			return "", false
		}
		if strings.HasPrefix(a, prefix) {
			return strings.TrimPrefix(a, prefix), true
		}
	}
	return "", false
}

// afterDashes returns the text after "--".
func afterDashes(args []string) string {
	if i := slices.Index(args, "--"); i >= 0 && i+1 < len(args) {
		return args[i+1]
	}
	return ""
}

func fail(format string, a ...any) int {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
	return 1
}

func runLane(b *Board, dir, cmd string, args []string, stdin string) int {
	now := time.Now().Unix()
	if cmd == "list" {
		status, _ := option(args, "--status")
		out := []any{}
		for _, id := range sortedIDs(b) {
			if c := b.Cards[id]; c.Status == status {
				out = append(out, cardJSON(c))
			}
		}
		return emit(out)
	}
	if cmd == "create" {
		return create(b, args, stdin)
	}
	id := firstPositional(args)
	c := b.Cards[id]
	if c == nil {
		if cmd == "notify-list" {
			return emit([]any{})
		}
		return fail("no such task: %s", id)
	}
	switch cmd {
	case "show":
		runs := []any{}
		latest := any(nil)
		for i, r := range c.Runs {
			var meta any
			if r.Metadata != nil {
				meta = r.Metadata
			}
			runs = append(runs, map[string]any{"id": i + 1, "profile": nil, "status": "done", "outcome": r.Outcome,
				"summary": r.Summary, "error": nil, "metadata": meta, "started_at": now, "ended_at": now})
			latest = r.Summary
		}
		comments := []any{}
		for _, cm := range c.Comments {
			comments = append(comments, map[string]any{"author": cm.Author, "body": cm.Body, "created_at": now})
		}
		return emit(map[string]any{"task": cardJSON(c), "latest_summary": latest, "parents": nonNil(c.Parents),
			"children": []string{}, "comments": comments, "events": []any{}, "runs": runs})
	case "claim":
		if c.Status != "ready" {
			return fail("cannot claim %s: status=%s lock=(none)", id, c.Status)
		}
		c.Status = "running"
		fmt.Println("claimed", id)
	case "block":
		if c.Status != "running" && c.Status != "ready" {
			return fail("cannot block %s", id)
		}
		kind, _ := option(args, "--kind")
		if reason := afterDashes(args); reason != "" {
			c.Comments = append(c.Comments, Comment{Author: "sidera", Body: "BLOCKED: " + reason})
		}
		if c.Blocks == nil {
			c.Blocks = map[string]int{}
		}
		c.Blocks[kind]++
		if c.Blocks[kind] > 1 {
			c.Status = "triage"
		} else {
			c.Status = "blocked"
		}
		c.BlockKind = kind
		fmt.Println("blocked", id)
	case "comment":
		author, _ := option(args, "--author")
		c.Comments = append(c.Comments, Comment{Author: author, Body: afterDashes(args)})
		fmt.Println("commented", id)
	case "complete":
		if !slices.Contains([]string{"running", "ready", "blocked", "review"}, c.Status) {
			return fail("cannot complete %s (unknown id or terminal state)", id)
		}
		summary, hasSummary := option(args, "--summary")
		result, hasResult := option(args, "--result")
		if !hasSummary && !hasResult {
			return fail("cannot complete %s: completion blocked: %s has no result or summary evidence.", id, id)
		}
		run := Run{Outcome: "completed", Summary: summary}
		if !hasSummary {
			run.Summary = result
		}
		if raw, ok := option(args, "--metadata"); ok {
			if err := json.Unmarshal([]byte(raw), &run.Metadata); err != nil {
				fmt.Fprintln(os.Stderr, "kanban: --metadata:", err)
				return 2
			}
		}
		c.Runs = append(c.Runs, run)
		c.Result, c.Status, c.BlockKind = result, "done", ""
		fmt.Println("completed", id)
	case "attach":
		name, _ := option(args, "--name")
		path := ""
		for _, a := range args[1:] {
			if !strings.HasPrefix(a, "-") {
				path = a
				break
			}
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return fail("kanban: no such file: %s", path)
		}
		stored := name
		for n := 1; slices.ContainsFunc(c.Attachments, func(a Attachment) bool { return a.Filename == stored }); n++ {
			ext := filepath.Ext(name)
			stored = strings.TrimSuffix(name, ext) + " (" + strconv.Itoa(n) + ")" + ext
		}
		dst := filepath.Join(dir, "attachments", id, stored)
		_ = os.MkdirAll(filepath.Dir(dst), 0o700)
		_ = os.WriteFile(dst, content, 0o600)
		c.Attachments = append(c.Attachments, Attachment{Filename: stored, Content: string(content), Path: dst})
		fmt.Println("attached", stored)
	case "attachments":
		out := []any{}
		for i, a := range c.Attachments {
			out = append(out, map[string]any{"id": i + 1, "filename": a.Filename, "content_type": "text/plain",
				"size": len(a.Content), "uploaded_by": "sidera", "stored_path": a.Path, "created_at": now})
		}
		return emit(out)
	case "notify-list":
		out := []any{}
		for _, s := range c.Subs {
			meta := s.Metadata
			if meta == nil {
				meta = map[string]any{}
			}
			out = append(out, map[string]any{"task_id": id, "platform": s.Platform, "chat_id": s.ChatID,
				"thread_id": s.ThreadID, "user_id": nilIfEmpty(s.UserID), "user_id_alt": nilIfEmpty(s.UserIDAlt),
				"chat_type": s.ChatType, "notifier_profile": s.NotifierProfile, "delivery_mode": s.DeliveryMode,
				"delivery_metadata": meta, "created_at": now, "last_event_id": 0})
		}
		return emit(out)
	case "notify-subscribe":
		s := Sub{DeliveryMode: "notify+wake", Metadata: map[string]any{}}
		s.Platform, _ = option(args, "--platform")
		s.ChatID, _ = option(args, "--chat-id")
		s.NotifierProfile, _ = option(args, "--notifier-profile")
		s.ThreadID, _ = option(args, "--thread-id")
		s.ChatType, _ = option(args, "--chat-type")
		c.Subs = append(c.Subs, s)
		fmt.Println("subscribed", id)
	}
	return 0
}

// create makes a card the way `hermes kanban create --json` does.
func create(b *Board, args []string, stdin string) int {
	key, _ := option(args, "--idempotency-key")
	if key != "" {
		for _, id := range sortedIDs(b) {
			if b.Cards[id].IdempotencyKey == key {
				return emit(cardJSON(b.Cards[id]))
			}
		}
	}
	c := &Card{ID: fmt.Sprintf("n_%d", len(b.Cards)+1), Title: afterDashes(args), Body: stdin, Status: "ready", IdempotencyKey: key}
	c.Assignee, _ = option(args, "--assignee")
	c.CreatedBy, _ = option(args, "--created-by")
	for _, a := range args {
		if a == "--" {
			break
		}
		if p, ok := strings.CutPrefix(a, "--parent="); ok {
			parent := b.Cards[p]
			if parent == nil {
				return fail("no such task: %s", p)
			}
			c.Parents = append(c.Parents, p)
			c.Subs = append(c.Subs, parent.Subs...)
			if parent.Status != "done" {
				c.Status = "todo"
			}
		}
	}
	b.Cards[c.ID] = c
	return emit(cardJSON(c))
}

func sortedIDs(b *Board) []string {
	ids := make([]string, 0, len(b.Cards))
	for id := range b.Cards {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func cardJSON(c *Card) map[string]any {
	return map[string]any{"id": c.ID, "title": c.Title, "body": c.Body, "assignee": c.Assignee, "status": c.Status,
		"priority": 0, "created_by": c.CreatedBy, "session_id": nilIfEmpty(c.SessionID), "result": nilIfEmpty(c.Result),
		"created_at": 1, "started_at": nil, "completed_at": nil, "last_failure_error": nil}
}

func emit(v any) int {
	_ = json.NewEncoder(os.Stdout).Encode(v)
	return 0
}
