package runtime

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

const mcpInitialize = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"sidera-adapter-claude","version":"0"}}}` + "\n"

// probeMCP starts `sidera mcp` as Claude Code would, sends it an initialize
// request and waits for the probe the server starts on connection to report on
// its standard error. The server reports success or failure once and exits when
// its input ends.
func probeMCP(ctx context.Context, sidera string, env []string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, sidera, "mcp")
	cmd.Env = env
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	defer func() {
		stdin.Close()
		_ = cmd.Wait()
	}()

	result := make(chan error, 1)
	go func() {
		if _, err := io.WriteString(stdin, mcpInitialize); err != nil {
			result <- fmt.Errorf("sidera mcp did not take the request: %w", err)
			return
		}
		if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || !strings.Contains(line, `"result"`) {
			result <- fmt.Errorf("sidera mcp did not answer initialize: %q %v", tail([]byte(line)), err)
			return
		}
		sc := bufio.NewScanner(stderr)
		sc.Buffer(nil, 1<<20)
		for sc.Scan() {
			switch line := sc.Text(); {
			case strings.Contains(line, "MCP probe succeeded"):
				result <- nil
				return
			case strings.Contains(line, "MCP probe failed"):
				result <- errors.New(tail([]byte(line)))
				return
			}
		}
		result <- errors.New("sidera mcp ended without reporting the probe")
	}()
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		return fmt.Errorf("sidera mcp: %w", ctx.Err())
	}
}
