package runtime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/aianyai/sidera/sdk/go/adapterkit/realpath"
)

// osFiles is Files on the operating system's file system.
type osFiles struct{}

func (osFiles) Open(path string, offset int64) (io.ReadCloser, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			f.Close()
			return nil, err
		}
	}
	return f, nil
}

func (osFiles) ListDir(path string) ([]string, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name()
	}
	return names, nil
}

func (osFiles) WriteFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".write-*")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}

func (osFiles) RealDir(path string) (string, error) {
	real, err := realpath.Resolve(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", path)
	}
	return real, nil
}

// maxCapture bounds what a Command's output keeps.
const maxCapture = 1 << 20

// osRunner is Runner on os/exec.
type osRunner struct{}

func (osRunner) Run(ctx context.Context, c Command) (CommandResult, error) {
	if len(c.Argv) == 0 {
		return CommandResult{}, errors.New("no command")
	}
	if c.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, c.Argv[0], c.Argv[1:]...)
	cmd.Env, cmd.Dir = c.Env, c.Dir
	var out, errb capped
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	res := CommandResult{Stdout: out.Bytes(), Stderr: errb.Bytes()}
	var exit *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exit) && ctx.Err() == nil:
		res.ExitCode = exit.ExitCode()
	case ctx.Err() != nil:
		return res, fmt.Errorf("%s: %w", c.Argv[0], ctx.Err())
	default:
		return res, err
	}
	return res, nil
}

// capped is a buffer that keeps the first maxCapture bytes and drops the rest.
type capped struct{ bytes.Buffer }

func (b *capped) Write(p []byte) (int, error) {
	if room := maxCapture - b.Len(); room > 0 {
		b.Buffer.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

// inboxTimeout bounds the connection to an inbox socket and the write.
const inboxTimeout = 3 * time.Second

// osInbox is Inbox on the operating system's local socket of Claude Code: a Unix
// domain socket, or a named pipe on Windows.
type osInbox struct{}
