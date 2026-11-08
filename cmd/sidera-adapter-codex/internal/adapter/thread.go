package adapter

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/appserver"
	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/codexcfg"
	"github.com/aianyai/sidera/sdk/go/adapterkit/realpath"
)

// errUnprotected marks a thread that cannot be given, or did not take, the
// permission profile that keeps its sandboxed commands out of the data directory.
// Such a thread is never used: a session without the protection would leave every
// session's token readable to the model's commands.
var errUnprotected = errors.New("the thread is not protected from reading the data directory")

// dataDirEnv names the variable of a session's tool access that holds the data
// directory.
const dataDirEnv = "SIDERA_HOME"

// threadConfig is the thread-level configuration of a session. For a preset with a
// sandbox it fails, with errUnprotected, when the session's data directory cannot
// be named as the sandbox needs it.
func (a *App) threadConfig(s *session, p codexcfg.Preset) (map[string]any, error) {
	a.mu.Lock()
	sidera := a.sideraPath
	a.mu.Unlock()
	acc := codexcfg.Access{Program: sidera, Env: s.access}
	if p.Sandboxed() {
		dir, err := realDir(s.access[dataDirEnv])
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %v", errUnprotected, dataDirEnv, err)
		}
		acc.DataDir = dir
	}
	cfg, err := p.ThreadConfig(acc)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errUnprotected, err)
	}
	return cfg, nil
}

// realDir is the directory a path names, as the platform resolves it: the sandbox
// compares the paths a command opens with the path it is told to deny, so a symbolic
// link, a junction or a short name in it must not make the two differ. The directory
// has to exist, since Codex creates a denied path that does not, and the path has to
// be absolute, since a relative one would be resolved against the adapter's working
// directory, which is not where the session's data directory is.
func realDir(path string) (string, error) {
	if path == "" {
		return "", errors.New("is not set")
	}
	if !filepath.IsAbs(path) {
		return "", errors.New("is not an absolute path")
	}
	return realpath.Resolve(path)
}

// threadStart starts the session's thread with its configuration and checks that
// the permission profile is in force.
func (a *App) threadStart(ctx context.Context, c *appserver.Client, s *session, p codexcfg.Preset) (appserver.Thread, error) {
	cfg, err := a.threadConfig(s, p)
	if err != nil {
		return appserver.Thread{}, err
	}
	model, _ := s.modelAndEffort(p)
	res, err := c.ThreadStart(ctx, appserver.ThreadStartParams{
		CWD: p.CWD, ApprovalPolicy: p.ApprovalPolicy, Sandbox: p.ThreadSandbox(), Model: model,
		Ephemeral: false, Config: cfg,
		DeveloperInstructions: codexcfg.DeveloperInstructions(p.DeveloperInstructions),
	})
	if err != nil {
		return appserver.Thread{}, err
	}
	return res.Thread, a.requireProfile(ctx, c, p, res)
}

// threadResume loads a thread that was stored, with the session's configuration,
// and checks that the permission profile is in force.
func (a *App) threadResume(ctx context.Context, c *appserver.Client, s *session, threadID string, p codexcfg.Preset) (appserver.Thread, error) {
	cfg, err := a.threadConfig(s, p)
	if err != nil {
		return appserver.Thread{}, err
	}
	// A resume that names a model drops the effort the thread persisted, which is why
	// every turn carries the effort itself.
	model, _ := s.modelAndEffort(p)
	res, err := c.ThreadResume(ctx, appserver.ThreadResumeParams{
		ThreadID: threadID, CWD: p.CWD, ApprovalPolicy: p.ApprovalPolicy, Sandbox: p.ThreadSandbox(), Model: model,
		ExcludeTurns: true, Config: cfg,
		DeveloperInstructions: codexcfg.DeveloperInstructions(p.DeveloperInstructions),
	})
	if err != nil {
		return appserver.Thread{}, err
	}
	return res.Thread, a.requireProfile(ctx, c, p, res)
}

// requireProfile checks that a thread of a preset with a sandbox runs under the
// permission profile the adapter defined. Codex accepts a configuration whose
// profile it then ignores (when the legacy sandbox mode is also given, for one),
// answering with no active profile, so only the answer shows that the sandbox
// denies the data directory. A thread that lacks it is unloaded for this
// connection, so that nothing is ever asked of it.
func (a *App) requireProfile(ctx context.Context, c *appserver.Client, p codexcfg.Preset, res appserver.ThreadResult) error {
	name, extends, ok := p.ExpectedProfile()
	if !ok {
		return nil
	}
	got := res.ActivePermissionProfile
	if got != nil && got.ID == name && got.Extends != nil && *got.Extends == extends {
		return nil
	}
	have := "none"
	if got != nil {
		have = got.ID
		if got.Extends != nil {
			have += " extending " + *got.Extends
		}
	}
	err := fmt.Errorf("%w: Codex reports the permission profile %s, expected %s extending %s", errUnprotected, have, name, extends)
	a.log.Error("a thread did not take the permission profile that protects the data directory; it is not used", "thread", res.Thread.ID, "error", err)
	uctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if uerr := c.ThreadUnsubscribe(uctx, res.Thread.ID); uerr != nil {
		a.log.Warn("cannot unload a thread that is not protected", "thread", res.Thread.ID, "error", uerr)
	}
	return err
}
