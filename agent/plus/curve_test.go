//go:build testing

package plus

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The fixtures are the host's own /etc files, copied in byte for byte, so that a
// parser change cannot quietly agree with a fixture that drifted from the file
// the daemon actually sources. They live in testdata rather than in literals
// because both platform files quote a command with backticks, which cannot
// appear in a Go raw string, and because a copied file stays reviewable.
func golden(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoErrorf(t, err, "testdata/%s must be present", name)
	return string(raw)
}

func TestGoldenFixturesAreTheHostFiles(t *testing.T) {
	for _, tc := range []struct {
		name string
		size int
	}{
		{"pfand.conf", 300},
		{"platform.conf", 636},
		{"fan-candidates.txt", 546},
	} {
		assert.Equalf(t, tc.size, len(golden(t, tc.name)),
			"testdata/%s drifted from the host file; re-extract it rather than editing", tc.name)
	}
}

func TestParseCurveReadsTheHostCurve(t *testing.T) {
	c, err := ParseCurve(golden(t, "pfand.conf"))
	require.NoError(t, err)
	// The interleaved skeleton: IDLE bounds the L0 band, and T1 bounds the L1
	// band, not L0. read_params and pfand's want_for both pair (Tn, Ln).
	assert.Equal(t, 50, c.Idle)
	assert.Equal(t, 25, c.L0)
	assert.Equal(t, 56, c.T1)
	assert.Equal(t, 30, c.L1)
	assert.Equal(t, 72, c.T4, "T4 is the host's fourth band edge, not an 84C backstop")
	assert.Equal(t, 60, c.L4)
	assert.Equal(t, 79, c.T5)
	assert.Equal(t, 70, c.L5)
	assert.Equal(t, 80, c.Emerg)
	assert.Equal(t, 100, c.E)
	assert.Equal(t, 75, c.EmergRel)
	assert.Equal(t, 70, c.Cap)
	assert.Equal(t, 6, c.Trim)
	assert.Equal(t, 1, c.Gain)
	assert.Equal(t, 3, c.Hyst)
	assert.Equal(t, 30, c.Dwell)
	assert.Equal(t, 2, c.Dead)
	assert.Equal(t, 10, c.IV)
}

// TestParseCurveIgnoresCommentsAndBlankLines is the multi-line case: the host
// file interleaves four comment sections among the assignments, and a parser
// that assumed one assignment per line still has to see all 22 keys.
func TestParseCurveIgnoresCommentsAndBlankLines(t *testing.T) {
	src := "# leading\n" +
		"\n" +
		"IDLE = 50   # trailing comment\n" +
		"L0=25\n" +
		"# mid-file section\n" +
		"T1=56\nL1=30\nT2=62\nL2=40\nT3=67\nL3=50\nT4=72\nL4=60\nT5=79\nL5=70\n" +
		"EMERG=80\nE=100\nEMERG_REL=75\nCAP=70\nTRIM=6\nGAIN=1\nHYST=3\nDWELL=30\nDEAD=2\nIV=10\n"
	c, err := ParseCurve(src)
	require.NoError(t, err)
	assert.Equal(t, 50, c.Idle)
	assert.Equal(t, 40, c.L2)
	assert.Equal(t, 10, c.IV)
}

// TestParseCurveRefusesTwoAssignmentsOnOneLine records a third disagreement
// between the host readers. The daemon sources the file, so "T2=62 L2=40" sets
// both; the panel's line regex is anchored at the end and skips the line, leaving
// both on their compiled-in defaults. Splitting the line would invent a reading
// neither side has, so it is refused instead.
func TestParseCurveRefusesTwoAssignmentsOnOneLine(t *testing.T) {
	src := strings.Replace(golden(t, "pfand.conf"), "L5=70\n", "L5=70 IV=11\n", 1)
	_, err := ParseCurve(src)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "L5")
}

// TestParseCurveToleratesValuePadding is the reading half of the same rule: a
// value with trailing spaces is trimmed by both host readers, so it reads as the
// number rather than as a malformed assignment.
func TestParseCurveToleratesValuePadding(t *testing.T) {
	c, err := ParseCurve(strings.Replace(golden(t, "pfand.conf"), "IV=10", "IV= 10  ", 1))
	require.NoError(t, err)
	assert.Equal(t, 10, c.IV)
}

// TestParseCurveToleratesCarriageReturns matters because both host readers strip
// trailing whitespace, so a config edited on another host must still parse.
func TestParseCurveToleratesCarriageReturns(t *testing.T) {
	c, err := ParseCurve(strings.ReplaceAll(golden(t, "pfand.conf"), "\n", "\r\n"))
	require.NoError(t, err)
	assert.Equal(t, 100, c.E)
	assert.Equal(t, 2, c.Dead)
}

// TestParseCurveRefusesJunk is the reason this is not a permissive key/value
// reader: the host daemon sources this file with bash, so a line that is not an
// assignment is a command. pfanel's read_params skips such a line silently.
func TestParseCurveRefusesJunk(t *testing.T) {
	junk := []string{
		"IDLE=50\necho pwned\n",
		"IDLE=50\nrm -rf /\n",
		"#!/bin/sh\n",
		"IDLE=50\nset -u\n",
	}
	for _, src := range junk {
		_, err := ParseCurve(src)
		assert.Errorf(t, err, "must refuse %q", strings.TrimSpace(src))
	}
}

// TestParseCurveRefusesJunkInACompleteCurve isolates the junk check from the
// missing-key check. Appending a command to an otherwise complete curve is the
// dangerous case: every parameter is present, so only the refusal itself catches
// it, and the daemon would source it.
func TestParseCurveRefusesJunkInACompleteCurve(t *testing.T) {
	for _, junk := range []string{"echo pwned", "rm -rf /", "exec /bin/sh", "curl evil"} {
		_, err := ParseCurve(golden(t, "pfand.conf") + junk + "\n")
		require.Errorf(t, err, "must refuse a complete curve followed by %q", junk)
	}
}

// TestParseCurveRefusesUnknownKey covers a mistyped key. The daemon sources the
// file over its own hardcoded defaults, so EMERG_RELX=75 leaves EMERG_REL at the
// compiled-in value and the hysteresis inverts with nothing logged.
func TestParseCurveRefusesUnknownKey(t *testing.T) {
	for _, src := range []string{
		strings.Replace(golden(t, "pfand.conf"), "EMERG_REL=75", "EMERG_RELX=75", 1),
		strings.Replace(golden(t, "pfand.conf"), "T5=79", "T7=79", 1),
		strings.Replace(golden(t, "pfand.conf"), "IV=10", "IVL=10", 1),
	} {
		_, err := ParseCurve(src)
		assert.Error(t, err)
	}
}

// TestParseCurveRefusesUnknownKeyInACompleteCurve isolates the unknown-key rule
// from the missing-key rule, which is what every case above trips over instead:
// replacing EMERG_REL with EMERG_RELX also deletes a required key, so a parser
// that simply skipped unrecognised keys would still be caught by the missing-key
// report and the unknown-key rule would look covered when nothing tests it.
// Both bypasses -- skipping the line, and taking it as a default -- survive that
// test and die here, because appending a key to a complete curve leaves every
// required key present and only the unknown-key rule can refuse it.
// The error has to name the key, since the daemon's symptom is a parameter
// left on its compiled-in default with nothing to search the log for.
func TestParseCurveRefusesUnknownKeyInACompleteCurve(t *testing.T) {
	for _, extra := range []string{"EVIL=1", "PFAN_PRESET=fast", "emerg_rel=75"} {
		key := strings.SplitN(extra, "=", 2)[0]
		_, err := ParseCurve(golden(t, "pfand.conf") + extra + "\n")
		require.Errorf(t, err, "a complete curve followed by %q must be refused", extra)
		assert.Contains(t, err.Error(), "unknown key",
			"the refusal has to be the unknown-key rule, not some later check")
		assert.Containsf(t, err.Error(), key, "the refusal must name %s", key)
	}
}

// TestParseCurveLastDuplicateWins keeps this parser aligned with both host
// readers, which assign as they go and leave the later value in place.
func TestParseCurveLastDuplicateWins(t *testing.T) {
	c, err := ParseCurve(golden(t, "pfand.conf") + "IV=42\n")
	require.NoError(t, err)
	assert.Equal(t, 42, c.IV)
}

func TestParseCurveRequiresEveryKey(t *testing.T) {
	for _, key := range curveKeys() {
		src := keyLineOf(golden(t, "pfand.conf"), key)
		_, err := ParseCurve(src)
		require.Errorf(t, err, "removing %s must fail", key)
		assert.Containsf(t, err.Error(), key, "the error must name %s", key)
	}
}

func TestParseCurveRejectsEmptyAndHeaderOnly(t *testing.T) {
	for _, src := range []string{
		"",
		"\n\n\n",
		"# only comments\n# and more\n",
	} {
		_, err := ParseCurve(src)
		assert.Errorf(t, err, "must refuse %q", src)
	}
}

// TestParseCurveRejectsOutOfRangeValue covers values that survive the shape
// check but not the conversion: the host readers match [0-9]+ and int().
func TestParseCurveRejectsOutOfRangeValue(t *testing.T) {
	for _, v := range []string{"-1", "99999999999999999999", "1e3", "0x10", "5 0", "1.0", "+5"} {
		src := strings.Replace(golden(t, "pfand.conf"), "IV=10", "IV="+v, 1)
		_, err := ParseCurve(src)
		assert.Errorf(t, err, "must refuse IV=%q", v)
	}
}

// TestValidateAcceptsTheHostCurve is the baseline the mutations break.
func TestValidateAcceptsTheHostCurve(t *testing.T) {
	c, err := ParseCurve(golden(t, "pfand.conf"))
	require.NoError(t, err)
	assert.NoError(t, c.Validate())
}

func TestValidateRejectsNonIncreasingTemps(t *testing.T) {
	base, err := ParseCurve(golden(t, "pfand.conf"))
	require.NoError(t, err)
	for _, tc := range []struct {
		name string
		mut  func(*Curve)
	}{
		{"equal T4 and T3", func(c *Curve) { c.T4 = c.T3 }},
		{"equal IDLE and T1", func(c *Curve) { c.T1 = c.Idle }},
		{"T5 reaches EMERG", func(c *Curve) { c.T5 = c.Emerg }},
		{"T2 below T1", func(c *Curve) { c.T2 = c.T1 - 1 }},
		{"EMERG below T5", func(c *Curve) { c.Emerg = c.T5 - 1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := *base
			tc.mut(&c)
			assert.Error(t, c.Validate())
		})
	}
}

func TestValidateRejectsInvertedHysteresis(t *testing.T) {
	c, err := ParseCurve(golden(t, "pfand.conf"))
	require.NoError(t, err)
	c.EmergRel = c.Emerg
	assert.Error(t, c.Validate(), "EMERG_REL equal to EMERG makes the emergency latch unreachable")
	c.EmergRel = c.Emerg + 1
	assert.Error(t, c.Validate())
}

// TestValidateRejectsZeroLowestBand covers both places a zero could reach the fan.
// The duty whitelist refuses it outright, and in a curve 0 is the firmware's stop
// value, which is why the host pins L0_MIN.
func TestValidateRejectsZeroLowestBand(t *testing.T) {
	require.Error(t, ValidateLevel(0), "0 is a duty of zero percent, not a curve handover")
	c, err := ParseCurve(golden(t, "pfand.conf"))
	require.NoError(t, err)
	c.L0 = 0
	err = c.Validate()
	require.Error(t, err, "L0=0 would make the lowest band a fan stop")
	assert.Contains(t, err.Error(), "L0")
	// The reason has to name the consequence, not merely that the value is
	// unrecognised: 0 is in the duty whitelist, so an operator meeting it there
	// needs to be told why it means something else in a curve.
	assert.Contains(t, err.Error(), "stop")
}

func TestValidateRejectsLevelBelowFloorAndAboveCap(t *testing.T) {
	c, err := ParseCurve(golden(t, "pfand.conf"))
	require.NoError(t, err)
	c.L2 = 24
	assert.Error(t, c.Validate(), "below the firmware table floor")
	c, err = ParseCurve(golden(t, "pfand.conf"))
	require.NoError(t, err)
	c.L5 = c.Cap + 1
	assert.Error(t, c.Validate())
}

// TestValidateRejectsDutyAboveCapSeparatelyFromTheTrimTable pins the two band
// rules apart. Every duty in the host's curve is inside the trim table, so
// shrinking the cap below them is the only way the range check is the one that
// fires; without this the range guard could be deleted unnoticed.
func TestValidateRejectsDutyAboveCapSeparatelyFromTheTrimTable(t *testing.T) {
	for _, cap := range []int{40, 50, 60, 69} {
		c, err := ParseCurve(golden(t, "pfand.conf"))
		require.NoError(t, err)
		c.Cap = cap
		err = c.Validate()
		require.Errorf(t, err, "CAP=%d leaves duties above the cap while all stay in the trim table", cap)
	}
}

// TestValidateRejectsBandsOutsideTheTrimTable covers a rule neither host
// validator has. pfand's floor_of and center_of are keyed on the duty value and
// fall through to EMERG for anything outside the six they know, so a curve may
// only band on duties the trim and hysteresis arithmetic can actually address.
func TestValidateRejectsBandsOutsideTheTrimTable(t *testing.T) {
	for _, lvl := range []int{80, 85, 90, 95, 100, 1, 10, 20} {
		c, err := ParseCurve(golden(t, "pfand.conf"))
		require.NoError(t, err)
		c.L3 = lvl
		assert.Errorf(t, c.Validate(), "band duty %d is outside the trim table", lvl)
	}
}

func TestValidateRejectsEmergBelowCap(t *testing.T) {
	c, err := ParseCurve(golden(t, "pfand.conf"))
	require.NoError(t, err)
	c.E = c.Cap - 1
	assert.Error(t, c.Validate(), "the emergency duty must clear the hard cap")
}

func TestValidateRejectsBoundaryParameterOutOfRange(t *testing.T) {
	for _, tc := range []struct {
		name string
		mut  func(*Curve)
	}{
		{"IV below 2", func(c *Curve) { c.IV = 1 }},
		{"IV zero", func(c *Curve) { c.IV = 0 }},
		{"DWELL below IV", func(c *Curve) { c.Dwell = c.IV - 1 }},
		{"TRIM above 30", func(c *Curve) { c.Trim = 31 }},
		{"TRIM negative", func(c *Curve) { c.Trim = -1 }},
		{"GAIN above 10", func(c *Curve) { c.Gain = 11 }},
		{"GAIN negative", func(c *Curve) { c.Gain = -1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := ParseCurve(golden(t, "pfand.conf"))
			require.NoError(t, err)
			tc.mut(c)
			assert.Error(t, c.Validate())
		})
	}
}

// TestValidateIsNotLooserThanTheDaemon guards the direction that matters: the
// daemon refuses to run on a curve it considers inconsistent, so a curve this
// accepts must be one it also accepts.
func TestValidateIsNotLooserThanTheDaemon(t *testing.T) {
	base, err := ParseCurve(golden(t, "pfand.conf"))
	require.NoError(t, err)
	for i := 0; i < base.Emerg; i++ {
		c := *base
		c.EmergRel = c.Emerg + i
		if c.Validate() == nil {
			assert.GreaterOrEqual(t, base.EmergRel, c.Emerg,
				"accepted EMERG_REL=%d with EMERG=%d, which the daemon rejects",
				c.EmergRel, c.Emerg)
		}
	}
	c := *base
	c.L5 = c.Cap + 1
	if c.Validate() == nil {
		assert.Greater(t, c.L5, c.Cap, "accepted L5 above CAP, which the daemon rejects")
	}
}

// TestRenderIsByteIdenticalToTheHostFile pins the emission order. Sorting the
// keys would group L0..L5 before T1..T5, still parse, and lose the pairing that
// makes the skeleton readable.
func TestRenderIsByteIdenticalToTheHostFile(t *testing.T) {
	c, err := ParseCurve(golden(t, "pfand.conf"))
	require.NoError(t, err)
	out := c.Render()
	assert.Equal(t, golden(t, "pfand.conf"), out)
	assert.Less(t, strings.Index(out, "IDLE=50"), strings.Index(out, "T1=56"))
	assert.Less(t, strings.Index(out, "T1=56"), strings.Index(out, "L1=30"),
		"each band edge must be written next to the duty it bounds")
	assert.Less(t, strings.Index(out, "L5=70"), strings.Index(out, "EMERG=80"))
	assert.True(t, strings.HasSuffix(out, "IV=10\n"), "exactly one trailing newline")
}

func TestRenderWritesTheEditedValue(t *testing.T) {
	c, err := ParseCurve(golden(t, "pfand.conf"))
	require.NoError(t, err)
	c.L1 = 35
	out := c.Render()
	assert.Contains(t, out, "L1=35")
	assert.NotContains(t, out, "L1=30")
	c2, err := ParseCurve(out)
	require.NoError(t, err, "the rewritten file must parse back")
	assert.Equal(t, 35, c2.L1)
	assert.Equal(t, c, c2)
}

// TestRenderNeverEmitsSpacingOrExtraEquals is the writer half of the tolerance
// question: the host readers disagree about spacing, and only one of them is
// the daemon.
func TestRenderNeverEmitsSpacingOrExtraEquals(t *testing.T) {
	c, err := ParseCurve(golden(t, "pfand.conf"))
	require.NoError(t, err)
	for _, line := range strings.Split(strings.TrimSuffix(c.Render(), "\n"), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		assert.Regexpf(t, `^[A-Z0-9_]+=[0-9]+$`, line,
			"bash sources this file, so an assignment with padding is not an assignment")
	}
}

// TestWriteCurveFileIsAtomicAndRoundTrips exercises the on-disk half: the host
// writes a sibling temp file and renames, so the daemon never reads a half file.
func TestWriteCurveFileIsAtomicAndRoundTrips(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pfand.conf")
	c, err := ParseCurve(golden(t, "pfand.conf"))
	require.NoError(t, err)
	require.NoError(t, WriteCurveFile(path, c))

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, golden(t, "pfand.conf"), string(raw))
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), info.Mode().Perm())

	back, err := ReadCurveFile(path)
	require.NoError(t, err)
	assert.Equal(t, c, back)

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Len(t, entries, 1, "the temp file must not survive the rename")
}

// TestWriteCurveFileRefusesAnInvalidCurve keeps a bad curve off the disk the
// daemon sources. Writing it would make the daemon fall back to full speed.
func TestWriteCurveFileRefusesAnInvalidCurve(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pfand.conf")
	c, err := ParseCurve(golden(t, "pfand.conf"))
	require.NoError(t, err)
	c.EmergRel = c.Emerg
	require.Error(t, WriteCurveFile(path, c))
	_, statErr := os.Stat(path)
	assert.True(t, os.IsNotExist(statErr), "nothing may be written for a curve that does not validate")
}

func TestReadCurveFileReportsAMissingFile(t *testing.T) {
	_, err := ReadCurveFile(filepath.Join(t.TempDir(), "absent.conf"))
	assert.Error(t, err)
}

// TestWantForPairsEachEdgeWithItsOwnBand is the offset the pairing comment in the
// host's own writer is there to prevent: T1 bounds the L1 band.
func TestWantForPairsEachEdgeWithItsOwnBand(t *testing.T) {
	c, err := ParseCurve(golden(t, "pfand.conf"))
	require.NoError(t, err)
	for _, tc := range []struct {
		temp int
		want int
		why  string
	}{
		{-5, 25, "below IDLE lands on the L0 band"},
		{50, 25, "IDLE itself is inclusive"},
		{51, 30, "one degree above IDLE enters the L1 band"},
		{56, 30, "T1 itself is inclusive"},
		{57, 40, "above T1 enters the L2 band"},
		{62, 40, "T2 itself is inclusive"},
		{67, 50, "T3 itself is inclusive"},
		{72, 60, "T4 itself is inclusive"},
		{79, 70, "T5 itself is inclusive"},
		{80, 100, "EMERG itself is full speed"},
		{95, 100, "well past EMERG stays full speed"},
	} {
		assert.Equalf(t, tc.want, WantFor(c, tc.temp), "%dC: %s", tc.temp, tc.why)
	}
}

// TestWantForFullSpeedsTheGapBetweenTheTopBandAndEmerg pins the branch the band
// walk falls through to. On this host's shipped curve the branch is unreachable,
// because T5 and EMERG are adjacent integers and no temperature lands between
// them; a curve that does leave a gap must run that span at the emergency duty
// rather than idling at the top band.
func TestWantForFullSpeedsTheGapBetweenTheTopBandAndEmerg(t *testing.T) {
	shipped, err := ParseCurve(golden(t, "pfand.conf"))
	require.NoError(t, err)
	assert.Equal(t, shipped.T5+1, shipped.Emerg,
		"the shipped edges are adjacent, so the fall-through is unreachable here")
	assert.Equal(t, shipped.L5, WantFor(shipped, shipped.Emerg-1),
		"one below the emergency edge is still the top band")

	gapped, err := ParseCurve(golden(t, "pfand.conf"))
	require.NoError(t, err)
	gapped.Emerg = 90
	require.NoError(t, gapped.Validate())
	assert.Equal(t, gapped.L5, WantFor(gapped, gapped.T5))
	assert.Equal(t, gapped.E, WantFor(gapped, gapped.T5+1), "the uncovered gap must not idle at the top band")
	assert.Equal(t, gapped.E, WantFor(gapped, gapped.Emerg-1))
}

// TestWantForNeverReportsZero pins that no band can produce the auto handover:
// 0 is a value the firmware treats as stop, so a curve resolving to it would
// look like a valid duty and stop the fan.
func TestWantForNeverReportsZero(t *testing.T) {
	c, err := ParseCurve(golden(t, "pfand.conf"))
	require.NoError(t, err)
	for t0 := -20; t0 <= 120; t0++ {
		got := WantFor(c, t0)
		assert.NotZerof(t, got, "%dC resolved to the stop value", t0)
		require.NoError(t, ValidateLevel(got), "%dC resolved to %d, outside the duty whitelist", t0, got)
	}
}

func curveKeys() []string {
	return []string{"IDLE", "L0", "T1", "L1", "T2", "L2", "T3", "L3", "T4", "L4", "T5", "L5",
		"EMERG", "E", "EMERG_REL", "CAP", "TRIM", "GAIN", "HYST", "DWELL", "DEAD", "IV"}
}

func keyLineOf(src, key string) string {
	var out []string
	for _, line := range strings.Split(src, "\n") {
		if strings.HasPrefix(line, key+"=") {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}
