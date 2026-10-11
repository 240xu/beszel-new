package plus

// NvmeInput is upstream's already-parsed SMART data in the shape this package can
// read without importing internal/entities/smart.
//
// The agent/plus boundary forbids importing internal/entities/*, so the
// adaptation happens at the injection point instead: agent/system.go reads
// *smart.SmartData and fills this in (see nvmePlusInput in agent/system.go). Only
// the fields beszel-new projects are carried, and Attrs is upstream's attribute
// list verbatim rather than a re-typed copy -- a second typed struct would be a
// second place for a field to be renamed or transposed without a test noticing.
type NvmeInput struct {
	// Model is the drive's model string.
	Model string
	// TempC is the composite temperature in Celsius, from upstream's top-level
	// temperature field rather than the log's own copy of the same number.
	TempC float64
	// Attrs is upstream's parsed attribute list flattened to name -> raw value.
	// Upstream maps each NVMe health-log field onto a named attribute
	// (parseSmartForNvme in agent/smart.go), so the keys below are upstream's
	// own names, not this package's invention.
	Attrs map[string]uint64
}

// nvmeAttrs are the upstream attribute names collectNvme reads. They must match
// the literal names in parseSmartForNvme's attribute list; the all-or-nothing
// check in collectNvme is what turns an upstream rename into a missing section
// rather than a panel full of zeros.
var nvmeAttrs = []string{
	"AvailableSpare",
	"PercentageUsed",
	"DataUnitsRead",
	"DataUnitsWritten",
	"PowerOnHours",
	"PowerCycles",
	"UnsafeShutdowns",
	"MediaErrors",
	"ControllerBusyTime",
	"CriticalCompTime",
}

// collectNvme projects upstream's parsed NVMe health log into the beszel-new
// shape. It does not read the filesystem and does not fork smartctl.
//
// Rationale: upstream already runs smartctl (agent/smart.go) and already parses
// NVMeSmartHealthInformationLog into SmartData, including unsafe_shutdowns and
// media_errors. Running smartctl a second time here would fork a process on
// every collection cycle, duplicate a parse upstream has done, and bypass
// upstream's SMART refresh interval -- for the same numbers. It would also fail
// the same way upstream fails: on this NAS a non-root agent cannot read
// /dev/nvme0 at all, so plus would report nothing where upstream reports
// something, which is strictly worse than reusing what upstream has.
//
// Contract: the result is all-or-nothing. Every attribute in nvmeAttrs must be
// present, otherwise nil is returned. Upstream emits all of them unconditionally
// for a successfully parsed NVMe log, including when the value is legitimately 0,
// so absence means the log was never parsed or upstream renamed a field -- never
// "the drive reported zero". Reporting a partial struct instead would let a
// renamed field surface as a plausible 0, which is exactly the failure mode a
// consumer cannot detect.
//
// Contract: the counters are CUMULATIVE device-lifetime totals, not rates, and
// data_units_* are in NVMe's own 1000x1000-byte units (512,000 bytes per unit),
// not bytes. Consumers take their own delta and must guard it: the counters reset
// when the drive is power-cycled and an unguarded uint64 subtraction wraps.
func collectNvme(in *NvmeInput) *NvmeStats {
	if in == nil {
		return nil
	}
	for _, name := range nvmeAttrs {
		if _, ok := in.Attrs[name]; !ok {
			return nil
		}
	}
	// ControllerBusyTime and CriticalCompTime are NVMe's Time class attributes and
	// their unit is minutes, so the _min suffix is the value, not a minimum.
	f := func(name string) float64 { return float64(in.Attrs[name]) }
	u := func(name string) uint64 { return in.Attrs[name] }
	return &NvmeStats{
		Model:               in.Model,
		TempC:               in.TempC,
		AvailableSparePct:   f("AvailableSpare"),
		PercentageUsedPct:   f("PercentageUsed"),
		DataUnitsRead:       u("DataUnitsRead"),
		DataUnitsWritten:    u("DataUnitsWritten"),
		PowerOnHours:        u("PowerOnHours"),
		PowerCycles:         u("PowerCycles"),
		UnsafeShutdowns:     u("UnsafeShutdowns"),
		MediaErrors:         u("MediaErrors"),
		ControllerBusyMin:   u("ControllerBusyTime"),
		CriticalCompTimeMin: u("CriticalCompTime"),
	}
}
