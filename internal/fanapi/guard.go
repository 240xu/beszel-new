// Package fanapi guards every write that changes how the fan runs. agent/plus
// knows how to program a duty and validate a curve; this package decides whether the
// write is allowed, proves it landed, and hands the fan back to the curve on a timer.
package fanapi

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/henrygd/beszel/agent/plus"
)

// RegressAfter is how long a manual duty outlives the request that asked for it.
const RegressAfter = 10 * time.Minute

// How long after a gate declines the fan is looked at again: it grows, then stops.
const (
	regressRetryBase = 15 * time.Second
	regressRetryMax  = 5 * time.Minute
)

// How often an armed thermal floor reads the package temperature. Deliberately its own
// number rather than a reuse of regressRetryBase: that one grows to five minutes to stop
// hammering a device, and the floor exists precisely for the stretch where nothing else
// is looking at the fan, so its cadence answers the fan and not a retry policy.
const thermalFloorInterval = 30 * time.Second

const (
	// breakerThreshold is how many writes in a row must fail before the control
	// path is presumed broken. A guard's refusal does not count: that is the guard
	// working, not a device fault.
	breakerThreshold = 3
	// breakerCooldown is how long the path is left alone before one probe is admitted:
	// long enough not to hammer a failing device, short enough to clear a transient.
	breakerCooldown = 2 * time.Minute
)

// Reasons this package adds. Device-level reasons stay in agent/plus so a caller
// switches on one set of sentinels; these cover policy decisions only made here.
var (
	ErrNotConfirmed       = errors.New("the write was not confirmed")
	ErrBreakerOpen        = errors.New("fan control is latched open after repeated failures")
	ErrTemperatureUnknown = errors.New("the package temperature could not be established")
	ErrWriteUnconfirmed   = errors.New("the fan is not holding the duty that was requested")
	ErrRollbackFailed     = errors.New("the previous state could not be restored")
	ErrCurveRejected      = errors.New("the curve is not one the host would run")
)

// Op names the guarded operation an audit record describes.
type Op string

const (
	OpSetLevel   Op = "set_level"
	OpRegress    Op = "regress"
	OpWriteCurve Op = "write_curve"
	// OpThermalFloor is the floor that stands in for a daemon nobody is running.
	// It is a separate op because it writes the fan without any operator
	// having asked, which is not what regress or set_level describe.
	OpThermalFloor Op = "thermal_floor"
)

// Outcome separates "we said no" from "we tried and could not".
type Outcome string

const (
	OutcomeApplied Outcome = "applied"
	OutcomeRefused Outcome = "refused"
	OutcomeFailed  Outcome = "failed"
	// OutcomeHeld is a write the guard made to keep the fan safe without returning it
	// to its curve: the emergency duty. It is neither refused (nothing was written) nor
	// applied (the fan did not go back to its curve), so the trail names the write and
	// the level now actually driving the fan instead of a refusal that hid it.
	OutcomeHeld Outcome = "held"
)

// Event is one audited attempt. Every path emits exactly one, refusals included:
// a write that never happened is what an incident review needs. Code and Message
// carry the refusal in actionable form; Reason stays untranslated for a log.
type Event struct {
	Op      Op
	Outcome Outcome
	Actor   string
	Level   int
	Reason  string
	Code    string
	Message string
	At      time.Time
}

// Request carries the confirmation a state-changing write must arrive with.
type Request struct {
	Confirm bool
	Actor   string
}

// Result reports what was achieved, never what was asked for.
type Result struct {
	Applied bool
	Level   int
	// NeedsPfandRestart says the curve is on disk but not yet in force, because
	// the daemon sources its configuration once at startup.
	NeedsPfandRestart bool
	RegressAt         time.Time
}

// Deps is every side effect the policy needs, so the rules can be exercised
// without this machine's device, daemon or clock.
type Deps struct {
	Now       func() time.Time
	ReadTempC func() (int, error)
	ReadCurve func() (*plus.Curve, error)
	SetLevel  func(int) error
	GetLevel  func() (int, error)
	PfandUp   func() bool

	ReadCurveBytes  func() ([]byte, error)
	WriteCurveBytes func([]byte) error

	Audit func(Event)

	// AfterFunc is the timer seam: a retry policy the tests cannot see
	AfterFunc func(time.Duration, func()) *time.Timer
}

type breakerState int

const (
	breakerClosed breakerState = iota
	breakerOpen
	breakerHalfOpen
)

type breaker struct {
	state    breakerState
	failures int
	openedAt time.Time
}

// Guard is the guardrail. The mutex is the whole of the "one half-open probe at a
// time" guarantee, which is why the breaker holds no reservation flag; see admit.
type Guard struct {
	d  Deps
	mu sync.Mutex

	brk       breaker
	regressAt time.Time
	timer     *time.Timer
	// declines counts consecutive refusals, so the wait can grow
	declines int

	// floorOn and floorTimer are the manual-mode thermal floor. Kept apart from
	// regressAt on purpose: a regress hands the fan back to its curve and ends,
	// while the floor stays armed for as long as the daemon is gone.
	floorOn    bool
	floorTimer *time.Timer

	// closed ends every watch this guard owns. Close sets it, and the entry
	// points that could put work back on the schedule read it: a regress
	// callback already in the air when Close ran must find a guard that is
	// done, not rebuild the timer Close just stopped.
	closed bool
}

// New builds a guard over the given seams.
func New(d Deps) *Guard {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Audit == nil {
		d.Audit = func(Event) {}
	}
	if d.AfterFunc == nil {
		d.AfterFunc = time.AfterFunc
	}
	return &Guard{d: d}
}

// SetManualLevel programs a duty, then proves the device took it. A restore that
// fails is reported rather than folded into a success.
func (g *Guard) SetManualLevel(level int, req Request) (Result, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.d.Now()

	res, err := g.setLevelLocked(level, req, now)
	g.audit(OpSetLevel, req, level, res.Level, err)
	return res, err
}

func (g *Guard) setLevelLocked(level int, req Request, now time.Time) (Result, error) {
	if !req.Confirm {
		return Result{}, fmt.Errorf("duty %d: %w", level, ErrNotConfirmed)
	}
	if err := plus.ValidateLevel(level); err != nil {
		return Result{}, fmt.Errorf("%s: %w", err, plus.ErrFanLevelNotAllowed)
	}
	// The daemon rewrites the duty whenever its own result moves the fan by more than
	// its dead zone, so a duty programmed under it is undone a tick later, before anyone
	// could act on the panel's claim. Its fail-safe reads a device candidate list, so an
	// unreadable one leaves a daemon that writes nothing: "up" is the conservative reading.
	if g.d.PfandUp() {
		return Result{}, fmt.Errorf("duty %d: %w", level, plus.ErrPfandActive)
	}
	temp, c, err := g.temperature()
	if err != nil {
		return Result{}, fmt.Errorf("duty %d: %w", level, err)
	}
	if temp >= c.Emerg {
		return Result{}, fmt.Errorf("duty %d at %dC, emergency edge is %dC: %w",
			level, temp, c.Emerg, plus.ErrPackageTemperature)
	}
	// Admission last, before the first byte moves; see admit.
	if err := g.admit(now); err != nil {
		return Result{}, err
	}

	prev, err := g.d.GetLevel()
	if err != nil {
		// A platform without fan control will not grow one during a cooldown.
		// Charging this read to the breaker would turn the honest unsupported
		// answer into a breaker_open latch after three tries, telling the
		// operator to wait for a device that will never answer. The count is
		// for faults that may clear; this one is a fact about the host.
		if !errors.Is(err, plus.ErrFanControlUnsupported) {
			g.fail(now)
		}
		return Result{}, fmt.Errorf("read the duty to restore: %w", err)
	}
	if err := g.d.SetLevel(level); err != nil {
		g.fail(now)
		return Result{}, fmt.Errorf("program duty %d: %w: %v", level, ErrWriteFailed, err)
	}
	got, err := g.d.GetLevel()
	if err != nil || got != level {
		// A disagreeing read-back names neither of its two authors: a write that
		// never landed, or a daemon that started inside this window and took the
		// fan back. The entry gate ran before the window opened, so it cannot
		// tell them apart. The undo is a write by hand, so it obeys that same
		// gate -- and nothing broke, so the breaker is left alone.
		if g.d.PfandUp() {
			return Result{}, fmt.Errorf("duty %d: %w", level, plus.ErrPfandActive)
		}
		g.fail(now)
		unconfirmed := fmt.Errorf("asked for %d, device reports %d (%v): %w",
			level, got, err, ErrWriteUnconfirmed)
		if rerr := g.restoreLevel(prev); rerr != nil {
			// Both reasons are wrapped, not flattened to prose: errors.Is must still
			// see the original fault and still see that the restore failed.
			return Result{}, fmt.Errorf("%w, and %w", unconfirmed, rerr)
		}
		return Result{}, unconfirmed
	}
	g.record()
	g.declines = 0
	g.scheduleRegressLocked(now, RegressAfter)
	return Result{Applied: true, Level: level, RegressAt: g.regressAt}, nil
}

// restoreLevel puts the previous duty back, reporting its own failure: a guard
// that swallows a failed restore reports a fan nobody knows the duty of.
func (g *Guard) restoreLevel(prev int) error {
	if err := g.d.SetLevel(prev); err != nil {
		return fmt.Errorf("%w: restoring duty %d: %v", ErrRollbackFailed, prev, err)
	}
	return nil
}

// WriteCurve replaces the curve, adding confirmation, arbitration and a restore to
// the validator agent/plus proved to be a superset of the host's.
func (g *Guard) WriteCurve(c *plus.Curve, req Request) (Result, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.d.Now()

	res, err := g.writeCurveLocked(c, req, now)
	g.audit(OpWriteCurve, req, 0, res.Level, err)
	return res, err
}

func (g *Guard) writeCurveLocked(c *plus.Curve, req Request, now time.Time) (Result, error) {
	if !req.Confirm {
		return Result{}, ErrNotConfirmed
	}
	if err := c.Validate(); err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrCurveRejected, err)
	}
	// Admission past the guards, as in setLevelLocked.
	if err := g.admit(now); err != nil {
		return Result{}, err
	}

	original, err := g.d.ReadCurveBytes()
	if err != nil {
		g.fail(now)
		return Result{}, fmt.Errorf("read the curve to restore: %w", err)
	}
	if err := g.d.WriteCurveBytes([]byte(c.Render())); err != nil {
		// A curve this stack refused is the guard working, not a path that broke.
		if errors.Is(err, plus.ErrCurveInvalid) {
			return Result{}, fmt.Errorf("%w: %v", ErrCurveRejected, err)
		}
		g.fail(now)
		return Result{}, fmt.Errorf("write curve: %w", err)
	}
	if err := g.verifyCurve(c); err != nil {
		g.fail(now)
		// The restore must reproduce the bytes, not re-render them: rendering
		// regenerates comments, so the restored file would not be the one that ran.
		if rerr := g.d.WriteCurveBytes(original); rerr != nil {
			return Result{}, fmt.Errorf("%w, and %w: restoring the previous curve: %v",
				err, ErrRollbackFailed, rerr)
		}
		return Result{}, err
	}
	g.record()
	// The daemon sources its configuration once at start, so a curve reaches the disk
	// before the fan either way: up, it holds the file it read at start; down, nothing
	// will read this one. Reading its state claimed "no restart needed" when inertest.
	return Result{Applied: true, NeedsPfandRestart: true}, nil
}

func (g *Guard) verifyCurve(want *plus.Curve) error {
	raw, err := g.d.ReadCurveBytes()
	if err != nil {
		return fmt.Errorf("%w: re-read the curve: %v", ErrWriteUnconfirmed, err)
	}
	got, err := plus.ParseCurve(string(raw))
	if err != nil {
		return fmt.Errorf("%w: re-read the curve does not parse: %v", ErrWriteUnconfirmed, err)
	}
	if *got != *want {
		return fmt.Errorf("%w: the curve on disk is not the curve that was written", ErrWriteUnconfirmed)
	}
	return nil
}

// temperature reads the package temperature and the emergency edge it is judged
// against. The edge comes from the operator's own curve, so the guard cannot drift
// from the control law it protects. An unreadable sensor is not a safe one.
func (g *Guard) temperature() (int, *plus.Curve, error) {
	c, err := g.d.ReadCurve()
	if err != nil {
		return 0, nil, fmt.Errorf("%w: read curve: %v", ErrTemperatureUnknown, err)
	}
	t, err := g.d.ReadTempC()
	if err != nil {
		return 0, nil, fmt.Errorf("%w: read package temperature: %v", ErrTemperatureUnknown, err)
	}
	return t, c, nil
}

// admit decides whether this attempt may reach the device. It carries no
// reservation: the flag that used to need clearing outlived the calls that owed
// it, latched, and refused every caller until an agent restart. A reservation
// outliving its call is a latch nobody owns, so admission sits past every guard.
func (g *Guard) admit(now time.Time) error {
	b := &g.brk
	switch b.state {
	case breakerOpen:
		if now.Sub(b.openedAt) < breakerCooldown {
			return ErrBreakerOpen
		}
		b.state = breakerHalfOpen
	}
	return nil
}

// fail records a broken write. A failed half-open probe reopens immediately: it
// is the one attempt that was allowed to prove the path still works.
func (g *Guard) fail(now time.Time) {
	b := &g.brk
	if b.state == breakerHalfOpen {
		b.state, b.openedAt, b.failures = breakerOpen, now, breakerThreshold
		return
	}
	b.failures++
	if b.failures >= breakerThreshold {
		b.state, b.openedAt = breakerOpen, now
	}
}

func (g *Guard) record() {
	g.brk = breaker{}
}

// Regress hands the fan back to the firmware curve once the manual duty has run
// its course. The bool separates "returned" from "nothing due" and "declined".
func (g *Guard) Regress() (bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		// A callback in the air when Close ran lands here. Nothing is pending
		// any more and nothing may be rescheduled: the handover it would try
		// to run was ended on purpose, not forgotten.
		return false, nil
	}
	now := g.d.Now()

	did, held, heldLevel, err := g.regressLocked(now)
	if err == nil && !did {
		// A safety gate stopped short of the device and nothing failed, so there is
		// no error for audit to classify. Recording it as applied would tell an
		// operator the fan went back to its curve when it is still held at a duty.
		//
		// The emergency gate is the one that wrote: it programmed the curve's own duty
		// and proved it, so the trail must record that write -- held, at the duty now in
		// force -- rather than a refusal at level zero that hid it. Every other gate
		// wrote nothing, so refused at level 0 is the truth for them.
		outcome, level := OutcomeRefused, 0
		if held == DeclineAtEmergency {
			outcome, level = OutcomeHeld, heldLevel
		}
		g.auditDeclined(OpRegress, "regress", held, outcome, level)
		// Nothing pending and a daemon that owns the fan are both endings rather
		// than refusals: there is nothing held to bring back, so retrying would be
		// a duty the guard no longer has any business polling.
		if held != DeclineNone && held != DeclineDaemon {
			g.retryLocked(now)
		}
		return did, nil
	}
	if err != nil {
		g.retryLocked(now)
	}
	g.audit(OpRegress, Request{Actor: "regress"}, 0, 0, err)
	return did, err
}

// holdEmergencyLocked puts the fan on the duty the curve itself commands here, and proves
// it landed. Only runs at or past the edge, so a refusal leaves the previous duty standing.
func (g *Guard) holdEmergencyLocked(now time.Time, duty int) error {
	if err := g.admit(now); err != nil {
		return err
	}
	if err := g.d.SetLevel(duty); err != nil {
		g.fail(now)
		return fmt.Errorf("program duty %d: %w", duty, err)
	}
	got, err := g.d.GetLevel()
	if err != nil || got != duty {
		// The daemon may have claimed the fan between the check above and this
		// read-back. That is not a fault here: past the edge the curve hands out
		// the same duty, so the daemon arriving is the outcome, not a lost write.
		if g.d.PfandUp() {
			return fmt.Errorf("duty %d: %w", duty, plus.ErrPfandActive)
		}
		g.fail(now)
		return fmt.Errorf("%w: asked for the emergency duty %d, device reports %d (%v)",
			ErrWriteUnconfirmed, duty, got, err)
	}
	g.record()
	return nil
}

// regressLocked also names which gate declined, and the duty an emergency hold wrote,
// so a handover that did not happen is auditable.
func (g *Guard) regressLocked(now time.Time) (bool, string, int, error) {
	if g.regressAt.IsZero() || now.Before(g.regressAt) {
		return false, DeclineNone, 0, nil
	}
	// Two things can have changed while the duty was held. If the daemon is up it
	// owns the fan and our write would be undone on its next tick, so the pending
	// regress is dropped rather than fought over.
	if g.d.PfandUp() {
		g.clearRegressLocked()
		return false, DeclineDaemon, 0, nil
	}
	temp, c, err := g.temperature()
	if err != nil {
		return false, DeclineUnreadable, 0, nil
	}
	// At or past the emergency edge the answer is the curve's emergency duty, so this
	// gate commands it rather than declining. It used to decline on the grounds that the
	// daemon was driving full speed, but the check above returns if one is up, so none is.
	if temp >= c.Emerg {
		return false, DeclineAtEmergency, c.E, g.holdEmergencyLocked(now, c.E)
	}
	// There is no duty that hands this fan back to its curve. _FSL forwards its
	// argument to an EC fan-duty write and has no case for zero, so a zero duty is a
	// request for zero percent: it stops the fan whenever the EC follows the set
	// value, and does nothing when it does not. Which of the two this host is in is
	// an EC curve setting the guard cannot read, so the duty stays held for an
	// operator. That costs convenience, not safety: the interlock above runs on every
	// attempt, which is what a held duty needs, so the retry below is kept.
	//
	// Admission last: a regress that declined to write never touched the device,
	// and letting it spend the probe would leave the breaker waiting for a write.
	if err := g.admit(now); err != nil {
		return false, "", 0, err
	}
	return false, DeclineNoHandover, 0, nil
}

func (g *Guard) scheduleRegressLocked(now time.Time, delay time.Duration) {
	if g.closed {
		return
	}
	g.regressAt = now.Add(delay)
	if g.timer != nil {
		g.timer.Stop()
	}
	g.timer = g.d.AfterFunc(delay, func() { _, _ = g.Regress() })
}

// retryLocked hands the decision on after a gate declined: one-shot timers meant a
// handover that stopped short stayed stopped for good.
func (g *Guard) retryLocked(now time.Time) {
	if g.closed {
		return
	}
	g.scheduleRegressLocked(now, regressRetryDelay(g.declines))
	g.declines++
}

func regressRetryDelay(declines int) time.Duration {
	d := regressRetryBase
	for i := 0; i < declines && d < regressRetryMax; i++ {
		d *= 2
	}
	if d > regressRetryMax {
		return regressRetryMax
	}
	return d
}

func (g *Guard) clearRegressLocked() {
	g.regressAt = time.Time{}
	g.declines = 0
	if g.timer != nil {
		g.timer.Stop()
		g.timer = nil
	}
}

// ThermalFloorArmed reports whether the floor is currently standing in for a
// curve daemon. Additive alongside PendingRegressAt so a panel can tell the two
// apart: a regress deadline is a duty going home, an armed floor is a fan being
// watched while it stays put.
func (g *Guard) ThermalFloorArmed() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.floorOn
}

// PendingRegressAt reports when the fan is due back on its curve, if at all.
func (g *Guard) PendingRegressAt() time.Time {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.regressAt
}

// Close stops the pending regress timer and any armed thermal floor: after Close
// the process must leave the fan to whoever still owns it, not keep watching it.
//
// Stopping the timers is not enough on its own: a regress callback that was
// already queued when Close ran would run after it and rebuild the timer through
// the retry path. closed is what those callbacks check, so closing is final
// rather than merely up to date with the timers.
func (g *Guard) Close() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.closed = true
	g.clearRegressLocked()
	g.disarmFloorLocked()
}

func (g *Guard) audit(op Op, req Request, level, got int, err error) {
	e := Event{Op: op, Outcome: OutcomeApplied, Actor: req.Actor, Level: level, At: g.d.Now()}
	if err != nil {
		e.Outcome, e.Reason = OutcomeFailed, err.Error()
		e.Code = codeOf(err)
		e.Message = messageOf(e.Code)
		if errors.Is(err, ErrNotConfirmed) || errors.Is(err, ErrBreakerOpen) ||
			errors.Is(err, ErrCurveRejected) || errors.Is(err, ErrTemperatureUnknown) ||
			errors.Is(err, plus.ErrPfandActive) || errors.Is(err, plus.ErrPackageTemperature) ||
			errors.Is(err, plus.ErrFanLevelNotAllowed) {
			e.Outcome = OutcomeRefused
		}
	}
	if e.Outcome == OutcomeApplied {
		e.Level = got
	}
	g.d.Audit(e)
}

// thermalFloorActor names the writer in an audit trail, since no operator asked for this
// write and a record with an empty actor is a record nobody can trace back.
const thermalFloorActor = "thermal-floor"

// ArmThermalFloor guarantees a thermal bottom line while no curve daemon is running.
//
// Manual mode stops pfand, and pfand is the only control loop in the process: nothing
// else reads the package temperature, so a duty left in force would sit there for ever
// with no path to the emergency duty. This arms a timer that does one thing -- read the
// temperature, and at or past the edge put the fan on the curve's own emergency duty.
// Below the edge it writes nothing at all.
//
// It deliberately does not return the fan to its curve and does not expire. Manual means
// manual: the operator chose this duty, and taking it away on a timer would be the guard
// overruling them. Only the emergency edge moves the fan, because only the emergency edge
// is the guard's business.
//
// Arming twice keeps the single timer, so a caller that arms on every poll cannot stack
// writes onto the same duty.
func (g *Guard) ArmThermalFloor() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.floorOn {
		return
	}
	g.floorOn = true
	g.armFloorLocked(g.d.Now())
}

// DisarmThermalFloor stands the floor down. Safe to call when it was never armed, so a
// caller restoring the daemon does not have to know whether it had been holding a floor.
func (g *Guard) DisarmThermalFloor() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.disarmFloorLocked()
}

func (g *Guard) disarmFloorLocked() {
	g.floorOn = false
	if g.floorTimer != nil {
		g.floorTimer.Stop()
		g.floorTimer = nil
	}
}

// armFloorLocked puts the next look on the schedule. Called with the lock held, and
// tolerant of a disarmed floor so a tick that raced a disarm cannot resurrect itself.
func (g *Guard) armFloorLocked(now time.Time) {
	if !g.floorOn {
		return
	}
	if g.floorTimer != nil {
		g.floorTimer.Stop()
	}
	g.floorTimer = g.d.AfterFunc(thermalFloorInterval, func() { g.floorTick() })
}

// floorTick is the floor's only action. It re-arms itself on every path out, including
// the ones where it declined to write: a floor that stopped checking because one read
// went wrong would be the one failure it exists to prevent.
func (g *Guard) floorTick() {
	g.mu.Lock()
	defer g.mu.Unlock()

	if !g.floorOn {
		return
	}
	now := g.d.Now()

	// The floor stands in for a daemon that was asked to stop. If the daemon is running
	// it is the control loop again, and the floor writes to a fan somebody else owns.
	if g.d.PfandUp() {
		g.disarmFloorLocked()
		return
	}

	// The curve is read on its own because a missing curve and a missing sensor are not
	// the same failure: without the curve there is no emergency duty to justify writing,
	// while without the sensor there is every reason to write the one we already know.
	c, cerr := g.d.ReadCurve()
	if cerr != nil || c == nil {
		g.armFloorLocked(now)
		return
	}
	temp, terr := g.d.ReadTempC()

	// An unreadable sensor is not proof the edge is clear. pfand treats the same reading
	// as the hottest it can prove and holds full speed; a floor that went quiet on a dead
	// sensor would be less safe than the daemon it replaced.
	if terr != nil || temp >= c.Emerg {
		g.holdFloorLocked(now, c.E, temp, terr)
	}
	g.armFloorLocked(now)
}

// holdFloorLocked writes the emergency duty and says so. A failed write is recorded as
// failed rather than dropped, because an operator looking at a cooling fan needs to see
// that the floor tried and could not.
func (g *Guard) holdFloorLocked(now time.Time, duty, temp int, readErr error) {
	err := g.holdEmergencyLocked(now, duty)
	e := Event{
		Op: OpThermalFloor, Actor: thermalFloorActor, Level: duty, At: g.d.Now(),
	}
	if err != nil {
		e.Outcome = OutcomeFailed
		e.Reason = err.Error()
		if readErr != nil {
			e.Reason = "the package temperature could not be read: " + readErr.Error()
		}
	} else {
		e.Outcome = OutcomeHeld
		e.Reason = "the curve daemon is not running; the emergency duty is held by the guard"
	}
	g.d.Audit(e)
}

// The gates a regress can decline at, named so an audit entry never carries prose.
const (
	DeclineNone        = "regress_nothing_pending"
	DeclineDaemon      = "regress_daemon_owns_fan"
	DeclineUnreadable  = "regress_temperature_unreadable"
	DeclineAtEmergency = "regress_at_emergency_edge"
	DeclineNoHandover  = "regress_no_handover_value"
)

var declineMessages = map[string]string{
	DeclineNone:        "no manual duty was pending",
	DeclineDaemon:      "the daemon owns the fan",
	DeclineUnreadable:  "the package temperature could not be established",
	DeclineAtEmergency: "the package is at or above the emergency edge",
	DeclineNoHandover: "no duty returns this fan to its curve: the firmware writes " +
		"zero to the EC as zero percent, so the duty is held until an operator moves it",
}

// auditDeclined records an attempt that stopped short of the device without failing.
// outcome and level are the caller's to set: the emergency gate wrote the curve's duty
// and reports held at that level; the gates that wrote nothing report refused at 0.
func (g *Guard) auditDeclined(op Op, actor, code string, outcome Outcome, level int) {
	g.d.Audit(Event{
		Op: op, Outcome: outcome, Level: level, Actor: actor,
		Code: code, Message: declineMessages[code], At: g.d.Now(),
	})
}
