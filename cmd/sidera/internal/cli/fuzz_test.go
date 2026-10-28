package cli

import (
	"io"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
)

// The management client validates the envelope. These projections and the human
// renderer additionally handle the heterogeneous result objects of each method.
func FuzzManagementProjectionAndRendering(f *testing.F) {
	for _, seed := range []string{`{"result":{"version":1}}`, `{"result":{"settings":{"limit":20000,"flag":false}}}`, `{"result":{"endpoints":[{"address":"a","state":"open"}]}}`, `{"result":{"version":18446744073709551615}}`, `{"result":{"version":null}}`, `{"result":[]}`} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		_, _ = managementVersion(raw)
		object, err := managementObject(raw)
		if err == nil {
			if err := humanFields(io.Discard, "", object); err != nil {
				t.Fatalf("a decoded result could not be rendered: %v", err)
			}
		}
	})
}

func FuzzSettingAssignments(f *testing.F) {
	for _, seed := range []string{"limit=20000", "flag=false", "name=text", "limit=", "name=\"text\"", "nested={\"value\":2}", "no-equals", "=value"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, arg string) {
		values, err := assignments([]string{arg})
		if err == nil && len(values) != 1 {
			t.Fatalf("one accepted assignment produced %d fields", len(values))
		}
	})
}

// Global scanning must preserve every registered local value, including values
// spelled like global flags, while keeping the caller's argv unchanged.
func FuzzGlobalAndLocalArguments(f *testing.F) {
	home, err := filepath.Abs("chosen-home")
	if err != nil {
		f.Fatal(err)
	}
	for _, seed := range []string{"--json", "--home", "--", "-h", "", "ordinary", "--payload=value"} {
		f.Add(seed, "--literal", int64(-1), true, false)
		f.Add(seed, "operand", int64(42), false, true)
	}
	f.Fuzz(func(t *testing.T, value, operand string, number int64, enabled, inline bool) {
		local := flags("fuzz")
		payload := local.String("payload", "", "local text")
		count := local.Int64("count", 0, "local number")
		active := local.Bool("active", false, "local boolean")
		args := []string{"--home=" + home, "before"}
		if inline {
			args = append(args, "--payload="+value)
		} else {
			args = append(args, "--payload", value)
		}
		boolean := "--active=false"
		if enabled {
			boolean = "--active"
		}
		args = append(args, boolean, "--json", "--count", strconv.FormatInt(number, 10), "--", operand)
		before := append([]string(nil), args...)
		c := command{Config: Config{Getenv: func(string) string { return "" }}}
		remaining, err := c.globals(args, local)
		if err != nil {
			t.Fatal(err)
		}
		positionals, err := parse(local, remaining)
		if err != nil {
			t.Fatal(err)
		}
		if *payload != value || *count != number || *active != enabled || !c.json || c.home != home || !reflect.DeepEqual(positionals, []string{"before", operand}) || !reflect.DeepEqual(args, before) {
			t.Fatalf("accepted argv lost a value or changed its input: payload %q, count %d, active %t, json %t, home %q, positionals %q", *payload, *count, *active, c.json, c.home, positionals)
		}
	})
}
