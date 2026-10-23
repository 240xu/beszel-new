//go:build linux

// WiFi radio details over nl80211.
//
// agent/plus cannot import upstream agent/wifi: agent/wifi -> internal/entities/
// system -> agent/plus is an import cycle, and system.WiFi carries only SSID and
// Signal, so three of the four fields below would be dropped even with the cycle
// broken. Adapting agent/wifi/wifi_linux.go instead leaves every upstream file
// untouched, so the rebase surface stays zero. Nothing forks and nothing under
// /proc is parsed, which keeps the package fork-free and drops the iw dependency.

package plus

import (
	"errors"
	"net"
	"os"
	"time"

	"github.com/mdlayher/genetlink"
	"github.com/mdlayher/netlink"
	native "github.com/mdlayher/wifi"
	"golang.org/x/sys/unix"
)

const (
	// wifiTimeout bounds the dumps and the station request, where a driver
	// callback decides how long the kernel takes. Family lookups do not block.
	wifiTimeout = 2 * time.Second
	// bitsPerMBit divides TransmitBitrate, which is in bits/second.
	bitsPerMBit = 1e6
	// wifiWiphyDir gates netlink access: looking up the nl80211 family while
	// cfg80211 is unloaded makes the kernel run modprobe, which loads the module.
	wifiWiphyDir = "/sys/class/ieee80211"
	// signalFloorDbM filters the zero a driver leaves when it cannot report RSSI.
	signalFloorDbM = -150
)

// wifiClient is the slice of nl80211 this collector uses. Narrow on purpose: it
// exists so the field mapping can be tested against fixtures with no radio.
type wifiClient interface {
	Interfaces() ([]*native.Interface, error)
	BSS(*native.Interface) (*native.BSS, error)
	Station(*native.Interface, net.HardwareAddr) (*native.StationInfo, error)
	SetDeadline(time.Time) error
	Close() error
}

// nl80211Client adds a targeted GET_STATION, as upstream agent/wifi does:
// mdlayher/wifi only dumps the list, which some full-MAC drivers answer empty.
type nl80211Client struct {
	*native.Client
	conn   *genetlink.Conn
	family genetlink.Family
}

func newNL80211Client() (*nl80211Client, error) {
	client, err := native.New()
	if err != nil {
		return nil, err
	}
	conn, err := genetlink.Dial(nil)
	if err != nil {
		client.Close()
		return nil, err
	}
	// Deadline first: collectWiFiWith cannot pin it until this returns, so a CTRL
	// round trip issued earlier would be the one query here with no bound. The
	// order is held by TestWifiFamilyLookupRunsUnderADeadline.
	if err := conn.SetDeadline(time.Now().Add(wifiTimeout)); err != nil {
		conn.Close()
		client.Close()
		return nil, err
	}
	family, err := conn.GetFamily(unix.NL80211_GENL_NAME)
	if err != nil {
		conn.Close()
		client.Close()
		return nil, err
	}
	return &nl80211Client{Client: client, conn: conn, family: family}, nil
}

func (c *nl80211Client) Station(ifi *native.Interface, mac net.HardwareAddr) (*native.StationInfo, error) {
	ae := netlink.NewAttributeEncoder()
	ae.Uint32(unix.NL80211_ATTR_IFINDEX, uint32(ifi.Index))
	ae.Bytes(unix.NL80211_ATTR_MAC, mac)
	data, err := ae.Encode()
	if err != nil {
		return nil, err
	}
	msgs, err := c.conn.Execute(genetlink.Message{
		Header: genetlink.Header{Command: unix.NL80211_CMD_GET_STATION, Version: c.family.Version},
		Data:   data,
	}, c.family.ID, netlink.Request)
	if err != nil {
		return nil, err
	}
	if len(msgs) == 0 {
		return nil, errors.New("nl80211: no station info")
	}
	return native.ParseStationInfo(msgs[0].Data)
}

func (c *nl80211Client) SetDeadline(t time.Time) error {
	return errors.Join(c.Client.SetDeadline(t), c.conn.SetDeadline(t))
}

func (c *nl80211Client) Close() error {
	return errors.Join(c.conn.Close(), c.Client.Close())
}

// hasWirelessDevice is not cached, so a radio registered late is still picked up.
func hasWirelessDevice(dir string) bool {
	f, err := os.Open(dir)
	if err != nil {
		return false
	}
	defer f.Close()
	names, _ := f.Readdirnames(1)
	return len(names) > 0
}

// collectWiFi reports the first associated station interface: Stats.WiFi is a
// single object, so the wire contract cannot carry a second radio.
func collectWiFi(root string) *WiFiStats {
	if !hasWirelessDevice(sourcePath(root, wifiWiphyDir)) {
		return nil
	}
	client, err := newNL80211Client()
	if err != nil {
		return nil
	}
	defer client.Close()
	return collectWiFiWith(client)
}

// collectWiFiWith maps one associated station onto the wire shape. Three traps
// here are all silent-wrong-value traps: TransmitBitrate is bits/second while
// RateInfo.Bitrate is 100 kbit/s units; retry is TransmitFailed, not the
// neighbouring TransmitRetries; BSS.Signal is the scan cache in mBm.
func collectWiFiWith(client wifiClient) *WiFiStats {
	if client.SetDeadline(time.Now().Add(wifiTimeout)) != nil {
		return nil
	}
	interfaces, err := client.Interfaces()
	if err != nil {
		return nil
	}
	for _, ifi := range interfaces {
		if ifi == nil || ifi.Type != native.InterfaceTypeStation || ifi.Name == "" {
			continue
		}
		bss, err := client.BSS(ifi)
		if err != nil || bss == nil || bss.Status != native.BSSStatusAssociated {
			continue
		}
		freq := bss.Frequency
		stats := &WiFiStats{Iface: ifi.Name, FreqMHz: &freq}
		if len(bss.BSSID) == 0 {
			return stats
		}
		station, err := client.Station(ifi, bss.BSSID)
		if err != nil || station == nil {
			// Association is real even where station statistics are not readable,
			// the usual outcome in a default container, so keep what the BSS proved.
			return stats
		}
		if station.Signal >= signalFloorDbM && station.Signal < 0 {
			signal := station.Signal
			stats.SignalDbm = &signal
		}
		tx := float64(station.TransmitBitrate) / bitsPerMBit
		stats.TxMBit = &tx
		// Never negative in practice; the guard stops a malformed reply wrapping
		// into an enormous uint64.
		if station.TransmitFailed >= 0 {
			retry := uint64(station.TransmitFailed)
			stats.Retry = &retry
		}
		return stats
	}
	return nil
}
