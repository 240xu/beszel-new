// Package plus collects metrics that upstream beszel does not expose on this
// platform: Intel DPTF thermal zones, per-core frequency and governor state,
// every network interface, full NVMe SMART, WiFi radio details, the firmware's
// own fan speed table, and live fan state from the pfan subsystem.
//
// Fan speed is NOT a tachometer reading. This platform reports no fan RPM
// (fan_speed_rpm reads 0xFFFFFFFF and OSDL returns a constant placeholder).
// Fields named rpm_est are converted from the EC 0x76 duty byte through the
// firmware's own _FPS table and must be labelled as estimates in any UI.
package plus

import "path/filepath"

// ProdRoot is the sysfs mount point to hand to Collect in production.
const ProdRoot = "/sys"

type DPTFStats struct {
	// Celsius keyed by thermal zone type, e.g. "B0D4", "x86_pkg_temp".
	Zones map[string]float64 `json:"zones,omitempty"`
}

// CpufreqCore is one logical CPU's frequency and scaling bounds.
//
// Idx is the CPU number from sysfs's "cpu<N>" directory, NOT this record's
// position in Cores. A core whose scaling_cur_freq cannot be read is absent from
// Cores entirely, so a slice position would shift every later core forward and no
// consumer could tell which record belongs to which CPU. Consumers MUST key on
// Idx and MUST NOT key on the slice index or cache per-core series by index.
//
// A 0 in MinMHz or MaxMHz means "bound unreadable", not "no bound": no real
// scaling bound reports 0, and 0 can therefore never appear in FreqMHz. Records
// are in ascending Idx.
//
// An element is a *logical* CPU, not a physical core: SMT siblings share a turbo
// budget, so on an idle machine two siblings commonly report the same frequency.
// Eight elements is eight sysfs directories, not eight independent measurements.
type CpufreqCore struct {
	Idx     int    `json:"idx"`
	FreqMHz uint32 `json:"freq_mhz"`
	MinMHz  uint32 `json:"min_mhz"`
	MaxMHz  uint32 `json:"max_mhz"`
}

// CpufreqStats is the per-core frequency set plus the machine-wide tuning policy.
// Per-core data is keyed by CpufreqCore.Idx; see that type for the index contract.
type CpufreqStats struct {
	Cores        []CpufreqCore `json:"cores,omitempty"`
	Governor     string        `json:"governor,omitempty"`
	EPP          string        `json:"epp,omitempty"`
	NoTurbo      *int          `json:"no_turbo,omitempty"`
	HwpBoost     *int          `json:"hwp_dynamic_boost,omitempty"`
	PstateStatus string        `json:"pstate_status,omitempty"`
}

// NetIfaceStats is one interface's counters and link attributes.
//
// Rx/TxBytes are CUMULATIVE, not rates, and unlike every other field here they
// carry no omitempty: 0 means the interface is genuinely idle, not that the read
// failed, because an unreadable counter drops the interface instead. Rx/TxPackets
// DO carry omitempty and disappear at 0, so an idle interface has bytes but no
// packets -- the two fields disagree on purpose. Read the Contract paragraph of
// collectNetAll in netall.go for the reset rule consumers must apply; this type
// only owns the wire names.
type NetIfaceStats struct {
	RxBytes   uint64 `json:"rx_bytes"`
	TxBytes   uint64 `json:"tx_bytes"`
	RxPackets uint64 `json:"rx_packets,omitempty"`
	TxPackets uint64 `json:"tx_packets,omitempty"`
	OperState string `json:"oper_state,omitempty"`
	SpeedMBit int    `json:"speed_mbit,omitempty"`
	MTU       int    `json:"mtu,omitempty"`
}

type NvmeStats struct {
	Model               string  `json:"model,omitempty"`
	PercentageUsed      float64 `json:"percentage_used,omitempty"`
	UnsafeShutdowns     uint64  `json:"unsafe_shutdowns,omitempty"`
	MediaErrors         uint64  `json:"media_errors,omitempty"`
	AvailableSpare      float64 `json:"available_spare,omitempty"`
	DataUnitsRead       uint64  `json:"data_units_read,omitempty"`
	DataUnitsWritten    uint64  `json:"data_units_written,omitempty"`
	PowerOnHours        uint64  `json:"power_on_hours,omitempty"`
	PowerCycles         uint64  `json:"power_cycles,omitempty"`
	ControllerBusyMin   uint64  `json:"controller_busy_min,omitempty"`
	CriticalCompTempMin uint64  `json:"critical_comp_temp_min,omitempty"`
	TempC               float64 `json:"temp_c,omitempty"`
	TempSensor2C        float64 `json:"temp_sensor2_c,omitempty"`
}

type WiFiStats struct {
	SignalDbm int     `json:"signal_dbm,omitempty"`
	TxMBit    float64 `json:"tx_mbit,omitempty"`
	FreqMHz   int     `json:"freq_mhz,omitempty"`
	Retry     uint64  `json:"retry,omitempty"`
	Iface     string  `json:"iface,omitempty"`
}

type FanStats struct {
	// SetLevelPct is the programmed level (0 = firmware curve).
	SetLevelPct int `json:"set_level_pct,omitempty"`
	// EC76Pct is the live duty byte from EC 0x76. It tracks load and may exceed
	// SetLevelPct because the firmware curve overrides the programmed floor.
	EC76Pct int `json:"ec76_pct,omitempty"`
	// RPMEst is interpolated from EC76Pct via _FPS. NOT a tach reading.
	RPMEst uint16 `json:"rpm_est,omitempty"`
	// RPMEstFlag is always true so consumers can assert the value is labelled.
	RPMEstFlag bool    `json:"rpm_est_is_estimate"`
	TempC      float64 `json:"temp_c,omitempty"`
}

type FanSpeedStep struct {
	Percent  int    `json:"percent"`
	RPMEst   uint16 `json:"rpm_est"`
	NoiseMdb int    `json:"noise_mdb"`
	PowerMw  int    `json:"power_mw"`
}

type Stats struct {
	DPTF     *DPTFStats                `json:"dptf,omitempty"`
	Cpufreq  *CpufreqStats             `json:"cpufreq,omitempty"`
	NetAll   map[string]*NetIfaceStats `json:"netall,omitempty"`
	Nvme     *NvmeStats                `json:"nvme,omitempty"`
	WiFi     *WiFiStats                `json:"wifi,omitempty"`
	Fan      *FanStats                 `json:"fan,omitempty"`
	FanSteps []FanSpeedStep            `json:"fan_steps,omitempty"`
}

// sourcePath resolves a collector's production absolute path against root.
//
// Most inputs live under sysfs, so their collectors join a relative subpath
// onto root. These do not: the WiFi radio table is procfs, the NVMe device node
// is /dev and the fan database is /var. Their production location is absolute,
// so root serves only as an injection point for tests — any non-production root
// re-roots the path under a fixture tree, and returns it verbatim for the
// production root or an empty root. The production case has to be tested first
// because filepath.Join would otherwise turn /proc/net/wireless into
// /sys/proc/net/wireless under ProdRoot.
func sourcePath(root, prodPath string) string {
	if root == "" || root == ProdRoot {
		return prodPath
	}
	return filepath.Join(root, prodPath)
}

// Collect gathers every plus metric.
//
// root is the sysfs mount point; pass ProdRoot in production. Every collector
// receives that same root and joins its own sysfs subpaths, so tests can point
// Collect at a t.TempDir() tree. Collectors whose input lives outside sysfs
// resolve it through sourcePath instead.
func Collect(root string) (*Stats, error) {
	s := &Stats{}
	if d := collectDPTF(root); len(d) > 0 {
		s.DPTF = &DPTFStats{Zones: d}
	}
	if c := collectCpufreq(root); c != nil {
		s.Cpufreq = c
	}
	if n := collectNetAll(root); len(n) > 0 {
		s.NetAll = n
	}
	if v := collectNvme(root); v != nil {
		s.Nvme = v
	}
	if w := collectWiFi(root); w != nil {
		s.WiFi = w
	}
	if f := collectFan(root); f != nil {
		s.Fan = f
	}
	if st := collectFanSteps(root); len(st) > 0 {
		s.FanSteps = st
	}
	return s, nil
}
