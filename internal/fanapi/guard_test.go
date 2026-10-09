package fanapi

import (
	"errors"
	"testing"
	"time"

	"github.com/henrygd/beszel/agent/plus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// goldenCurve is the curve this host runs: the six band duties the trim table
// can address, and an emergency edge that coincides with the write guard.
const goldenCurve = `IDLE=50
L0=25
T1=56
L1=30
T2=62
L2=40
T3=67
L3=50
T4=72
L4=60
T5=79
L5=70
EMERG=80
E=100
EMERG_REL=75
CAP=70
TRIM=6
GAIN=1
HYST=3
DWELL=30
DEAD=2
IV=10
`

var confirmed = Request{Confirm: true, Actor: "tester"}

type fixture struct {
	t   *testing.T
	g   *Guard
	now time.Time

	temp    int
	tempErr error

	level  int
	setErr error
	// setErrAfter is the write count at which injected failures start, so a test
	// can fail the restore without also failing the write it is restoring.
	setErrAfter int
	getErr      error
	// loseRaceTo models the single-slot device being claimed by another writer
	// between our write and our read-back: the write is accepted and lost anyway.
	loseRaceTo *int

	pfand bool

	curveBytes []byte
	curveErr   error
	writeErr   error
	// corruptAfterWrite lets a write land and then come back wrong, which is the
	// only way to reach the restore branch of a curve edit.
	corruptAfterWrite bool

	writeCalls []int
	events     []Event
}

func newFixture(t *testing.T) *fixture {
	f := &fixture{t: t, now: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC), temp: 46}
	c, err := plus.ParseCurve(goldenCurve)
	require.NoError(t, err, "the golden curve must parse")
	require.Equal(t, 80, c.Emerg, "the write guard is anchored on this edge")
	f.curveBytes = []byte(goldenCurve)

	f.g = New(Deps{
		Now:       func() time.Time { return f.now },
		ReadTempC: func() (int, error) { return f.temp, f.tempErr },
		ReadCurve: func() (*plus.Curve, error) {
			if f.curveErr != nil {
				return nil, f.curveErr
			}
			return plus.ParseCurve(string(f.curveBytes))
		},
		SetLevel: f.setLevel,
		GetLevel: f.getLevel,
		PfandUp:  func() bool { return f.pfand },
		ReadCurveBytes: func() ([]byte, error) {
			if f.curveErr != nil {
				return nil, f.curveErr
			}
			return append([]byte(nil), f.curveBytes...), nil
		},
		WriteCurveBytes: func(b []byte) error {
			if f.writeErr != nil {
				return f.writeErr
			}
			f.curveBytes = append([]byte(nil), b...)
			if f.corruptAfterWrite {
				// The filesystem goes away between the write and the read-back, so
				// both the verification and the restore have to cope with it.
				f.curveErr = errors.New("device or resource busy")
				f.writeErr = errors.New("device or resource busy")
			}
			return nil
		},
		Audit: func(e Event) { f.events = append(f.events, e) },
	})
	return f
}

func (f *fixture) setLevel(lv int) error {
	f.writeCalls = append(f.writeCalls, lv)
	if f.setErr != nil && len(f.writeCalls) >= f.setErrAfter {
		return f.setErr
	}
	f.level = lv
	if f.loseRaceTo != nil {
		f.level = *f.loseRaceTo
	}
	return nil
}

func (f *fixture) getLevel() (int, error) {
	if f.getErr != nil {
		return 0, f.getErr
	}
	return f.level, nil
}

func (f *fixture) advance(d time.Duration) { f.now = f.now.Add(d) }

// ---- confirm ----

func TestSetManualLevelRefusesWithoutConfirm(t *testing.T) {
	f := newFixture(t)
	_, err := f.g.SetManualLevel(70, Request{Actor: "tester"})
	require.Error(t, err, "an unconfirmed write is not a write")
	assert.ErrorIs(t, err, ErrNotConfirmed)
	assert.Empty(t, f.writeCalls, "the device must not be touched before confirmation")
	assert.Len(t, f.events, 1, "a refusal is auditable")
	assert.Equal(t, OutcomeRefused, f.events[0].Outcome)
}

func TestSetManualLevelRejectsDutyOutsideTheWhitelist(t *testing.T) {
	f := newFixture(t)
	_, err := f.g.SetManualLevel(73, confirmed)
	assert.ErrorIs(t, err, plus.ErrFanLevelNotAllowed)
	assert.Empty(t, f.writeCalls)
}

// ---- package temperature ----

func TestSetManualLevelRefusesAtOrAboveTheEmergencyEdge(t *testing.T) {
	// The edge comes from the operator's own curve rather than a literal, so the
	// three cases straddle the curve this host actually runs.
	for _, tc := range []struct {
		name    string
		temp    int
		allowed bool
	}{{"below", 79, true}, {"at", 80, false}, {"above", 81, false}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.temp = tc.temp
			_, err := f.g.SetManualLevel(70, confirmed)
			if tc.allowed {
				require.NoError(t, err)
				assert.Equal(t, []int{70}, f.writeCalls)
				return
			}
			assert.ErrorIs(t, err, plus.ErrPackageTemperature,
				"a duty programmed where the daemon would already be at full speed is the write to refuse")
			assert.Empty(t, f.writeCalls, "a refused write must not reach the device")
		})
	}
}

func TestSetManualLevelRefusesWhenTemperatureIsUnreadable(t *testing.T) {
	f := newFixture(t)
	f.tempErr = errors.New("zone gone")
	_, err := f.g.SetManualLevel(70, confirmed)
	require.Error(t, err, "an unreadable package temperature is not a safe temperature")
	assert.ErrorIs(t, err, ErrTemperatureUnknown)
	assert.NotErrorIs(t, err, plus.ErrPackageTemperature,
		"an unreadable sensor is a different fact from a hot one, and Task 15 switches on the difference")
	assert.Empty(t, f.writeCalls)
}

func TestSetManualLevelRefusesWhenTheCurveIsUnreadable(t *testing.T) {
	f := newFixture(t)
	f.curveErr = errors.New("no such file")
	_, err := f.g.SetManualLevel(70, confirmed)
	require.Error(t, err, "without the curve there is no emergency edge to stay below")
	assert.ErrorIs(t, err, ErrTemperatureUnknown)
	assert.Empty(t, f.writeCalls)
}

// ---- arbitration ----

func TestManualLevelIsRefusedWhilePfandOwnsTheFan(t *testing.T) {
	f := newFixture(t)
	f.pfand = true
	_, err := f.g.SetManualLevel(70, confirmed)
	require.Error(t, err)
	assert.ErrorIs(t, err, plus.ErrPfandActive)
	assert.Empty(t, f.writeCalls, "pfand rewrites the duty every interval, so this level would be a lie")
}

func TestCurveEditStaysAvailableWhilePfandRuns(t *testing.T) {
	f := newFixture(t)
	f.pfand = true
	c := mustCurve(t)
	c.Cap = 70
	c.Trim = 8
	res, err := f.g.WriteCurve(c, confirmed)
	require.NoError(t, err, "the curve is how behaviour is changed under a running daemon")
	assert.True(t, res.NeedsPfandRestart,
		"the daemon sources its configuration once at start, so the panel must not report the change as live")
	assert.True(t, res.Applied)
}

// ---- circuit breaker ----

func TestBreakerOpensAfterConsecutiveFailures(t *testing.T) {
	f := newFixture(t)
	f.setErr = errors.New("device busy")
	for i := 0; i < breakerThreshold; i++ {
		_, err := f.g.SetManualLevel(70, confirmed)
		require.Error(t, err)
		require.NotErrorIs(t, err, ErrBreakerOpen, "the breaker has not opened yet on attempt %d", i+1)
	}
	callsBefore := len(f.writeCalls)
	_, err := f.g.SetManualLevel(70, confirmed)
	require.ErrorIs(t, err, ErrBreakerOpen)
	assert.Len(t, f.writeCalls, callsBefore, "an open breaker must not reach the device")
}

func TestBreakerClosesOnASuccessBeforeTheThreshold(t *testing.T) {
	f := newFixture(t)
	f.setErr = errors.New("device busy")
	_, err := f.g.SetManualLevel(70, confirmed)
	require.Error(t, err)
	f.setErr = nil
	_, err = f.g.SetManualLevel(70, confirmed)
	require.NoError(t, err)
	f.setErr = errors.New("device busy")
	for i := 0; i < breakerThreshold; i++ {
		_, err := f.g.SetManualLevel(70, confirmed)
		require.NotErrorIs(t, err, ErrBreakerOpen, "the run of failures restarted, so attempt %d still reaches the device", i+1)
	}
}

func TestBreakerStaysOpenUntilTheCooldownElapses(t *testing.T) {
	f := openBreaker(t)
	f.advance(breakerCooldown - time.Second)
	_, err := f.g.SetManualLevel(70, confirmed)
	assert.ErrorIs(t, err, ErrBreakerOpen, "half a cooldown is not a probe")
}

func TestBreakerRecoversThroughAProbe(t *testing.T) {
	f := openBreaker(t)
	f.advance(breakerCooldown)
	f.setErr = nil
	res, err := f.g.SetManualLevel(70, confirmed)
	require.NoError(t, err, "the probe confirms the device works again")
	assert.Equal(t, 70, res.Level)
	f.setErr = errors.New("device busy")
	for i := 0; i < breakerThreshold; i++ {
		_, err := f.g.SetManualLevel(70, confirmed)
		require.NotErrorIs(t, err, ErrBreakerOpen, "the breaker is closed again, so failure %d must reach the device", i+1)
	}
}

func TestAFailedProbeReopensTheBreaker(t *testing.T) {
	f := openBreaker(t)
	f.advance(breakerCooldown)
	_, err := f.g.SetManualLevel(70, confirmed)
	require.Error(t, err, "the device is still broken")
	require.NotErrorIs(t, err, ErrBreakerOpen, "the probe was admitted, it just failed")
	calls := len(f.writeCalls)
	_, err = f.g.SetManualLevel(70, confirmed)
	assert.ErrorIs(t, err, ErrBreakerOpen,
		"a probe that fails must put the breaker back open rather than leave it ready")
	assert.Len(t, f.writeCalls, calls, "the second call must not reach the device")
}

func TestGuardRejectionsDoNotTripTheBreaker(t *testing.T) {
	f := newFixture(t)
	for i := 0; i < breakerThreshold*3; i++ {
		f.temp = 80
		_, err := f.g.SetManualLevel(70, confirmed)
		require.ErrorIs(t, err, plus.ErrPackageTemperature)
	}
	f.temp = 46
	_, err := f.g.SetManualLevel(70, confirmed)
	assert.NoError(t, err, "a guard doing its job is not a broken control path")
}

func openBreaker(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	f.setErr = errors.New("device busy")
	for i := 0; i < breakerThreshold; i++ {
		_, err := f.g.SetManualLevel(70, confirmed)
		require.Error(t, err)
	}
	calls := len(f.writeCalls)
	_, err := f.g.SetManualLevel(70, confirmed)
	require.ErrorIs(t, err, ErrBreakerOpen)
	require.Len(t, f.writeCalls, calls)
	return f
}

// ---- verify, roll back, report ----

func TestALostRaceIsRolledBackAndReported(t *testing.T) {
	f := newFixture(t)
	f.level = 0
	other := 40
	f.loseRaceTo = &other
	_, err := f.g.SetManualLevel(70, confirmed)
	require.Error(t, err, "the level on the device is not the level that was asked for")
	assert.ErrorIs(t, err, ErrWriteUnconfirmed)
	assert.Equal(t, []int{70, 0}, f.writeCalls,
		"the pre-write level is restored rather than left at somebody else's duty")
}

func TestAFailedRollbackIsReportedRatherThanSwallowed(t *testing.T) {
	f := newFixture(t)
	f.level = 0
	other := 40
	f.loseRaceTo = &other
	f.setErr = errors.New("lock held")
	f.setErrAfter = 2 // the request lands, the restore does not
	_, err := f.g.SetManualLevel(70, confirmed)
	require.Error(t, err, "the restore failed, so the caller is left holding an unknown duty")
	assert.ErrorIs(t, err, ErrWriteUnconfirmed)
	assert.ErrorIs(t, err, ErrRollbackFailed)
}

func TestACurveWriteIsRolledBackWhenVerificationFails(t *testing.T) {
	f := newFixture(t)
	c := mustCurve(t)
	c.Trim = 9
	f.writeErr = errors.New("read-only filesystem")
	_, err := f.g.WriteCurve(c, confirmed)
	require.Error(t, err)
	assert.Equal(t, goldenCurve, string(f.curveBytes), "a refused write leaves the running curve exactly as it was")
}

func TestAFailedCurveRollbackIsReported(t *testing.T) {
	f := newFixture(t)
	c := mustCurve(t)
	c.Trim = 9
	f.corruptAfterWrite = true
	res, err := f.g.WriteCurve(c, confirmed)
	require.Error(t, err, "the write did not take and the restore did not either")
	assert.ErrorIs(t, err, ErrWriteUnconfirmed)
	assert.ErrorIs(t, err, ErrRollbackFailed)
	assert.False(t, res.Applied, "a caller reading Applied would believe the old curve is back in force")
}

// ---- plan falsification: the guard must follow the real validator ----

func TestCurveEditUsesTheRealValidatorNotAPlanLiteral(t *testing.T) {
	f := newFixture(t)
	c := mustCurve(t)
	c.T4 = 75 // still strictly between T3 and T5
	_, err := f.g.WriteCurve(c, confirmed)
	require.NoError(t, err, "T4 is an ordinary band edge; refusing it would lock a legitimate edit")
}

func TestCurveEditRejectsABandDutyOutsideTheTrimTable(t *testing.T) {
	f := newFixture(t)
	c := mustCurve(t)
	c.Cap = 100
	c.L3 = 80 // passes every numeric check the host runs, then never steps down
	_, err := f.g.WriteCurve(c, confirmed)
	require.Error(t, err, "80 is not a duty the trim table can band on")
	assert.Equal(t, goldenCurve, string(f.curveBytes), "a refused edit must not touch the file")
}

func TestCurveEditRejectsAnOutOfOrderEdge(t *testing.T) {
	f := newFixture(t)
	c := mustCurve(t)
	c.T4 = 95 // past T5, so the skeleton no longer rises
	_, err := f.g.WriteCurve(c, confirmed)
	require.Error(t, err, "the edges must keep rising")
	assert.Equal(t, goldenCurve, string(f.curveBytes))
}

func TestCurveEditRequiresConfirmation(t *testing.T) {
	f := newFixture(t)
	_, err := f.g.WriteCurve(mustCurve(t), Request{Actor: "tester"})
	assert.ErrorIs(t, err, ErrNotConfirmed)
	assert.Equal(t, goldenCurve, string(f.curveBytes))
}

// ---- ten minute regress ----

func TestRegressDoesNotFireBeforeItsDeadline(t *testing.T) {
	f := newFixture(t)
	_, err := f.g.SetManualLevel(70, confirmed)
	require.NoError(t, err)
	f.writeCalls = nil
	f.advance(RegressAfter - time.Second)
	did, err := f.g.Regress()
	require.NoError(t, err)
	assert.False(t, did, "the fan is still meant to hold the level it was given")
	assert.Empty(t, f.writeCalls, "an early regress is a write nobody asked for")
}

func TestRegressReturnsToAutoAtItsDeadline(t *testing.T) {
	f := newFixture(t)
	_, err := f.g.SetManualLevel(70, confirmed)
	require.NoError(t, err)
	f.writeCalls = nil
	f.advance(RegressAfter)
	did, err := f.g.Regress()
	require.NoError(t, err)
	require.True(t, did)
	assert.Equal(t, []int{0}, f.writeCalls, "0 is the handover back to the firmware curve, never 'off'")
	assert.Equal(t, 0, f.level)
}

func TestRegressYieldsWhenPfandHasTakenOver(t *testing.T) {
	f := newFixture(t)
	_, err := f.g.SetManualLevel(70, confirmed)
	require.NoError(t, err)
	f.writeCalls = nil
	f.advance(RegressAfter)
	f.pfand = true
	did, err := f.g.Regress()
	require.NoError(t, err, "the daemon owns the fan now; a regress would fight it")
	assert.False(t, did)
	assert.Empty(t, f.writeCalls)
}

func TestRegressIsHeldBackWhileTheEmergencyEdgeIsReached(t *testing.T) {
	f := newFixture(t)
	_, err := f.g.SetManualLevel(70, confirmed)
	require.NoError(t, err)
	f.writeCalls = nil
	f.advance(RegressAfter)
	f.temp = 80
	did, err := f.g.Regress()
	require.NoError(t, err)
	assert.False(t, did, "the daemon drives full speed here; returning to auto would override that")
	assert.Empty(t, f.writeCalls)

	f.temp = 46
	did, err = f.g.Regress()
	require.NoError(t, err)
	assert.True(t, did, "the regress stays pending until it is safe to run")
	assert.Equal(t, []int{0}, f.writeCalls)
}

func TestRegressIsHeldBackWhileTheTemperatureIsUnreadable(t *testing.T) {
	f := newFixture(t)
	_, err := f.g.SetManualLevel(70, confirmed)
	require.NoError(t, err)
	f.writeCalls = nil
	f.advance(RegressAfter)
	f.tempErr = errors.New("zone gone")
	did, err := f.g.Regress()
	require.NoError(t, err)
	assert.False(t, did, "an unreadable sensor is what sends the daemon to its fail-safe, not a licence to write")
	assert.Empty(t, f.writeCalls)
}

func TestANewManualWriteExtendsTheRegressDeadline(t *testing.T) {
	f := newFixture(t)
	_, err := f.g.SetManualLevel(70, confirmed)
	require.NoError(t, err)
	f.advance(RegressAfter / 2)
	_, err = f.g.SetManualLevel(50, confirmed)
	require.NoError(t, err)
	f.advance(RegressAfter/2 + time.Second)
	did, err := f.g.Regress()
	require.NoError(t, err)
	assert.False(t, did, "the second write is what the deadline belongs to")
	f.advance(RegressAfter)
	did, err = f.g.Regress()
	require.NoError(t, err)
	assert.True(t, did)
}

func TestAHeldBackRegressDoesNotBurnAHalfOpenProbe(t *testing.T) {
	f := newFixture(t)
	_, err := f.g.SetManualLevel(70, confirmed)
	require.NoError(t, err)

	f.setErr = errors.New("device busy")
	for i := 0; i < breakerThreshold; i++ {
		_, err := f.g.SetManualLevel(70, confirmed)
		require.Error(t, err)
	}
	f.setErr = nil
	f.advance(RegressAfter + breakerCooldown)
	f.temp = 80
	did, err := f.g.Regress()
	require.NoError(t, err)
	assert.False(t, did, "the regress is held back, not written")

	// A regress that declined to write has not exercised the device, so it cannot
	// be allowed to spend the probe that is there to find out whether the device
	// recovered.
	f.temp = 46
	did, err = f.g.Regress()
	require.NoError(t, err, "the probe is still owed to the write that never happened")
	assert.True(t, did)
}

func TestRegressWithNothingPendingIsANoOp(t *testing.T) {
	f := newFixture(t)
	did, err := f.g.Regress()
	require.NoError(t, err)
	assert.False(t, did)
	assert.Empty(t, f.writeCalls)
}

func TestAFailedRegressStaysPending(t *testing.T) {
	f := newFixture(t)
	_, err := f.g.SetManualLevel(70, confirmed)
	require.NoError(t, err)
	f.advance(RegressAfter)
	f.setErr = errors.New("device busy")
	did, err := f.g.Regress()
	require.Error(t, err)
	assert.False(t, did)
	f.setErr = nil
	f.writeCalls = nil
	did, err = f.g.Regress()
	require.NoError(t, err)
	assert.True(t, did, "a regress that could not be delivered must not be reported as delivered")
}

// ---- audit ----

func TestEveryAttemptIsAudited(t *testing.T) {
	f := newFixture(t)
	_, _ = f.g.SetManualLevel(73, confirmed)
	_, _ = f.g.SetManualLevel(70, confirmed)
	require.Len(t, f.events, 2)
	assert.Equal(t, OutcomeRefused, f.events[0].Outcome)
	assert.Equal(t, OpSetLevel, f.events[0].Op)
	assert.Equal(t, OutcomeApplied, f.events[1].Outcome)
	assert.Equal(t, "tester", f.events[1].Actor)
	assert.False(t, f.events[1].At.IsZero(), "an audit record with no time cannot be ordered against an incident")
}

func mustCurve(t *testing.T) *plus.Curve {
	t.Helper()
	c, err := plus.ParseCurve(goldenCurve)
	require.NoError(t, err)
	return c
}
