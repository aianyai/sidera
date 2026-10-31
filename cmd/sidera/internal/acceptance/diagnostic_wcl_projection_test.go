//go:build diagnostic

package acceptance

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiagnosticWCLProjectionExcludesNativeNamesAndErrorText(t *testing.T) {
	const canary = "PRIVATE-wcl-app-service-error-CANARY"
	var raw diagnosticWCLProcessInfo
	for i, r := range canary {
		raw.AppName[i], raw.ServiceName[i] = uint16(r), uint16(r)
	}
	raw.Process = diagnosticWCLIdentity{42, diagnosticWCLFiletime{7, 9}}
	var out bytes.Buffer
	diagnosticWCLWrite(&out, diagnosticWCLRecord{
		Phase: "after_remove", Status: "query_success", RelativePath: "l/adapter_fake-42.jsonl",
		ResourceUsers: diagnosticWCLUsers([]diagnosticWCLProcessInfo{raw}),
		Errno:         diagnosticWCLNativeErrno(errors.New(canary)),
	})
	if strings.Contains(out.String(), canary) || strings.Contains(out.String(), "AppName") || strings.Contains(out.String(), "ServiceName") {
		t.Fatal("native names or freeform error escaped the projection")
	}
	const want = "[DEBUG-wcl] {\"phase\":\"after_remove\",\"status\":\"query_success\",\"relative_path\":\"l/adapter_fake-42.jsonl\",\"errno\":0,\"duration_ms\":0,\"resource_users\":[{\"pid\":42,\"creation_filetime\":{\"low\":7,\"high\":9}}]}\n"
	if out.String() != want {
		t.Fatal("typed resource-user projection changed")
	}
}

func TestDiagnosticWCLRemovalScopeUsesOnlyExactFixtureAdapterPath(t *testing.T) {
	root := t.TempDir()
	for _, tc := range []struct {
		name, path string
		want       bool
	}{
		{"owned adapter", filepath.Join(root, "l", "adapter_fake-42.jsonl"), true},
		{"different root", filepath.Join(root, "..", "other", "l", "adapter_fake-42.jsonl"), false},
		{"directory", filepath.Join(root, "l"), false},
		{"runtime", filepath.Join(root, "l", "runtime_fake-42.jsonl"), false},
		{"nested path", filepath.Join(root, "other", "l", "adapter_fake-42.jsonl"), false},
		{"nonledger", filepath.Join(root, "l", "adapter_fake-42.out"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, rel, ok := diagnosticWCLFailedPath(root, &os.PathError{Op: "remove", Path: tc.path, Err: errors.New("unexported")})
			if ok != tc.want || (ok && (path != tc.path || rel != "l/adapter_fake-42.jsonl")) {
				t.Fatal("removal query escaped its exact adapter-file scope")
			}
		})
	}
}
