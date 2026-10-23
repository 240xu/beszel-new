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
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/henrygd/beszel/agent/pfanel"
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
		{
			name:  "wifi",
			has:   func() bool { return s.WiFi != nil },
			print: func() { printWiFi(s.WiFi) },
		},
		{
			name:  "fan",
			has:   func() bool { return s.Fan != nil },
			print: func() { printFan(s.Fan) },
		},
		{
			name:  "fan_steps",
			has:   func() bool { return len(s.FanSteps) > 0 },
			print: func() { printFanSteps(s.FanSteps) },
		},
		{
			name: "fanctl",
			// Always shown. The other sections hide themselves when there is
			// nothing to print, which is right for a collector that reads a file
			// tree and wrong for one that talks to a device: an unreadable ACPI
			// reply would then be indistinguishable from a machine without one.
			has:   func() bool { return true },
			print: func() { printFanControl() },
		},
	}
}

// printFanControl probes the ACPI fan surface against the live machine.
//
// Read-only by construction: _FST and EC88 are reads, and SetFanLevel is
// deliberately not called -- changing this machine's duty is Task 14's job and
// is not something a verification harness should do as a side effect.
func printFanControl() {
	lv, lvErr := plus.GetFanLevel()
	fmt.Printf("  level: %d (%s) err: %v\n", lv, plus.LevelLabel(lv), lvErr)
	ec, ecErr := plus.ReadEC76()
	fmt.Printf("  ec76: %d err: %v\n", ec, ecErr)
	fmt.Printf("  pfand active: %v\n", plus.PfandActive())
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

	// Unlike nvme, the fan sample is read here rather than injected. The reason nvme
	// has to be injected is that agent/plus may not import agent; the reason this one
	// can be read here is the other half of that arrangement -- agent/plus may not
	// import the sqlite driver either, because it reaches os/exec through
	// modernc.org/libc. Package agent/pfanel owns the read and sits outside that
	// closure, so this harness may call it directly.
	fanPath := flag.String("fan-db", pfanel.DBPath,
		"pfanel history database to read the newest sample from; empty means none")
	fanSample, err := pfanel.LatestSample(*fanPath)
	if err != nil && !errors.Is(err, pfanel.ErrNoRow) {
		fmt.Fprintf(os.Stderr, "pluscheck: fan sample: %v\n", err)
	}

	stats, err := plus.Collect(*root, nvme, fanSample)
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
		if s.SpeedMBit == nil {
			unknownSpeed++
		}
		fmt.Printf("  %-18s rx=%-13d tx=%-12d pkts=%d/%d oper=%-8q speed=%-8s mtu=%s\n",
			name, s.RxBytes, s.TxBytes, derefU64(s.RxPackets), derefU64(s.TxPackets),
			s.OperState, speedStr(s.SpeedMBit), mtuStr(s.MTU))
	}
	fmt.Printf("  %d interface(s), %d with unknown speed, totals rx=%d tx=%d\n",
		len(n), unknownSpeed, sumRx, sumTx)
}

// speedStr renders the link rate, naming the unknown case rather than printing a
// bare 0 that reads like a real zero-Mbit link.
func speedStr(mbit *int) string {
	if mbit == nil {
		return "unknown"
	}
	return fmt.Sprintf("%dM", *mbit)
}

// mtuStr distinguishes "the interface did not report an MTU" from a real value.
func mtuStr(mtu *int) string {
	if mtu == nil {
		return "unknown"
	}
	return fmt.Sprintf("%d", *mtu)
}

func retryStr(v *uint64) string {
	if v == nil {
		return "unknown"
	}
	return fmt.Sprintf("%d", *v)
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

// printWiFi prints the radio attributes one wire name per line.
//
// Two of the fields are worth reading together rather than alone, which is why
// this is not one packed line: signal_dbm against tx_mbit separates a link that
// is far away or contended (level low, rate still negotiated high) from a radio
// with a transmit problem (level fine, rate collapsed), and that is the first
// question anyone asks about a NAS that dropped off the network.
//
// retry is the driver's lifetime TransmitFailed counter, not a rate, so the label
// says so; it also resets to zero when the interface reassociates, which is why
// the trend needs a second run and a guarded subtraction. It is the one field
// printed raw rather than through the unknown-formatter, because a zero here is
// worth seeing -- it is what a link with no failed frames looks like -- even
// though a station query that never succeeded looks the same, here and on the
// wire.
//
// The other three print "unknown" rather than a bare 0, because a zero is never a
// measurement of them: collectWiFi assigns signal_dbm only for a reading inside
// [-150, 0), freq_mhz only from a BSS that carried a frequency attribute, and
// tx_mbit only from a station reply that arrived and whose bitrate the driver may
// still have left out. omitempty then drops all three zeros, so the JSON key is
// missing in exactly the same cases. That is the rule WiFiStats' doc comment
// states, and this harness follows it instead of restating it more loosely.
func printWiFi(w *plus.WiFiStats) {
	fmt.Printf("  iface=%q\n", w.Iface)
	fmt.Printf("  signal_dbm=%s\n", dbmStr(w.SignalDbm))
	fmt.Printf("  retry=%s (cumulative, not a rate; resets on reassociation)\n", retryStr(w.Retry))
	fmt.Printf("  tx_mbit=%s\n", mbitStr(w.TxMBit))
	fmt.Printf("  freq_mhz=%s\n", mhzStr(w.FreqMHz))
}

// dbmStr, mbitStr and mhzStr name the unknown case instead of printing a bare 0.
func dbmStr(v *int) string {
	if v == nil {
		return "unknown"
	}
	return fmt.Sprintf("%d", *v)
}

func mbitStr(v *float64) string {
	if v == nil {
		return "unknown"
	}
	return fmt.Sprintf("%.1f", *v)
}

func mhzStr(v *int) string {
	if v == nil {
		return "unknown"
	}
	return fmt.Sprintf("%d", *v)
}

// printFan prints pfanel's newest history row.
//
// The two duty fields print through levelStr rather than a bare number, because
// pfanel stores NULL in both and the live table also holds real 0 values in both --
// 70 NULL levels against 226 rows at level 0, 63 NULL duties against 77 at duty 0.
// Printing either as a plain number would make "not recorded" and "set to zero" the
// same output, which is the confusion the pointer fields exist to prevent.
//
// The rpm line carries the label "ESTIMATE, not a tachometer reading" in full rather
// than relying on the field name. This host has no fan speed sensor -- fan_speed_rpm
// reads 0xFFFFFFFF -- and pfanel's stored rpm is a pure function of the duty byte:
// 50 distinct duties give exactly 50 distinct (duty, rpm) pairs, so the column is a
// table lookup carrying no information beyond ec76. A live run is the last place
// someone is likely to read that, so it is spelled out here.
func printFan(f *plus.FanStats) {
	fmt.Printf("  set_level_pct=%s  ec76_pct=%s  gov=%s\n",
		levelStr(f.SetLevelPct), levelStr(f.EC76Pct), govStr(f.Gov))
	fmt.Printf("  rpm_est=%d -- %s\n", f.RPMEst, estimateLabel(f.RPMEst))
	fmt.Printf("  temp_c=%s  sample_ts_s=%d\n", celsiusStr(f.TempC), f.SampleTsS)
}

// estimateLabel names the one thing a bare number cannot: that rpm_est is computed,
// not measured. Zero means no estimate was available, which is printed as such
// rather than as a fan turning at zero.
func estimateLabel(rpm uint16) string {
	if rpm == 0 {
		return "no estimate available (this host has no tachometer)"
	}
	return "ESTIMATE, not a tachometer reading"
}

func levelStr(v *int) string {
	if v == nil {
		return "unknown"
	}
	return fmt.Sprintf("%d", *v)
}

func govStr(v string) string {
	if v == "" {
		return "unknown"
	}
	return fmt.Sprintf("%q", v)
}

func celsiusStr(v float64) string {
	if v == 0 {
		return "unknown"
	}
	return fmt.Sprintf("%.1f C", v)
}

// printFanSteps prints the firmware's own duty table, one line per step.
//
// The payload is a slice rather than a map, so its order is the firmware's step
// order and this printer deliberately does not re-sort: a row that moves between
// two runs is the collector's bug, and sorting here would hide the one property
// the slice promises. Percent is a duty level the firmware accepts, RPMEst the
// speed it associates with that duty -- an estimate, never a tachometer reading --
// and the noise and power figures are what it declares for the same step.
//
// The count is printed next to the rows because the auto placeholder at state0 is
// excluded by design, so a table of 12 files reports 11 steps and a bare row count
// would otherwise look like a file went missing.
func printFanSteps(steps []plus.FanSpeedStep) {
	fmt.Printf("  %d step(s), auto placeholder excluded\n", len(steps))
	for _, s := range steps {
		fmt.Printf("  %3d%% -> %5d rpm, %6d mdb, %5d mW\n",
			s.Percent, s.RPMEst, s.NoiseMdb, s.PowerMw)
	}
}

func derefU64(v *uint64) uint64 {
	if v == nil {
		return 0
	}
	return *v
}
