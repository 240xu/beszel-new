//go:build testing

package plus

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The bounds are stated behaviourally here on purpose: a test comparing a field
// against the constant that bounds it would pass when the bound moves. Each test
// sweeps a field and pins the set of values that survive, which is the claim.

// accepted reports which values of one field this validator admits.
func accepted(t *testing.T, lo, hi int, mutate func(*Curve, int)) map[int]bool {
	t.Helper()
	base, err := ParseCurve(golden(t, "pfand.conf"))
	require.NoError(t, err)
	out := make(map[int]bool, hi-lo+1)
	for v := lo; v <= hi; v++ {
		c := *base
		mutate(&c, v)
		if c.Validate() == nil {
			out[v] = true
		}
	}
	return out
}

func highest(got map[int]bool, lo, hi int) int {
	high := -1
	for v := lo; v <= hi; v++ {
		if got[v] {
			high = v
		}
	}
	return high
}

func lowest(got map[int]bool, lo, hi int) int {
	for v := lo; v <= hi; v++ {
		if got[v] {
			return v
		}
	}
	return -1
}

// TestACurveThatCanNeverAskForMoreCoolingIsRefused is the audit's curve intact: an
// emergency edge no temperature can reach, and every band on the same duty. Nothing
// in it contradicts anything else, which is why only an absolute bound refuses it.
func TestACurveThatCanNeverAskForMoreCoolingIsRefused(t *testing.T) {
	c, err := ParseCurve(golden(t, "pfand.conf"))
	require.NoError(t, err)
	c.Emerg = 2000
	c.E = 30
	c.Cap = 25
	for _, l := range []*int{&c.L0, &c.L1, &c.L2, &c.L3, &c.L4, &c.L5} {
		*l = 25
	}
	assert.Error(t, c.Validate(),
		"an emergency edge above any temperature the host can reach disarms the "+
			"guard's own over-temperature interlock, and every band sits at one duty")
}

// TestTheEmergencyEdgeStopsAtTheTemperatureTheHostCanSurvive pins the ceiling.
// Above it the platform shuts the core down on its own schedule, so an edge beyond
// it is not a late emergency -- it is an unreachable one.
func TestTheEmergencyEdgeStopsAtTheTemperatureTheHostCanSurvive(t *testing.T) {
	got := accepted(t, 80, 260, func(c *Curve, v int) { c.Emerg = v })
	assert.Equal(t, 100, highest(got, 80, 260),
		"the emergency edge is a temperature; past the host's own thermal ceiling "+
			"the fan curve has no authority left to lose")
	assert.False(t, got[101],
		"one past the ceiling is still past it, so the boundary is not off by a step")
}

// TestTheEmergencyEdgeIsReachable pins the other side of the same bound. A low edge
// is not a safety problem -- it pins the fan at maximum -- but it must still clear
// the rest of the ladder, which the sweep above cannot show.
func TestTheEmergencyEdgeIsReachable(t *testing.T) {
	c, err := ParseCurve(golden(t, "pfand.conf"))
	require.NoError(t, err)
	assert.Less(t, c.EmergRel, c.Emerg)
	assert.Less(t, c.T5, c.Emerg)
	assert.Less(t, c.Idle, c.T5)
	assert.Greater(t, c.Emerg, c.Idle)
}

// TestTheCurveThisHostRunsStillPasses guards the direction a false rejection takes:
// nothing is written and the operator is left guessing. The shipping file is the
// only evidence of what is legal.
func TestTheCurveThisHostRunsStillPasses(t *testing.T) {
	c, err := ParseCurve(golden(t, "pfand.conf"))
	require.NoError(t, err)
	assert.NoError(t, c.Validate(), "the bounds must admit the curve in the field")
}

// TestTheEmergencyDutyMustClearTheHardCap pins the direction against the daemon,
// which refuses an emergency duty that is not strictly above the ceiling. Equal is
// the case that matters: the panel would take it and the daemon would fail safe to
// full speed and exit, leaving no control loop at all.
func TestTheEmergencyDutyMustClearTheHardCap(t *testing.T) {
	base, err := ParseCurve(golden(t, "pfand.conf"))
	require.NoError(t, err)
	c := *base
	c.E = c.Cap
	assert.Error(t, c.Validate(),
		"an emergency duty equal to the ceiling is not an emergency, and the daemon refuses it")
	c.E = c.Cap + 1
	assert.NoError(t, c.Validate(), "one above the ceiling is the narrowest emergency that still clears it")
}

// TestTheEmergencyDutyStaysOnTheFanScale pins the top of the duty scale: above full
// speed is not a stronger fan, it is an uncommandable one.
func TestTheEmergencyDutyStaysOnTheFanScale(t *testing.T) {
	got := accepted(t, 95, 400, func(c *Curve, v int) { c.E = v })
	assert.Equal(t, 100, highest(got, 95, 400),
		"100% is the whole range of the control the fan exposes")
}

// TestTheHardCeilingStaysOnTheFanScale pins the same scale on the cap, as a pair
// of points: its ceiling is currently an accident of the emergency duty, not a
// bound of its own.
func TestTheHardCeilingStaysOnTheFanScale(t *testing.T) {
	got := accepted(t, 70, 400, func(c *Curve, v int) { c.Cap = v })
	assert.True(t, got[99],
		"the ceiling may reach just under the whole scale, which is all the "+
			"emergency duty above it leaves room for")
	assert.False(t, got[101],
		"the cap is a duty; above full speed it is not a stronger ceiling, it is "+
			"an uncommandable one")
}

// TestTheDebounceTuningsCannotBeNegative is the parse-layer agreement from the
// validation side: a negative survives here and is then refused on the way to
// disk, which the write path charges to the breaker.
func TestTheDebounceTuningsCannotBeNegative(t *testing.T) {
	for _, tc := range []struct {
		name string
		mut  func(*Curve)
	}{
		{"HYST", func(c *Curve) { c.Hyst = -5 }},
		{"DWELL", func(c *Curve) { c.Dwell = -1 }},
		{"DEAD", func(c *Curve) { c.Dead = -1 }},
		{"IV", func(c *Curve) { c.IV = -1 }},
		{"EMERG_REL", func(c *Curve) { c.EmergRel = -1 }},
		{"IDLE", func(c *Curve) { c.Idle = -1 }},
		{"CAP", func(c *Curve) { c.Cap = -1 }},
		{"E", func(c *Curve) { c.E = -1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base, err := ParseCurve(golden(t, "pfand.conf"))
			require.NoError(t, err)
			c := *base
			tc.mut(&c)
			assert.Error(t, c.Validate(),
				"a value the parser cannot even read must not be called valid")
		})
	}
}

// TestTheOutputDeadBandCannotSwallowABandChange pins the one tuning whose failure
// is not loud: the daemon writes a duty only when the change clears the band, so a
// band covering the gap between two duties leaves the fan wherever it was.
func TestTheOutputDeadBandCannotSwallowABandChange(t *testing.T) {
	got := accepted(t, 0, 40, func(c *Curve, v int) { c.Dead = v })
	assert.Equal(t, 5, highest(got, 0, 40),
		"the widest dead band is the narrowest step the duty ladder can express; "+
			"wider than that and the loop stops correcting")
}

// TestTheSampleIntervalCannotOutliveTheEmergencyItExistsToCatch pins the other quiet
// failure: the daemon re-reads the temperature once per interval, so a long interval
// is a long stretch of stale duty past the edge.
func TestTheSampleIntervalCannotOutliveTheEmergencyItExistsToCatch(t *testing.T) {
	// Dwell rides along: the residence time must clear the sample interval, so
	// holding it at the shipped value would mask this bound with that one.
	got := accepted(t, 2, 600, func(c *Curve, v int) { c.IV, c.Dwell = v, v })
	assert.Equal(t, 60, highest(got, 2, 600),
		"long enough that the emergency branch is polled about once a minute")
}

// TestEveryCurveValidateAcceptsRoundTripsThroughTheParser is the agreement the
// write path depends on. Validation and parsing keep separate value domains only
// until one accepts what the other refuses, and the seam between them is the
// write: a curve cleared by the first and dropped by the second is charged to
// the breaker as though the device had failed.
func TestEveryCurveValidateAcceptsRoundTripsThroughTheParser(t *testing.T) {
	base, err := ParseCurve(golden(t, "pfand.conf"))
	require.NoError(t, err)
	fields := []struct {
		name string
		mut  func(*Curve, int)
	}{
		{"IDLE", func(c *Curve, v int) { c.Idle = v }},
		{"T1", func(c *Curve, v int) { c.T1 = v }},
		{"T2", func(c *Curve, v int) { c.T2 = v }},
		{"T3", func(c *Curve, v int) { c.T3 = v }},
		{"T4", func(c *Curve, v int) { c.T4 = v }},
		{"T5", func(c *Curve, v int) { c.T5 = v }},
		{"EMERG", func(c *Curve, v int) { c.Emerg = v }},
		{"EMERG_REL", func(c *Curve, v int) { c.EmergRel = v }},
		{"E", func(c *Curve, v int) { c.E = v }},
		{"CAP", func(c *Curve, v int) { c.Cap = v }},
		{"HYST", func(c *Curve, v int) { c.Hyst = v }},
		{"DWELL", func(c *Curve, v int) { c.Dwell = v }},
		{"DEAD", func(c *Curve, v int) { c.Dead = v }},
		{"IV", func(c *Curve, v int) { c.IV = v }},
		{"L0", func(c *Curve, v int) { c.L0 = v }},
		{"L1", func(c *Curve, v int) { c.L1 = v }},
		{"L2", func(c *Curve, v int) { c.L2 = v }},
		{"L3", func(c *Curve, v int) { c.L3 = v }},
		{"L4", func(c *Curve, v int) { c.L4 = v }},
		{"L5", func(c *Curve, v int) { c.L5 = v }},
	}
	for _, f := range fields {
		t.Run(f.name, func(t *testing.T) {
			for v := -5; v <= 130; v++ {
				c := *base
				f.mut(&c, v)
				if c.Validate() != nil {
					continue
				}
				_, perr := ParseCurve(c.Render())
				assert.NoErrorf(t, perr,
					"%s=%d was called valid and then refused on the way to disk", f.name, v)
			}
		})
	}
}

// The one tuning the daemon gates downshifts on: it never gates an upshift. A
// hysteresis that reaches the emergency edge removes the only test that lets the
// fan leave the emergency duty, so the curve drives to E and stops there forever.
// The bound is a relationship, so it is checked as one -- moving the edge has to
// move the widest survivable hysteresis with it, which a constant could not.
func TestTheHysteresisCannotSwallowTheWayOutOfEmergency(t *testing.T) {
	for _, edge := range []int{60, 80, 95} {
		base, err := ParseCurve(golden(t, "pfand.conf"))
		require.NoError(t, err)
		// Squeeze the ladder under the edge under test; the shipped spacing only
		// fits the shipped edge.
		edges := []*int{&base.Idle, &base.T1, &base.T2, &base.T3, &base.T4, &base.T5, &base.EmergRel}
		for i, e := range edges {
			*e = edge - (len(edges)-i)*3
		}
		base.Emerg = edge
		require.NoErrorf(t, base.Validate(),
			"the curve the sweep runs against has to be one this validator admits at all")

		keep := make(map[int]bool, 201)
		for v := 0; v <= 200; v++ {
			c := *base
			c.Hyst = v
			keep[v] = c.Validate() == nil
		}

		assert.Equalf(t, edge-1, highest(keep, 0, 200),
			"at edge %d, one below is the widest hysteresis that still leaves a way out", edge)
		assert.Falsef(t, keep[edge],
			"at edge %d a hysteresis at the edge leaves the emergency duty with no exit", edge)
	}
}
