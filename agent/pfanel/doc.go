// Package pfanel reads pfanel's fan history database and hands the newest row to
// package plus, which converts it and owns the wire contract.
//
// This package exists because plus cannot import the sqlite driver. The only driver
// in this build graph is modernc.org/sqlite, which reaches os/exec through
// modernc.org/libc -- libc implements C system() with exec.Command -- and the fork
// policy test in agent/plus forbids os/exec anywhere in a collector closure, because
// the invariant it pins is that no collector forks a program. That constraint is the
// reason for the split, not a preference, which is also why fork_policy_test.go
// governs this package as a plus-side collector: the boundary is enforced rather than
// remembered. If a future collector needs sqlite, it belongs here too. If a future
// write endpoint needs it, that is a larger decision than this package -- the
// read-only DSN below is the reason nothing here can promote a hot journal on a
// database another program owns.
//
// That DSN is mode=ro plus busy_timeout(3000), and both parts are load-bearing. The
// live file runs journal_mode=delete, where a writer holds an EXCLUSIVE lock for the
// whole transaction, and pfanel writes roughly every 12 seconds: without the timeout a
// reader landing in that window gets SQLITE_BUSY and loses the sample outright rather
// than reporting it a moment late. Read-only is what keeps this process from
// promoting a hot journal on a database another program owns.
//
// The obvious way to add this reader is a `go get modernc.org/sqlite`. Do not. That
// upgrades modernc.org/libc and modernc.org/sqlite and adds three modules to go.sum,
// on a build that already carried the driver as an indirect dependency of
// pocketbase's. The blank import in pfanel.go is enough; there is nothing to fetch.
// The other easy mistake is the schema: the live database has no table called
// fan_history, and it holds real zeros alongside NULLs in level, ec76 and rpm, so a
// plain int column cannot carry it. pfanel.go has the query and the per-column NULL
// handling.
//
// Errors are split so an operator hears about the ones that matter. A missing
// database or a missing samples table is a supported configuration -- a machine
// without pfanel -- and is reported as nil, nil. A table that exists but holds no
// rows is a real state rather than a failure, and gets its own ErrNoRow so a caller
// can drop the sample without logging it. Everything else -- locked, corrupt, schema
// drift -- is returned, because a silent stop is worse than a log line. The caller
// currently logs all of it at debug, so a pfanel that stops being readable freezes
// the fan section with no default-level trace; until that changes, the staleness
// check in plus.FanStats.SampleTsS is what a consumer has to work with.
package pfanel
