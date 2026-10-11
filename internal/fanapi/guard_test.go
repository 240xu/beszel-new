package fanapi

import (
	"errors"
	"strings"
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

	confirm bool

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
	// only way to reach the restore branch of a curve edit when the restore is
	// itself broken.
	corruptAfterWrite bool
	// written records that a write got through, so a read-back can be told apart
	// from the read that captured what was there before it.
	written bool
	// readBackAs is the curve a read-back sees once a write has landed. It is how
	// a restore that *succeeds* is reached at all: the disk answers with a curve
	// that is not the one written, and the write that undoes it works.
	readBackAs string

	writeCalls []int
	events     []Event
	// waits records what the guard asked to be woken up in, in order. The retry
	// after a decline is a policy, and a policy that cannot be observed cannot be
	// tested for the only thing that matters about it: how long it waits.
	waits []time.Duration
}

func newFixture(t *testing.T) *fixture {
	f := &fixture{t: t, now: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC), temp: 46, confirm: true}
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
			if f.readBackAs != "" && f.written {
				return []byte(f.readBackAs), nil
			}
			return append([]byte(nil), f.curveBytes...), nil
		},
		WriteCurveBytes: func(b []byte) error {
			if f.writeErr != nil {
				return f.writeErr
			}
			f.curveBytes = append([]byte(nil), b...)
			f.written = true
			if f.corruptAfterWrite {
				// The filesystem goes away between the write and the read-back, so
				// both the verification and the restore have to cope with it.
				f.curveErr = errors.New("device or resource busy")
				f.writeErr = errors.New("device or resource busy")
			}
			return nil
		},
		Audit: func(e Event) { f.events = append(f.events, e) },
		AfterFunc: func(d time.Duration, fn func()) *time.Timer {
			f.waits = append(f.waits, d)
			return time.AfterFunc(time.Hour, fn)
		},
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

func (f *fixture) req() Request {
	return Request{Confirm: f.confirm, Actor: "tester"}
}

func (f *fixture) lastEvent() Event { return f.events[len(f.events)-1] }

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

// ---- every refusal is a refusal, and none of them is a fault ----
//
// Each case runs on its own fixture and asserts its own sentinel. A shared
// fixture is what made the earlier version of this test cover one refusal while
// claiming to cover five: the first case left a flag standing, and every later
// case was short-circuited by it before it reached its own guard.

func TestGuardRejectionsDoNotTripTheBreaker(t *testing.T) {
	for _, tc := range []struct {
		name  string
		want  error
		setup func(*fixture)
		ask   func(*fixture) error
		allow func(*fixture) error
	}{
		{
			name:  "the write was not confirmed",
			want:  ErrNotConfirmed,
			setup: func(f *fixture) { f.confirm = false },
			ask:   func(f *fixture) error { _, err := f.g.SetManualLevel(70, f.req()); return err },
			allow: func(f *fixture) error { f.confirm = true; _, err := f.g.SetManualLevel(70, f.req()); return err },
		},
		{
			name:  "the duty is outside the whitelist",
			want:  plus.ErrFanLevelNotAllowed,
			ask:   func(f *fixture) error { _, err := f.g.SetManualLevel(73, confirmed); return err },
			allow: func(f *fixture) error { _, err := f.g.SetManualLevel(70, confirmed); return err },
		},
		{
			name:  "the daemon owns the fan",
			want:  plus.ErrPfandActive,
			setup: func(f *fixture) { f.pfand = true },
			ask:   func(f *fixture) error { _, err := f.g.SetManualLevel(70, confirmed); return err },
			allow: func(f *fixture) error { f.pfand = false; _, err := f.g.SetManualLevel(70, confirmed); return err },
		},
		{
			name:  "the package is at the emergency edge",
			want:  plus.ErrPackageTemperature,
			setup: func(f *fixture) { f.temp = 80 },
			ask:   func(f *fixture) error { _, err := f.g.SetManualLevel(70, confirmed); return err },
			allow: func(f *fixture) error { f.temp = 46; _, err := f.g.SetManualLevel(70, confirmed); return err },
		},
		{
			name:  "the package temperature is unknown",
			want:  ErrTemperatureUnknown,
			setup: func(f *fixture) { f.tempErr = errors.New("zone gone") },
			ask:   func(f *fixture) error { _, err := f.g.SetManualLevel(70, confirmed); return err },
			allow: func(f *fixture) error { f.tempErr = nil; _, err := f.g.SetManualLevel(70, confirmed); return err },
		},
		{
			name:  "the curve cannot be read",
			want:  ErrTemperatureUnknown,
			setup: func(f *fixture) { f.curveErr = errors.New("no such file") },
			ask:   func(f *fixture) error { _, err := f.g.SetManualLevel(70, confirmed); return err },
			allow: func(f *fixture) error { f.curveErr = nil; _, err := f.g.SetManualLevel(70, confirmed); return err },
		},
		{
			name: "the curve is one the host would not run",
			want: ErrCurveRejected,
			ask: func(f *fixture) error {
				c := mustCurve(f.t)
				c.L3 = 80 // outside the trim table
				_, err := f.g.WriteCurve(c, confirmed)
				return err
			},
			allow: func(f *fixture) error {
				c := mustCurve(f.t)
				c.Trim = 9
				_, err := f.g.WriteCurve(c, confirmed)
				return err
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			if tc.setup != nil {
				tc.setup(f)
			}
			// More refusals than it takes to open the breaker. If any of these
			// counted as a fault the path would be latched by now, and the write
			// that follows would be refused with ErrBreakerOpen instead of landing.
			for i := 0; i < 4; i++ {
				err := tc.ask(f)
				require.ErrorIs(t, err, tc.want,
					"refusal %d must be this refusal, not some other one short-circuiting it", i+1)
			}
			require.NoError(t, tc.allow(f),
				"a guard doing its job is not a broken control path, so the breaker must still be closed")
		})
	}
}

// ---- circuit breaker ----
//
// The count and the window are spelled out rather than reused from the
// constants, so a change to either shows up as a failure instead of quietly
// re-deriving the expectations from the value under test.

func TestBreakerOpensOnTheThirdConsecutiveFailure(t *testing.T) {
	f := newFixture(t)
	f.setErr = errors.New("device busy")
	for i := 0; i < 2; i++ {
		_, err := f.g.SetManualLevel(70, confirmed)
		require.Error(t, err)
		require.NotErrorIs(t, err, ErrBreakerOpen, "two failures are not enough, this is failure %d", i+1)
	}
	_, err := f.g.SetManualLevel(70, confirmed)
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrBreakerOpen,
		"the third failure is what the breaker trips on, and it is reported as the failure it is")

	calls := len(f.writeCalls)
	_, err = f.g.SetManualLevel(70, confirmed)
	require.ErrorIs(t, err, ErrBreakerOpen)
	assert.Len(t, f.writeCalls, calls, "an open breaker must not reach the device")
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
	for i := 0; i < 3; i++ {
		_, err := f.g.SetManualLevel(70, confirmed)
		require.NotErrorIs(t, err, ErrBreakerOpen, "the run of failures restarted, so attempt %d still reaches the device", i+1)
	}
}

func TestBreakerStaysShutUntilTheCooldownElapses(t *testing.T) {
	f := openBreaker(t)
	f.advance(2*time.Minute - time.Second)
	_, err := f.g.SetManualLevel(70, confirmed)
	assert.ErrorIs(t, err, ErrBreakerOpen, "half a cooldown is not a probe")
}

func TestBreakerRecoversThroughAProbe(t *testing.T) {
	f := openBreaker(t)
	f.advance(2 * time.Minute)
	f.setErr = nil
	res, err := f.g.SetManualLevel(70, confirmed)
	require.NoError(t, err, "the probe confirms the device works again")
	assert.Equal(t, 70, res.Level)
	f.setErr = errors.New("device busy")
	for i := 0; i < 3; i++ {
		_, err := f.g.SetManualLevel(70, confirmed)
		require.NotErrorIs(t, err, ErrBreakerOpen, "the breaker is closed again, so failure %d must reach the device", i+1)
	}
}

func TestAFailedProbeReopensTheBreaker(t *testing.T) {
	f := openBreaker(t)
	f.advance(2 * time.Minute)
	_, err := f.g.SetManualLevel(70, confirmed)
	require.Error(t, err, "the device is still broken")
	require.NotErrorIs(t, err, ErrBreakerOpen, "the probe was admitted, it just failed")
	calls := len(f.writeCalls)
	_, err = f.g.SetManualLevel(70, confirmed)
	assert.ErrorIs(t, err, ErrBreakerOpen,
		"a probe that fails must put the breaker back open rather than leave it ready")
	assert.Len(t, f.writeCalls, calls, "the second call must not reach the device")
}

// TestARefusalNeverConsumesTheHalfOpenProbe is the recovery half of C1. A guard
// that refuses before the device is reached learned nothing about the device, so
// it must not spend the probe that exists to find out whether the device works.
// Each case is one early exit: a reservation that survived a refusal left the
// breaker half-open with its probe already taken, and every caller after that —
// including a caller whose guards were all satisfied and whose device was fine —
// was refused forever, recoverable only by restarting the agent.
func TestARefusalNeverConsumesTheHalfOpenProbe(t *testing.T) {
	for _, tc := range []struct {
		name  string
		want  error
		setup func(*fixture)
		ask   func(*fixture) error
		allow func(*fixture) error
	}{
		{
			name:  "pfand owns the fan",
			want:  plus.ErrPfandActive,
			setup: func(f *fixture) { f.pfand = true },
			ask:   func(f *fixture) error { _, err := f.g.SetManualLevel(70, confirmed); return err },
			allow: func(f *fixture) error { f.pfand = false; _, err := f.g.SetManualLevel(70, confirmed); return err },
		},
		{
			name:  "the package is at the emergency edge",
			want:  plus.ErrPackageTemperature,
			setup: func(f *fixture) { f.temp = 80 },
			ask:   func(f *fixture) error { _, err := f.g.SetManualLevel(70, confirmed); return err },
			allow: func(f *fixture) error { f.temp = 46; _, err := f.g.SetManualLevel(70, confirmed); return err },
		},
		{
			name:  "the package temperature is unknown",
			want:  ErrTemperatureUnknown,
			setup: func(f *fixture) { f.tempErr = errors.New("zone gone") },
			ask:   func(f *fixture) error { _, err := f.g.SetManualLevel(70, confirmed); return err },
			allow: func(f *fixture) error { f.tempErr = nil; _, err := f.g.SetManualLevel(70, confirmed); return err },
		},
		{
			name:  "the write was not confirmed",
			want:  ErrNotConfirmed,
			setup: func(f *fixture) { f.confirm = false },
			ask:   func(f *fixture) error { _, err := f.g.SetManualLevel(70, f.req()); return err },
			allow: func(f *fixture) error { f.confirm = true; _, err := f.g.SetManualLevel(70, f.req()); return err },
		},
		{
			name: "the curve is one the host would not run",
			want: ErrCurveRejected,
			ask: func(f *fixture) error {
				c := mustCurve(f.t)
				c.L3 = 80
				_, err := f.g.WriteCurve(c, confirmed)
				return err
			},
			allow: func(f *fixture) error {
				c := mustCurve(f.t)
				c.Trim = 9
				_, err := f.g.WriteCurve(c, confirmed)
				return err
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := openBreaker(t)
			f.advance(2 * time.Minute)
			f.setErr = nil // the device recovered; only the guard stands in the way
			if tc.setup != nil {
				tc.setup(f)
			}

			require.ErrorIs(t, tc.ask(f), tc.want)
			// Twice, so a reservation leaked on one call cannot look like it was
			// merely taken and then spent on the next one.
			require.ErrorIs(t, tc.ask(f), tc.want)

			require.NoError(t, tc.allow(f),
				"the guard has stood down and the device is healthy, so the breaker owes a probe to this write")
		})
	}
}

// TestAGuardRefusalIsReportedAheadOfTheBreakerLocksDownThePrecedence. Admission
// runs after the guards, so a caller whose guards refuse is told which guard it
// was. The alternative ordering is defensible on its own terms — a latch is the
// more persistent fact — so the choice is pinned here rather than left to
// whichever order the code happens to be written in: an operator told "the
// daemon owns the fan" stops the daemon and is then told the latch, which is two
// round trips but loses no information either way.
func TestAGuardRefusalIsReportedAheadOfTheBreaker(t *testing.T) {
	f := openBreaker(t)
	f.pfand = true
	_, err := f.g.SetManualLevel(70, confirmed)
	require.Error(t, err)
	assert.ErrorIs(t, err, plus.ErrPfandActive,
		"the guard that stood in the way is the thing the caller has to act on")
	assert.NotErrorIs(t, err, ErrBreakerOpen,
		"and the latch is not what this attempt was refused for")

	// With the guard gone, the same call now surfaces the latch, so nothing was
	// hidden by reporting the guard first.
	f.pfand = false
	_, err = f.g.SetManualLevel(70, confirmed)
	assert.ErrorIs(t, err, ErrBreakerOpen)
}

func TestAManualDutyIsHeldForTenMinutes(t *testing.T) {
	f := newFixture(t)
	res, err := f.g.SetManualLevel(70, confirmed)
	require.NoError(t, err)
	// Spelled out rather than reusing the constant, so the value the product
	// asked for is the thing under test and not a tautology.
	want := f.now.Add(10 * time.Minute)
	assert.Equal(t, want, f.g.PendingRegressAt(),
		"a manual override is a temporary measure, and the window is part of the contract")
	assert.Equal(t, want, res.RegressAt,
		"the caller is told the deadline in the result it already has, not only through a second call")
}

func TestAFurtherManualWriteRestartsTheTenMinuteWindow(t *testing.T) {
	f := newFixture(t)
	_, err := f.g.SetManualLevel(70, confirmed)
	require.NoError(t, err)
	f.advance(9 * time.Minute)
	res, err := f.g.SetManualLevel(50, confirmed)
	require.NoError(t, err)
	want := f.now.Add(10 * time.Minute)
	assert.Equal(t, want, f.g.PendingRegressAt(),
		"the window restarts, otherwise the newest request would outlive its own term")
	assert.Equal(t, want, res.RegressAt, "the result carries the same deadline the guard holds")
}

func TestARefusedWriteReportsNoDeadline(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*fixture)
		ask   func(*fixture) (Result, error)
	}{
		{"not confirmed", func(f *fixture) { f.confirm = false },
			func(f *fixture) (Result, error) { return f.g.SetManualLevel(70, f.req()) }},
		{"pfand owns the fan", func(f *fixture) { f.pfand = true },
			func(f *fixture) (Result, error) { return f.g.SetManualLevel(70, confirmed) }},
		{"at the emergency edge", func(f *fixture) { f.temp = 80 },
			func(f *fixture) (Result, error) { return f.g.SetManualLevel(70, confirmed) }},
		{"the write lost its race", func(f *fixture) {
			other := 40
			f.loseRaceTo = &other
		},
			func(f *fixture) (Result, error) { return f.g.SetManualLevel(70, confirmed) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			tc.setup(f)
			res, err := tc.ask(f)
			require.Error(t, err, "a write that did not land is not a write")
			assert.True(t, res.RegressAt.IsZero(),
				"nothing was programmed, so nothing is due to be handed back and no deadline may be promised")
			assert.False(t, res.Applied)
		})
	}
}

func TestACurveEditPromisesNoDeadline(t *testing.T) {
	f := newFixture(t)
	c := mustCurve(t)
	c.Trim = 9
	res, err := f.g.WriteCurve(c, confirmed)
	require.NoError(t, err)
	assert.True(t, res.RegressAt.IsZero(),
		"a curve edit schedules no handover: the curve is what the fan runs on")
	assert.True(t, f.g.PendingRegressAt().IsZero())
}

func openBreaker(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	f.setErr = errors.New("device busy")
	for i := 0; i < 3; i++ {
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

// TestACurveRollbackRestoresTheBytesNotARendering is the fixture that makes the
// difference between the two visible. Here the read-back disagrees with what was
// written *and* the restore works, so the bytes that end up on disk are
// observable. They have to be the ones that were there, comment line and all:
// re-rendering would substitute a file the daemon was never running.
func TestACurveRollbackRestoresTheBytesNotARendering(t *testing.T) {
	f := newFixture(t)
	// An operator's file, with a comment Render would never emit and spacing it
	// does not produce.
	running := goldenCurve + "# tuned by hand for the 2019 chassis\n"
	f.curveBytes = []byte(running)
	// The disk answers the read-back with a different, valid curve, so the
	// verification fails and the guard has to put the old file back.
	f.readBackAs = strings.Replace(goldenCurve, "TRIM=6", "TRIM=8", 1)

	c := mustCurve(t)
	c.Trim = 9
	res, err := f.g.WriteCurve(c, confirmed)
	require.Error(t, err, "the curve on disk is not the one that was written")
	assert.ErrorIs(t, err, ErrWriteUnconfirmed)
	assert.NotErrorIs(t, err, ErrRollbackFailed, "the restore itself works here, which is the point")
	assert.False(t, res.Applied)
	assert.Equal(t, running, string(f.curveBytes),
		"a restore must reproduce the bytes that were running, not a fresh rendering of them")
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
	f.advance(10*time.Minute - time.Second)
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
	f.advance(10 * time.Minute)
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
	f.advance(10 * time.Minute)
	f.pfand = true
	did, err := f.g.Regress()
	require.NoError(t, err, "the daemon owns the fan now; a regress would fight it")
	assert.False(t, did)
	assert.Empty(t, f.writeCalls)
}

// At the emergency edge this used to decline, on the stated grounds that the
// daemon was driving full speed. Nothing was: the daemon check returns first, so
// declining left the fan on a duty an operator had set, at the one temperature
// the curve says must not be reached that way. It now commands the duty the
// curve itself names there, and still holds the handover until the edge clears.
func TestAtTheEmergencyEdgeTheFanIsPutOnTheCurveOwnDuty(t *testing.T) {
	f := newFixture(t)
	_, err := f.g.SetManualLevel(70, confirmed)
	require.NoError(t, err)
	f.writeCalls = nil
	f.advance(10 * time.Minute)
	f.temp = 80
	did, err := f.g.Regress()
	require.NoError(t, err)
	assert.False(t, did, "control is not handed back while the package is at the edge")
	assert.Equal(t, []int{mustCurve(t).E}, f.writeCalls,
		"no daemon is running, so the curve's own emergency duty is the only thing "+
			"standing between the package and a fan nobody is driving")

	f.temp = 46
	f.advance(f.waits[len(f.waits)-1])
	did, err = f.g.Regress()
	require.NoError(t, err)
	assert.True(t, did, "the handover waits for the edge to clear")
	assert.Equal(t, []int{mustCurve(t).E, 0}, f.writeCalls)
}

func TestRegressIsHeldBackWhileTheTemperatureIsUnreadable(t *testing.T) {
	f := newFixture(t)
	_, err := f.g.SetManualLevel(70, confirmed)
	require.NoError(t, err)
	f.writeCalls = nil
	f.advance(10 * time.Minute)
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
	f.advance(5 * time.Minute)
	_, err = f.g.SetManualLevel(50, confirmed)
	require.NoError(t, err)
	f.advance(5*time.Minute + time.Second)
	did, err := f.g.Regress()
	require.NoError(t, err)
	assert.False(t, did, "the second write is what the deadline belongs to")
	f.advance(10 * time.Minute)
	did, err = f.g.Regress()
	require.NoError(t, err)
	assert.True(t, did)
}

func TestAHeldBackRegressDoesNotBurnAHalfOpenProbe(t *testing.T) {
	f := newFixture(t)
	_, err := f.g.SetManualLevel(70, confirmed)
	require.NoError(t, err)

	f.setErr = errors.New("device busy")
	for i := 0; i < 3; i++ {
		_, err := f.g.SetManualLevel(70, confirmed)
		require.Error(t, err)
	}
	f.setErr = nil
	f.advance(10*time.Minute + 2*time.Minute)
	f.tempErr = errors.New("zone gone")
	did, err := f.g.Regress()
	require.NoError(t, err)
	assert.False(t, did, "the regress is held back, not written")

	// A regress that declined to write has not exercised the device, so it cannot
	// be allowed to spend the probe that is there to find out whether the device
	// recovered. The unreadable sensor is the gate that still works that way; the
	// emergency edge now writes, because it has to.
	f.tempErr = nil
	f.advance(f.waits[len(f.waits)-1])
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
	f.advance(10 * time.Minute)
	f.setErr = errors.New("device busy")
	did, err := f.g.Regress()
	require.Error(t, err)
	assert.False(t, did)
	f.setErr = nil
	f.writeCalls = nil
	f.advance(f.waits[len(f.waits)-1])
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
	assert.Equal(t, 70, f.events[1].Level,
		"a record of what was done has to say what was done to it")
	assert.False(t, f.events[1].At.IsZero(), "an audit record with no time cannot be ordered against an incident")
}

// TestARegressThatDidNotWriteIsNotAuditedAsApplied separates "the fan went back
// to its curve" from "a safety gate declined to send it there". Both return no
// error, so an outcome that defaults to applied told an operator reading the log
// that the handover had happened while the fan was still being held.
func TestARegressThatDidNotWriteIsNotAuditedAsApplied(t *testing.T) {
	// The event carries the gate's code and its own text rather than the phrase
	// regressLocked used to build, so a reader of the trail never has to match
	// English prose to learn which gate stood in the way.
	for _, tc := range []struct {
		name    string
		pending bool
		setup   func(*fixture)
		want    string
	}{
		{"nothing was pending", false, func(*fixture) {}, "no manual duty was pending"},
		{"the daemon took over", true, func(f *fixture) { f.pfand = true }, "the daemon owns the fan"},
		{"the emergency edge is reached", true, func(f *fixture) { f.temp = 80 }, "emergency edge"},
		{"the temperature is unknown", true, func(f *fixture) { f.tempErr = errors.New("zone gone") }, "temperature"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			if tc.pending {
				_, err := f.g.SetManualLevel(70, confirmed)
				require.NoError(t, err)
				f.advance(10 * time.Minute)
			}
			tc.setup(f)

			did, err := f.g.Regress()
			require.NoError(t, err, "declining to write is not a failure")
			require.False(t, did)

			e := f.lastEvent()
			assert.Equal(t, OpRegress, e.Op)
			assert.Equal(t, OutcomeRefused, e.Outcome,
				"the write did not happen, and an applied outcome would say that it did")
			assert.Contains(t, e.Message, tc.want,
				"an operator reading the trail is owed which gate stood in the way")
			assert.NotEmpty(t, e.Code,
				"a panel branches on the code; prose is for whoever reads the log")
			assert.False(t, e.At.IsZero())
		})
	}
}

func TestASucceededRegressIsAuditedAsApplied(t *testing.T) {
	f := newFixture(t)
	_, err := f.g.SetManualLevel(70, confirmed)
	require.NoError(t, err)
	f.advance(10 * time.Minute)

	did, err := f.g.Regress()
	require.NoError(t, err)
	require.True(t, did)
	assert.Equal(t, OutcomeApplied, f.lastEvent().Outcome)
}

func TestAFailedRegressIsAuditedAsFailed(t *testing.T) {
	f := newFixture(t)
	_, err := f.g.SetManualLevel(70, confirmed)
	require.NoError(t, err)
	f.advance(10 * time.Minute)
	f.setErr = errors.New("device busy")

	did, err := f.g.Regress()
	require.Error(t, err)
	assert.False(t, did)
	assert.Equal(t, OutcomeFailed, f.lastEvent().Outcome)
}

func mustCurve(t *testing.T) *plus.Curve {
	t.Helper()
	c, err := plus.ParseCurve(goldenCurve)
	require.NoError(t, err)
	return c
}
