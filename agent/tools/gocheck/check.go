package main

import (
	"fmt"
	"strings"

	"github.com/henrygd/beszel/agent/plus"
)

// defaultMaxAgeS is how old the newest pfanel row may be before gocheck calls it
// stale. pfanel writes roughly every 12 s, so this tolerates several missed writes:
// long enough that one slow sample is not an alarm, short enough that a pfanel
// stopped for a minute is.
const defaultMaxAgeS = 60

// fanVerdict is what gocheck concluded about one fan sample. It is separate from
// the printing so the rule can be tested with no sysfs root, no database and no
// clock: both times are arguments.
type fanVerdict struct {
	Present   bool     `json:"sample_present"`
	SampleTsS int64    `json:"sample_ts_s"`
	AgeS      int64    `json:"age_s"`
	MaxAgeS   int64    `json:"max_age_s"`
	Stale     bool     `json:"stale"`
	RPMEst    uint16   `json:"rpm_est"`
	Flagged   bool     `json:"rpm_est_is_estimate"`
	Gov       string   `json:"gov"`
	Duty      *int     `json:"ec76_pct"`
	Level     *int     `json:"set_level_pct"`
	Failures  []string `json:"failures"`
}

// OK reports whether the sample satisfied every rule. Absent counts as not OK: on
// this platform pfanel is expected to be installed, so a missing row is a finding
// rather than a neutral state.
func (v fanVerdict) OK() bool { return len(v.Failures) == 0 }

// checkFan applies the rules a consumer of Stats.Plus.Fan owes its reader.
//
// The gate lives here, not in agent/plus: a collector reading the clock makes
// collectFan impure, and FanStats has nowhere to put a verdict -- rpm_est is
// omitempty, so a row rejected for age would vanish rather than read as stale.
func checkFan(f *plus.FanStats, nowS, maxAgeS int64) fanVerdict {
	if f == nil {
		return fanVerdict{
			MaxAgeS:  maxAgeS,
			Failures: []string{"no fan sample: pfanel has no readable row"},
		}
	}
	v := fanVerdict{
		Present:   true,
		SampleTsS: f.SampleTsS,
		MaxAgeS:   maxAgeS,
		RPMEst:    f.RPMEst,
		Flagged:   f.RPMEstFlag,
		Gov:       f.Gov,
		Duty:      f.EC76Pct,
		Level:     f.SetLevelPct,
	}
	if f.SampleTsS <= 0 {
		v.Failures = append(v.Failures,
			fmt.Sprintf("sample_ts_s must be present, got %d", f.SampleTsS))
	}
	v.AgeS = nowS - f.SampleTsS
	switch {
	case f.SampleTsS > 0 && v.AgeS < 0:
		v.Failures = append(v.Failures,
			fmt.Sprintf("sample_ts_s is in the future by %ds: clock skew or a bad row", -v.AgeS))
	case f.SampleTsS > 0 && v.AgeS > maxAgeS:
		v.Stale = true
		v.Failures = append(v.Failures,
			fmt.Sprintf("stale: %ds old, limit %ds", v.AgeS, maxAgeS))
	}
	// The label is the contract. An estimate reaching a consumer with no marker is
	// the one failure here that cannot be repaired downstream.
	if f.RPMEst != 0 && !f.RPMEstFlag {
		v.Failures = append(v.Failures,
			fmt.Sprintf("rpm_est %d carries no rpm_est_is_estimate marker", f.RPMEst))
	}
	return v
}

// describe renders the verdict for a human, one rule per line.
func (v fanVerdict) describe(root string) string {
	if !v.Present {
		return fmt.Sprintf("fan %s: %s", root, strings.Join(v.Failures, "; "))
	}
	head := fmt.Sprintf("fan %s: sample_ts_s=%d age=%ds/%ds%s rpm_est=%d labelled=%t",
		root, v.SampleTsS, v.AgeS, v.MaxAgeS, freshness(v.Stale), v.RPMEst, v.Flagged)
	if v.Gov != "" {
		head += " gov=" + v.Gov
	}
	if v.Duty != nil {
		head += fmt.Sprintf(" ec76_pct=%d", *v.Duty)
	}
	if v.Level != nil {
		head += fmt.Sprintf(" set_level_pct=%d", *v.Level)
	}
	if len(v.Failures) == 0 {
		return head
	}
	return head + "\n  FAIL " + strings.Join(v.Failures, "\n  FAIL ")
}

func freshness(stale bool) string {
	if stale {
		return " STALE"
	}
	return " fresh"
}
