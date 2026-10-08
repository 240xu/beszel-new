//go:build testing

package pfanel

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This file is the classification guard for package pfanel: it pins WHICH of the
// three outcomes a given on-disk state produces -- silence, ErrOpen, or ErrRead --
// against real files rather than hand-built errors, because the caller grades on
// these sentinels and nothing else.
//
// The caller in agent/system.go maps ErrOpen to Warn and ErrRead to Error, and the
// line between them is whether the condition can clear without an operator: a wrong
// mode or owner bit can, schema and media damage cannot. Silence is reserved for the
// two states that mean "there is no fan data here" and nothing is wrong.
//
// What this file exists to prevent is the inverse mistake. Matching the driver's
// "unable to open database file" as though it meant the file was absent put the
// entire installed-but-unreadable class at nil, nil: the driver emits that same
// string for EACCES, so a pfanel that was present and could not be read was
// reported as a machine with no pfanel, with nothing written at any level.

// skipIfRoot guards the fixtures that rely on a mode bit being enforced. As root the
// bit is advisory and the "unreadable" file would read cleanly, which would make the
// test assert nothing while appearing to pass.
func skipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("running as root: mode 000 does not deny access, so this fixture cannot produce the condition")
	}
}

// TestIsAbsentDoesNotMatchOpenFailures is the C-2 guard in its smallest form. The
// predicate is what the whole fix turned on, so it is pinned directly rather than
// only through LatestSample.
func TestIsAbsentDoesNotMatchOpenFailures(t *testing.T) {
	assert.True(t, isAbsent(errors.New("no such table: samples")),
		"a database that opened and has no samples table is an absence, and stays silent")
	assert.False(t, isAbsent(errors.New("unable to open database file (14)")),
		"this is the string the driver also emits for EACCES; treating it as an absence "+
			"is what silenced installed-but-unreadable pfanel")
	assert.False(t, isAbsent(errors.New("no such file")))
}

// TestIsOpenFailureIsADifferentQuestion keeps the two predicates from collapsing into
// each other, since either one swallowing the other's matches restores the old bug.
func TestIsOpenFailureIsADifferentQuestion(t *testing.T) {
	assert.True(t, isOpenFailure(errors.New("unable to open database file (14)")))
	assert.True(t, isOpenFailure(errors.New("no such file or directory")))
	assert.False(t, isOpenFailure(errors.New("no such table: samples")))
	assert.False(t, isOpenFailure(errors.New("database disk image is malformed (11)")))
}

// TestNotOnDiskRequiresProofOfAbsence pins the difference between "not there" and
// "could not tell", which err != nil conflates. Silence is only defensible on the
// first; the second must fall through to a graded error.
func TestNotOnDiskRequiresProofOfAbsence(t *testing.T) {
	dir := t.TempDir()

	absent := filepath.Join(dir, "never-created.db")
	assert.True(t, notOnDisk(absent), "a definitive ENOENT is the one thing silence is allowed on")

	present := writeDB(t, realDDL, [][]any{{int64(1791333981), 49.0, 30, 30, 3800, "curve"}})
	assert.False(t, notOnDisk(present), "the live database is on disk")

	skipIfRoot(t)
	locked := filepath.Join(dir, "locked")
	require.NoError(t, os.Mkdir(locked, 0o755))
	hidden := filepath.Join(locked, "pfanel.db")
	require.NoError(t, os.WriteFile(hidden, nil, 0o644))
	require.NoError(t, os.Chmod(locked, 0o000))
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	assert.False(t, notOnDisk(hidden),
		"stat failed with EACCES, which is not proof the file is missing. Returning true "+
			"here would drop a permission fault on the floor, which is the failure this "+
			"predicate was written to remove")
}

// TestHostileDatabasesAreNeverSilent walks the on-disk states that are not a supported
// absence and asserts each one arrives as a graded sentinel.
//
// Every row here was previously silent or, for the two corrupt shapes, already
// ErrRead. The rows are ordered by what the driver reports, which on every shape
// measured coincides with whether the condition can clear on its own:
//
//	unable to open database file (14)  -> ErrOpen -> the caller's Warn
//	everything else with the file present -> ErrRead -> the caller's Error
//
// That coincidence is worth stating rather than assuming: the code splits on the
// driver's own open-versus-IO boundary, so a future driver rewording can move a case
// across the line between Warn and Error. What no rewording can do is silence one,
// because the two silent branches above are a schema message and a stat, not text the
// driver owns.
func TestHostileDatabasesAreNeverSilent(t *testing.T) {
	skipIfRoot(t)
	dir := t.TempDir()

	// A real pfanel-shaped database that exists and cannot be opened. This is the row
	// the old text match swallowed; it is why ErrOpen was a branch nothing could reach.
	unreadable := writeDB(t, realDDL, [][]any{{int64(1791333981), 49.0, 30, 30, 3800, "curve"}})
	require.NoError(t, os.Chmod(unreadable, 0o000))
	t.Cleanup(func() { _ = os.Chmod(unreadable, 0o600) })

	// A path that is a directory. Something is there, so it is not an absence, and a
	// directory at the database path does not resolve itself.
	asDir := filepath.Join(dir, "directory.db")
	require.NoError(t, os.Mkdir(asDir, 0o755))

	// Not sqlite at all. The most common shape for a truncated or half-written file.
	garbage := filepath.Join(dir, "garbage.db")
	require.NoError(t, os.WriteFile(garbage, []byte("not a database, not even close\n"), 0o644))

	// A genuine database with its tail cut off: the header parses, the pages do not.
	// Distinct from garbage because it passes the "is it sqlite" check and fails later,
	// which is the case a header-only check would misfile.
	truncated := writeDB(t, realDDL, [][]any{{int64(1791333981), 49.0, 30, 30, 3800, "curve"}})
	raw, err := os.ReadFile(truncated)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(truncated, raw[:len(raw)/2], 0o644))

	// A database whose parent directory cannot be searched, so the file's very
	// existence is unprovable. NotOnDisk must decline to answer here.
	unsearchable := filepath.Join(dir, "sealed")
	require.NoError(t, os.Mkdir(unsearchable, 0o755))
	sealedDB := filepath.Join(unsearchable, "pfanel.db")
	require.NoError(t, os.WriteFile(sealedDB, nil, 0o644))
	require.NoError(t, os.Chmod(unsearchable, 0o000))
	t.Cleanup(func() { _ = os.Chmod(unsearchable, 0o755) })

	for _, tc := range []struct {
		name string
		path string
		want error
	}{
		{"mode-000 on a real database", unreadable, ErrOpen},
		{"directory at the database path", asDir, ErrRead},
		{"file that is not sqlite", garbage, ErrRead},
		{"sqlite database truncated mid-file", truncated, ErrRead},
		{"parent directory cannot be searched", sealedDB, ErrOpen},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := LatestSample(tc.path)

			require.Error(t, err,
				"pfanel is present and this state is not a supported absence; silence here "+
					"is what freezes the fan section with no trace at any level")
			assert.Nil(t, s, "an error means no sample, so none may be returned alongside it")
			assert.ErrorIs(t, err, tc.want)
			assert.NotErrorIs(t, err, ErrNoRow,
				"ErrNoRow means a table with no rows, which is a different, non-fault state")
		})
	}
}

// TestLatestSampleGradesMode000AsOpen states the headline case on its own, because it
// is the one that regressed: a pfanel that is installed, current and correctly
// written, that this process simply cannot read.
//
// It must be ErrOpen rather than ErrRead because ErrOpen is what the caller grades as
// transient. A chmod or chown by whatever manages pfanel clears it, and no interval
// heals the alternative reading -- "the file disagrees with pfanel" -- which would be
// a false account of a file that is perfectly fine.
func TestLatestSampleGradesMode000AsOpen(t *testing.T) {
	skipIfRoot(t)
	path := writeDB(t, realDDL, [][]any{{int64(1791333981), 49.0, 30, 30, 3800, "curve"}})
	require.NoError(t, os.Chmod(path, 0o000))
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })

	_, err := LatestSample(path)

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrOpen,
		"the file is present and the driver could not get a handle on it")
	assert.NotErrorIs(t, err, ErrRead,
		"grading this as a read failure reports a schema disagreement between two "+
			"programs that is not what happened; the database was never read at all")
}

// TestLatestSampleKeepsSilenceScopedToAbsence guards the other direction. Making every
// failure loud would fix the original bug and be wrong in a new way: a machine without
// pfanel, and a pfanel too old to have written a samples table, are supported
// configurations and must not put a line in an operator's log every minute.
func TestLatestSampleKeepsSilenceScopedToAbsence(t *testing.T) {
	dir := t.TempDir()

	missing := filepath.Join(dir, "never-created.db")

	// A zero-length file: opened as an empty database, understood, no samples table.
	// This is the residue a daemon killed mid-create leaves, and it stays silent.
	headerOnly := filepath.Join(dir, "header-only.db")
	require.NoError(t, os.WriteFile(headerOnly, nil, 0o644))

	// A real sqlite database whose schema has no samples table at all.
	noTable := writeDB(t, "CREATE TABLE events(id INTEGER PRIMARY KEY, ts INTEGER);", nil)

	for _, tc := range []struct {
		name string
		path string
	}{
		{"no database at all", missing},
		{"zero-length file", headerOnly},
		{"database without a samples table", noTable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := LatestSample(tc.path)
			assert.NoError(t, err, "an absence is not a fault and must stay off the log")
			assert.Nil(t, s)
		})
	}
}
