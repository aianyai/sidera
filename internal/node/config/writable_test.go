package config_test

import (
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/authz"
	"github.com/aianyai/sidera/internal/node/config"
)

// No file is written that its own decoder refuses: a change whose request has no
// operation id would otherwise leave a file the next start refuses to read.
func TestChangeWithoutOperationIDWritesNothing(t *testing.T) {
	for _, ch := range changes(t) {
		t.Run(ch.name, func(t *testing.T) {
			rg, c := loaded(t)
			before := snapshot(t, rg.mem)
			_, _, err := ch.do(c, request(0, ch.current), isOpen(t))
			if err == nil {
				t.Fatal("a change without an operation id was accepted")
			}
			if ce := changeReason(t, err); ce.Reason != config.ChangeInvalid || ce.Field != "id" {
				t.Errorf("error = %+v, want ChangeInvalid at id", ce)
			}
			if !reflect.DeepEqual(before, snapshot(t, rg.mem)) {
				t.Error("a refused change wrote to the disk")
			}
			if got := rg.load(); !reflect.DeepEqual(got.Node(), c.Node()) || !reflect.DeepEqual(got.Presets(), c.Presets()) || !reflect.DeepEqual(got.Instances(), c.Instances()) {
				t.Error("a refused change altered the configuration")
			}
		})
	}
}

// An operation with a zero id is not encoded, whichever file carries it.
func TestEncodeRefusesAZeroOperationID(t *testing.T) {
	op := &config.Operation{Result: []byte(`{}`)}
	node := config.Node{Version: 1, Settings: config.DefaultNodeSettings(), LastOperation: op}
	policy := config.PolicyFile{Version: 1, Policy: authz.DefaultPolicy(), LastOperation: op}
	presets := config.Presets{Version: 1, Items: map[string]config.Preset{}, LastOperation: op}
	instances := config.Instances{Version: 1, Items: map[string]config.Instance{}, LastOperation: op}
	for name, enc := range map[string]func() ([]byte, error){
		"node": node.Encode, "policy": policy.Encode, "presets": presets.Encode, "instances": instances.Encode,
	} {
		t.Run(name, func(t *testing.T) {
			if data, err := enc(); err == nil {
				t.Errorf("encoded an operation with a zero id:\n%s", data)
			}
		})
	}
}

// Versions start at 1 in every versioned file, so a value at version 0 is not
// encoded.
func TestEncodeRefusesVersionZero(t *testing.T) {
	node := config.Node{Settings: config.DefaultNodeSettings()}
	policy := config.PolicyFile{Policy: authz.DefaultPolicy()}
	presets := config.Presets{Items: map[string]config.Preset{}}
	instances := config.Instances{Items: map[string]config.Instance{}}
	for name, enc := range map[string]func() ([]byte, error){
		"node": node.Encode, "policy": policy.Encode, "presets": presets.Encode, "instances": instances.Encode,
	} {
		t.Run(name, func(t *testing.T) {
			if data, err := enc(); err == nil {
				t.Errorf("encoded version 0:\n%s", data)
			}
		})
	}
}

// A collection at the largest version cannot be changed: the next version would
// wrap to 0, which no file may hold.
func TestChangeRefusesToWrapTheVersion(t *testing.T) {
	max := fmt.Sprint(uint64(math.MaxUint64))
	tree := validTree(t)
	for file, from := range map[string]string{
		config.NodePath:      `"version": 3,`,
		config.PresetsPath:   `"version": 2,`,
		config.InstancesPath: `"version": 4,`,
	} {
		if !strings.Contains(tree[file], from) {
			t.Fatalf("%s lacks %s", file, from)
		}
		tree[file] = strings.Replace(tree[file], from, `"version": `+max+`,`, 1)
	}
	for _, ch := range changes(t) {
		t.Run(ch.name, func(t *testing.T) {
			rg := newRig(t)
			putTree(t, rg.mem, tree)
			c := rg.load()
			before := snapshot(t, rg.mem)
			_, _, err := ch.do(c, request(5, admin.Version(math.MaxUint64)), isOpen(t))
			if err == nil {
				t.Fatal("a change that wraps the version was accepted")
			}
			// Refused by the change itself, not only by the encoder behind it.
			if ce := changeReason(t, err); ce.Reason != config.ChangeInvalid || ce.Field != "version" {
				t.Errorf("error = %+v, want ChangeInvalid at version", ce)
			}
			if !reflect.DeepEqual(before, snapshot(t, rg.mem)) {
				t.Error("a refused change wrote to the disk")
			}
			rg.load() // the file is still readable
		})
	}
}
