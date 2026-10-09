package fanapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/henrygd/beszel/agent/plus"
)

// This file is the only place that decides what a caller is told. The guard
// decides what happens to the fan; the wire decides what the operator is told
// happened. Keeping the two apart is what lets the guard keep saying "the bytes
// reached the disk" while the response says "that is not in force yet".

// Status values the wire reports, so a caller branches on a name and not on a
// number that a future edit is free to move.
const (
	StatusApplied        = "applied"
	StatusPendingRestart = "pending_restart"
)

// NextAction values. A pending change with no way to finish it is a panel that
// makes the operator press save until something appears to change.
const (
	ActionRestartPfand = "restart_pfand"
)

// Description is the wire form of a Result.
//
// Applied is deliberately not Result.Applied: a curve on disk that no daemon has
// read is not in force, and a panel told "applied" renders "saved".
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
	// The zero time would serialise as year one and invite a countdown to it.
	if !r.RegressAt.IsZero() {
		at := r.RegressAt
		d.RegressAt = &at
	}
	return d
}

// Fault is a refusal or a failure in wire form. Code is what a caller branches
// on; Message is prose for a log or a one-line notice. Neither carries the
// wrapped error: the errors this package wraps name the device, the lock and the
// curve file, and none of those belong in an HTTP body.
type Fault struct {
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Classify maps an error onto the wire.
//
// The order is the guard's order: a refusal that named the guard which stood in
// the way is reported as that guard, and the latch only shows once the guards
// are out of the way. Reordering this would tell an operator to wait out a
// cooldown when the thing in front of them is the answer.
func Classify(err error) Fault {
	for _, c := range classifications {
		if errors.Is(err, c.sentinel) {
			return Fault{Status: c.status, Code: c.code, Message: c.message}
		}
	}
	return internalFault
}

// classifications is ordered, and the order is load-bearing: rollback before
// unconfirmed, because an unconfirmed write whose restore failed leaves the fan
// holding a duty nobody asked for, which is the part to act on tonight.
// ErrWriteFailed is a device that refused the program outright, as opposed to
// one that took the write and then disagreed about the result. The two are
// different incidents: the first is the fan control path being broken, the
// second is a lost race against another writer. Collapsing them would report a
// broken path as a lost race and vice versa.
var ErrWriteFailed = errors.New("the fan control device refused the write")

var classifications = []struct {
	sentinel error
	status   int
	code     string
	message  string
}{
	{ErrRollbackFailed, http.StatusInternalServerError, "rollback_failed",
		"the fan is holding an unrequested duty: the previous one could not be restored"},
	{ErrWriteUnconfirmed, http.StatusInternalServerError, "write_unconfirmed",
		"the fan is not holding the duty that was requested"},
	{ErrWriteFailed, http.StatusInternalServerError, "write_failed",
		"the fan control device refused the write"},
	{ErrNotConfirmed, http.StatusBadRequest, "confirm_required",
		"the change must be confirmed before the fan is touched"},
	{plus.ErrFanLevelNotAllowed, http.StatusBadRequest, "level_not_allowed",
		"that duty is not one the firmware accepts"},
	{ErrCurveRejected, http.StatusBadRequest, "curve_rejected",
		"the curve is not one this host would run"},
	{plus.ErrPfandActive, http.StatusConflict, "pfand_active",
		"the curve daemon owns the fan; stop it before setting a duty by hand"},
	// The two temperature facts share a status on purpose: neither is a fault in
	// the request, and both mean the write did not happen. They must not share a
	// code or a message, because one says the machine is too hot and the other
	// says the sensor cannot be read, and an operator who is told the first will
	// cool a machine that was never hot.
	{plus.ErrPackageTemperature, http.StatusServiceUnavailable, "package_too_hot",
		"the package is at or above the emergency edge in your curve, so no duty was written"},
	{ErrTemperatureUnknown, http.StatusServiceUnavailable, "temperature_unknown",
		"the package temperature could not be read, so no duty was written"},
	{ErrBreakerOpen, http.StatusServiceUnavailable, "breaker_open",
		"fan control is latched open after repeated failures; nothing was written"},
	{plus.ErrFanControlUnsupported, http.StatusNotImplemented, "unsupported",
		"fan control is only implemented on linux"},
}

var internalFault = Fault{
	Status:  http.StatusInternalServerError,
	Code:    "internal",
	Message: "the write did not complete",
}

// messageOf is the prose that goes with a code, so the trail and the response
// cannot offer two texts for one refusal.
func messageOf(code string) string {
	for _, c := range classifications {
		if c.code == code {
			return c.message
		}
	}
	return ""
}

// codeOf names the sentinel an error carries, for the audit trail. It is the
// same chain Classify walks, so the record and the response cannot disagree
// about why a write was refused.
func codeOf(err error) string {
	if err == nil {
		return ""
	}
	for _, c := range classifications {
		if errors.Is(err, c.sentinel) {
			return c.code
		}
	}
	return "internal"
}

// decline codes. regressLocked reports which gate declined with a phrase, and
// the phrase is the wire text, so the two cannot drift apart.
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

// codeOfDecline names a decline by its phrase. The phrases are unexported
// constants of this package rather than operator input, and TestEveryDeclineIs
// Coded fails if one is renamed without a code.
func codeOfDecline(reason string) (code, message string) {
	for c, m := range declineMessages {
		if reason == m {
			return c, m
		}
	}
	return "regress_declined", reason
}

// CodeOf names a refusal for the audit trail. It is the same chain Classify
// walks, so the record and the response cannot disagree about why a write was
// refused. It is exported because the event carries the code, not the message:
// recovering one from the other would mean matching English prose.
func CodeOf(err error) string { return codeOf(err) }

// DeclineCode names a declined regress by its reason. The reasons are constants
// of this package, and TestEveryDeclineIsCoded fails if one is renamed.
func DeclineCode(reason string) (code, message string) { return codeOfDecline(reason) }
