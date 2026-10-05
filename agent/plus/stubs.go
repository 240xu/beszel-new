package plus

// Every collector receives the same sysfs root and joins its own subpaths, so
// tests can point Collect at a fixture tree. The inputs the next tasks read
// are therefore:
//
//	collectDPTF      <root>/class/thermal/thermal_zone*
//	collectCpufreq   <root>/devices/system/cpu/cpu[0-9]*/cpufreq
//	collectNetAll    <root>/class/net/<if>/statistics/*
//	collectFanSteps  <root>/bus/acpi/devices/INT3404:00/state*
//	collectWiFi      sourcePath(root, "/proc/net/wireless")
//	collectNvme      sourcePath(root, "/dev/nvme0")
//	collectFan       sourcePath(root, "/var/lib/pfanel/pfanel.db")
//
// The last three sit outside sysfs and have to go through sourcePath rather
// than a plain join; the first four are sysfs-relative.

func collectCpufreq(root string) *CpufreqStats            { return nil }
func collectNetAll(root string) map[string]*NetIfaceStats { return nil }
func collectNvme(root string) *NvmeStats                  { return nil }
func collectWiFi(root string) *WiFiStats                  { return nil }
func collectFan(root string) *FanStats                    { return nil }
func collectFanSteps(root string) []FanSpeedStep          { return nil }
