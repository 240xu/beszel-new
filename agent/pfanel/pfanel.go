// Package pfanel reads pfanel's fan history database and hands the newest row to
// package plus, which converts it. The split is structural: plus cannot import the
// sqlite driver, because the only driver in this build graph reaches os/exec
// through modernc.org/libc and agent/plus' fork policy test forbids that in its
// closure. This package sits outside it, the same arrangement plus takes NvmeInput
// as a parameter for.
package pfanel

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/henrygd/beszel/agent/plus"

	_ "modernc.org/sqlite"
)

// DBPath is pfanel's database on this platform.
const DBPath = "/var/lib/pfanel/pfanel.db"

// latestRowSQL names its columns rather than using *, so a pfanel release adding a
// column cannot break this reader. ts is the table's INTEGER PRIMARY KEY -- the
// rowid -- so it is unique, it is the index this query seeks, and DESC is the newest
// row. It is Unix SECONDS, not milliseconds or nanoseconds.
const latestRowSQL = `SELECT ts, temp, level, ec76, rpm, gov FROM samples ORDER BY ts DESC LIMIT 1`

// ErrNoRow reports a database with a samples table but no rows, which is a real
// state -- a fresh or just-truncated file -- rather than a failure. It is distinct
// from every other error so a caller can drop the sample silently while still
// logging a locked or corrupt database.
var ErrNoRow = errors.New("pfanel: samples table is empty")

// dsn opens read-only and waits out pfanel's write transaction. The live file runs
// journal_mode=delete, where a writer holds an EXCLUSIVE lock for the whole
// transaction, so a reader landing in that window gets SQLITE_BUSY; pfanel writes
// roughly every 12 seconds, so a busy timeout turns a lost sample into a late one.
// Read-only also means this process never promotes a hot journal on a file another
// program owns.
func dsn(path string) string {
	return "file:" + path + "?mode=ro&_pragma=busy_timeout(3000)"
}

// LatestSample returns pfanel's newest samples row, or nil with a nil error when the
// database or its samples table is absent -- a machine without pfanel is a supported
// configuration. Every other failure -- locked, corrupt, schema drift -- is returned,
// because an operator needs to see those.
func LatestSample(path string) (*plus.FanSample, error) {
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, fmt.Errorf("pfanel: open %s: %w", path, err)
	}
	defer db.Close()

	var (
		ts               int64
		temp             sql.NullFloat64
		level, ec76, rpm sql.NullInt64
		gov              sql.NullString
	)
	err = db.QueryRow(latestRowSQL).Scan(&ts, &temp, &level, &ec76, &rpm, &gov)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, ErrNoRow
	case err != nil:
		// A missing file and a missing table both surface as a SQL error, and
		// neither is worth reporting: the collector is optional.
		if isAbsent(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("pfanel: read %s: %w", path, err)
	}

	s := &plus.FanSample{TsUnix: ts, TempC: temp.Float64, Gov: gov.String}
	// Each NULL stays nil. The live table also holds real zeros in all three columns
	// -- 226 rows at level 0, 77 at ec76 0 -- so substituting 0 would erase the
	// difference between "not recorded" and "set to zero".
	if level.Valid {
		v := int(level.Int64)
		s.LevelPct = &v
	}
	if ec76.Valid {
		v := int(ec76.Int64)
		s.EC76Pct = &v
	}
	if rpm.Valid {
		v := int(rpm.Int64)
		s.RPM = &v
	}
	return s, nil
}

// isAbsent reports whether err means the file or the samples table is not there, as
// opposed to being present and unreadable.
func isAbsent(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "no such table") ||
		strings.Contains(msg, "unable to open database file") ||
		strings.Contains(msg, "no such file")
}
