package plus

import (
	"log/slog"
	"math"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// unnumberedState ranks a state<N> with a non-numeric suffix, and a device whose instance will not parse; both sort last rather than being dropped, since an unrecognised name is still a real entry.
const unnumberedState = math.MaxInt

func stateOrder(match string) int {
	n, err := strconv.Atoi(strings.TrimPrefix(filepath.Base(match), "state"))
	if err != nil {
		return unnumberedState
	}
	return n
}

// deviceOrder parses the instance out of an "INT3404:07" directory name, because Glob is
// lexical and puts ":10" before ":02" -- the ordering that misplaces state10 below state1.
func deviceOrder(name string) int {
	_, instance, ok := strings.Cut(filepath.Base(name), ":")
	if !ok {
		return unnumberedState
	}
	n, err := strconv.Atoi(instance)
	if err != nil {
		return unnumberedState
	}
	return n
}

// fanTableDir returns the one ACPI device directory that publishes a numeric state table,
// or "" when none does. Three layers where there was one literal path: this host's fan is
// INT3404:00 and nothing guarantees that elsewhere, so naming it emptied fan_steps on a
// machine that numbered its fan differently, with nothing reporting the loss.
// Several qualifying devices still yield exactly one -- lowest instance, glob's lexical
// order breaking a tie. FanSpeedStep has no instance field, so concatenating tables would
// serve one fan's numbers as another's, and that lie is worse than the absent key an
// absent table leaves. Ordering picks a winner; it never licenses reporting both.
func fanTableDir(root string) string {
	devices, err := filepath.Glob(filepath.Join(root, "bus", "acpi", "devices", "*"))
	if err != nil {
		return ""
	}
	var candidates []string
	best := ""
	for _, device := range devices {
		if !fanTableDevice(device) || !publishesFanTable(device) {
			continue
		}
		candidates = append(candidates, filepath.Base(device))
		if best == "" || fanDeviceRank(device) < fanDeviceRank(best) ||
			(fanDeviceRank(device) == fanDeviceRank(best) && deviceOrder(device) < deviceOrder(best)) {
			best = device
		}
	}
	// Reachable on hardware this has not run on, so it is logged rather than left to a comment.
	if len(candidates) > 1 {
		slog.Debug("fan_steps: several ACPI devices publish a table; reporting one",
			"candidates", candidates, "chosen", filepath.Base(best))
	}
	return best
}

// fanDeviceRank orders the two HIDs that can carry a table. DPTF (INT340*) ranks first
// because the EC path this project is built around targets that fan; PNP0C0B is the
// fallback for firmware that exposes the ACPI fan instead. Ranking is explicit rather
// than left to glob's lexical order, which happens to put INT3404 first today.
func fanDeviceRank(device string) int {
	if strings.HasPrefix(filepath.Base(device), "INT340") {
		return 0
	}
	return 1
}

// fanTableDevice reports whether an ACPI device is one that carries the fan
// performance table. INT340* is Intel DPTF; PNP0C0B is the ACPI fan device, which
// publishes the same state* files on firmware that does not expose a DPTF fan. The
// earlier INT340-only glob silently hid a real fan on such a machine, leaving
// fan_steps empty with nothing saying why.
func fanTableDevice(device string) bool {
	name := filepath.Base(device)
	if strings.HasPrefix(name, "INT340") {
		return true
	}
	// PNP0C0B is the ACPI fan; the trailing instance (":00") is optional in sysfs.
	return name == "PNP0C0B" || strings.HasPrefix(name, "PNP0C0B:")
}

// publishesFanTable reports whether dir holds at least one state file with a numeric
// suffix -- the predicate is the table's existence, never the device's instance number or
// description. This host publishes INT3404:01 described "Fan 1" holding no state file at
// all, so a name or index test guesses at firmware intent where this asks.
// The "state*" glob alone is not the test either: it also matches siblings such as
// state_default, which carry no step number and are dropped further down.
func publishesFanTable(dir string) bool {
	matches, err := filepath.Glob(filepath.Join(dir, "state*"))
	if err != nil {
		return false
	}
	for _, match := range matches {
		if stateOrder(match) != unnumberedState {
			return true
		}
	}
	return false
}

// collectFanSteps reads the duty levels published as state1..state11 beside a state0 auto placeholder, from whichever device fanTableDir settles on; the sort re-parses each suffix because Glob puts state10 between state1 and state2.
func collectFanSteps(root string) []FanSpeedStep {
	dir := fanTableDir(root)
	if dir == "" {
		return nil
	}
	matches, err := filepath.Glob(filepath.Join(dir, "state*"))
	if err != nil || len(matches) == 0 {
		return nil
	}
	sort.SliceStable(matches, func(i, j int) bool {
		return stateOrder(matches[i]) < stateOrder(matches[j])
	})
	var out []FanSpeedStep
	for _, match := range matches {
		if stateOrder(match) == unnumberedState {
			continue
		}
		line, err := readTrimmed(match)
		if err != nil {
			continue
		}
		if step, ok := parseFpsLine(line); ok {
			out = append(out, step)
		}
	}
	return out
}

// parseFpsLine reads control%:trip:speed_rpm:noise_level_mdb:power_mw per the kernel's
// fan_performance_states.rst, stepping over trip_point_index: "not-defined" is how the kernel
// marks a field the firmware left unpopulated. noise_level_mdb and power_mw are millidecibels
// and milliwatts — 12.5 dB and 1.25 W at the first step once divided by 1000 — yet across
// all 11 steps both are exactly 500*pct and 50*pct: duty multiples, not measurements.
func parseFpsLine(line string) (FanSpeedStep, bool) {
	parts := strings.Split(line, ":")
	if len(parts) != 5 {
		return FanSpeedStep{}, false
	}
	// Duty at or below 0 is the auto placeholder, which carries no speed or power.
	pct, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil || pct <= 0 {
		return FanSpeedStep{}, false
	}
	// The duty is the only field this table exists to publish, so it is the only one
	// whose absence drops the row. Speed, noise and power are per-step firmware
	// measurements that a partial table can leave "not-defined"; dropping the whole
	// row there discarded a duty the firmware does define. A field that is absent or
	// unparsable stays 0, which is the same "no value" this wire already uses for a
	// no-tach fan, rather than a fabricated measurement.
	var rpm uint64
	if v, err := strconv.ParseUint(strings.TrimSpace(parts[2]), 10, 64); err == nil {
		// A parseable speed above uint16 is not an absent field: it is a concrete value
		// the wire cannot carry, and 0xFFFFFFFF is the no-tachometer sentinel, whose low
		// 16 bits are a plausible-looking 65535. Drop the row rather than admit a speed
		// this fan never reported. "not-defined" and unparsable stay 0 and keep the row.
		if v > 65535 {
			return FanSpeedStep{}, false
		}
		rpm = v
	}
	var noise, power int
	if v, err := strconv.Atoi(strings.TrimSpace(parts[3])); err == nil {
		noise = v
	}
	if v, err := strconv.Atoi(strings.TrimSpace(parts[4])); err == nil {
		power = v
	}
	return FanSpeedStep{
		Percent: pct, RPMEst: uint16(rpm), NoiseMdb: noise, PowerMw: power,
	}, true
}
