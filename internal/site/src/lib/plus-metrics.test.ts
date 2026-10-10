import { expect, test } from "bun:test"
import {
	cumulativeDelta,
	cpufreqRows,
	dptfRows,
	FAN_ESTIMATE_NOTE,
	FAN_POWER_IS_NOMINAL,
	FAN_STALE_AFTER_S,
	FAN_STALE_NOTE,
	FAN_STEP_RPM_IS_ESTIMATE,
	fanRows,
	fanStepRows,
	fmtBytes,
	fmtCount,
	fmtCelsius,
	fmtPct,
	fmtText,
	latestPlus,
	netRows,
	nvmeRows,
	plusAvailable,
	UNKNOWN,
	wifiRows,
	wifiSignal,
} from "./plus-metrics"
import type { PlusStats, SystemStatsRecord } from "@/types"

const at = (plus?: PlusStats, created = 1): SystemStatsRecord[] => [
	({ system: "s", stats: plus ? { plus } : {}, created }) as SystemStatsRecord,
]

const value = (rows: { key: string; value: string }[], key: string) => rows.find((row) => row.key === key)?.value
const notes = (rows: { key: string; notes?: string[] }[], key: string) => rows.find((row) => row.key === key)?.notes ?? []
const coreKeys = (rows: { key: string }[]) => rows.filter((row) => /^\d+$/.test(row.key)).map((row) => row.key)

const FULL: PlusStats = {
	dptf: { zones: { ACPI: 47.5, BXU: 41 } },
	cpufreq: {
		cores: [
			{ idx: 0, freq_mhz: 2294, min_mhz: 800, max_mhz: 4900 },
			{ idx: 4, freq_mhz: 2294, min_mhz: 800, max_mhz: 4900 },
		],
		governor: "powersave",
		epp: "balance_performance",
		no_turbo: 0,
		hwp_dynamic_boost: 1,
		pstate_status: "active",
	},
	netall: { eno1: { rx_bytes: 9007199254740991, tx_bytes: 0, oper_state: "up", speed_mbit: 1000, mtu: 1500 } },
	nvme: {
		model: "CFQV8-1",
		temp_c: 41,
		available_spare_pct: 100,
		percentage_used_pct: 3,
		data_units_read: 12345,
		data_units_written: 6789,
		power_on_hours: 1000,
		power_cycles: 42,
		unsafe_shutdowns: 18,
		media_errors: 0,
		controller_busy_min: 7,
		critical_comp_time_min: 0,
	},
	wifi: { signal_dbm: -47, tx_mbit: 433, freq_mhz: 5180, retry: 1146, iface: "wlp0s20f3" },
	fan: {
		set_level_pct: 0,
		ec76_pct: 30,
		rpm_est: 1180,
		rpm_est_is_estimate: true,
		gov: "curve",
		temp_c: 44,
		sample_ts_s: 1759500000,
	},
	fan_steps: [{ percent: 65, rpm_est: 2450, noise_mdb: 32, power_mw: 4100 }],
}

test("a reading the agent never reported renders as unknown, never as zero", () => {
	expect(fmtCount(undefined)).toBe(UNKNOWN)
	expect(fmtCount(null)).toBe(UNKNOWN)
	expect(fmtCount(Number.NaN)).toBe(UNKNOWN)
	expect(fmtPct(undefined)).toBe(UNKNOWN)
	expect(fmtCelsius(undefined)).toBe(UNKNOWN)
	expect(fmtBytes(undefined)).toBe(UNKNOWN)
	expect(fmtText(undefined)).toBe(UNKNOWN)
	expect(fmtText("")).toBe(UNKNOWN)
})

test("a real zero is a reading and is rendered as one", () => {
	expect(fmtCount(0)).toBe("0")
	expect(fmtPct(0)).toBe("0%")
	expect(fmtBytes(0)).toBe("0 B")
	expect(fmtText("curve")).toBe("curve")
	expect(fmtCelsius(41)).toBe("41.0 °C")
})

test("the newest record carrying a plus snapshot is the one shown", () => {
	const records = [...at(undefined), ...at(FULL), ...at({ nvme: { model: "later" } }, 2)]
	expect(latestPlus(records)?.nvme?.model).toBe("later")
	expect(latestPlus(at(undefined))).toBeUndefined()
	expect(plusAvailable(records)).toBe(true)
	expect(plusAvailable(at(undefined))).toBe(false)
	expect(plusAvailable([])).toBe(false)
})

test("nvme percentage used reads the field the agent writes and never invents a zero", () => {
	const rows = nvmeRows(at(FULL))
	expect(value(rows, "nvme.percentage_used_pct")).toBe("3%")
	expect(value(rows, "nvme.available_spare_pct")).toBe("100%")
	expect(value(rows, "nvme.critical_comp_time_min")).toBe("0")
	expect(value(rows, "nvme.media_errors")).toBe("0")
	expect(value(rows, "nvme.unsafe_shutdowns")).toBe("18")
	expect(value(rows, "nvme.model")).toBe("CFQV8-1")
})

test("an nvme field dropped by omitempty reads as unknown rather than 0%", () => {
	const rows = nvmeRows(at({ nvme: { model: "CFQV8-1", percentage_used_pct: 0, unsafe_shutdowns: 18 } }))
	// percentage_used_pct: 0 arrived on the wire, so it stays a reading.
	expect(value(rows, "nvme.percentage_used_pct")).toBe("0%")
	// available_spare_pct and critical_comp_time_min never arrived.
	expect(value(rows, "nvme.available_spare_pct")).toBe(UNKNOWN)
	expect(value(rows, "nvme.critical_comp_time_min")).toBe(UNKNOWN)
	expect(nvmeRows(at(undefined))).toEqual([])
	expect(nvmeRows(at({}))).toEqual([])
})

test("nvme data units are labelled cumulative because no trustworthy sample interval exists", () => {
	const rows = nvmeRows(at(FULL))
	expect(rows.find((row) => row.key === "nvme.data_units_read")?.label).toBe("Data Read (cumulative)")
	expect(rows.find((row) => row.key === "nvme.data_units_written")?.label).toBe("Data Written (cumulative)")
	expect(rows.some((row) => /\/h|per h|rate/i.test(row.label))).toBe(false)
})

test("a dptf zone that was never collected produces no row and no reading", () => {
	const rows = dptfRows(at(FULL))
	expect(rows.map((row) => row.key)).toEqual(["ACPI", "BXU"])
	expect(value(rows, "ACPI")).toBe("47.5 °C")
	expect(rows.some((row) => row.value === "0.0 °C")).toBe(false)
	expect(dptfRows(at({ dptf: { zones: {} } }))).toEqual([])
	expect(dptfRows(at(undefined))).toEqual([])
	expect(dptfRows(at({}))).toEqual([])
})

test("cpu cores are keyed and labelled by their cpu number, not by position", () => {
	const rows = cpufreqRows(at(FULL))
	// idx 0 and 4 present, 1..3 absent: positions shift, so a position key would
	// name the wrong cpu.
	expect(coreKeys(rows)).toEqual(["0", "4"])
	expect(rows[1].label).toBe("CPU4")
	expect(value(rows, "4")).toBe("2294 MHz")
})

test("a dropped cpu core leaves a gap instead of shifting the rest", () => {
	const rows = cpufreqRows(at({ cpufreq: { cores: [{ idx: 14, freq_mhz: 2300, min_mhz: 800, max_mhz: 4900 }] } }))
	expect(coreKeys(rows)).toEqual(["14"])
	expect(rows[0].label).toBe("CPU14")
})

test("cpufreq policy fields that were not read read as unknown", () => {
	const rows = cpufreqRows(at({ cpufreq: { cores: [] } }))
	expect(value(rows, "cpufreq.governor")).toBe(UNKNOWN)
	expect(value(rows, "cpufreq.epp")).toBe(UNKNOWN)
	expect(value(rows, "cpufreq.pstate_status")).toBe(UNKNOWN)
	expect(cpufreqRows(at(undefined))).toEqual([])
})

test("a cpufreq toggle reported as 0 stays a reading", () => {
	const rows = cpufreqRows(at({ cpufreq: { cores: [], no_turbo: 0, hwp_dynamic_boost: 0 } }))
	expect(value(rows, "cpufreq.no_turbo")).toBe("0")
	expect(value(rows, "cpufreq.hwp_dynamic_boost")).toBe("0")
})

test("retry differences the last two samples that carry it", () => {
	const rows = wifiRows([...at({ wifi: { retry: 1146 } }, 1), ...at({ wifi: { retry: 1200 } }, 2)])
	expect(value(rows, "wifi.retry_delta")).toBe("54")
	expect(cumulativeDelta(1146, 1200)).toBe(54)
})

test("retry differences the newest pair rather than the oldest pair", () => {
	const records = [
		...at({ wifi: { retry: 10 } }, 1),
		...at({ wifi: { retry: 40 } }, 2),
		...at({ wifi: { retry: 95 } }, 3),
	]
	expect(value(wifiRows(records), "wifi.retry_delta")).toBe("55")
})

test("retry is never shown as the raw counter value", () => {
	const rows = wifiRows([...at({ wifi: { retry: 1146 } }, 1), ...at({ wifi: { retry: 1200 } }, 2)])
	const retry = rows.find((row) => row.key === "wifi.retry_delta")
	expect(retry?.label).toBe("Retry since previous sample")
	expect(retry?.value).toBe("54")
})

test("one sample cannot produce a retry delta so the row reads unknown", () => {
	expect(value(wifiRows(at({ wifi: { retry: 1146 } })), "wifi.retry_delta")).toBe(UNKNOWN)
	expect(wifiRows([])).toEqual([])
	expect(wifiRows(at(undefined))).toEqual([])
})

test("a counter that went backwards is a reset or a wrap so the delta is unknown", () => {
	expect(cumulativeDelta(1200, 1146)).toBeUndefined()
	expect(cumulativeDelta(undefined, 1200)).toBeUndefined()
	expect(cumulativeDelta(1146, undefined)).toBeUndefined()
	expect(cumulativeDelta(0, 0)).toBe(0)
})

test("a 0 dBm signal is not a reading", () => {
	expect(wifiSignal({ signal_dbm: 0 })).toBeUndefined()
	expect(wifiSignal({})).toBeUndefined()
	expect(wifiSignal(undefined)).toBeUndefined()
	expect(wifiSignal({ signal_dbm: -47 })).toBe(-47)
	expect(value(wifiRows(at({ wifi: { signal_dbm: 0 } })), "wifi.signal_dbm")).toBe(UNKNOWN)
})

test("a virtual interface has no meaningful link rate so none is shown", () => {
	const rows = netRows(at(FULL))
	expect(value(rows, "netall.eno1")).toBe("↓ 8.0 PiB ↑ 0 B")
	expect(rows.some((row) => /mbit/i.test(row.value))).toBe(false)
	expect(netRows(at(undefined))).toEqual([])
	expect(netRows(at({ netall: {} }))).toEqual([])
})

test("rpm is labelled an estimate because the platform has no tachometer", () => {
	const rows = fanRows(at(FULL), FULL.fan!.sample_ts_s)
	expect(value(rows, "fan.rpm_est")).toBe("1180 rpm")
	expect(notes(rows, "fan.rpm_est")).toContain(FAN_ESTIMATE_NOTE)
	expect(FAN_STEP_RPM_IS_ESTIMATE).toBe(true)
})

test("the estimate label follows the flag the agent sends", () => {
	const rows = fanRows(at({ fan: { ...FULL.fan, rpm_est_is_estimate: false } }), FULL.fan!.sample_ts_s)
	expect(notes(rows, "fan.rpm_est")).not.toContain(FAN_ESTIMATE_NOTE)
})

test("a fan reading older than the staleness window is marked stale", () => {
	const rows = fanRows(at(FULL), FULL.fan!.sample_ts_s + FAN_STALE_AFTER_S + 1)
	expect(notes(rows, "fan.rpm_est")).toContain(FAN_STALE_NOTE)
})

test("a fan reading inside the staleness window is not marked stale", () => {
	const rows = fanRows(at(FULL), FULL.fan!.sample_ts_s + FAN_STALE_AFTER_S)
	expect(notes(rows, "fan.rpm_est")).not.toContain(FAN_STALE_NOTE)
})

test("a fan snapshot from the future is not stale", () => {
	const rows = fanRows(at(FULL), FULL.fan!.sample_ts_s - 10_000)
	expect(notes(rows, "fan.rpm_est")).not.toContain(FAN_STALE_NOTE)
})

test("an unreadable fan level is unknown while a programmed level of 0 is a reading", () => {
	const absent = fanRows(at({ fan: { rpm_est_is_estimate: true, sample_ts_s: 1 } }), 1)
	expect(value(absent, "fan.set_level_pct")).toBe(UNKNOWN)
	expect(value(absent, "fan.ec76_pct")).toBe(UNKNOWN)
	const programmed = fanRows(at(FULL), 1)
	expect(value(programmed, "fan.set_level_pct")).toBe("0%")
	expect(value(programmed, "fan.ec76_pct")).toBe("30%")
	expect(fanRows(at(undefined), 1)).toEqual([])
	expect(fanRows(at({}), 1)).toEqual([])
})

test("fan steps are keyed by position because the firmware table repeats duties", () => {
	const rows = fanStepRows(
		at({
			fan_steps: [
				{ percent: 25, rpm_est: 900, noise_mdb: 20, power_mw: 300 },
				{ percent: 50, rpm_est: 1800, noise_mdb: 28, power_mw: 800 },
				{ percent: 50, rpm_est: 1810, noise_mdb: 29, power_mw: 810 },
			],
		})
	)
	expect(rows.map((row) => row.key)).toEqual(["0", "1", "2"])
	expect(new Set(rows.map((row) => row.key)).size).toBe(3)
	expect(fanStepRows(at(undefined))).toEqual([])
	expect(fanStepRows(at({ fan_steps: [] }))).toEqual([])
})

test("the firmware noise column is not presented as a measurement", () => {
	const rows = fanStepRows(at(FULL))
	expect(Object.keys(rows[0]).some((key) => /noise/i.test(key))).toBe(false)
	expect(rows[0].rpm).toBe("2450")
	expect(FAN_POWER_IS_NOMINAL).toBe(true)
})