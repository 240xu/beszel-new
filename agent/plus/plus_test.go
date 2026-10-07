//go:build testing

package plus

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCollectReturnsNonNilWithEmptyRoot(t *testing.T) {
	s, err := Collect(t.TempDir(), nil, nil)
	require.NoError(t, err)
	require.NotNil(t, s)
}

func TestCollectOmitsEmptySections(t *testing.T) {
	s, err := Collect(t.TempDir(), nil, nil)
	require.NoError(t, err)
	assert.Nil(t, s.DPTF)
	assert.Nil(t, s.Cpufreq)
	assert.Nil(t, s.NetAll)
	assert.Nil(t, s.Nvme)
	assert.Nil(t, s.WiFi)
	assert.Nil(t, s.Fan)
	assert.Nil(t, s.FanSteps)
}

// prodSources are the collector inputs that live outside sysfs, so no relative
// join onto the root can reach them. pfanel's history database used to be listed
// here and was removed in Task 9: package agent/pfanel opens it now, because the
// sqlite driver reaches os/exec through modernc.org/libc and this package's fork
// policy forbids that. See FanSample's doc comment.
var prodSources = []string{"/proc/net/wireless"}

// sysfsSubpaths are the sysfs-relative locations the other four collectors glob.
var sysfsSubpaths = []string{
	filepath.Join("class", "thermal", "thermal_zone0"),
	filepath.Join("devices", "system", "cpu", "cpufreq"),
	filepath.Join("class", "net"),
	filepath.Join("bus", "acpi", "devices", "INT3404:00"),
}

// TestSourcePathKeepsProductionPathsForProductionRoot guards the trap that
// makes a naive join wrong: /sys is itself the production root, so re-rooting
// would produce /sys/proc/net/wireless and the collector would silently read
// nothing.
func TestSourcePathKeepsProductionPathsForProductionRoot(t *testing.T) {
	for _, root := range []string{ProdRoot, ""} {
		for _, prod := range prodSources {
			assert.Equal(t, prod, sourcePath(root, prod),
				"root %q is production and must not re-root %q", root, prod)
		}
	}
}

// TestSourcePathInjectsFixtureRoot proves a fixture root actually redirects the
// non-sysfs inputs, which is what makes those collectors testable.
func TestSourcePathInjectsFixtureRoot(t *testing.T) {
	root := t.TempDir()
	for _, prod := range prodSources {
		assert.Equal(t, filepath.Join(root, prod), sourcePath(root, prod),
			"fixture root must re-root %q under the fixture tree", prod)
	}
}

// TestCollectAcceptsFixtureTreeWithEveryCollectorSource builds the whole input
// layout Collect hands down and checks every source resolves inside it, so the
// wiring can be exercised without touching the real filesystem. pfanel's database
// is not among them any more: it is read by package agent/pfanel and handed in as a
// FanSample, so an empty file at that path proves nothing here.
func TestCollectAcceptsFixtureTreeWithEveryCollectorSource(t *testing.T) {
	root := t.TempDir()

	for _, prod := range prodSources {
		path := sourcePath(root, prod)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, nil, 0o644))
		assert.FileExists(t, path)
	}
	for _, sub := range sysfsSubpaths {
		path := filepath.Join(root, sub)
		require.NoError(t, os.MkdirAll(path, 0o755))
		assert.DirExists(t, path)
	}

	// Collect must walk the fixture tree without error, and while the
	// collectors are still stubs it must report every section as absent.
	s, err := Collect(root, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, s)
	assert.Nil(t, s.WiFi)
	assert.Nil(t, s.Nvme)
	assert.Nil(t, s.Fan)
	assert.Nil(t, s.FanSteps)
	assert.Nil(t, s.DPTF)
	assert.Nil(t, s.Cpufreq)
	assert.Nil(t, s.NetAll)
}

// populatedStats fills every section so one marshal exercises every json tag in
// the package.
func populatedStats() Stats {
	return Stats{
		DPTF: &DPTFStats{Zones: map[string]float64{"x86_pkg_temp": 43, "SEN1": 38}},
		Cpufreq: &CpufreqStats{
			Cores:    []CpufreqCore{{Idx: 0, FreqMHz: 2800, MinMHz: 800, MaxMHz: 4200}},
			Governor: "powersave",
		},
		NetAll: map[string]*NetIfaceStats{
			"wlp0s20f3": {RxBytes: 1 << 30, TxBytes: 1 << 29, RxPackets: 10, TxPackets: 20, OperState: "up", SpeedMBit: 866, MTU: 1500},
		},
		Nvme: &NvmeStats{
			Model: "WD SN770", PercentageUsedPct: 1, UnsafeShutdowns: 2, MediaErrors: 3,
			AvailableSparePct: 100, DataUnitsRead: 4, DataUnitsWritten: 5, PowerOnHours: 6,
			PowerCycles: 7, ControllerBusyMin: 8, CriticalCompTimeMin: 9,
			TempC: 41.5,
		},
		WiFi:     &WiFiStats{SignalDbm: -53, TxMBit: 780, FreqMHz: 5180, Retry: 4, Iface: "wlp0s20f3"},
		Fan:      &FanStats{SetLevelPct: intPtr(55), EC76Pct: intPtr(60), RPMEst: 2400, RPMEstFlag: true, Gov: "curve", SampleTsS: 1791333981, TempC: 44},
		FanSteps: []FanSpeedStep{{Percent: 0, RPMEst: 0, NoiseMdb: 20, PowerMw: 3}, {Percent: 100, RPMEst: 5200, NoiseMdb: 45, PowerMw: 11}},
	}
}

// unitTags lists every json tag in the package that names a physical quantity,
// so each one must be present in a fully populated payload. Pure counters are
// included because their names carry the counted unit.
var unitTags = []string{
	// cpufreq -- idx is a CPU number, not a physical quantity, so it is not
	// listed here; TestCpufreqCoreWireShape owns it.
	"freq_mhz", "min_mhz", "max_mhz",
	// netall
	"rx_bytes", "tx_bytes", "rx_packets", "tx_packets", "speed_mbit", "mtu",
	// nvme -- idx-style key names aside, the two percentages carry _pct and the
	// two NVMe Time attributes carry _min; nvme_test.go owns the full payload.
	"percentage_used_pct", "available_spare_pct", "data_units_read", "data_units_written",
	"power_on_hours", "power_cycles", "unsafe_shutdowns", "media_errors",
	"controller_busy_min", "critical_comp_time_min", "temp_c",
	// wifi
	"signal_dbm", "tx_mbit", "retry",
	// fan
	"set_level_pct", "ec76_pct", "rpm_est", "rpm_est_is_estimate", "temp_c",
	// fan -- gov is a mode name rather than a quantity, so only sample_ts_s is
	// listed here; its _s suffix is what tells a consumer the unit is seconds.
	"sample_ts_s",
	// fan steps
	"percent", "rpm_est", "noise_mdb", "power_mw",
}

func TestStatsUseUnitSuffixes(t *testing.T) {
	b, err := json.Marshal(populatedStats())
	require.NoError(t, err)
	str := string(b)

	// Map keys are ACPI zone types, which name a physical quantity too.
	assert.Contains(t, str, `"x86_pkg_temp"`)
	for _, want := range unitTags {
		assert.Contains(t, str, `"`+want+`"`, "unit tag %q missing from the marshalled payload", want)
	}
}

// intPtr builds the *int fields FanStats uses to keep a real 0 distinct from an
// unknown, which is the whole reason those two fields are pointers.
func intPtr(v int) *int { return &v }

// ambiguousTags are names the unit contract rejects: bare physical units and
// the megabit/megabyte-ambiguous "_mbps".
//
// speed_mbps was NetIfaceStats' tag and the one known offender; Task 5 renamed
// it to speed_mbit, so the fixture below includes NetAll again and this assertion
// guards the rename from both sides: a reverted tag reappears here, and the
// populated fixture keeps speed_mbit covered by TestStatsUseUnitSuffixes.
var ambiguousTags = []string{
	"speed_mbps", "speed_mb", "speed",
	"temp", "temperature", "temp_celsius", "degrees_celsius",
	"mhz", "dbm", "mw", "mbit",
}

func TestStatsOmitAmbiguousUnitNames(t *testing.T) {
	s := populatedStats()
	b, err := json.Marshal(s)
	require.NoError(t, err)
	str := string(b)

	for _, bad := range ambiguousTags {
		assert.NotContains(t, str, `"`+bad+`"`, "ambiguous unit name %q must not appear", bad)
	}
}
