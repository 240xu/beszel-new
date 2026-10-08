//go:build testing

package plus

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// nativeFanShape is beszel's own Stats.Fans type. Anything an exporter hands back or
// accepts in that shape reaches the hub with no way to say it is not a tachometer
// reading.
const nativeFanShape = "map[string]uint16"

// TestNoExportedNativeFanProjection pins that this package offers no way to reach
// the native Fans field.
//
// An export existed once, returning {"cpu_fan_1": <estimate>}, and it was removed
// rather than wired up: the shape is the whole problem. internal/records averages
// that map into the hub's permanent f column, and nothing downstream can tell the
// value from a fan_speed_rpm reading. The estimate lives on FanStats instead, which
// carries rpm_est_is_estimate and sample_ts_s on every sample.
//
// The scan runs over parsed declarations, not over text, so renaming or reformatting
// cannot hide one. It checks everything package agent could actually call:
//
//   - exported functions, which it reaches as plus.X;
//   - exported methods on an exported receiver type, which it reaches the same way.
//     These were skipped by an earlier version of this guard on the grounds that
//     "unexported helpers cannot be called from package agent", which is true of an
//     unexported function and false of an exported method on an exported type: agent
//     names the type all the time, plus.Collect is called from system.go.
//   - parameters as well as results, since a projection handed to a caller is as
//     launderable as one returned to it;
//   - struct fields of package-local named types, since plus.T{}.Fans reaches agent
//     exactly as plainly as a function result does;
//   - the shape through this package's own named types, so
//     `type NativeFanMap = map[string]uint16` and
//     `type NativeFanMap map[string]uint16` are both caught. Comparing the written
//     text against the literal, as an earlier version did, matched neither.
//   - through one layer of indirection, so a pointer, a slice or an address-of is
//     peeled first. A projection is exactly as launderable when it arrives one layer
//     down.
//
// Unexported functions and methods on unexported types are still allowed: package
// agent cannot name them, so it cannot reach them.
//
// The matching guard on the agent side is TestNativeFansIsNeverWrittenFromPfanel,
// so neither file can enforce this alone.
func TestNoExportedNativeFanProjection(t *testing.T) {
	paths, err := filepath.Glob("*.go")
	require.NoError(t, err)

	// Declared names are collected across every file before any file is checked,
	// because a named type may be declared after the function that returns it.
	named := namedTypeShapes(paths, t)

	parsed := 0
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, p, nil, 0)
		require.NoErrorf(t, err, "cannot parse %s", p)
		parsed++

		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || !callableFromAgent(fn) {
				continue
			}
			where := fset.Position(fn.Pos())
			for _, r := range append(renderedTypes(fn.Type.Results), renderedTypes(fn.Type.Params)...) {
				assert.NotEqualf(t, nativeFanShape, resolveNamed(r, named),
					"%s: %s handles the native Fans shape, which cannot carry "+
						"rpm_est_is_estimate", where, fn.Name.Name)
			}
		}
		for _, spec := range typeSpecs(f) {
			st, ok := spec.Type.(*ast.StructType)
			if !ok {
				continue
			}
			for _, field := range st.Fields.List {
				for _, r := range renderedTypes(&ast.FieldList{List: []*ast.Field{field}}) {
					assert.NotEqualf(t, nativeFanShape, resolveNamed(r, named),
						"%s: %s.%s is the native Fans shape, which cannot carry "+
							"rpm_est_is_estimate", fset.Position(spec.Pos()), spec.Name.Name, fieldName(field))
				}
			}
		}
	}
	require.NotZero(t, parsed, "the scan must have read the package's own sources")
}

// callableFromAgent reports whether package agent can reach this declaration.
//
// Reachability, not visibility, is the question: an exported method on an unexported
// type is as unreachable as an unexported function, because agent cannot name the
// type and so cannot produce a receiver value to call it on.
func callableFromAgent(fn *ast.FuncDecl) bool {
	if !fn.Name.IsExported() {
		return false
	}
	if fn.Recv == nil {
		return true
	}
	for _, f := range fn.Recv.List {
		t := f.Type
		if star, ok := t.(*ast.StarExpr); ok {
			t = star.X
		}
		// A generic receiver is written T[P]; the reachability question is about T.
		if idx, ok := t.(*ast.IndexExpr); ok {
			t = idx.X
		} else if idx, ok := t.(*ast.IndexListExpr); ok {
			t = idx.X
		}
		id, ok := t.(*ast.Ident)
		return ok && id.IsExported()
	}
	return false
}

// namedTypeShapes maps this package's declared type names to the written type beneath
// them, so a projection can be named rather than spelled and still be caught. An
// alias and a defined type produce the same entry, which is the point: both name the
// native shape.
func namedTypeShapes(paths []string, t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, p, nil, 0)
		require.NoErrorf(t, err, "cannot parse %s", p)
		for _, spec := range typeSpecs(f) {
			out[spec.Name.Name] = types.ExprString(spec.Type)
		}
	}
	return out
}

// typeSpecs returns every type specification the file declares, across all of its
// type declaration blocks.
//
// It has to walk all of them, not just the first. go/parser starts a new declaration
// block at a blank line, so a file that writes one `type` per line separated by
// blank lines -- which is how plus.go is written -- has one block per type. Reading
// only the first block leaves every later type unregistered, which silently
// disables name resolution for all of them: a projection spelled
// `type NativeFanMap InnerFanMap` then looks like an unknown name rather than the
// native shape, and the guard passes it.
func typeSpecs(f *ast.File) []*ast.TypeSpec {
	var out []*ast.TypeSpec
	for _, decl := range f.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.TYPE {
			continue
		}
		for _, spec := range gen.Specs {
			if ts, ok := spec.(*ast.TypeSpec); ok {
				out = append(out, ts)
			}
		}
	}
	return out
}

// resolveNamed reduces a written type to the shape it ultimately has, following this
// package's own declared names. The walk is bounded because a declaration cycle would
// otherwise hang the test rather than fail it.
func resolveNamed(rendered string, named map[string]string) string {
	for range 8 {
		s := stripWrappers(rendered)
		def, ok := named[s]
		if !ok {
			return s
		}
		rendered = def
	}
	return stripWrappers(rendered)
}

// stripWrappers peels the indirections a projection can hide behind -- a pointer, an
// address-of, a slice -- because a map handed over one layer down is as launderable as
// one handed over bare.
func stripWrappers(s string) string {
	for {
		switch {
		case strings.HasPrefix(s, "[]"):
			// Two characters, so it cannot share the single-character arm below.
			s = s[2:]
		case strings.HasPrefix(s, "*"), strings.HasPrefix(s, "&"):
			s = s[1:]
		default:
			return s
		}
	}
}

// renderedTypes flattens a field list to the exact written text of each type, so the
// comparison above is against what is on the page rather than against a normalised
// form nothing else in the package computes.
func renderedTypes(fl *ast.FieldList) []string {
	if fl == nil {
		return nil
	}
	var out []string
	for _, f := range fl.List {
		out = append(out, types.ExprString(f.Type))
	}
	return out
}

// fieldName names a struct field for a failure message, falling back to a placeholder
// for the embedded and multi-name forms, which cannot occur here but should not
// produce an empty label if one ever did.
func fieldName(f *ast.Field) string {
	if len(f.Names) > 0 {
		return f.Names[0].Name
	}
	return "<embedded>"
}
