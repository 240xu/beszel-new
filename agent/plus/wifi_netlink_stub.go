//go:build !linux

package plus

// collectWiFi is Linux-only: nl80211 has no portable equivalent. Upstream
// agent/wifi does carry Darwin and Windows implementations, but those report RSSI
// only, via osascript or wlanapi, so neither would supply the other three fields.
// Nil omits the "wifi" key rather than publishing zeroes, so a consumer can tell
// "no radio on this platform" from "associated at 0 Mbit/s".
func collectWiFi(root string) *WiFiStats { return nil }
