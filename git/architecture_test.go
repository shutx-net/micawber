package git

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

// gitDir is the directory the guards below scan: this package's own.
const gitDir = "."

// execFile is the one file allowed to import os/exec. Micawber's whole trust in
// an external program lives there, so an auditor reads one file to check every
// argument vector, and a future library-backed adapter replaces one file rather
// than a package.
const execFile = "exec.go"

// allowedImports maps every import path this package may use from outside the
// standard library to the one file that may import it, or to the empty string
// when any file may.
//
// It is deliberately short. This adapter adds no Go module at all: it drives the
// git binary through os/exec, so a go-git, a libgit2 binding or a provider SDK
// appearing here fails the build's conscience before it reaches review.
var allowedImports = map[string]string{
	"github.com/shutx-net/micawber/core":     "",
	"github.com/shutx-net/micawber/markdown": "",
}

// gitFiles returns the non-test Go files of this package. It fails the test if
// there are none, so none of the guards below can pass vacuously.
func gitFiles(t *testing.T) []string {
	t.Helper()

	entries, err := os.ReadDir(gitDir)
	if err != nil {
		t.Fatalf("read package directory: %v", err)
	}

	var names []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		names = append(names, filepath.Join(gitDir, name))
	}
	if len(names) == 0 {
		t.Fatalf("no non-test Go files in the package directory; the guard would pass vacuously")
	}
	return names
}

// packageImports returns every import of every non-test file, keyed by the file
// it appeared in.
func packageImports(t *testing.T) map[string][]string {
	t.Helper()

	fset := token.NewFileSet()
	imports := make(map[string][]string)

	for _, name := range gitFiles(t) {
		file, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, spec := range file.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatalf("%s: unquote import %s: %v", name, spec.Path.Value, err)
			}
			imports[name] = append(imports[name], path)
		}
	}
	return imports
}

// TestGitImportsAreAllowlisted keeps this adapter's dependencies to the standard
// library, the core and the markdown package. Adding a Git library is the change
// this guard exists to catch: the whole argument for driving the git binary is
// that it costs no module, so a module appearing here means the argument was
// abandoned rather than revisited.
func TestGitImportsAreAllowlisted(t *testing.T) {
	for name, paths := range packageImports(t) {
		for _, path := range paths {
			if pkg, err := build.Import(path, gitDir, build.FindOnly); err == nil && pkg.Goroot {
				continue
			}

			owner, allowed := allowedImports[path]
			if !allowed {
				t.Errorf("%s imports %q, which is not on the allowlist; this adapter depends on the standard library, the core and the markdown package only", name, path)
				continue
			}
			if owner != "" && filepath.Base(name) != owner {
				t.Errorf("%s imports %q, which belongs to %s alone", name, path, owner)
			}
		}
	}
}

// TestOSExecIsConfinedToTheRunner is the guard that matters most here.
//
// os/exec is standard library, so the allowlist above waves it through; this
// check runs before that short-circuit on purpose. The subprocess boundary is
// this package's seam, in the same way the two format libraries are markdown's,
// and confining it to one file is what keeps the seam a seam.
func TestOSExecIsConfinedToTheRunner(t *testing.T) {
	found := false

	for name, paths := range packageImports(t) {
		for _, path := range paths {
			if path != "os/exec" {
				continue
			}
			found = true
			if filepath.Base(name) != execFile {
				t.Errorf("%s imports %q, which belongs to %s alone; every git invocation goes through the runner", name, path, execFile)
			}
		}
	}

	if !found {
		t.Fatalf("no file imports %q; either the runner has gone or this guard has stopped scanning the package", "os/exec")
	}
}

// TestGitExportedDeclarationsAreDocumented mirrors the guards in core and
// markdown: the doc comments on this package's exported surface are where its
// contract with a caller is written down, so an undocumented declaration is a
// missing specification.
func TestGitExportedDeclarationsAreDocumented(t *testing.T) {
	fset := token.NewFileSet()

	var files []*ast.File
	for _, name := range gitFiles(t) {
		file, err := parser.ParseFile(fset, name, nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		files = append(files, file)
	}

	pkg, err := doc.NewFromFiles(fset, files, "github.com/shutx-net/micawber/git")
	if err != nil {
		t.Fatalf("build documentation: %v", err)
	}
	if pkg.Doc == "" {
		t.Errorf("package git has no package comment")
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

// TestCoreErrUnsupportedIsNotUsed keeps a promise this package can actually
// keep.
//
// core.ErrUnsupported means a backend cannot perform an operation at all, and
// there is nothing in core.ContentRepository or core.ContentHistory that a Git
// backend cannot do. Reaching for it here would almost always mean an operation
// had been left unfinished, which is exactly what it looked like while this
// package was being written.
func TestCoreErrUnsupportedIsNotUsed(t *testing.T) {
	fset := token.NewFileSet()

	for _, name := range gitFiles(t) {
		// Parsed rather than searched: the package documentation says in prose
		// that this sentinel is not used, and a guard that could not tell code
		// from a comment would fail on the sentence describing it.
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			selector, ok := n.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "ErrUnsupported" {
				return true
			}
			if pkg, ok := selector.X.(*ast.Ident); ok && pkg.Name == "core" {
				t.Errorf("%s uses core.ErrUnsupported; every operation the two interfaces define, a Git backend can do", name)
			}
			return true
		})
	}
}
