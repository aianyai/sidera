package kanban

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

// The Hermes releases this client is built and recorded against: from
// SupportedFrom up to, and excluding, SupportedBefore. Hermes is 0.x and changes
// its command line and JSON between minor releases; a release outside the range
// is refused until the samples in testdata are re-recorded against it.
const (
	SupportedFrom   = "0.22.2"
	SupportedBefore = "0.23.0"
)

// Version is a Hermes release number. A build suffix ("+feishu") is dropped.
type Version struct{ Major, Minor, Patch int }

func (v Version) String() string { return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch) }

func (v Version) less(o Version) bool {
	if v.Major != o.Major {
		return v.Major < o.Major
	}
	if v.Minor != o.Minor {
		return v.Minor < o.Minor
	}
	return v.Patch < o.Patch
}

var versionLine = regexp.MustCompile(`(?m)^\s*Hermes Agent v(\d+)\.(\d+)\.(\d+)`)

// ParseVersion reads the number out of the output of `hermes --version`, whose
// first line looks like "Hermes Agent v0.22.2+feishu (2026.9.24) · stable · ...".
func ParseVersion(output string) (Version, error) {
	m := versionLine.FindStringSubmatch(output)
	if m == nil {
		return Version{}, fmt.Errorf("kanban: no hermes version in %q", firstLine(output))
	}
	var n [3]int
	for i := range n {
		v, err := strconv.Atoi(m[i+1])
		if err != nil {
			return Version{}, fmt.Errorf("kanban: hermes version %q: %w", m[0], err)
		}
		n[i] = v
	}
	return Version{n[0], n[1], n[2]}, nil
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	if len(line) > 80 {
		line = line[:80]
	}
	return line
}

var (
	supportedFrom   = mustVersion(SupportedFrom)
	supportedBefore = mustVersion(SupportedBefore)
)

func mustVersion(s string) Version {
	v, err := ParseVersion("Hermes Agent v" + s)
	if err != nil {
		panic(err)
	}
	return v
}

// CheckVersion refuses a version outside the supported range, naming both.
func CheckVersion(v Version) error {
	if v.less(supportedFrom) || !v.less(supportedBefore) {
		return fmt.Errorf("kanban: hermes %s is outside the supported range %s up to but excluding %s",
			v, SupportedFrom, SupportedBefore)
	}
	return nil
}

// CheckExecutable refuses a Windows batch shim. Arguments to a .cmd or .bat file
// are interpreted by cmd.exe, so metacharacters in text the client passes would
// run as commands; only a real executable takes its arguments verbatim. The rule
// is a function of the resolved path and the operating system so that it is
// checked on any machine.
func CheckExecutable(goos, resolved string) error {
	if goos != "windows" {
		return nil
	}
	switch strings.ToLower(filepath.Ext(strings.ReplaceAll(resolved, `\`, "/"))) {
	case ".cmd", ".bat":
		return fmt.Errorf("kanban: %s is a batch shim; cmd.exe would interpret the metacharacters "+
			"of every argument, so the adapter needs hermes.exe", resolved)
	}
	return nil
}

// Version runs `hermes --version`.
func (c *Client) Version(ctx context.Context) (Version, error) {
	out, err := c.run(ctx, "version", nil, []string{"--version"})
	if err != nil {
		return Version{}, err
	}
	return ParseVersion(string(out))
}

// reservedProfile reports whether name is, or is shaped like, an assignee of the
// adapter's cards. The Hermes dispatcher tests every card's assignee with
// profile_exists on each tick and starts a worker for the card when a profile of
// that name exists, so such a profile would run the lane's cards as Hermes tasks.
func reservedProfile(name string) bool {
	name = strings.ToLower(name)
	return name == "sidera" || name == "sidera-inbox" || strings.HasPrefix(name, "sidera:")
}

// CheckProfiles refuses a Hermes profile on disk that the dispatcher would take
// for the assignee of the adapter's cards. Names that are only the assignees of
// cards (not on disk) are what the adapter itself uses and pass.
func CheckProfiles(assignees []Assignee) error {
	var clash []string
	for _, a := range assignees {
		if a.OnDisk && reservedProfile(a.Name) {
			clash = append(clash, a.Name)
		}
	}
	if len(clash) > 0 {
		return fmt.Errorf("kanban: hermes profile(s) %s exist: the Hermes dispatcher would start a worker "+
			"for the cards assigned to them; delete or rename them", strings.Join(clash, ", "))
	}
	return nil
}

// Assignees lists the profiles on disk and the assignees of the board's cards.
func (c *Client) Assignees(ctx context.Context) ([]Assignee, error) {
	out, err := c.kanban(ctx, "assignees", nil, "assignees", "--json")
	if err != nil {
		return nil, err
	}
	var as []Assignee
	if err := decodeJSON("assignees", out, &as); err != nil {
		return nil, err
	}
	return as, nil
}

// BoardExists reports whether the configured board is among the active boards.
func (c *Client) BoardExists(ctx context.Context) (bool, error) {
	out, err := c.kanban(ctx, "boards", nil, "boards", "list", "--json")
	if err != nil {
		return false, err
	}
	var boards []struct {
		Slug string `json:"slug"`
	}
	if err := decodeJSON("boards", out, &boards); err != nil {
		return false, err
	}
	for _, b := range boards {
		if b.Slug == c.cfg.Board {
			return true, nil
		}
	}
	return false, nil
}

// Probe checks everything the adapter needs of Hermes before it takes a card: a
// hermes that is not a Windows batch shim, in the supported version range, with
// the board in place and no profile that the dispatcher would confuse with the
// lane. The returned error says which check failed.
func (c *Client) Probe(ctx context.Context) error {
	resolved, err := exec.LookPath(c.cfg.Executable)
	if err != nil {
		return fmt.Errorf("kanban: hermes executable: %w", err)
	}
	if err := CheckExecutable(runtime.GOOS, resolved); err != nil {
		return err
	}
	v, err := c.Version(ctx)
	if err != nil {
		return err
	}
	if err := CheckVersion(v); err != nil {
		return err
	}
	ok, err := c.BoardExists(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("kanban: board %q does not exist; create it with `hermes kanban boards create %s`",
			c.cfg.Board, c.cfg.Board)
	}
	as, err := c.Assignees(ctx)
	if err != nil {
		return err
	}
	return CheckProfiles(as)
}
