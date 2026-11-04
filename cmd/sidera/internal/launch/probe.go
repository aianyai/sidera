package launch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/aianyai/sidera/internal/node/childproc"
	"github.com/aianyai/sidera/internal/release"
)

const (
	// probeLimit bounds a probe whose context has no deadline: a program that
	// does not answer `version` at once is not one to start.
	probeLimit = 30 * time.Second
	// probeOutput caps what is kept of each output stream.
	probeOutput = 64 << 10
)

// Probe runs Program as `sidera version --json` and returns what it reports. Args[0]
// is "sidera" whatever name the program was started under: the name siderad selects
// the daemon, and `siderad version --json` would run `daemon version --json`.
func (l Launch) Probe(ctx context.Context) (release.Info, error) {
	ctx, cancel := context.WithTimeout(ctx, probeLimit)
	defer cancel()
	var stdout, stderr cappedBuffer
	cmd := exec.CommandContext(ctx, l.Program, "version", "--json")
	cmd.Args[0] = "sidera"
	childproc.Windowless(cmd)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	// A killed program can leave a child holding the pipes; do not wait for it.
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return release.Info{}, fmt.Errorf("launch: run %s version --json: %w%s", l.Program, err, quoted(stderr.String()))
	}
	var info release.Info
	if err := json.Unmarshal(stdout.Bytes(), &info); err != nil {
		return release.Info{}, fmt.Errorf("launch: %s version --json did not print a version object: %w%s", l.Program, err, quoted(stdout.String()))
	}
	if info.Version == "" {
		return release.Info{}, fmt.Errorf("launch: %s version --json reported no version", l.Program)
	}
	return info, nil
}

func quoted(s string) string {
	if s = strings.TrimSpace(s); s != "" {
		return fmt.Sprintf(" (%q)", s)
	}
	return ""
}

// cappedBuffer keeps the first probeOutput bytes written and drops the rest
// without failing the writer.
type cappedBuffer struct{ bytes.Buffer }

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if room := probeOutput - b.Len(); room > 0 {
		b.Buffer.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}
