package pairing

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/record"
)

// A persisted record has the outer shape of a message, {1: type, 2: payload}.
// The expected bytes come from the independent implementation.
func TestPersistedRecordGoldenVectors(t *testing.T) {
	created := time.Unix(1798761600, 0).UTC()
	l, err := NewLimits([]time.Time{created}, []time.Time{created.Add(100 * time.Second), created.Add(200 * time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		key  string
		enc  func() ([]byte, error)
	}{
		{"limits: {1: type, 2: {1: reveals, 2: failures}}", "persist.limits", l.Encode},
		{"empty limits: {1: type, 2: {}}", "persist.limits_empty", Limits{}.Encode},
		{"no expectations: {1: type, 2: {}}", "persist.expectations_empty", func() ([]byte, error) { return EncodeExpectations(nil) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.enc()
			if err != nil {
				t.Fatal(err)
			}
			if want := v(t, tt.key); !bytes.Equal(got, want) {
				t.Fatalf("encoded = %x\nwant      %x", got, want)
			}
		})
	}
	t.Run("the golden limits decode back", func(t *testing.T) {
		got, err := DecodeLimits(v(t, "persist.limits"))
		if err != nil || len(got.Reveals()) != 1 || len(got.Failures()) != 2 {
			t.Fatalf("DecodeLimits = %+v, %v", got, err)
		}
	})
}

func TestPersistedRecordsRefuseAWrongShape(t *testing.T) {
	limits := v(t, "persist.limits")
	t.Run("a record of another type", func(t *testing.T) {
		if _, err := DecodeExpectations(limits); !errors.Is(err, ErrPersisted) {
			t.Fatalf("limits read as expectations: %v", err)
		}
		if _, err := DecodeLimits(v(t, "persist.expectations_empty")); !errors.Is(err, ErrPersisted) {
			t.Fatalf("expectations read as limits: %v", err)
		}
		if _, err := DecodeTargetRecord(limits); !errors.Is(err, ErrPersisted) {
			t.Fatalf("limits read as a target record: %v", err)
		}
		if _, err := DecodeInitiatorRecord(limits); !errors.Is(err, ErrPersisted) {
			t.Fatalf("limits read as an initiator record: %v", err)
		}
	})
	t.Run("the type is not at the top and the fields are not nested", func(t *testing.T) {
		flat, err := record.MarshalCanonical(map[int]any{0: TypeLimits, 1: []int64{1798761600}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := DecodeLimits(flat); !errors.Is(err, ErrPersisted) {
			t.Fatalf("flat shape: %v", err)
		}
	})
	t.Run("an unknown field beside the payload or in it", func(t *testing.T) {
		for name, m := range map[string]map[int]any{
			"beside": {1: TypeLimits, 2: map[int]any{}, 3: 1},
			"inside": {1: TypeLimits, 2: map[int]any{9: 1}},
		} {
			b, err := record.MarshalCanonical(m)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeLimits(b); !errors.Is(err, ErrPersisted) {
				t.Fatalf("%s: %v", name, err)
			}
		}
	})
}
