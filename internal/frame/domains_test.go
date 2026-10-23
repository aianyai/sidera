package frame

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// There is no registry of domains: each package declares its own with
// frame.Domain("..."). This test is the single list. It parses every Go file of
// the module (tests included), finds each call of frame.Domain, and requires
// that the argument is a string literal and that no literal appears twice, so
// two purposes can never share a domain.

type domainUse struct {
	domain string
	where  string
}

// domainCalls returns the domains passed to frame.Domain in src and the
// positions of calls whose argument is not a string literal.
func domainCalls(t testing.TB, filename string, src any) (uses []domainUse, nonLiteral []string) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, 0)
	if err != nil {
		t.Fatal(err)
	}
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Domain" {
			return true
		}
		if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "frame" {
			return true
		}
		where := fset.Position(call.Pos()).String()
		if len(call.Args) != 1 {
			nonLiteral = append(nonLiteral, where)
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			nonLiteral = append(nonLiteral, where)
			return true
		}
		s, err := strconv.Unquote(lit.Value)
		if err != nil {
			t.Fatal(err)
		}
		uses = append(uses, domainUse{s, where})
		return true
	})
	return uses, nonLiteral
}

func checkDomains(uses []domainUse, nonLiteral []string) []string {
	var problems []string
	for _, w := range nonLiteral {
		problems = append(problems, w+": the argument of frame.Domain must be a string literal")
	}
	first := map[string]string{}
	for _, u := range uses {
		if err := CheckDomain(u.domain); err != nil {
			problems = append(problems, u.where+": "+err.Error())
		}
		if prev, dup := first[u.domain]; dup {
			problems = append(problems, u.where+": domain "+strconv.Quote(u.domain)+" is already used at "+prev)
			continue
		}
		first[u.domain] = u.where
	}
	return problems
}

func moduleRoot(t testing.TB) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the test directory")
		}
		dir = parent
	}
}

func TestEveryDomainInTheModuleIsValidAndDistinct(t *testing.T) {
	root := moduleRoot(t)
	var uses []domainUse
	var nonLiteral []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == ".worktrees" || d.Name() == "node_modules") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		u, n := domainCalls(t, path, nil)
		uses = append(uses, u...)
		nonLiteral = append(nonLiteral, n...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if problems := checkDomains(uses, nonLiteral); len(problems) > 0 {
		sort.Strings(problems)
		t.Fatalf("domain problems:\n%s", strings.Join(problems, "\n"))
	}
	t.Logf("%d domains in the module", len(uses))
}

// The scan must notice what it exists to notice.
func TestDomainScanFindsViolations(t *testing.T) {
	src := `package p
import "example/frame"
var (
	a = frame.Domain("sidera/a/1")
	b = frame.Domain("sidera/a/1")
	c = frame.Domain("other/c/1")
	d = frame.Domain(name)
	e = frame.Domain("sidera/e/1")
	f = unrelated.Domain("sidera/a/1")
)`
	uses, nonLiteral := domainCalls(t, "p.go", src)
	if len(uses) != 4 || len(nonLiteral) != 1 {
		t.Fatalf("found %d literal domains and %d non-literal calls, want 4 and 1", len(uses), len(nonLiteral))
	}
	problems := checkDomains(uses, nonLiteral)
	want := []string{"must be a string literal", "is already used", "does not start with"}
	if len(problems) != len(want) {
		t.Fatalf("problems = %q", problems)
	}
	for _, w := range want {
		found := false
		for _, p := range problems {
			found = found || strings.Contains(p, w)
		}
		if !found {
			t.Fatalf("no problem mentions %q: %q", w, problems)
		}
	}
	if problems := checkDomains(uses[:1], nil); len(problems) != 0 {
		t.Fatalf("a valid domain was reported: %q", problems)
	}
}
