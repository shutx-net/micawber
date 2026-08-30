package localfs

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

// localfsDir is the directory the guards below scan: this package's own.
const localfsDir = "."

// rootFile is the one file allowed to import os, io/fs, path/filepath or
// syscall. Every dangerous operation this package performs is a filesystem
// call, so an auditor reads one file to check them all, and an S3 store or a
// fake replaces one file rather than a package.
const rootFile = "root.go"

// confinedImports are the standard-library packages that reach the filesystem.
// They are standard library, so the allowlist below waves them through; the
// confinement guard is what actually polices them.
var confinedImports = []string{"os", "io/fs", "path/filepath", "syscall"}

// allowedImports maps every import path this package may use from outside the
// standard library to the one file that may import it, or to the empty string
// when any file may.
//
// It is shorter than the git adapter's, which also allows markdown: a store
// holds bytes and never parses a document, so core is the whole of it. This
// package adds no Go module at all, so any module appearing here fails the
// build before it reaches review.
var allowedImports = map[string]string{
	"github.com/shutx-net/micawber/core": "",
}

// localfsFiles returns the non-test Go files of this package. It fails the test
// if there are none, so none of the guards below can pass vacuously.
func localfsFiles(t *testing.T) []string {
	t.Helper()

	entries, err := os.ReadDir(localfsDir)
	if err != nil {
		t.Fatalf("read package directory: %v", err)
	}

	var names []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		names = append(names, filepath.Join(localfsDir, name))
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

	for _, name := range localfsFiles(t) {
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

// packageFiles parses every non-test file of this package for inspection.
func packageFiles(t *testing.T, mode parser.Mode) (*token.FileSet, map[string]*ast.File) {
	t.Helper()

	fset := token.NewFileSet()
	files := make(map[string]*ast.File)
	for _, name := range localfsFiles(t) {
		file, err := parser.ParseFile(fset, name, nil, mode)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		files[name] = file
	}
	return fset, files
}

// TestLocalfsImportsAreAllowlisted keeps this adapter's dependencies to the
// standard library and the core.
//
// The three things a filesystem store might reach for a module for -- extended
// attributes, content-type sniffing and a filesystem abstraction -- were each
// priced and each rejected, so a module appearing here means one of those
// arguments was abandoned rather than revisited.
func TestLocalfsImportsAreAllowlisted(t *testing.T) {
	for name, paths := range packageImports(t) {
		for _, path := range paths {
			if pkg, err := build.Import(path, localfsDir, build.FindOnly); err == nil && pkg.Goroot {
				continue
			}

			owner, allowed := allowedImports[path]
			if !allowed {
				t.Errorf("%s imports %q, which is not on the allowlist; this store depends on the standard library and the core only", name, path)
				continue
			}
			if owner != "" && filepath.Base(name) != owner {
				t.Errorf("%s imports %q, which belongs to %s alone", name, path, owner)
			}
		}
	}
}

// TestFilesystemAccessIsConfinedToTheRoot is the guard that matters most here,
// and it is this package's equivalent of the git adapter's
// TestOSExecIsConfinedToTheRunner.
//
// os, io/fs, path/filepath and syscall are standard library, so the allowlist
// above waves them through; this check runs without that short-circuit on
// purpose. Copying the allowlist loop verbatim would make this guard skip every
// import it exists to police.
func TestFilesystemAccessIsConfinedToTheRoot(t *testing.T) {
	confined := make(map[string]bool, len(confinedImports))
	for _, path := range confinedImports {
		confined[path] = true
	}

	sawOS := false
	for name, paths := range packageImports(t) {
		for _, path := range paths {
			if !confined[path] {
				continue
			}
			if path == "os" {
				sawOS = true
			}
			if filepath.Base(name) != rootFile {
				t.Errorf("%s imports %q, which belongs to %s alone; every path operation goes through the one *os.Root", name, path, rootFile)
			}
		}
	}

	if !sawOS {
		t.Fatalf("no file imports %q; either the filesystem seam has gone or this guard has stopped scanning the package", "os")
	}
}

// TestMimeTypeByExtensionIsNotUsed encodes a measured finding as something the
// build checks rather than something a reviewer has to remember.
//
// mime.TypeByExtension augments its built-in table at init from /etc/mime.types
// and the XDG databases, so its answer is a property of the host rather than of
// the key: on this machine ".otf" resolves to an OpenDocument formula template.
// A store that reports a different content type on the deployment host than on
// the developer's laptop is not portable, so the table this package ships is
// fixed. mime.ParseMediaType is allowed, and is used to validate.
func TestMimeTypeByExtensionIsNotUsed(t *testing.T) {
	// Parsed rather than searched: the package documentation says in prose that
	// this function is not used, and a guard that could not tell code from a
	// comment would fail on the sentence describing the rule.
	_, files := packageFiles(t, 0)

	for name, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			selector, ok := n.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "TypeByExtension" {
				return true
			}
			if pkg, ok := selector.X.(*ast.Ident); ok && pkg.Name == "mime" {
				t.Errorf("%s uses mime.TypeByExtension, whose answer varies with the host; this package ships its own table", name)
			}
			return true
		})
	}
}

// TestCoreErrUnsupportedIsNotUsed keeps a promise this package can actually
// keep.
//
// core.ErrUnsupported means a backend cannot perform an operation at all, and
// there is nothing in core.AssetStore a directory cannot do. Reaching for it
// here would mean an operation had been left unfinished.
func TestCoreErrUnsupportedIsNotUsed(t *testing.T) {
	_, files := packageFiles(t, 0)

	for name, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			selector, ok := n.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "ErrUnsupported" {
				return true
			}
			if pkg, ok := selector.X.(*ast.Ident); ok && pkg.Name == "core" {
				t.Errorf("%s uses core.ErrUnsupported; every operation core.AssetStore defines, a directory can do", name)
			}
			return true
		})
	}
}

// TestLocalfsExportedDeclarationsAreDocumented mirrors the guards in core,
// markdown and git: the doc comments on this package's exported surface are
// where its contract with a caller is written down, so an undocumented
// declaration is a missing specification.
func TestLocalfsExportedDeclarationsAreDocumented(t *testing.T) {
	fset, parsed := packageFiles(t, parser.ParseComments)

	files := make([]*ast.File, 0, len(parsed))
	for _, file := range parsed {
		files = append(files, file)
	}

	pkg, err := doc.NewFromFiles(fset, files, "github.com/shutx-net/micawber/localfs")
	if err != nil {
		t.Fatalf("build documentation: %v", err)
	}
	if pkg.Doc == "" {
		t.Errorf("package localfs has no package comment")
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
