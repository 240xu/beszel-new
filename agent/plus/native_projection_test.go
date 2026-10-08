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

// nativeFanShape is beszel's own Stats.Fans type. Anything an exporter returns in
// that shape reaches the hub with no way to say it is not a tachometer reading.
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
// The scan is on parsed signatures, not on text, so renaming or reformatting cannot
// hide one, and it covers the pointer and slice variants as well as the bare map --
// a projection is just as launderable when it arrives one layer down. Unexported
// helpers are deliberately allowed: they cannot be called from package agent.
//
// The matching guard on the agent side is TestNativeFansIsNeverWrittenFromPfanel,
// so neither file can enforce this alone.
func TestNoExportedNativeFanProjection(t *testing.T) {
	paths, err := filepath.Glob("*.go")
	require.NoError(t, err)

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
			if !ok || fn.Recv != nil || !fn.Name.IsExported() {
				continue
			}
			for _, r := range renderedResults(fn.Type.Results) {
				assert.NotEqualf(t, nativeFanShape, r,
					"%s: %s returns the native Fans shape, which cannot carry "+
						"rpm_est_is_estimate", fset.Position(fn.Pos()), fn.Name.Name)
			}
		}
	}
	require.NotZero(t, parsed, "the scan must have read the package's own sources")
}

// renderedResults flattens a result list to the exact text of each type, so the
// comparison above is against the written type rather than a normalised form that
// nothing else in the package computes.
func renderedResults(fl *ast.FieldList) []string {
	if fl == nil {
		return nil
	}
	var out []string
	for _, f := range fl.List {
		s := types.ExprString(f.Type)
		if strings.HasPrefix(s, "*") || strings.HasPrefix(s, "[]") {
			s = strings.TrimPrefix(strings.TrimPrefix(s, "*"), "[]")
		}
		out = append(out, s)
	}
	return out
}
