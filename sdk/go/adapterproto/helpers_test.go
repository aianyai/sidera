package adapterproto_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

const (
	ep1      = "22222222222222222222222222222222"
	ep2      = "33333333333333333333333333333333"
	epRT     = "11111111111111111111111111111111"
	attempt1 = "6f1c2a4e-8b3d-4c5a-9e7f-0a1b2c3d4e5f"
	attempt2 = "0b9e7d52-3c41-4f8a-8d6e-5a7c9b1e2f30"
	lease1   = "0123456789abcdef0123456789abcdef"
	token1   = "k3JpQ9vL2mXwR7tYbN5cZ8dHfG1sAeUo0iWxM4yTqBA"
	addr1    = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb/22222222222222222222222222222222"
)

// row is one rule of a Validate method: a change that breaks it, or no change at
// all. wantErr is a substring of the message of the error; empty means the value
// must be valid.
type row[T any] struct {
	name    string
	mutate  func(*T)
	wantErr string
	beyond  bool // the rule cannot be stated in JSON Schema
}

// validator is any message type.
type validator interface{ Validate() error }

// runRows applies each row to a fresh valid value and checks Validate.
func runRows[T any](t *testing.T, base func() T, validate func(T) error, rows []row[T]) {
	t.Helper()
	if err := validate(base()); err != nil {
		t.Fatalf("the base value is invalid: %v", err)
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			v := base()
			if r.mutate != nil {
				r.mutate(&v)
			}
			err := validate(v)
			if r.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate = %v, want valid", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate accepted it, want an error mentioning %q", r.wantErr)
			}
			if !strings.Contains(err.Error(), r.wantErr) {
				t.Fatalf("Validate = %v, want an error mentioning %q", err, r.wantErr)
			}
			if got := errors.Is(err, adapterproto.ErrBeyondSchema); got != r.beyond {
				t.Fatalf("beyond-schema = %v, want %v (%v)", got, r.beyond, err)
			}
		})
	}
}

func method[T validator](t *testing.T, base func() T, rows []row[T]) {
	t.Helper()
	runRows(t, base, func(v T) error { return v.Validate() }, rows)
}

func raw(s string) json.RawMessage { return json.RawMessage(s) }

func proc() *adapterproto.ProcessIdentity {
	return &adapterproto.ProcessIdentity{PID: 4821, StartKey: "linux:2417733:boot-1", StartedAtMS: 1792713595000,
		Container: &adapterproto.Container{Kind: adapterproto.ContainerSystemdUnit, Name: "sidera-session.scope"}}
}

func toolAccess() adapterproto.ToolAccess {
	return adapterproto.ToolAccess{Env: map[string]string{"SIDERA_TOKEN": token1}}
}
