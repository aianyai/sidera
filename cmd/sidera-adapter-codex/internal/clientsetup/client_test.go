package clientsetup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestMain lets the test binary stand in for the Codex program: when
// CLIENTSETUP_FAKE_CODEX is set it prints what the variable says and exits as told.
func TestMain(m *testing.M) {
	if mode := os.Getenv("CLIENTSETUP_FAKE_CODEX"); mode != "" {
		os.Exit(fakeCodexMain(mode))
	}
	os.Exit(m.Run())
}

func fakeCodexMain(mode string) int {
	switch mode {
	case "echo":
		// Standard input is closed, not a terminal that would be waited on.
		buf := make([]byte, 1)
		n, _ := os.Stdin.Read(buf)
		wd, _ := os.Getwd()
		fmt.Printf("args=%s stdin=%d wd=%s", strings.Join(os.Args[1:], " "), n, wd)
		return 0
	case "fail":
		fmt.Fprintln(os.Stderr, "Error: it did not work")
		return 1
	case "chatty":
		fmt.Fprint(os.Stderr, strings.Repeat("x", 100000)+"THE END")
		return 1
	case "unreadable":
		fmt.Fprintln(os.Stderr, "Error: failed to load marketplace(s):\n- `sidera` at /some dir/state/setup: marketplace root does not contain a supported manifest\n- `foo` at /elsewhere/foo: marketplace root does not contain a supported manifest")
		return 1
	}
	return 3
}

func realPath(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func fakeCLI(mode string, t *testing.T) cli {
	t.Setenv("CLIENTSETUP_FAKE_CODEX", mode)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return cli{program: exe, dir: t.TempDir()}
}

func TestCommandsRunWithoutInputInTheirDirectoryAndReportWhatCodexSaid(t *testing.T) {
	ctx := context.Background()
	c := fakeCLI("echo", t)
	out, err := c.run(ctx, "plugin", "add", "sidera@sidera", "--json")
	prefix, ranIn, _ := strings.Cut(out, " wd=")
	if got, _ := filepath.EvalSymlinks(ranIn); got != realPath(t, c.dir) || prefix != "args=plugin add sidera@sidera --json stdin=0" || err != nil {
		t.Errorf("out = %q, err = %v, want it to have run in %s", out, err, c.dir)
	}
	_, err = fakeCLI("fail", t).run(ctx, "mcp", "list")
	if err == nil || !strings.Contains(err.Error(), "it did not work") || !strings.Contains(err.Error(), `"mcp" "list"`) {
		t.Errorf("err = %v", err)
	}
	_, err = fakeCLI("chatty", t).run(ctx, "mcp", "list")
	if err == nil || !strings.HasSuffix(err.Error(), "THE END") || len(err.Error()) > 3000 {
		t.Errorf("a failure keeps the end of standard error, clipped: %d bytes", len(err.Error()))
	}
}

func TestCodexThatIsNotInstalledIsSaidSo(t *testing.T) {
	for _, program := range []string{"/nonexistent/codex", "codex-that-is-not-installed"} {
		_, err := (cli{program: program, dir: t.TempDir()}).run(context.Background(), "mcp", "list")
		if err == nil || !strings.Contains(err.Error(), "Codex is not installed") {
			t.Errorf("%s: err = %v", program, err)
		}
	}
}

func TestAnUnreadableMarketplaceIsNamedWithItsOwnDirectory(t *testing.T) {
	_, err := fakeCLI("unreadable", t).run(context.Background(), "plugin", "list", "--json")
	var u *unreadableError
	if !errors.As(err, &u) {
		t.Fatalf("err = %#v", err)
	}
	if root, ok := u.root("sidera"); !ok || root != "/some dir/state/setup" {
		t.Errorf("root of sidera = %q, %v", root, ok)
	}
	if root, ok := u.root("foo"); !ok || root != "/elsewhere/foo" {
		t.Errorf("root of foo = %q, %v", root, ok)
	}
	if _, ok := u.root("bar"); ok {
		t.Error("a marketplace that is not named has a root")
	}
	want(t, "others", u.namesOtherThan("sidera"), []string{"foo"})
	// Codex on Windows ends its lines with a carriage return.
	crlf := parseUnreadable("- `foo` at C:\\foo: "+unreadableText+"\r\n", "x")
	if root, ok := crlf.root("foo"); !ok || root != `C:\foo` {
		t.Errorf("root of foo with CRLF = %q, %v", root, ok)
	}
	// A message that names no marketplace is never taken to be ours.
	want(t, "unnamed", parseUnreadable("marketplace root does not contain a supported manifest", "x").namesOtherThan("sidera"), []string{"(one that Codex does not name)"})
}

// The fixtures below are what Codex 0.166.0 wrote.
func TestParsePlugin(t *testing.T) {
	cases := []struct {
		name, in string
		want     plugin
	}{
		{"installed", `{"installed":[{"pluginId":"sidera@sidera","name":"sidera","marketplaceName":"sidera","version":"0.15.0","installed":true,"enabled":true,"source":{"source":"local","path":"/x"}}],"available":[]}`,
			plugin{Installed: true, Enabled: true, Version: "0.15.0"}},
		{"disabled", `{"installed":[{"name":"sidera","marketplaceName":"sidera","version":"1","installed":true,"enabled":false}],"available":[]}`,
			plugin{Installed: true, Version: "1"}},
		{"a plugin of that name from another marketplace", `{"installed":[{"name":"sidera","marketplaceName":"other","version":"1","installed":true,"enabled":true}]}`, plugin{}},
		{"none", `{"installed":[],"available":[]}`, plugin{}},
	}
	for _, c := range cases {
		got, err := parsePlugin(c.in)
		if err != nil || got != c.want {
			t.Errorf("%s: %+v, %v", c.name, got, err)
		}
	}
	if _, err := parsePlugin("not json"); err == nil {
		t.Error("text was taken for JSON")
	}
}

func TestParseMarketplace(t *testing.T) {
	list := `{"marketplaces":[
	{"name":"other","root":"/x/y","marketplaceSource":{"sourceType":"local","source":"/x/y"}},
	{"name":"sidera","root":"/Users/a b/state/setup/codex/marketplace","marketplaceSource":{"sourceType":"local","source":"/Users/a b/state/setup/codex/marketplace"}}]}`
	if got, err := parseMarketplace(list, "sidera"); err != nil || !got.Registered || got.Root != "/Users/a b/state/setup/codex/marketplace" {
		t.Errorf("got %+v, %v", got, err)
	}
	for _, none := range []string{`{"marketplaces":[]}`, `{"marketplaces":[{"name":"other","root":"/x"}]}`} {
		if got, err := parseMarketplace(none, "sidera"); err != nil || got.Registered {
			t.Errorf("%q: %+v, %v", none, got, err)
		}
	}
	// A reply that is not the list is an error: reading it as "none" would plan to add a
	// marketplace that exists.
	for _, bad := range []string{"", "No plugin marketplaces in scope.\n", `{}`, `{"marketplaces":3}`, `[]`} {
		if _, err := parseMarketplace(bad, "sidera"); err == nil {
			t.Errorf("%q was taken for a marketplace list", bad)
		}
	}
}

func TestParseServer(t *testing.T) {
	list := `[{"name":"other","enabled":true,"transport":{"type":"stdio","command":"x","args":[]}},
	{"name":"sidera","enabled":true,"disabled_reason":null,"transport":{"type":"stdio","command":"/bin/sidera","args":["--home","/h","mcp"],"env":null}}]`
	s, err := parseServer(list, "sidera")
	if err != nil || !s.Found || !s.Enabled || s.Command != "/bin/sidera" || !slices.Equal(s.Args, []string{"--home", "/h", "mcp"}) {
		t.Errorf("got %+v, %v", s, err)
	}
	if s, _ := parseServer(`[]`, "sidera"); s.Found {
		t.Error("found in an empty list")
	}
	get := "sidera\n  enabled: true\n  transport: stdio\n  command: /bin/sidera\n  args: mcp\n  default_tools_approval_mode: approve\n  remove: codex mcp remove sidera\n"
	if m := approvalLine.FindStringSubmatch(get); m == nil || m[1] != "approve" {
		t.Errorf("approval = %v", m)
	}
	if m := approvalLine.FindStringSubmatch(strings.Replace(get, "  default_tools_approval_mode: approve\n", "", 1)); m != nil {
		t.Errorf("an approval mode that is not there was read: %v", m)
	}
}
