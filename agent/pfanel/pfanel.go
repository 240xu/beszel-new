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
//
// The order of the three tests below is the whole design, so it is worth stating: a
// message only decides the outcome once the filesystem has already ruled out
// absence. Get that backwards and a machine with an unreadable pfanel reads as a
// machine with no pfanel, which is the failure this ordering exists to prevent.
func LatestSample(path string) (*plus.FanSample, error) {
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		// Malformed DSN only: sql.Open parses and returns without connecting, so
		// this branch cannot see a filesystem condition. The open failures that
		// operators actually meet arrive at the switch below instead.
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
			// Opened as a database and understood it; there is just no samples
			// table in it. Another program, or a pfanel too old to have one.
			return nil, nil
		case notOnDisk(path):
			// The file is not there. Not a fault, and not this package's to grade.
			return nil, nil
		case isOpenFailure(err):
			// The file is there and the driver still could not open it: wrong
			// permissions, wrong owner, replaced by something it cannot read.
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

// isAbsent matches the one driver message that means "nothing here to read", and it
// is a claim about the SCHEMA rather than about the file: the database was opened
// and understood, it simply has no samples table.
//
// It deliberately does NOT match the driver's "unable to open database file". That
// message is not a statement that the file is missing -- the driver emits the same
// string for EACCES -- so matching it classified "installed and unreadable" as "not
// installed" and returned nil, nil, which silenced the entire class this package
// grades. Existence is decided by notOnDisk instead, from the filesystem, where a
// permission error cannot masquerade as an absent file. A pfanel release rewording
// this substring can now move a real failure between ErrOpen and ErrRead, but it can
// no longer make one disappear.
func isAbsent(err error) bool {
	return strings.Contains(err.Error(), "no such table")
}

// isOpenFailure matches the driver's "I could not get a handle on the file" family,
// which is a different question from isAbsent's. It is only consulted after notOnDisk
// has established that the file IS present, so a match here means installed and
// unreadable -- the condition ErrOpen exists to name, and the only one that can
// sometimes clear without an operator.
func isOpenFailure(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "unable to open database file") ||
		strings.Contains(msg, "no such file")
}

// notOnDisk reports that path is definitely not there.
//
// Only a definitive "does not exist" counts. A stat that failed for any other reason
// -- a parent directory we cannot search, a path we cannot even form -- is not proof
// of absence, so it returns false and the driver's error is graded above instead of
// dropped. Errnoent's absence and EACCES's absence look identical if you test with
// err != nil, and that is precisely how a fault becomes silence.
func notOnDisk(path string) bool {
	_, err := os.Stat(path)
	return errors.Is(err, os.ErrNotExist)
}
