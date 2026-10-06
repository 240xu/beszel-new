//go:build testing

package plus

// Platform scope for this package's test files.
//
// Two rules, both derived from a break that cost a round: a test file with no OS
// constraint that touches a Linux-only production symbol compiles and passes on
// this host, then fails `GOOS=windows go vet -tags testing` on three other
// platforms. Nothing in the local suite would ever have reported it.
//
// The Linux-only symbol set is derived from the production files' own build
// constraints rather than listed here. A list would be data, and data can be
// edited -- which is the exact failure mode fork_policy_test.go exists to rule
// out. Deriving it means a new Linux-only production file is covered the moment
// it lands, with nothing to remember.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func filesWithSuffix(suffix string) []string {
	entries, err := os.ReadDir(".")
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), suffix) {
			names = append(names, e.Name())
		}
	}
	return names
}

// buildConstraint returns a file's //go:build expression, or "" when it has none.
func buildConstraint(t *testing.T, name string) string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, name, nil, parser.PackageClauseOnly|parser.ParseComments)
	require.NoErrorf(t, err, "cannot read the build constraint of %s", name)
	for _, group := range file.Comments {
		if group.Pos() > file.Package {
			break
		}
		for _, c := range group.List {
			if text, ok := strings.CutPrefix(c.Text, "//go:build "); ok {
				return strings.TrimSpace(text)
			}
		}
	}
	return ""
}

func isLinuxOnlyConstraint(c string) bool {
	return strings.Contains(c, "linux") && !strings.Contains(c, "!linux")
}

// topLevelNames collects what a file declares at package scope, so the guard below
// has the same view of the package the compiler has.
func topLevelNames(t *testing.T, name string) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
	require.NoErrorf(t, err, "cannot parse %s", name)
	var names []string
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Recv == nil {
				names = append(names, d.Name.Name)
			}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					names = append(names, s.Name.Name)
				case *ast.ValueSpec:
					for _, n := range s.Names {
						names = append(names, n.Name)
					}
				}
			}
		}
	}
	return names
}

// linuxOnlySymbols is the set of names the compiler only builds on Linux. A name
// declared on both sides -- collectWiFi, which has a real !linux stub -- is not in
// it, because a portable test file may call that one.
func linuxOnlySymbols(t *testing.T) map[string]bool {
	t.Helper()
	linux, everywhere := map[string]bool{}, map[string]bool{}
	for _, name := range filesWithSuffix(".go") {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		constraint := buildConstraint(t, name)
		names := topLevelNames(t, name)
		if isLinuxOnlyConstraint(constraint) {
			for _, n := range names {
				linux[n] = true
			}
		}
		if strings.Contains(constraint, "!linux") {
			for _, n := range names {
				everywhere[n] = true
			}
		}
	}
	for n := range linux {
		if everywhere[n] {
			delete(linux, n)
		}
	}
	return linux
}

func identifiersIn(t *testing.T, name string) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
	require.NoErrorf(t, err, "cannot parse %s", name)
	seen := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok {
			seen[id.Name] = true
		}
		return true
	})
	return seen
}

// TestLinuxOnlySymbolsStayInLinuxConstrainedTests is the D-B guard. It fails if a
// test file that any platform can build references something only Linux builds,
// which is the shape of the break that shipped once already.
func TestLinuxOnlySymbolsStayInLinuxConstrainedTests(t *testing.T) {
	linuxOnly := linuxOnlySymbols(t)
	require.NotEmpty(t, linuxOnly,
		"no Linux-only production symbol found -- the guard would prove nothing")

	testFiles := filesWithSuffix("_test.go")
	require.NotEmpty(t, testFiles)
	for _, name := range testFiles {
		if isLinuxOnlyConstraint(buildConstraint(t, name)) {
			continue
		}
		seen := identifiersIn(t, name)
		for symbol := range linuxOnly {
			assert.Falsef(t, seen[symbol],
				"%s is a portable test file but references %s, which only //go:build linux "+
					"declares. Add `&& linux` to its build constraint: otherwise `GOOS=windows "+
					"go vet -tags testing ./agent/plus/` fails on a file that passes every "+
					"test here", name, symbol)
		}
	}
}

// TestEveryTestFileCarriesTheTestingTag covers the quieter version of the same
// hazard: `go test -tags testing` skips a test file that lost its constraint, so
// the suite would report green while running fewer tests than it claims.
func TestEveryTestFileCarriesTheTestingTag(t *testing.T) {
	for _, name := range filesWithSuffix("_test.go") {
		assert.Containsf(t, buildConstraint(t, name), "testing",
			"%s has no testing constraint, so `go test -tags testing` silently skips it", name)
	}
}
