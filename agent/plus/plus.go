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

// NvmeStats is the NVMe health log as Beszelplus reports it.
//
// Every value is copied from upstream's already-parsed SMART data; this package
// never forks smartctl itself. Read the Contract paragraphs of collectNvme in
// nvme.go for why, and NvmeInput there for the shape the data arrives in.
//
// Breaking change from the pre-Task-6 draft: percentage_used and available_spare
// became percentage_used_pct and available_spare_pct (both are percentages, and
// the bare names read as ratios), and critical_comp_temp_min became
// critical_comp_time_min because the NVMe attribute is a duration in minutes, not
// a temperature. temp_sensor2_c is gone because upstream already carries that
// reading: the second sensor reaches the hub as its own system temperature
// Temperatures["nvme_sensor_2"], so a copy here would be the same number twice.
//
// Every field carries omitempty, but collectNvme is all-or-nothing, so the two
// levels still mean different things and the payload stays unambiguous. If the
// enclosing "nvme" key is ABSENT, no NVMe data was collected and nothing is known.
// If it is PRESENT, an absent field inside it means exactly zero: upstream emits
// an attribute even for a legitimately zero reading, so an attribute is missing
// only when the health log was never parsed. A consumer may therefore substitute
// a numeric zero for a missing field, but only after checking the section exists.
type NvmeStats struct {
	Model               string  `json:"model,omitempty"`
	TempC               float64 `json:"temp_c,omitempty"`
	AvailableSparePct   float64 `json:"available_spare_pct,omitempty"`
	PercentageUsedPct   float64 `json:"percentage_used_pct,omitempty"`
	DataUnitsRead       uint64  `json:"data_units_read,omitempty"`
	DataUnitsWritten    uint64  `json:"data_units_written,omitempty"`
	PowerOnHours        uint64  `json:"power_on_hours,omitempty"`
	PowerCycles         uint64  `json:"power_cycles,omitempty"`
	UnsafeShutdowns     uint64  `json:"unsafe_shutdowns,omitempty"`
	MediaErrors         uint64  `json:"media_errors,omitempty"`
	ControllerBusyMin   uint64  `json:"controller_busy_min,omitempty"`
	CriticalCompTimeMin uint64  `json:"critical_comp_time_min,omitempty"`
}

// WiFiStats is a stateless snapshot of one associated station interface, taken
// over nl80211.
//
// An absent "wifi" key means no station was associated, or the host has no
// cfg80211 radio. Inside a present key an absent field means UNKNOWN, never
// exactly zero: a value is assigned only once the read that proves it has
// succeeded, and omitempty drops the zero left behind on the paths that returned
// early. Three tiers, and only the first is a fact about the machine:
//
//   - iface is assigned from the interface that reported itself associated, and a
//     nameless interface is skipped, so a present iface proves association.
//   - freq_mhz comes from that same BSS; absent means it carried no frequency
//     attribute, not a link sitting on channel 0.
//   - signal_dbm, tx_mbit and retry are assigned only after the GET_STATION query
//     returned, so an absent one is either a denied or empty query -- the normal
//     outcome in a default container -- or a driver that reported nothing for that
//     attribute. The wire cannot tell the two apart, so a consumer MUST render an
//     absent field as unknown and MUST NOT substitute 0.
//
// signal_dbm adds one more consequence: only readings in [-150, 0) are accepted,
// so the Go value can never be 0 and omitempty would drop it if it were. 0 dBm is
// unrepresentable, and an absent signal_dbm always means no usable RSSI arrived.
//
// Contract: Retry is CUMULATIVE, not a rate, and is the driver's TransmitFailed
// counter. It rises monotonically while the association holds and resets to zero
// when the interface reassociates, so consumers must difference consecutive
// samples and MUST guard the subtraction: an unguarded uint64 cur-prev across a
// reassociation wraps to about 1.8e19. Reuse the rule netall.go documents for
// Rx/TxBytes -- cur < prev means a reset.
//
// TxMBit is the instantaneous negotiated transmit rate, not throughput and not
// link capacity: rate control renegotiates it continuously, so the same link
// reports 866, 1040 and 1733 Mbit/s within a minute. Read it beside SignalDbm
// rather than alone; level low with the rate still high is distance or
// contention, level healthy with the rate collapsed is a transmit fault.
type WiFiStats struct {
	SignalDbm int     `json:"signal_dbm,omitempty"`
	TxMBit    float64 `json:"tx_mbit,omitempty"`
	FreqMHz   int     `json:"freq_mhz,omitempty"`
	// Retry is the wire doc for the cumulative contract above, kept on the field
	// because that is the line a UI author reads: the name carries no _total, so a
	// consumer that trusts it as a rate graphs a line that only ever rises.
	Retry uint64 `json:"retry,omitempty"`
	Iface string `json:"iface,omitempty"`
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

// FanSpeedStep is one row of the firmware's own _FPS duty curve, read straight from
// sysfs rather than inferred from the EC duty byte. RPMEst is the nominal speed the
// firmware claims for that duty, never a tach reading: a real one lives in fan_speed_rpm,
// from the optional _FST object, which reads 0xFFFFFFFF when the platform has no sensor.
// So unlike FanStats, this struct carries no rpm_est_is_estimate marker. That flag exists
// because FanStats.rpm_est is omitempty, so a consumer must be able to tell an absent
// reading from a labelled one; rpm_est here is always present, always non-zero unless the
// firmware says so, and always named for what it is, so a constant-true flag would carry
// no information a consumer could branch on. NoiseMdb and PowerMw are nominal multiples
// of Percent (see parseFpsLine), not acoustic or electrical measurements.
//
// Percent is not guaranteed unique: _FPS has an independent trip_point_index column, so
// two states may share a duty. UI must key rows by index, never by percent alone.
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
// onto root. These do not: the WiFi radio table is procfs and the fan database
// is /var. Their production location is absolute, so root serves only as an
// injection point for tests — any non-production root re-roots the path under a
// fixture tree, and returns it verbatim for the production root or an empty
// root. The production case has to be tested first because filepath.Join would
// otherwise turn /proc/net/wireless into /sys/proc/net/wireless under ProdRoot.
// collectNvme is absent because it has no path of its own to resolve at all.
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
//
// nvme is upstream's already-parsed SMART data, or nil when none has been
// collected. It is a parameter rather than something this package reads because
// upstream owns the smartctl invocation: pass nil and the NVMe section is simply
// absent, which is also what every fixture test does.
func Collect(root string, nvme *NvmeInput) (*Stats, error) {
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
	if v := collectNvme(nvme); v != nil {
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
