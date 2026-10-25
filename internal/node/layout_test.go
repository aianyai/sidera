package node

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// These tests enforce the layout of the module: contract packages stay pure
// and never reach the runtime packages below internal/node, and runtime
// packages obtain time, files and randomness only through the seams that tests
// replace. A violation of the last three fails no other test at once; it makes
// a fake-clock test fail now and then, or lets the crash model miss a write
// path. Each rule has an exception list below. Adding an entry is a change to
// this file and is reviewed as one.

const runtimeDir = "internal/node"

// testkitDir is the test tooling of the runtime packages.
const testkitDir = "internal/node/testkit"

// Exceptions are exact directories, relative to the module root. A package
// that sits below an exempt directory is not exempt: it is added by name.
var (
	// timeExceptions may read the real time: the clock itself and the fake
	// clock that shares its types.
	timeExceptions = []string{
		"internal/node/clock",
		"internal/node/clock/clocktest",
	}

	// fileExceptions may use the operating system's file functions directly.
	// fsx is the file-system layer and ipc creates and protects socket files,
	// which net and the operating system own. The testkit packages stand in for
	// the outside world in tests: a third-party adapter, a native runtime and
	// the ledger that records what happened. They import only the standard
	// library and sdk/go, as a real third party would, so they cannot reach
	// fsx; the files they touch are not daemon state; and the ledger is a test
	// oracle that must stay independent of the fsx under test.
	fileExceptions = []string{
		"internal/node/fsx",
		"internal/node/ipc",
		"internal/node/testkit/ledger",
		"internal/node/testkit/fakeadapter",
		"internal/node/testkit/fakeruntime",
	}

	// randomnessExceptions may import math/rand: the crash model draws its
	// clones from a *rand.Rand that the test injects, and the engine-layer
	// fixture makes that source and the engine's from the seed a test names.
	randomnessExceptions = []string{
		"internal/node/fsx/fsxtest",
		"internal/node/testkit/harness",
	}

	// cryptoRandDirs may import crypto/rand: the composition root hands its
	// reader down as an io.Reader. cmd/ is outside the scanned tree.
	cryptoRandDirs = []string{
		"internal/node/daemon",
	}
)

// bannedTimeFuncs are the functions of package time that read or wait on the
// real clock.
var bannedTimeFuncs = map[string]map[string]bool{
	"time": setOf("Now", "Since", "Until", "After", "AfterFunc", "NewTimer", "NewTicker", "Tick", "Sleep"),
}

// bannedFileFuncs are the file functions of package os, their counterparts in
// io/ioutil, path/filepath, syscall and golang.org/x/sys, and the
// whole-directory helpers that open files underneath. Functions that only read
// the process environment (Getenv, Exit, Getpid), the standard streams and the
// pure reader helpers of io/ioutil (ReadAll, NopCloser, Discard) are not here.
var bannedFileFuncs = map[string]map[string]bool{
	"os": setOf(
		"Create", "CreateTemp", "OpenFile", "Open", "ReadFile", "WriteFile",
		"Rename", "Remove", "RemoveAll", "Mkdir", "MkdirAll", "MkdirTemp",
		"ReadDir", "Stat", "Lstat", "Chmod", "Chown", "Lchown", "Chtimes",
		"Link", "Symlink", "Readlink", "Truncate", "OpenRoot", "DirFS", "CopyFS",
	),
	"io/ioutil":     setOf("ReadFile", "WriteFile", "ReadDir", "TempFile", "TempDir"),
	"path/filepath": setOf("Walk", "WalkDir", "Glob", "EvalSymlinks"),
	// syscall carries the file functions of every platform.
	"syscall":                  union(unixFileFuncs(), windowsFileFuncs()),
	"golang.org/x/sys/unix":    unixFileFuncs(),
	"golang.org/x/sys/windows": windowsFileFuncs(),
}

func unixFileFuncs() map[string]bool {
	return setOf(
		"Open", "Openat", "Creat", "Unlink", "Unlinkat", "Rename", "Renameat",
		"Mkdir", "Mkdirat", "Rmdir", "Stat", "Lstat", "Fstat", "Fstatat",
		"Chmod", "Fchmod", "Fchmodat", "Chown", "Fchown", "Lchown", "Link",
		"Linkat", "Symlink", "Symlinkat", "Readlink", "Truncate", "Ftruncate",
		"Fsync", "Fdatasync", "Flock", "Mknod", "Mkfifo", "FcntlFlock",
	)
}

func windowsFileFuncs() map[string]bool {
	return setOf(
		"CreateFile", "CreateFile2", "DeleteFile", "MoveFile", "MoveFileEx",
		"ReplaceFile", "CopyFile", "CreateDirectory", "RemoveDirectory",
		"FlushFileBuffers", "WriteFile", "ReadFile", "SetEndOfFile",
		"SetFilePointer", "SetFileAttributes", "GetFileAttributes",
		"GetFileAttributesEx", "FindFirstFile", "LockFileEx", "UnlockFileEx",
		"CreateHardLink", "CreateSymbolicLink", "Open", "Stat", "Mkdir",
		"Rmdir", "Unlink", "Rename", "Fsync", "Ftruncate", "Chmod", "Symlink",
		"Link",
	)
}

func union(sets ...map[string]bool) map[string]bool {
	out := map[string]bool{}
	for _, set := range sets {
		for name := range set {
			out[name] = true
		}
	}
	return out
}

func setOf(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

// --- contract packages never import the runtime ---

// TestContractPackagesDoNotImportRuntime lists every package under internal/
// except internal/node, with its tests, and fails when its transitive
// dependencies include internal/node or cmd. The listing is repeated per
// operating system and per tag set because a platform file or a tagged file can
// pull in a dependency the default build does not.
func TestContractPackagesDoNotImportRuntime(t *testing.T) {
	root := moduleRoot(t)
	module := modulePath(t, root)
	forbidden := []string{module + "/" + runtimeDir, module + "/cmd"}

	for _, goos := range []string{"darwin", "linux", "windows"} {
		for _, tags := range []string{"", "crashtest,testclock,acceptance"} {
			name := goos + "/tags=" + tags
			t.Run(name, func(t *testing.T) {
				env := append(os.Environ(), "GOOS="+goos, "CGO_ENABLED=0")
				contract := contractPackages(t, root, env, tags, module)
				if len(contract) == 0 {
					t.Fatalf("no contract package found under internal/ for %s: the listing is broken", name)
				}
				listing := goList(t, root, env, tags, append([]string{
					"-deps", "-test",
					"-f", `{{.ImportPath}}{{"\t"}}{{join .Imports " "}}`,
				}, contract...))
				violations := runtimeImporters(listing, forbidden)
				for _, v := range violations {
					t.Errorf("%s: %s", name, v)
				}
			})
		}
	}
}

// runtimeImporters parses go list output of "importer<TAB>imports" lines and
// returns one message per import of a forbidden package by a package that is
// not itself forbidden. Importers inside the forbidden trees are skipped: the
// listing contains them only as dependencies, and what they import is exactly
// what the rule allows them.
func runtimeImporters(listing string, forbidden []string) []string {
	isForbidden := func(importPath string) bool {
		importPath, _, _ = strings.Cut(importPath, " [")
		return slices.ContainsFunc(forbidden, func(prefix string) bool {
			return importPath == prefix || strings.HasPrefix(importPath, prefix+"/")
		})
	}
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(listing), "\n") {
		importer, imports, _ := strings.Cut(line, "\t")
		if isForbidden(importer) {
			continue
		}
		for _, imp := range strings.Fields(imports) {
			if isForbidden(imp) {
				out = append(out, fmt.Sprintf("%s imports %s", importer, imp))
			}
		}
	}
	sort.Strings(out)
	return out
}

func TestRuntimeImportersDetectViolations(t *testing.T) {
	forbidden := []string{"m/internal/node", "m/cmd"}
	listing := strings.Join([]string{
		"m/internal/a\tfmt m/internal/b",
		"m/internal/b\tm/internal/node/fsx",
		"m/internal/c [m/internal/c.test]\tm/cmd/tool testing",
		"m/internal/node/fsx\tm/internal/node/clock os",
		"m/internal/nodes\tfmt",
	}, "\n")
	got := runtimeImporters(listing, forbidden)
	want := []string{
		"m/internal/b imports m/internal/node/fsx",
		"m/internal/c [m/internal/c.test] imports m/cmd/tool",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func contractPackages(t *testing.T, root string, env []string, tags, module string) []string {
	t.Helper()
	listing := goList(t, root, env, tags, []string{"-f", "{{.ImportPath}}", "./internal/..."})
	var pkgs []string
	for _, p := range strings.Fields(listing) {
		if p == module+"/"+runtimeDir || strings.HasPrefix(p, module+"/"+runtimeDir+"/") {
			continue
		}
		pkgs = append(pkgs, p)
	}
	return pkgs
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
		t.Fatalf("go %s (GOOS=%s): %v\n%s", strings.Join(full, " "), envValue(env, "GOOS"), err, stderr.String())
	}
	return stdout.String()
}

func envValue(env []string, key string) string {
	for i := len(env) - 1; i >= 0; i-- {
		if v, ok := strings.CutPrefix(env[i], key+"="); ok {
			return v
		}
	}
	return ""
}

// moduleRoot is the directory of the module's go.mod, two levels above this
// package.
func moduleRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
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
	out := goList(t, root, os.Environ(), "", []string{"-f", "{{.Module.Path}}", "./internal/node"})
	return strings.TrimSpace(out)
}

// --- runtime source scans ---

// TestRuntimeReadsTimeOnlyThroughClock fails on any use of a function that
// reads or waits on the real clock in a runtime package outside the clock
// packages. Fake clocks can then drive every deadline, lease and retry.
func TestRuntimeReadsTimeOnlyThroughClock(t *testing.T) {
	scanRuntime(t, timeExceptions, func(fset *token.FileSet, f *ast.File) []string {
		return bannedSelectors(fset, f, bannedTimeFuncs)
	})
}

// TestRuntimeTouchesFilesOnlyThroughFsx fails on any use of an operating
// system file function in a runtime package outside fsx and the listed
// exceptions. Every write then passes through the layer the crash model wraps.
func TestRuntimeTouchesFilesOnlyThroughFsx(t *testing.T) {
	scanRuntime(t, fileExceptions, func(fset *token.FileSet, f *ast.File) []string {
		return bannedSelectors(fset, f, bannedFileFuncs)
	})
}

// TestRuntimeTakesRandomnessAsReader fails when a runtime package imports
// math/rand, or crypto/rand outside the composition root, so randomness always
// reaches the code as an injected io.Reader.
func TestRuntimeTakesRandomnessAsReader(t *testing.T) {
	scanRuntimeDirs(t, func(dir string, fset *token.FileSet, f *ast.File) []string {
		var banned []string
		if !slices.Contains(randomnessExceptions, dir) {
			banned = append(banned, "math/rand", "math/rand/v2")
		}
		if !slices.Contains(cryptoRandDirs, dir) {
			banned = append(banned, "crypto/rand")
		}
		return bannedImports(fset, f, banned)
	})
}

// dirCheck reports violations in one parsed file. dir is the file's directory
// relative to the scanned root, with forward slashes.
type dirCheck func(dir string, fset *token.FileSet, f *ast.File) []string

func scanRuntime(t *testing.T, exceptions []string, check func(*token.FileSet, *ast.File) []string) {
	t.Helper()
	scanRuntimeDirs(t, exemptDirs(exceptions, check))
}

// exemptDirs applies check to every directory except the exact ones listed.
func exemptDirs(exceptions []string, check func(*token.FileSet, *ast.File) []string) dirCheck {
	return func(dir string, fset *token.FileSet, f *ast.File) []string {
		if slices.Contains(exceptions, dir) {
			return nil
		}
		return check(fset, f)
	}
}

// scanRuntimeDirs scans the runtime tree of the module and fails the test on
// every violation.
func scanRuntimeDirs(t *testing.T, check dirCheck) {
	t.Helper()
	root := moduleRoot(t)
	violations, err := scanTree(root, filepath.Join(root, filepath.FromSlash(runtimeDir)), check)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range violations {
		t.Error(v)
	}
}

// scanTree parses every non-test Go file below base, whatever its build
// constraints, and returns what check reports for it. Directories named
// testdata and those starting with "." or "_" are skipped, as the go tool
// skips them. Directories passed to check are relative to root.
func scanTree(root, base string, check dirCheck) ([]string, error) {
	fset := token.NewFileSet()
	var violations []string
	err := filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if p != base && (name == "testdata" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, filepath.Dir(p))
		if err != nil {
			return err
		}
		violations = append(violations, check(filepath.ToSlash(rel), fset, f)...)
		return nil
	})
	return violations, err
}

// bannedSelectors returns one message per selector pkg.Name in f where pkg is
// an import of a package in banned and Name is one of its banned names. It
// looks at selectors, not only calls, so `now := time.Now` is caught, and it
// resolves import aliases. A dot import of a package in banned is reported as a
// whole: its names can no longer be told from local ones.
func bannedSelectors(fset *token.FileSet, f *ast.File, banned map[string]map[string]bool) []string {
	var out []string
	locals := map[string]string{} // local package name -> import path
	for _, spec := range f.Imports {
		importPath, err := strconv.Unquote(spec.Path.Value)
		if err != nil || banned[importPath] == nil {
			continue
		}
		name := path.Base(importPath)
		if spec.Name != nil {
			name = spec.Name.Name
		}
		switch name {
		case "_":
		case ".":
			out = append(out, fmt.Sprintf("%s: dot import of %s hides its banned functions", fset.Position(spec.Pos()), importPath))
		default:
			locals[name] = importPath
		}
	}
	if len(locals) == 0 {
		return out
	}
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		id, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		if importPath, ok := locals[id.Name]; ok && banned[importPath][sel.Sel.Name] {
			out = append(out, fmt.Sprintf("%s: %s.%s is not allowed here", fset.Position(sel.Pos()), importPath, sel.Sel.Name))
		}
		return true
	})
	return out
}

// bannedImports returns one message per import of a package in banned.
func bannedImports(fset *token.FileSet, f *ast.File, banned []string) []string {
	var out []string
	for _, spec := range f.Imports {
		importPath, err := strconv.Unquote(spec.Path.Value)
		if err == nil && slices.Contains(banned, importPath) {
			out = append(out, fmt.Sprintf("%s: import of %s is not allowed here", fset.Position(spec.Pos()), importPath))
		}
	}
	return out
}

// TestRuntimeScansDetectViolations proves each scan reports what its rule
// forbids and nothing that the rule allows, so an empty tree passing means the
// tree is clean and not that the scan is blind.
func TestRuntimeScansDetectViolations(t *testing.T) {
	type row struct {
		name string
		scan func(*token.FileSet, *ast.File) []string
		src  string
		want int
	}
	timeScan := func(fset *token.FileSet, f *ast.File) []string { return bannedSelectors(fset, f, bannedTimeFuncs) }
	fileScan := func(fset *token.FileSet, f *ast.File) []string { return bannedSelectors(fset, f, bannedFileFuncs) }
	randScan := func(fset *token.FileSet, f *ast.File) []string {
		return bannedImports(fset, f, []string{"math/rand", "math/rand/v2", "crypto/rand"})
	}
	rows := []row{
		{"time.Now call", timeScan, `package p; import "time"; var _ = time.Now()`, 1},
		{"time.Sleep call", timeScan, `package p; import "time"; func f() { time.Sleep(1) }`, 1},
		{"time.After call", timeScan, `package p; import "time"; var _ = time.After(1)`, 1},
		{"time.Now as a function value", timeScan, `package p; import "time"; var now = time.Now`, 1},
		{"time.Now under an alias", timeScan, `package p; import t "time"; var _ = t.Now()`, 1},
		{"time dot import", timeScan, `package p; import . "time"; var _ = Now()`, 1},
		{"time types and constants are fine", timeScan, `package p; import "time"; var _ time.Duration = time.Second; var _ time.Time`, 0},
		{"time.Time methods are fine", timeScan, `package p; import "time"; func f(a, b time.Time) time.Duration { return a.Sub(b) }`, 0},
		{"another package named time is fine", timeScan, `package p; import "example.com/time"; var _ = time.Now()`, 0},
		{"os.ReadFile call", fileScan, `package p; import "os"; var _, _ = os.ReadFile("x")`, 1},
		{"os.OpenFile call", fileScan, `package p; import "os"; var _, _ = os.OpenFile("x", 0, 0)`, 1},
		{"os.Rename call", fileScan, `package p; import "os"; var _ = os.Rename("a", "b")`, 1},
		{"os.OpenRoot call", fileScan, `package p; import "os"; var _, _ = os.OpenRoot("a")`, 1},
		{"os.Stat as a function value", fileScan, `package p; import "os"; var stat = os.Stat`, 1},
		{"os under an alias", fileScan, `package p; import o "os"; var _ = o.Remove("x")`, 1},
		{"syscall.Open call", fileScan, `package p; import "syscall"; var _, _ = syscall.Open("x", 0, 0)`, 1},
		{"syscall.CreateFile call", fileScan, `package p; import "syscall"; var _, _ = syscall.CreateFile(nil, 0, 0, nil, 0, 0, 0)`, 1},
		{"io/ioutil.ReadFile call", fileScan, `package p; import "io/ioutil"; var _, _ = ioutil.ReadFile("x")`, 1},
		{"io/ioutil.WriteFile call", fileScan, `package p; import "io/ioutil"; var _ = ioutil.WriteFile("x", nil, 0)`, 1},
		{"io/ioutil.ReadDir call", fileScan, `package p; import "io/ioutil"; var _, _ = ioutil.ReadDir("x")`, 1},
		{"io/ioutil.TempFile call", fileScan, `package p; import "io/ioutil"; var _, _ = ioutil.TempFile("", "x")`, 1},
		{"io/ioutil.TempDir call", fileScan, `package p; import "io/ioutil"; var _, _ = ioutil.TempDir("", "x")`, 1},
		{"path/filepath.Walk call", fileScan, `package p; import "path/filepath"; var _ = filepath.Walk("x", nil)`, 1},
		{"path/filepath.WalkDir call", fileScan, `package p; import "path/filepath"; var _ = filepath.WalkDir("x", nil)`, 1},
		{"path/filepath.Glob call", fileScan, `package p; import "path/filepath"; var _, _ = filepath.Glob("x")`, 1},
		{"path/filepath.EvalSymlinks call", fileScan, `package p; import "path/filepath"; var _, _ = filepath.EvalSymlinks("x")`, 1},
		{"x/sys/unix.Fsync call", fileScan, `package p; import "golang.org/x/sys/unix"; var _ = unix.Fsync(3)`, 1},
		{"x/sys/windows.CreateFile call", fileScan, `package p; import "golang.org/x/sys/windows"; var _, _ = windows.CreateFile(nil, 0, 0, nil, 0, 0, 0)`, 1},
		{"os dot import", fileScan, `package p; import . "os"; var _ = Remove`, 1},
		{"os environment and streams are fine", fileScan, `package p; import "os"; var _ = os.Getenv("x"); var _ = os.Stdout; var _ = os.ErrNotExist; func f() { os.Exit(1) }`, 0},
		{"io/ioutil reader helpers are fine", fileScan, `package p; import "io/ioutil"; var _, _ = ioutil.ReadAll(nil); var _ = ioutil.NopCloser(nil); var _ = ioutil.Discard`, 0},
		{"path/filepath path helpers are fine", fileScan, `package p; import "path/filepath"; var _ = filepath.Join("a", "b"); var _ = filepath.Base("a")`, 0},
		{"syscall signals are fine", fileScan, `package p; import "syscall"; var _ = syscall.SIGTERM`, 0},
		{"math/rand import", randScan, `package p; import "math/rand"; var _ = rand.Int()`, 1},
		{"math/rand/v2 import", randScan, `package p; import "math/rand/v2"; var _ = rand.Int()`, 1},
		{"crypto/rand import", randScan, `package p; import "crypto/rand"; var _ = rand.Reader`, 1},
		{"blank import of math/rand", randScan, `package p; import _ "math/rand"`, 1},
		{"io.Reader parameter is fine", randScan, `package p; import "io"; func f(r io.Reader) {}`, 0},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, "p.go", r.src, parser.SkipObjectResolution)
			if err != nil {
				t.Fatal(err)
			}
			if got := r.scan(fset, f); len(got) != r.want {
				t.Fatalf("got %d reports %q, want %d", len(got), got, r.want)
			}
		})
	}
}

// TestRuntimeScanWalksTree proves the directory walk: it reaches nested
// directories and platform files, honors the exact-directory exemptions, and
// skips test files and testdata.
func TestRuntimeScanWalksTree(t *testing.T) {
	root := t.TempDir()
	const violating = "package a\n\nimport \"os\"\n\nvar _ = os.Remove\n"
	files := map[string]string{
		"internal/node/a/a.go":          violating,
		"internal/node/a/a_test.go":     violating,
		"internal/node/a/a_windows.go":  "//go:build windows\n\n" + violating,
		"internal/node/fsx/x.go":        violating,
		"internal/node/fsx/sub/x.go":    violating,
		"internal/node/a/testdata/x.go": violating,
	}
	for name, src := range files {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	fileScan := func(fset *token.FileSet, f *ast.File) []string { return bannedSelectors(fset, f, bannedFileFuncs) }
	got, err := scanTree(root, filepath.Join(root, "internal", "node"), exemptDirs([]string{"internal/node/fsx"}, fileScan))
	if err != nil {
		t.Fatal(err)
	}

	reported := map[string]int{}
	for _, v := range got {
		for name := range files {
			if strings.Contains(filepath.ToSlash(v), "/"+name+":") {
				reported[name]++
			}
		}
	}
	want := map[string]int{
		"internal/node/a/a.go":         1,
		"internal/node/a/a_windows.go": 1,
		"internal/node/fsx/sub/x.go":   1,
	}
	if len(got) != len(want) || !maps.Equal(reported, want) {
		t.Fatalf("reports per file %v (all reports %q), want %v", reported, got, want)
	}
}

// --- test tooling stays out of production ---

// testkitImporters lists the fake programs that are built from the test tooling and
// never installed.
var testkitImporters = []string{
	"cmd/sidera-fake-adapter",
	"cmd/sidera-fake-runtime",
}

// isUnder reports whether dir is base or below it.
func isUnder(dir, base string) bool {
	return dir == base || strings.HasPrefix(dir, base+"/")
}

// TestTestToolingReachesNoProductionBinary fails when a non-test file outside the test
// tooling and the two fake programs imports a package below internal/node/testkit. The
// tooling builds test homes and plays the owner and the outside world; were a production
// package to import it, that would be shipped in the daemon or the command line.
func TestTestToolingReachesNoProductionBinary(t *testing.T) {
	root := moduleRoot(t)
	module := modulePath(t, root)
	violations, err := scanTree(root, root, testkitImportCheck(module))
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range violations {
		t.Error(v)
	}
}

// testkitImportCheck reports every import of a package below the test tooling in a
// directory that is neither the tooling nor one of the fake programs.
func testkitImportCheck(module string) dirCheck {
	return func(dir string, fset *token.FileSet, f *ast.File) []string {
		if isUnder(dir, testkitDir) || slices.Contains(testkitImporters, dir) {
			return nil
		}
		var out []string
		for _, spec := range f.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err == nil && isUnder(importPath, module+"/"+testkitDir) {
				out = append(out, fmt.Sprintf("%s: import of %s is not allowed here: test tooling never reaches a production binary", fset.Position(spec.Pos()), importPath))
			}
		}
		return out
	}
}

func TestTestToolingImportCheckDetectsViolations(t *testing.T) {
	const m = "example.com/m"
	check := testkitImportCheck(m)
	for _, r := range []struct {
		name string
		dir  string
		src  string
		want int
	}{
		{"a daemon package importing the harness", "internal/node/daemon", `package p; import _ "example.com/m/internal/node/testkit/harness"`, 1},
		{"the command line importing a fake", "cmd/sidera", `package p; import _ "example.com/m/internal/node/testkit/fakeadapter"`, 1},
		{"a contract package importing the ledger", "internal/admin", `package p; import "example.com/m/internal/node/testkit/ledger"; var _ = ledger.X`, 1},
		{"the test tooling importing itself", "internal/node/testkit/harness", `package p; import _ "example.com/m/internal/node/testkit/ledger"`, 0},
		{"a fake program", "cmd/sidera-fake-adapter", `package p; import _ "example.com/m/internal/node/testkit/fakeadapter"`, 0},
		{"another runtime package", "internal/node/daemon", `package p; import _ "example.com/m/internal/node/engine"`, 0},
		{"a package whose name only starts like the tooling", "internal/node/daemon", `package p; import _ "example.com/m/internal/node/testkitchen"`, 0},
	} {
		t.Run(r.name, func(t *testing.T) {
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, "p.go", r.src, parser.SkipObjectResolution)
			if err != nil {
				t.Fatal(err)
			}
			if got := check(r.dir, fset, f); len(got) != r.want {
				t.Fatalf("got %d reports %q, want %d", len(got), got, r.want)
			}
		})
	}
}

// --- who may make the local owner, and who may judge a local source ---

// The actor of the local owner may be made only by the management interface, which
// takes it from the one fact that makes someone the local owner: the connection
// came in on the management listener, whose peer ipc checked to be the user's own.
// Anywhere else it would be a way to act as the owner on the strength of nothing.
// cmd/ is scanned too: a client of the interface has no business making it.
// Test tooling below internal/node/testkit is the one exemption: it stands in for the
// owner when it builds a test home, because the write functions of config that it seeds
// the home through take the owner's request key, and TestTestToolingReachesNoProductionBinary
// keeps it out of every production binary, so no runtime path can reach what it makes.
var localOwnerAllowed = []string{"internal/node/manageapi"}

// A request is of a local source, and judged as one, only when the loopback
// transport delivers it: the engine takes what the loopback hands it and judges it,
// and no interface and no composition root makes a request of a local origin. A
// remote connection that presents this node's identifier is never one.
var localSourceAllowed = []string{"internal/node/engine", "internal/node/transport"}

func localOwnerNames(module string) map[string]map[string]bool {
	return map[string]map[string]bool{
		module + "/internal/admin":   setOf("LocalOwner", "ActorLocalOwner"),
		module + "/internal/pairing": setOf("LocalAuthor"),
	}
}

func localSourceNames(module string) map[string]map[string]bool {
	return map[string]map[string]bool{
		module + "/internal/authz": setOf("EvaluateLocal", "LocalCreate", "LocalRequest"),
	}
}

// scanRuntimeAndCmd runs check over the runtime tree and over cmd/.
func scanRuntimeAndCmd(t *testing.T, check dirCheck) {
	t.Helper()
	root := moduleRoot(t)
	for _, base := range []string{runtimeDir, "cmd"} {
		violations, err := scanTree(root, filepath.Join(root, filepath.FromSlash(base)), check)
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range violations {
			t.Error(v)
		}
	}
}

func TestTheActorOfTheLocalOwnerIsMadeOnlyByTheManagementInterface(t *testing.T) {
	module := modulePath(t, moduleRoot(t))
	scanRuntimeAndCmd(t, func(dir string, fset *token.FileSet, f *ast.File) []string {
		if slices.Contains(localOwnerAllowed, dir) || isUnder(dir, testkitDir) {
			return nil
		}
		return bannedSelectors(fset, f, localOwnerNames(module))
	})
}

func TestLocalSourcesAreJudgedOnlyWhereTheLoopbackDeliversThem(t *testing.T) {
	module := modulePath(t, moduleRoot(t))
	scanRuntimeAndCmd(t, exemptDirs(localSourceAllowed, func(fset *token.FileSet, f *ast.File) []string {
		return bannedSelectors(fset, f, localSourceNames(module))
	}))
}

// The two scans report what they forbid, under any spelling, and nothing else of
// the packages they look at.
func TestTheLocalOwnerAndLocalSourceScansDetectViolations(t *testing.T) {
	const m = "example.com/m"
	owner := func(fset *token.FileSet, f *ast.File) []string { return bannedSelectors(fset, f, localOwnerNames(m)) }
	source := func(fset *token.FileSet, f *ast.File) []string { return bannedSelectors(fset, f, localSourceNames(m)) }
	for _, r := range []struct {
		name string
		scan func(*token.FileSet, *ast.File) []string
		src  string
		want int
	}{
		{"LocalOwner call", owner, `package p; import "example.com/m/internal/admin"; var _ = admin.LocalOwner()`, 1},
		{"LocalOwner under an alias", owner, `package p; import a "example.com/m/internal/admin"; var _ = a.LocalOwner()`, 1},
		{"LocalOwner as a function value", owner, `package p; import "example.com/m/internal/admin"; var f = admin.LocalOwner`, 1},
		{"the actor kind written out", owner, `package p; import "example.com/m/internal/admin"; var _ = admin.Actor{Kind: admin.ActorLocalOwner}`, 1},
		{"the author of the pairing", owner, `package p; import "example.com/m/internal/pairing"; var _ = pairing.LocalAuthor()`, 1},
		{"a dot import of admin", owner, `package p; import . "example.com/m/internal/admin"; var _ = LocalOwner()`, 1},
		{"authorizing is fine", owner, `package p; import "example.com/m/internal/admin"; var _ = admin.Authorize; var _ admin.Actor`, 0},
		{"a remote person is fine", owner, `package p; import "example.com/m/internal/admin"; var _ = admin.RemotePerson`, 0},
		{"another package of that name", owner, `package p; import "example.org/admin"; var _ = admin.LocalOwner()`, 0},
		{"EvaluateLocal call", source, `package p; import "example.com/m/internal/authz"; var _ = authz.EvaluateLocal`, 1},
		{"LocalCreate call", source, `package p; import "example.com/m/internal/authz"; var _ = authz.LocalCreate`, 1},
		{"a local request", source, `package p; import "example.com/m/internal/authz"; var _ = authz.LocalRequest{}`, 1},
		{"Evaluate for remote sources is fine", source, `package p; import "example.com/m/internal/authz"; var _ = authz.Evaluate; var _ authz.Verdict`, 0},
	} {
		t.Run(r.name, func(t *testing.T) {
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, "p.go", r.src, parser.SkipObjectResolution)
			if err != nil {
				t.Fatal(err)
			}
			if got := r.scan(fset, f); len(got) != r.want {
				t.Fatalf("got %d reports %q, want %d", len(got), got, r.want)
			}
		})
	}
}
