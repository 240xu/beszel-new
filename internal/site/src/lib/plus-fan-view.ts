import type { FanCurve, FanMode, FanState } from "@/types"
import { FAN_SAVE_STATUS, type CurveSaveResult, saveCurve, setFanLevel, setFanMode } from "@/lib/plus-api"
import type { FanApiError, FanRejectCode } from "@/lib/plus-refusals"
import { UNKNOWN } from "@/lib/plus-metrics"

export type { CurveSaveResult }

/** The closed duty whitelist, mirroring `validLevels` in agent/plus/levels.go.
 *  Zero is the handover back to the firmware curve. */
export const FAN_DUTIES = [0, 25, 30, 40, 50, 60, 70, 80, 85, 90, 95, 100] as const

/** The server publishes the label for a duty it read, so the panel never
 *  rebuilds one: a firmware table re-tuned to call 30 something else must not
 *  be contradicted by a second opinion derived from the number. */
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

/** The three writes this panel can make, each carrying the confirm flag the
 *  server refuses to do without. Exported so the flag can be asserted against
 *  the request that goes out rather than against a call site. */
export async function issueFanWrite(write: FanWrite): Promise<CurveSaveResult | undefined> {
	if (write.kind === "level") {
		const w = levelWrite(write.level)
		return setFanLevel(w.level, w.confirm)
	}
	if (write.kind === "mode") {
		const w = modeWrite(write.mode)
		await setFanMode(w.mode, w.confirm)
		return undefined
	}
	const w = curveWrite(write.curve)
	return saveCurve(w, w.confirm)
}

export type FanOutcomeKind = "applied" | "pending_restart"

/** Wording that has to mean one thing. The pending arm borrows none of the
 *  applied arm's: a panel that calls a curve with no daemon in front of it
 *  in force has told the operator the opposite of the truth about their fan. */
const OUTCOME_TEXT: Record<FanOutcomeKind, string> = {
	applied: "In force — the fan is running this now.",
	pending_restart: "Written to disk; the curve daemon has not picked it up.",
}

export interface FanWriteOutcome {
	kind: FanOutcomeKind
	/** The English source text. `plus-fan-tab.tsx` translates both keys. */
	text: string
	inForce: boolean
	needsRestart: boolean
	nextAction: "restart_pfand" | null
	level: number
}

/** Branches on `status`, the wire discriminator, so the arm a write lands in is
 *  the one the server named. The pending arm is the one a panel must never call
 *  saved: the bytes are on disk and nothing has read them. */
export function fanWriteOutcome(result: CurveSaveResult): FanWriteOutcome {
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

/** The English source text, and the only place the meaning is written down.
 *  `plus-fan-tab.tsx` translates every key and falls back to this. */
const REFUSAL_TEXT: Record<FanRefusalKey, string> = {
	rollback_failed: "The previous duty could not be restored: the fan is holding one you did not ask for.",
	write_unconfirmed: "The fan is not holding the duty that was requested; the old one was put back.",
	write_failed: "The fan control device refused the write.",
	confirm_required: "The change was not confirmed, so the fan was left alone.",
	level_not_allowed: "That duty is not one this firmware accepts.",
	curve_rejected: "The curve failed validation and was rejected before anything was written.",
	pfand_active: "The curve daemon owns the fan; stop it before setting a duty by hand.",
	package_too_hot: "The package is at or above the emergency edge in the curve, so no duty was written.",
	temperature_unknown:
		"The package temperature could not be read, so no duty was written and the reading cannot be trusted.",
	breaker_open: "Fan control is latched open after repeated failures; nothing was written.",
	unsupported: "Fan control is only implemented on Linux.",
	internal: "The write did not complete.",
	unclassified: "The request was refused before it reached the fan.",
	unrecognised: "The server refused this for a reason this build does not know.",
}

export interface FanRefusalView {
	key: FanRefusalKey
	text: string
	status: number
	detail?: string
}

export type FanRefusalError = Pick<FanApiError, "code" | "rawCode" | "status">

/** Reads `code` and never `status`. A refusal with an identifier this build does
 *  not know is reported as unknown rather than filed under the status it shares
 *  with a fault of a different kind: a dead sensor and a hot package are both a
 *  503, and telling an operator to cool a machine with a broken sensor sends them
 *  after the wrong problem. */
export function fanRefusal(error: FanRefusalError): FanRefusalView {
	const key: FanRefusalKey = error.code ?? (error.rawCode === undefined ? "unclassified" : "unrecognised")
	return {
		key,
		text: REFUSAL_TEXT[key],
		status: error.status,
		detail: error.rawCode === error.code ? undefined : error.rawCode,
	}
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
 *  lost to an agent restart; nothing on the wire separates them, so this does
 *  not pretend to. A false yields no countdown and no claim about expiry. A
 *  true with no readable deadline yields no countdown either, because the
 *  backend omits `regress_at` whenever none exists and a zero time would
 *  serialise as year one and invite a countdown towards it. */
export function fanRegressView(state: FanState, nowMs: number): RegressView {
	if (!state.regress_pending) return { kind: "none", countdown: null }
	const at = state.regress_at === undefined ? Number.NaN : Date.parse(state.regress_at)
	if (!Number.isFinite(at) || at <= nowMs) return { kind: "no_deadline", countdown: null }
	return { kind: "counting_down", countdown: formatCountdown(at - nowMs) }
}
