//go:build testing

package plus

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCollectNetAllIncludesVirtualInterfaces(t *testing.T) {
	root := t.TempDir()
	for name, rx := range map[string]string{
		"wlp0s20f3":       "21898421",
		"vethf9b5091":     "866312",
		"docker0":         "0",
		"enp0s31f6":       "0",
		"br-3afba6b18fd3": "5417",
	} {
		iface := filepath.Join(root, "class", "net", name)
		writeFile(t, filepath.Join(iface, "statistics", "rx_bytes"), rx+"\n")
		writeFile(t, filepath.Join(iface, "statistics", "tx_bytes"), "123\n")
		writeFile(t, filepath.Join(iface, "statistics", "rx_packets"), "10\n")
		writeFile(t, filepath.Join(iface, "statistics", "tx_packets"), "11\n")
		writeFile(t, filepath.Join(iface, "operstate"), "up\n")
		writeFile(t, filepath.Join(iface, "mtu"), "1500\n")
		writeFile(t, filepath.Join(iface, "speed"), "1000\n")
	}

	got := collectNetAll(root)
	require.Len(t, got, 5)
	assert.Contains(t, got, "vethf9b5091")
	assert.Contains(t, got, "docker0")
	assert.Equal(t, uint64(21898421), got["wlp0s20f3"].RxBytes)
	assert.Equal(t, "up", got["wlp0s20f3"].OperState)
	assert.Equal(t, 1000, got["wlp0s20f3"].SpeedMBit)
	assert.Equal(t, 1500, got["wlp0s20f3"].MTU)
}

func TestCollectNetAllReturnsNilWithoutInterfaces(t *testing.T) {
	assert.Nil(t, collectNetAll(t.TempDir()))
}

// netIfacePath is the sysfs location of one interface beneath a fixture root.
//
// The fixture root stands in for a sysfs root, the same value Collect receives in
// production, so the class/net prefix belongs here and not inside the collector.
// Building it in the helper keeps every test below on one spelling of that
// prefix; a fixture that dropped it would pass against a collector that globbed
// <root>/net/* and then read nothing at all on the NAS.
func netIfacePath(root, name string) string {
	return filepath.Join(root, "class", "net", name)
}

// writeNetIface writes one complete interface directory and returns its path.
// Byte counters deliberately differ per direction so a swapped read is visible.
func writeNetIface(t *testing.T, root, name, rx, tx string) string {
	t.Helper()
	iface := netIfacePath(root, name)
	writeFile(t, filepath.Join(iface, "statistics", "rx_bytes"), rx+"\n")
	writeFile(t, filepath.Join(iface, "statistics", "tx_bytes"), tx+"\n")
	writeFile(t, filepath.Join(iface, "statistics", "rx_packets"), "10\n")
	writeFile(t, filepath.Join(iface, "statistics", "tx_packets"), "11\n")
	writeFile(t, filepath.Join(iface, "operstate"), "up\n")
	writeFile(t, filepath.Join(iface, "mtu"), "1500\n")
	writeFile(t, filepath.Join(iface, "speed"), "1000\n")
	return iface
}

// setSpeed rewrites one interface's speed file after writeNetIface created it.
func setSpeed(t *testing.T, root, name, content string) {
	t.Helper()
	writeFile(t, filepath.Join(netIfacePath(root, name), "speed"), content)
}

// TestCollectNetAllKeepsRxAndTxApart pins the two directions to their own
// counters. A collector that read rx_bytes for both fields would satisfy any
// test whose fixture happened to use one value, so the values here differ in
// length as well as magnitude.
func TestCollectNetAllKeepsRxAndTxApart(t *testing.T) {
	root := t.TempDir()
	// Byte counts as the NAS actually reports them for these two interfaces.
	writeNetIface(t, root, "lo", "3965781728", "3965781780")
	writeNetIface(t, root, "wlp0s20f3", "3361763402", "847991638")

	got := collectNetAll(root)
	require.Len(t, got, 2)
	assert.Equal(t, uint64(3965781728), got["lo"].RxBytes)
	assert.Equal(t, uint64(3965781780), got["lo"].TxBytes)
	assert.Equal(t, uint64(3361763402), got["wlp0s20f3"].RxBytes)
	assert.Equal(t, uint64(847991638), got["wlp0s20f3"].TxBytes)
}

// TestCollectNetAllTreatsNegativeSpeedAsUnknown covers the sentinel the kernel
// uses for "this interface has no link rate". docker0 and enp0s31f6 both report
// speed=-1 on the NAS. strconv.Atoi accepts "-1" without complaint, so the value
// reaches the payload verbatim unless it is filtered, and a negative rate is not
// something a consumer can compute with.
func TestCollectNetAllTreatsNegativeSpeedAsUnknown(t *testing.T) {
	root := t.TempDir()
	writeNetIface(t, root, "docker0", "0", "0")
	setSpeed(t, root, "docker0", "-1\n")
	writeNetIface(t, root, "enp0s31f6", "0", "0")
	setSpeed(t, root, "enp0s31f6", "-1\n")
	writeNetIface(t, root, "br-82552716bfdf", "20547644", "531696")

	got := collectNetAll(root)
	require.Len(t, got, 3)
	assert.Equal(t, 0, got["docker0"].SpeedMBit, "speed=-1 means unknown, not -1 Mbit/s")
	assert.Equal(t, 0, got["enp0s31f6"].SpeedMBit, "speed=-1 means unknown, not -1 Mbit/s")
	// Blanking every negative would also pass the two assertions above, so the
	// real readings have to survive.
	assert.Equal(t, 1000, got["br-82552716bfdf"].SpeedMBit)

	// The Go field being 0 is only half the claim: omitempty drops it, so an
	// unknown speed must leave the key out of the payload rather than ship a
	// sentinel a consumer has to recognise.
	b, err := json.Marshal(got)
	require.NoError(t, err)
	assert.NotContains(t, string(b), `"speed_mbit":-1`)
	assert.NotContains(t, string(b), `"speed_mbit":0`)
	assert.Contains(t, string(b), `"speed_mbit":1000`)
}

// TestCollectNetAllSurvivesUnreadableSpeed separates the two ways speed can fail
// to yield a number. On the NAS lo and wlp0s20f3 fail the read outright
// (EINVAL, so the file opens but reads nothing); a zero-byte file models an
// attribute that exists and is empty. Neither may cost the interface its byte
// counters, which are the entire point of this collector, and the two failures
// must not be conflated with each other or with the negative sentinel.
func TestCollectNetAllSurvivesUnreadableSpeed(t *testing.T) {
	root := t.TempDir()
	writeNetIface(t, root, "lo", "3965781728", "3965781780")
	setSpeed(t, root, "lo", "")
	writeNetIface(t, root, "wlp0s20f3", "3361763402", "847991638")
	require.NoError(t, os.Remove(filepath.Join(netIfacePath(root, "wlp0s20f3"), "speed")))

	got := collectNetAll(root)
	require.Len(t, got, 2)
	for _, name := range []string{"lo", "wlp0s20f3"} {
		s := got[name]
		require.NotNil(t, s, "interface %q must survive a speed it cannot read", name)
		assert.Equal(t, 0, s.SpeedMBit, "interface %q has no readable speed", name)
	}
	assert.Equal(t, uint64(3965781728), got["lo"].RxBytes)
	assert.Equal(t, uint64(3361763402), got["wlp0s20f3"].RxBytes)
}

// TestCollectNetAllDropsInterfacesWithoutByteCounters pins the membership rule:
// rx_bytes and tx_bytes decide whether an interface is reported at all, because
// a counter pair that is silently zero-filled is indistinguishable from an idle
// interface. Everything else is optional and filled in when present.
func TestCollectNetAllDropsInterfacesWithoutByteCounters(t *testing.T) {
	root := t.TempDir()
	writeNetIface(t, root, "docker0", "0", "0")

	// rx_bytes present but empty parses to nothing.
	emptyRx := netIfacePath(root, "br-empty-rx")
	writeFile(t, filepath.Join(emptyRx, "statistics", "rx_bytes"), "")
	writeFile(t, filepath.Join(emptyRx, "statistics", "tx_bytes"), "5\n")

	// rx_bytes absent entirely.
	missingRx := netIfacePath(root, "br-missing-rx")
	writeFile(t, filepath.Join(missingRx, "statistics", "tx_bytes"), "5\n")

	// rx readable, tx missing: half a counter pair is not a report.
	missingTx := netIfacePath(root, "br-missing-tx")
	writeFile(t, filepath.Join(missingTx, "statistics", "rx_bytes"), "7\n")

	got := collectNetAll(root)
	require.Len(t, got, 1, "only the interface with both byte counters may be reported")
	require.Contains(t, got, "docker0")
}

// TestCollectNetAllKeepsUnknownOperState guards the reading of operstate as an
// opaque string. "unknown" is a state the kernel really reports (lo does), not a
// missing file, and neither is "down" (docker0 and enp0s31f6 report it).
func TestCollectNetAllKeepsUnknownOperState(t *testing.T) {
	root := t.TempDir()
	writeNetIface(t, root, "lo", "3965781728", "3965781780")
	writeFile(t, filepath.Join(netIfacePath(root, "lo"), "operstate"), "unknown\n")
	writeFile(t, filepath.Join(netIfacePath(root, "lo"), "mtu"), "65536\n")
	writeNetIface(t, root, "docker0", "0", "0")
	writeFile(t, filepath.Join(netIfacePath(root, "docker0"), "operstate"), "down\n")

	got := collectNetAll(root)
	require.Len(t, got, 2)
	assert.Equal(t, "unknown", got["lo"].OperState)
	assert.Equal(t, "down", got["docker0"].OperState)
	// lo carries a loopback MTU, not the 1500 every physical and virtual NIC
	// shares here, so a hardcoded or truncated MTU is visible.
	assert.Equal(t, 65536, got["lo"].MTU)
	assert.Equal(t, 1500, got["docker0"].MTU)
	assert.Equal(t, uint64(10), got["lo"].RxPackets)
	assert.Equal(t, uint64(11), got["lo"].TxPackets)
}

// TestCollectNetAllTagsNameTheFieldNotTheFile guards the wire names of this
// struct. The sysfs attributes are lowercase (operstate) while the payload calls
// the field oper_state, and speed_mbit is the corrected tag: speed_mbps is
// ambiguous between megabits and megabytes and was the one offender this struct
// carried. Reading a file name straight into a tag, or reverting the rename, is
// invisible to every assertion that reads Go fields instead of bytes.
func TestCollectNetAllTagsNameTheFieldNotTheFile(t *testing.T) {
	root := t.TempDir()
	writeNetIface(t, root, "docker0", "0", "0")
	writeNetIface(t, root, "wlp0s20f3", "3361763402", "847991638")

	b, err := json.Marshal(collectNetAll(root))
	require.NoError(t, err)
	str := string(b)
	for _, want := range []string{
		`"oper_state"`, `"mtu"`, `"speed_mbit"`, `"rx_bytes"`, `"tx_bytes"`,
	} {
		assert.Contains(t, str, want, "json tag %s must be present", want)
	}
	for _, bad := range []string{
		`"speed_mbps"`, `"speed"`, `"operstate"`, `"MTU"`, `"oper_state_name"`,
	} {
		assert.NotContains(t, str, bad, "json tag %s must not appear", bad)
	}
}

// TestCollectNetAllGlobsUnderClassNetOnly is the path-semantics guard. Every
// decoy below is a directory that a glob at the wrong depth would mistake for an
// interface, so the assertion is on the exact entry count and value rather than
// on a subset.
func TestCollectNetAllGlobsUnderClassNetOnly(t *testing.T) {
	root := t.TempDir()
	writeNetIface(t, root, "docker0", "11", "22")

	// One level too shallow: a <root>/class/* glob matches the net directory
	// itself and reads this as if "net" were an interface named net.
	writeFile(t, filepath.Join(root, "class", "net", "statistics", "rx_bytes"), "999\n")
	writeFile(t, filepath.Join(root, "class", "net", "statistics", "tx_bytes"), "888\n")

	// One level too deep: a <root>/class/net/*/* glob matches this nested
	// directory and reports it as an interface named veth0.
	nested := filepath.Join(netIfacePath(root, "docker0"), "veth0")
	writeFile(t, filepath.Join(nested, "statistics", "rx_bytes"), "555\n")
	writeFile(t, filepath.Join(nested, "statistics", "tx_bytes"), "444\n")

	// Same interface name elsewhere in the tree, which a collector that reached
	// outside sysfs would fold into the result.
	elsewhere := filepath.Join(root, "devices", "net", "docker0")
	writeFile(t, filepath.Join(elsewhere, "statistics", "rx_bytes"), "777\n")
	writeFile(t, filepath.Join(elsewhere, "statistics", "tx_bytes"), "666\n")

	got := collectNetAll(root)
	require.Len(t, got, 1, "only class/net/<iface> entries are interfaces")
	require.Contains(t, got, "docker0")
	assert.NotContains(t, got, "net")
	assert.NotContains(t, got, "veth0")
	assert.Equal(t, uint64(11), got["docker0"].RxBytes)
	assert.Equal(t, uint64(22), got["docker0"].TxBytes)
}

// TestCollectNetAllKeysInterfacesByVerbatimName checks the map keys. Interface
// names on this host carry hyphens and never a separator, and a VLAN-style dot is
// legal too, so no name filtering or rewriting may be introduced.
func TestCollectNetAllKeysInterfacesByVerbatimName(t *testing.T) {
	root := t.TempDir()
	names := []string{
		"br-3afba6b18fd3",
		"docker0",
		"enp0s31f6",
		"lo",
		"veth1255f81",
		"vetheaa147b",
		"wlp0s20f3",
		"eth0.100", // VLAN-style name: legal, and not filtered away
	}
	for i, name := range names {
		writeNetIface(t, root, name, "1", "2")
		got := collectNetAll(root)
		require.Len(t, got, i+1, "interface %q must be reported", name)
		assert.Contains(t, got, name, "interface %q must keep its verbatim name", name)
	}
	assert.Len(t, collectNetAll(root), len(names))
}

// TestCollectNetAllCoversExactlyWhatUpstreamFilters is the falsifiable half of the
// collector's rationale. Upstream enumerates every interface and then keeps a
// name whitelist (skipNetworkInterface, agent/network.go:247) that drops the
// lo/docker/br-/veth/bond/cali prefixes plus any interface whose rx or tx reads
// zero. This test pins the six prefixes as data, so inheriting upstream's filter
// here is a test failure rather than a silent divergence, and adds lowpan0 --
// which upstream drops only because HasPrefix("lowpan0", "lo") is true.
//
// The counts asserted here are the fixture's, not the host's: the real host
// splits 26-by-prefix / 1-by-idle / 1-survivor, which is recorded in netall.go's
// rationale rather than pinned here because container churn changes it hourly.
func TestCollectNetAllCoversExactlyWhatUpstreamFilters(t *testing.T) {
	root := t.TempDir()
	// One interface per prefix upstream drops, each carrying real traffic so a
	// zero-traffic rule could not be what keeps them in the result.
	for _, name := range []string{"lo", "lowpan0", "docker0", "br-3afba6b18fd3", "veth1255f81", "bond0", "cali0"} {
		writeNetIface(t, root, name, "126", "61484")
	}
	// The category that is NOT a name rule: an idle physical NIC. Upstream drops
	// this one for reading zero, which is why "upstream reports one interface" is
	// a fact about this host rather than a property of upstream.
	writeNetIface(t, root, "enp0s31f6", "0", "0")
	// And a busy physical NIC, the one upstream would keep.
	writeNetIface(t, root, "wlp0s20f3", "3373750863", "856981582")

	got := collectNetAll(root)
	require.Len(t, got, 9, "no interface may be dropped for a name upstream also drops")
	for name, s := range got {
		require.NotNil(t, s, "interface %q must be reported", name)
	}
	for _, name := range []string{"lo", "lowpan0", "docker0", "br-3afba6b18fd3", "veth1255f81", "bond0", "cali0", "enp0s31f6", "wlp0s20f3"} {
		assert.Contains(t, got, name, "interface %q must survive", name)
	}
	// Blanking the counters would pass the containment assertions above, so the
	// traffic that keeps each interface eligible has to survive too.
	assert.Equal(t, uint64(3373750863), got["wlp0s20f3"].RxBytes)
	assert.Equal(t, uint64(856981582), got["wlp0s20f3"].TxBytes)
	assert.Equal(t, uint64(126), got["veth1255f81"].RxBytes)
	assert.Equal(t, uint64(0), got["enp0s31f6"].RxBytes, "an idle NIC is reported, not dropped")
}

// TestCollectNetAllReportsCumulativeCountersVerbatim pins the "cumulative
// snapshot, not a rate" half of the collector contract. Values are the host's
// real lo readings, which exceed 2^31 so a 32-bit accumulator or a /1024 or
// /1000 rescale would all show, and they do not divide evenly so a truncated
// rate cannot coincidentally match.
func TestCollectNetAllReportsCumulativeCountersVerbatim(t *testing.T) {
	root := t.TempDir()
	iface := writeNetIface(t, root, "lo", "4003869867", "4003869988")
	writeFile(t, filepath.Join(iface, "statistics", "rx_packets"), "5038409\n")
	writeFile(t, filepath.Join(iface, "statistics", "tx_packets"), "5038412\n")

	got := collectNetAll(root)
	require.Len(t, got, 1)
	s := got["lo"]
	require.NotNil(t, s)
	assert.Equal(t, uint64(4003869867), s.RxBytes, "rx_bytes is the raw sysfs counter, not a rate or a delta")
	assert.Equal(t, uint64(4003869988), s.TxBytes, "tx_bytes is the raw sysfs counter, not a rate or a delta")
	assert.Equal(t, uint64(5038409), s.RxPackets, "rx_packets is the raw sysfs counter")
	assert.Equal(t, uint64(5038412), s.TxPackets, "tx_packets is the raw sysfs counter")
}

// TestCollectNetAllSurfacesCounterReset guards the other half of the contract:
// a reset must be visible as a decrease, because that decrease is the only
// signal a consumer has to detect the reset and refuse to difference across it.
//
// The sequence is the one observed in production, not a construction: this host
// rebooted between two audits and lo's rx_bytes went from 4,003,869,867 back to
// zero. The assertions pin that the collector reports the zero rather than
// clamping, carrying forward or wrapping, and that the interface survives it --
// dropping the entry would hide the reset behind a missing key.
func TestCollectNetAllSurfacesCounterReset(t *testing.T) {
	root := t.TempDir()
	writeNetIface(t, root, "lo", "4003869867", "4003869988")

	before := collectNetAll(root)
	require.Len(t, before, 1)
	require.Equal(t, uint64(4003869867), before["lo"].RxBytes)

	// Interface bounces (or the container is recreated): the kernel zeroes the
	// counters while the name stays put.
	iface := netIfacePath(root, "lo")
	writeFile(t, filepath.Join(iface, "statistics", "rx_bytes"), "0\n")
	writeFile(t, filepath.Join(iface, "statistics", "tx_bytes"), "0\n")
	writeFile(t, filepath.Join(iface, "statistics", "rx_packets"), "0\n")
	writeFile(t, filepath.Join(iface, "statistics", "tx_packets"), "0\n")

	after := collectNetAll(root)
	require.Len(t, after, 1, "a reset must not remove the interface; the consumer needs the zero")
	require.NotNil(t, after["lo"])
	assert.Equal(t, uint64(0), after["lo"].RxBytes, "the reset is reported verbatim, not clamped or carried forward")
	assert.Equal(t, uint64(0), after["lo"].TxBytes, "the reset is reported verbatim, not clamped or carried forward")
	assert.Less(t, after["lo"].RxBytes, before["lo"].RxBytes,
		"the decrease is the only reset signal a consumer gets, so it must survive collection")

	// On the wire a reset shows as bytes pinned at 0 while the packet counters
	// vanish under omitempty. Both halves matter: a consumer cannot read the
	// reset off a missing packet key, and cannot read it off a missing byte key
	// either, because bytes have no omitempty and are therefore always present.
	b, err := json.Marshal(after)
	require.NoError(t, err)
	assert.Contains(t, string(b), `"rx_bytes":0`, "rx_bytes has no omitempty, so zero must still be on the wire")
	assert.NotContains(t, string(b), `"rx_packets"`, "rx_packets has omitempty, so a zero packet count vanishes")
}

// TestCollectNetAllIsStatelessAcrossCalls is the guard on the choice to keep this
// collector a stateless snapshot rather than have it difference counters itself.
// Calling it twice over an unchanged tree must return identical values, and a
// later call over grown counters must return the new raw values -- not a delta
// against the previous call, and not the previous call's numbers.
//
// This is what makes the documented contract falsifiable: any future in-Go delta
// or baseline implementation turns the first assertion red, so switching to that
// design has to be a deliberate edit here rather than an accident.
func TestCollectNetAllIsStatelessAcrossCalls(t *testing.T) {
	root := t.TempDir()
	writeNetIface(t, root, "veth1255f81", "126", "61484")

	first := collectNetAll(root)
	second := collectNetAll(root)
	require.Len(t, first, 1)
	require.Len(t, second, 1)
	assert.Equal(t, first, second, "two calls over an unchanged tree must agree exactly")

	// The kernel grows the counters between cycles. A collector holding a
	// baseline would emit 0 here; one carrying state forward would emit 126.
	iface := netIfacePath(root, "veth1255f81")
	writeFile(t, filepath.Join(iface, "statistics", "rx_bytes"), "8762\n")
	writeFile(t, filepath.Join(iface, "statistics", "tx_bytes"), "117771\n")

	third := collectNetAll(root)
	require.Len(t, third, 1)
	require.NotNil(t, third["veth1255f81"])
	assert.Equal(t, uint64(8762), third["veth1255f81"].RxBytes, "the new raw value, not a delta against the previous call")
	assert.Equal(t, uint64(117771), third["veth1255f81"].TxBytes, "the new raw value, not a delta against the previous call")
}
