package markdown

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

// markdownDir is the directory the guards below scan: this package's own.
const markdownDir = "."

// allowedImports maps every import path this package may use from outside the
// standard library to the one file that may import it, or to the empty string
// when any file may.
//
// The two format modules are confined to a file each so that the seam stays a
// seam: a decoder's types must not spread into the parser or the serializer, and
// replacing one of them must not mean auditing the package. A provider SDK, a
// second YAML library or a stray HTTP client fails here rather than in review.
var allowedImports = map[string]string{
	"github.com/shutx-net/micawber/core": "",
	"go.yaml.in/yaml/v3":                 "yaml.go",
	"github.com/pelletier/go-toml/v2":    "toml.go",
}

// markdownFiles returns the non-test Go files of this package. It fails the test
// if there are none, so neither guard below can pass vacuously.
func markdownFiles(t *testing.T) []string {
	t.Helper()

	entries, err := os.ReadDir(markdownDir)
	if err != nil {
		t.Fatalf("read package directory: %v", err)
	}

	var names []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		names = append(names, filepath.Join(markdownDir, name))
	}
	if len(names) == 0 {
		t.Fatalf("no non-test Go files in the package directory; the guard would pass vacuously")
	}
	return names
}

// TestMarkdownImportsAreAllowlisted keeps this package's dependencies to the
// standard library, the core, and the two format modules it was given -- and
// each of those modules to the single file that owns it.
func TestMarkdownImportsAreAllowlisted(t *testing.T) {
	fset := token.NewFileSet()

	for _, name := range markdownFiles(t) {
		file, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}

		for _, spec := range file.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatalf("%s: unquote import %s: %v", name, spec.Path.Value, err)
			}

			if pkg, err := build.Import(path, markdownDir, build.FindOnly); err == nil && pkg.Goroot {
				continue
			}

			owner, allowed := allowedImports[path]
			if !allowed {
				t.Errorf("%s imports %q, which is not on the allowlist; this package depends on the standard library, the core, and its two format modules only", name, path)
				continue
			}
			if owner != "" && filepath.Base(name) != owner {
				t.Errorf("%s imports %q, which belongs to %s alone; keep the format library behind the codec seam", name, path, owner)
			}
		}
	}
}

// TestMarkdownExportedDeclarationsAreDocumented mirrors the core's guard: the doc
// comments on this package's exported surface are where its byte-level contract
// is written down, so an undocumented declaration is a missing specification.
func TestMarkdownExportedDeclarationsAreDocumented(t *testing.T) {
	fset := token.NewFileSet()

	var files []*ast.File
	for _, name := range markdownFiles(t) {
		file, err := parser.ParseFile(fset, name, nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		files = append(files, file)
	}

	pkg, err := doc.NewFromFiles(fset, files, "github.com/shutx-net/micawber/markdown")
	if err != nil {
		t.Fatalf("build documentation: %v", err)
	}
	if pkg.Doc == "" {
		t.Errorf("package markdown has no package comment")
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
