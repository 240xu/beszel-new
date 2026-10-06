//go:build testing

package plus

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
	"time"

	native "github.com/mdlayher/wifi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeWiFiClient serves fixtures for the nl80211 mapping. One station per BSSID,
// and every call is recorded so a test can prove which interface was queried.
type fakeWiFiClient struct {
	ifaces    []*native.Interface
	ifacesErr error
	bss       map[string]*native.BSS
	bssErr    error
	station   map[string]*native.StationInfo
	stErr     error
	deadline  time.Time
	closed    bool
	bssAsked  []string
}

func (f *fakeWiFiClient) Interfaces() ([]*native.Interface, error) {
	return f.ifaces, f.ifacesErr
}

func (f *fakeWiFiClient) BSS(ifi *native.Interface) (*native.BSS, error) {
	f.bssAsked = append(f.bssAsked, ifi.Name)
	if f.bssErr != nil {
		return nil, f.bssErr
	}
	return f.bss[ifi.Name], nil
}

func (f *fakeWiFiClient) Station(ifi *native.Interface, mac net.HardwareAddr) (*native.StationInfo, error) {
	if f.stErr != nil {
		return nil, f.stErr
	}
	return f.station[ifi.Name], nil
}

func (f *fakeWiFiClient) SetDeadline(t time.Time) error {
	f.deadline = t
	return nil
}

func (f *fakeWiFiClient) Close() error {
	f.closed = true
	return nil
}

var testBSSID = net.HardwareAddr{0x02, 0x11, 0x22, 0x33, 0x44, 0x55}

// associated builds the normal case: one station interface, associated, with a
// station reply. Values are the ones measured on the real NAS so a unit or field
// mistake shows up as a number that cannot be right.
func associated() *fakeWiFiClient {
	return &fakeWiFiClient{
		ifaces: []*native.Interface{
			{Name: "wlp0s20f3", Index: 3, Type: native.InterfaceTypeStation},
		},
		bss: map[string]*native.BSS{
			"wlp0s20f3": {
				SSID:      "CMCC-Da3Z-5G",
				BSSID:     testBSSID,
				Frequency: 5180,
				Status:    native.BSSStatusAssociated,
				// The scan cache, in mBm. Deliberately inconsistent with
				// StationInfo.Signal so a mix-up cannot pass by coincidence.
				Signal: -3700,
			},
		},
		station: map[string]*native.StationInfo{
			"wlp0s20f3": {Signal: -42, TransmitBitrate: 780_000_000, TransmitFailed: 1146, TransmitRetries: 98464},
		},
	}
}

func TestCollectWiFiMapsAllFourFields(t *testing.T) {
	got := collectWiFiWith(associated())
	require.NotNil(t, got)
	assert.Equal(t, "wlp0s20f3", got.Iface)
	assert.Equal(t, 5180, got.FreqMHz)
	assert.Equal(t, -42, got.SignalDbm)
	assert.Equal(t, 780.0, got.TxMBit)
	assert.Equal(t, uint64(1146), got.Retry)
}

// TestCollectWiFiRetryIsTransmitFailed pins the counter identity. TransmitFailed
// and TransmitRetries sit next to each other in StationInfo and differ by two
// orders of magnitude in the fixture, so reading the wrong one cannot pass.
func TestCollectWiFiRetryIsTransmitFailed(t *testing.T) {
	client := associated()
	got := collectWiFiWith(client)
	require.NotNil(t, got)
	assert.Equal(t, uint64(1146), got.Retry, "retry must be TransmitFailed")
	assert.NotEqual(t, uint64(98464), got.Retry, "TransmitRetries is a different counter")
}

// TestCollectWiFiTxMBitConvertsBitsPerSecond pins the unit. TransmitBitrate is
// bits/second, so 780000000 is 780 Mbit/s. The neighbouring RateInfo.Bitrate is
// in 100 kbit/s units and a /10 conversion there is the wrong answer by 1e5.
func TestCollectWiFiTxMBitConvertsBitsPerSecond(t *testing.T) {
	client := associated()
	client.station["wlp0s20f3"] = &native.StationInfo{Signal: -42, TransmitBitrate: 780_000_000}
	got := collectWiFiWith(client)
	require.NotNil(t, got)
	assert.Equal(t, 780.0, got.TxMBit, "bits/second divided by 1e6, not by 10")
	assert.NotEqual(t, 78_000_000.0, got.TxMBit)
}

// TestCollectWiFiSignalIsStationNotBSSCache pins the source and its unit.
// BSS.Signal is the scan cache in mBm and StationInfo.Signal is dBm; the
// fixture gives them different magnitudes so only the right one can pass.
func TestCollectWiFiSignalIsStationNotBSSCache(t *testing.T) {
	client := associated()
	got := collectWiFiWith(client)
	require.NotNil(t, got)
	assert.Equal(t, -42, got.SignalDbm, "StationInfo.Signal is already dBm")
	assert.NotEqual(t, -3700, got.SignalDbm, "BSS.Signal is mBm and must not be used")
	assert.NotEqual(t, -37, got.SignalDbm, "BSS.Signal is not divided here")
}

// TestCollectWiFiFreqIsBSSFrequency pins where freq_mhz comes from. The
// associated BSS is authoritative; Interface.Frequency can differ before
// association completes and is not a fallback.
func TestCollectWiFiFreqIsBSSFrequency(t *testing.T) {
	client := associated()
	client.ifaces[0].Frequency = 2412
	got := collectWiFiWith(client)
	require.NotNil(t, got)
	assert.Equal(t, 5180, got.FreqMHz, "the associated BSS frequency")
	assert.NotEqual(t, 2412, got.FreqMHz, "Interface.Frequency is not the source")
}

func TestCollectWiFiSkipsUnassociatedAndNonStation(t *testing.T) {
	client := associated()
	client.ifaces = []*native.Interface{
		{Name: "wlan0ap", Index: 1, Type: native.InterfaceTypeAP},
		{Name: "wlp0s20f3", Index: 3, Type: native.InterfaceTypeStation},
	}
	client.bss["wlp0s20f3"] = &native.BSS{
		BSSID: testBSSID, Frequency: 5180, Status: native.BSSStatusAuthenticated,
	}
	assert.Nil(t, collectWiFiWith(client), "only an associated BSS counts")
}

func TestCollectWiFiTakesTheFirstAssociatedStation(t *testing.T) {
	client := associated()
	client.ifaces = []*native.Interface{
		{Name: "wlan0ap", Index: 1, Type: native.InterfaceTypeAP},
		{Name: "wlp1s20f3", Index: 2, Type: native.InterfaceTypeStation},
		{Name: "wlp0s20f3", Index: 3, Type: native.InterfaceTypeStation},
	}
	client.bss["wlan0ap"] = &native.BSS{Frequency: 2412, Status: native.BSSStatusAssociated}
	client.bss["wlp1s20f3"] = &native.BSS{
		BSSID: testBSSID, Frequency: 2412, Status: native.BSSStatusAssociated,
	}
	client.station["wlan0ap"] = &native.StationInfo{Signal: -70, TransmitBitrate: 72_200_000}
	client.station["wlp1s20f3"] = &native.StationInfo{Signal: -55, TransmitBitrate: 144_400_000}
	got := collectWiFiWith(client)
	require.NotNil(t, got)
	assert.Equal(t, "wlp1s20f3", got.Iface, "Stats.WiFi is one object, so the first one wins")
	assert.Equal(t, 2412, got.FreqMHz)
	assert.NotEqual(t, "wlp0s20f3", got.Iface)
}

func TestCollectWiFiKeepsAssociationWhenStationStatsAreDenied(t *testing.T) {
	for name, deny := range map[string]func(*fakeWiFiClient){
		"the query fails": func(c *fakeWiFiClient) { c.stErr = errors.New("operation not permitted") },
		"the reply is nil": func(c *fakeWiFiClient) {
			c.station = map[string]*native.StationInfo{}
		},
	} {
		t.Run(name, func(t *testing.T) { assertWifiKeepsAssociationOnly(t, deny) })
	}
}

func assertWifiKeepsAssociationOnly(t *testing.T, deny func(*fakeWiFiClient)) {
	t.Helper()
	client := associated()
	deny(client)
	got := collectWiFiWith(client)
	require.NotNil(t, got, "the BSS already proved association")
	assert.Equal(t, "wlp0s20f3", got.Iface)
	assert.Equal(t, 5180, got.FreqMHz)
	assert.Zero(t, got.SignalDbm)
	assert.Zero(t, got.TxMBit)
	assert.Zero(t, got.Retry)
}

func TestCollectWiFiKeepsAssociationWhenBSSIDIsUnknown(t *testing.T) {
	client := associated()
	client.bss["wlp0s20f3"].BSSID = nil
	got := collectWiFiWith(client)
	require.NotNil(t, got)
	assert.Equal(t, 5180, got.FreqMHz)
	assert.Zero(t, got.SignalDbm)
}

func TestCollectWiFiSkipsNilAndNamelessInterfaces(t *testing.T) {
	client := associated()
	client.ifaces = []*native.Interface{
		nil,
		{Index: 2, Type: native.InterfaceTypeStation},
	}
	assert.Nil(t, collectWiFiWith(client))
	assert.Empty(t, client.bssAsked, "no BSS query for an interface with no name")
}

func TestCollectWiFiNilOnEveryErrorPath(t *testing.T) {
	for name, setup := range map[string]func(*fakeWiFiClient){
		"interface list fails":     func(c *fakeWiFiClient) { c.ifacesErr = errors.New("boom") },
		"interface list is empty":  func(c *fakeWiFiClient) { c.ifaces = nil },
		"BSS query fails":          func(c *fakeWiFiClient) { c.bssErr = errors.New("boom") },
		"BSS reply is nil":         func(c *fakeWiFiClient) { c.bss = map[string]*native.BSS{} },
		"no interface is a radio":  func(c *fakeWiFiClient) { c.ifaces[0].Type = native.InterfaceTypeAP },
		"associated BSS is absent": func(c *fakeWiFiClient) { delete(c.bss, "wlp0s20f3") },
	} {
		t.Run(name, func(t *testing.T) {
			client := associated()
			setup(client)
			assert.Nil(t, collectWiFiWith(client), "a failure omits the section rather than reporting zeros")
		})
	}
}

func TestCollectWiFiRejectsImplausibleSignal(t *testing.T) {
	for name, signal := range map[string]int{
		"no reading at all":    0,
		"positive, impossible": 40,
		"below the floor":      -300,
	} {
		t.Run(name, func(t *testing.T) {
			client := associated()
			client.station["wlp0s20f3"] = &native.StationInfo{
				Signal: signal, TransmitBitrate: 780_000_000, TransmitFailed: 1146,
			}
			got := collectWiFiWith(client)
			require.NotNil(t, got)
			assert.Zero(t, got.SignalDbm, "out-of-range RSSI is dropped, not published")
			assert.Equal(t, 780.0, got.TxMBit, "the other fields survive")
		})
	}
}

// TestCollectWiFiDropsNegativeRetry guards the uint64 conversion. A malformed
// reply must not wrap into a value near 1.8e19.
func TestCollectWiFiDropsNegativeRetry(t *testing.T) {
	client := associated()
	client.station["wlp0s20f3"] = &native.StationInfo{
		Signal: -42, TransmitBitrate: 780_000_000, TransmitFailed: -1,
	}
	got := collectWiFiWith(client)
	require.NotNil(t, got)
	assert.Zero(t, got.Retry)
}

func TestCollectWiFiSetsTheDeadline(t *testing.T) {
	client := associated()
	before := time.Now()
	collectWiFiWith(client)
	assert.False(t, client.deadline.Before(before.Add(wifiTimeout-time.Second)),
		"the exchange must be bounded")
	assert.False(t, client.deadline.After(time.Now().Add(wifiTimeout+time.Second)),
		"and not outlive the budget")
}

// TestCollectWiFiGatesOnCfg80211Devices proves the modprobe guard. Without it a
// host with no radio would run modprobe on every poll.
func TestCollectWiFiGatesOnCfg80211Devices(t *testing.T) {
	empty := t.TempDir()
	assert.Nil(t, collectWiFi(empty), "no registered device means no netlink query")
	withRadio := t.TempDir()
	wiphy := filepath.Join(withRadio, "class", "ieee80211")
	require.NoError(t, os.MkdirAll(filepath.Join(wiphy, "phy0"), 0o755))
	assert.True(t, hasWirelessDevice(wiphy))
}

func TestHasWirelessDeviceIgnoresMissingDir(t *testing.T) {
	assert.False(t, hasWirelessDevice(t.TempDir()+"/absent"))
}

func TestNL80211ClientImplementsWifiClient(t *testing.T) {
	var _ wifiClient = (*nl80211Client)(nil)
}

// ---------------------------------------------------------------------------
// /proc/net/wireless marker bytes -- characterisation, not production coverage.
//
// Task 7's first implementation read /proc/net/wireless and ran iw. Both are
// gone: nl80211 supplies all four fields directly (wifi_netlink_linux.go), so no
// procfs parse and no subprocess remain. The knowledge below is kept because it
// is why that simplification was safe, and because the trap it describes is the
// one a future reader re-enters first.
//
// The kernel prints each quality column as %3d followed by a marker byte that is
// '.' only when the driver set the matching IW_QUAL_*_UPDATED bit, and ' ' when
// it did not. Confirmed on this host with cat -A:
//
//	wlp0s20f3: 0000   70.  -37.  -256        0      0      0   1187      4        0
//	                        ^^  ^^  ^^^ space
//
// So link and level carry '.' here while noise carries a space, and that is a
// per-driver, per-poll difference rather than a fixed layout. Requiring the dot
// rejects drivers that leave the flags clear; forbidding it rejects the ones that
// set them. A correct pattern therefore accepts both, which is (-?\d+)[. ]: inside
// a character class a dot is a literal, and only a dot outside one is a wildcard.
//
// The two tests below pin the halves of that claim. What makes it worth pinning
// is not the marker but the consequence, which TestWirelessQualityWildcardDot
// measures rather than asserts by hand-waving.

// wirelessRow builds a data row whose three quality columns carry the given
// markers, over the trailing counters of a real row from this host.
func wirelessRow(link, level, noise string) string {
	return "wlp0s20f3: 0000   " + link + "  " + level + "  " + noise +
		"        0      0      0   1187      4        0"
}

// wirelessQuality is the pattern these fixtures are about.
const wirelessQuality = `(-?\d+)[. ]`

// wirelessColumns returns every integer the pattern extracts, in order.
func wirelessColumns(pat, row string) []int {
	matches := regexp.MustCompile(pat).FindAllStringSubmatch(row, -1)
	cols := make([]int, 0, len(matches))
	for _, m := range matches {
		v, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		cols = append(cols, v)
	}
	return cols
}

// wifiRetryIndex is where retry sits in the extracted run: status, three quality
// columns, then nwid, crypt, frag, retry, misc. The fixture puts retry at 1187 and
// its neighbour misc at 4, two orders of magnitude apart, so an off-by-one or an
// off-by-three cannot land on it by luck.
const wifiRetryIndex = 7

func TestWirelessQualityAcceptsBothMarkerKinds(t *testing.T) {
	for name, row := range map[string]string{
		"as this host prints it":       wirelessRow("70.", "-37.", "-256 "),
		"no column marked":             wirelessRow("70 ", "-37 ", "-256 "),
		"only the noise column marked": wirelessRow("70 ", "-37 ", "-256."),
		"all three columns marked":     wirelessRow("70.", "-37.", "-256."),
		"wide noise, marked":           wirelessRow("70.", "-37.", "-256000."),
		"wide noise, unmarked":         wirelessRow("70.", "-37.", "-256000 "),
	} {
		t.Run(name, func(t *testing.T) {
			cols := wirelessColumns(wirelessQuality, row)
			require.Len(t, cols, 9, "status, three quality columns, then five counters")
			assert.Equal(t, 1187, cols[wifiRetryIndex], "retry must not shift with the marker kind")
			assert.Equal(t, 4, cols[wifiRetryIndex+1], "misc must not shift with the marker kind")
		})
	}
}

// TestWirelessQualityWildcardDot measures the mutation the pattern exists to
// prevent, rather than asserting a hand-picked wrong value.
//
// Rewriting [. ] as an unescaped . does not truncate a long value -- the digit
// run is greedy, so the group still takes every digit. What it does is match
// inside the interface name: wlp0s20f3 yields three spurious columns (0, 20, 3)
// before the real ones. Every positional index after that shifts by three, so a
// reader asking for retry gets -256, the noise value, and never learns anything
// went wrong. That is the silent wrong value this pattern is here to prevent.
//
// For the record, the other variant that cannot be caught is [-?\d+][\s.]: on
// these fixtures it is functionally identical, because the kernel's only two
// markers are a dot and a space. It is listed here so nobody re-tests it expecting
// a failure.
func TestWirelessQualityWildcardDot(t *testing.T) {
	row := wirelessRow("70.", "-37.", "-256000.")

	correct := wirelessColumns(wirelessQuality, row)
	require.Len(t, correct, 9)
	assert.Equal(t, -256000, correct[3], "a six-digit noise column survives intact")
	assert.Equal(t, 1187, correct[wifiRetryIndex], "retry")

	wildcard := wirelessColumns(`(-?\d+).`, row)
	assert.Len(t, wildcard, 12,
		"an unescaped dot is a wildcard, so it also matches digits inside the interface name")
	assert.Equal(t, []int{0, 20, 3}, wildcard[:3],
		"three spurious columns from wlp0s20f3, which is what shifts every index after them")
	assert.Equal(t, -256000, wildcard[6],
		"the noise value, three positions right of where the correct pattern puts it")
	assert.Equal(t, 1187, wildcard[10], "and retry likewise shifted by three")
	assert.Equal(t, 0, wildcard[wifiRetryIndex],
		"the consequence: retry read at the index the correct pattern uses returns nwid, which "+
			"on a healthy link is 0 -- indistinguishable from a legitimately zero retry, and "+
			"nothing in the code reports a problem")

	// A row whose frag is non-zero shows the shift landing on a wrong-but-plausible
	// field instead of on zero.
	varred := "wlp0s20f3: 0000   70.  -37.  -256000.    0      0      7   1187      4        0"
	require.Equal(t, 1187, wirelessColumns(wirelessQuality, varred)[wifiRetryIndex])
	assert.Equal(t, 7, wirelessColumns(`(-?\d+).`, varred)[wifiRetryIndex+2],
		"under the wildcard the same read returns frag, not retry")
}
