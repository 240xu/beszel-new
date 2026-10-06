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
// so the later tasks (net, wifi, fan) reuse this shape rather than reinventing
// it. NVMe joins the same way, with one difference: it reads nothing, so its
// payload is injected with -nvme rather than discovered from the root.
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
		{
			name:  "netall",
			has:   func() bool { return len(s.NetAll) > 0 },
			print: func() { printNetAll(s.NetAll) },
		},
		{
			name:  "nvme",
			has:   func() bool { return s.Nvme != nil },
			print: func() { printNvme(s.Nvme) },
		},
	}
}

func main() {
	root := flag.String("root", plus.ProdRoot, "sysfs root to read from")
	asJSON := flag.Bool("json", false, "print the full stats payload as JSON")
	nvmePath := flag.String("nvme", "",
		"path to a JSON plus.NvmeInput to inject for the nvme section; empty means none")
	flag.Parse()

	// The NVMe collector reads no file, so there is nothing on the root to
	// discover. Upstream's SMART manager is what fills this in production and it
	// lives in package agent, which a dev harness cannot import, so -nvme takes
	// the same struct from a file instead. That keeps the harness honest about
	// the collector's real input shape instead of inventing a parallel one.
	var nvme *plus.NvmeInput
	if *nvmePath != "" {
		b, err := os.ReadFile(*nvmePath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "pluscheck: read -nvme: %v\n", err)
			os.Exit(1)
		}
		nvme = &plus.NvmeInput{}
		if err := json.Unmarshal(b, nvme); err != nil {
			fmt.Fprintf(os.Stderr, "pluscheck: parse -nvme: %v\n", err)
			os.Exit(1)
		}
	}

	stats, err := plus.Collect(*root, nvme)
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

// printCpufreq prints one line per core, labelled with the CPU number the record
// carries -- not with its position in the slice.
//
// That distinction is the whole point: a core that could not report a frequency
// is absent, so a position-based label silently renumbers every core after it and
// a live run would print "CPU2" for data that came from cpu3. Printing the Idx
// makes the mapping auditable by eye, and a bound that could not be read prints
// as 0 rather than being left out, so an unreadable bound is visible instead of
// looking like a tight frequency range. Cores come out in ascending Idx, which is
// /sys cpu<N> for every core that reported a frequency.
func printCpufreq(c *plus.CpufreqStats) {
	fmt.Printf("  %d core(s)\n", len(c.Cores))
	for _, core := range c.Cores {
		fmt.Printf("  CPU%-3d cur=%-6d min=%-6d max=%-6d\n", core.Idx, core.FreqMHz, core.MinMHz, core.MaxMHz)
	}
	fmt.Printf("  governor=%q epp=%q pstate=%q\n", c.Governor, c.EPP, c.PstateStatus)
	fmt.Printf("  no_turbo=%s hwp_dynamic_boost=%s\n", intPtr(c.NoTurbo), intPtr(c.HwpBoost))
}

func intPtr(p *int) string {
	if p == nil {
		return "nil"
	}
	return fmt.Sprintf("%d", *p)
}

// printNetAll prints every interface the collector reported, one line each,
// ordered by name.
//
// The payload is a map keyed by interface name, so Go randomises iteration order
// and the names are sorted here to keep two runs comparable line for line. Speed
// prints as "unknown" instead of a number when it is 0, which is where an absent,
// an empty and a -1 speed file all land, so a live run shows how many interfaces
// on this host carry a real link rate at all. The two totals are the point of
// enumerating every interface: upstream charts the primary one only, so its share
// of the sum is the traffic a per-interface view makes visible.
func printNetAll(n map[string]*plus.NetIfaceStats) {
	names := make([]string, 0, len(n))
	var sumRx, sumTx uint64
	for k, s := range n {
		names = append(names, k)
		sumRx += s.RxBytes
		sumTx += s.TxBytes
	}
	sort.Strings(names)

	unknownSpeed := 0
	for _, name := range names {
		s := n[name]
		if s.SpeedMBit == 0 {
			unknownSpeed++
		}
		fmt.Printf("  %-18s rx=%-13d tx=%-12d pkts=%d/%d oper=%-8q speed=%-8s mtu=%d\n",
			name, s.RxBytes, s.TxBytes, s.RxPackets, s.TxPackets,
			s.OperState, speedStr(s.SpeedMBit), s.MTU)
	}
	fmt.Printf("  %d interface(s), %d with unknown speed, totals rx=%d tx=%d\n",
		len(n), unknownSpeed, sumRx, sumTx)
}

// speedStr renders the link rate, naming the unknown case rather than printing a
// bare 0 that reads like a real zero-Mbit link.
func speedStr(mbit int) string {
	if mbit == 0 {
		return "unknown"
	}
	return fmt.Sprintf("%dM", mbit)
}

// printNvme prints the NVMe health projection one field per line.
//
// Every counter is a device-lifetime total, not a rate, so a run is only
// meaningful against a previous one -- and unsafe_shutdowns in particular is the
// field worth reading here, because it counts power cuts the drive did not get to
// flush for. The two percentage fields are printed as percentages and the two NVMe
// Time attributes as minutes, so the unit in the label is the unit on the wire.
func printNvme(n *plus.NvmeStats) {
	fmt.Printf("  model=%q\n", n.Model)
	fmt.Printf("  temp_c=%.1f\n", n.TempC)
	fmt.Printf("  percentage_used_pct=%.1f%%  available_spare_pct=%.1f%%\n", n.PercentageUsedPct, n.AvailableSparePct)
	fmt.Printf("  unsafe_shutdowns=%d  media_errors=%d\n", n.UnsafeShutdowns, n.MediaErrors)
	fmt.Printf("  power_on_hours=%d  power_cycles=%d\n", n.PowerOnHours, n.PowerCycles)
	fmt.Printf("  data_units_read=%d  data_units_written=%d\n", n.DataUnitsRead, n.DataUnitsWritten)
	fmt.Printf("  controller_busy_min=%d  critical_comp_time_min=%d\n", n.ControllerBusyMin, n.CriticalCompTimeMin)
}
