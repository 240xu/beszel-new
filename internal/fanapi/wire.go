package fanapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/henrygd/beszel/agent/plus"
)

// The guard decides what happens to the fan; this file decides what the operator
// is told happened. Keeping them apart lets the guard keep reporting "the bytes
// reached the disk" truthfully while the response declines to call it applied.

// Names rather than numbers, so a caller branching on one survives an edit that
// moves it.
const (
	StatusApplied        = "applied"
	StatusPendingRestart = "pending_restart"

	// A panel that only says pending leaves the operator pressing save.
	ActionRestartPfand = "restart_pfand"
)

// Description is the wire form of a Result. Applied is deliberately not
// Result.Applied: a curve no daemon has read is not in force, and a panel told
// "applied" renders "saved".
type Description struct {
	Applied           bool       `json:"applied"`
	Level             int        `json:"level"`
	NeedsPfandRestart bool       `json:"needs_pfand_restart"`
	Status            string     `json:"status"`
	NextAction        string     `json:"next_action,omitempty"`
	RegressAt         *time.Time `json:"regress_at,omitempty"`
}

// Describe maps a Result onto the wire.
func Describe(r Result) Description {
	d := Description{
		Applied:           r.Applied && !r.NeedsPfandRestart,
		Level:             r.Level,
		NeedsPfandRestart: r.NeedsPfandRestart,
		Status:            StatusApplied,
	}
	if r.NeedsPfandRestart {
		d.Status, d.NextAction = StatusPendingRestart, ActionRestartPfand
	}
	// The zero time would serialise as year one and invite a countdown.
	if !r.RegressAt.IsZero() {
		at := r.RegressAt
		d.RegressAt = &at
	}
	return d
}

// Fault is a wire refusal. Neither field carries the wrapped error: those name
// the device, the lock and the curve file, none of which belong in an HTTP body.
type Fault struct {
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Classify maps an error onto the wire, in the guard's order: the guard that
// stood in the way is the answer, and the latch only shows once the guards are
// out of the way. Reordering would send an operator to wait out a cooldown when
// the answer is already in front of them.
func Classify(err error) Fault {
	if f, ok := classify(err); ok {
		return f
	}
	return internalFault
}

// A device that refused the program outright, rather than one that took the write
// and then disagreed: a broken control path, not a lost race.
var ErrWriteFailed = errors.New("the fan control device refused the write")

// The order is load-bearing: rollback before unconfirmed, because a failed
// restore leaves the fan holding a duty nobody asked for.
type classification struct {
	Fault
	sentinel error
}

var classifications = []classification{
	{Status: http.StatusInternalServerError, Code: "rollback_failed",
		Message: "the fan is holding an unrequested duty: the previous one could not be restored", sentinel: ErrRollbackFailed},
	{Status: http.StatusInternalServerError, Code: "write_unconfirmed",
		Message: "the fan is not holding the duty that was requested", sentinel: ErrWriteUnconfirmed},
	{Status: http.StatusInternalServerError, Code: "write_failed",
		Message: "the fan control device refused the write", sentinel: ErrWriteFailed},
	{Status: http.StatusBadRequest, Code: "confirm_required",
		Message: "the change must be confirmed before the fan is touched", sentinel: ErrNotConfirmed},
	{Status: http.StatusBadRequest, Code: "level_not_allowed",
		Message: "that duty is not one the firmware accepts", sentinel: plus.ErrFanLevelNotAllowed},
	{Status: http.StatusBadRequest, Code: "curve_rejected",
		Message: "the curve is not one this host would run", sentinel: ErrCurveRejected},
	{Status: http.StatusConflict, Code: "pfand_active",
		Message: "the curve daemon owns the fan; stop it before setting a duty by hand", sentinel: plus.ErrPfandActive},
	// These two share a status: neither is a fault in the request. They must
	// not share a code or a message, because an operator told the first will
	// cool a machine whose sensor is simply broken.
	{Status: http.StatusServiceUnavailable, Code: "package_too_hot",
		Message: "the package is at or above the emergency edge in your curve, so no duty was written", sentinel: plus.ErrPackageTemperature},
	{Status: http.StatusServiceUnavailable, Code: "temperature_unknown",
		Message: "the package temperature could not be read, so no duty was written", sentinel: ErrTemperatureUnknown},
	{Status: http.StatusServiceUnavailable, Code: "breaker_open",
		Message: "fan control is latched open after repeated failures; nothing was written", sentinel: ErrBreakerOpen},
	{Status: http.StatusNotImplemented, Code: "unsupported",
		Message: "fan control is only implemented on linux", sentinel: plus.ErrFanControlUnsupported},
}

var internalFault = Fault{
	Status:  http.StatusInternalServerError,
	Code:    "internal",
	Message: "the write did not complete",
}

func classify(err error) (Fault, bool) {
	for _, c := range classifications {
		if errors.Is(err, c.sentinel) {
			return c.Fault, true
		}
	}
	return Fault{}, false
}

// The event carries the code and its own text rather than the error, so that
// classifying happens here where the error is in hand.
func codeOf(err error) string {
	if f, ok := classify(err); ok {
		return f.Code
	}
	return "internal"
}

func messageOf(code string) string {
	for _, c := range classifications {
		if c.Code == code {
			return c.Message
		}
	}
	return ""
}
