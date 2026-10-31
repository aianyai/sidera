package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/aianyai/sidera/cmd/sidera-adapter-claude/internal/claudeargs"
)

// commandTimeout bounds `claude --version` and `claude auth status`.
const commandTimeout = 30 * time.Second

// claudeStatus is what `claude auth status` says.
type claudeStatus struct {
	LoggedIn bool `json:"loggedIn"`
	// AuthMethod says how the login was made; the account itself is never read.
	AuthMethod        string `json:"authMethod"`
	ProjectsDirectory string `json:"projectsDirectory"`
}

// claudeStatus asks the runtime who is logged in and where it keeps its
// sessions. It runs with the environment a turn process would have, so the
// answer is about the login a turn would use, and it reads the answer from the
// output whatever the exit code: a runtime that is not logged in says so and exits
// with an error.
func (a *App) claudeStatus(ctx context.Context, claude string) (claudeStatus, error) {
	res, err := a.o.Runner.Run(ctx, Command{
		Argv: []string{claude, "auth", "status"}, Env: claudeargs.SanitizeEnv(a.o.Environ), Timeout: commandTimeout,
	})
	if err != nil {
		return claudeStatus{}, fmt.Errorf("claude auth status: %w", err)
	}
	var st claudeStatus
	if err := json.Unmarshal(res.Stdout, &st); err != nil {
		return claudeStatus{}, fmt.Errorf("claude auth status exited with %d and printed no status: %s", res.ExitCode, tail(res.Stderr))
	}
	if st.LoggedIn && st.ProjectsDirectory == "" {
		return claudeStatus{}, fmt.Errorf("claude auth status does not say where sessions are kept")
	}
	return st, nil
}

// claudeVersion returns the version `claude --version` prints, "2.1.316" for
// "2.1.316 (Claude Code)".
func (a *App) claudeVersion(ctx context.Context, claude string) (string, error) {
	res, err := a.o.Runner.Run(ctx, Command{Argv: []string{claude, "--version"}, Env: claudeargs.SanitizeEnv(a.o.Environ), Timeout: commandTimeout})
	if err != nil {
		return "", fmt.Errorf("claude --version: %w", err)
	}
	fields := strings.Fields(string(res.Stdout))
	if res.ExitCode != 0 || len(fields) == 0 {
		return "", fmt.Errorf("claude --version exited with %d: %s", res.ExitCode, tail(res.Stderr))
	}
	return fields[0], nil
}

// tail is the last of what a program wrote, as one line fit for a detail.
func tail(b []byte) string {
	s := strings.Join(strings.Fields(string(b)), " ")
	if len(s) > 300 {
		s = "..." + s[len(s)-300:]
	}
	return strings.ToValidUTF8(s, "")
}
