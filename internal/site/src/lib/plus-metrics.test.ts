import { expect, test } from "bun:test"
import {
	cumulativeDelta,
	cpufreqRows,
	dptfRows,
	FAN_POWER_IS_NOMINAL,
	FAN_STALE_AFTER_S,
	fanRows,
	fanStepRows,
	fmtBytes,
	fmtCount,
	fmtCelsius,
	fmtPct,
	fmtText,
	formatSpan,
	latestPlus,
	netRows,
	nvmeRows,
	plusAvailable,
	previewRows,
	STATIC_LABEL_KEYS,
	UNKNOWN,
	wifiRows,
	wifiSignal,
} from "./plus-metrics"
import type { FanStats, PlusStats, SystemStatsRecord } from "@/types"

const at = (plus?: PlusStats, created = 1): SystemStatsRecord[] => [
	{ system: "s", stats: plus ? { plus } : {}, created } as SystemStatsRecord,
]

const value = (rows: { key: string; value: string }[], key: string) => rows.find((row) => row.key === key)?.value
const notes = (rows: { key: string; notes?: { kind: string }[] }[], key: string) =>
	(rows.find((row) => row.key === key)?.notes ?? []).map((note) => note.kind)
const noteOf = (rows: { key: string; notes?: unknown[] }[], key: string) =>
	rows.find((row) => row.key === key)?.notes?.[0]
const coreKeys = (rows: { key: string }[]) => rows.filter((row) => /^\d+$/.test(row.key)).map((row) => row.key)

const FAN: FanStats = {
	set_level_pct: 0,
	ec76_pct: 30,
	rpm_est: 1180,
	rpm_est_is_estimate: true,
	gov: "curve",
	temp_c: 44,
	sample_ts_s: 1759500000,
}

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
	fan: FAN,
	fan_steps: [{ percent: 65, rpm_est: 2450, noise_mdb: 32, power_mw: 4100 }],
}

// --- the rendered strings, pinned as literals ---------------------------------
//
// Everything below this block asserts against these literals rather than against
// the constants they came from. Comparing a constant to itself is true whatever
// it is set to, so every such assertion was previously vacuous: renaming
// UNKNOWN to "0" turned every unreported field into a confident zero and left
// the suite green. These are the only places the strings are written out, and
// the rest of the file is free to go on comparing against the constants.

test("the unreadable placeholder is the em dash, not a zero and not a blank", () => {
	expect(UNKNOWN).toBe("—")
	expect(UNKNOWN).not.toBe("0")
	expect(UNKNOWN.trim()).not.toBe("")
})

test("the staleness window is 300 seconds", () => {
	expect(FAN_STALE_AFTER_S).toBe(300)
	expect(FAN_STALE_AFTER_S).toBeGreaterThan(0)
})

test("the firmware nominal flag is on, so the sheet says so", () => {
	expect(FAN_POWER_IS_NOMINAL).toBe(true)
})

// --- absence is never a number, whatever the placeholder is spelled ----------

test("no formatter turns an unreported reading into anything numeric", () => {
	const formatters = [fmtCount, fmtPct, fmtCelsius, fmtBytes]
	for (const format of formatters) {
		for (const absent of [undefined, null, Number.NaN]) {
			const rendered = format(absent)
			expect(rendered).toBe(UNKNOWN)
			// The invariant, independent of the glyph: an absent reading must not
			// carry a digit, a sign or a decimal point.
			expect(rendered).not.toMatch(/[\d]/)
			expect(rendered).not.toMatch(/[-+]/)
			expect(rendered).not.toContain(".")
		}
	}
	expect(fmtText(undefined)).toBe(UNKNOWN)
	expect(fmtText("")).toBe(UNKNOWN)
	expect(fmtText(undefined)).not.toMatch(/[\d]/)
})

test("an unreported reading is distinguishable from a reported zero", () => {
	// Stated as behaviour rather than as a string: this holds for any placeholder
	// that is not itself the digit zero, so it survives a change of glyph.
	expect(fmtCount(undefined)).not.toBe(fmtCount(0))
	expect(fmtPct(undefined)).not.toBe(fmtPct(0))
	expect(fmtBytes(undefined)).not.toBe(fmtBytes(0))
	expect(fmtCelsius(undefined)).not.toBe(fmtCelsius(0))
	expect(fmtText(undefined)).not.toBe(fmtText("0"))
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

test("no fan speed is offered for display, because this platform cannot measure one", () => {
	// `rpm_est` is interpolated from the duty byte by estRPM() on the agent; the
	// host reports no tachometer. A reader cannot tell that from a measured speed,
	// so the presentation layer does not show it at all. The wire field is
	// untouched -- the value below is still on the record being rendered.
	const rows = fanRows(at(FULL), FAN.sample_ts_s)
	expect(rows.map((row) => row.key)).not.toContain("fan.rpm_est")
	expect(rows.map((row) => row.value)).not.toContain("1180 rpm")
	expect(rows.some((row) => /rpm/i.test(row.label))).toBe(false)
	expect(Object.keys(rows[0] ?? {})).not.toContain("rpm")
})

test("an estimate is never presented as a measurement, and never labelled either", () => {
	// Not "estimated" beside a number, which still puts a speed on screen: no row
	// at all. This holds whether or not the agent flagged the value.
	for (const flag of [true, false]) {
		const rows = fanRows(at({ fan: { rpm_est: 1180, rpm_est_is_estimate: flag, sample_ts_s: 1 } }), 1)
		expect(rows.some((row) => row.key.includes("rpm"))).toBe(false)
		expect(rows.every((row) => !(row.notes ?? []).includes({ kind: "estimated" } as never))).toBe(true)
	}
})

test("a fan reading older than the staleness window is marked stale", () => {
	const rows = fanRows(at(FULL), FAN.sample_ts_s + FAN_STALE_AFTER_S + 1)
	expect(notes(rows, "fan.set_level_pct")).toContain("stale")
})

test("a fan reading inside the staleness window is not marked stale", () => {
	const rows = fanRows(at(FULL), FAN.sample_ts_s + FAN_STALE_AFTER_S)
	expect(notes(rows, "fan.set_level_pct")).not.toContain("stale")
})

test("a fan snapshot from the future is not stale", () => {
	const rows = fanRows(at(FULL), FAN.sample_ts_s - 10_000)
	expect(notes(rows, "fan.set_level_pct")).not.toContain("stale")
})

test("a qualifier is never attached to a reading that was not reported", () => {
	// The stale mark used to hang off the collector's clock alone, so an absent
	// value rendered as "— (stale)": stale about a number nobody published.
	const old = FAN.sample_ts_s + FAN_STALE_AFTER_S + 1
	const absent = fanRows(at({ fan: { rpm_est_is_estimate: true, sample_ts_s: FAN.sample_ts_s } }), old)
	expect(value(absent, "fan.set_level_pct")).toBe(UNKNOWN)
	expect(notes(absent, "fan.set_level_pct")).toEqual([])
	// Same instant, but a reported level keeps the mark.
	const present = fanRows(at(FULL), old)
	expect(notes(present, "fan.set_level_pct")).toEqual(["stale"])
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

test("neither firmware-derived column is presented as a measurement", () => {
	// noise is the duty scaled by a constant and rpm_est is interpolated from the
	// duty byte; both would restate the duty the row beside them already carries.
	const rows = fanStepRows(at(FULL))
	expect(Object.keys(rows[0]).some((key) => /noise/i.test(key))).toBe(false)
	expect(Object.keys(rows[0])).not.toContain("rpm")
	expect(rows[0].power).toBe("4100 mW")
})

test("the firmware fan table keeps no speed column at all", () => {
	// Same rule as the fan card, and the reverse direction: a column added back
	// for a platform that does have a tachometer would need its own evidence.
	const columns = Object.keys(fanStepRows(at(FULL))[0] ?? {})
	expect(columns.sort()).toEqual(["duty", "key", "power"])
})

// --- the retry row names the interval it covers ------------------------------

test("a retry difference says how long it spans", () => {
	const rows = wifiRows([...at({ wifi: { retry: 10 } }, 0), ...at({ wifi: { retry: 64 } }, 3600_000)])
	expect(value(rows, "wifi.retry_delta")).toBe("54")
	expect(noteOf(rows, "wifi.retry_delta")).toEqual({ kind: "span", seconds: 3600 })
})

test("the same difference over a short window is labelled with the short window", () => {
	const rows = wifiRows([...at({ wifi: { retry: 10 } }, 0), ...at({ wifi: { retry: 64 } }, 12_000)])
	expect(value(rows, "wifi.retry_delta")).toBe("54")
	expect(noteOf(rows, "wifi.retry_delta")).toEqual({ kind: "span", seconds: 12 })
})

test("a span is not claimed when no difference exists to qualify", () => {
	const single = wifiRows(at({ wifi: { retry: 1146 } }))
	expect(value(single, "wifi.retry_delta")).toBe(UNKNOWN)
	expect(notes(single, "wifi.retry_delta")).toEqual([])
	const backwards = wifiRows([...at({ wifi: { retry: 2000 } }, 0), ...at({ wifi: { retry: 1000 } }, 5000)])
	expect(value(backwards, "wifi.retry_delta")).toBe(UNKNOWN)
	expect(notes(backwards, "wifi.retry_delta")).toEqual([])
})

test("two records sharing a timestamp span no interval, so no span is claimed", () => {
	const rows = wifiRows([...at({ wifi: { retry: 10 } }, 1000), ...at({ wifi: { retry: 64 } }, 1000)])
	expect(value(rows, "wifi.retry_delta")).toBe("54")
	expect(notes(rows, "wifi.retry_delta")).toEqual([])
})

test("a created value that cannot be parsed leaves the span unknown", () => {
	const records = [
		{ system: "s", stats: { plus: { wifi: { retry: 10 } } }, created: "not-a-time" } as SystemStatsRecord,
		{ system: "s", stats: { plus: { wifi: { retry: 64 } } }, created: 3600_000 } as SystemStatsRecord,
	]
	expect(value(wifiRows(records), "wifi.retry_delta")).toBe("54")
	expect(notes(wifiRows(records), "wifi.retry_delta")).toEqual([])
})

test("spans read in the largest unit that keeps them whole", () => {
	expect(formatSpan(12)).toBe("12s")
	expect(formatSpan(59)).toBe("59s")
	expect(formatSpan(60)).toBe("1m")
	expect(formatSpan(300)).toBe("5m")
	expect(formatSpan(3540)).toBe("59m")
	expect(formatSpan(3600)).toBe("1h")
	expect(formatSpan(7200)).toBe("2h")
	expect(formatSpan(86_400)).toBe("24h")
})

test("the note carries the seconds, so one formatter serves every span", () => {
	// The "over ..." wording moved to plus-sheet.tsx, which is where it can be
	// translated; what the module still owns is the number behind it. If a second
	// formatter reappeared here, the sheet would have two sources for one span.
	expect(formatSpan(3600)).toBe("1h")
	expect(
		noteOf(wifiRows([...at({ wifi: { retry: 10 } }, 0), ...at({ wifi: { retry: 64 } }, 3600_000)]), "wifi.retry_delta")
	).toEqual({ kind: "span", seconds: 3600 })
})

// --- the collapsed card never renders empty ----------------------------------

test("the preview carries the fan and the zones when the snapshot has both", () => {
	const rows = previewRows(at(FULL), FAN.sample_ts_s)
	expect(rows.map((row) => row.key)).toEqual([
		"fan.set_level_pct",
		"fan.ec76_pct",
		"fan.temp_c",
		"fan.gov",
		"ACPI",
		"BXU",
	])
})

test("a snapshot with no fan and no zones previews another section instead of nothing", () => {
	const rows = previewRows(at({ nvme: { model: "CFQV8-1" } }), 1)
	expect(rows.length).toBeGreaterThan(0)
	expect(rows[0].key).toBe("nvme.model")
	expect(rows[0].value).toBe("CFQV8-1")
})

test("a snapshot carrying only the firmware table still previews something", () => {
	const rows = previewRows(at({ fan_steps: [{ percent: 30, rpm_est: 3000, noise_mdb: 20, power_mw: 400 }] }), 1)
	expect(rows.length).toBeGreaterThan(0)
	expect(value(rows, "fan_steps.count")).toBe("1")
})

test("no snapshot and an empty one both preview nothing", () => {
	expect(previewRows(at(undefined), 1)).toEqual([])
	expect(previewRows([], 1)).toEqual([])
	expect(previewRows(at({}), 1)).toEqual([])
	expect(previewRows(at({ dptf: { zones: {} } }), 1)).toEqual([])
})

// --- every static row key is one the sheet can translate --------------------

test("every row a snapshot emits has a key the sheet can either translate or name", () => {
	// The sheet holds `LABELS: Record<StaticLabelKey, string>` and falls back to
	// `row.label` for anything else. A new fixed-term row that skips this list
	// would still render, in English, in every locale — so the emitted keys are
	// checked against the list here rather than left to review.
	const emitted = [
		...nvmeRows(at(FULL)),
		...cpufreqRows(at(FULL)),
		...wifiRows(at(FULL)),
		...fanRows(at(FULL), FAN.sample_ts_s),
	]
	const staticKeys = new Set<string>(STATIC_LABEL_KEYS)
	const deviceKeyed = (key: string) => !key.includes(".")
	for (const row of emitted) {
		if (staticKeys.has(row.key) || deviceKeyed(row.key)) continue
		expect(`${row.key} is neither a static label key nor device-named`).toBe("static label key")
	}
	// The reverse direction: nothing listed is dead weight.
	const emittedKeys = new Set(emitted.map((row) => row.key))
	for (const key of STATIC_LABEL_KEYS) {
		expect(emittedKeys.has(key)).toBe(true)
	}
})

test("the static label keys are unique", () => {
	expect(new Set(STATIC_LABEL_KEYS).size).toBe(STATIC_LABEL_KEYS.length)
})
