package cli

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestGlobalFlagsUseTheRegisteredLocalArity(t *testing.T) {
	home, err := filepath.Abs("chosen-home")
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"--json", "--home", "--", "-h"} {
		t.Run(value, func(t *testing.T) {
			local := flags("test")
			payload := local.String("new-value-flag", "", "a newly registered local flag")
			enabled := local.Bool("new-bool-flag", false, "a newly registered boolean")
			c := command{Config: Config{Getenv: func(string) string { return "" }}}
			args := []string{"--home", home, "operand", "--new-value-flag", value, "--new-bool-flag", "--json", "--", "--home"}
			before := append([]string(nil), args...)
			remaining, err := c.globals(args, local)
			if err != nil {
				t.Fatal(err)
			}
			positionals, err := parse(local, remaining)
			if err != nil {
				t.Fatal(err)
			}
			if *payload != value || !*enabled || !c.json || c.home != home || !reflect.DeepEqual(positionals, []string{"operand", "--home"}) || !reflect.DeepEqual(args, before) {
				t.Fatalf("payload %q, enabled %t, json %t, home %q, positionals %q, argv %q", *payload, *enabled, c.json, c.home, positionals, args)
			}
		})
	}
}
