//go:build testing

package pfanel

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// realDDL is /var/lib/pfanel/pfanel.db's schema, transcribed verbatim from
// `sqlite3 /var/lib/pfanel/pfanel.db .schema`, tab indentation included.
//
// The columns are named and the PRIMARY KEY is ts, which is what makes the fixture
// behave like the live database: ts is the rowid, so it is unique and ORDER BY ts
// DESC is the newest row. A fixture keyed on anything else cannot catch an ordering
// bug.
const realDDL = "CREATE TABLE samples(\n" +
	"        ts INTEGER PRIMARY KEY, temp REAL, level INTEGER,\n" +
	"        ec76 INTEGER, rpm INTEGER, gov TEXT);\n" +
	"CREATE INDEX i_ts ON samples(ts);"

// writeDB lays a pfanel-shaped sqlite database at a fresh temp path and returns it.
// rows may be shorter or longer than 6 wide so a fixture can carry a table with a
// dropped or added column; the placeholder count follows len(row).
func writeDB(t *testing.T, ddl string, rows [][]any) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pfanel.db")
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer db.Close()
	if ddl != "" {
		_, err = db.Exec(ddl)
		require.NoError(t, err)
	}
	for _, row := range rows {
		marks := ""
		for i := range row {
			if i > 0 {
				marks += ","
			}
			marks += "?"
		}
		_, err = db.Exec("INSERT INTO samples VALUES("+marks+")", row...)
		require.NoError(t, err)
	}
	return path
}

// TestLatestSampleReadsHighestTsNotLastInserted is the ordering contract, written
// so insertion order and ts order disagree: the newest row is inserted first. A
// reader that takes the last row it sees, or that forgets DESC, lands on
// ts=1791333940 here and reports the oldest sample's numbers instead of the
// machine's current state -- a bug no single-row fixture could show.
func TestLatestSampleReadsHighestTsNotLastInserted(t *testing.T) {
	path := writeDB(t, realDDL, [][]any{
		{int64(1791333981), 49.0, 30, 30, 3800, "curve"},
		{int64(1791333971), 56.0, 25, 25, 3200, "curve"},
		{int64(1791333961), 47.0, 25, 25, 3200, "curve"},
		{int64(1791333940), 48.0, 40, 40, 5100, "quiet"},
	})

	s, err := LatestSample(path)
	require.NoError(t, err)
	require.NotNil(t, s)
	assert.Equal(t, int64(1791333981), s.TsUnix, "newest row is ts=1791333981 (level 30)")
	require.NotNil(t, s.LevelPct)
	assert.Equal(t, 30, *s.LevelPct)
	require.NotNil(t, s.EC76Pct)
	assert.Equal(t, 30, *s.EC76Pct)
	require.NotNil(t, s.RPM)
	assert.Equal(t, 3800, *s.RPM)
	assert.Equal(t, "curve", s.Gov)
	assert.Equal(t, 49.0, s.TempC)
}

// TestLatestSampleSampleTsIsUnixSeconds pins the unit of ts.
//
// The live column holds 1791333981, which is a Unix second count: read as
// milliseconds it would be January 1970. Asserting the exact literal catches a
// seconds/milliseconds conversion on the way in; asserting only "> 1e9" would not,
// because 1791333981 * 1000 clears that bar too.
func TestLatestSampleTsIsUnixSeconds(t *testing.T) {
	path := writeDB(t, realDDL, [][]any{
		{int64(1791333981), 49.0, 30, 30, 3800, "curve"},
	})

	s, err := LatestSample(path)
	require.NoError(t, err)
	require.NotNil(t, s)
	assert.Equal(t, int64(1791333981), s.TsUnix,
		"ts must be passed through unchanged; the column is already Unix seconds")
}

// TestLatestSampleNullColumnsStayNil is the per-column NULL contract. The live table
// has 70 NULL levels, 63 NULL duties and 140 NULL rpms, and it also has real zeros
// in the same columns -- 226 rows at level 0, 77 at duty 0. A reader that
// substitutes 0 for a NULL erases the difference between "not recorded" and "set to
// zero", which is the distinction plus's pointer fields exist to carry.
func TestLatestSampleNullColumnsStayNil(t *testing.T) {
	path := writeDB(t, realDDL, [][]any{
		{int64(1791333981), 47.0, nil, nil, nil, nil},
		{int64(1791333980), 46.0, 0, 0, 0, "curve"},
	})

	s, err := LatestSample(path)
	require.NoError(t, err)
	require.NotNil(t, s)
	assert.Nil(t, s.LevelPct)
	assert.Nil(t, s.EC76Pct)
	assert.Nil(t, s.RPM)
	assert.Empty(t, s.Gov)
	assert.Equal(t, 47.0, s.TempC)

	// Now drop the NULL row so the newest row is the all-zero one: its three duty
	// fields must come back as pointers to 0, not as nil. If the reader nil'd
	// everything, both halves of this test would pass.
	_, err = sqlExec(t, path, "DELETE FROM samples WHERE level IS NULL")
	require.NoError(t, err)
	zero, err := LatestSample(path)
	require.NoError(t, err)
	require.NotNil(t, zero)
	require.NotNil(t, zero.LevelPct, "level 0 is a real value, not unknown")
	require.NotNil(t, zero.EC76Pct, "duty 0 is a real value, not unknown")
	require.NotNil(t, zero.RPM, "rpm 0 is stored, so it is present even though it is no estimate")
	assert.Equal(t, 0, *zero.LevelPct)
	assert.Equal(t, 0, *zero.EC76Pct)
	assert.Equal(t, 0, *zero.RPM)
}

func sqlExec(t *testing.T, path, query string, args ...any) (sql.Result, error) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer db.Close()
	return db.Exec(query, args...)
}

// TestLatestSampleLevelAndEC76AreDistinct kills the column-swap mutation.
//
// The fixture uses level 25 with ec76 30 and stores rpm 3800 -- the _FPS value for
// duty 30, because on the live database rpm is a pure function of the duty byte
// (50 distinct duties, 50 distinct (duty, rpm) pairs) and not of the programmed
// level. Swapping the two scanned columns therefore reports level 30 / duty 25 and
// passes every test that only ever sets them equal.
func TestLatestSampleLevelAndEC76AreDistinct(t *testing.T) {
	path := writeDB(t, realDDL, [][]any{
		{int64(1791333981), 49.0, 25, 30, 3800, "curve"},
	})

	s, err := LatestSample(path)
	require.NoError(t, err)
	require.NotNil(t, s)
	require.NotNil(t, s.LevelPct)
	require.NotNil(t, s.EC76Pct)
	assert.Equal(t, 25, *s.LevelPct, "level is pfanel's programmed level")
	assert.Equal(t, 30, *s.EC76Pct, "ec76 is the live duty byte, a different column")
}

func TestLatestSampleReadsGovVerbatim(t *testing.T) {
	for _, gov := range []string{"curve", "free", "s0i3", "quiet", "manual"} {
		path := writeDB(t, realDDL, [][]any{
			{int64(1791333981), 49.0, 30, 30, 3800, gov},
		})
		s, err := LatestSample(path)
		require.NoError(t, err, gov)
		require.NotNil(t, s, gov)
		assert.Equal(t, gov, s.Gov)
	}
}

// ---------------------------------------------------------------------------
// Absent, empty and hostile databases
// ---------------------------------------------------------------------------

func TestLatestSampleIsSilentOnMissingDatabase(t *testing.T) {
	s, err := LatestSample(filepath.Join(t.TempDir(), "absent.db"))
	assert.NoError(t, err, "a machine without pfanel is a supported configuration")
	assert.Nil(t, s)
}

func TestLatestSampleIsSilentOnHeaderOnlyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pfanel.db")
	require.NoError(t, os.WriteFile(path, nil, 0o644))

	s, err := LatestSample(path)
	assert.NoError(t, err, "a zero-length file is what a daemon killed mid-create leaves")
	assert.Nil(t, s)
}

func TestLatestSampleIsSilentOnMissingTable(t *testing.T) {
	path := writeDB(t, "CREATE TABLE events(id INTEGER PRIMARY KEY, ts INTEGER);", nil)

	s, err := LatestSample(path)
	assert.NoError(t, err, "a database without a samples table has no fan state")
	assert.Nil(t, s)
}

func TestLatestSampleReportsEmptyTableDistinctly(t *testing.T) {
	path := writeDB(t, realDDL, nil)

	s, err := LatestSample(path)
	assert.ErrorIs(t, err, ErrNoRow,
		"an empty samples table is a real state, not a missing file, and must not "+
			"be confused with one")
	assert.Nil(t, s)
}

func TestLatestSampleToleratesExtraColumns(t *testing.T) {
	path := writeDB(t,
		realDDL+"\nALTER TABLE samples ADD COLUMN host TEXT;\nALTER TABLE samples ADD COLUMN seq INTEGER;",
		[][]any{{int64(1791333981), 49.0, 30, 30, 3800, "curve", "nas", 7}})

	s, err := LatestSample(path)
	require.NoError(t, err)
	require.NotNil(t, s)
	require.NotNil(t, s.LevelPct)
	assert.Equal(t, 30, *s.LevelPct)
	assert.Equal(t, "curve", s.Gov)
}

func TestLatestSampleReportsMissingColumn(t *testing.T) {
	path := writeDB(t,
		"CREATE TABLE samples(ts INTEGER PRIMARY KEY, temp REAL, level INTEGER, ec76 INTEGER, rpm INTEGER);",
		[][]any{{int64(1791333981), 49.0, 30, 30, 3800}})

	s, err := LatestSample(path)
	require.Error(t, err, "an older pfanel without gov is a schema change an operator must see")
	assert.Nil(t, s)
}

// TestLatestSampleWaitsOutAHeldWriteLock covers the concurrency case.
//
// The live file runs journal_mode=delete, where a writer holds an EXCLUSIVE lock for
// the whole transaction, so a reader with no busy timeout gets SQLITE_BUSY and loses
// the sample. The writer here holds the lock for 200ms against a 3s timeout, and
// rolls back rather than commits, so a reader that gets through must see the last
// COMMITTED row -- which is also the answer to whether a half-written row can leak.
func TestLatestSampleWaitsOutAHeldWriteLock(t *testing.T) {
	path := writeDB(t, realDDL, [][]any{
		{int64(1791333981), 49.0, 30, 30, 3800, "curve"},
	})

	// A dedicated connection, because database/sql pools: BEGIN EXCLUSIVE and
	// ROLLBACK have to land on the same session or the lock is not really held.
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer db.Close()
	conn, err := db.Conn(context.Background())
	require.NoError(t, err)
	defer conn.Close()
	_, err = conn.ExecContext(context.Background(), "BEGIN EXCLUSIVE")
	require.NoError(t, err)
	_, err = conn.ExecContext(context.Background(),
		"INSERT INTO samples VALUES(?,?,?,?,?,?)", int64(1791333991), 50.0, 30, 30, 3800, "curve")
	require.NoError(t, err)

	released := make(chan error, 1)
	go func() {
		time.Sleep(200 * time.Millisecond)
		_, err := conn.ExecContext(context.Background(), "ROLLBACK")
		released <- err
	}()

	s, err := LatestSample(path)
	require.NoError(t, <-released)
	require.NoError(t, err, "a concurrent uncommitted write must not blank the reader")
	require.NotNil(t, s)
	assert.Equal(t, int64(1791333981), s.TsUnix,
		"the uncommitted row must not be visible; the last committed one is current")
}

func TestDSNIsReadOnlyAndWaits(t *testing.T) {
	got := dsn("/var/lib/pfanel/pfanel.db")
	assert.Contains(t, got, "mode=ro", "the reader must never be able to write the file")
	assert.Contains(t, got, "busy_timeout")
}
