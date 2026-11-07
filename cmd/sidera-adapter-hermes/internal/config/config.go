// Package config is the configuration of one Hermes adapter instance: what the
// owner decides about the board the adapter serves, where wake-ups go when a card
// has no conversation, which names a card may use, and the timings.
//
// The daemon hands an adapter its configuration as flat values (a string, an
// integer, a boolean or a list of strings), so every field here is one such value
// and the alias table is a list of one-line entries.
package config

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// The keys of the instance configuration.
const (
	KeyHermesPath       = "hermes_path"
	KeyHermesHome       = "hermes_home"
	KeyKanbanHome       = "kanban_home"
	KeyBoard            = "board"
	KeyDisplayName      = "display_name"
	KeyHomeProfile      = "home_profile"
	KeyHomePlatform     = "home_platform"
	KeyHomeChatID       = "home_chat_id"
	KeyHomeChatType     = "home_chat_type"
	KeyHomeThreadID     = "home_thread_id"
	KeyHomeUserID       = "home_user_id"
	KeyHomeUserIDAlt    = "home_user_id_alt"
	KeyHomeParentChatID = "home_parent_chat_id"
	KeyHomeGuildID      = "home_guild_id"
	KeyAliases          = "aliases"
	KeyCreatorProfiles  = "creator_profiles"
	KeyNoReplyTimeout   = "no_reply_timeout"
	KeyClaimTTL         = "claim_ttl"
	KeySessionRetention = "session_retention"
	KeyCloseWindow      = "close_window"
	KeyEndpointIdle     = "endpoint_idle"
	KeyPollInterval     = "poll_interval"
	KeyMaxEndpoints     = "max_endpoints"
	KeyMaxOpenSessions  = "max_open_sessions"
)

// The defaults.
const (
	DefaultHermesPath = "hermes"

	// DefaultNoReplyTimeout is how long a card waits for the remote before it is
	// blocked once.
	DefaultNoReplyTimeout = 24 * time.Hour
	// DefaultClaimTTL is how long a claim on a card lasts. A claim made from the
	// command line cannot be renewed and a second expiry makes Hermes give the
	// card up, so the TTL is far longer than any wait; the adapter's own timer
	// decides when a wait has gone on too long.
	DefaultClaimTTL = 7 * 24 * time.Hour
	// DefaultSessionRetention is how long a remote session of a conversation is
	// kept after the last card that used it.
	DefaultSessionRetention = 7 * 24 * time.Hour
	// DefaultCloseWindow is how long the remote session of a cron or orphan card
	// is kept after the card finished, for a late message.
	DefaultCloseWindow = time.Hour
	// DefaultEndpointIdle is how long a conversation endpoint may go unused
	// before it is retired. It matches the age at which Hermes clears the
	// subscriptions of finished cards.
	DefaultEndpointIdle = 30 * 24 * time.Hour
	// DefaultPollInterval is the pause between looks at the board. Each look
	// starts a Hermes process, which costs a fraction of a second, and a new card
	// waiting ten seconds is invisible next to the hours a delegation takes.
	DefaultPollInterval = 10 * time.Second

	DefaultMaxEndpoints    = 64
	DefaultMaxOpenSessions = 32

	// MinMaxEndpoints leaves room for the instance endpoint, the orphan endpoint
	// and one conversation.
	MinMaxEndpoints = 4
	// MaxMaxEndpoints is the node's own cap on endpoints.
	MaxMaxEndpoints = 256

	minPollInterval = time.Second
)

var boardRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// profileRE is the grammar of a Hermes profile name (hermes_constants.PROFILE_ID_RE,
// Hermes 0.22.2). Hermes lowercases the name given to -p before it applies the rule.
var profileRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// ValidateProfileName refuses what Hermes would not take for a profile name. A name
// reaches Hermes as the value of -p, where one that begins with a dash would be read as
// an option, so the rule is applied before any command is built from it.
func ValidateProfileName(name string) error {
	if !profileRE.MatchString(strings.ToLower(name)) {
		return fmt.Errorf("%q is not a Hermes profile name: lowercase letters, digits, - and _, starting with a letter or digit, up to 64 characters", name)
	}
	return nil
}

var chatTypes = []string{"dm", "group", "channel", "thread"}

// ChatTypes lists the values of the chat type of a notify subscription.
func ChatTypes() []string { return slices.Clone(chatTypes) }

// Target says where a wake-up goes: the fields of a Hermes notify subscription,
// always with the delivery mode that wakes the agent.
type Target struct {
	// Profile is the profile whose gateway owns the subscription.
	Profile      string `json:"profile"`
	Platform     string `json:"platform"`
	ChatID       string `json:"chat_id"`
	ThreadID     string `json:"thread_id,omitempty"`
	ChatType     string `json:"chat_type,omitempty"`
	UserID       string `json:"user_id,omitempty"`
	UserIDAlt    string `json:"user_id_alt,omitempty"`
	ParentChatID string `json:"parent_chat_id,omitempty"`
	GuildID      string `json:"guild_id,omitempty"`
}

// Validate checks that the target can be subscribed to.
func (t Target) Validate() error {
	switch {
	case t.Profile == "":
		return fmt.Errorf("profile is required")
	case t.Platform == "":
		return fmt.Errorf("platform is required")
	case t.ChatID == "":
		return fmt.Errorf("chat id is required")
	case t.ChatType != "" && !slices.Contains(chatTypes, t.ChatType):
		return fmt.Errorf("chat type %q is not one of %v", t.ChatType, chatTypes)
	}
	return nil
}

// Config is the configuration of one adapter instance.
type Config struct {
	// HermesPath is the Hermes executable, looked up on the path when it is a
	// bare name.
	HermesPath string
	// HermesHome is given to every Hermes process: the adapter runs in the
	// daemon's service environment, which need not have the owner's shell
	// settings.
	HermesHome string
	// KanbanHome is the optional HERMES_KANBAN_HOME.
	KanbanHome string
	// Board is the slug of the one board the instance serves.
	Board string
	// DisplayName is what the instance endpoint is called in the directory. Empty
	// leaves the choice to the adapter, which names it Hermes.
	DisplayName string
	// Home is where a wake-up goes when a card has no conversation to wake and
	// where an inbound request from a stranger lands.
	Home Target
	// Aliases are the only names a card may address.
	Aliases Aliases
	// CreatorProfiles, when not empty, are the profiles whose cards are served.
	CreatorProfiles []string

	NoReplyTimeout   time.Duration
	ClaimTTL         time.Duration
	SessionRetention time.Duration
	CloseWindow      time.Duration
	EndpointIdle     time.Duration
	PollInterval     time.Duration
	// MaxEndpoints bounds the endpoints the adapter holds, the instance endpoint
	// and the orphan endpoint included.
	MaxEndpoints int
	// MaxOpenSessions bounds the remote sessions the adapter keeps open.
	MaxOpenSessions int
}

// Decode reads the instance configuration with its defaults and checks it. A key
// that is not a field is refused: the alias table bounds what a card can do, so a
// misspelled key must not fall back to a default silently.
func Decode(values adapterproto.ConfigValues) (Config, error) {
	r := reader{values: values, used: map[string]bool{}}
	c := Config{
		HermesPath:       r.str(KeyHermesPath, DefaultHermesPath),
		HermesHome:       r.str(KeyHermesHome, ""),
		KanbanHome:       r.str(KeyKanbanHome, ""),
		Board:            r.str(KeyBoard, ""),
		DisplayName:      r.str(KeyDisplayName, ""),
		CreatorProfiles:  r.list(KeyCreatorProfiles),
		NoReplyTimeout:   r.duration(KeyNoReplyTimeout, DefaultNoReplyTimeout, time.Minute),
		ClaimTTL:         r.duration(KeyClaimTTL, DefaultClaimTTL, time.Hour),
		SessionRetention: r.duration(KeySessionRetention, DefaultSessionRetention, time.Minute),
		CloseWindow:      r.duration(KeyCloseWindow, DefaultCloseWindow, time.Minute),
		EndpointIdle:     r.duration(KeyEndpointIdle, DefaultEndpointIdle, time.Hour),
		PollInterval:     r.duration(KeyPollInterval, DefaultPollInterval, minPollInterval),
		MaxEndpoints:     r.integer(KeyMaxEndpoints, DefaultMaxEndpoints, MinMaxEndpoints, MaxMaxEndpoints),
		MaxOpenSessions:  r.integer(KeyMaxOpenSessions, DefaultMaxOpenSessions, 1, 1000),
		Home: Target{
			Profile:      r.str(KeyHomeProfile, ""),
			Platform:     r.str(KeyHomePlatform, ""),
			ChatID:       r.str(KeyHomeChatID, ""),
			ChatType:     r.str(KeyHomeChatType, ""),
			ThreadID:     r.str(KeyHomeThreadID, ""),
			UserID:       r.str(KeyHomeUserID, ""),
			UserIDAlt:    r.str(KeyHomeUserIDAlt, ""),
			ParentChatID: r.str(KeyHomeParentChatID, ""),
			GuildID:      r.str(KeyHomeGuildID, ""),
		},
	}
	aliasEntries := r.list(KeyAliases)
	for k := range values {
		if !r.used[k] {
			r.problem("%s: not a setting of this adapter", k)
		}
	}
	if len(r.problems) > 0 {
		return Config{}, r.err()
	}

	var err error
	if c.Aliases, err = ParseAliases(aliasEntries); err != nil {
		r.problem("%s: %v", KeyAliases, err)
	}
	if len(c.Aliases) == 0 && err == nil {
		r.problem("%s: at least one alias is required, or no card could be delivered", KeyAliases)
	}
	if !filepath.IsAbs(c.HermesHome) {
		r.problem("%s: an absolute path is required", KeyHermesHome)
	}
	if c.KanbanHome != "" && !filepath.IsAbs(c.KanbanHome) {
		r.problem("%s: must be an absolute path", KeyKanbanHome)
	}
	if c.DisplayName != "" {
		if err := adapterproto.ValidateDisplayName(c.DisplayName); err != nil {
			r.problem("%s: %v", KeyDisplayName, err)
		}
	}
	if !boardRE.MatchString(c.Board) {
		r.problem("%s: want a board slug (lowercase letters, digits, - and _)", KeyBoard)
	}
	if err := c.Home.Validate(); err != nil {
		r.problem("home_*: %v", err)
	} else if err := ValidateProfileName(c.Home.Profile); err != nil {
		r.problem("%s: %v", KeyHomeProfile, err)
	}
	for i, p := range c.CreatorProfiles {
		if err := ValidateProfileName(p); err != nil {
			r.problem("%s: %v", KeyCreatorProfiles, err)
		}
		c.CreatorProfiles[i] = strings.ToLower(p)
	}
	if c.ClaimTTL <= c.NoReplyTimeout {
		r.problem("%s must be longer than %s, or Hermes would see the claim expire during a wait", KeyClaimTTL, KeyNoReplyTimeout)
	}
	if len(r.problems) > 0 {
		return Config{}, r.err()
	}
	return c, nil
}

// WakeProfiles are the profiles whose gateways wake the conversations this instance
// answers: the home profile and, when cards are limited to some creators, those
// profiles. A card of any other profile is served only when creator_profiles is empty,
// and its profile is then not known before the card exists.
func (c Config) WakeProfiles() []string {
	profiles := append([]string{c.Home.Profile}, c.CreatorProfiles...)
	slices.Sort(profiles)
	return slices.Compact(profiles)
}

// reader collects every problem of a configuration instead of stopping at the
// first, so that the owner fixes them in one pass.
type reader struct {
	values   adapterproto.ConfigValues
	used     map[string]bool
	problems []string
}

func (r *reader) problem(format string, args ...any) {
	r.problems = append(r.problems, fmt.Sprintf(format, args...))
}

func (r *reader) err() error {
	return fmt.Errorf("instance configuration: %s", strings.Join(r.problems, "; "))
}

func (r *reader) raw(key string) (json.RawMessage, bool) {
	r.used[key] = true
	v, ok := r.values[key]
	return v, ok
}

func (r *reader) str(key, def string) string {
	raw, ok := r.raw(key)
	if !ok {
		return def
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		r.problem("%s: a string is expected", key)
		return def
	}
	return s
}

func (r *reader) list(key string) []string {
	raw, ok := r.raw(key)
	if !ok {
		return nil
	}
	var l []string
	if err := json.Unmarshal(raw, &l); err != nil {
		r.problem("%s: a list of strings is expected", key)
	}
	return l
}

func (r *reader) integer(key string, def, min, max int) int {
	raw, ok := r.raw(key)
	if !ok {
		return def
	}
	var n int
	if err := json.Unmarshal(raw, &n); err != nil {
		r.problem("%s: an integer is expected", key)
		return def
	}
	if n < min || n > max {
		r.problem("%s: %d is outside %d..%d", key, n, min, max)
	}
	return n
}

// duration reads a Go duration such as "90m" or "168h".
func (r *reader) duration(key string, def, min time.Duration) time.Duration {
	s := r.str(key, "")
	if s == "" {
		return def
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		r.problem("%s: %q is not a duration such as 90m or 168h", key, s)
		return def
	}
	if d < min {
		r.problem("%s: %s is below the minimum %s", key, d, min)
	}
	return d
}
