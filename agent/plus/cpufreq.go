package plus

import (
	"math"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// unnumberedCPU ranks a cpufreq directory whose CPU id is not a plain number:
// cpu[0-9]* only promises a leading digit, so "cpu0thermal" matches too and has
// no place in the sequence. It is a sort key only; unnumberedCPUIdx is what such a
// directory reports as its Idx.
const (
	unnumberedCPU    = math.MaxInt
	unnumberedCPUIdx = -1
)

// cpuOrderID reads the CPU id out of a ".../cpu<N>/cpufreq" match, returning the
// sort key and the Idx to report. They are separate because the sort needs a total
// order while the payload must distinguish "CPU 3" from "no CPU number": handing
// an unnumbered directory a slice position would forge an identity it does not
// have, which is the same class of bug as reporting cpu10's data under cpu2's Idx.
// The id sits in the parent -- Base of the match itself is "cpufreq" for every
// entry.
func cpuOrderID(match string) (key, idx int) {
	id, err := strconv.Atoi(strings.TrimPrefix(filepath.Base(filepath.Dir(match)), "cpu"))
	if err != nil {
		return unnumberedCPU, unnumberedCPUIdx
	}
	return id, id
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
// Each core is one CpufreqCore record carrying its own sysfs CPU number, so a core
// dropped for having no readable frequency cannot renumber the ones after it.
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
		ki, _ := cpuOrderID(matches[i])
		kj, _ := cpuOrderID(matches[j])
		return ki < kj
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
		// idx comes from the directory name, read before the append, so it is
		// the CPU number and never this record's position. Reading it here also
		// means a core skipped above cannot shift the numbers of those below.
		_, idx := cpuOrderID(dir)
		// Both bounds are recorded whether or not they parsed. Dropping the core
		// for a missing bound would discard a valid frequency because of an
		// unrelated file, and three parallel slices cannot express "this one
		// value is absent" without a validity vector -- which is why this is a
		// record rather than three slices. boundMHz's 0 marks the unreadable
		// case: no real scaling bound reports 0, so 0 stays decidable.
		out.Cores = append(out.Cores, CpufreqCore{
			Idx:     idx,
			FreqMHz: uint32(hz / 1000),
			MinMHz:  boundMHz(filepath.Join(dir, "scaling_min_freq")),
			MaxMHz:  boundMHz(filepath.Join(dir, "scaling_max_freq")),
		})
		if v, err := readTrimmed(filepath.Join(dir, "scaling_governor")); err == nil {
			out.Governor = v
		}
		if v, err := readTrimmed(filepath.Join(dir, "energy_performance_preference")); err == nil {
			out.EPP = v
		}
	}
	if len(out.Cores) == 0 {
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
