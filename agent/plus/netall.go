package plus

import (
	"path/filepath"
	"strconv"
)

// collectNetAll reports counters and link state for every network interface the
// kernel exposes. root is the sysfs mount point, so interfaces live at
// <root>/class/net/<iface> and Collect passes "/sys".
//
// Rationale: upstream filters out exactly the interfaces container traffic
// arrives on. It enumerates all 28 here (psutilNet.IOCounters(true), agent/
// network.go:85) but caches the survivors as a process-lifetime whitelist
// (initializeNetIoStats :93, enforced :167) after skipNetworkInterface (:247)
// drops the lo/docker/br-/veth/bond/cali prefixes (:260) and any interface whose
// rx or tx reads zero (:266). One survives: wlp0s20f3, with 26 dropped by prefix
// and enp0s31f6 for being idle -- so "upstream reports one interface" is this
// host, not upstream (two active NICs would give two). Container links are
// always dropped by name, and one idle at agent startup stays dropped until
// :133 or :233 rebuilds the whitelist.
//
// Contract: Rx/TxBytes and Rx/TxPackets are CUMULATIVE, not rates, and this is a
// stateless snapshot -- it never differences, scales or smooths, so two calls
// over an unchanged tree agree exactly. Consumers must take their own delta and
// MUST guard it: the counters zero on an interface bounce or a container
// recreate, and an unguarded uint64 cur-prev wraps to ~1.8e19. Upstream guards
// the same hazard by comparison alone (agent/network.go:184), so reuse that
// rule -- cur < prev means a reset.
//
// Membership is decided by the two byte counters alone; packets, operstate, MTU
// and speed are attributes a driver may decline, and unreadable counters drop
// the interface rather than zero-fill it -- docker0 and enp0s31f6 idle at zero,
// so "unknown" must stay distinct from "idle".
//
// Speed is Mbit/s; the kernel's -1 "unknown" sentinel and any unreadable or
// non-positive value leave the field nil, so the key is absent rather than a
// reported 0 Mbit/s.
func collectNetAll(root string) map[string]*NetIfaceStats {
	matches, err := filepath.Glob(filepath.Join(root, "class", "net", "*"))
	if err != nil || len(matches) == 0 {
		return nil
	}
	out := make(map[string]*NetIfaceStats)
	for _, dir := range matches {
		rx, err := readUint(filepath.Join(dir, "statistics", "rx_bytes"))
		if err != nil {
			continue
		}
		tx, err := readUint(filepath.Join(dir, "statistics", "tx_bytes"))
		if err != nil {
			continue
		}
		s := &NetIfaceStats{RxBytes: rx, TxBytes: tx}
		if v, err := readUint(filepath.Join(dir, "statistics", "rx_packets")); err == nil {
			s.RxPackets = &v
		}
		if v, err := readUint(filepath.Join(dir, "statistics", "tx_packets")); err == nil {
			s.TxPackets = &v
		}
		if v, err := readTrimmed(filepath.Join(dir, "operstate")); err == nil {
			s.OperState = v
		}
		if v, err := readTrimmed(filepath.Join(dir, "speed")); err == nil {
			// Linux reports the link speed in Mbit/s; the _mbit suffix keeps the
			// unit explicit and matches the other rate fields. n > 0 rejects the
			// kernel's -1 "unknown" sentinel, leaving SpeedMBit nil: an unreadable
			// speed is absent from the wire, not a 0 Mbit/s link.
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				s.SpeedMBit = &n
			}
		}
		if v, err := readUint(filepath.Join(dir, "mtu")); err == nil {
			n := int(v)
			s.MTU = &n
		}
		out[filepath.Base(dir)] = s
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
