package config

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// Modes a card can ask an alias for. A reply is not among them: it answers an
// inbound request and never names an alias.
const (
	ModeNewSession = "new_session"
	ModeMessage    = "message"
)

var aliasModes = []string{ModeNewSession, ModeMessage}

// aliasNameRE is the form of an alias once lowercased. A slash is allowed so that
// an alias can read like the node and agent it stands for.
var aliasNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._/-]{0,63}$`)

// Alias is what a card is allowed to ask of one name: the address it stands for,
// the ways it may be used and, for a new session, the presets it may start.
type Alias struct {
	// Name is lowercase: Hermes lowercases the assignee, and a card's alias is
	// lowercased the same way before it is looked up.
	Name    string
	Address adapterproto.Address
	Modes   []string
	// Presets are the presets a new_session card may name. Empty exactly when the
	// alias does not allow new_session.
	Presets []string
}

// Allows reports whether the alias may be used in the mode.
func (a Alias) Allows(mode string) bool { return slices.Contains(a.Modes, mode) }

// AllowsPreset reports whether a new_session card may name the preset.
func (a Alias) AllowsPreset(preset string) bool { return slices.Contains(a.Presets, preset) }

// Aliases is the allow-list of everything a card can make this node do: a card
// names an alias, never an address.
type Aliases map[string]Alias

// Lookup finds an alias by the name a card wrote, in any letter case.
func (t Aliases) Lookup(name string) (Alias, bool) {
	a, ok := t[strings.ToLower(name)]
	return a, ok
}

// ParseAliases reads one alias per entry, written
//
//	<name>=<address>;modes=<mode>[,<mode>];presets=<preset>[,<preset>]
//
// Modes are new_session and message. Presets are required with new_session and
// refused without it. The name is lowercased; two entries that differ only in
// letter case are a duplicate.
func ParseAliases(entries []string) (Aliases, error) {
	table := make(Aliases, len(entries))
	for i, entry := range entries {
		a, err := parseAlias(entry)
		if err != nil {
			return nil, fmt.Errorf("entry %d: %w", i+1, err)
		}
		if _, dup := table[a.Name]; dup {
			return nil, fmt.Errorf("entry %d: alias %q is listed twice", i+1, a.Name)
		}
		table[a.Name] = a
	}
	return table, nil
}

func parseAlias(entry string) (Alias, error) {
	parts := strings.Split(entry, ";")
	name, address, ok := strings.Cut(strings.TrimSpace(parts[0]), "=")
	if !ok {
		return Alias{}, fmt.Errorf("want <name>=<address>;modes=...")
	}
	a := Alias{Name: strings.ToLower(strings.TrimSpace(name)), Address: adapterproto.Address(strings.TrimSpace(address))}
	if !aliasNameRE.MatchString(a.Name) {
		return Alias{}, fmt.Errorf("alias name %q: want letters, digits and . _ / - , at most 64 characters", name)
	}
	if err := a.Address.Validate(); err != nil || strings.ContainsAny(string(a.Address), " \t\r\n") {
		return Alias{}, fmt.Errorf("alias %q: not a usable address", a.Name)
	}
	for _, p := range parts[1:] {
		key, list, ok := strings.Cut(strings.TrimSpace(p), "=")
		if !ok {
			return Alias{}, fmt.Errorf("alias %q: %q is not key=value", a.Name, p)
		}
		items := splitList(list)
		switch key {
		case "modes":
			if a.Modes != nil {
				return Alias{}, fmt.Errorf("alias %q: modes given twice", a.Name)
			}
			a.Modes = items
		case "presets":
			if a.Presets != nil {
				return Alias{}, fmt.Errorf("alias %q: presets given twice", a.Name)
			}
			a.Presets = items
		default:
			return Alias{}, fmt.Errorf("alias %q: unknown key %q", a.Name, key)
		}
	}
	return a, a.check()
}

func (a Alias) check() error {
	if len(a.Modes) == 0 {
		return fmt.Errorf("alias %q: modes is required", a.Name)
	}
	for _, m := range a.Modes {
		if !slices.Contains(aliasModes, m) {
			return fmt.Errorf("alias %q: mode %q is not one of %v", a.Name, m, aliasModes)
		}
	}
	if len(slices.Compact(slices.Sorted(slices.Values(a.Modes)))) != len(a.Modes) {
		return fmt.Errorf("alias %q: a mode is listed twice", a.Name)
	}
	if !a.Allows(ModeNewSession) {
		if len(a.Presets) > 0 {
			return fmt.Errorf("alias %q: presets belong to new_session", a.Name)
		}
		return nil
	}
	if len(a.Presets) == 0 {
		return fmt.Errorf("alias %q: new_session needs the presets it may start", a.Name)
	}
	for _, p := range a.Presets {
		if err := adapterproto.ValidatePresetName(p); err != nil {
			return fmt.Errorf("alias %q: preset %q: %w", a.Name, p, err)
		}
	}
	return nil
}

func splitList(s string) []string {
	var out []string
	for _, it := range strings.Split(s, ",") {
		if it = strings.TrimSpace(it); it != "" {
			out = append(out, it)
		}
	}
	return out
}
