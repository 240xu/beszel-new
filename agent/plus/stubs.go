package plus

// Every collector receives the same sysfs root and joins its own subpaths, so
// tests can point Collect at a fixture tree. The inputs the next tasks read
// are therefore:
//
//	collectDPTF      <root>/class/thermal/thermal_zone*
//	collectCpufreq   <root>/devices/system/cpu/cpu[0-9]*/cpufreq
//	collectNetAll    <root>/class/net/<if>/statistics/*
//	collectFanSteps  <root>/bus/acpi/devices/INT3404:00/state*
//	collectWiFi      sourcePath(root, "/sys/class/ieee80211") -- gate only
//	collectFan       sourcePath(root, "/var/lib/pfanel/pfanel.db")
//
// The last two sit outside the paths their siblings join, so they go through
// sourcePath rather than a plain join; the first three are sysfs-relative.
// collectNvme is gone from this list because it reads neither: it is handed
// upstream's parsed SMART data, so it has no path of its own (see nvme.go).
//
// collectWiFi is no longer stubbed here because it is not platform-neutral any
// more. Its Linux implementation lives in wifi_netlink_linux.go and the stub for
// every other platform in wifi_netlink_stub.go, so that package agent/plus can
// hold build-tagged files without leaking a netlink dependency to platforms that
// cannot compile it.

func collectFan(root string) *FanStats           { return nil }
func collectFanSteps(root string) []FanSpeedStep { return nil }
