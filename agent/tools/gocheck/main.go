// Command gocheck is the consumer-side half of the fan wire: it runs the plus
// collectors against this machine, then judges the fan sample against a clock the
// way a UI has to.
//
// pluscheck answers "did the collectors read anything", not "is what they read still
// true": agent/plus deliberately reads no clock, so sample_ts_s is the only staleness
// signal on the wire and rejecting an old row is the consumer's job. This is that
// consumer, and it exits non-zero on any failure -- a tripwire for a pfanel that has
// stopped writing. The nvme section is left empty because filling it needs upstream's
// parsed SMART data, which lives in package agent.
//
// Usage:
//
//	go run ./agent/tools/gocheck [-root /sys] [-fan-db PATH] [-max-age 60] [-now UNIX] [-json]
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/henrygd/beszel/agent/pfanel"
	"github.com/henrygd/beszel/agent/plus"
)

// nativeFansNote is printed every run so a blank fan chart on a beszel dashboard
// reads as the decision it is, not as a collector that stopped working.
const nativeFansNote = "left empty by design: map[string]uint16 cannot mark an estimate"

type result struct {
	Root     string     `json:"root"`
	NowS     int64      `json:"now"`
	MaxAgeS  int64      `json:"max_age_s"`
	Present  []string   `json:"sections_present"`
	Fan      fanVerdict `json:"fan"`
	Native   string     `json:"native_fans_field"`
	NFailure int        `json:"failures"`
}

func main() {
	root := flag.String("root", plus.ProdRoot, "sysfs root to read from")
	fanDB := flag.String("fan-db", pfanel.DBPath, "pfanel history database; empty means none")
	maxAge := flag.Int64("max-age", defaultMaxAgeS, "seconds the newest fan sample may be old")
	now := flag.Int64("now", 0, "treat this Unix second as now; 0 uses the real clock")
	asJSON := flag.Bool("json", false, "print the verdict as JSON")
	flag.Parse()

	sample, err := pfanel.LatestSample(*fanDB)
	if err != nil && !errors.Is(err, pfanel.ErrNoRow) {
		fmt.Fprintf(os.Stderr, "gocheck: pfanel history: %v\n", err)
		sample = nil
	}
	stats, err := plus.Collect(*root, nil, sample)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gocheck: collect failed: %v\n", err)
		os.Exit(1)
	}

	clock := *now
	if clock == 0 {
		clock = time.Now().Unix()
	}
	verdict := checkFan(stats.Fan, clock, *maxAge)
	out := result{
		Root: *root, NowS: clock, MaxAgeS: *maxAge,
		Present: sections(stats), Fan: verdict, Native: nativeFansNote,
		NFailure: len(verdict.Failures),
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(out); err != nil {
			fmt.Fprintf(os.Stderr, "gocheck: encode failed: %v\n", err)
			os.Exit(1)
		}
	} else {
		fmt.Println(verdict.describe(*root))
		for _, name := range out.Present {
			fmt.Printf("  section %s: present\n", name)
		}
		fmt.Printf("  native Fans field: %s\n", nativeFansNote)
	}
	if !verdict.OK() {
		os.Exit(1)
	}
}

// sections sorts its result because the map is iterated, and an unstable order
// would make two transcripts look like a change when nothing was.
func sections(s *plus.Stats) []string {
	present := map[string]bool{
		"dptf":     s.DPTF != nil && len(s.DPTF.Zones) > 0,
		"cpufreq":  s.Cpufreq != nil,
		"netall":   len(s.NetAll) > 0,
		"wifi":     s.WiFi != nil,
		"fan":      s.Fan != nil,
		"fansteps": len(s.FanSteps) > 0,
	}
	out := make([]string, 0, len(present))
	for name, ok := range present {
		if ok {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}
