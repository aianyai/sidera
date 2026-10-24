package node

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
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

// Exceptions are exact directories, relative to the module root. A package
// that sits below an exempt directory is not exempt: it is added by name.
var (
	// timeExceptions may read the real time: the clock itself and the fake
	// clock that shares its types.
	timeExceptions = []string{
		"internal/node/clock",
		"internal/node/clock/clocktest",
	}

	// fileExceptions may use the operating system's file functions directly:
	// fsx is the file-system layer; ipc creates and protects socket files,
	// which net and the operating system own; the ledger and the fixtures are
	// test oracles that must stay independent of the fsx under test.
	fileExceptions = []string{
		"internal/node/fsx",
		"internal/node/ipc",
		"internal/node/testkit/ledger",
		"internal/node/testkit/fixture",
	}

	// randomnessExceptions may import math/rand: the crash model draws its
	// clones from a *rand.Rand that the test injects.
	randomnessExceptions = []string{
		"internal/node/fsx/fsxtest",
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

// bannedFileFuncs are the file functions of package os and their counterparts
// in syscall and golang.org/x/sys. Functions that only read the process
// environment (Getenv, Exit, Getpid) and the standard streams are not here.
var bannedFileFuncs = map[string]map[string]bool{
	"os": setOf(
		"Create", "CreateTemp", "OpenFile", "Open", "ReadFile", "WriteFile",
		"Rename", "Remove", "RemoveAll", "Mkdir", "MkdirAll", "MkdirTemp",
		"ReadDir", "Stat", "Lstat", "Chmod", "Chown", "Lchown", "Chtimes",
		"Link", "Symlink", "Readlink", "Truncate", "OpenRoot", "DirFS", "CopyFS",
	),
	"syscall":               unixFileFuncs(),
	"golang.org/x/sys/unix": unixFileFuncs(),
	"golang.org/x/sys/windows": setOf(
		"CreateFile", "CreateFile2", "DeleteFile", "MoveFile", "MoveFileEx",
		"ReplaceFile", "CopyFile", "CreateDirectory", "RemoveDirectory",
		"FlushFileBuffers", "WriteFile", "ReadFile", "SetEndOfFile",
		"SetFilePointer", "SetFileAttributes", "GetFileAttributes",
		"GetFileAttributesEx", "FindFirstFile", "LockFileEx", "UnlockFileEx",
		"CreateHardLink", "CreateSymbolicLink", "Open", "Stat", "Mkdir",
		"Rmdir", "Unlink", "Rename", "Fsync", "Ftruncate", "Chmod", "Symlink",
		"Link",
	),
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
		for _, tags := range []string{"", "crashtest,testclock"} {
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

func scanRuntime(t *testing.T, exceptions []string, check func(*token.FileSet, *ast.File) []string) {
	t.Helper()
	scanRuntimeDirs(t, func(dir string, fset *token.FileSet, f *ast.File) []string {
		if slices.Contains(exceptions, dir) {
			return nil
		}
		return check(fset, f)
	})
}

// scanRuntimeDirs parses every non-test Go file below internal/node, whatever
// its build constraints, and reports what check returns for it. dir is the
// file's directory relative to the module root, with forward slashes.
func scanRuntimeDirs(t *testing.T, check func(dir string, fset *token.FileSet, f *ast.File) []string) {
	t.Helper()
	root := moduleRoot(t)
	base := filepath.Join(root, filepath.FromSlash(runtimeDir))
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
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range violations {
		t.Error(v)
	}
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
		{"x/sys/unix.Fsync call", fileScan, `package p; import "golang.org/x/sys/unix"; var _ = unix.Fsync(3)`, 1},
		{"x/sys/windows.CreateFile call", fileScan, `package p; import "golang.org/x/sys/windows"; var _, _ = windows.CreateFile(nil, 0, 0, nil, 0, 0, 0)`, 1},
		{"os dot import", fileScan, `package p; import . "os"; var _ = Remove`, 1},
		{"os environment and streams are fine", fileScan, `package p; import "os"; var _ = os.Getenv("x"); var _ = os.Stdout; var _ = os.ErrNotExist; func f() { os.Exit(1) }`, 0},
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
