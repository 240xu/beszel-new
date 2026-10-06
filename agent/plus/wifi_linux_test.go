//go:build testing && linux

package plus

// nl80211 mapping tests, Linux-only.
//
// The split is not cosmetic. wifi_netlink_linux.go declares collectWiFiWith,
// hasWirelessDevice and nl80211Client behind //go:build linux, so a test file
// with no OS constraint that calls them compiles and passes here and fails to
// build on darwin, windows and freebsd. That is exactly the defect the first cut
// of this file had, and `GOOS=windows go vet -tags testing ./agent/plus/` is what
// found it. platform_scope_test.go keeps the split honest from now on.

import (
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"net"
	"os"
	"path/filepath"
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
	// bssErrWithData models a driver that answers with a usable BSS *and* an
	// error, which is the case where ignoring the error would change the result.
	bssErrWithData error
	// stationMAC records what each Station call was asked for, so a test can
	// prove the associated BSSID is the one queried.
	stationMAC []net.HardwareAddr
}

func (f *fakeWiFiClient) Interfaces() ([]*native.Interface, error) {
	return f.ifaces, f.ifacesErr
}

func (f *fakeWiFiClient) BSS(ifi *native.Interface) (*native.BSS, error) {
	f.bssAsked = append(f.bssAsked, ifi.Name)
	if f.bssErr != nil {
		return nil, f.bssErr
	}
	if bss := f.bss[ifi.Name]; bss != nil && f.bssErrWithData != nil {
		return bss, f.bssErrWithData
	}
	return f.bss[ifi.Name], nil
}

func (f *fakeWiFiClient) Station(ifi *native.Interface, mac net.HardwareAddr) (*native.StationInfo, error) {
	f.stationMAC = append(f.stationMAC, mac)
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

	// The Go zeros above are only half the contract. What reaches the consumer is
	// this payload, and the rule WiFiStats documents is that three absent keys
	// mean "not read" rather than three zeroes, so the two have to be asserted
	// together: a change that started publishing the zero would leave every other
	// assertion in this file green.
	b, err := json.Marshal(got)
	require.NoError(t, err)
	assert.JSONEq(t, `{"freq_mhz":5180,"iface":"wlp0s20f3"}`, string(b),
		"an unread station query must drop all three keys rather than send zero for them")
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

// TestCollectWiFiSetsTheDeadline bounds the exchange against a literal rather
// than against wifiTimeout, so raising the constant cannot raise the bar with it.
func TestCollectWiFiSetsTheDeadline(t *testing.T) {
	client := associated()
	before := time.Now()
	collectWiFiWith(client)
	assert.True(t, client.deadline.After(before),
		"the exchange must carry a deadline")
	assert.True(t, client.deadline.Before(time.Now().Add(30*time.Second)),
		"and it must be a short one: a wedged netlink query would otherwise stall "+
			"the whole collection pass, got %v", client.deadline.Sub(before))
	assert.True(t, client.deadline.After(time.Now().Add(-time.Minute)),
		"and not already expired")
}

// TestWifiFamilyLookupRunsUnderADeadline is a source-order check on
// newNL80211Client, and it is source-order because nothing else can reach it: the
// function talks to a concrete *genetlink.Conn, so there is no seam to fake and a
// behavioural test would have to wedge the kernel to observe anything.
//
// The order is the whole claim. Collect calls newNL80211Client before
// collectWiFiWith sets its deadline, so a CTRL round trip issued in between is the
// one query in this collector with no bound, and a wedged one stalls the whole
// collection pass -- which is the outcome wifiTimeout exists to prevent.
func TestWifiFamilyLookupRunsUnderADeadline(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "wifi_netlink_linux.go", nil, parser.SkipObjectResolution)
	require.NoError(t, err)

	var setDeadline, getFamily token.Pos
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "newNL80211Client" {
			continue
		}
		ast.Inspect(fn, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			switch sel.Sel.Name {
			case "SetDeadline":
				setDeadline = call.Pos()
			case "GetFamily":
				getFamily = call.Pos()
			}
			return true
		})
	}
	require.NotEqual(t, token.NoPos, getFamily, "no family lookup found -- the assertion proved nothing")
	require.NotEqual(t, token.NoPos, setDeadline, "no deadline is set at all")
	assert.Less(t, setDeadline, getFamily,
		"the deadline must be pinned on the socket before the CTRL round trip, not after it")
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

// TestCollectWiFiSkipsABSSReportedAlongsideAnError covers the driver that
// answers with usable data and an error at once. Ignoring the error would let a
// stale or unassociated BSS through, so the error has to win.
func TestCollectWiFiSkipsABSSReportedAlongsideAnError(t *testing.T) {
	client := associated()
	client.bssErrWithData = errors.New("netlink: interrupted")
	assert.Nil(t, collectWiFiWith(client),
		"a BSS that arrives with an error is not evidence of association")
}

// TestCollectWiFiQueriesTheAssociatedBSSID pins which MAC the station query is
// addressed to: the BSSID of the BSS that reported itself associated.
func TestCollectWiFiQueriesTheAssociatedBSSID(t *testing.T) {
	client := associated()
	collectWiFiWith(client)
	require.Len(t, client.stationMAC, 1)
	assert.Equal(t, testBSSID, client.stationMAC[0],
		"GET_STATION must address the associated BSSID")
}

func TestHasWirelessDeviceIgnoresMissingDir(t *testing.T) {
	assert.False(t, hasWirelessDevice(t.TempDir()+"/absent"))
}

func TestNL80211ClientImplementsWifiClient(t *testing.T) {
	var _ wifiClient = (*nl80211Client)(nil)
}
