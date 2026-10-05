package plus

import (
	"math"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// unnumberedCPU is the sort key for a cpufreq directory whose CPU id is not a
// plain number: cpu[0-9]* only promises a leading digit, so "cpu0thermal"
// matches too and has no place in the sequence.
const unnumberedCPU = math.MaxInt

// cpuOrderKey reads the CPU id out of a ".../cpu<N>/cpufreq" match, ranking
// unnumbered names last. The id sits in the parent: Base of the match itself is
// "cpufreq" for every entry.
func cpuOrderKey(match string) int {
	id, err := strconv.Atoi(strings.TrimPrefix(filepath.Base(filepath.Dir(match)), "cpu"))
	if err != nil {
		return unnumberedCPU
	}
	return id
}

// boundMHz reads a scaling bound in kHz as MHz, 0 when it cannot be read.
func boundMHz(path string) uint32 {
	v, err := readUint(path)
	if err != nil {
		return 0
	}
	return uint32(v / 1000)
}

// collectCpufreq reports per-core frequency plus the active tuning policy.
// Beszel exposes CPU usage but not frequency, leaving turbo behaviour and the
// governor/EPP choice invisible.
//
// All three vectors are parallel: index i is the same CPU in each. That index is
// the CPU's position in ascending numeric order, which equals the CPU number
// unless a core was dropped for having no readable frequency -- the one case
// that needs a per-core record rather than a wider slice.
func collectCpufreq(root string) *CpufreqStats {
	matches, err := filepath.Glob(
		filepath.Join(root, "devices", "system", "cpu", "cpu[0-9]*", "cpufreq"))
	if err != nil || len(matches) == 0 {
		return nil
	}
	// filepath.Glob returns lexical order, where cpu10 lands between cpu1 and
	// cpu2, so the payload would report cpu10's frequency as cpu2's on any host
	// with ten or more logical CPUs. Sorting numerically first is what makes an
	// index mean one thing; SliceStable keeps glob order among unnumbered names.
	sort.SliceStable(matches, func(i, j int) bool {
		return cpuOrderKey(matches[i]) < cpuOrderKey(matches[j])
	})
	out := &CpufreqStats{}
	for _, dir := range matches {
		cur, err := readTrimmed(filepath.Join(dir, "scaling_cur_freq"))
		if err != nil {
			continue
		}
		hz, err := strconv.ParseUint(cur, 10, 64)
		if err != nil {
			continue
		}
		out.FreqMHz = append(out.FreqMHz, uint32(hz/1000))
		// Appended unconditionally. Appending only when a bound parses leaves
		// the vectors different lengths and shifts the short ones left, so a
		// consumer pairing index i across them reads cpu1's min as cpu0's.
		// Skipping the core instead would discard a valid frequency because of
		// an unrelated missing file, and filling from cpuinfo_min_freq would
		// make min_mhz mean different things on different hosts. 0 is the fill
		// because no real scaling bound reports 0, so "unreadable" stays a
		// decidable value without a parallel validity vector.
		out.MinMHz = append(out.MinMHz, boundMHz(filepath.Join(dir, "scaling_min_freq")))
		out.MaxMHz = append(out.MaxMHz, boundMHz(filepath.Join(dir, "scaling_max_freq")))
		if v, err := readTrimmed(filepath.Join(dir, "scaling_governor")); err == nil {
			out.Governor = v
		}
		if v, err := readTrimmed(filepath.Join(dir, "energy_performance_preference")); err == nil {
			out.EPP = v
		}
	}
	if len(out.FreqMHz) == 0 {
		return nil
	}
	pstate := filepath.Join(root, "devices", "system", "cpu", "intel_pstate")
	if v, err := readTrimmed(filepath.Join(pstate, "status")); err == nil {
		out.PstateStatus = v
	}
	if v, err := readTrimmed(filepath.Join(pstate, "no_turbo")); err == nil {
		if n, err := strconv.Atoi(v); err == nil {
			out.NoTurbo = &n
		}
	}
	if v, err := readTrimmed(filepath.Join(pstate, "hwp_dynamic_boost")); err == nil {
		if n, err := strconv.Atoi(v); err == nil {
			out.HwpBoost = &n
		}
	}
	return out
}
