package acceptance

import (
	"fmt"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/node/daemon"
)

func TestTraceRejectsFalseCrashEvidence(t *testing.T) {
	table, err := daemon.Table()
	if err != nil {
		t.Fatal(err)
	}
	key := table.Keys()[0]
	kind := "normal"
	line := fmt.Sprintf(`{"gen":1,"point":%q,"path":"state/.tmp-abc","from":"","kind":%q}`, key.Key.String()+"#1", kind)
	for _, tt := range []struct{ name, raw string }{
		{"wrong generation", strings.Replace(line, `"gen":1`, `"gen":2`, 1) + "\n"},
		{"wrong kind", strings.Replace(line, `"normal"`, `"proc"`, 1) + "\n"},
		{"partial line", line},
		{"marker without arrival", strings.TrimSuffix(line, "}") + `,"crash":true}` + "\n"},
		{"ordinal gap", strings.Replace(line, "#1", "#2", 1) + "\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, err := parseTrace([]byte(tt.raw), 1, 0, table, false); err == nil {
				t.Fatal("invalid evidence accepted")
			}
		})
	}
	trace, n, err := parseTrace([]byte(line+"\n"+line[:12]), 1, 1, table, true)
	if err != nil || n != 1 || len(trace.Hits) != 1 || !trace.Hits[0].Setup || trace.Reached {
		t.Fatalf("prefix: %+v, %d, %v", trace, n, err)
	}
}

func TestTraceMarkerDoesNotDuplicateHitAndProcStaysSeparate(t *testing.T) {
	table, err := daemon.Table()
	if err != nil {
		t.Fatal(err)
	}
	key := table.Keys()[0].Key.String()
	line := fmt.Sprintf(`{"gen":1,"point":%q,"path":"state/.tmp-abc","from":"state/.tmp-def","kind":"normal"}`, key+"#1")
	marker := strings.TrimSuffix(line, "}") + `,"crash":true}`
	trace, n, err := parseTrace([]byte(line+"\n"+marker+"\n"), 1, 1, table, false)
	if err != nil || n != 2 || len(trace.Hits) != 1 || trace.Crash != key+"#1" || trace.Reached {
		t.Fatalf("marker: %+v %d %v", trace, n, err)
	}
	if trace.Hits[0].Path != "state/.tmp-*" || trace.Hits[0].From != "state/.tmp-*" {
		t.Fatalf("normalization: %+v", trace.Hits[0])
	}
	proc := `{"gen":1,"point":"proc/cli.recv-after-response","path":"","from":"","kind":"proc"}`
	trace, _, err = parseTrace([]byte(proc+"\n"), 1, 0, table, false)
	if err != nil || len(trace.Hits) != 0 || len(trace.Proc) != 1 {
		t.Fatalf("proc: %+v %v", trace, err)
	}
}
