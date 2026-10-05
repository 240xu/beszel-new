package plus

import (
	"path/filepath"
	"strconv"
)

// collectCpufreq reports per-core frequency plus the active tuning policy.
// Beszel exposes CPU usage but not frequency, leaving turbo behaviour and the
// governor/EPP choice invisible.
func collectCpufreq(root string) *CpufreqStats {
	matches, err := filepath.Glob(
		filepath.Join(root, "devices", "system", "cpu", "cpu[0-9]*", "cpufreq"))
	if err != nil || len(matches) == 0 {
		return nil
	}
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
		if v, err := readUint(filepath.Join(dir, "scaling_min_freq")); err == nil {
			out.MinMHz = append(out.MinMHz, uint32(v/1000))
		}
		if v, err := readUint(filepath.Join(dir, "scaling_max_freq")); err == nil {
			out.MaxMHz = append(out.MaxMHz, uint32(v/1000))
		}
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
