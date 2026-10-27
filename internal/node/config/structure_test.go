package config_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// config writes only through the transactions of txn: it holds no writable file
// system. Of fsx it uses the read side, its errors and the types a write call
// needs, and nothing that creates, replaces, renames or removes a file. The scan reads the
// source of the package, so a write that slipped in through any path fails here
// and not only when a crash test happens to walk it.
func TestConfigHoldsNoWritableFileSystem(t *testing.T) {
	allowed := map[string]bool{
		"Reader": true, "Exists": true, "Mode": true, "ModePrivate": true, "ModePrivateExec": true,
		"Entry": true, "Info": true, "IsLeftover": true, "TempPrefix": true, "TrashPrefix": true,
		"ErrTooLarge": true,
	}
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("no source files: %v", err)
	}
	fset := token.NewFileSet()
	checked := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		checked++
		fsxName := ""
		for _, imp := range f.Imports {
			if imp.Path.Value == `"github.com/aianyai/sidera/internal/node/fsx"` {
				fsxName = "fsx"
				if imp.Name != nil {
					fsxName = imp.Name.Name
				}
			}
			if v := imp.Path.Value; v == `"os"` || v == `"io/ioutil"` || v == `"path/filepath"` {
				t.Errorf("%s imports %s: files are reached only through fsx", name, v)
			}
		}
		if fsxName == "" {
			continue
		}
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == fsxName && !allowed[sel.Sel.Name] {
				t.Errorf("%s: %s uses fsx.%s: config writes only through txn", fset.Position(sel.Pos()), name, sel.Sel.Name)
			}
			return true
		})
	}
	if checked == 0 {
		t.Fatal("the scan found no non-test file")
	}
}
