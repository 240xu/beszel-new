//go:build testing

package fanapi

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A gate that declined is not the last word. The regress has one caller and it
// used to be a one-shot timer, so a duty declined at a gate stayed held for the
// life of the agent, with nothing on screen to say so.
func TestADeclinedRegressIsLookedAtAgain(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*fixture)
	}{
		{"the sensor cannot be read", func(f *fixture) { f.tempErr = errors.New("no such device") }},
		{"the package is at the emergency edge", func(f *fixture) { f.temp = 85 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			_, err := f.g.SetManualLevel(70, confirmed)
			require.NoError(t, err)
			initial := len(f.waits)

			f.advance(10 * time.Minute)
			tc.setup(f)
			did, err := f.g.Regress()
			require.NoError(t, err)
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

	// Enough rounds for the doubling to reach its ceiling, so the plateau is
	// observed rather than assumed: a backoff that only ever grows has not been
	// shown to stop growing.
	var seen []time.Duration
	for i := 0; i < 8; i++ {
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

	// Both ends, because either alone is satisfiable by a backoff that never had
	// them: a wait short enough to be a busy loop still grows, and one that grows
	// without a ceiling still starts out reasonable. One second is the floor: every
	// attempt takes the control lock, reads a sensor file and may write the fan.
	assert.GreaterOrEqual(t, seen[0], time.Second,
		"the first retry has to be long enough not to be a loop")
	plateau := false
	for i := 1; i < len(seen); i++ {
		if seen[i] == seen[i-1] {
			plateau = true
		}
	}
	assert.True(t, plateau,
		"the wait has to stop growing; a retry that doubles forever is a duty held "+
			"for good wearing the name of a retry")

	// The ceiling itself. The loop already stops doubling once the wait is long, so
	// growth stops either way and a plateau alone cannot tell a capped backoff from
	// an uncapped one -- only the value can. Five minutes is the point past which the
	// wait stops being a retry, written out here rather than read back from the
	// constant so the number in the policy is the number under test.
	assert.LessOrEqual(t, seen[len(seen)-1], 5*time.Minute,
		"the wait stops at a ceiling instead of doubling until it stops happening")
}

// At or past the emergency edge the curve's own answer is its emergency duty. The
// premise this gate used to be justified on -- that the daemon is driving full speed
// -- is false here, because the daemon check above returns first. Declining left the
// fan on a duty an operator set once, at the temperature the curve forbids.
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

// The duty stays held, the refusal is on record, and the attempt is still looked
// at again. The last part is what a held duty depends on: the emergency interlock
// runs inside every regress attempt, so a schedule that stopped would leave a
// manual duty cooling the machine on its own.
func TestAHeldDutyIsRefusedAuditedAndWatched(t *testing.T) {
	f := newFixture(t)
	_, err := f.g.SetManualLevel(70, confirmed)
	require.NoError(t, err)
	f.advance(10 * time.Minute)

	before := len(f.waits)
	did, err := f.g.Regress()
	require.NoError(t, err, "declining to invent a handover is not a failure")
	assert.False(t, did)
	assert.Greater(t, len(f.waits), before,
		"a held duty is still watched, so the emergency interlock keeps running")

	assert.False(t, f.g.PendingRegressAt().IsZero(),
		"the duty is still held, so it is still due back on the curve")

	require.NotEmpty(t, f.events)
	last := f.events[len(f.events)-1]
	assert.Equal(t, OpRegress, last.Op)
	assert.Equal(t, OutcomeRefused, last.Outcome)
	assert.Equal(t, DeclineNoHandover, last.Code)
	assert.NotEmpty(t, last.Message,
		"an operator told only that it was refused cannot tell what to do instead")
}

// The guard writes duties and this firmware turns every argument into an EC
// fan-duty byte, so a zero write is a request for zero percent rather than a
// handover. A timer that issued one unattended is the whole failure: the guard
// cannot read the EC curve setting that decides whether the fan keeps turning on
// that byte, so there is no safe value to write here.
func TestTheRegressNeverAsksForTheZeroDuty(t *testing.T) {
	f := newFixture(t)
	_, err := f.g.SetManualLevel(70, confirmed)
	require.NoError(t, err)
	f.advance(10 * time.Minute)

	// Repeated, because the retry is what would eventually have issued the write.
	for i := 0; i < 4; i++ {
		_, err := f.g.Regress()
		require.NoError(t, err)
		f.advance(5 * time.Minute)
	}
	for _, lv := range f.writeCalls {
		assert.NotEqual(t, 0, lv,
			"the guard must never write the zero duty on this firmware")
	}
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

// The emergency hold writes the curve's own duty and proves it landed. It is not a
// refusal and it is not a handover, so an audit that says "refused, level 0" hides
// the duty now actually driving the fan from the operator reading the trail: R6
// caught a real probe that recorded a device write while the trail said nothing was
// written. The event must read as the write it is, at the level that was written.
func TestTheEmergencyHoldIsAuditedAsHeldAtTheDutyItWrote(t *testing.T) {
	f := newFixture(t)
	_, err := f.g.SetManualLevel(70, confirmed)
	require.NoError(t, err)
	f.advance(10 * time.Minute)
	f.temp = 85

	did, rerr := f.g.Regress()
	require.NoError(t, rerr, "holding the emergency duty is not a failure")
	require.False(t, did, "the hold is a duty, not a handover back to the curve")

	c := mustCurve(t)
	require.NotEmpty(t, f.writeCalls, "the emergency duty reached the device")
	written := f.writeCalls[len(f.writeCalls)-1]
	assert.Equal(t, c.E, written, "the regress wrote the curve's own emergency duty")

	last := f.lastEvent()
	assert.Equal(t, "held", string(last.Outcome),
		"a duty that was written is not a refusal, and an applied outcome would claim a handover that did not happen")
	assert.NotEqual(t, OutcomeRefused, last.Outcome, "refused at level 0 hid the write from the operator")
	assert.NotEqual(t, OutcomeApplied, last.Outcome, "the fan did not go back to its curve, so the hold is not applied")
	assert.Equal(t, written, last.Level, "the audit names the duty actually in force, not a zero that reads as auto")
	assert.Equal(t, DeclineAtEmergency, last.Code)
}

// Close is final. The floor has floorOn to stop a tick from resurrecting it after
// Close; the regress had nothing, so a callback already in the air when Close ran
// would rebuild the timer through the retry path and put the guard back on watch
// after it promised to stop.
func TestClosingTheGuardAlsoEndsTheRegress(t *testing.T) {
	f := newFixture(t)
	_, err := f.g.SetManualLevel(70, confirmed)
	require.NoError(t, err)
	require.False(t, f.g.PendingRegressAt().IsZero(), "precondition: a handover is pending")

	f.g.Close()
	assert.True(t, f.g.PendingRegressAt().IsZero(),
		"a closed guard has no handover left to promise")

	// The callback that was in the air when Close ran, fired late.
	f.advance(11 * time.Minute)
	waitsBefore := len(f.waits)
	writesBefore := len(f.writeCalls)
	did, err := f.g.Regress()
	require.NoError(t, err)
	assert.False(t, did)
	assert.Len(t, f.waits, waitsBefore,
		"a late callback must not rebuild the timer the Close stopped")
	assert.Len(t, f.writeCalls, writesBefore, "and must not reach the device")

	// A callback already past its deadline check when Close ran reaches the
	// retry path through code the entry check never sees, so that path is
	// pinned directly: it must be dead after Close too.
	f.g.retryLocked(f.now)
	assert.Len(t, f.waits, waitsBefore,
		"the retry path must not rebuild the timer after Close either")
}
