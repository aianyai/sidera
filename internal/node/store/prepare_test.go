package store_test

import (
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/store"
)

// What a cross-owner write cannot encode is refused by the prepare step, which
// runs before the composing package opens its transaction: no transaction is
// opened and no crash point is reached.
func TestPrepareRefusesWhatCannotBeWrittenBeforeAnyTransaction(t *testing.T) {
	badResult := adminResult(1)
	badResult.Result = []byte("{ \"closed\": true }") // not compact
	badEndpoint := directEndpoint(7, "chat")
	badEndpoint.Token = []byte("short")
	for name, prepare := range map[string]func() error{
		"an admin result whose result is not compact JSON": func() error { _, err := store.PrepareAdminResult(badResult); return err },
		"an endpoint whose token has the wrong length":     func() error { _, err := store.PrepareEndpoint(badEndpoint); return err },
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			before := snapshot(t, r.mem, ".")
			if err := prepare(); err == nil {
				t.Fatal("accepted")
			}
			if r.rec.count() != 0 {
				t.Errorf("%d crash points were reached by a refused prepare", r.rec.count())
			}
			if after := snapshot(t, r.mem, "."); len(after) != len(before) {
				t.Errorf("the disk changed: %v", keysOf(after))
			}
		})
	}
}

// A composer that follows the documented shape (prepare, then Begin, write,
// Commit, returning the first error) gets the error of an invalid result and
// nothing else: before the prepare step existed the encoding failed after the
// first write of the transaction had passed its commit point, and the deferred
// Close panicked with "a defect of the caller" for a composer that had done
// nothing wrong.
func TestComposedTransactionWithAnInvalidResultReturnsTheErrorAndDoesNotPanic(t *testing.T) {
	r := newRig(t)
	res := adminResult(1)
	res.Result = []byte("{ \"closed\": true }")
	before := snapshot(t, r.mem, ".")
	var panicked any
	var err error
	func() {
		defer func() { panicked = recover() }()
		err = func() error {
			pr, err := store.PrepareAdminResult(res)
			if err != nil {
				return err
			}
			tx := r.w.Begin(&declXClear)
			defer tx.Close()
			if err := tx.Replace("clock", "keys/clock.rec", []byte("x"), fsx.ModePrivate); err != nil {
				return err
			}
			if err := store.PutAdminResult(tx, store.RoleResult, pr); err != nil {
				return err
			}
			return tx.Commit()
		}()
	}()
	if panicked != nil {
		t.Fatalf("the composer panicked: %v", panicked)
	}
	if err == nil {
		t.Fatal("the invalid result was accepted")
	}
	if r.rec.count() != 0 {
		t.Errorf("%d crash points were reached", r.rec.count())
	}
	if after := snapshot(t, r.mem, "."); len(after) != len(before) {
		t.Errorf("the disk changed: %v", keysOf(after))
	}
}

// A zero prepared value is a defect of the caller, and it fails at once rather
// than after the commit point.
func TestPutOfAValueThatWasNotPreparedPanics(t *testing.T) {
	r := newRig(t)
	tx := r.w.Begin(&declXAdmit)
	defer tx.Close()
	func() {
		defer func() {
			if recover() == nil {
				t.Error("PutEndpoint of a zero value did not panic")
			}
		}()
		_ = store.PutEndpoint(tx, store.RoleEndpoint, store.PreparedEndpoint{})
	}()
	func() {
		defer func() {
			if recover() == nil {
				t.Error("PutAdminResult of a zero value did not panic")
			}
		}()
		_ = store.PutAdminResult(tx, store.RoleResult, store.PreparedAdminResult{})
	}()
}
