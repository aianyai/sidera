package engine

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestEveryReasonHasItsOwnName(t *testing.T) {
	seen := map[string]Reason{}
	for _, r := range Reasons() {
		n := r.String()
		if n == "" || strings.HasPrefix(n, "invalid(") {
			t.Errorf("reason %d has no name", r)
		}
		if other, dup := seen[n]; dup {
			t.Errorf("reasons %d and %d are both %q", other, r, n)
		}
		seen[n] = r
	}
	if len(Reasons()) != int(reasonEnd)-1 {
		t.Errorf("Reasons lists %d of %d", len(Reasons()), reasonEnd-1)
	}
	if Reason(0).String() != "invalid(0)" || reasonEnd.String() == "" {
		t.Error("a value that is not a reason must not read as one")
	}
}

func TestEveryDetailHasItsOwnName(t *testing.T) {
	seen := map[string]Detail{}
	for _, d := range Details() {
		n := d.String()
		if n == "" || strings.HasPrefix(n, "invalid(") {
			t.Errorf("detail %d has no name", d)
		}
		if other, dup := seen[n]; dup {
			t.Errorf("details %d and %d are both %q", other, d, n)
		}
		seen[n] = d
	}
	if len(Details()) != int(detailEnd)-1 {
		t.Errorf("Details lists %d of %d", len(Details()), detailEnd-1)
	}
	if Detail(0).String() != "" || detailEnd.String() == "" {
		t.Error("the zero detail says nothing, and a value that is not a detail must not read as one")
	}
	if got := (&Refusal{Reason: ReasonInvalidArgument, Field: "patch", Detail: DetailEmptyPatch}).Error(); !strings.Contains(got, "patch") || !strings.Contains(got, "empty_patch") {
		t.Errorf("Error = %q", got)
	}
}

func TestARefusalSaysWhyAndKeepsItsCause(t *testing.T) {
	cause := errors.New("disk")
	err := fmt.Errorf("wrapped: %w", &Refusal{Reason: ReasonEndpointLimit, Limit: LimitNode, Err: cause})
	if r, ok := ReasonOf(err); !ok || r != ReasonEndpointLimit {
		t.Errorf("ReasonOf = %v, %v", r, ok)
	}
	if !errors.Is(err, cause) {
		t.Error("the cause is not reachable")
	}
	if got := (&Refusal{Reason: ReasonEndpointLimit, Limit: LimitNode}).Error(); !strings.Contains(got, "endpoint_limit") || !strings.Contains(got, "node") {
		t.Errorf("Error = %q", got)
	}
	if got := (&Refusal{Reason: ReasonInvalidArgument, Field: "key"}).Error(); !strings.Contains(got, "key") {
		t.Errorf("Error = %q", got)
	}
	if _, ok := ReasonOf(errors.New("plain")); ok {
		t.Error("a plain error has a reason")
	}
}

func TestParamsTakeTheDefaultsForWhatIsNotSet(t *testing.T) {
	d := DefaultParams()
	if got := (Params{}).withDefaults(); got != d {
		t.Errorf("zero Params = %+v, want %+v", got, d)
	}
	set := Params{InboxLen: 3, SubmitWait: time.Second, ChunkReplaces: 2, ChunkRemoves: 5, ChunkTickRecords: 7, RetryBackoff: time.Hour}
	if got := set.withDefaults(); got != set {
		t.Errorf("Params that are set changed: %+v", got)
	}
	if d.InboxLen < 1 || d.ChunkReplaces < 1 || d.ChunkRemoves < 1 || d.ChunkTickRecords < 1 || d.SubmitWait <= 0 || d.RetryBackoff <= 0 {
		t.Errorf("a default is not usable: %+v", d)
	}
}
