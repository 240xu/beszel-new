//go:build testing

package hub

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/henrygd/beszel/internal/fanapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The trail is the only record of a write nobody asked for. The thermal floor writes on
// its own, so "the fan sat at a hundred per cent all afternoon" has to be answerable
// after a restart -- and a guard that lives in the hub process takes its memory with it.

func trailLine(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	return strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
}

func TestTheAuditTrailComesBackAfterAProcessRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fan-audit.jsonl")

	first := newFanAudit(path)
	first.add(fanapi.Event{Op: fanapi.OpSetLevel, Outcome: fanapi.OutcomeApplied, Level: 40, At: time.Now()})
	first.add(fanapi.Event{Op: fanapi.OpThermalFloor, Outcome: fanapi.OutcomeHeld, Level: 100, At: time.Now()})

	// What the next hub start does: read the file back rather than start empty.
	second := newFanAudit(path)
	items := second.entries()
	require.Len(t, items, 2, "a restart that forgets the record of an unattended write loses the only one there is")
	assert.Equal(t, "thermal_floor", items[1]["op"])
	assert.Equal(t, "held", items[1]["outcome"])
	assert.EqualValues(t, 100, items[1]["level"])
}

func TestTheAuditTrailKeepsWritingWhenTheDeviceRefusesIt(t *testing.T) {
	// A directory where the file belongs: every append fails, on any platform.
	trail := newFanAudit(t.TempDir())
	trail.add(fanapi.Event{Op: fanapi.OpThermalFloor, Outcome: fanapi.OutcomeHeld, Level: 100, At: time.Now()})

	assert.Len(t, trail.entries(), 1,
		"a full disk must not cost the operator the live trail, and must not take the fan path down with it")
}

func TestTheAuditTrailSkipsALineTheCrashCutInHalf(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fan-audit.jsonl")
	// One whole event, then the first half of another: what a kill during an append
	// leaves behind. Losing the whole file over it would turn a survivable crash into
	// the loss the persistence was added to prevent.
	// Three whole events and then half of a fourth. One good line would not be enough:
	// skipping the bad line and abandoning the file both leave a trail of length one, so
	// the test would pass either way.
	require.NoError(t, os.WriteFile(path, []byte(
		`{"op":"set_level","outcome":"applied","level":40,"at":"2026-10-16T00:00:00Z"}`+"\n"+
			`{"op":"write_curve","outcome":"applied","level":0,"at":"2026-10-16T00:01:00Z"}`+"\n"+
			`{"op":"thermal_floor","outcome":"held","level":100,"at":"2026-10-16T00:02:00Z"}`+"\n"+
			`{"op":"regress","outcome":"hel`), 0o600))

	trail := newFanAudit(path)
	items := trail.entries()
	require.Len(t, items, 3, "one torn line must not cost the records written before it")
	assert.Equal(t, "set_level", items[0]["op"])
	assert.Equal(t, "thermal_floor", items[2]["op"])
}

func TestTheAuditTrailStaysBoundedOnDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fan-audit.jsonl")
	trail := newFanAudit(path)
	for i := 0; i < fanAuditLimit+40; i++ {
		trail.add(fanapi.Event{Op: fanapi.OpThermalFloor, Outcome: fanapi.OutcomeHeld, Level: 100, At: time.Now()})
	}

	lines := trailLine(t, path)
	assert.LessOrEqual(t, len(lines), fanAuditLimit,
		"an append-only file that is never trimmed grows until the disk stops, and a disk that stops is a fan path that stops")
	// The window the route serves is unchanged by any of this.
	assert.Len(t, trail.entries(), fanAuditLimit)
}

func TestTheAuditTrailKeepsReadingPastALineItCannotParse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fan-audit.jsonl")
	// A whole record, a line that will not parse, and a whole record after it. The torn
	// line in the test above sits last, where skipping it and abandoning the file both
	// leave the same trail -- this one is the case that tells them apart, so a load that
	// gives up at the first bad line cannot pass both.
	require.NoError(t, os.WriteFile(path, []byte(
		`{"op":"set_level","outcome":"applied","level":40,"at":"2026-10-16T00:00:00Z"}`+"\n"+
			`{"op": this line is not a record}`+"\n"+
			`{"op":"thermal_floor","outcome":"held","level":100,"at":"2026-10-16T00:02:00Z"}`+"\n"), 0o600))

	trail := newFanAudit(path)
	items := trail.entries()
	require.Len(t, items, 2, "one unreadable line must not cost the records written after it")
	assert.Equal(t, "set_level", items[0]["op"])
	assert.Equal(t, "thermal_floor", items[1]["op"])
}

// The trim rewrites the trail, and the rewrite must not be observable as a gap:
// writing in place truncates first, so a kill inside that window loses records
// the rewrite was going to keep. The rewritten trail is swapped in by rename
// instead, and the swap leaves nothing of itself behind.
func TestTheAuditTrimSwapsTheTrailIntoPlace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fan-audit.jsonl")
	trail := newFanAudit(path)
	for i := 0; i < fanAuditLimit+40; i++ {
		trail.add(fanapi.Event{Op: fanapi.OpThermalFloor, Outcome: fanapi.OutcomeHeld, Level: 100, At: time.Now()})
	}

	_, err := os.Stat(path + ".tmp")
	assert.True(t, os.IsNotExist(err),
		"the temporary file belongs to a rewrite in flight; one left behind is a rewrite that leaked")

	lines := trailLine(t, path)
	require.Len(t, lines, fanAuditLimit, "the trim keeps the window it claims to keep")
	for i, line := range lines {
		assert.True(t, json.Valid([]byte(line)),
			"line %d must be a whole record: the swap cannot leave half a line behind", i)
	}
}

// The app creates the data directory under the process umask, so the trail can
// sit in a directory wider than itself. The directory is narrowed when the
// trail opens, so the file's 0600 is not the only thing between it and every
// other account on the machine.
func TestTheAuditDirectoryIsNarrowedAtOpen(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0o755), "precondition: a directory wider than the trail")

	newFanAudit(filepath.Join(dir, "fan-audit.jsonl"))

	fi, err := os.Stat(dir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), fi.Mode().Perm(),
		"the trail is owner-only, and the directory around it has to be too")
}
