//go:build testing

package plus

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// corePath builds the sysfs-shaped location of one logical CPU's cpufreq
// directory under root.
//
// The fixture root is a sysfs root, the same thing Collect receives in
// production, so the devices/system prefix lives here rather than inside the
// collector. Fixtures that dropped it would pass against a collector that
// globbed <root>/cpu[0-9]*/cpufreq and then read nothing at all on the NAS.
func corePath(root, core string) string {
	return filepath.Join(root, "devices", "system", "cpu", core, "cpufreq")
}

func TestCollectCpufreqReadsEveryCore(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 4; i++ {
		base := filepath.Join(root, "devices", "system", "cpu", "cpu"+fmt.Sprint(i), "cpufreq")
		writeFile(t, filepath.Join(base, "scaling_cur_freq"), "2800016\n")
		writeFile(t, filepath.Join(base, "scaling_min_freq"), "400000\n")
		writeFile(t, filepath.Join(base, "scaling_max_freq"), "4100000\n")
		writeFile(t, filepath.Join(base, "scaling_governor"), "powersave\n")
		writeFile(t, filepath.Join(base, "energy_performance_preference"), "balance_performance\n")
	}
	writeFile(t, filepath.Join(root, "devices", "system", "cpu", "intel_pstate", "status"), "active\n")
	writeFile(t, filepath.Join(root, "devices", "system", "cpu", "intel_pstate", "no_turbo"), "0\n")

	got := collectCpufreq(root)
	require.NotNil(t, got)
	assert.Equal(t, []uint32{2800, 2800, 2800, 2800}, got.FreqMHz)
	// The brief's draft asserted []uint32{400} and []uint32{4100} here, one
	// element each, while writing min/max into all four cores. That cannot hold
	// against the brief's own implementation, which appends min/max inside the
	// per-core loop, and it disagrees with the field types declared in plus.go,
	// where MinMHz and MaxMHz are []uint32 parallel to FreqMHz rather than the
	// scalar uint32 a single-value reading would need. So the assertions here
	// state the whole vector. Same intent as the draft -- min is 400 MHz and max
	// is 4100 MHz on every core -- just not truncated to one entry.
	assert.Equal(t, []uint32{400, 400, 400, 400}, got.MinMHz)
	assert.Equal(t, []uint32{4100, 4100, 4100, 4100}, got.MaxMHz)
	assert.Equal(t, "powersave", got.Governor)
	assert.Equal(t, "balance_performance", got.EPP)
	assert.Equal(t, "active", got.PstateStatus)
	require.NotNil(t, got.NoTurbo)
	assert.Equal(t, 0, *got.NoTurbo)
}

func TestCollectCpufreqSkipsCoresWithoutCpufreqDir(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "devices", "system", "cpu", "cpu0", "cpufreq")
	writeFile(t, filepath.Join(base, "scaling_cur_freq"), "800000\n")

	got := collectCpufreq(root)
	require.NotNil(t, got)
	assert.Equal(t, []uint32{800}, got.FreqMHz)
}

func TestCollectCpufreqReturnsNilWhenAbsent(t *testing.T) {
	assert.Nil(t, collectCpufreq(t.TempDir()))
}

// --- additions beyond the brief -------------------------------------------
// The brief's three fixtures do not discriminate truncation from rounding and
// none of them pins where under the sysfs root the glob looks. The ones below
// close both holes, using values actually read off this NAS.

// TestCollectCpufreqTruncatesToWholeMHz pins the Hz -> MHz conversion.
//
// FreqMHz is []uint32, so the sub-MHz remainder is lost by construction; the
// question is only which side of the remainder survives. Every fixture in the
// brief divides to a value whose fraction is under 0.5: 2800016 Hz is
// 2800.016 MHz and 800000 Hz is a whole 800, so a collector that rounded
// instead of truncating would leave all of them green. These two do
// discriminate, because 2970952 Hz is 2970.952 MHz and 1099830 Hz is 1099.830
// MHz -- both round up (2971, 1100) and both truncate down (2970, 1099). They
// are not invented: 2970952 was cpu0's scaling_cur_freq in the pre-task
// capture and 1099830 was cpu5's during this task's own read of /sys.
//
// The assertions are exact so a lost or invented digit is a failure rather
// than a tolerance nobody tightens. The dropped remainder is 952 Hz on one core
// and 830 Hz on the other; see the report for why uint32 is the wrong shape if
// that ever stops being acceptable.
func TestCollectCpufreqTruncatesToWholeMHz(t *testing.T) {
	cases := []struct {
		core string
		hz   string
		want uint32
	}{
		{core: "cpu0", hz: "2970952", want: 2970},
		{core: "cpu5", hz: "1099830", want: 1099},
	}
	for _, tc := range cases {
		t.Run(tc.core, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, filepath.Join(corePath(root, tc.core), "scaling_cur_freq"), tc.hz+"\n")

			got := collectCpufreq(root)
			require.NotNil(t, got)
			assert.Equal(t, []uint32{tc.want}, got.FreqMHz)
		})
	}
}

// TestCollectReadsCpufreqThroughCollect is the wiring guard for the production
// path, and it exists because of how Task 2 shipped: a collector whose glob was
// already pre-joined passed every fixture test and then read nothing in
// production. Collect receives the sysfs mount point ("/sys" via ProdRoot), so
// the cpu tree is only reachable at <root>/devices/system/cpu, never at
// <root>/cpu. This fixture is written in the sysfs shape and asserted through
// Collect, so a prefix that is one level short fails here.
func TestCollectReadsCpufreqThroughCollect(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(corePath(root, "cpu0"), "scaling_cur_freq"), "4100000\n")

	s, err := Collect(root)
	require.NoError(t, err)
	require.NotNil(t, s.Cpufreq, "cpufreq must survive the Collect wiring")
	assert.Equal(t, []uint32{4100}, s.Cpufreq.FreqMHz)
}

// TestCollectCpufreqIgnoresCpuTreeOutsideSysfsPrefix pins the glob depth with a
// decoy, the way TestCollectDPTFIgnoresUnrelatedFilesUnderClassThermal does for
// thermal zones.
//
// Dropping "devices/system" from the prefix is the plausible off-by-one-level
// mistake, and on this machine it fails closed and silently: a shallower glob
// matches zero directories, collectCpufreq returns nil, and Collect omits the
// key entirely. A decoy carrying a complete and readable cpufreq tree at the
// shallow path turns that silent empty into a wrong non-empty answer, so the
// correct tree beside it has to be the one that is reported.
func TestCollectCpufreqIgnoresCpuTreeOutsideSysfsPrefix(t *testing.T) {
	root := t.TempDir()
	decoy := filepath.Join(root, "cpu0", "cpufreq")
	writeFile(t, filepath.Join(decoy, "scaling_cur_freq"), "9999999\n")
	writeFile(t, filepath.Join(decoy, "scaling_governor"), "performance\n")
	writeFile(t, filepath.Join(corePath(root, "cpu0"), "scaling_cur_freq"), "2970952\n")
	writeFile(t, filepath.Join(corePath(root, "cpu0"), "scaling_governor"), "powersave\n")

	got := collectCpufreq(root)
	require.NotNil(t, got)
	assert.Equal(t, []uint32{2970}, got.FreqMHz)
	assert.Equal(t, "powersave", got.Governor)
}

// TestCollectCpufreqSkipsUnparsableCurFreq pins the two bail-outs inside the
// per-core loop. sysfs is readable but not trusted: scaling_cur_freq is a
// kernel-internal value and a corrupt or truncated read must drop that one core
// without taking the whole collector down with it.
func TestCollectCpufreqSkipsUnparsableCurFreq(t *testing.T) {
	t.Run("garbage", func(t *testing.T) {
		root := t.TempDir()
		writeFile(t, filepath.Join(corePath(root, "cpu0"), "scaling_cur_freq"), "not-a-number\n")

		assert.Nil(t, collectCpufreq(root))
	})

	t.Run("absent", func(t *testing.T) {
		root := t.TempDir()
		writeFile(t, filepath.Join(corePath(root, "cpu0"), "scaling_governor"), "powersave\n")

		assert.Nil(t, collectCpufreq(root))
	})
}

// TestCollectCpufreqReportsAllEightLogicalCores records what the live machine
// actually yields, because the count is a design decision rather than an
// accident.
//
// This NAS is an i5-8365U: four physical cores, eight logical, two SMT
// siblings per core. sysfs exposes a cpufreq directory for all eight logical
// CPUs and siblings do not share one scaling_cur_freq, so cpu[0-9]* yields eight
// elements -- which is what Beszel counts as CPUs. On an idle machine siblings
// frequently report the same frequency, so an eight-element vector is not eight
// independent measurements. This test asserts the shape (8 in, 8 out, indices
// intact) rather than endorsing it as correct; see the report.
func TestCollectCpufreqReportsAllEightLogicalCores(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 8; i++ {
		core := "cpu" + fmt.Sprint(i)
		// Distinct MHz per core so a collector that collapsed the vector, or
		// re-ordered it, cannot pass.
		writeFile(t, filepath.Join(corePath(root, core), "scaling_cur_freq"), fmt.Sprint(2000000+i*1000)+"\n")
	}

	got := collectCpufreq(root)
	require.NotNil(t, got)
	assert.Len(t, got.FreqMHz, 8)
	assert.Equal(t, []uint32{2000, 2001, 2002, 2003, 2004, 2005, 2006, 2007}, got.FreqMHz)
}
