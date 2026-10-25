package config_test

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/record"
)

// The revocation records of config/revocations are decoded, verified with the
// rules of the revocation package, and kept: a record that does not verify is
// not a revocation, and a file whose name is not its digest is not the file it
// claims to be.
func TestRevocationsAreVerifiedWhenLoaded(t *testing.T) {
	_, c := loaded(t)
	revs := c.Revocations()
	if len(revs) != 1 {
		t.Fatalf("revocations = %d", len(revs))
	}
	signer7 := signer(t, 7)
	node7, err := ident.NodeIDFromKey(signer7.Public())
	if err != nil {
		t.Fatal(err)
	}
	if revs[0].Node() != node7 {
		t.Errorf("the revocation names node %v, want the node of the signer %v", revs[0].Node(), node7)
	}
	if !c.RevocationSet().NodeRetired(node7) || c.RevocationSet().NodeRetired(mustNode(t, node1Text)) {
		t.Error("the set retires the wrong nodes")
	}

	t.Run("a record of another type is not a revocation", func(t *testing.T) {
		rec, err := record.Sign("test/other", []byte{1, 2, 3}, signer7)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := rec.Encode()
		rg := newRig(t)
		tree := validTree(t)
		tree[config.RevocationPath(rec.Digest())] = string(data)
		putTree(t, rg.mem, tree)
		_, err = config.Load(rg.w)
		if fe := fileError(t, err); fe.Kind != config.KindRevocation || fe.Reason != config.FileInvalid || fe.Path != config.RevocationPath(rec.Digest()) {
			t.Errorf("error = %+v", fe)
		}
	})
	t.Run("a record whose signature does not verify", func(t *testing.T) {
		_, _, rec := selfRevocation(t, 9)
		rec.Signatures[0].Sig[0] ^= 0xff
		data, err := rec.Encode()
		if err != nil {
			t.Fatal(err)
		}
		rg := newRig(t)
		tree := validTree(t)
		tree[config.RevocationPath(rec.Digest())] = string(data)
		putTree(t, rg.mem, tree)
		_, err = config.Load(rg.w)
		if fe := fileError(t, err); fe.Kind != config.KindRevocation || fe.Reason != config.FileInvalid {
			t.Errorf("error = %+v", fe)
		}
	})
	t.Run("a revocation file with a name that is not a digest", func(t *testing.T) {
		data, _, _ := selfRevocation(t, 9)
		rg := newRig(t)
		tree := validTree(t)
		tree[config.RevocationsDir+"/not-a-digest.rec"] = string(data)
		putTree(t, rg.mem, tree)
		_, err := config.Load(rg.w)
		if fe := fileError(t, err); fe.Reason != config.FileMisnamed || fe.Path != config.RevocationsDir+"/not-a-digest.rec" {
			t.Errorf("error = %+v", fe)
		}
	})
}

// A revocation is added by a write function that verifies it first and writes
// it under the name its digest gives; memory takes it over after the commit.
// Adding the same revocation again changes nothing.
func TestPutRevocation(t *testing.T) {
	rg, c := loaded(t)
	ensureState(t, rg.mem)
	data, digest, rec := selfRevocation(t, 9)
	put := func() {
		pr, err := config.PrepareRevocation(rec)
		if err != nil {
			t.Fatalf("PrepareRevocation: %v", err)
		}
		tx := rg.w.Begin(testIdentityRetire)
		defer tx.Close()
		done, err := c.PutRevocation(tx, pr)
		if err != nil {
			t.Fatalf("PutRevocation: %v", err)
		}
		if err := tx.Replace("result", "state/r", []byte("ok"), 0); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		done()
	}
	if len(c.Revocations()) != 1 {
		t.Fatal("setup")
	}
	put()
	if len(c.Revocations()) != 2 {
		t.Fatalf("revocations = %d, want 2", len(c.Revocations()))
	}
	got, err := rg.mem.ReadFile(config.RevocationPath(digest))
	if err != nil || string(got) != string(data) {
		t.Errorf("the file is %q, %v: it must be the record's own bytes", got, err)
	}
	put()
	if len(c.Revocations()) != 2 {
		t.Errorf("revocations = %d after adding one twice, want 2", len(c.Revocations()))
	}
	if again := rg.load(); len(again.Revocations()) != 2 {
		t.Errorf("a restart reads %d revocations", len(again.Revocations()))
	}

	t.Run("a record that does not verify is not written", func(t *testing.T) {
		rg, _ := loaded(t)
		bad, err := record.Sign("test/other", []byte{1}, signer(t, 7))
		if err != nil {
			t.Fatal(err)
		}
		before := snapshot(t, rg.mem)
		if _, err := config.PrepareRevocation(bad); err == nil {
			t.Error("PrepareRevocation accepted a record that is not a revocation")
		}
		if !reflect.DeepEqual(before, snapshot(t, rg.mem)) {
			t.Error("a refused record changed the disk")
		}
	})
}

// The confirmation of the offline backup holds only for the revocation it
// names, so that a new identity is reminded again.
func TestBackupConfirmsOnlyItsOwnRevocation(t *testing.T) {
	_, c := loaded(t)
	b, ok := c.Backup()
	if !ok {
		t.Fatal("no confirmation")
	}
	_, mine, _ := selfRevocation(t, 7)
	_, other, _ := selfRevocation(t, 9)
	if !b.Confirms(mine) || b.Confirms(other) || b.Confirms(record.Digest{}) {
		t.Errorf("Confirms: mine %v, other %v, zero %v", b.Confirms(mine), b.Confirms(other), b.Confirms(record.Digest{}))
	}
}

func TestPutBackup(t *testing.T) {
	rg, c := loaded(t)
	ensureState(t, rg.mem)
	_, digest, _ := selfRevocation(t, 7)
	nb := config.Backup{ConfirmedAt: time.Date(2026, 11, 2, 8, 30, 0, 0, time.UTC), Digest: digest}
	pb, err := config.PrepareBackup(nb)
	if err != nil {
		t.Fatal(err)
	}
	tx := rg.w.Begin(testBackupConfirm)
	defer tx.Close()
	done, err := c.PutBackup(tx, pb)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Replace("result", "state/r", []byte("ok"), 0); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	done()
	got, _ := c.Backup()
	if !got.ConfirmedAt.Equal(nb.ConfirmedAt) {
		t.Errorf("confirmed at %v", got.ConfirmedAt)
	}
	data, _ := rg.mem.ReadFile(config.BackupPath)
	if !strings.Contains(string(data), `"confirmed_at": "2026-11-02T08:30:00Z"`) {
		t.Errorf("file = %s", data)
	}
	for name, bad := range map[string]config.Backup{
		"no time":   {Digest: digest},
		"no digest": {ConfirmedAt: nb.ConfirmedAt},
	} {
		if _, err := config.PrepareBackup(bad); err == nil {
			t.Errorf("%s: PrepareBackup accepted it", name)
		}
	}
}
