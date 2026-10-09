// Package fanapi guards every write that changes how the fan runs.
//
// agent/plus knows how to program a duty and how to validate a curve. This
// package decides whether the write is allowed, proves it landed, puts the fan
// back when it did not, and hands it back to the curve on a timer so a manual
// override cannot outlive its reason.
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

const (
	// breakerThreshold is how many writes in a row must fail before the control
	// path is presumed broken. A refusal by a guard does not count: that is the
	// guard working, not a device fault.
	breakerThreshold = 3
	// breakerCooldown is how long the path is left alone before one probe is
	// admitted: long enough not to hammer a failing device, short enough that a
	// transient fault clears unnoticed.
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
)

// Outcome separates "we said no" from "we tried and could not".
type Outcome string

const (
	OutcomeApplied Outcome = "applied"
	OutcomeRefused Outcome = "refused"
	OutcomeFailed  Outcome = "failed"
)

// Event is one audited attempt. Every path emits exactly one, including
// refusals: a write that never happened is what an incident review needs.
type Event struct {
	Op      Op
	Outcome Outcome
	Actor   string
	Level   int
	Reason  string
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
	probing  bool
}

// Guard is the guardrail. The mutex is what stops a second caller slipping past
// a half-open probe that is already in flight.
type Guard struct {
	d  Deps
	mu sync.Mutex

	brk       breaker
	regressAt time.Time
	timer     *time.Timer
}

// New builds a guard over the given seams.
func New(d Deps) *Guard {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Audit == nil {
		d.Audit = func(Event) {}
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
	if err := g.admit(now); err != nil {
		return Result{}, err
	}
	// The daemon rewrites the duty every interval, so a manual level programmed
	// under it would be overwritten before anyone could act on the panel's claim
	// that it was set. Refusing is honest; winning a race lost on the next tick
	// is not.
	if g.d.PfandUp() {
		return Result{}, fmt.Errorf("duty %d: %w", level, plus.ErrPfandActive)
	}
	temp, emerg, err := g.temperature()
	if err != nil {
		return Result{}, fmt.Errorf("duty %d: %w", level, err)
	}
	if temp >= emerg {
		return Result{}, fmt.Errorf("duty %d at %dC, emergency edge is %dC: %w",
			level, temp, emerg, plus.ErrPackageTemperature)
	}

	prev, err := g.d.GetLevel()
	if err != nil {
		g.fail(now)
		return Result{}, fmt.Errorf("read the duty to restore: %w", err)
	}
	if err := g.d.SetLevel(level); err != nil {
		g.fail(now)
		return Result{}, fmt.Errorf("program duty %d: %w", level, err)
	}
	got, err := g.d.GetLevel()
	if err != nil || got != level {
		g.fail(now)
		// A single-slot device has no way to say "your write lost", so the only
		// evidence is a read-back that disagrees with the request.
		unconfirmed := fmt.Errorf("asked for %d, device reports %d (%v): %w",
			level, got, err, ErrWriteUnconfirmed)
		if rerr := g.restoreLevel(prev); rerr != nil {
			// Both reasons are wrapped, not flattened to prose: errors.Is must still
			// see the original fault and still see that the restore failed.
			return Result{}, fmt.Errorf("%w, and %w", unconfirmed, rerr)
		}
		return Result{}, unconfirmed
	}
	g.record(now)
	g.scheduleRegressLocked(now)
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

// WriteCurve replaces the curve, adding confirmation, arbitration and a restore
// to the validator agent/plus already proved to be a superset of the host's.
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
	if err := g.admit(now); err != nil {
		return Result{}, err
	}
	if err := c.Validate(); err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrCurveRejected, err)
	}
	original, err := g.d.ReadCurveBytes()
	if err != nil {
		g.fail(now)
		return Result{}, fmt.Errorf("read the curve to restore: %w", err)
	}
	if err := g.d.WriteCurveBytes([]byte(c.Render())); err != nil {
		g.fail(now)
		return Result{}, fmt.Errorf("write curve: %w", err)
	}
	if err := g.verifyCurve(c); err != nil {
		g.fail(now)
		// The restore must reproduce the bytes, not re-render them: rendering
		// regenerates comments, so a restore built that way would not be the file
		// that was running a moment ago.
		if rerr := g.d.WriteCurveBytes(original); rerr != nil {
			return Result{}, fmt.Errorf("%w, and %w: restoring the previous curve: %v",
				err, ErrRollbackFailed, rerr)
		}
		return Result{}, err
	}
	g.record(now)
	return Result{Applied: true, NeedsPfandRestart: g.d.PfandUp()}, nil
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
// against. The edge comes from the operator's own curve, so the guard cannot
// drift away from the control law it protects. An unreadable sensor is not a
// safe one: the daemon reacts to a failed read with full speed, and refusing
// matches that posture instead of fighting it.
func (g *Guard) temperature() (temp, emerg int, err error) {
	c, err := g.d.ReadCurve()
	if err != nil {
		return 0, 0, fmt.Errorf("%w: read curve: %v", ErrTemperatureUnknown, err)
	}
	t, err := g.d.ReadTempC()
	if err != nil {
		return 0, 0, fmt.Errorf("%w: read package temperature: %v", ErrTemperatureUnknown, err)
	}
	return t, c.Emerg, nil
}

// admit decides whether this attempt may reach the device, and reserves a
// half-open probe if one is due.
func (g *Guard) admit(now time.Time) error {
	b := &g.brk
	switch b.state {
	case breakerOpen:
		if now.Sub(b.openedAt) < breakerCooldown {
			return ErrBreakerOpen
		}
		b.state, b.probing = breakerHalfOpen, true
	case breakerHalfOpen:
		if b.probing {
			return ErrBreakerOpen
		}
		b.probing = true
	}
	return nil
}

// fail records a broken write. A failed half-open probe reopens immediately: it
// is the one attempt that was allowed to prove the path still works.
func (g *Guard) fail(now time.Time) {
	b := &g.brk
	if b.state == breakerHalfOpen || b.probing {
		b.state, b.openedAt, b.probing, b.failures = breakerOpen, now, false, breakerThreshold
		return
	}
	b.failures++
	if b.failures >= breakerThreshold {
		b.state, b.openedAt = breakerOpen, now
	}
}

func (g *Guard) record(now time.Time) {
	g.brk = breaker{}
}

// Regress hands the fan back to the firmware curve once the manual duty has run
// its course. The bool separates "returned to auto" from "nothing to return"
// and from "declined to return".
func (g *Guard) Regress() (bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.d.Now()

	did, err := g.regressLocked(now)
	g.audit(OpRegress, Request{Actor: "regress"}, 0, 0, err)
	return did, err
}

func (g *Guard) regressLocked(now time.Time) (bool, error) {
	if g.regressAt.IsZero() || now.Before(g.regressAt) {
		return false, nil
	}
	// Two things can have changed while the duty was held. If the daemon is up it
	// owns the fan and our write would be undone on its next tick, so the pending
	// regress is dropped rather than fought over.
	if g.d.PfandUp() {
		g.clearRegressLocked()
		return false, nil
	}
	// At or past the emergency edge the daemon is driving full speed. Returning
	// control to the curve is harmless when they agree and harmful when they do
	// not, so the regress stays pending rather than written and forgotten.
	if temp, emerg, err := g.temperature(); err != nil || temp >= emerg {
		return false, nil
	}
	// Admission comes last: a regress that declined to write never touched the
	// device, and spending the probe here would leave the breaker waiting on a
	// write that did not happen.
	if err := g.admit(now); err != nil {
		return false, err
	}
	if err := g.d.SetLevel(0); err != nil {
		g.fail(now)
		return false, fmt.Errorf("return the fan to its curve: %w", err)
	}
	got, err := g.d.GetLevel()
	if err != nil || got != 0 {
		g.fail(now)
		return false, fmt.Errorf("%w: asked for the curve, device reports %d (%v)",
			ErrWriteUnconfirmed, got, err)
	}
	g.record(now)
	g.clearRegressLocked()
	return true, nil
}

func (g *Guard) scheduleRegressLocked(now time.Time) {
	g.regressAt = now.Add(RegressAfter)
	if g.timer != nil {
		g.timer.Stop()
	}
	g.timer = time.AfterFunc(RegressAfter, func() { _, _ = g.Regress() })
}

func (g *Guard) clearRegressLocked() {
	g.regressAt = time.Time{}
	if g.timer != nil {
		g.timer.Stop()
		g.timer = nil
	}
}

// PendingRegressAt reports when the fan is due back on its curve, if at all.
func (g *Guard) PendingRegressAt() time.Time {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.regressAt
}

// Close stops the pending regress timer.
func (g *Guard) Close() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.timer != nil {
		g.timer.Stop()
		g.timer = nil
	}
}

func (g *Guard) audit(op Op, req Request, level, got int, err error) {
	e := Event{Op: op, Outcome: OutcomeApplied, Actor: req.Actor, Level: level, At: g.d.Now()}
	if err != nil {
		e.Outcome, e.Reason = OutcomeFailed, err.Error()
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
