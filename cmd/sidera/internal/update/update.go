// Package update is the converging run of `sidera update`: it brings the programs
// next to the one that is running, the daemon and the installed adapters to one
// release, doing only the steps that are missing, so that a run that was cut short
// is finished by running it again.
//
// Getting the new files depends on how the installation was made (a channel); only
// the programs of a release archive are replaced here. Making the running node
// follow (restart the daemon, install the adapters) does not depend on the channel.
// The subpackages do the parts: fetch reaches GitHub, verify believes a release,
// unpack opens an archive, swap replaces the programs. README.md has the rules.
package update

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/aianyai/sidera/cmd/sidera/internal/update/fetch"
	"github.com/aianyai/sidera/internal/minisign"
	"github.com/aianyai/sidera/internal/release"
)

// Node is the running daemon, as far as an update needs it. The CLI implements it
// over the management interface; the methods it relies on are the ones the reference
// promises to keep readable across versions.
type Node interface {
	// Status reads the daemon's version; ErrNotRunning when no daemon runs.
	Status(ctx context.Context) (NodeStatus, error)
	// Restart restarts the daemon, requiring the program it starts to report
	// expectVersion, and returns once the new daemon runs.
	Restart(ctx context.Context, expectVersion string) error
	// Adapters lists the installed adapters.
	Adapters(ctx context.Context) ([]Adapter, error)
	// Install installs the adapter package in dir, an absolute path.
	Install(ctx context.Context, dir string) (Installed, error)
	// Instances lists the adapter instances.
	Instances(ctx context.Context) ([]Instance, error)
	// RefreshSetup runs the refresh of the client setup of one instance, the same
	// refresh as `sidera adapter setup <instance> --refresh`. An instance whose adapter
	// declares no setup answers a report with the status SetupSkipped; an error is a
	// refresh that could not be made at all (the report of a refresh that ran and had an
	// item fail is a report with the status SetupError). The run fills the report's
	// Instance and Adapter.
	RefreshSetup(ctx context.Context, instance string) (SetupReport, error)
}

// ErrNotRunning is what Node.Status reports when no daemon runs.
var ErrNotRunning = errors.New("no daemon is running")

// NodeStatus is what update reads of a running daemon.
type NodeStatus struct {
	Version string
}

// Adapter is an installed adapter.
type Adapter struct {
	ID      string
	Version string
	// Faulted is true when the installed package cannot run: the daemon reports a fault
	// of the package itself, or of one of the adapter's instances for the sake of it.
	// Its version then says nothing of what runs, and it may be empty.
	Faulted bool
}

// Instance is an adapter instance.
type Instance struct {
	ID      string
	Adapter string
}

// Installed is the answer to an installation.
type Installed struct {
	ID             string
	Version        string
	AlreadyCurrent bool
	Replaced       bool
}

// Config is everything a run is given. Nothing in it comes from a flag or the
// environment except what the command line program fills from its own facts.
type Config struct {
	// Installed is what the running program was built from.
	Installed release.Info
	// Program is the path of the running program, with its links kept: the file to
	// replace, and the directory its companions are looked for in.
	Program string
	// GOOS and GOARCH name the platform whose archive is wanted.
	GOOS, GOARCH string

	// Fetch reaches the releases; Keys are the keys they are believed by. The
	// command passes fetch.New and verify.EmbeddedKeys; a test passes a local
	// server and a throwaway key.
	Fetch *fetch.Client
	Keys  []minisign.PublicKey

	// Node is the daemon of the data directory of this run; nil for Check.
	Node Node

	// CacheRoot is the directory the downloads and the update lock live in,
	// <user cache dir>/sidera-update. Random names the temporary files of the
	// replacement.
	CacheRoot string
	Random    io.Reader

	// Smoke runs a program as `sidera version --json` and says what it reports;
	// nil runs it as a process.
	Smoke func(ctx context.Context, program string) (release.Info, error)

	// Confirm is asked once, after the release is verified and unpacked and before
	// anything is changed; nil goes ahead. A false answer ends the run with
	// ErrDeclined.
	Confirm func(Plan) (bool, error)
	// Step, when not nil, is told what the run is about to do that takes a while.
	Step func(string)
}

// ErrDeclined is the end of a run whose confirmation was answered no.
var ErrDeclined = errors.New("not confirmed; nothing was changed")

// Plan is what a run is about to do, shown for the confirmation.
type Plan struct {
	Current, Target, ReleaseURL string
	// Replace lists the programs to replace, as absolute paths.
	Replace []string
	// Restart is true when the daemon, which runs DaemonVersion, is to be restarted.
	Restart       bool
	DaemonVersion string
	// Daemon is false when no daemon runs.
	Daemon bool
	// Adapters lists the installed adapters that are to be installed; Missing those
	// whose package the release does not have.
	Adapters []string
	Missing  []string
	// Faulted lists the adapters whose installed package cannot run and that are to be
	// installed again whatever their version; FaultedMissing those whose package the
	// release does not have, which the owner has to install by hand.
	Faulted        []string
	FaultedMissing []string
	// RefreshSetup is true when the client setup of the instances that have one is to be
	// refreshed, which every run does while the daemon runs.
	RefreshSetup bool
}

// Release is what is known of the release asked for and of the running program's
// standing towards it; a check and a run both report it.
type Release struct {
	Current    string `json:"current"`
	Target     string `json:"target"`
	ReleaseURL string `json:"release"`
	// Admitted is true when the release's signed floor allows the running program
	// to update to it.
	Admitted bool   `json:"admitted"`
	Floor    string `json:"floor,omitempty"`
}

// Verdict is what a check reports: whether a newer release is available and
// admitted, and the first reason a run would stop. It looks at nothing but the
// release and the running program, so it says nothing of the daemon or the adapters,
// whatever an earlier run left behind there.
type Verdict struct {
	Release
	// UpdateAvailable is true when the target is newer than the running program.
	UpdateAvailable bool `json:"update_available"`
	// Refusal is the first reason a run would stop, or empty.
	Refusal string `json:"refusal,omitempty"`
}

// Report is what is known of a run at the point it ended.
type Report struct {
	Release
	// AlreadyCurrent is true when the programs, the daemon and the adapters were all
	// at the target and a run had nothing to do: no instance's client setup was
	// refreshed or failed to be either (drift is not something a run does). It is never
	// true while no daemon runs, because the adapters cannot be looked at then.
	AlreadyCurrent bool            `json:"already_current,omitempty"`
	Program        ProgramReport   `json:"program"`
	Daemon         DaemonReport    `json:"daemon"`
	Adapters       []AdapterReport `json:"adapters,omitempty"`
	// Setup is what became of the client setup of each adapter instance, in the order
	// the daemon lists them. It is empty while no daemon runs.
	Setup []SetupReport `json:"setup,omitempty"`
}

// ProgramReport is what became of the programs.
type ProgramReport struct {
	// Updated is true when the programs were replaced; Replaced lists them.
	Updated  bool     `json:"updated"`
	Replaced []string `json:"replaced,omitempty"`
	// ConsoleMissing is true when no sidera-adapter-console was found next to the
	// program, which an update does not create.
	ConsoleMissing bool `json:"console_missing,omitempty"`
	// Restored lists programs that an earlier, cut short update had taken from
	// their names and this run put back; Left the files of earlier updates that a
	// process still runs from, so that they could not be removed.
	Restored []string `json:"restored,omitempty"`
	Left     []string `json:"left,omitempty"`
}

// DaemonReport is what became of the daemon.
type DaemonReport struct {
	Running   bool   `json:"running"`
	Previous  string `json:"previous_version,omitempty"`
	Restarted bool   `json:"restarted"`
	Version   string `json:"version,omitempty"`
}

// AdapterReport is what became of one installed adapter.
type AdapterReport struct {
	ID       string `json:"id"`
	Previous string `json:"previous_version"`
	Version  string `json:"version,omitempty"`
	Replaced bool   `json:"replaced,omitempty"`
	// Faulted is true when the installed package could not run, which is why it is
	// installed again whatever its version, or why it is skipped.
	Faulted bool `json:"faulted,omitempty"`
	// Skipped says why the adapter was left as it is, or is empty.
	Skipped string `json:"skipped,omitempty"`
	// Error is why its installation failed, or is empty.
	Error string `json:"error,omitempty"`
}

// SetupStatus says what became of the refresh of one instance's client setup.
type SetupStatus string

const (
	// SetupRefreshed: a record exists and the recorded items that need it were
	// brought up to date; Items say which, which may be none.
	SetupRefreshed SetupStatus = "refreshed"
	// SetupNone: nothing was ever set up for the instance, or it was undone.
	SetupNone SetupStatus = "none"
	// SetupDrift: something recorded is gone, was changed by hand or cannot work; a
	// person decides, and Fix is the command that does.
	SetupDrift SetupStatus = "drift"
	// SetupSkipped: there is no setup to refresh: the instance's adapter declares none,
	// the daemon cannot say, the adapter's package cannot be loaded, or the adapter is
	// not installed. Message says which.
	SetupSkipped SetupStatus = "skipped"
	// SetupError: the refresh could not be made or an item of it failed; Message
	// says why. It makes the run fail.
	SetupError SetupStatus = "error"
)

// SetupReport is what became of the client setup of one instance.
type SetupReport struct {
	Instance string      `json:"instance"`
	Adapter  string      `json:"adapter"`
	Status   SetupStatus `json:"status"`
	// Message says why the setup was skipped or the refresh failed.
	Message string `json:"message,omitempty"`
	// Items are what the refresh did, item by item.
	Items []SetupItem `json:"items,omitempty"`
	// Drift is what a refresh found and leaves to a person.
	Drift []SetupDifference `json:"drift,omitempty"`
	// Fix is the command that puts drift right, as the node renders it for the person
	// who runs it; it is set whenever Drift is not empty.
	Fix string `json:"fix,omitempty"`
}

// SetupItem is what became of one recorded item: its id, the status (done, skipped or
// failed, or one a later release adds) and what the adapter said.
type SetupItem struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

// SetupDifference is one difference a refresh found: the item's id, how it differs
// (missing, changed, stale, broken, needs_setup, or one a later release adds) and what
// puts it right.
type SetupDifference struct {
	ID      string `json:"id"`
	Problem string `json:"problem"`
	Summary string `json:"summary"`
}

// Acted says whether a run changed or restored anything, or found that nothing needed
// changing: the cases in which its report is worth showing although it failed.
func (r Report) Acted() bool {
	p := r.Program
	return r.AlreadyCurrent || p.Updated || len(p.Restored) > 0 || len(p.Left) > 0 || r.Daemon.Restarted || len(r.Adapters) > 0 || len(r.Setup) > 0
}

// NormalizeVersion reads the version a person asked for: the release version
// syntax with or without a leading v. The empty string means the latest release.
func NormalizeVersion(text string) (string, error) {
	if text == "" {
		return "", nil
	}
	version := strings.TrimPrefix(text, "v")
	if _, err := release.ParseVersion(version); err != nil {
		return "", fmt.Errorf("%q is not a release version such as 0.12.0 or v0.12.0-rc.1", text)
	}
	return version, nil
}
