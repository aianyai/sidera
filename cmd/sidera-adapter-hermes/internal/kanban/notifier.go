package kanban

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// NotifierSetting is the Hermes configuration key that switches the Kanban notifier
// of a gateway on. The notifier is what wakes the conversation that subscribed to a
// card when the card is completed; with it off the card is still written, and nothing
// wakes the agent.
const NotifierSetting = "kanban.notify_in_gateway"

// NotifierEnabled reports whether the gateway of the profile runs the Kanban
// notifier, as `hermes -p <profile> config get kanban.notify_in_gateway` resolves it:
// the profile's own value, or Hermes' default (on) when the profile sets none. It
// reads the configuration; it does not say that a gateway is running.
func (c *Client) NotifierEnabled(ctx context.Context, profile string) (bool, error) {
	out, err := c.run(ctx, "config get", nil, []string{"-p", profile, "config", "get", NotifierSetting})
	if err != nil {
		return false, err
	}
	enabled, err := parseNotifierSetting(string(out))
	if err != nil {
		return false, &Error{Op: "config get", Class: Transient, ExitCode: -1,
			Err: fmt.Errorf("%w: %v", ErrUnexpectedOutput, err)}
	}
	return enabled, nil
}

// DefaultProfile is the profile of Hermes' root directory. The one gateway that serves
// every profile is normally started from it.
const DefaultProfile = "default"

// NotifierDisabled returns those of the profiles, and the default profile, whose
// configuration has the notifier off. The notifier is a task of a gateway process and
// reads the configuration of the profile the process was started from. By default one
// gateway serves every profile and delivers all their notifications, started from the
// default profile, so that profile's setting governs the others; a profile that runs its
// own gateway (it sets gateway.standalone, or another gateway keeps the shared one from
// taking it over) is governed by its own. The command line cannot say which of the two
// arrangements runs, so both settings are read for every profile asked about and either
// one being off is reported. The profiles come in the order given, the default profile
// last when it was not among them.
//
// Every profile is read, and a profile that cannot be read does not hide what the others
// show: the list holds the profiles known to be off, and the error, when not nil, joins
// the failures, each naming its profile. A caller that gets both uses the list and
// reports the error. A profile that is not in the list is on or unread; only a nil error
// says it is on.
func (c *Client) NotifierDisabled(ctx context.Context, profiles []string) ([]string, error) {
	candidates := slices.Clone(profiles)
	if !slices.Contains(candidates, DefaultProfile) {
		candidates = append(candidates, DefaultProfile)
	}
	var off []string
	var failed []error
	for _, p := range candidates {
		enabled, err := c.NotifierEnabled(ctx, p)
		if err != nil {
			failed = append(failed, fmt.Errorf("profile %s: %w", p, err))
			if ctx.Err() != nil {
				break
			}
			continue
		}
		if !enabled {
			off = append(off, p)
		}
	}
	return off, errors.Join(failed...)
}

// parseNotifierSetting reads what `config get` prints for the setting. Hermes prints
// a boolean as true or false and any other value as Python's str; the gateway tests
// the value for truth, so the number 0 and an empty value (printed null) switch the
// notifier off and 1 switches it on. Any other text is not a value this client
// knows the meaning of, and is refused rather than guessed.
func parseNotifierSetting(out string) (bool, error) {
	switch v := strings.TrimSpace(out); v {
	case "true", "1":
		return true, nil
	case "false", "0", "null":
		return false, nil
	default:
		return false, fmt.Errorf("%s is %q, want true or false", NotifierSetting, firstLine(v))
	}
}
