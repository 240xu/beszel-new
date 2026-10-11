// Package fanapi guards every write that changes how the fan runs. agent/plus
// knows how to program a duty and validate a curve; this package decides whether
// the write is allowed, proves it landed, puts the fan back when it did not, and
// hands it back to the curve on a timer.
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

// Event is one audited attempt. Every path emits exactly one, refusals included:
// a write that never happened is what an incident review needs.
// Code and Message carry the refusal in the form a caller can act on; Reason
// stays untranslated for a log.
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
// time" guarantee, which is why the breaker holds no reservation flag of its own:
// see admit.
type Guard struct {
	d  Deps
	mu sync.Mutex

	brk       breaker
	regressAt time.Time
	timer     *time.Timer
	// declines counts consecutive refusals, so the wait can grow
	declines int
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
	// The daemon rewrites the duty whenever its own result moves the fan by more
	// than its dead zone, so a manual level programmed under it would be undone a
	// tick or two later — before anyone could act on the panel's claim that it was
	// set. Refusing is honest; winning a race lost on the next tick is not. Its
	// fail-safe reads a device candidate list, so a host whose list is unreadable
	// has a daemon that logs and writes nothing at all: "the daemon is up" is the
	// conservative reading, and being wrong that way costs a refused write.
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
		g.fail(now)
		return Result{}, fmt.Errorf("read the duty to restore: %w", err)
	}
	if err := g.d.SetLevel(level); err != nil {
		g.fail(now)
		return Result{}, fmt.Errorf("program duty %d: %w: %v", level, ErrWriteFailed, err)
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
// against. The edge comes from the operator's own curve, so the guard cannot
// drift from the control law it protects. An unreadable sensor is not a safe one:
// the daemon answers a failed read with full speed, and refusing matches that.
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
// reservation to release: the flag used to have to be cleared by every admitted
// path that returned without writing, one forgot, and it outlived the call —
// after which admit refused every caller in perpetuity and only an agent restart
// recovered. A reservation that outlives its call is a latch nothing owns, so
// admission sits where a write must follow: past every guard.
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
	now := g.d.Now()

	did, held, err := g.regressLocked(now)
	if err == nil && !did {
		// A safety gate stopped short of the device and nothing failed, so there is
		// no error for audit to classify. Recording it as applied would tell an
		// operator the fan went back to its curve when it is still held at a duty.
		g.auditDeclined(OpRegress, "regress", held)
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

// holdEmergencyLocked puts the fan on the duty the curve itself commands here, and
// proves it landed. Only runs at or past the edge, so a refusal leaves the previous
// duty standing, reported.
func (g *Guard) holdEmergencyLocked(now time.Time, duty int) error {
	if err := g.admit(now); err != nil {
		return err
	}
	if err := g.d.SetLevel(duty); err != nil {
		g.fail(now)
		return fmt.Errorf("hold the emergency duty %d: %w", duty, err)
	}
	got, err := g.d.GetLevel()
	if err != nil || got != duty {
		g.fail(now)
		return fmt.Errorf("%w: asked for the emergency duty %d, device reports %d (%v)",
			ErrWriteUnconfirmed, duty, got, err)
	}
	g.record()
	return nil
}

// regressLocked also names which gate declined, so a handover that did not
// happen is still auditable.
func (g *Guard) regressLocked(now time.Time) (bool, string, error) {
	if g.regressAt.IsZero() || now.Before(g.regressAt) {
		return false, DeclineNone, nil
	}
	// Two things can have changed while the duty was held. If the daemon is up it
	// owns the fan and our write would be undone on its next tick, so the pending
	// regress is dropped rather than fought over.
	if g.d.PfandUp() {
		g.clearRegressLocked()
		return false, DeclineDaemon, nil
	}
	temp, c, err := g.temperature()
	if err != nil {
		return false, DeclineUnreadable, nil
	}
	// At or past the emergency edge the answer is the curve's emergency duty, so this
	// gate commands it rather than declining. It used to decline on the grounds that the
	// daemon was driving full speed, but the check above returns if one is up, so none is.
	if temp >= c.Emerg {
		return false, DeclineAtEmergency, g.holdEmergencyLocked(now, c.E)
	}
	// Admission last: a regress that declined to write never touched the device,
	// and letting it spend the probe would leave the breaker waiting for a write
	// that did not happen.
	if err := g.admit(now); err != nil {
		return false, "", err
	}
	if err := g.d.SetLevel(0); err != nil {
		g.fail(now)
		return false, "", fmt.Errorf("return the fan to its curve: %w", err)
	}
	got, err := g.d.GetLevel()
	if err != nil || got != 0 {
		g.fail(now)
		return false, "", fmt.Errorf("%w: asked for the curve, device reports %d (%v)",
			ErrWriteUnconfirmed, got, err)
	}
	g.record()
	g.clearRegressLocked()
	return true, "", nil
}

func (g *Guard) scheduleRegressLocked(now time.Time, delay time.Duration) {
	g.regressAt = now.Add(delay)
	if g.timer != nil {
		g.timer.Stop()
	}
	g.timer = g.d.AfterFunc(delay, func() { _, _ = g.Regress() })
}

// retryLocked hands the decision on after a gate declined: one-shot timers meant a
// handover that stopped short stayed stopped for good.
func (g *Guard) retryLocked(now time.Time) {
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

// The gates a regress can decline at, named rather than phrased so an audit
// entry never carries prose a panel would have to match.
const (
	DeclineNone        = "regress_nothing_pending"
	DeclineDaemon      = "regress_daemon_owns_fan"
	DeclineUnreadable  = "regress_temperature_unreadable"
	DeclineAtEmergency = "regress_at_emergency_edge"
)

var declineMessages = map[string]string{
	DeclineNone:        "no manual duty was pending",
	DeclineDaemon:      "the daemon owns the fan",
	DeclineUnreadable:  "the package temperature could not be established",
	DeclineAtEmergency: "the package is at or above the emergency edge",
}

// auditDeclined records an attempt that stopped short of the device without
// failing. The caller still gets no error, but the event says no write happened.
func (g *Guard) auditDeclined(op Op, actor, code string) {
	g.d.Audit(Event{
		Op: op, Outcome: OutcomeRefused, Actor: actor,
		Code: code, Message: declineMessages[code], At: g.d.Now(),
	})
}
