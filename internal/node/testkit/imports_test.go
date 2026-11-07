package testkit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

// The third-party side of the testkit stands in for what a stranger would write:
// an adapter, a native runtime, the files they leave behind. Those programs know
// the adapter protocol and nothing else of Sidera, so the packages that play
// them import only the standard library, the SDK module, the modules the SDK
// module itself requires, and each other. A real third-party adapter that
// imports the SDK links what the SDK needs, so those requirements belong to
// "the SDK module" and are not a dependency of the adapter's own choosing. If
// one of the packages reached an internal package, or a module only the daemon
// requires, the protocol boundary would be proved against a program that is not
// a third party, and the proof would be empty.

// thirdPartyPackages are the packages of the third-party side, relative to the
// module path, and the two programs built from them.
var thirdPartyPackages = []string{
	"internal/node/testkit/ledger",
	"internal/node/testkit/fakectl",
	"internal/node/testkit/fakeruntime",
	"internal/node/testkit/fakeadapter",
	"cmd/sidera-fake-adapter",
	"cmd/sidera-fake-runtime",
}

func TestThirdPartySideImportsOnlyStandardLibraryAndSDK(t *testing.T) {
	root := moduleRoot(t)
	module := modulePath(t, root)
	sdk := module + "/sdk/go"
	sdkRequires := sdkRequirements(t, root)
	own := make([]string, len(thirdPartyPackages))
	for i, p := range thirdPartyPackages {
		own[i] = module + "/" + p
	}

	for _, goos := range []string{"darwin", "linux", "windows"} {
		for _, tags := range []string{"", "crashtest,testclock,acceptance"} {
			name := goos + "/tags=" + tags
			t.Run(name, func(t *testing.T) {
				env := append(os.Environ(), "GOOS="+goos, "CGO_ENABLED=0")
				listing := goList(t, root, env, tags, append([]string{
					"-deps", "-f", `{{if not .Standard}}{{.ImportPath}}{{end}}`,
				}, own...))
				deps := strings.Fields(listing)
				for _, p := range own {
					if !slices.Contains(deps, p) {
						t.Errorf("%s: package %s is not in the dependency listing: it does not exist or is not buildable", name, p)
					}
				}
				for _, v := range forbiddenDeps(deps, sdk, sdkRequires, own) {
					t.Errorf("%s: %s", name, v)
				}
			})
		}
	}
}

// forbiddenDeps returns one message per non-standard dependency that is neither
// in the SDK module, nor in a module the SDK module requires, nor one of the
// third-party packages themselves.
func forbiddenDeps(deps []string, sdkPath string, sdkRequires []string, own []string) []string {
	allowedModules := append([]string{sdkPath}, sdkRequires...)
	var out []string
	for _, d := range deps {
		d, _, _ = strings.Cut(d, " [")
		if slices.ContainsFunc(allowedModules, func(m string) bool { return d == m || strings.HasPrefix(d, m+"/") }) {
			continue
		}
		if slices.Contains(own, d) {
			continue
		}
		out = append(out, fmt.Sprintf("depends on %s: only the standard library, %s/..., the modules %s requires and the third-party side itself are allowed", d, sdkPath, sdkPath))
	}
	sort.Strings(out)
	return out
}

// sdkRequirements are the module paths that sdk/go/go.mod requires, direct and
// indirect, read from the file so that the list cannot drift from it.
func sdkRequirements(t *testing.T, root string) []string {
	t.Helper()
	cmd := exec.Command("go", "mod", "edit", "-json", filepath.Join("sdk", "go", "go.mod"))
	cmd.Dir = root
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("go mod edit -json sdk/go/go.mod: %v\n%s", err, stderr.String())
	}
	var mod struct {
		Require []struct{ Path string }
	}
	if err := json.Unmarshal(stdout.Bytes(), &mod); err != nil {
		t.Fatalf("decode go mod edit -json of sdk/go/go.mod: %v", err)
	}
	var paths []string
	for _, r := range mod.Require {
		paths = append(paths, r.Path)
	}
	return paths
}

func TestImportRuleDetectsViolations(t *testing.T) {
	const sdk = "m/sdk/go"
	sdkRequires := []string{"golang.org/x/sys", "golang.org/x/text"}
	own := []string{"m/internal/node/testkit/ledger", "m/cmd/sidera-fake-adapter"}
	cases := []struct {
		name string
		deps []string
		want int
	}{
		{"the standard library needs no entry", nil, 0},
		{"the SDK module and its packages are allowed", []string{"m/sdk/go/peer", "m/sdk/go/adapterproto"}, 0},
		{"the third-party side itself is allowed", []string{"m/internal/node/testkit/ledger"}, 0},
		{"a contract package is not", []string{"m/internal/delivery"}, 1},
		{"a runtime package is not", []string{"m/internal/node/fsx"}, 1},
		{"another testkit package is not", []string{"m/internal/node/testkit/harness"}, 1},
		{"a third-party module is not", []string{"github.com/fxamacker/cbor/v2"}, 1},
		{"a package of a module the SDK requires is allowed", []string{"golang.org/x/sys/unix", "golang.org/x/text"}, 0},
		{"a path that only starts like a required module is not", []string{"golang.org/x/sysx"}, 1},
		{"a path that only starts like the SDK is not", []string{"m/sdk/gonext"}, 1},
		{"a test variant of an allowed package is allowed", []string{"m/sdk/go/peer [m/sdk/go/peer.test]"}, 0},
		{"two violations are two messages", []string{"m/internal/delivery", "m/internal/node/store"}, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := forbiddenDeps(c.deps, sdk, sdkRequires, own); len(got) != c.want {
				t.Fatalf("got %d violations %q, want %d", len(got), got, c.want)
			}
		})
	}
}

func goList(t *testing.T, root string, env []string, tags string, args []string) string {
	t.Helper()
	full := []string{"list"}
	if tags != "" {
		full = append(full, "-tags", tags)
	}
	full = append(full, args...)
	cmd := exec.Command("go", full...)
	cmd.Dir = root
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("go %s: %v\n%s", strings.Join(full, " "), err, stderr.String())
	}
	return stdout.String()
}

// moduleRoot is the directory of the module's go.mod, three levels above this
// package.
func moduleRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("module root %s has no go.mod: %v", root, err)
	}
	return root
}

func modulePath(t *testing.T, root string) string {
	t.Helper()
	out := goList(t, root, os.Environ(), "", []string{"-f", "{{.Module.Path}}", "./internal/node/testkit"})
	return strings.TrimSpace(out)
}
