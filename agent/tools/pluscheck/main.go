// Command pluscheck runs the plus collectors against a live sysfs root and
// prints what they read. It is the harness for verifying that production
// path resolution actually reaches the machine's own files, which fixture
// tests cannot prove: a collector can pass every fixture test and still glob
// nothing at runtime if it joins the wrong prefix onto the root.
//
// Usage:
//
//	go run ./agent/tools/pluscheck [-root /sys] [-json]
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/henrygd/beszel/agent/plus"
)

// section pairs a collector's payload with the predicate that says it had data.
//
// Keeping the predicate beside the printer means a collector that is wired but
// absent on this machine reports as "absent" instead of aborting the run, which
// is what the old dptf-only early return did: a root with no thermal zones
// printed one line and nothing else, so a second collector could never be seen.
// Adding a collector is one append to sectionsFor, not another branch in main,
// so the later tasks (net, nvme, wifi, fan) reuse this shape rather than
// reinventing it.
type section struct {
	name  string
	has   func() bool
	print func()
}

func sectionsFor(s *plus.Stats) []section {
	return []section{
		{
			name:  "dptf",
			has:   func() bool { return s.DPTF != nil && len(s.DPTF.Zones) > 0 },
			print: func() { printDPTF(s.DPTF) },
		},
		{
			name:  "cpufreq",
			has:   func() bool { return s.Cpufreq != nil },
			print: func() { printCpufreq(s.Cpufreq) },
		},
	}
}

func main() {
	root := flag.String("root", plus.ProdRoot, "sysfs root to read from")
	asJSON := flag.Bool("json", false, "print the full stats payload as JSON")
	flag.Parse()

	stats, err := plus.Collect(*root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pluscheck: collect failed: %v\n", err)
		os.Exit(1)
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(stats); err != nil {
			fmt.Fprintf(os.Stderr, "pluscheck: encode failed: %v\n", err)
			os.Exit(1)
		}
		return
	}

	sections := sectionsFor(stats)
	var shown []string
	for _, sec := range sections {
		if !sec.has() {
			continue
		}
		shown = append(shown, sec.name)
		fmt.Printf("root %s: [%s]\n", *root, sec.name)
		sec.print()
	}
	if len(shown) == 0 {
		fmt.Printf("root %s: no plus collector returned data\n", *root)
		return
	}
	fmt.Printf("root %s: %d of %d collector(s) with data (%s)\n",
		*root, len(shown), len(sections), strings.Join(shown, ", "))
}

func printDPTF(d *plus.DPTFStats) {
	zones := d.Zones
	keys := make([]string, 0, len(zones))
	for k := range zones {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	fmt.Printf("  %d zone(s)\n", len(zones))
	for _, k := range keys {
		fmt.Printf("  %-20s %.1f C\n", k, zones[k])
	}
}

// printCpufreq prints the per-core vectors in full rather than a summary.
//
// FreqMHz, MinMHz and MaxMHz are independent slices, one entry per logical CPU
// that had a readable scaling_cur_freq. Printing them as parallel arrays makes a
// length mismatch between them visible instead of hiding it behind a count, and
// keeps the index of each core aligned with /sys cpu<N>.
func printCpufreq(c *plus.CpufreqStats) {
	fmt.Printf("  %d core(s)\n", len(c.FreqMHz))
	fmt.Printf("  cur MHz %v\n", c.FreqMHz)
	fmt.Printf("  min MHz %v\n", c.MinMHz)
	fmt.Printf("  max MHz %v\n", c.MaxMHz)
	fmt.Printf("  governor=%q epp=%q pstate=%q\n", c.Governor, c.EPP, c.PstateStatus)
	fmt.Printf("  no_turbo=%s hwp_dynamic_boost=%s\n", intPtr(c.NoTurbo), intPtr(c.HwpBoost))
}

func intPtr(p *int) string {
	if p == nil {
		return "nil"
	}
	return fmt.Sprintf("%d", *p)
}
