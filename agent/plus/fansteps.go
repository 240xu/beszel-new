package plus

import (
	"math"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// unnumberedState ranks a state<N> file with a non-numeric suffix; stateOrder parses it out of a ".../state<N>" match.
const unnumberedState = math.MaxInt

func stateOrder(match string) int {
	n, err := strconv.Atoi(strings.TrimPrefix(filepath.Base(match), "state"))
	if err != nil {
		return unnumberedState
	}
	return n
}

// collectFanSteps reads the firmware's own fan speed table from INT3404:00, the duty
// levels it publishes as state1..state11 beside a state0 auto placeholder, under the
// injected sysfs root. Only INT3404:00, though this host also publishes INT3404:01 as
// "Fan 1": FanSpeedStep has no instance field, so its rows cannot be attributed to a
// fan, while an absent key reads as unknown. Covering all instances is a schema change.
func collectFanSteps(root string) []FanSpeedStep {
	matches, err := filepath.Glob(filepath.Join(root, "bus", "acpi", "devices", "INT3404:00", "state*"))
	if err != nil || len(matches) == 0 {
		return nil
	}
	// Glob returns lexical order, where state10 lands between state1 and state2; the 12 files here make that live.
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
