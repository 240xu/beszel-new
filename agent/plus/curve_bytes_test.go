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

// TestWriteCurveBytesFilePutsBackExactlyWhatItWasGiven is the reason this
// function exists. The guard restores the bytes it captured before a write, and
// a restore that re-renders would return a file whose comments were regenerated
// rather than the file that was actually running.
func TestWriteCurveBytesFilePutsBackExactlyWhatItWasGiven(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pfand.conf")
	require.NoError(t, os.WriteFile(path, []byte(golden(t, "pfand.conf")), curveConfMode))

	captured, err := os.ReadFile(path)
	require.NoError(t, err)

	// The edit must be one this host would run: the byte path now holds the gate.
	edited := []byte(strings.ReplaceAll(golden(t, "pfand.conf"), "TRIM=6", "TRIM=8"))
	require.NotEqual(t, string(captured), string(edited))
	require.NoError(t, WriteCurveBytesFile(path, edited))
	require.NoError(t, WriteCurveBytesFile(path, captured))

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, string(captured), string(got),
		"the restore must reproduce the bytes, not a rendering of them")
}

// TestWriteCurveBytesFileRefusesBytesThatDoNotParse keeps a file the daemon
// cannot run off the disk. The daemon's reaction to one it dislikes is full
// speed, so an unparsed write is a cooling decision made by a typo.
func TestWriteCurveBytesFileRefusesBytesThatDoNotParse(t *testing.T) {
	for _, tc := range []struct{ name, src string }{
		{"truncated", "IDLE=50\nL0=25\n"},
		{"not an assignment", "rm -rf /\n"},
		{"unknown key", strings.ReplaceAll(golden(t, "pfand.conf"), "CAP=70", "CAP=70\nSOMETHING=1")},
		{"not a number", strings.ReplaceAll(golden(t, "pfand.conf"), "CAP=70", "CAP=loud")},
		{"empty", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "pfand.conf")
			require.NoError(t, os.WriteFile(path, []byte(golden(t, "pfand.conf")), curveConfMode))

			require.Error(t, WriteCurveBytesFile(path, []byte(tc.src)))

			got, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, golden(t, "pfand.conf"), string(got),
				"a refused write must leave the curve that was running in place")
		})
	}
}

// TestWriteCurveBytesFileLeavesNoTemporaryBehind pins the half of the atomic
// replace that only shows up when the rename fails.
func TestWriteCurveBytesFileLeavesNoTemporaryBehind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pfand.conf")
	require.NoError(t, os.WriteFile(path, []byte(golden(t, "pfand.conf")), curveConfMode))

	// A directory where the temp file goes makes the write fail after the file
	// is created.
	require.NoError(t, os.Mkdir(path+".tmp", 0o755))
	require.Error(t, WriteCurveBytesFile(path, []byte(golden(t, "pfand.conf"))))

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, golden(t, "pfand.conf"), string(got))
}
