//go:build !mips && !mipsle && !mips64 && !mips64le

package pfanel

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/henrygd/beszel/agent/plus"

	_ "modernc.org/sqlite"
)

// latestRowSQL names its columns rather than using *, so a pfanel release adding a
// column cannot break this reader. ts is the table's INTEGER PRIMARY KEY -- the
// rowid -- so it is unique and DESC is the newest row. It is Unix SECONDS.
const latestRowSQL = `SELECT ts, temp, level, ec76, rpm, gov FROM samples ORDER BY ts DESC LIMIT 1`

// dsn opens read-only and waits out pfanel's write transaction. WHY both parts are
// here, and what each costs, is in the package comment.
func dsn(path string) string {
	return "file:" + path + "?mode=ro&_pragma=busy_timeout(3000)"
}

// LatestSample returns pfanel's newest samples row, or nil with a nil error when the
// database or its samples table is absent. The error classes are split as the package
// comment describes, and the order below is the whole design: a message decides the
// outcome only after the filesystem has ruled out absence.
func LatestSample(path string) (*plus.FanSample, error) {
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		// Malformed DSN only: sql.Open returns without connecting, so the open
		// failures operators meet arrive at the switch below instead.
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
		switch {
		case isAbsent(err):
			return nil, nil
		case notOnDisk(path):
			return nil, nil
		case isOpenFailure(err):
			return nil, fmt.Errorf("%w %s: %w", ErrOpen, path, err)
		default:
			return nil, fmt.Errorf("%w %s: %w", ErrRead, path, err)
		}
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

// isAbsent matches the one driver message that means "nothing here to read": the
// database was opened and understood, and simply has no samples table.
//
// It deliberately does NOT match "unable to open database file". That message is not
// a statement that the file is missing -- the driver emits it for EACCES too -- so
// matching it classified an installed-but-unreadable pfanel as not installed and
// returned nil, nil, silencing the class ErrOpen exists to name. Existence is
// decided by notOnDisk instead, where a permission error cannot pass for an absent
// file. A pfanel release rewording this substring can now move a real failure
// between ErrOpen and ErrRead, but can no longer make one disappear.
//
// isOpenFailure is the other half of the same distinction: the driver's "I could not
// get a handle on the file" family, consulted only after notOnDisk has established
// that the file IS there, so a match means installed and unreadable.
func isAbsent(err error) bool {
	return strings.Contains(err.Error(), "no such table")
}

func isOpenFailure(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "unable to open database file") ||
		strings.Contains(msg, "no such file")
}

// notOnDisk reports that path is definitely not there.
//
// Only a definitive "does not exist" counts. A stat that failed for any other reason
// -- an unsearchable parent directory, say -- is not proof of absence, so it returns
// false and the driver's error is graded above instead of dropped. Absence by
// ENOENT and absence by EACCES look identical under `err != nil`, and conflating
// them is how a fault becomes silence.
func notOnDisk(path string) bool {
	_, err := os.Stat(path)
	return errors.Is(err, os.ErrNotExist)
}
