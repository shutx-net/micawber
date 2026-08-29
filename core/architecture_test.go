package core

import (
	"go/ast"
	"go/build"
	"go/doc"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// coreDir is the directory the guards below scan: the core package's own.
const coreDir = "."

// coreFiles returns the non-test Go files of this package. It fails the test
// if there are none, so neither guard below can pass vacuously.
func coreFiles(t *testing.T) []string {
	t.Helper()

	entries, err := os.ReadDir(coreDir)
	if err != nil {
		t.Fatalf("read package directory: %v", err)
	}

	var names []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		names = append(names, filepath.Join(coreDir, name))
	}
	if len(names) == 0 {
		t.Fatalf("no non-test Go files in the package directory; the guard would pass vacuously")
	}
	return names
}

// TestCoreImportsOnlyStandardLibrary is the compiler-adjacent enforcement of
// the rule that dependencies point toward the core: no provider SDK, and no
// module dependency at all, may be imported here.
func TestCoreImportsOnlyStandardLibrary(t *testing.T) {
	fset := token.NewFileSet()

	for _, name := range coreFiles(t) {
		file, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}

		for _, spec := range file.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatalf("%s: unquote import %s: %v", name, spec.Path.Value, err)
			}

			if first, _, _ := strings.Cut(path, "/"); strings.Contains(first, ".") {
				t.Errorf("%s imports %q: a dotted first segment is a domain, so this is a module dependency; the core imports only the standard library", name, path)
				continue
			}

			pkg, err := build.Import(path, coreDir, build.FindOnly)
			if err != nil {
				t.Errorf("%s imports %q, which does not resolve: %v", name, path, err)
				continue
			}
			if !pkg.Goroot {
				t.Errorf("%s imports %q, which resolves to %s, outside GOROOT; the core imports only the standard library", name, path, pkg.Dir)
			}
		}
	}
}

// TestExportedDeclarationsAreDocumented keeps the package's own documentation
// honest: in this phase the doc comments on the interfaces are the contract,
// so an undocumented exported declaration is a missing specification.
func TestExportedDeclarationsAreDocumented(t *testing.T) {
	fset := token.NewFileSet()

	var files []*ast.File
	for _, name := range coreFiles(t) {
		file, err := parser.ParseFile(fset, name, nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		files = append(files, file)
	}

	pkg, err := doc.NewFromFiles(fset, files, "github.com/shutx-net/micawber/core")
	if err != nil {
		t.Fatalf("build documentation: %v", err)
	}
	if pkg.Doc == "" {
		t.Errorf("package core has no package comment")
	}

	report := func(kind, name, docText string) {
		t.Helper()
		if strings.TrimSpace(docText) == "" {
			t.Errorf("exported %s %s has no doc comment", kind, name)
		}
	}
	reportValues := func(kind string, values []*doc.Value) {
		t.Helper()
		for _, value := range values {
			report(kind, strings.Join(value.Names, ", "), value.Doc)
		}
	}
	reportFuncs := func(kind string, funcs []*doc.Func) {
		t.Helper()
		for _, fn := range funcs {
			name := fn.Name
			if fn.Recv != "" {
				name = fn.Recv + "." + fn.Name
			}
			report(kind, name, fn.Doc)
		}
	}

	reportValues("const", pkg.Consts)
	reportValues("var", pkg.Vars)
	reportFuncs("func", pkg.Funcs)

	if len(pkg.Types) == 0 {
		t.Fatalf("no exported types found; the guard would pass vacuously")
	}
	for _, typ := range pkg.Types {
		report("type", typ.Name, typ.Doc)
		reportValues("const", typ.Consts)
		reportValues("var", typ.Vars)
		reportFuncs("func", typ.Funcs)
		reportFuncs("method", typ.Methods)
	}
}
