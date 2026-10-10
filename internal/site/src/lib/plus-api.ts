import type { FanAuditTrail, FanCurve, FanMode, FanModeResult, FanState, PlusStats } from "@/types"
import { readFanRefusal } from "@/lib/plus-refusals"

/** What a fan write reports back. `applied` means "in force now", never
 *  "accepted": a curve reaches the disk before any daemon has read it, so a body
 *  carrying only `ok` would let a panel render "saved" over a curve the machine
 *  is not running. The union makes that unbypassable — the pending arm cannot be
 *  written with `applied: true`, and its `next_action` is required, so a pending
 *  write always has an outlet and never an "applied" label to fall back on.
 *  Both fan write routes answer with this. */
export const FAN_SAVE_STATUS = {
	applied: "applied",
	pendingRestart: "pending_restart",
} as const

export const FAN_NEXT_ACTION = {
	restartPfand: "restart_pfand",
} as const

export type FanSaveStatus = (typeof FAN_SAVE_STATUS)[keyof typeof FAN_SAVE_STATUS]
export type FanNextAction = (typeof FAN_NEXT_ACTION)[keyof typeof FAN_NEXT_ACTION]

export type CurveSaveResult =
	| {
			applied: true
			needs_pfand_restart: false
			status: typeof FAN_SAVE_STATUS.applied
			next_action?: never
			level: number
			regress_at?: string
	  }
	| {
			applied: false
			needs_pfand_restart: true
			status: typeof FAN_SAVE_STATUS.pendingRestart
			next_action: typeof FAN_NEXT_ACTION.restartPfand
			level: number
			regress_at?: string
	  }

/** The gate a panel branches on: the write is on disk and nothing is running
 *  it. Show the restart outlet here, never render it as saved. */
export function isPendingRestart(result: CurveSaveResult): boolean {
	return result.status === FAN_SAVE_STATUS.pendingRestart
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
	const res = await fetch(path, {
		...init,
		headers: { "Content-Type": "application/json", ...(init?.headers ?? {}) },
	})
	const text = await res.text()
	if (!res.ok) {
		throw readFanRefusal(res.status, text)
	}
	if (!text) return undefined as T
	return JSON.parse(text) as T
}

/** The duty, the daemon's state and the curve arrive together, so an editor
 *  cannot be built against a curve read before the write it is about to make. */
export function getFanState(): Promise<FanState> {
	return request<FanState>("/api/plus/fan/state")
}

export function getFanAudit(): Promise<FanAuditTrail> {
	return request<FanAuditTrail>("/api/plus/fan/audit")
}

/** `confirm` is required and has no default: a fan write is a physical
 *  instruction, and the server refuses an unconfirmed one. */
export function setFanLevel(level: number, confirm: boolean): Promise<CurveSaveResult> {
	return request<CurveSaveResult>("/api/plus/fan/level", {
		method: "POST",
		body: JSON.stringify({ level, confirm }),
	})
}

/** Starting the daemon is the only way a curve that reached the disk becomes
 *  behaviour, which is why it is the outlet a pending write needs. */
export function setFanMode(mode: FanMode, confirm: boolean): Promise<FanModeResult> {
	return request<FanModeResult>("/api/plus/fan/mode", {
		method: "POST",
		body: JSON.stringify({ mode, confirm }),
	})
}

/** Every curve field is sent, the emergency edge included. The server rejects
 *  unknown fields and refuses a curve with any field missing, so a partial
 *  payload is a refusal rather than a partial write. */
export function buildCurvePayload(curve: FanCurve, confirm: boolean): FanCurve & { confirm: boolean } {
	return { ...curve, confirm }
}

export function saveCurve(curve: FanCurve, confirm: boolean): Promise<CurveSaveResult> {
	return request<CurveSaveResult>("/api/plus/fan/curve", {
		method: "POST",
		body: JSON.stringify(buildCurvePayload(curve, confirm)),
	})
}

const PLUS_STATS_PATH = "/api/collections/plus_stats/records"
const WINDOW_FILTER = /^[A-Za-z0-9_-]{1,10}$/

/** The window goes into a PocketBase filter as a quoted literal, and URL
 *  encoding does not escape a quote, so the value is whitelisted, not encoded. */
export async function getPlusStats(window: string): Promise<PlusStats[]> {
	if (!WINDOW_FILTER.test(window)) {
		throw new Error("window is not a valid record filter value")
	}
	const res = await request<{ items?: PlusStats[] }>(
		`${PLUS_STATS_PATH}?perPage=200&filter=${encodeURIComponent(`(window='${window}')`)}&sort=-created`
	)
	return res.items ?? []
}
