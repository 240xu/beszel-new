import type { FanCurve, FanMode, FanModeResult, FanState } from "@/types"
import { FAN_SAVE_STATUS, type CurveSaveResult, saveCurve, setFanLevel, setFanMode } from "@/lib/plus-api"
import { FanApiError, type FanRejectCode } from "@/lib/plus-refusals"
import { UNKNOWN } from "@/lib/plus-metrics"

export type { CurveSaveResult }

/** The mode route has no curve to put in force, so it answers in another shape —
 *  but it does answer: a write that returns nothing can never be stated. */
export type FanWriteResult = CurveSaveResult | FanModeResult

/** Mirrors `validLevels` in agent/plus/levels.go; zero is the curve handover.
 *  `plus-fan-duties.drift.test.ts` reads that Go file and fails on any drift. */
export const FAN_DUTIES = [0, 25, 30, 40, 50, 60, 70, 80, 85, 90, 95, 100] as const

/** The server publishes the label for a duty it read, so the panel never rebuilds
 *  one: a re-tuned firmware table must not be contradicted from the number. */
export function fanLevelLabel(state: FanState): string {
	return state.level_read && state.level_label ? state.level_label : UNKNOWN
}

export type Confirmed = true

/** A fan write is a physical instruction and the server refuses an unconfirmed
 *  one. `Confirmed` is the literal type, so that refusal cannot be assembled. */
export function confirmed(): Confirmed {
	return true
}

export function levelWrite(level: number): { level: number; confirm: Confirmed } {
	return { level, confirm: confirmed() }
}

export function modeWrite(mode: FanMode): { mode: FanMode; confirm: Confirmed } {
	return { mode, confirm: confirmed() }
}

export function curveWrite(curve: FanCurve): FanCurve & { confirm: Confirmed } {
	return { ...curve, confirm: confirmed() }
}

export type FanWrite =
	| { kind: "level"; level: number }
	| { kind: "mode"; mode: FanMode }
	| { kind: "curve"; curve: FanCurve }

/** The three writes this panel can make, each carrying the confirm flag. */
export async function issueFanWrite(write: FanWrite): Promise<FanWriteResult> {
	if (write.kind === "level") {
		const w = levelWrite(write.level)
		return setFanLevel(w.level, w.confirm)
	}
	if (write.kind === "mode") {
		const w = modeWrite(write.mode)
		return await setFanMode(w.mode, w.confirm)
	}
	const w = curveWrite(write.curve)
	return saveCurve(w, w.confirm)
}

export type FanOutcomeKind = "applied" | "pending_restart" | "daemon_running" | "daemon_stopped"

/** Wording that has to mean one thing. The pending arm borrows none of the applied
 *  arm's: calling a curve with no daemon in front of it in force is the opposite
 *  of the truth about the fan. The daemon arms report what the server says runs. */
const OUTCOME_TEXT: Record<FanOutcomeKind, string> = {
	applied: "In force — the fan is running this now.",
	pending_restart: "Written to disk; the curve daemon has not picked it up.",
	daemon_running: "The curve daemon is running and is driving the fan from the curve on disk.",
	daemon_stopped: "The curve daemon is not running, so it is not driving the fan.",
}

const DAEMON_ARMS = {
	running: { kind: "daemon_running", inForce: true },
	stopped: { kind: "daemon_stopped", inForce: false },
} as const satisfies Record<string, { kind: FanOutcomeKind; inForce: boolean }>

export interface FanWriteOutcome {
	kind: FanOutcomeKind
	/** The English source text. `plus-fan-tab.tsx` translates all four keys. */
	text: string
	inForce: boolean
	needsRestart: boolean
	nextAction: "restart_pfand" | null
	/** Null for a daemon change: zero would read as the curve handover. */
	level: number | null
}

/** Branches on `status`, the wire discriminator, so the arm a write lands in is the
 *  one the server named. The pending arm must never be called saved. */
export function fanWriteOutcome(result: FanWriteResult): FanWriteOutcome {
	if (!("status" in result)) {
		const arm = DAEMON_ARMS[result.pfand_active ? "running" : "stopped"]
		return {
			kind: arm.kind,
			text: OUTCOME_TEXT[arm.kind],
			inForce: arm.inForce,
			needsRestart: false,
			nextAction: null,
			level: null,
		}
	}
	if (result.status === FAN_SAVE_STATUS.pendingRestart) {
		return {
			kind: "pending_restart",
			text: OUTCOME_TEXT.pending_restart,
			inForce: false,
			needsRestart: true,
			nextAction: "restart_pfand",
			level: result.level,
		}
	}
	return {
		kind: "applied",
		text: OUTCOME_TEXT.applied,
		inForce: true,
		needsRestart: false,
		nextAction: null,
		level: result.level,
	}
}

export type FanRefusalKey = FanRejectCode | "unclassified" | "unrecognised"

/** The English source text, and the only place the meaning is written down. */
const REFUSAL_TEXT: Record<FanRefusalKey, string> = {
	rollback_failed: "The previous duty could not be restored: the fan is holding one you did not ask for.",
	write_unconfirmed: "The fan is not holding the duty that was requested; the old one was put back.",
	write_failed: "The fan control device refused the write.",
	confirm_required: "The change was not confirmed, so the fan was left alone.",
	level_not_allowed: "That duty is not one this firmware accepts.",
	curve_rejected: "The curve failed validation and was rejected before anything was written.",
	pfand_active: "The curve daemon owns the fan; stop it before setting a duty by hand.",
	package_too_hot:
		"The package is at or above the emergency edge in the curve, so no duty was written. Let it cool, or raise emerg in the curve, before writing again.",
	temperature_unknown:
		"The package temperature could not be read, so no duty was written and the reading cannot be trusted. Check the sensor and the zone it names rather than cooling the machine.",
	breaker_open: "Fan control is latched open after repeated failures; nothing was written.",
	unsupported: "Fan control is only implemented on Linux.",
	internal: "The write did not complete.",
	// Neither fallback may assert anything about the fan: a server that names no
	// reason may have refused before it read the request or failed after it acted.
	unclassified: "The server refused this without naming a reason, so nothing can be concluded about the fan.",
	unrecognised: "The server refused this for a reason this build does not know.",
}

export interface FanRefusalView {
	key: FanRefusalKey
	text: string
	status: number
	detail?: string
}

export type FanRefusalError = Pick<FanApiError, "code" | "rawCode" | "status">

/** Reads `code` and never `status`. An identifier this build does not know is not
 *  filed under the status it shares with a fault of another kind: a dead sensor
 *  and a hot package are both a 503, and cooling a machine with a broken sensor
 *  sends the operator after the wrong problem. */
export function fanRefusal(error: FanRefusalError): FanRefusalView {
	const key: FanRefusalKey = error.code ?? (error.rawCode === undefined ? "unclassified" : "unrecognised")
	return {
		key,
		text: REFUSAL_TEXT[key],
		status: error.status,
		detail: error.rawCode === error.code ? undefined : error.rawCode,
	}
}

/** How a write ended, and what the panel may still be standing on. `dropBanner`
 *  is true when the banner from the last write describes the action that has just
 *  failed or gone unanswered, so it must go: "Awaiting restart" printed above "the
 *  restart failed" states the operator's next step twice, once of them known
 *  broken. A duty or curve refusal says nothing about the write before it, which
 *  is still in force. */
export type FanWriteSettled =
	| { ok: true; outcome: FanWriteOutcome }
	| { ok: false; offline: boolean; refusal: FanRefusalView | null; dropBanner: boolean }

/** There is no `if (result)` for a caller to write: a write that could return
 *  nothing is a write whose banner can be silently suppressed. */
export function fanWriteSettled(result: FanWriteResult): FanWriteSettled {
	return { ok: true, outcome: fanWriteOutcome(result) }
}

export function fanWriteRefused(write: FanWrite, error: unknown): FanWriteSettled {
	if (error instanceof FanApiError) {
		return { ok: false, offline: false, refusal: fanRefusal(error), dropBanner: write.kind === "mode" }
	}
	return { ok: false, offline: true, refusal: null, dropBanner: true }
}

export type RegressKind = "none" | "no_deadline" | "counting_down"

export interface RegressView {
	kind: RegressKind
	countdown: string | null
}

function formatCountdown(milliseconds: number): string {
	const seconds = Math.floor(milliseconds / 1000)
	const hours = Math.floor(seconds / 3600)
	const minutes = Math.floor((seconds % 3600) / 60)
	if (hours > 0) return `${hours}h ${minutes}m`
	if (minutes > 0) return `${minutes}m ${seconds % 60}s`
	return `${seconds}s`
}

/** `regress_pending` is false both when no regress is on record and when one was
 *  lost to an agent restart; nothing on the wire separates them, so this does not
 *  pretend to. An unreadable deadline yields no countdown. */
export function fanRegressView(state: FanState, nowMs: number): RegressView {
	if (!state.regress_pending) return { kind: "none", countdown: null }
	const at = state.regress_at === undefined ? Number.NaN : Date.parse(state.regress_at)
	if (!Number.isFinite(at) || at <= nowMs) return { kind: "no_deadline", countdown: null }
	return { kind: "counting_down", countdown: formatCountdown(at - nowMs) }
}
