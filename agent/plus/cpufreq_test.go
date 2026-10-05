//go:build testing

package plus

import (
	"fmt"
	"os"
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
	writeFile(t, filepath.Join(root, "devices", "system", "cpu", "intel_pstate", "hwp_dynamic_boost"), "1\n")

	got := collectCpufreq(root)
	require.NotNil(t, got)
	assert.Equal(t, []uint32{2800, 2800, 2800, 2800}, got.FreqMHz)
	// MinMHz and MaxMHz are []uint32 parallel to FreqMHz in plus.go, not the
	// scalar uint32 a single-value reading would need, so the whole vector is
	// asserted; the brief's draft asserted one element each. See the report.
	assert.Equal(t, []uint32{400, 400, 400, 400}, got.MinMHz)
	assert.Equal(t, []uint32{4100, 4100, 4100, 4100}, got.MaxMHz)
	assert.Equal(t, "powersave", got.Governor)
	assert.Equal(t, "balance_performance", got.EPP)
	assert.Equal(t, "active", got.PstateStatus)
	require.NotNil(t, got.NoTurbo)
	assert.Equal(t, 0, *got.NoTurbo)
	// Asserted as 1, not 0: a collector that reported a hardcoded zero, or one
	// that never read the file, would look the same against a fixture of "0".
	require.NotNil(t, got.HwpBoost)
	assert.Equal(t, 1, *got.HwpBoost)
}

// TestCollectCpufreqLeavesHwpBoostNilWhenUnreadable pins the two ways the
// hwp_dynamic_boost read can come back empty-handed. Both leave the pointer nil
// rather than storing a zero that a UI would render as "HWP boost off".
func TestCollectCpufreqLeavesHwpBoostNilWhenUnreadable(t *testing.T) {
	// no_turbo is written alongside so the intel_pstate directory exists, which
	// is the state on any host with the driver loaded.
	prime := func(t *testing.T, root string, hwp string) {
		t.Helper()
		writeFile(t, filepath.Join(corePath(root, "cpu0"), "scaling_cur_freq"), "1000000\n")
		pstate := filepath.Join(root, "devices", "system", "cpu", "intel_pstate")
		writeFile(t, filepath.Join(pstate, "no_turbo"), "0\n")
		if hwp != "" {
			writeFile(t, filepath.Join(pstate, "hwp_dynamic_boost"), hwp)
		}
	}

	t.Run("absent", func(t *testing.T) {
		root := t.TempDir()
		prime(t, root, "")

		got := collectCpufreq(root)
		require.NotNil(t, got)
		require.NotNil(t, got.NoTurbo)
		assert.Nil(t, got.HwpBoost)
	})

	t.Run("unparsable", func(t *testing.T) {
		root := t.TempDir()
		prime(t, root, "enabled\n")

		got := collectCpufreq(root)
		require.NotNil(t, got)
		assert.Nil(t, got.HwpBoost)
	})
}

func TestCollectCpufreqSkipsCoresWithoutCpufreqDir(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "devices", "system", "cpu", "cpu0", "cpufreq")
	writeFile(t, filepath.Join(base, "scaling_cur_freq"), "800000\n")

	got := collectCpufreq(root)
	require.NotNil(t, got)
	assert.Equal(t, []uint32{800}, got.FreqMHz)
	// Only cur_freq exists, so both bounds are unreadable: the core is kept and
	// the missing bounds are filled, not dropped. See the alignment test below.
	assert.Equal(t, []uint32{0}, got.MinMHz)
	assert.Equal(t, []uint32{0}, got.MaxMHz)
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
// Every value in the brief's fixtures has a sub-MHz fraction below 0.5, so a
// collector that rounded instead of truncating would pass all of them. These two
// do discriminate: 2970952 Hz is 2970.952 MHz and 1099830 Hz is 1099.830 MHz --
// both round up and both truncate down. Both were read off this machine's
// /sys during the task, not invented.
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

// TestCollectCpufreqOrdersCoresNumerically pins that the vectors are in
// ascending CPU-number order.
//
// filepath.Glob returns lexical order, in which cpu10 sits between cpu1 and cpu2,
// so any host with ten or more logical CPUs would report cpu10's frequency under
// cpu2's index -- silently, with every vector still the right length. Sixteen
// cores is the smallest fixture that distinguishes the two orders, and this
// machine has eight, where lexical and numeric order coincide and no fixture
// below cpu10 can tell them apart. Every value is distinct per core and per
// vector so a reordered or shifted vector cannot pass.
func TestCollectCpufreqOrdersCoresNumerically(t *testing.T) {
	const cores = 16
	root := t.TempDir()
	for i := 0; i < cores; i++ {
		base := corePath(root, "cpu"+fmt.Sprint(i))
		writeFile(t, filepath.Join(base, "scaling_cur_freq"), fmt.Sprint(1000000+100000*i)+"\n")
		writeFile(t, filepath.Join(base, "scaling_min_freq"), fmt.Sprint(100000+10000*i)+"\n")
		writeFile(t, filepath.Join(base, "scaling_max_freq"), fmt.Sprint(9000000+10000*i)+"\n")
	}
	wantFreq := make([]uint32, cores)
	wantMin := make([]uint32, cores)
	wantMax := make([]uint32, cores)
	for i := range wantFreq {
		wantFreq[i] = uint32(1000 + 100*i)
		wantMin[i] = uint32(100 + 10*i)
		wantMax[i] = uint32(9000 + 10*i)
	}

	got := collectCpufreq(root)
	require.NotNil(t, got)
	assert.Equal(t, wantFreq, got.FreqMHz)
	assert.Equal(t, wantMin, got.MinMHz)
	assert.Equal(t, wantMax, got.MaxMHz)
}

// TestCollectCpufreqSortsUnnumberedDirectoriesLast pins the other half of the
// ordering rule. The glob pattern cpu[0-9]* only promises a leading digit, so a
// directory such as "cpu0thermal" matches too and has no CPU number to sort by.
// It is kept -- dropping a matched directory would be a silent data loss -- and
// placed after the numbered cores so it cannot displace a real one.
func TestCollectCpufreqSortsUnnumberedDirectoriesLast(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(corePath(root, "cpu0"), "scaling_cur_freq"), "1000000\n")
	writeFile(t, filepath.Join(corePath(root, "cpu1"), "scaling_cur_freq"), "1100000\n")
	writeFile(t, filepath.Join(corePath(root, "cpu0thermal"), "scaling_cur_freq"), "9990000\n")

	got := collectCpufreq(root)
	require.NotNil(t, got)
	assert.Equal(t, []uint32{1000, 1100, 9990}, got.FreqMHz)
}

// TestCollectCpufreqKeepsVectorsAligned pins the invariant that index i names the
// same CPU in FreqMHz, MinMHz and MaxMHz.
//
// Appending a bound only when it parses leaves the shorter vectors shifted left,
// which is worse than a missing value: with cpu1's min unreadable, MinMHz ends up
// [100 300 400] and a consumer pairing index i across vectors reads cpu2's min as
// cpu1's. Nothing reports the mistake -- the lengths differ, but the collector
// never says so. The first and middle cases below are the two ways the shift
// misreports a value; the third pins that a core whose frequency is unreadable
// leaves all three vectors together rather than leaving a zero behind in one.
func TestCollectCpufreqKeepsVectorsAligned(t *testing.T) {
	// write builds N cores where core i reports cur 1000+1000i MHz and, unless
	// omit[i] is set, min 100+100i and max 9000+100i.
	base := func(t *testing.T, cores int) string {
		t.Helper()
		root := t.TempDir()
		for i := 0; i < cores; i++ {
			dir := corePath(root, "cpu"+fmt.Sprint(i))
			writeFile(t, filepath.Join(dir, "scaling_cur_freq"), fmt.Sprint(1000000+1000000*i)+"\n")
			writeFile(t, filepath.Join(dir, "scaling_min_freq"), fmt.Sprint(100000+100000*i)+"\n")
			writeFile(t, filepath.Join(dir, "scaling_max_freq"), fmt.Sprint(9000000+100000*i)+"\n")
		}
		return root
	}
	aligned := func(t *testing.T, root string, wantCores int) *CpufreqStats {
		t.Helper()
		got := collectCpufreq(root)
		require.NotNil(t, got)
		require.Len(t, got.FreqMHz, wantCores)
		assert.Len(t, got.MinMHz, wantCores, "min vector must stay parallel to freq")
		assert.Len(t, got.MaxMHz, wantCores, "max vector must stay parallel to freq")
		return got
	}

	t.Run("first core has no min file", func(t *testing.T) {
		root := base(t, 2)
		require.NoError(t, os.Remove(filepath.Join(corePath(root, "cpu0"), "scaling_min_freq")))

		got := aligned(t, root, 2)
		assert.Equal(t, []uint32{1000, 2000}, got.FreqMHz)
		assert.Equal(t, []uint32{0, 200}, got.MinMHz, "0 marks the unreadable bound; 200 must stay on cpu1")
		assert.Equal(t, []uint32{9000, 9100}, got.MaxMHz)
	})

	t.Run("middle core has unparsable max", func(t *testing.T) {
		root := base(t, 3)
		writeFile(t, filepath.Join(corePath(root, "cpu1"), "scaling_max_freq"), "unknown\n")

		got := aligned(t, root, 3)
		assert.Equal(t, []uint32{1000, 2000, 3000}, got.FreqMHz)
		assert.Equal(t, []uint32{100, 200, 300}, got.MinMHz)
		assert.Equal(t, []uint32{9000, 0, 9200}, got.MaxMHz, "9200 must stay on cpu2")
	})

	t.Run("core with unreadable freq leaves all three vectors", func(t *testing.T) {
		root := base(t, 3)
		writeFile(t, filepath.Join(corePath(root, "cpu1"), "scaling_cur_freq"), "not-a-number\n")

		got := aligned(t, root, 2)
		assert.Equal(t, []uint32{1000, 3000}, got.FreqMHz)
		assert.Equal(t, []uint32{100, 300}, got.MinMHz, "cpu2's min follows cpu0, not cpu1")
		assert.Equal(t, []uint32{9000, 9200}, got.MaxMHz)
	})
}

// TestCollectCpufreqReportsAllEightLogicalCores records what the live machine
// actually yields, because the count is a design decision rather than an
// accident.
//
// This NAS is an i5-8365U: four physical cores, eight logical, two SMT siblings
// per core. sysfs exposes a cpufreq directory for all eight logical CPUs and
// siblings do not share one scaling_cur_freq, so cpu[0-9]* yields eight
// elements -- which is what Beszel counts as CPUs. On an idle machine siblings
// frequently report the same frequency, so an eight-element vector is not eight
// independent measurements. The shape is asserted, not endorsed as correct;
// ordering is pinned by the sixteen-core fixture above, since lexical and
// numeric order coincide below cpu10 and this fixture could not tell them apart.
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
