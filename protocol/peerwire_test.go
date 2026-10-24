package protocol_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestPeerOutputMatchesSchema: every line the Go peer writes while its own tests
// run validates against message.schema.json, so a reader in any language takes
// what the peer sends. The tests of package peer wrap the writer of each peer
// they create and copy the lines to a file when the variable below names one;
// this test runs them with it set and validates the file. It is in this module
// because the schema validator is a dependency of this module only. Skipped when
// the go command cannot be run.
func TestPeerOutputMatchesSchema(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the tests of package peer")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("go command: %v", err)
	}
	log := filepath.Join(t.TempDir(), "wire.log")
	cmd := exec.Command(goBin, "test", "-count=1", "./peer/")
	cmd.Dir = filepath.Join("..", "sdk", "go")
	cmd.Env = append(os.Environ(), "SIDERA_PEER_WIRE_LOG="+log)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the tests of package peer fail: %v\n%s", err, out)
	}
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("the peer tests wrote no lines: %v", err)
	}
	if len(raw) == 0 || raw[len(raw)-1] != '\n' {
		t.Fatalf("the log does not end with a newline: %d bytes", len(raw))
	}
	shapes := map[string]int{}
	bad := 0
	for _, line := range bytes.Split(raw[:len(raw)-1], []byte("\n")) {
		if err := schemaVerdict(t, "frame", "", line); err != nil {
			if bad++; bad <= 10 {
				t.Errorf("the peer wrote a line the schema rejects: %v\n  %.300s", err, line)
			}
		}
		var f frame
		if json.Unmarshal(line, &f) != nil {
			continue
		}
		switch {
		case f.Method != "" && len(f.ID) > 0:
			shapes["request"]++
		case f.Method != "":
			shapes["notification"]++
		case len(f.Error) > 0:
			shapes["error"]++
		case len(f.Result) > 0:
			shapes["result"]++
		}
	}
	if bad > 10 {
		t.Errorf("%d lines in all are rejected", bad)
	}
	for _, shape := range []string{"request", "notification", "result", "error"} {
		if shapes[shape] < 20 {
			t.Errorf("only %d %s lines were checked; the peer tests no longer exercise the writer", shapes[shape], shape)
		}
	}
	t.Logf("%d lines checked: %v", strings.Count(string(raw), "\n"), shapes)
}
