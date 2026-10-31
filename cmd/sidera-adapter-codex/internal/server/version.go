package server

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"time"
)

var versionRE = regexp.MustCompile(`\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?`)

// ParseCLIVersion extracts the version from the output of `codex --version`
// ("codex-cli 0.164.0").
func ParseCLIVersion(output string) (string, error) {
	v := versionRE.FindString(output)
	if v == "" {
		return "", fmt.Errorf("no version in %q", output)
	}
	return v, nil
}

// CLIVersion runs `<program> --version`.
func CLIVersion(ctx context.Context, program string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, program, "--version").Output()
	if err != nil {
		return "", fmt.Errorf("%s --version: %w", program, err)
	}
	return ParseCLIVersion(string(out))
}
