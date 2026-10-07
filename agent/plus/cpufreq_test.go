//go:build testing

package plus

import (
	"encoding/json"
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

// coreIdxs lists the reported CPU numbers in payload order.
//
// Every assertion below goes through this or through coreAt rather than through a
// slice position: CpufreqCore.Idx is the contract, the position is the bug.
func coreIdxs(s *CpufreqStats) []int {
	idxs := make([]int, len(s.Cores))
	for i, c := range s.Cores {
		idxs[i] = c.Idx
	}
	return idxs
}

// coreAt returns the record for one CPU number, failing the test if that core is
// absent. Looking a core up by its number is the only way the consumer is allowed
// to read this payload, so the tests do it too.
func coreAt(t *testing.T, s *CpufreqStats, idx int) CpufreqCore {
	t.Helper()
	for _, c := range s.Cores {
		if c.Idx == idx {
			return c
		}
	}
	require.FailNowf(t, "core missing", "no record for CPU %d in %v", idx, coreIdxs(s))
	return CpufreqCore{}
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
	assert.Equal(t, []int{0, 1, 2, 3}, coreIdxs(got))
	// Each core is looked up by its own CPU number, so the whole vector of
	// frequencies/bounds is asserted without ever naming a slice position.
	for i := 0; i < 4; i++ {
		c := coreAt(t, got, i)
		assert.Equalf(t, uint32(2800), c.FreqMHz, "cpu%d freq", i)
		assert.Equalf(t, uint32(400), c.MinMHz, "cpu%d min", i)
		assert.Equalf(t, uint32(4100), c.MaxMHz, "cpu%d max", i)
	}
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

func TestCollectCpufreqKeepsCoreWithUnreadableBounds(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "devices", "system", "cpu", "cpu0", "cpufreq")
	writeFile(t, filepath.Join(base, "scaling_cur_freq"), "800000\n")

	got := collectCpufreq(root)
	require.NotNil(t, got)
	// Only cur_freq exists, so both bounds are unreadable: the core is kept and
	// the missing bounds are filled, not dropped. See the coherence test below.
	assert.Equal(t, []int{0}, coreIdxs(got))
	c := coreAt(t, got, 0)
	assert.Equal(t, uint32(800), c.FreqMHz)
	assert.Equal(t, uint32(0), c.MinMHz)
	assert.Equal(t, uint32(0), c.MaxMHz)
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
			// cpu5's value must be reachable under Idx 5, which it only is if Idx
			// carries the directory's CPU number rather than its arrival order.
			assert.Equal(t, uint32(tc.want), coreAt(t, got, atoi(tc.core[3:])).FreqMHz)
		})
	}
}

// atoi is a tiny helper for the handful of fixtures that name a CPU as a string.
func atoi(s string) int {
	var n int
	_, _ = fmt.Sscan(s, &n)
	return n
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

	s, err := Collect(root, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, s.Cpufreq, "cpufreq must survive the Collect wiring")
	assert.Equal(t, uint32(4100), coreAt(t, s.Cpufreq, 0).FreqMHz)
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
	c := coreAt(t, got, 0)
	assert.Equal(t, uint32(2970), c.FreqMHz)
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

// TestCollectCpufreqIdxIsCpuNumberNotSlicePosition is the load-bearing test for
// the per-core record: Idx must be the number in the cpu<N> directory name, not
// the record's position in Cores.
//
// The fixture drops cpu0's scaling_cur_freq, so cpu0 produces no record at all.
// A positional Idx would then hand CPU 1 the number 0 and CPU 2 the number 1 --
// still a well-formed, ascending, gap-free vector, which is exactly why the bug
// is invisible downstream: a consumer keying on Idx would file two real cores
// under the wrong CPUs and never notice. Every value is distinct so a shifted
// assignment cannot pass by coincidence.
func TestCollectCpufreqIdxIsCpuNumberNotSlicePosition(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(corePath(root, "cpu1"), "scaling_cur_freq"), "1500000\n")
	writeFile(t, filepath.Join(corePath(root, "cpu2"), "scaling_cur_freq"), "2500000\n")
	// cpu0 gets a cpufreq directory with no readable frequency: it must be
	// skipped, and skipping it must not renumber the cores after it.
	writeFile(t, filepath.Join(corePath(root, "cpu0"), "scaling_governor"), "powersave\n")

	got := collectCpufreq(root)
	require.NotNil(t, got)
	assert.Equal(t, []int{1, 2}, coreIdxs(got),
		"Idx must stay the sysfs CPU number; a dropped core may not shift later ones")
	assert.Equal(t, uint32(1500), coreAt(t, got, 1).FreqMHz, "CPU 1 keeps its own reading")
	assert.Equal(t, uint32(2500), coreAt(t, got, 2).FreqMHz, "CPU 2 keeps its own reading")
}

// TestCollectCpufreqOrdersCoresNumerically pins that records are in ascending
// CPU-number order.
//
// filepath.Glob returns lexical order, in which cpu10 sits between cpu1 and cpu2,
// so any host with ten or more logical CPUs would report cpu10's frequency under
// cpu2's Idx -- and with Idx now explicit the mistake becomes a visible wrong
// number rather than a bare shift. Sixteen cores is the smallest fixture that
// distinguishes the two orders, and this machine has eight, where lexical and
// numeric order coincide and no fixture below cpu10 can tell them apart. Every
// value is distinct per core and per bound so a reordered record cannot pass.
func TestCollectCpufreqOrdersCoresNumerically(t *testing.T) {
	const cores = 16
	root := t.TempDir()
	for i := 0; i < cores; i++ {
		base := corePath(root, "cpu"+fmt.Sprint(i))
		writeFile(t, filepath.Join(base, "scaling_cur_freq"), fmt.Sprint(1000000+100000*i)+"\n")
		writeFile(t, filepath.Join(base, "scaling_min_freq"), fmt.Sprint(100000+10000*i)+"\n")
		writeFile(t, filepath.Join(base, "scaling_max_freq"), fmt.Sprint(9000000+10000*i)+"\n")
	}
	wantIdxs := make([]int, cores)
	for i := range wantIdxs {
		wantIdxs[i] = i
	}

	got := collectCpufreq(root)
	require.NotNil(t, got)
	assert.Equal(t, wantIdxs, coreIdxs(got), "records must be in ascending CPU-number order")
	for i := 0; i < cores; i++ {
		c := coreAt(t, got, i)
		assert.Equalf(t, uint32(1000+100*i), c.FreqMHz, "cpu%d freq", i)
		assert.Equalf(t, uint32(100+10*i), c.MinMHz, "cpu%d min", i)
		assert.Equalf(t, uint32(9000+10*i), c.MaxMHz, "cpu%d max", i)
	}
}

// TestCollectCpufreqSortsUnnumberedDirectoriesLast pins the other half of the
// ordering rule. The glob pattern cpu[0-9]* only promises a leading digit, so a
// directory such as "cpu0thermal" matches too and has no CPU number to sort by.
// It is kept -- dropping a matched directory would be a silent data loss -- and
// placed after the numbered cores so it cannot displace a real one. Its Idx is
// unnumberedCPUIdx (-1) rather than a slice position, because handing it the
// position 2 would claim it is CPU 2.
func TestCollectCpufreqSortsUnnumberedDirectoriesLast(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(corePath(root, "cpu0"), "scaling_cur_freq"), "1000000\n")
	writeFile(t, filepath.Join(corePath(root, "cpu1"), "scaling_cur_freq"), "1100000\n")
	writeFile(t, filepath.Join(corePath(root, "cpu0thermal"), "scaling_cur_freq"), "9990000\n")

	got := collectCpufreq(root)
	require.NotNil(t, got)
	assert.Equal(t, []int{0, 1, unnumberedCPUIdx}, coreIdxs(got))
	assert.Equal(t, uint32(1000), coreAt(t, got, 0).FreqMHz)
	assert.Equal(t, uint32(1100), coreAt(t, got, 1).FreqMHz)
	assert.Equal(t, uint32(9990), coreAt(t, got, unnumberedCPUIdx).FreqMHz,
		"a directory with no CPU number must not inherit a real core's Idx")
}

// TestCollectCpufreqKeepsCoreRecordsCoherent pins the invariant that one record
// carries one core's three readings, so they cannot disagree about which CPU they
// describe.
//
// It used to be three parallel slices, where a bound that failed to parse left
// the shorter vector shifted left and a consumer pairing index i across them read
// cpu2's min as cpu1's. A record removes that failure mode by construction; what
// is left to pin is the zero-fill and the skip-one-core behaviour. The first and
// middle cases are the two ways an absent bound must not disturb its neighbours,
// the third is the same gap the Idx test above covers seen from the data side.
func TestCollectCpufreqKeepsCoreRecordsCoherent(t *testing.T) {
	// write builds N cores where core i reports cur 1000+1000i MHz and, unless
	// omitted, min 100+100i and max 9000+100i.
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

	t.Run("first core has no min file", func(t *testing.T) {
		root := base(t, 2)
		require.NoError(t, os.Remove(filepath.Join(corePath(root, "cpu0"), "scaling_min_freq")))

		got := collectCpufreq(root)
		require.NotNil(t, got)
		assert.Equal(t, []int{0, 1}, coreIdxs(got), "an unreadable bound must not drop the core")
		c0 := coreAt(t, got, 0)
		assert.Equal(t, uint32(1000), c0.FreqMHz)
		assert.Equal(t, uint32(0), c0.MinMHz, "0 marks the unreadable bound")
		c1 := coreAt(t, got, 1)
		assert.Equal(t, uint32(200), c1.MinMHz, "cpu1's own min must stay on cpu1")
		assert.Equal(t, uint32(2000), c1.FreqMHz)
		assert.Equal(t, uint32(9100), c1.MaxMHz)
	})

	t.Run("middle core has unparsable max", func(t *testing.T) {
		root := base(t, 3)
		writeFile(t, filepath.Join(corePath(root, "cpu1"), "scaling_max_freq"), "unknown\n")

		got := collectCpufreq(root)
		require.NotNil(t, got)
		assert.Equal(t, []int{0, 1, 2}, coreIdxs(got))
		assert.Equal(t, uint32(0), coreAt(t, got, 1).MaxMHz, "0 marks the unreadable bound")
		assert.Equal(t, uint32(9200), coreAt(t, got, 2).MaxMHz, "cpu2's max must stay on cpu2")
	})

	t.Run("core with unreadable freq leaves no record", func(t *testing.T) {
		root := base(t, 3)
		writeFile(t, filepath.Join(corePath(root, "cpu1"), "scaling_cur_freq"), "not-a-number\n")

		got := collectCpufreq(root)
		require.NotNil(t, got)
		assert.Equal(t, []int{0, 2}, coreIdxs(got), "cpu1 has no frequency, so it has no record")
		assert.Equal(t, uint32(3000), coreAt(t, got, 2).FreqMHz)
		assert.Equal(t, uint32(300), coreAt(t, got, 2).MinMHz, "cpu2's min follows cpu0, not cpu1")
		assert.Equal(t, uint32(9200), coreAt(t, got, 2).MaxMHz)
	})
}

// TestCollectCpufreqReportsAllEightLogicalCores records what the live machine
// actually yields, because the count is a design decision rather than an
// accident.
//
// This NAS is an i5-8365U: four physical cores, eight logical, two SMT siblings
// per core. sysfs exposes a cpufreq directory for all eight logical CPUs and
// siblings do not share one scaling_cur_freq, so cpu[0-9]* yields eight records
// -- which is what Beszel counts as CPUs. On an idle machine siblings frequently
// report the same frequency, so an eight-record payload is not eight independent
// measurements. The shape is asserted, not endorsed as correct; ordering is
// pinned by the sixteen-core fixture above, since lexical and numeric order
// coincide below cpu10 and this fixture could not tell them apart.
func TestCollectCpufreqReportsAllEightLogicalCores(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 8; i++ {
		core := "cpu" + fmt.Sprint(i)
		// Distinct MHz per core so a collector that collapsed the records, or
		// re-ordered them, cannot pass.
		writeFile(t, filepath.Join(corePath(root, core), "scaling_cur_freq"), fmt.Sprint(2000000+i*1000)+"\n")
	}

	got := collectCpufreq(root)
	require.NotNil(t, got)
	require.Len(t, got.Cores, 8)
	assert.Equal(t, []int{0, 1, 2, 3, 4, 5, 6, 7}, coreIdxs(got))
	for i := 0; i < 8; i++ {
		assert.Equalf(t, uint32(2000+i), coreAt(t, got, i).FreqMHz, "cpu%d", i)
	}
}

// TestCpufreqCoreWireShape pins the marshalled record, because the schema is the
// contract: Task 6 replaced three parallel slices with this record, and a later
// refactor that reverted the field names or dropped the omitempty-free scalars
// would not fail any behavioural test here.
func TestCpufreqCoreWireShape(t *testing.T) {
	b, err := json.Marshal(CpufreqStats{Cores: []CpufreqCore{{Idx: 3, FreqMHz: 2800, MinMHz: 400, MaxMHz: 4100}}})
	require.NoError(t, err)
	assert.JSONEq(t, `{"cores":[{"idx":3,"freq_mhz":2800,"min_mhz":400,"max_mhz":4100}]}`, string(b),
		"idx carries the CPU number and must be emitted even when 0")

	b, err = json.Marshal(CpufreqStats{Cores: []CpufreqCore{{Idx: 0}}})
	require.NoError(t, err)
	assert.JSONEq(t, `{"cores":[{"idx":0,"freq_mhz":0,"min_mhz":0,"max_mhz":0}]}`, string(b),
		"idx 0 and a zero-filled bound must survive; consumers read 0 as \"unreadable\"")
}
