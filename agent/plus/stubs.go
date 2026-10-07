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
//
// collectWiFi sits outside the paths its siblings join, so it goes through sourcePath
// rather than a plain join; the first three are sysfs-relative.
//
// collectFan is no longer in this list because it no longer reads a file. pfanel's
// history database is opened by package agent/pfanel and arrives as a FanSample,
// because the only sqlite driver in this build graph reaches os/exec through
// modernc.org/libc, which the fork policy test forbids in this package's closure.
// See FanSample's doc comment for the full reasoning.
// collectNvme is gone from this list because it reads neither: it is handed
// upstream's parsed SMART data, so it has no path of its own (see nvme.go).
//
// collectWiFi is no longer stubbed here because it is not platform-neutral any
// more. Its Linux implementation lives in wifi_netlink_linux.go and the stub for
// every other platform in wifi_netlink_stub.go, so that package agent/plus can
// hold build-tagged files without leaking a netlink dependency to platforms that
// cannot compile it.
