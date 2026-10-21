package fanapi

import (
	"time"

	"github.com/henrygd/beszel/agent/plus"
)

// Backend is the machine-specific half of fan control: how to read the package
// temperature, how to program and read a duty, whether the curve daemon is
// running, which duties this machine can actually write, and how to read and
// replace the curve file.
//
// The guard deliberately does not know which of these it is talking to. On this
// chassis the implementation is an ACPI/EC path (/dev/pfanctl + _FSL/_FPS), but
// the same contract fits hwmon (pwm1 + pwm1_enable), a vendor EC driver, or a
// daemon socket. A new machine should add a Backend and a profile, not another
// branch in the policy.
//
// Every method may fail: an unreadable temperature is not zero and an absent
// duty is not "off". The guard treats an error as a refusal, never as a value.
type Backend interface {
	// ReadTempC returns the package temperature in whole degrees Celsius.
	ReadTempC() (int, error)
	// SetLevel programs a duty percentage.
	SetLevel(level int) error
	// GetLevel reads the duty the device currently reports.
	GetLevel() (int, error)
	// PfandUp reports whether the curve daemon currently owns the fan.
	PfandUp() bool
	// ResolveDutySet reports profile ∩ firmware: the duties SetLevel will accept.
	ResolveDutySet() (plus.DutySet, error)
	// ReadCurve returns the curve the daemon would run.
	ReadCurve() (*plus.Curve, error)
	// ReadCurveBytes returns the raw bytes, for an exact restore.
	ReadCurveBytes() ([]byte, error)
	// WriteCurveBytes replaces the curve file.
	WriteCurveBytes([]byte) error
}

// Capabilities lets a Backend say what it can honestly do. The zero value is
// "everything the interface provides", so an existing backend keeps working
// without a new method. It exists for the differences a caller cannot infer
// from an error: a backend with no tachometer cannot report RPM at all, and a
// backend with no stored setpoint cannot answer GetLevel after a write.
type Capabilities struct {
	// RealRPM is true only when the device reports a measured fan speed. This
	// chassis reports 0xFFFFFFFF and the UI's rpm_est is derived from the duty
	// byte, so it must stay false here.
	RealRPM bool
	// ReadBack is true when GetLevel reflects the stored setpoint rather than a
	// synthesised value.
	ReadBack bool
	// CurveFile is true when ReadCurve/WriteCurveBytes address a real file the
	// daemon reads at startup (as opposed to a device register).
	CurveFile bool
}

// CapabilityReporter is optional so a Backend need not implement it.
type CapabilityReporter interface {
	Capabilities() Capabilities
}

// DepsFromBackend adapts a Backend to the guard's side-effect seams. The guard's
// Deps stays the narrow, testable surface it is today; this is the one place a
// machine-specific backend is bound to it.
func DepsFromBackend(b Backend, now func() time.Time, audit func(Event), after func(time.Duration, func()) *time.Timer) Deps {
	if now == nil {
		now = time.Now
	}
	if after == nil {
		after = time.AfterFunc
	}
	if audit == nil {
		audit = func(Event) {}
	}
	return Deps{
		Now:             now,
		ReadTempC:       b.ReadTempC,
		ReadCurve:       b.ReadCurve,
		SetLevel:        b.SetLevel,
		GetLevel:        b.GetLevel,
		PfandUp:         b.PfandUp,
		ResolveDutySet:  b.ResolveDutySet,
		ReadCurveBytes:  b.ReadCurveBytes,
		WriteCurveBytes: b.WriteCurveBytes,
		Audit:           audit,
		AfterFunc:       after,
	}
}

// BackendCapabilities reports a Backend's declared capabilities, or the zero
// value (meaning "not declared") when it does not implement the optional
// interface. Callers use this to label estimates rather than present them as
// measurements.
func BackendCapabilities(b Backend) Capabilities {
	if r, ok := b.(CapabilityReporter); ok {
		return r.Capabilities()
	}
	return Capabilities{}
}
