package plus

import (
	"math"
	"sort"
)

// collectFan converts pfanel's newest row into the reported state. A nil sample
// yields nil, so the "fan" key is absent rather than present-and-empty.
//
// root is needed for the rpm fallback, which interpolates through the firmware's
// own table. Every pointer passes through as-is: a NULL stays nil, never 0.
func collectFan(root string, sample *FanSample) *FanStats {
	if sample == nil {
		return nil
	}
	s := &FanStats{
		RPMEstFlag:  true,
		SampleTsS:   sample.TsUnix,
		Gov:         sample.Gov,
		TempC:       sample.TempC,
		SetLevelPct: sample.LevelPct,
		EC76Pct:     sample.EC76Pct,
	}
	// The stored rpm wins: it is pfanel's own estimate for the duty it recorded, sampling
	// the same firmware curve more finely than the 11-step table rather than on a different
	// scale (measured agreement in FanStats). _FPS interpolation is the fallback for rows
	// that stored none; a value above uint16's ceiling is rejected, not wrapped, as in
	// parseFpsLine.
	switch {
	case sample.RPM != nil && *sample.RPM > 0 && *sample.RPM <= 65535:
		s.RPMEst = uint16(*sample.RPM)
	case sample.EC76Pct != nil && *sample.EC76Pct > 0:
		s.RPMEst = estRPM(*sample.EC76Pct, collectFanSteps(root))
	}
	return s
}

// estRPM interpolates a duty percentage to an approximate speed across the
// firmware's table. pct at or below 0 yields 0, meaning "no estimate", not "stopped".
func estRPM(pct int, steps []FanSpeedStep) uint16 {
	if len(steps) == 0 || pct <= 0 {
		return 0
	}
	sorted := append([]FanSpeedStep(nil), steps...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Percent < sorted[j].Percent })
	if pct <= sorted[0].Percent {
		return sorted[0].RPMEst
	}
	last := sorted[len(sorted)-1]
	if pct >= last.Percent {
		return last.RPMEst
	}
	for i := 1; i < len(sorted); i++ {
		lo, hi := sorted[i-1], sorted[i]
		if pct >= lo.Percent && pct <= hi.Percent {
			span := float64(hi.Percent - lo.Percent)
			if span == 0 {
				return lo.RPMEst
			}
			frac := (float64(pct) - float64(lo.Percent)) / span
			return uint16(math.Round(
				float64(lo.RPMEst) + frac*float64(hi.RPMEst-lo.RPMEst)))
		}
	}
	return last.RPMEst
}
