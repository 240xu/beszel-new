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

	"github.com/henrygd/beszel/agent/plus"
)

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

	if stats.DPTF == nil || len(stats.DPTF.Zones) == 0 {
		fmt.Printf("root %s: no DPTF zones found\n", *root)
		return
	}

	zones := stats.DPTF.Zones
	keys := make([]string, 0, len(zones))
	for k := range zones {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	fmt.Printf("root %s: %d DPTF zone(s)\n", *root, len(zones))
	for _, k := range keys {
		fmt.Printf("  %-20s %.1f C\n", k, zones[k])
	}
}
