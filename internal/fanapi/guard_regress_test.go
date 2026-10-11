//go:build testing

package fanapi

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A gate that declined is not the last word. The regress has exactly one caller
// and it used to be a one-shot timer, so a duty declined at a gate stayed held
// for the life of the agent -- the fan on a hand-set duty, and an operator with
// nothing on screen to tell them so.
func TestADeclinedRegressIsLookedAtAgain(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*fixture)
	}{
		{"the sensor cannot be read", func(f *fixture) { f.tempErr = errors.New("no such device") }},
		{"the package is at the emergency edge", func(f *fixture) { f.temp = 85 }},
		{"the device refuses the write", func(f *fixture) { f.setErr = errors.New("device busy") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			_, err := f.g.SetManualLevel(70, confirmed)
			require.NoError(t, err)
			initial := len(f.waits)

			f.advance(10 * time.Minute)
			tc.setup(f)
			did, err := f.g.Regress()
			if tc.name == "the device refuses the write" {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			assert.False(t, did, "nothing was handed back")
			assert.Greater(t, len(f.waits), initial,
				"a regress that stopped short has to be looked at again; nothing else re-arms it")
			assert.False(t, f.g.PendingRegressAt().IsZero(),
				"the duty is still held, so it is still due back on the curve")
		})
	}
}

// The retry cannot be immediate. A package sitting at the emergency edge, or a
// sensor that will not read, would otherwise turn the retry into a loop against
// the control device.
func TestTheRegressRetryWaitsAndThenWaitsLonger(t *testing.T) {
	f := newFixture(t)
	_, err := f.g.SetManualLevel(70, confirmed)
	require.NoError(t, err)
	f.advance(10 * time.Minute)
	f.tempErr = errors.New("no such device")

	var seen []time.Duration
	for i := 0; i < 4; i++ {
		before := len(f.waits)
		_, err := f.g.Regress()
		require.NoError(t, err)
		seen = append(seen, f.waits[before])
		f.advance(f.waits[before])
	}

	for i, d := range seen {
		assert.Positive(t, d, "retry %d must not be immediate", i)
	}
	for i := 1; i < len(seen); i++ {
		assert.GreaterOrEqualf(t, seen[i], seen[i-1],
			"retry %d backs off rather than hammering the control path", i)
	}
	assert.Greater(t, seen[len(seen)-1], seen[0],
		"a gate that keeps declining has to stop being retried at a fixed rate")
}

// At or past the emergency edge the curve's own answer is its emergency duty.
// The premise this gate used to be justified on -- that the daemon is driving
// full speed -- is false by the time it is reached, because the daemon check
// above returns first. So with no daemon running, declining here leaves the fan
// on whatever duty an operator set once, which may be the minimum, at the one
// temperature the curve says it must not be.
func TestAtTheEmergencyEdgeTheFanIsNotLeftOnTheDutyAnOperatorSet(t *testing.T) {
	f := newFixture(t)
	lowest, err := f.g.SetManualLevel(25, confirmed)
	require.NoError(t, err)
	f.advance(10 * time.Minute)

	f.temp = 85 // at or past the emergency edge in the golden curve
	did, rerr := f.g.Regress()
	require.NoError(t, rerr, "holding the emergency duty is not a failure")
	assert.False(t, did, "control is not handed back while the edge holds")

	c := mustCurve(t)
	assert.NotEmpty(t, f.writeCalls)
	assert.Equal(t, c.E, f.writeCalls[len(f.writeCalls)-1],
		"the duty the curve itself commands at this temperature, not the one an operator picked")
	assert.Greater(t, c.E, lowest.Level,
		"and that has to be more cooling than the duty that was held")

	assert.False(t, f.g.PendingRegressAt().IsZero(),
		"the handover still has to happen once the edge clears")
}

// A regress that completed must not leave a retry behind, or the fan would be
// handed over again and again after the manual duty is long gone.
func TestACompletedRegressSchedulesNothingFurther(t *testing.T) {
	f := newFixture(t)
	_, err := f.g.SetManualLevel(70, confirmed)
	require.NoError(t, err)
	f.advance(10 * time.Minute)

	before := len(f.waits)
	did, err := f.g.Regress()
	require.NoError(t, err)
	assert.True(t, did)
	assert.Equal(t, before, len(f.waits), "there is nothing left to retry")
	assert.True(t, f.g.PendingRegressAt().IsZero())
}

// The daemon taking the fan back is a handover that has happened, not a gate
// that declined: there is nothing to retry, because nothing is held.
func TestADaemonThatTakesOverEndsTheRegress(t *testing.T) {
	f := newFixture(t)
	_, err := f.g.SetManualLevel(70, confirmed)
	require.NoError(t, err)
	f.advance(10 * time.Minute)
	f.pfand = true

	before := len(f.waits)
	did, err := f.g.Regress()
	require.NoError(t, err)
	assert.False(t, did)
	assert.Equal(t, before, len(f.waits))
	assert.True(t, f.g.PendingRegressAt().IsZero(),
		"the daemon owns the fan, so there is no pending handover left to promise")
}
