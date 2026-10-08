//go:build testing

package plus

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var fpsFixture = []FanSpeedStep{
	{Percent: 25, RPMEst: 3200},
	{Percent: 50, RPMEst: 6300},
	{Percent: 100, RPMEst: 12000},
}

func TestEstRPMAtExactSteps(t *testing.T) {
	assert.Equal(t, uint16(3200), estRPM(25, fpsFixture))
	assert.Equal(t, uint16(6300), estRPM(50, fpsFixture))
}

func TestEstRPMInterpolatesBetweenSteps(t *testing.T) {
	// 60 sits midway between 50 (6300) and 100 (12000); expect ~7440
	assert.InDelta(t, 7440, estRPM(60, fpsFixture), 30)
}

func TestEstRPMClampsOutsideTable(t *testing.T) {
	assert.Equal(t, uint16(3200), estRPM(10, fpsFixture))
	assert.Equal(t, uint16(12000), estRPM(150, fpsFixture))
}

func TestEstRPMReturnsZeroWithoutTable(t *testing.T) {
	assert.Equal(t, uint16(0), estRPM(25, nil))
	assert.Equal(t, uint16(0), estRPM(0, fpsFixture))
}

func TestEstRPMSortsUnorderedInput(t *testing.T) {
	unsorted := []FanSpeedStep{
		{Percent: 100, RPMEst: 12000},
		{Percent: 25, RPMEst: 3200},
		{Percent: 50, RPMEst: 6300},
	}
	assert.Equal(t, uint16(6300), estRPM(50, unsorted))
}

func TestFanStatsAlwaysFlagsRPMAsEstimate(t *testing.T) {
	f := &FanStats{RPMEstFlag: true}
	assert.True(t, f.RPMEstFlag,
		"rpm_est must be flagged so the UI labels it an estimate, never a tach reading")
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func ptrInt(v int) *int { return &v }

// fanFPSRoot lays the firmware's _FPS table under a fresh root and returns it, so
// the rpm fallback is exercised through the collector's own path resolution.
func fanFPSRoot(t *testing.T) string {
	t.Helper()
	return writeFanFixture(t, realFanStates)
}

// fpsSample is a sample whose stored rpm is deliberately absent, so collectFan has
// to fall back to interpolating through the _FPS table at that root.
func fpsSample(ec76 int) *FanSample {
	return &FanSample{TsUnix: 1791333981, EC76Pct: ptrInt(ec76)}
}

// ---------------------------------------------------------------------------
// Sample to stats
// ---------------------------------------------------------------------------

func TestCollectFanConvertsEveryColumn(t *testing.T) {
	s := collectFan(fanFPSRoot(t), &FanSample{
		TsUnix:   1791333981,
		TempC:    49.0,
		LevelPct: ptrInt(30),
		EC76Pct:  ptrInt(30),
		RPM:      ptrInt(3800),
		Gov:      "curve",
	})
	require.NotNil(t, s)
	assert.Equal(t, int64(1791333981), s.SampleTsS)
	assert.Equal(t, ptrInt(30), s.SetLevelPct)
	assert.Equal(t, ptrInt(30), s.EC76Pct)
	assert.Equal(t, uint16(3800), s.RPMEst)
	assert.Equal(t, "curve", s.Gov)
	assert.Equal(t, 49.0, s.TempC)
}

func TestCollectFanReturnsNilWithoutSample(t *testing.T) {
	assert.Nil(t, collectFan(t.TempDir(), nil),
		"a machine without pfanel must drop the fan key, not emit an empty section")
}

// ---------------------------------------------------------------------------
// NULL handling -- missing is unknown, never zero
// ---------------------------------------------------------------------------

// TestCollectFanNilLevelIsUnknownNotZero is the NULL contract. 70 of 13036 live
// rows carry no level. The assertion is on the pointer, not a rendered number:
// substituting 0 for unknown would otherwise pass an int-based test, because the
// live table ALSO has 226 rows whose level is a real 0.
func TestCollectFanNilLevelIsUnknownNotZero(t *testing.T) {
	s := collectFan(fanFPSRoot(t), &FanSample{
		TsUnix: 1791333981, TempC: 49.0, EC76Pct: ptrInt(30), RPM: ptrInt(3800), Gov: "curve",
	})
	require.NotNil(t, s)
	assert.Nil(t, s.SetLevelPct, "a NULL level is unknown and must stay nil, never 0")
	assert.Equal(t, ptrInt(30), s.EC76Pct, "the other duty column must be unaffected")
}

// TestCollectFanZeroLevelIsReportedAsZero is the other half, and the reason the
// field is a pointer. level 0 is a real programmed level on this host (226 rows),
// distinct from NULL (70 rows). With a plain int and omitempty both render as an
// absent key, so a UI cannot tell "fan set to level 0" from "pfanel recorded no
// level". omitempty drops a nil pointer but still emits a non-nil pointer to 0,
// which is exactly the three-state split needed here.
func TestCollectFanZeroLevelIsReportedAsZero(t *testing.T) {
	s := collectFan(fanFPSRoot(t), &FanSample{
		TsUnix: 1791333981, TempC: 49.0, LevelPct: ptrInt(0), EC76Pct: ptrInt(0),
	})
	require.NotNil(t, s)
	require.NotNil(t, s.SetLevelPct, "level 0 is a real value and must not be reported as unknown")
	assert.Equal(t, 0, *s.SetLevelPct)
	require.NotNil(t, s.EC76Pct, "duty 0 is a real value, not unknown")
	assert.Equal(t, 0, *s.EC76Pct)
}

// TestCollectFanNilColumnsLeaveOtherFieldsIntact proves the NULL handling is
// per-column rather than all-or-nothing: the live table has rows with a NULL ec76
// (63) and a NULL rpm (140), and dropping the whole sample on any one of them would
// discard the level and governor that row does carry.
func TestCollectFanNilColumnsLeaveOtherFieldsIntact(t *testing.T) {
	s := collectFan(fanFPSRoot(t), &FanSample{
		TsUnix: 1791333981, TempC: 47.0, LevelPct: ptrInt(25), Gov: "s0i3",
	})
	require.NotNil(t, s)
	assert.Nil(t, s.EC76Pct)
	assert.Equal(t, uint16(0), s.RPMEst)
	assert.Equal(t, ptrInt(25), s.SetLevelPct, "a NULL duty must not discard the level")
	assert.Equal(t, "s0i3", s.Gov)
	assert.Equal(t, 47.0, s.TempC)
}

func TestCollectFanNilTempIsUnknown(t *testing.T) {
	s := collectFan(fanFPSRoot(t), &FanSample{
		TsUnix: 1791333981, LevelPct: ptrInt(25), EC76Pct: ptrInt(25), RPM: ptrInt(3200),
	})
	require.NotNil(t, s)
	assert.Equal(t, 0.0, s.TempC)
	assert.Equal(t, ptrInt(25), s.SetLevelPct)
}

// ---------------------------------------------------------------------------
// Field mapping
// ---------------------------------------------------------------------------

// TestCollectFanLevelAndEC76AreDistinct kills the swap mutation.
//
// The sample uses level 25 with ec76 30 and stores rpm 3800 -- the _FPS value for
// duty 30, because on the live database rpm is a pure function of the duty byte
// (50 distinct duties, 50 distinct (duty, rpm) pairs) and not of the programmed
// level. Swapping the two fields therefore reports level 30 / duty 25 and passes
// every test that only ever sets them equal.
func TestCollectFanLevelAndEC76AreDistinct(t *testing.T) {
	s := collectFan(fanFPSRoot(t), &FanSample{
		TsUnix: 1791333981, TempC: 49.0, LevelPct: ptrInt(25), EC76Pct: ptrInt(30), RPM: ptrInt(3800),
	})
	require.NotNil(t, s)
	assert.Equal(t, ptrInt(25), s.SetLevelPct, "level is pfanel's programmed level")
	assert.Equal(t, ptrInt(30), s.EC76Pct, "ec76 is the live duty byte, a different column")
}

// TestCollectFanPrefersStoredRPMOverInterpolation proves the stored rpm survives
// instead of being recomputed. 6426 is off the _FPS table and is what pfanel wrote
// for this duty, so a collector that always re-interpolates would report 3200 here
// and quietly disagree with the daemon that actually drives the fan.
func TestCollectFanPrefersStoredRPMOverInterpolation(t *testing.T) {
	s := collectFan(fanFPSRoot(t), &FanSample{
		TsUnix: 1791333981, LevelPct: ptrInt(25), EC76Pct: ptrInt(25), RPM: ptrInt(6426),
	})
	require.NotNil(t, s)
	assert.Equal(t, uint16(6426), s.RPMEst, "pfanel's own estimate must not be replaced")
}

// TestCollectFanRejectsOutOfRangeStoredRPM covers the wrap guard: 70000 truncated
// to uint16 is 4464, which would look like a plausible fan speed.
func TestCollectFanRejectsOutOfRangeStoredRPM(t *testing.T) {
	s := collectFan(fanFPSRoot(t), &FanSample{
		TsUnix: 1791333981, EC76Pct: ptrInt(25), RPM: ptrInt(70000),
	})
	require.NotNil(t, s)
	assert.Equal(t, uint16(3200), s.RPMEst,
		"an rpm above uint16's ceiling must fall through to interpolation, not wrap")
}

// TestCollectFanFallsBackToFPSWhenRPMIsNil covers the 140 live rows with no rpm.
// With the real _FPS table present, duty 60 interpolates to 7400.
func TestCollectFanFallsBackToFPSWhenRPMIsNil(t *testing.T) {
	s := collectFan(fanFPSRoot(t), fpsSample(60))
	require.NotNil(t, s)
	assert.Equal(t, uint16(7400), s.RPMEst, "duty 60 must interpolate through _FPS")
}

// TestCollectFanNoFallbackWithoutFPSTable proves the fallback degrades to unknown
// rather than to 0-as-a-real-speed: with no _FPS table to interpolate through, the
// estimate stays 0 and rpm_est_is_estimate is still emitted, so a consumer reads
// "no estimate" and not "a fan turning at 0 rpm".
func TestCollectFanNoFallbackWithoutFPSTable(t *testing.T) {
	s := collectFan(t.TempDir(), fpsSample(60))
	require.NotNil(t, s)
	assert.Equal(t, uint16(0), s.RPMEst)
	assert.True(t, s.RPMEstFlag)
}

// TestCollectFanZeroDutyDoesNotEstimate proves duty 0 -- a real value in 77 live
// rows -- does not get pushed through the interpolation, which would clamp it up to
// the lowest table step and report a turning fan for a stopped one.
func TestCollectFanZeroDutyDoesNotEstimate(t *testing.T) {
	s := collectFan(fanFPSRoot(t), fpsSample(0))
	require.NotNil(t, s)
	assert.Equal(t, uint16(0), s.RPMEst, "duty 0 is off the table, so there is no estimate")
}

// TestCollectFanAlwaysSetsEstimateFlag is the non-tautological version of the
// brief's TestFanStatsAlwaysFlagsRPMAsEstimate, which constructs the struct with the
// flag set and asserts it is set -- a mutant that drops the assignment from the
// collector survives it. This drives the real collector instead.
func TestCollectFanAlwaysSetsEstimateFlag(t *testing.T) {
	root := fanFPSRoot(t)
	for name, sample := range map[string]*FanSample{
		"stored rpm": {TsUnix: 1791333981, EC76Pct: ptrInt(30), RPM: ptrInt(3800)},
		"no rpm":     {TsUnix: 1791333981, EC76Pct: ptrInt(30)},
		"nothing":    {TsUnix: 1791333981},
	} {
		s := collectFan(root, sample)
		require.NotNil(t, s, name)
		assert.True(t, s.RPMEstFlag,
			"%s: rpm_est_is_estimate must be emitted on every sample, so a present "+
				"rpm_est is never readable as a tachometer reading", name)
	}
}

// ---------------------------------------------------------------------------
// gov
// ---------------------------------------------------------------------------

// TestCollectFanReadsGovVerbatim passes every value the live table has ever held,
// including "manual" -- a 1-row value that a hand-written enum copied from the
// brief's four-value list would drop.
func TestCollectFanReadsGovVerbatim(t *testing.T) {
	root := fanFPSRoot(t)
	for _, gov := range []string{"curve", "free", "s0i3", "quiet", "manual"} {
		s := collectFan(root, &FanSample{TsUnix: 1791333981, Gov: gov})
		require.NotNil(t, s)
		assert.Equal(t, gov, s.Gov)
	}
}

func TestCollectFanEmptyGovIsAbsent(t *testing.T) {
	s := collectFan(fanFPSRoot(t), &FanSample{TsUnix: 1791333981})
	require.NotNil(t, s)
	assert.Empty(t, s.Gov)
	b, err := json.Marshal(s)
	require.NoError(t, err)
	assert.NotContains(t, string(b), `"gov"`, "a NULL gov must be absent, not an empty string")
}

// ---------------------------------------------------------------------------
// Wire shape
// ---------------------------------------------------------------------------

// TestFanStatsWireDistinguishesUnknownZeroAndValue is the wire-level version of the
// three-state contract, because the pointer split only exists to survive JSON.
func TestFanStatsWireDistinguishesUnknownZeroAndValue(t *testing.T) {
	root := fanFPSRoot(t)
	for _, tc := range []struct {
		name       string
		sample     *FanSample
		wantLevel  bool
		wantLevelV int
	}{
		{"unknown", &FanSample{TsUnix: 1791333981, EC76Pct: ptrInt(30), RPM: ptrInt(3800)}, false, 0},
		{"zero", &FanSample{TsUnix: 1791333981, LevelPct: ptrInt(0), EC76Pct: ptrInt(0)}, true, 0},
		{"value", &FanSample{TsUnix: 1791333981, LevelPct: ptrInt(55), EC76Pct: ptrInt(60), RPM: ptrInt(6300)}, true, 55},
	} {
		s := collectFan(root, tc.sample)
		require.NotNil(t, s, tc.name)
		b, err := json.Marshal(s)
		require.NoError(t, err, tc.name)
		str := string(b)
		if tc.wantLevel {
			assert.Contains(t, str, `"set_level_pct":`+strconv.Itoa(tc.wantLevelV), tc.name)
		} else {
			assert.NotContains(t, str, `"set_level_pct"`, tc.name)
		}
		assert.Contains(t, str, `"sample_ts_s":1791333981`, tc.name)
	}
}

// ---------------------------------------------------------------------------
// estRPM edge cases the brief's three-step table cannot express
// ---------------------------------------------------------------------------

func TestEstRPMHandlesSingleStepAndDuplicatePercents(t *testing.T) {
	assert.Equal(t, uint16(3200), estRPM(25, []FanSpeedStep{{Percent: 25, RPMEst: 3200}}))
	assert.Equal(t, uint16(3200), estRPM(25, []FanSpeedStep{{Percent: 25, RPMEst: 3200}, {Percent: 25, RPMEst: 9999}}),
		"a zero-width span must return the lower step, not divide by zero")
}

func TestEstRPMDoesNotMutateItsInput(t *testing.T) {
	steps := append([]FanSpeedStep(nil), fpsFixture...)
	estRPM(60, steps)
	assert.Equal(t, fpsFixture, steps, "sorting must not reorder the caller's slice")
}
