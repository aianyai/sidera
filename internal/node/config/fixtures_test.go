package config_test

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io/fs"
	"path"
	"sort"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/txn"
	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/internal/revocation"
)

// Identifiers of the fixtures. They are the text forms of ids made from
// consecutive bytes, written out so that a test does not depend on the code
// that prints them.
const (
	node1Text = "node1qypqxpq9qcrsszg2pvxq6rs0zqg3yyc5z5tpwxqergd3c8g7rusqe9v0c0"
	node2Text = "node1zyfpx9q4zct3sxg6rvwp68slyqsjygeyy5nzw2pf9g4jctfw9ucqt8an5y"

	ep1Text = "ep1qypqxpq9qcrsszg2pvxq6rs0zqear7st" // the runtime endpoint of the instance "example"
	ep2Text = "ep1zyfpx9q4zct3sxg6rvwp68slyqq2dn6x" // its direct endpoint "notes"
	ep3Text = "ep1yy3zxfp9ycnjs2f29vkz6t30xqgefelz" // a closed session endpoint with settings
	ep4Text = "ep1xyerxdp4xcmnswf68v7r603lgqjsymlk" // an open session endpoint
	ep5Text = "ep1g9pyx3z9ger5sj22fdxy6nj02qwlu337" // an endpoint nobody knows

	personText = "person1q5rqwzqfpg9scrgwpugpzysnzs23v9ccrydpk8qarc0jqgfzyvjqet2zs9"

	opIDText  = "00112233445566778899aabbccddeeff"
	fingerHex = "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"
)

func mustEndpoint(t testing.TB, s string) ident.EndpointID {
	t.Helper()
	id, err := ident.ParseEndpointID(s)
	if err != nil {
		t.Fatalf("ParseEndpointID(%q): %v", s, err)
	}
	return id
}

func mustNode(t testing.TB, s string) ident.NodeID {
	t.Helper()
	id, err := ident.ParseNodeID(s)
	if err != nil {
		t.Fatalf("ParseNodeID(%q): %v", s, err)
	}
	return id
}

// The golden files of a valid, hand edited looking configuration. Each is also
// the exact canonical encoding of the value it decodes to.
const (
	goldenNode = `{
  "format": 1,
  "version": 3,
  "settings": {
    "reply_permission": true,
    "node_queue_limit": 10000,
    "disk_reserve_bytes": 268435456,
    "node_prompt_turns": 8,
    "archive_retention_days": 30,
    "log_total_bytes": 157286400,
    "endpoint_defaults": {
      "max_queue_s": 1800,
      "backlog_limit": 200
    }
  },
  "last_operation": {
    "id": "` + opIDText + `",
    "person": "local",
    "fingerprint": "` + fingerHex + `",
    "result": {
      "version": 3
    }
  }
}
`

	goldenPolicy = `{
  "format": 1,
  "version": 2,
  "rules": [
    {
      "effect": "allow",
      "subject": {
        "kind": "member"
      },
      "object": {
        "kind": "all_endpoints"
      },
      "action": "see"
    },
    {
      "effect": "deny",
      "subject": {
        "kind": "node",
        "node": "` + node2Text + `"
      },
      "object": {
        "kind": "endpoint",
        "endpoint": "` + ep2Text + `"
      },
      "action": "message.send",
      "conditions": {
        "not_after": "2027-01-01T00:00:00Z"
      }
    },
    {
      "effect": "allow",
      "subject": {
        "kind": "direct"
      },
      "object": {
        "kind": "runtime",
        "endpoint": "` + ep1Text + `"
      },
      "action": "session.create",
      "conditions": {
        "rate": {
          "count": 5,
          "window_s": 60
        },
        "presets": [
          "default"
        ]
      }
    },
    {
      "effect": "allow",
      "subject": {
        "kind": "endpoint",
        "address": "` + node2Text + `/` + ep2Text + `"
      },
      "object": {
        "kind": "all_endpoints"
      },
      "action": "message.send",
      "conditions": {
        "not_before": "2026-10-24T06:00:00Z",
        "max_body_bytes": 1024
      }
    },
    {
      "effect": "restrict",
      "object": {
        "kind": "node"
      },
      "action": "relay.reserve",
      "nodes": [
        "` + node1Text + `",
        "` + node2Text + `"
      ]
    }
  ]
}
`

	goldenPresets = `{
  "format": 1,
  "version": 2,
  "presets": {
    "default": {
      "local_sources": "allow",
      "description": "Created by sidera init."
    },
    "sandboxed": {
      "local_sources": "deny",
      "config": {
        "example": {
          "model": "small",
          "turns": 4
        }
      }
    }
  }
}
`

	goldenInstances = `{
  "format": 1,
  "version": 4,
  "instances": {
    "example": {
      "adapter": "example",
      "enabled": true,
      "config": {
        "workspace": "/Users/me/work"
      },
      "endpoints": {
        "` + ep1Text + `": {
          "backlog_limit": 50
        },
        "` + ep3Text + `": {
          "max_queue_s": 3600,
          "backlog_limit": 400
        }
      }
    }
  }
}
`

	goldenEndpointIDs = `{
  "format": 1,
  "runtime": "` + ep1Text + `",
  "direct": {
    "notes": "` + ep2Text + `"
  }
}
`

	goldenBackup = `{
  "format": 1,
  "confirmed_at": "2026-10-24T06:00:00Z",
  "digest": "%s"
}
`

	goldenCurrent = `{
  "format": 1,
  "version": "1.0.0"
}
`

	manifestJSON = `{"manifest_version":1,"id":"example","version":"%s","protocol":{"min":1,"max":1},` +
		`"entry":{"program":"bin/sidera-adapter-example"},"platforms":[{"os":"linux","arch":"amd64"}]}`
)

func manifestOf(id, version string) string {
	return strings.NewReplacer(`"id":"example"`, `"id":"`+id+`"`).Replace(strings.Replace(manifestJSON, "%s", version, 1))
}

func signer(t testing.TB, seed byte) record.Signer {
	t.Helper()
	s, err := record.NewEd25519Signer(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, ed25519.SeedSize)))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// selfRevocation is the node self-revocation of the node of signer seed, as a
// file: its bytes and its digest.
func selfRevocation(t testing.TB, seed byte) (data []byte, digest record.Digest, rec record.Record) {
	t.Helper()
	rec, err := revocation.IssueNodeSelfRevocation(signer(t, seed))
	if err != nil {
		t.Fatal(err)
	}
	data, err = rec.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return data, rec.Digest(), rec
}

// validTree is a complete valid configuration as files: path to content.
func validTree(t testing.TB) map[string]string {
	t.Helper()
	revData, digest, _ := selfRevocation(t, 7)
	return map[string]string{
		config.NodePath:                                goldenNode,
		config.PolicyPath:                              goldenPolicy,
		config.PresetsPath:                             goldenPresets,
		config.InstancesPath:                           goldenInstances,
		config.EndpointIDsPath("example"):              goldenEndpointIDs,
		config.BackupPath:                              strings.Replace(goldenBackup, "%s", digest.String(), 1),
		config.RevocationPath(digest):                  string(revData),
		config.AdapterCurrentPath("example"):           goldenCurrent,
		config.AdapterManifestPath("example", "1.0.0"): manifestOf("example", "1.0.0"),
		path.Join(config.AdapterVersionDir("example", "1.0.0"), "bin/sidera-adapter-example"): "#!program\n",
	}
}

var setupTag = fsx.Tag{Tx: "setup", Role: "f"}

// putTree writes files and the directories they need on a bare Mem, durably,
// outside any transaction.
func putTree(t testing.TB, mem *fsxtest.Mem, files map[string]string) {
	t.Helper()
	for _, d := range config.LayoutDirs() {
		mkdirAll(t, mem, d)
	}
	for p, content := range files {
		mkdirAll(t, mem, path.Dir(p))
		if err := fsx.Replace(mem, setupTag, p, []byte(content), fsx.ModePrivate); err != nil {
			t.Fatalf("setup Replace(%q): %v", p, err)
		}
	}
}

func mkdirAll(t testing.TB, mem *fsxtest.Mem, dir string) {
	t.Helper()
	if dir == "." {
		return
	}
	if _, err := mem.Stat(dir); err == nil {
		return
	}
	mkdirAll(t, mem, path.Dir(dir))
	if err := fsx.MakeDir(mem, setupTag, dir); err != nil {
		t.Fatalf("setup MakeDir(%q): %v", dir, err)
	}
}

func removeFile(t testing.TB, mem *fsxtest.Mem, p string) {
	t.Helper()
	if err := fsx.Remove(mem, setupTag, p); err != nil {
		t.Fatalf("setup Remove(%q): %v", p, err)
	}
	if err := fsx.SyncDir(mem, setupTag, path.Dir(p)); err != nil {
		t.Fatal(err)
	}
}

func removeTree(t testing.TB, mem *fsxtest.Mem, dir string) {
	t.Helper()
	if err := fsx.Discard(mem, setupTag, dir); err != nil {
		t.Fatalf("setup Discard(%q): %v", dir, err)
	}
}

// snapshot reads every file and directory of r, leftovers excluded: a file by
// path with its content, a directory as "path/".
func snapshot(t testing.TB, r fsx.Reader) map[string]string {
	t.Helper()
	got := map[string]string{}
	var walk func(dir string)
	walk = func(dir string) {
		entries, err := r.ReadDir(dir)
		if err != nil {
			t.Fatalf("ReadDir(%q): %v", dir, err)
		}
		for _, e := range entries {
			if fsx.IsLeftover(e.Name) {
				continue
			}
			p := path.Join(dir, e.Name)
			if e.IsDir {
				got[p+"/"] = ""
				walk(p)
				continue
			}
			b, err := r.ReadFile(p)
			if err != nil {
				t.Fatalf("ReadFile(%q): %v", p, err)
			}
			got[p] = string(b)
		}
	}
	walk(".")
	return got
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func exists(r fsx.Reader, p string) bool {
	_, err := r.Stat(p)
	return !errors.Is(err, fs.ErrNotExist)
}

// declarations that other packages compose from the roles config exports. The
// roles that are not config's stand for the store: endpoint, result and
// marker.
var (
	testInit = &txn.Decl{Name: "init", Kind: txn.Normal, Writes: []txn.Write{
		{Role: config.RoleDir, Op: txn.OpMakeDir, Many: true, Optional: true},
		{Role: config.RoleNode, Op: txn.OpReplace, Optional: true},
		{Role: config.RolePolicy, Op: txn.OpReplace, Optional: true},
		{Role: config.RolePresets, Op: txn.OpReplace, Optional: true},
		{Role: config.RoleInstances, Op: txn.OpReplace, Optional: true},
		{Role: "marker", Op: txn.OpReplace},
	}}
	testDirectRegister = &txn.Decl{Name: "direct-register", Kind: txn.Normal, Writes: []txn.Write{
		{Role: config.RoleEndpointIDs, Op: txn.OpReplace, Optional: true},
		{Role: "endpoint", Op: txn.OpReplace, Optional: true},
	}}
	testRuntimeAdmit = &txn.Decl{Name: "runtime-admit", Kind: txn.Normal, Writes: []txn.Write{
		{Role: config.RoleEndpointIDs, Op: txn.OpReplace, Optional: true},
		{Role: "endpoint", Op: txn.OpReplace, Optional: true},
	}}
	testBackupConfirm = &txn.Decl{Name: "backup-confirm", Kind: txn.Normal, Writes: []txn.Write{
		{Role: config.RoleBackup, Op: txn.OpReplace},
		{Role: "result", Op: txn.OpReplace},
	}}
	testIdentityRetire = &txn.Decl{Name: "identity-retire", Kind: txn.Normal, Writes: []txn.Write{
		{Role: config.RoleRevocation, Op: txn.OpReplace},
		{Role: "result", Op: txn.OpReplace},
	}}
	testAdapterInstall = &txn.Decl{Name: "adapter-install", Kind: txn.Normal, Writes: []txn.Write{
		{Role: config.RoleStale, Op: txn.OpDiscard, Optional: true, Prepare: true},
		{Role: config.RoleVersion, Op: txn.OpRenameDir, Prepare: true},
		{Role: config.RoleCurrent, Op: txn.OpReplace},
		{Role: "result", Op: txn.OpReplace},
		{Role: config.RoleOld, Op: txn.OpDiscard, Optional: true},
	}}
	composed = []*txn.Decl{testInit, testDirectRegister, testRuntimeAdmit, testBackupConfirm, testIdentityRetire, testAdapterInstall}
)

func newTable(t testing.TB) *txn.Table {
	t.Helper()
	decls := append(txn.Internal(), config.Transactions()...)
	decls = append(decls, composed...)
	tab, err := txn.NewTable(decls...)
	if err != nil {
		t.Fatalf("NewTable: %v", err)
	}
	return tab
}

// rig is a Writer over a Mem with the table of this package and the composed
// declarations.
type rig struct {
	t   testing.TB
	mem *fsxtest.Mem
	tab *txn.Table
	w   *txn.Writer
	obs txn.Observer
}

func newRig(t testing.TB) *rig {
	t.Helper()
	r := &rig{t: t, mem: fsxtest.NewMem(), tab: newTable(t)}
	r.w = txn.New(r.mem, r.tab, nil)
	return r
}

// onClone is a rig over an existing disk, as a restart gives.
func onDisk(t testing.TB, mem *fsxtest.Mem, obs txn.Observer) *rig {
	t.Helper()
	r := &rig{t: t, mem: mem, tab: newTable(t), obs: obs}
	r.w = txn.New(mem, r.tab, obs)
	return r
}

// loaded is a rig holding a valid tree, and the Config loaded from it.
func loaded(t testing.TB) (*rig, *config.Config) {
	t.Helper()
	r := newRig(t)
	putTree(t, r.mem, validTree(t))
	return r, r.load()
}

func (r *rig) load() *config.Config {
	r.t.Helper()
	c, err := config.Load(r.w)
	if err != nil {
		r.t.Fatalf("Load: %v", err)
	}
	return c
}

// request makes a request of the local owner with a recognisable operation.
func request(n byte, expected admin.Version) config.Request {
	var id admin.OperationID
	id[0], id[15] = n, n
	var fp admin.Fingerprint
	fp[0], fp[31] = n, n
	return config.Request{ID: id, Fingerprint: fp, Expected: expected}
}

// result returns a ResultFunc that records the state it was handed.
func result[T any](seen *[]T, doc string) config.ResultFunc[T] {
	return func(after T) (json.RawMessage, error) {
		*seen = append(*seen, after)
		return json.RawMessage(doc), nil
	}
}

func changeReason(t testing.TB, err error) *config.ChangeError {
	t.Helper()
	var ce *config.ChangeError
	if !errors.As(err, &ce) {
		t.Fatalf("error %v is not a *config.ChangeError", err)
	}
	return ce
}

func fileError(t testing.TB, err error) *config.FileError {
	t.Helper()
	var fe *config.FileError
	if !errors.As(err, &fe) {
		t.Fatalf("error %v is not a *config.FileError", err)
	}
	return fe
}
