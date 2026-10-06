//go:build testing

package plus

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseFpsLineExtractsAllFields(t *testing.T) {
	// sysfs format: control%:trip:speed_rpm:noise_level_mdb:power_mw
	step, ok := parseFpsLine("25:not-defined:3200:12500:1250")
	require.True(t, ok)
	assert.Equal(t, 25, step.Percent)
	assert.Equal(t, uint16(3200), step.RPMEst)
	assert.Equal(t, 12500, step.NoiseMdb)
	assert.Equal(t, 1250, step.PowerMw)
}

func TestParseFpsLineRejectsAutoState(t *testing.T) {
	_, ok := parseFpsLine("0:not-defined:0:0:0")
	assert.False(t, ok, "state0 is the auto placeholder and must be skipped")
}

func TestParseFpsLineRejectsMalformed(t *testing.T) {
	for _, line := range []string{"", "25:not-defined:3200", "a:b:c:d:e"} {
		_, ok := parseFpsLine(line)
		assert.False(t, ok)
	}
}

func TestParseFpsLineHandlesRealTableEntries(t *testing.T) {
	for _, tc := range []struct {
		line    string
		percent int
	}{
		{"30:not-defined:3800:15000:1500", 30},
		{"70:not-defined:9300:35000:3500", 70},
		{"100:not-defined:12000:50000:5000", 100},
	} {
		s, ok := parseFpsLine(tc.line)
		require.True(t, ok)
		assert.Equal(t, tc.percent, s.Percent)
	}
}

// The table this NAS's firmware publishes, transcribed from
// /sys/bus/acpi/devices/INT3404:00/state*, keyed by step number.
//
// The key count is the point: there are 12 files, so "state*" spans step numbers
// both below and above ten, which is exactly the width at which lexical order and
// numeric order disagree. A fixture with fewer than 11 real steps cannot tell the
// two apart, so a shorter table would hide the bug this file exists to catch.
var realFanStates = map[int]string{
	0:  "0:not-defined:0:0:0",
	1:  "25:not-defined:3200:12500:1250",
	2:  "30:not-defined:3800:15000:1500",
	3:  "40:not-defined:5100:20000:2000",
	4:  "50:not-defined:6300:25000:2500",
	5:  "60:not-defined:7400:30000:3000",
	6:  "70:not-defined:9300:35000:3500",
	7:  "80:not-defined:9800:40000:4000",
	8:  "85:not-defined:10500:42500:4250",
	9:  "90:not-defined:11200:45000:4500",
	10: "95:not-defined:11600:47500:4750",
	11: "100:not-defined:12000:50000:5000",
}

// writeFanFixture lays a state table under a fresh temp root and returns the root,
// so the collector's own path resolution is exercised rather than stubbed.
//
// The trailing newline is not decoration: od -c on a live state file shows one, so
// a fixture without it would never prove the collector trims what the kernel
// actually hands it.
func writeFanFixture(t *testing.T, states map[int]string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "bus", "acpi", "devices", "INT3404:00")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	for step, line := range states {
		name := filepath.Join(dir, "state"+strconv.Itoa(step))
		require.NoError(t, os.WriteFile(name, []byte(line+"\n"), 0o644))
	}
	return root
}

func percentsOf(steps []FanSpeedStep) []int {
	out := make([]int, 0, len(steps))
	for _, s := range steps {
		out = append(out, s.Percent)
	}
	return out
}

// TestCollectFanStepsOrdersStepsNumericallyNotLexically is the ordering contract.
//
// FanSteps is a slice, so its positions are the only ordering a consumer gets. Read
// lexically, the 12 live files produce state0, state1, state10, state11, state2 ...
// and after dropping the auto placeholder the payload opens 25%, 95%, 100%, 30% --
// state10's numbers sitting in state2's slot. This asserts the whole ascending
// sequence, so one misplaced entry fails, and asserts monotonicity separately so
// the failure message names the property that broke rather than a diff.
func TestCollectFanStepsOrdersStepsNumericallyNotLexically(t *testing.T) {
	steps := collectFanSteps(writeFanFixture(t, realFanStates))
	require.Len(t, steps, 11)
	assert.Equal(t,
		[]int{25, 30, 40, 50, 60, 70, 80, 85, 90, 95, 100},
		percentsOf(steps),
		"step N must land at index N-1; lexical order puts state10 and state11 between "+
			"state1 and state2")
	for i := 1; i < len(steps); i++ {
		assert.Lessf(t, steps[i-1].Percent, steps[i].Percent,
			"duty must rise with step number, but index %d (state%d) is %d%% and "+
				"index %d is %d%%", i, i+1, steps[i].Percent, i-1, steps[i-1].Percent)
	}
}

// TestCollectFanStepsDropsAutoPlaceholder covers state0 by value rather than by
// name: the guard cannot be "skip the file called state0", because that would let
// a firmware that shipped a real 0% step through, nor can it be a state index test,
// because a table need not start at 0. The invariant is that no step at or below 0%
// duty enters the payload.
func TestCollectFanStepsDropsAutoPlaceholder(t *testing.T) {
	steps := collectFanSteps(writeFanFixture(t, realFanStates))
	require.Len(t, steps, 11)
	for _, s := range steps {
		assert.Positive(t, s.Percent, "the auto placeholder carries 0% duty and no speed")
	}
	assert.Equal(t, 25, steps[0].Percent,
		"25% is the lowest level the firmware accepts; if state0 survived it would be 0")
}

// TestCollectFanStepsIsNilWithoutATable keeps the absent case honest: an unreadable
// or absent table must leave fan_steps out of the payload entirely rather than
// publishing an empty slice, so the JSON key's absence still means "unknown".
func TestCollectFanStepsIsNilWithoutATable(t *testing.T) {
	assert.Nil(t, collectFanSteps(t.TempDir()))
}

// TestCollectFanStepsSkipsStateWithUnparsableBody checks one bad file costs only
// that step. A truncated or padded state file is a firmware quirk this collector
// should step over, not a reason to drop the eleven good rows around it.
func TestCollectFanStepsSkipsStateWithUnparsableBody(t *testing.T) {
	steps := collectFanSteps(writeFanFixture(t, map[int]string{
		1: "25:not-defined:3200:12500:1250",
		2: "30:not-defined:3800",
		3: "40:not-defined:5100:20000:2000",
		4: "50:not-defined:6300:25000:2500:",
	}))
	require.Len(t, steps, 2, "a short row and a trailing-separator row must both be skipped")
	assert.Equal(t, []int{25, 40}, percentsOf(steps))
}

// TestCollectFanStepsIgnoresNonNumericStateFiles covers the "state*" glob's other
// edge: a sibling file that carries no step number at all. Such a file must be
// ignored outright rather than sorted in and parsed, because it has no position in
// the sequence.
func TestCollectFanStepsIgnoresNonNumericStateFiles(t *testing.T) {
	root := writeFanFixture(t, map[int]string{1: "25:not-defined:3200:12500:1250"})
	dir := filepath.Join(root, "bus", "acpi", "devices", "INT3404:00")
	// The body is a well-formed row on purpose. A name with no number is only
	// detectable by its name; if the collector filtered it downstream instead --
	// or not at all -- a parseable body is the only thing that exposes the miss.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "state_default"), []byte("50:not-defined:9999:7:7\n"), 0o644))
	assert.Equal(t, []int{25}, percentsOf(collectFanSteps(root)))
}

// TestCollectFanStepsIgnoresOtherFanInstances pins the single-instance scope.
//
// This host publishes INT3404:01 alongside INT3404:00, both described "Fan 1", and
// FanSpeedStep carries no instance field. Reading :01 as well would append its rows
// to :00's with nothing to tell the two apart, so the second fan's numbers would be
// served as the first fan's. The fixture gives :01 a table with a value no row of
// :00 has, so a collector that merged would be caught by the length alone.
func TestCollectFanStepsIgnoresOtherFanInstances(t *testing.T) {
	root := writeFanFixture(t, realFanStates)
	other := filepath.Join(root, "bus", "acpi", "devices", "INT3404:01")
	require.NoError(t, os.MkdirAll(other, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(other, "state1"), []byte("25:not-defined:9999:7:7\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(other, "state2"), []byte("30:not-defined:8888:8:8\n"), 0o644))

	steps := collectFanSteps(root)
	require.Len(t, steps, 11, "a second instance's rows must not be appended to the first's")
	assert.Equal(t, uint16(3200), steps[0].RPMEst, "row 1 belongs to INT3404:00, not :01")
}

// TestParseFpsLineAcceptsTrailingNewline pins the trimming at the parse boundary
// rather than only where the collector happens to read the file. The kernel's
// state files end in "\n" (od -c confirms it), so parseFpsLine has to tolerate an
// untrimmed line itself: the collector currently strips it first, and a caller that
// ever stops doing so would otherwise lose every row to a silent parse failure.
func TestParseFpsLineAcceptsTrailingNewline(t *testing.T) {
	s, ok := parseFpsLine("25:not-defined:3200:12500:1250\n")
	require.True(t, ok)
	assert.Equal(t, 1250, s.PowerMw)
}

// TestParseFpsLineRejectsExtraFields covers the other half of the field-count
// guard. Only the short direction was asserted at first, which left "at least five
// fields" passing the whole suite: reading the first five of a longer row turns a
// firmware that appended a column into a plausible-looking step rather than a skip.
// The count is therefore exact in both directions.
func TestParseFpsLineRejectsExtraFields(t *testing.T) {
	for _, line := range []string{
		"25:not-defined:3200:12500:1250:",
		"25:not-defined:3200:12500:1250:extra",
		"25:not-defined:3200:12500:1250:0:0:0:0",
	} {
		_, ok := parseFpsLine(line)
		assert.Falsef(t, ok, "a %d-field line must be skipped, not truncated: %q",
			len(strings.Split(line, ":")), line)
	}
}

// TestParseFpsLineKeepsEachColumnInItsOwnField pins the column mapping with four
// values no transposition can hide: the four numbers differ from one another, so a
// speed/noise or noise/power swap moves a value into a field whose value differs.
func TestParseFpsLineKeepsEachColumnInItsOwnField(t *testing.T) {
	s, ok := parseFpsLine("70:not-defined:9300:35000:3500")
	require.True(t, ok)
	assert.Equal(t, 70, s.Percent)
	assert.Equal(t, uint16(9300), s.RPMEst)
	assert.Equal(t, 35000, s.NoiseMdb)
	assert.Equal(t, 3500, s.PowerMw)
}

// TestParseFpsLineBoundsSpeedAtUint16 fixes the ceiling from both sides.
//
// 65535 is kept, 65536 is rejected, and the rejection is a rejection rather than a
// truncation. The sentinel in fan_speed_rpm is 0xFFFFFFFF, which as a 64-bit number
// is far above the bound but as a uint16 is exactly 65535 -- the one value a strict
// "> 65535" guard admits. Keeping the bound inclusive of 65535 is therefore a
// deliberate decision, asserted here so it cannot drift into a silent wrap.
func TestParseFpsLineBoundsSpeedAtUint16(t *testing.T) {
	ceiling, ok := parseFpsLine("50:not-defined:65535:25000:2500")
	require.True(t, ok, "65535 is representable and must be kept")
	assert.Equal(t, uint16(65535), ceiling.RPMEst)

	_, ok = parseFpsLine("50:not-defined:65536:25000:2500")
	assert.False(t, ok, "65536 does not fit uint16 and must be dropped, not wrapped")
}

// TestCollectFanStepsDropsSentinelSpeed checks the same bound inside the collector,
// where a 0xFFFFFFFF row is a real risk rather than a hypothetical: it is what a
// firmware that copied the no-tachometer sentinel into a state file would publish,
// and dropping just that row keeps the rest of the table usable.
func TestCollectFanStepsDropsSentinelSpeed(t *testing.T) {
	steps := collectFanSteps(writeFanFixture(t, map[int]string{
		1: "25:not-defined:3200:12500:1250",
		2: "50:not-defined:4294967295:25000:2500",
		3: "60:not-defined:7400:30000:3000",
	}))
	require.Len(t, steps, 2)
	assert.Equal(t, []int{25, 60}, percentsOf(steps))
	for _, s := range steps {
		assert.NotEqual(t, uint16(65535), s.RPMEst,
			"4294967295 truncated to uint16 is 65535, a plausible-looking speed")
	}
}

// TestCollectExposesFanStepsThroughCollect runs the one function production calls,
// so a collector that reads the right files but is never wired into the payload
// fails here rather than in a live run.
func TestCollectExposesFanStepsThroughCollect(t *testing.T) {
	s, err := Collect(writeFanFixture(t, realFanStates), nil)
	require.NoError(t, err)
	require.Len(t, s.FanSteps, 11)
	assert.Equal(t, 25, s.FanSteps[0].Percent)
	assert.Equal(t, uint16(3200), s.FanSteps[0].RPMEst)
	assert.Equal(t, 100, s.FanSteps[10].Percent)
	assert.Equal(t, uint16(12000), s.FanSteps[10].RPMEst)
}
