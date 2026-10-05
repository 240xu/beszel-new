package plus

import (
	"path/filepath"
	"strconv"
)

// collectNetAll reports byte, packet, MTU and link state for every network
// interface the kernel exposes, including the veth pairs and docker bridges that
// upstream beszel never looks at.
//
// The reason to enumerate is container visibility. Upstream reports the primary
// interface alone, so the one interface the dashboard charts hides most of the
// traffic: on this host 21 of the 28 interfaces are veth pairs, and a veth pair
// is a container's link to the host, which makes its counters the closest thing
// to per-container network accounting the kernel offers.
//
// Membership is decided by the two byte counters alone. rx_bytes and tx_bytes are
// the payload; packets, operstate, MTU and speed are attributes a driver may
// decline to implement. An interface whose byte counters cannot both be read is
// dropped rather than reported as zeros, because a zero-filled entry is
// indistinguishable from an idle interface -- and docker0 and enp0s31f6 really do
// idle at zero, so "unknown" has to stay distinguishable from "idle".
//
// root is the sysfs mount point, so the interfaces live at
// <root>/class/net/<iface>, not at <root>/net/<iface> or <root>/class/<iface>.
// In production Collect passes ProdRoot ("/sys"), so a glob joined to any other
// depth matches nothing here and the collector reports no traffic at all while
// still looking healthy.
//
// Interface names are map keys verbatim. They carry hyphens ("veth1255f81",
// "br-3afba6b18fd3") and may carry a dot for VLANs ("eth0.100"); neither is a
// path separator, so Base recovers the name with no filtering or rewriting.
//
// Speed is in Mbit/s, and 0 means unknown. That is the convention collectCpufreq
// already established for an unreadable scaling bound: one decidable fill value
// instead of a parallel validity vector. The kernel answers -1 for an interface
// with no link rate -- docker0 and enp0s31f6 both report -1 -- and -1 is not a
// rate, so passing it through would publish a negative speed that no consumer can
// divide by. A missing or empty speed file leaves the same 0. Paired with the
// omitempty on the tag, unknown means the field is absent from the payload rather
// than present and wrong.
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
			s.RxPackets = v
		}
		if v, err := readUint(filepath.Join(dir, "statistics", "tx_packets")); err == nil {
			s.TxPackets = v
		}
		if v, err := readTrimmed(filepath.Join(dir, "operstate")); err == nil {
			s.OperState = v
		}
		if v, err := readTrimmed(filepath.Join(dir, "speed")); err == nil {
			// Linux reports the link speed in Mbit/s; the _mbit suffix keeps the
			// unit explicit and matches the other rate fields. n > 0 rejects the
			// kernel's -1 "unknown" sentinel, leaving SpeedMBit at its zero fill.
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				s.SpeedMBit = n
			}
		}
		if v, err := readUint(filepath.Join(dir, "mtu")); err == nil {
			s.MTU = int(v)
		}
		out[filepath.Base(dir)] = s
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
