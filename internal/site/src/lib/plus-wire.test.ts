/**
 * Compile-time wire contract.
 *
 * Every `@ts-expect-error` below fails the type check if the line stops being an
 * error, so widening a field to optional, or loosening a union back to string,
 * turns this file red rather than quietly reopening the hole. Nothing here is
 * executed: it is exported and never called.
 */
import { expect, test } from "bun:test"
import type {
	CpufreqCore,
	FanAuditEntry,
	FanCurve,
	FanModeResult,
	FanState,
	FanSpeedStep,
	FanStats,
	PlusStats,
} from "@/types"
import { FAN_NEXT_ACTION, FAN_SAVE_STATUS, saveCurve, setFanLevel, setFanMode } from "@/lib/plus-api"
import type { CurveSaveResult } from "@/lib/plus-api"
import type { FanRejectCode } from "@/lib/plus-refusals"

const REAL: PlusStats = {
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
	wifi: { signal_dbm: -47, tx_mbit: 433, freq_mhz: 5180, retry: 2, iface: "wlan0" },
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

const FULL_CURVE: FanCurve = {
	idle: 52,
	l0: 30,
	t1: 65,
	l1: 40,
	t2: 72,
	l2: 60,
	t3: 78,
	l3: 80,
	t4: 84,
	l4: 100,
	t5: 88,
	l5: 100,
	emerg: 95,
	e: 40,
	emerg_rel: 8,
	cap: 100,
	trim: 0,
	gain: 100,
	hyst: 2,
	dwell: 5,
	dead: 3,
	iv: 10,
}

const APPLIED: CurveSaveResult = {
	applied: true,
	needs_pfand_restart: false,
	status: "applied",
	level: 60,
}

const PENDING: CurveSaveResult = {
	applied: false,
	needs_pfand_restart: true,
	status: "pending_restart",
	next_action: "restart_pfand",
	level: 0,
}

export function typeAssertions(): unknown[] {
	// @ts-expect-error a pending write cannot claim to be applied
	const appliedWhilePending: CurveSaveResult = { ...PENDING, applied: true }
	// @ts-expect-error a pending write always names its outlet
	const pendingWithoutOutlet: CurveSaveResult = { ...PENDING, next_action: undefined }
	// @ts-expect-error an applied write has no outlet to take
	const appliedWithOutlet: CurveSaveResult = { ...APPLIED, next_action: "restart_pfand" }
	// @ts-expect-error the two statuses are the only ones
	const inventedStatus: CurveSaveResult = { ...APPLIED, status: "rejected" }
	// @ts-expect-error applied is never optional, so a reader cannot get undefined
	const appliedMissing: CurveSaveResult = { needs_pfand_restart: false, status: "applied", level: 0 }
	// @ts-expect-error a call with no confirm must not be writable
	const levelWithoutConfirm: () => Promise<CurveSaveResult> = () => setFanLevel(70)
	// @ts-expect-error nor must a mode change
	const modeWithoutConfirm: () => Promise<FanModeResult> = () => setFanMode("auto")
	// @ts-expect-error nor a curve save
	const curveWithoutConfirm: () => Promise<CurveSaveResult> = () => saveCurve(FULL_CURVE)
	// @ts-expect-error sample_ts_s is written on every sample
	const fanNoTimestamp: FanStats = { rpm_est_is_estimate: true }
	// @ts-expect-error rpm_est_is_estimate is written on every sample
	const fanNoFlag: FanStats = { sample_ts_s: 1 }
	// @ts-expect-error every step field is written
	const stepMissingNoise: FanSpeedStep = { percent: 0, rpm_est: 0, power_mw: 0 }
	// @ts-expect-error every core field is written
	const coreMissingMax: CpufreqCore = { idx: 0, freq_mhz: 800, min_mhz: 800 }
	// @ts-expect-error a curve missing the emergency edge must not typecheck
	const curveNoEmergent: FanCurve = { ...FULL_CURVE, emerg: undefined }
	// @ts-expect-error whether a deadline is on record is always answered
	const stateNoDeadline: FanState = {
		pfand_active: true,
		level_read: true,
		level: 0,
		pkg_temp_read: true,
		pkg_temp_c: 40,
		curve: null,
	}
	// @ts-expect-error an identifier this build has no wording for is not a code
	const unclassified: FanRejectCode = "thermal_profile_t4"
	// @ts-expect-error refusals name an existing fact, not an arbitrary string
	const looseCode: FanRejectCode = "something_new"
	const audit: FanAuditEntry = {
		op: "write_curve",
		outcome: "applied",
		level: 0,
		at: "2026-01-01T00:00:00Z",
		actor: "admin",
		code: "curve_rejected",
	}
	return [
		appliedWhilePending,
		pendingWithoutOutlet,
		appliedWithOutlet,
		inventedStatus,
		appliedMissing,
		levelWithoutConfirm,
		modeWithoutConfirm,
		curveWithoutConfirm,
		fanNoTimestamp,
		fanNoFlag,
		stepMissingNoise,
		coreMissingMax,
		curveNoEmergent,
		stateNoDeadline,
		unclassified,
		looseCode,
		audit,
	]
}

test("the refusal identifiers are the classified ones and nothing else", () => {
	expect(FAN_SAVE_STATUS).toEqual({ applied: "applied", pendingRestart: "pending_restart" })
	expect(FAN_NEXT_ACTION).toEqual({ restartPfand: "restart_pfand" })
})

test("both write shapes are reachable and neither is the other", () => {
	expect(APPLIED.applied).toBe(true)
	expect(APPLIED.needs_pfand_restart).toBe(false)
	expect(PENDING.applied).toBe(false)
	expect(PENDING.next_action).toBe("restart_pfand")
	expect(REAL.fan?.sample_ts_s).toBe(1759500000)
	expect(REAL.fan_steps?.[0]?.noise_mdb).toBe(32)
	expect(Object.keys(FULL_CURVE)).toHaveLength(22)
})
