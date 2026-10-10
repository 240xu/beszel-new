import type { FanStats, PlusStats, SystemStatsRecord, WiFiStats } from "@/types"

/** Text for a reading the agent did not report. Every formatter funnels through
 *  `reported()` so no call site can quietly substitute a zero. */
export const UNKNOWN = "—"

export const FAN_ESTIMATE_NOTE = "estimated"
export const FAN_STALE_NOTE = "stale"
/** The firmware derives power and noise from the duty byte by fixed constants, so
 *  neither is an independent measurement. */
export const FAN_POWER_IS_NOMINAL = true
export const FAN_STEP_RPM_IS_ESTIMATE = true

/** Ages are compared against the reader's own clock, because the collector never
 *  checks one. Sized as a few hub update intervals, which are configurable, so
 *  this is a policy and not a measurement. */
export const FAN_STALE_AFTER_S = 300

const BYTE_UNITS = ["B", "KiB", "MiB", "GiB", "TiB", "PiB"]

/** The single gate between "not reported" and a number. Nothing else in this
 *  module is allowed to invent a value. */
function reported(value: number | undefined | null): number | null {
	return typeof value === "number" && Number.isFinite(value) ? value : null
}

function withUnit(text: string, unit: string): string {
	return text === UNKNOWN ? UNKNOWN : `${text} ${unit}`
}

export function fmtCount(value: number | undefined | null): string {
	const count = reported(value)
	return count === null ? UNKNOWN : `${count}`
}

export function fmtPct(value: number | undefined | null): string {
	const pct = reported(value)
	return pct === null ? UNKNOWN : `${pct}%`
}

export function fmtCelsius(value: number | undefined | null): string {
	const celsius = reported(value)
	return celsius === null ? UNKNOWN : `${celsius.toFixed(1)} °C`
}

export function fmtText(value: string | undefined | null): string {
	return value ? value : UNKNOWN
}

export function fmtBytes(value: number | undefined | null): string {
	const bytes = reported(value)
	if (bytes === null) return UNKNOWN
	const magnitude = Math.abs(bytes) === 0 ? 1 : Math.abs(bytes)
	const exponent = Math.min(Math.floor(Math.log2(magnitude) / 10), BYTE_UNITS.length - 1)
	return exponent === 0 ? `${bytes} B` : `${(bytes / 1024 ** exponent).toFixed(1)} ${BYTE_UNITS[exponent]}`
}

export interface MetricRow {
	key: string
	label: string
	value: string
	notes?: string[]
}

export interface FanStepRow {
	key: string
	duty: string
	rpm: string
	power: string
}

export function latestPlus(records: SystemStatsRecord[]): PlusStats | undefined {
	for (let i = records.length - 1; i >= 0; i--) {
		const plus = records[i]?.stats?.plus
		if (plus) return plus
	}
	return undefined
}

export function plusAvailable(records: SystemStatsRecord[]): boolean {
	return latestPlus(records) !== undefined
}

export function nvmeRows(records: SystemStatsRecord[]): MetricRow[] {
	const nvme = latestPlus(records)?.nvme
	if (!nvme) return []
	return [
		{ key: "nvme.model", label: "Model", value: fmtText(nvme.model) },
		{ key: "nvme.temp_c", label: "Temperature", value: fmtCelsius(nvme.temp_c) },
		{ key: "nvme.percentage_used_pct", label: "Percentage Used", value: fmtPct(nvme.percentage_used_pct) },
		{ key: "nvme.available_spare_pct", label: "Available Spare", value: fmtPct(nvme.available_spare_pct) },
		{ key: "nvme.unsafe_shutdowns", label: "Unsafe Shutdowns", value: fmtCount(nvme.unsafe_shutdowns) },
		{ key: "nvme.media_errors", label: "Media Errors", value: fmtCount(nvme.media_errors) },
		{ key: "nvme.power_on_hours", label: "Power On Hours", value: fmtCount(nvme.power_on_hours) },
		{ key: "nvme.power_cycles", label: "Power Cycles", value: fmtCount(nvme.power_cycles) },
		{ key: "nvme.controller_busy_min", label: "Controller Busy (min)", value: fmtCount(nvme.controller_busy_min) },
		{
			key: "nvme.critical_comp_time_min",
			label: "Critical Comp Time (min)",
			value: fmtCount(nvme.critical_comp_time_min),
		},
		{ key: "nvme.data_units_read", label: "Data Read (cumulative)", value: fmtCount(nvme.data_units_read) },
		{ key: "nvme.data_units_written", label: "Data Written (cumulative)", value: fmtCount(nvme.data_units_written) },
	]
}

export function dptfRows(records: SystemStatsRecord[]): MetricRow[] {
	const zones = latestPlus(records)?.dptf?.zones
	if (!zones) return []
	return Object.entries(zones)
		.sort(([a], [b]) => a.localeCompare(b))
		.map(([name, celsius]) => ({ key: name, label: name, value: fmtCelsius(celsius) }))
}

/** Keyed by the cpu number from sysfs. A core whose frequency is unreadable is
 *  absent from the array, so the position is not its identity. */
export function cpufreqRows(records: SystemStatsRecord[]): MetricRow[] {
	const cpufreq = latestPlus(records)?.cpufreq
	if (!cpufreq) return []
	const coreRows: MetricRow[] = [...(cpufreq.cores ?? [])]
		.sort((a, b) => a.idx - b.idx)
		.map((core) => ({
			key: String(core.idx),
			label: `CPU${core.idx}`,
			value: withUnit(fmtCount(core.freq_mhz), "MHz"),
		}))
	return coreRows.concat([
		{ key: "cpufreq.governor", label: "Governor", value: fmtText(cpufreq.governor) },
		{ key: "cpufreq.epp", label: "EPP", value: fmtText(cpufreq.epp) },
		{ key: "cpufreq.pstate_status", label: "P-State", value: fmtText(cpufreq.pstate_status) },
		{ key: "cpufreq.no_turbo", label: "No Turbo", value: fmtCount(cpufreq.no_turbo) },
		{ key: "cpufreq.hwp_dynamic_boost", label: "HWP Dynamic Boost", value: fmtCount(cpufreq.hwp_dynamic_boost) },
	])
}

/** Link rate is deliberately absent: on this host the interesting entries are
 *  virtual interfaces whose reported speed means nothing. */
export function netRows(records: SystemStatsRecord[]): MetricRow[] {
	const netall = latestPlus(records)?.netall
	if (!netall) return []
	return Object.entries(netall)
		.sort(([a], [b]) => a.localeCompare(b))
		.map(([name, iface]) => ({
			key: `netall.${name}`,
			label: name,
			value: `↓ ${fmtBytes(iface?.rx_bytes)} ↑ ${fmtBytes(iface?.tx_bytes)}`,
		}))
}

/** A difference between two unsigned cumulative counters. A decrease means a
 *  reset or a wrap, and the traffic across the window is then not recoverable. */
export function cumulativeDelta(previous: number | undefined, current: number | undefined): number | undefined {
	const before = reported(previous)
	const after = reported(current)
	if (before === null || after === null) return undefined
	if (after < before) return undefined
	return after - before
}

export function wifiRetryDelta(records: SystemStatsRecord[]): number | undefined {
	const samples: number[] = []
	for (const record of records) {
		const retry = reported(record?.stats?.plus?.wifi?.retry)
		if (retry !== null) samples.push(retry)
	}
	if (samples.length < 2) return undefined
	return cumulativeDelta(samples[samples.length - 2], samples[samples.length - 1])
}

export function wifiSignal(wifi?: WiFiStats): number | undefined {
	const signal = reported(wifi?.signal_dbm)
	// The collector discards an RSSI outside its accepted window, so a 0 that
	// reaches a consumer is a missing reading rather than a 0 dBm link.
	return signal === null || signal === 0 ? undefined : signal
}

export function wifiRows(records: SystemStatsRecord[]): MetricRow[] {
	const wifi = latestPlus(records)?.wifi
	if (!wifi) return []
	return [
		{ key: "wifi.iface", label: "Interface", value: fmtText(wifi.iface) },
		{ key: "wifi.signal_dbm", label: "Signal", value: withUnit(fmtCount(wifiSignal(wifi)), "dBm") },
		{ key: "wifi.freq_mhz", label: "Frequency", value: withUnit(fmtCount(wifi.freq_mhz), "MHz") },
		{ key: "wifi.tx_mbit", label: "Transmit Rate", value: withUnit(fmtCount(wifi.tx_mbit), "Mbit/s") },
		{ key: "wifi.retry_delta", label: "Retry since previous sample", value: fmtCount(wifiRetryDelta(records)) },
	]
}

export function fanIsStale(fan: FanStats, nowSeconds: number): boolean {
	const sampledAt = reported(fan.sample_ts_s)
	if (sampledAt === null) return false
	return nowSeconds - sampledAt > FAN_STALE_AFTER_S
}

export function fanRows(records: SystemStatsRecord[], nowSeconds: number): MetricRow[] {
	const fan = latestPlus(records)?.fan
	if (!fan) return []
	const stale = fanIsStale(fan, nowSeconds)
	const speed = reported(fan.rpm_est)
	const marks = (estimate: boolean) => {
		const collected: string[] = []
		if (estimate) collected.push(FAN_ESTIMATE_NOTE)
		if (stale) collected.push(FAN_STALE_NOTE)
		return collected.length > 0 ? collected : undefined
	}
	return [
		{
			key: "fan.rpm_est",
			label: "Speed",
			value: withUnit(fmtCount(speed), "rpm"),
			notes: marks(speed !== null && fan.rpm_est_is_estimate),
		},
		{ key: "fan.set_level_pct", label: "Programmed Level", value: fmtPct(fan.set_level_pct) },
		{ key: "fan.ec76_pct", label: "Applied Duty", value: fmtPct(fan.ec76_pct) },
		{ key: "fan.temp_c", label: "Temperature", value: fmtCelsius(fan.temp_c) },
		{ key: "fan.gov", label: "Mode", value: fmtText(fan.gov) },
	]
}

/** Keyed by position because the firmware table repeats duties, so the duty
 *  column cannot identify a step. Noise is not carried: it is the duty scaled by
 *  a fixed constant and adds nothing the duty does not already say. */
export function fanStepRows(records: SystemStatsRecord[]): FanStepRow[] {
	const steps = latestPlus(records)?.fan_steps
	if (!steps) return []
	return steps.map((step, index) => ({
		key: String(index),
		duty: fmtPct(step.percent),
		rpm: fmtCount(step.rpm_est),
		power: withUnit(fmtCount(step.power_mw), "mW"),
	}))
}
