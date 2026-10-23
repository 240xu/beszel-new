package pfanel

import "errors"

// DBPath is pfanel's database on this platform.
const DBPath = "/var/lib/pfanel/pfanel.db"

// ErrNoRow: see the package comment for why it is separate from every other error.
var ErrNoRow = errors.New("pfanel: samples table is empty")

// ErrOpen and ErrRead split opening from reading so the caller can grade them; both wrap the driver's own error.
var ErrOpen = errors.New("pfanel: cannot open database")
var ErrRead = errors.New("pfanel: cannot read newest row")
