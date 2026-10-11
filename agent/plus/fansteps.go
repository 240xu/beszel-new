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
	devices, err := filepath.Glob(filepath.Join(root, "bus", "acpi", "devices", "INT340*"))
	if err != nil {
		return ""
	}
	var candidates []string
	best := ""
	for _, device := range devices {
		if !publishesFanTable(device) {
			continue
		}
		candidates = append(candidates, filepath.Base(device))
		if best == "" || deviceOrder(device) < deviceOrder(best) {
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
	rpm, err1 := strconv.ParseUint(strings.TrimSpace(parts[2]), 10, 64)
	noise, err2 := strconv.Atoi(strings.TrimSpace(parts[3]))
	power, err3 := strconv.Atoi(strings.TrimSpace(parts[4]))
	if err1 != nil || err2 != nil || err3 != nil {
		return FanSpeedStep{}, false
	}
	// Rejected above uint16's ceiling, not wrapped into it: fan_speed_rpm is the no-tach
	// sentinel 0xFFFFFFFF, whose low 16 bits are exactly 65535 — why nothing reads it.
	if rpm > 65535 {
		return FanSpeedStep{}, false
	}
	return FanSpeedStep{
		Percent: pct, RPMEst: uint16(rpm), NoiseMdb: noise, PowerMw: power,
	}, true
}
