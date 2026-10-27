package engine

import (
	"errors"
	"fmt"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
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
	set := Params{InboxLen: 3, SubmitWait: time.Second, ChunkReplaces: 2, ChunkRemoves: 5, ChunkTickRecords: 7, RetryBackoff: time.Hour, ResendBackoff: time.Minute, PullInterval: time.Hour,
		CreateReplyDeadline: 2 * time.Minute, CreateRetryBackoff: time.Second, ControlReplyDeadline: time.Minute, InterruptConfirmWait: time.Second, PromptCallDeadline: time.Hour,
		Feedback: delivery.FeedbackParams{MergeWindow: time.Second, RateCap: 3, RateWindow: time.Hour}}
	if got := set.withDefaults(); got != set {
		t.Errorf("Params that are set changed: %+v", got)
	}
	if d.InboxLen < 1 || d.ChunkReplaces < 1 || d.ChunkRemoves < 1 || d.ChunkTickRecords < 1 || d.SubmitWait <= 0 || d.RetryBackoff <= 0 || d.ResendBackoff <= 0 || d.PullInterval <= 0 || d.CreateReplyDeadline <= 0 || d.CreateRetryBackoff <= 0 || d.ControlReplyDeadline <= 0 || d.InterruptConfirmWait <= 0 || d.PromptCallDeadline <= 0 {
		t.Errorf("a default is not usable: %+v", d)
	}
}

// A failed operation of the file layer that no transaction wrapped is told as the
// storage being unavailable, or full, and not as an internal error; an error that
// is none of the storage's stays what it is.
func TestClassifyTellsAFailedFileOperationFromAnUnexpectedError(t *testing.T) {
	unexpected := errors.New("not the storage's")
	for _, tc := range []struct {
		name string
		err  error
		want Reason
	}{
		{"an I/O error of the file layer", &fsx.Error{Class: fsx.Failed, Err: syscall.EIO}, ReasonStorageUnavailable},
		{"a full disk of the file layer", &fsx.Error{Class: fsx.Failed, Err: fsxtest.DiskFull}, ReasonStorageFull},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ReasonOf((&Engine{}).classify(tc.err))
			if !ok || got != tc.want {
				t.Errorf("classified as %v (%v), want %v", got, ok, tc.want)
			}
		})
	}
	if err := (&Engine{}).classify(unexpected); err != unexpected {
		t.Errorf("an unexpected error became %v", err)
	}
}
