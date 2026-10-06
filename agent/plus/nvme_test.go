//go:build testing

package plus

import (
	"encoding/json"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// nvmeAttrsFixture mirrors what upstream's parseSmartForNvme produces for a real
// drive, read out of `sudo smartctl -a -j /dev/nvme0` during the task. The log
// carries 18 fields, of which upstream maps the 17 scalars into named attributes
// — the 18th is a slice SmartAttribute cannot hold — and these are the ten
// Beszelplus projects.
//
// The values are deliberately not the ones the plan's fixture used. A field
// mapping can only be caught if the source values are distinguishable, and the
// plan's fixture gave percentage_used, media_errors and critical_comp_time all
// the same value, so a test built on it cannot tell those three apart at all.
// Every value here is distinct instead. They are a captured snapshot, not live
// readings: the drive's own counters only ever move upwards, so no comment in
// this file may quote a counter value as if it were a fact about the machine.
func nvmeAttrsFixture() map[string]uint64 {
	return map[string]uint64{
		"CriticalWarning":         0,
		"Temperature":             32,
		"AvailableSpare":          100,
		"AvailableSpareThreshold": 10,
		"PercentageUsed":          7,
		"DataUnitsRead":           2324984,
		"DataUnitsWritten":        764742,
		"HostReads":               25296779,
		"HostWrites":              14232793,
		"ControllerBusyTime":      46,
		"PowerCycles":             34,
		"PowerOnHours":            183,
		"UnsafeShutdowns":         19,
		"MediaErrors":             5,
		"NumErrLogEntries":        3,
		"WarningTempTime":         11,
		"CriticalCompTime":        13,
	}
}

func nvmeInputFixture() *NvmeInput {
	return &NvmeInput{
		Model: "HFM128GD3GX013N-SKhynix",
		TempC: 32,
		Attrs: nvmeAttrsFixture(),
	}
}

// TestCollectNvmeCopiesUpstreamFields pins each source value to its own
// destination. Every fixture value is distinct, so any transposition -- the
// unsafe_shutdowns/media_errors swap being the obvious one -- moves a different
// number into the wrong field and fails here.
func TestCollectNvmeCopiesUpstreamFields(t *testing.T) {
	attrs := nvmeAttrsFixture()
	got := collectNvme(&NvmeInput{Model: "HFM128GD3GX013N-SKhynix", TempC: 32, Attrs: attrs})
	require.NotNil(t, got)

	assert.Equal(t, "HFM128GD3GX013N-SKhynix", got.Model)
	assert.Equal(t, 32.0, got.TempC)
	assert.Equal(t, 100.0, got.AvailableSparePct, "available_spare is a percentage")
	assert.Equal(t, 7.0, got.PercentageUsedPct, "percentage_used is a percentage")
	assert.Equal(t, uint64(2324984), got.DataUnitsRead)
	assert.Equal(t, uint64(764742), got.DataUnitsWritten)
	assert.Equal(t, uint64(183), got.PowerOnHours)
	assert.Equal(t, uint64(34), got.PowerCycles)
	assert.Equal(t, uint64(19), got.UnsafeShutdowns, "must not carry media_errors' value")
	assert.Equal(t, uint64(5), got.MediaErrors, "must not carry unsafe_shutdowns' value")
	// controller_busy_time and critical_comp_time are NVMe Time class attributes
	// in minutes, so 46 and 13 must land on the two _min fields unchanged.
	assert.Equal(t, uint64(46), got.ControllerBusyMin)
	assert.Equal(t, uint64(13), got.CriticalCompTimeMin)
}

// TestCollectNvmeIgnoresUnprojectedAttributes pins that upstream's extra seven
// attributes do not leak into the payload. There is no field for
// available_spare_threshold, critical_warning or the host_reads/host_writes pair,
// and inventing one would put a bare number on the wire with no name for its
// meaning.
func TestCollectNvmeIgnoresUnprojectedAttributes(t *testing.T) {
	got := collectNvme(nvmeInputFixture())
	require.NotNil(t, got)

	b, err := json.Marshal(got)
	require.NoError(t, err)
	str := string(b)
	for _, absent := range []string{
		"available_spare_threshold", "critical_warning", "host_reads", "host_writes",
		"num_err_log_entries", "warning_temp_time", "temperature_sensors",
	} {
		assert.NotContains(t, str, absent, "%q is not projected and must not appear", absent)
	}
}

// TestCollectNvmeReturnsNilWithoutUpstreamData covers the two ways there is
// nothing to report: no upstream data at all, and upstream data for a drive that
// is not NVMe (or whose health log never parsed). Neither may become a
// zero-filled struct -- that is indistinguishable from a healthy drive reporting
// zeroes, and it is what makes an SSD panel read "0% used, 0 media errors"
// forever.
func TestCollectNvmeReturnsNilWithoutUpstreamData(t *testing.T) {
	assert.Nil(t, collectNvme(nil), "absent upstream data must not become a zero struct")

	t.Run("non-nvme attribute set", func(t *testing.T) {
		assert.Nil(t, collectNvme(&NvmeInput{
			Model: "WDC WD40EFRX",
			TempC: 35,
			// A SATA drive's attributes: none of the NVMe health-log names.
			Attrs: map[string]uint64{"Power_On_Hours": 9000, "Reallocated_Sector_Ct": 0},
		}), "an ATA attribute list must not produce an NVMe panel")
	})

	t.Run("empty attribute list", func(t *testing.T) {
		assert.Nil(t, collectNvme(&NvmeInput{Model: "x", TempC: 20}))
	})
}

// TestCollectNvmeIsAllOrNothing pins the presence check one attribute at a time.
// Upstream emits all ten unconditionally for a parsed NVMe log, including when the
// value is 0, so a missing name means upstream renamed or dropped it -- and the
// only safe answer then is to report nothing.
func TestCollectNvmeIsAllOrNothing(t *testing.T) {
	for _, name := range nvmeAttrs {
		t.Run(name, func(t *testing.T) {
			attrs := nvmeAttrsFixture()
			delete(attrs, name)
			assert.Nil(t, collectNvme(&NvmeInput{Attrs: attrs}),
				"a partial attribute set must yield nil, not a struct of zeros")
		})
	}

	t.Run("all present with zero values is still reported", func(t *testing.T) {
		attrs := make(map[string]uint64, len(nvmeAttrs))
		for _, name := range nvmeAttrs {
			attrs[name] = 0
		}
		got := collectNvme(&NvmeInput{Attrs: attrs})
		require.NotNil(t, got, "presence is the signal, not the value")
		assert.Equal(t, uint64(0), got.UnsafeShutdowns)
		assert.Equal(t, uint64(0), got.MediaErrors)
	})
}

// TestNvmeStatsUseUnitSuffixes pins the wire names. The _pct suffix on the two
// percentages and the _min suffix on the two NVMe Time attributes are the
// decision points; a consumer reading percentage_used as a ratio would show
// "0.07% used" on a drive that is 7% used.
func TestNvmeStatsUseUnitSuffixes(t *testing.T) {
	got := collectNvme(nvmeInputFixture())
	require.NotNil(t, got)
	b, err := json.Marshal(got)
	require.NoError(t, err)
	str := string(b)

	for _, want := range []string{
		`"temp_c"`, `"available_spare_pct"`, `"percentage_used_pct"`,
		`"controller_busy_min"`, `"critical_comp_time_min"`,
	} {
		assert.Contains(t, str, want, "unit tag %s missing from the payload", want)
	}
	for _, gone := range []string{
		`"percentage_used"`, `"available_spare"`, `"critical_comp_temp_min"`, `"temp_sensor2_c"`,
	} {
		assert.NotContains(t, str, gone, "%s is a pre-Task-6 name and must not reappear", gone)
	}
	// The whole payload, so a field added later without a unit cannot slip past a
	// substring check on the names above.
	assert.JSONEq(t, `{
		"model":"HFM128GD3GX013N-SKhynix","temp_c":32,"available_spare_pct":100,
		"percentage_used_pct":7,"data_units_read":2324984,"data_units_written":764742,
		"power_on_hours":183,"power_cycles":34,"unsafe_shutdowns":19,"media_errors":5,
		"controller_busy_min":46,"critical_comp_time_min":13
	}`, string(b))
}

// TestNvmeZeroValuesAreOmitted documents that every NVMe field carries
// omitempty, so a healthy drive with nothing to say sends no keys at all. This is
// the flip side of the all-or-nothing presence check: presence of an attribute is
// checked inside the package, presence of a key on the wire means "non-zero".
func TestNvmeZeroValuesAreOmitted(t *testing.T) {
	b, err := json.Marshal(&NvmeStats{})
	require.NoError(t, err)
	assert.JSONEq(t, `{}`, string(b))
}

// TestNvmeCollectorNeverForks pins the design decision that the NVMe collector
// reuses upstream's parse instead of running smartctl again.
//
// The invariant is structural, so it is checked structurally: no file in this
// package may import os/exec. There is no behavioural fixture that could prove
// this -- a fork only shows up as a slow test and a stray child process, neither
// of which fails anything -- so the import list is what gets asserted. A second
// smartctl would also bypass upstream's SMART refresh interval and, on a host
// where the agent cannot read /dev/nvme0, would silently report nothing where
// upstream reports real numbers.
func TestNvmeCollectorNeverForks(t *testing.T) {
	dir, err := os.Getwd()
	require.NoError(t, err, "the test must run in the package directory")
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	fset := token.NewFileSet()
	checked := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.ImportsOnly)
		require.NoErrorf(t, err, "cannot parse %s", name)
		checked++
		for _, imp := range file.Imports {
			path := imp.Path.Value
			assert.NotEqualf(t, `"os/exec"`, path,
				"%s imports os/exec: this package must not fork a process", name)
			assert.NotEqualf(t, `"syscall"`, path,
				"%s imports syscall: this package must not shell out to smartctl", name)
		}
	}
	assert.Greater(t, checked, 0, "no package source found -- the assertion proved nothing")
}

// TestCollectWiresNvmeThroughCollect is the wiring guard for the injected data:
// Collect must place the projection in the payload, and must omit the key
// entirely when there is no upstream data. Fixtures elsewhere call collectNvme
// directly, so nothing else would notice Collect dropping the argument.
func TestCollectWiresNvmeThroughCollect(t *testing.T) {
	t.Run("injected", func(t *testing.T) {
		s, err := Collect(t.TempDir(), nvmeInputFixture())
		require.NoError(t, err)
		require.NotNil(t, s.Nvme, "nvme must survive the Collect wiring")
		assert.Equal(t, uint64(19), s.Nvme.UnsafeShutdowns)
	})

	t.Run("nil upstream data", func(t *testing.T) {
		s, err := Collect(t.TempDir(), nil)
		require.NoError(t, err)
		assert.Nil(t, s.Nvme)

		b, err := json.Marshal(s)
		require.NoError(t, err)
		assert.NotContains(t, string(b), `"nvme"`)
	})
}

// TestCollectNvmeIgnoresUpstreamTempSensors guards a decision, not code. Upstream
// cannot carry the log's temperature_sensors array into SmartData — agent/smart.go
// maps the log's 17 scalar fields and SmartAttribute has nowhere to put a slice —
// and it does not need to, because the second sensor already reaches the hub as
// the system temperature nvme_sensor_2. A copy inside the NVMe block would be the
// same number twice, so the field stays deliberately absent. If a future upstream
// revision does start carrying the array, this test is where the new field
// belongs.
func TestCollectNvmeIgnoresUpstreamTempSensors(t *testing.T) {
	got := collectNvme(nvmeInputFixture())
	require.NotNil(t, got)

	b, err := json.Marshal(got)
	require.NoError(t, err)
	assert.NotContains(t, string(b), "sensor2",
		"temp_sensor2_c would only duplicate upstream's nvme_sensor_2; see NvmeStats' doc")
}

// TestNvmeInputMarshalsForTheDevHarness keeps NvmeInput a plain wire type.
// pluscheck loads it from a file so a live run can exercise the NVMe projection
// without the agent's smartctl data, and that only works while the struct has no
// unexported state and no custom marshaller.
func TestNvmeInputMarshalsForTheDevHarness(t *testing.T) {
	b, err := json.Marshal(nvmeInputFixture())
	require.NoError(t, err)

	var back NvmeInput
	require.NoError(t, json.Unmarshal(b, &back))
	assert.Equal(t, "HFM128GD3GX013N-SKhynix", back.Model)
	assert.Equal(t, 32.0, back.TempC)
	assert.Equal(t, uint64(2324984), back.Attrs["DataUnitsRead"])
}

// TestNvmeInputAttrKeyNamesMatchUpstream keeps the attribute names in nvme.go
// spelled the way upstream's parseSmartForNvme spells them. The names are the
// contract with a file this package cannot see, and all-or-nothing turns a typo
// into a silently absent panel -- so the spellings are pinned here rather than
// left to a comment.
func TestNvmeInputAttrKeyNamesMatchUpstream(t *testing.T) {
	// The literal strings from parseSmartForNvme's attribute list in
	// agent/smart.go, transcribed independently of nvme.go's constants.
	upstream := []string{
		"CriticalWarning", "Temperature", "AvailableSpare", "AvailableSpareThreshold",
		"PercentageUsed", "DataUnitsRead", "DataUnitsWritten", "HostReads", "HostWrites",
		"ControllerBusyTime", "PowerCycles", "PowerOnHours", "UnsafeShutdowns",
		"MediaErrors", "NumErrLogEntries", "WarningTempTime", "CriticalCompTime",
	}
	for _, name := range nvmeAttrs {
		assert.Containsf(t, upstream, name,
			"nvme.go reads %q, which upstream's NVMe parse does not emit", name)
	}
}
