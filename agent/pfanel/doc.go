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
// Errors are split so an operator hears about the ones that matter. A missing database
// or a missing samples table is a supported configuration -- a machine without pfanel,
// or one whose pfanel never wrote a samples table -- and is reported as nil, nil. A
// table that exists but holds no rows is a real state rather than a failure, and gets
// its own ErrNoRow so a caller can drop the sample without logging it. Everything else
// arrives as ErrOpen or ErrRead, because a silent stop is worse than a log line, and
// the caller in agent/system.go grades those Warn and Error respectively: Warn for
// installed-but-unreadable, since a mode or owner bit can be corrected without the
// collector's help, and Error for schema or media damage, which no interval heals.
//
// Silence is the part that has to be earned, and the way it is earned is the one thing
// worth knowing before editing this package: it is decided by the FILESYSTEM, never by
// the driver's wording. Deciding it from the message meant matching "unable to open
// database file", which the driver also emits for EACCES -- so a pfanel that was
// installed, current and completely healthy was classified as not installed, returned
// nil with a nil error, and was reported at no level at all. The entire
// installed-but-unreadable class was silently dropped, which is precisely the class
// ErrOpen exists to name. Existence is now os.Stat, only a schema message ("no such
// table") may imply absence without the filesystem agreeing, and a stat that failed
// for any reason other than a definitive ENOENT counts as unknown rather than absent.
//
// The cost of that rule is worth stating too. A driver that rewords one of its strings
// can still move a real failure between ErrOpen and ErrRead, hence between Warn and
// Error, because the split still follows the driver's own open-versus-IO boundary. It
// can no longer move one to silence, which is the property worth having. The remaining
// staleness signal is unchanged: plus.FanStats.SampleTsS, and nothing else in the
// sample carries a time.
package pfanel
