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
// rowid -- so it is unique and DESC is the newest row. It is Unix SECONDS.
const latestRowSQL = `SELECT ts, temp, level, ec76, rpm, gov FROM samples ORDER BY ts DESC LIMIT 1`

// ErrNoRow: see the package comment for why it is separate from every other error.
var ErrNoRow = errors.New("pfanel: samples table is empty")

// ErrOpen and ErrRead split opening from reading so the caller can grade them; both wrap the driver's own error.
var ErrOpen = errors.New("pfanel: cannot open database")
var ErrRead = errors.New("pfanel: cannot read newest row")

// dsn opens read-only and waits out pfanel's write transaction. WHY both parts are
// here, and what each costs, is in the package comment.
func dsn(path string) string {
	return "file:" + path + "?mode=ro&_pragma=busy_timeout(3000)"
}

// LatestSample returns pfanel's newest samples row, or nil with a nil error when the
// database or its samples table is absent. The error classes are split as the package
// comment describes.
func LatestSample(path string) (*plus.FanSample, error) {
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, fmt.Errorf("%w %s: %w", ErrOpen, path, err)
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
		// A missing file and a missing table are an optional collector, not a fault.
		if isAbsent(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("%w %s: %w", ErrRead, path, err)
	}

	s := &plus.FanSample{TsUnix: ts, TempC: temp.Float64, Gov: gov.String}
	// Each NULL stays nil; substituting 0 would erase "not recorded".
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

// isAbsent matches sqlite's message text, so each substring below is covered by a
// test: a pfanel that rewords one turns an optional collector into a hard error.
func isAbsent(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "no such table") ||
		strings.Contains(msg, "unable to open database file") ||
		strings.Contains(msg, "no such file")
}
