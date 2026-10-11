//go:build testing

package plus

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Parsing and validating are different gates, and only one of them used to stand
// on the byte path. The guard restores captured bytes through that path, so a
// restore refused for a value-domain reason was reported to it as a failed
// control path -- and the breaker counts those.
func TestWriteCurveBytesFileRefusesBytesOutsideTheValueDomain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pfand.conf")
	require.NoError(t, os.WriteFile(path, []byte(golden(t, "pfand.conf")), curveConfMode))
	before, err := os.ReadFile(path)
	require.NoError(t, err)

	// Parses cleanly, and the daemon refuses it: an emergency duty that does not
	// clear the ceiling is not an emergency.
	bad := []byte(strings.ReplaceAll(golden(t, "pfand.conf"), "E=100", "E=70"))
	_, perr := ParseCurve(string(bad))
	require.NoError(t, perr, "the bytes under test must parse")

	werr := WriteCurveBytesFile(path, bad)
	require.Error(t, werr, "a curve the host would not run must not reach the disk")
	assert.ErrorIs(t, werr, ErrCurveInvalid,
		"the caller has to be able to tell this refusal from a disk that would not take the write")

	after, rerr := os.ReadFile(path)
	require.NoError(t, rerr)
	assert.Equal(t, before, after, "a refused write leaves the running curve alone")
}

// The same has to hold for the Curve-shaped path, which is what the panel's own
// save goes through.
func TestWriteCurveFileNamesItsOwnRefusal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pfand.conf")
	c, err := ParseCurve(golden(t, "pfand.conf"))
	require.NoError(t, err)
	c.E = c.Cap
	werr := WriteCurveFile(path, c)
	require.Error(t, werr)
	assert.ErrorIs(t, werr, ErrCurveInvalid)

	_, serr := os.Stat(path)
	assert.Error(t, serr, "nothing may be created for a curve that was refused")
}

// Every refusal this package raises has to name itself, because the guard's
// breaker is a safety device and what counts against it has to be a stated set
// rather than whatever the error happened to say.
func TestEveryCurveRefusalNamesItself(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pfand.conf")
	c, err := ParseCurve(golden(t, "pfand.conf"))
	require.NoError(t, err)
	c.E = c.Cap
	assert.ErrorIs(t, WriteCurveFile(path, c), ErrCurveInvalid)
	assert.ErrorIs(t, WriteCurveBytesFile(path, []byte("NOT A CURVE\n")), ErrCurveInvalid)
}
