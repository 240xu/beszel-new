import type { FanCurve, FanMode, FanModeResult, FanState } from "@/types"
import { FAN_SAVE_STATUS, type CurveSaveResult, saveCurve, setFanLevel, setFanMode } from "@/lib/plus-api"
import { FanApiError, type FanRejectCode } from "@/lib/plus-refusals"
import { UNKNOWN } from "@/lib/plus-metrics"

export type { CurveSaveResult }

/** The mode route has no curve to put in force, so it answers in another shape —
 *  but it does answer: a write that returns nothing can never be stated. */
export type FanWriteResult = CurveSaveResult | FanModeResult

/** Mirrors `validLevels` in agent/plus/levels.go. Zero is not in either: the
 *  firmware writes whatever duty it is given straight into the EC fan-duty
 *  register, so zero is a request for zero percent and not a mode.
 *  `plus-fan-duties.drift.test.ts` reads that Go file and fails on any drift. */
export const FAN_DUTIES = [25, 30, 40, 50, 60, 70, 80, 85, 90, 95, 100] as const

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

const DAEMON_ARMS = {
	running: { kind: "daemon_running", inForce: true },
	stopped: { kind: "daemon_stopped", inForce: false },
} as const satisfies Record<string, { kind: FanOutcomeKind; inForce: boolean }>

export interface FanWriteOutcome {
	kind: FanOutcomeKind
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
			inForce: arm.inForce,
			needsRestart: false,
			nextAction: null,
			level: null,
		}
	}
	if (result.status === FAN_SAVE_STATUS.pendingRestart) {
		return {
			kind: "pending_restart",
			inForce: false,
			needsRestart: true,
			nextAction: "restart_pfand",
			level: result.level,
		}
	}
	return {
		kind: "applied",
		inForce: true,
		needsRestart: false,
		nextAction: null,
		level: result.level,
	}
}

export type FanRefusalKey = FanRejectCode | "unclassified" | "unrecognised"

export interface FanRefusalView {
	key: FanRefusalKey
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
		status: error.status,
		detail: error.rawCode === error.code ? undefined : error.rawCode,
	}
}

/** How a write ended, and what the panel may still be standing on. `dropBanner`
 *  is true when the banner from the last write describes the action that has just
 *  failed or gone unanswered, so it must go: "Awaiting restart" printed above "the
 *  restart failed" states the operator's next step twice, once of them known
 *  broken. A refusal that only says this request was turned away (a hot package, a
 *  rejected curve, a device that refused it) leaves the write before it in force,
 *  so its banner stays. One that says the fan is no longer holding what that write
 *  put there takes the banner down with it. */
export type FanWriteSettled =
	| { ok: true; outcome: FanWriteOutcome }
	| { ok: false; offline: boolean; refusal: FanRefusalView | null; dropBanner: boolean }

/** A refusal that reports the fan no longer holds the duty the last banner claimed:
 *  the restore failed, or the write could not be confirmed and may not have landed. */
const LOST_THE_LAST_WRITE: ReadonlySet<FanRefusalKey> = new Set(["rollback_failed", "write_unconfirmed"])

/** There is no `if (result)` for a caller to write: a write that could return
 *  nothing is a write whose banner can be silently suppressed. */
export function fanWriteSettled(result: FanWriteResult): FanWriteSettled {
	return { ok: true, outcome: fanWriteOutcome(result) }
}

export function fanWriteRefused(write: FanWrite, error: unknown): FanWriteSettled {
	if (error instanceof FanApiError) {
		const refusal = fanRefusal(error)
		return {
			ok: false,
			offline: false,
			refusal,
			dropBanner: write.kind === "mode" || LOST_THE_LAST_WRITE.has(refusal.key),
		}
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
 *  pretend to. An unreadable deadline yields no countdown. The `none` arm is
 *  still a state the panel renders rather than drops -- a duty set before that
 *  restart keeps driving the fan, and silence would hide the possibility. */
export function fanRegressView(state: FanState, nowMs: number): RegressView {
	if (!state.regress_pending) return { kind: "none", countdown: null }
	const at = state.regress_at === undefined ? Number.NaN : Date.parse(state.regress_at)
	if (!Number.isFinite(at) || at <= nowMs) return { kind: "no_deadline", countdown: null }
	return { kind: "counting_down", countdown: formatCountdown(at - nowMs) }
}
