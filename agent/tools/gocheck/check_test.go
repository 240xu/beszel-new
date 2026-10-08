//go:build testing

package main

import (
	"strings"
	"testing"

	"github.com/henrygd/beszel/agent/plus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func intPtr(v int) *int { return &v }

const (
	nowS = int64(1791334100)
	tsS  = int64(1791334060) // 40 s before nowS, inside the default limit
)

// A healthy sample must produce no finding at all, otherwise every other case in
// this file is asserting against a baseline that is itself broken.
func TestCheckFanAcceptsAFreshLabelledSample(t *testing.T) {
	v := checkFan(&plus.FanStats{
		RPMEstFlag: true, RPMEst: 3200, SampleTsS: tsS, Gov: "curve",
	}, nowS, defaultMaxAgeS)

	require.True(t, v.OK(), "a fresh labelled sample must pass: %v", v.Failures)
	assert.True(t, v.Present)
	assert.Equal(t, int64(40), v.AgeS)
	assert.False(t, v.Stale)
	assert.True(t, v.Flagged)
	assert.NotContains(t, v.describe("/sys"), "STALE")
}

// TestCheckFanRejectsAnUnlabelledEstimate is the whole reason the marker is on the
// wire. rpm_est with no rpm_est_is_estimate is a number a consumer will draw as a
// tachometer reading, and nothing downstream can repair that.
func TestCheckFanRejectsAnUnlabelledEstimate(t *testing.T) {
	v := checkFan(&plus.FanStats{
		RPMEstFlag: false, RPMEst: 3200, SampleTsS: tsS,
	}, nowS, defaultMaxAgeS)

	assert.False(t, v.OK(), "an estimate without its label must be refused")
	require.Len(t, v.Failures, 1)
	assert.Contains(t, v.Failures[0], "rpm_est_is_estimate")
	assert.Contains(t, v.Failures[0], "3200")
}

// TestCheckFanRejectsAStaleSample is the gate the collector deliberately does not
// perform. A row that stopped being written looks identical to a live one on the
// wire until somebody compares sample_ts_s against a clock.
func TestCheckFanRejectsAStaleSample(t *testing.T) {
	v := checkFan(&plus.FanStats{
		RPMEstFlag: true, RPMEst: 3200, SampleTsS: nowS - 3600,
	}, nowS, defaultMaxAgeS)

	assert.False(t, v.OK(), "an hour-old sample must be refused")
	assert.True(t, v.Stale)
	require.Len(t, v.Failures, 1)
	assert.Contains(t, v.Failures[0], "stale")
	assert.Contains(t, v.describe("/sys"), "STALE")
}

// TestCheckFanStaleBoundaryIsExact pins both sides of the comparison, because an
// off-by-one here is invisible in a live run: ages move in whole seconds and the
// limit is a minute wide.
func TestCheckFanStaleBoundaryIsExact(t *testing.T) {
	cases := []struct {
		name      string
		age       int64
		wantStale bool
	}{
		{"one second under", defaultMaxAgeS - 1, false},
		{"exactly at the limit", defaultMaxAgeS, false},
		{"one second over", defaultMaxAgeS + 1, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := checkFan(&plus.FanStats{
				RPMEstFlag: true, RPMEst: 3200, SampleTsS: nowS - tc.age,
			}, nowS, defaultMaxAgeS)
			assert.Equal(t, tc.wantStale, v.Stale,
				"age %d against a limit of %d", tc.age, defaultMaxAgeS)
			assert.Equal(t, !tc.wantStale, v.OK())
		})
	}
}

// TestCheckFanRejectsZeroAndFutureTimestamps covers the two timestamps that would
// otherwise pass an age check for the wrong reason: a zero reads as "very old",
// which is refused, but for the right reason only if the reason is named; and a
// timestamp ahead of the clock yields a negative age, which no threshold catches.
func TestCheckFanRejectsZeroAndFutureTimestamps(t *testing.T) {
	zero := checkFan(&plus.FanStats{RPMEstFlag: true, SampleTsS: 0}, nowS, defaultMaxAgeS)
	assert.False(t, zero.OK(), "no timestamp must be refused")
	require.Len(t, zero.Failures, 1)
	assert.Contains(t, zero.Failures[0], "sample_ts_s must be present")
	assert.False(t, zero.Stale, "a zero timestamp is missing, not merely old")

	future := checkFan(&plus.FanStats{
		RPMEstFlag: true, SampleTsS: nowS + 120,
	}, nowS, defaultMaxAgeS)
	assert.False(t, future.OK(), "a future timestamp must be refused")
	require.Len(t, future.Failures, 1)
	assert.Contains(t, future.Failures[0], "future")
	assert.False(t, future.Stale)
}

// TestCheckFanReportsEveryFailureAtOnce keeps a broken sample from being reported
// one rule at a time across successive runs.
func TestCheckFanReportsEveryFailureAtOnce(t *testing.T) {
	v := checkFan(&plus.FanStats{
		RPMEstFlag: false, RPMEst: 3200, SampleTsS: nowS - 3600,
	}, nowS, defaultMaxAgeS)

	assert.Len(t, v.Failures, 2, "stale and unlabelled must be reported together")
	assert.Contains(t, v.describe("/sys"), "stale")
	assert.Contains(t, v.describe("/sys"), "rpm_est_is_estimate")
	assert.Equal(t, 2, strings.Count(v.describe("/sys"), "FAIL"))
}

// TestCheckFanReportsAbsentRatherThanPassing is the fail-closed half: on this
// platform pfanel is installed, so a missing row is a finding. A tool that printed
// nothing and exited 0 would hide exactly the failure it exists to catch.
func TestCheckFanReportsAbsentRatherThanPassing(t *testing.T) {
	v := checkFan(nil, nowS, defaultMaxAgeS)

	assert.False(t, v.Present)
	assert.False(t, v.OK())
	require.Len(t, v.Failures, 1)
	assert.Contains(t, v.Failures[0], "no fan sample")
	assert.Contains(t, v.describe("/sys"), "no fan sample")
}

// TestCheckFanCarriesDutyAndLevelThrough keeps the two pointer fields readable, so
// the verdict is something a human can act on rather than a bare age. A nil duty
// must print as absent rather than dereference.
func TestCheckFanCarriesDutyAndLevelThrough(t *testing.T) {
	v := checkFan(&plus.FanStats{
		RPMEstFlag: true, RPMEst: 3200, SampleTsS: tsS,
		Gov: "curve", EC76Pct: intPtr(61), SetLevelPct: intPtr(0),
	}, nowS, defaultMaxAgeS)

	d := v.describe("/sys")
	assert.Contains(t, d, "gov=curve")
	assert.Contains(t, d, "ec76_pct=61")
	// set_level_pct 0 is a recorded value, not a missing one, so it must appear.
	assert.Contains(t, d, "set_level_pct=0")

	unknown := checkFan(&plus.FanStats{RPMEstFlag: true, SampleTsS: tsS}, nowS, defaultMaxAgeS)
	assert.NotContains(t, unknown.describe("/sys"), "ec76_pct")
	assert.NotContains(t, unknown.describe("/sys"), "gov=")
}
