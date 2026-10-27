import type { FanCurve, FanMode, FanModeResult, FanState } from "@/types"
import { FAN_SAVE_STATUS, UnreadableResponseError, type CurveSaveResult, saveCurve, setFanLevel, setFanMode } from "@/lib/plus-api"
import { FanApiError, type FanBodyReason, type FanRejectCode } from "@/lib/plus-refusals"
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

/** The duties the daemon's trim and hysteresis can address as CURVE BANDS.
 *  Mirrors `curveBandDuty` in agent/plus/curve.go, which Validate enforces for
 *  L0..L5. Narrower than FAN_DUTIES on purpose: 80/85/90/95/100 are accepted as a
 *  hand-set duty but cannot be banded, because floor_of and center_of are keyed on
 *  the duty value and fall through to the emergency edge for anything else.
 *  `plus-fan-band-duties.drift.test.ts` reads the Go map and fails on drift. */
export const CURVE_BAND_DUTIES = [25, 30, 40, 50, 60, 70] as const

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
	/** Ask the daemon to re-read the curve file. Not a mode: the fan is not left
	 *  in a different state, the process is just restarted so a curve that already
	 *  reached the disk becomes behaviour. */
	| { kind: "restart" }

/** The four writes this panel can make, each carrying the confirm flag. */
export async function issueFanWrite(write: FanWrite): Promise<FanWriteResult> {
	if (write.kind === "level") {
		const w = levelWrite(write.level)
		return setFanLevel(w.level, w.confirm)
	}
	if (write.kind === "mode") {
		const w = modeWrite(write.mode)
		return await setFanMode(w.mode, w.confirm)
	}
	if (write.kind === "restart") {
		return await setFanMode("restart", true)
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

/** `version_skew` is not a hub refusal code: it is the shape mismatch the hub reports
 *  in data.reason when it cannot read the body at all. Keeping it in this union lets the
 *  panel say something actionable instead of "refused for an unknown reason". */
export type FanRefusalKey = FanRejectCode | "unclassified" | "unrecognised" | "version_skew"

export interface FanRefusalView {
	key: FanRefusalKey
	status: number
	detail?: string
	/** Why the body was rejected, when the hub said. Present on a shape mismatch that
	 *  has no refusal code, which is the version-skew case. */
	reason?: FanBodyReason
}

export type FanRefusalError = Pick<FanApiError, "code" | "rawCode" | "status" | "reason">

/** Reads `code` and never `status`. An identifier this build does not know is not
 *  filed under the status it shares with a fault of another kind: a dead sensor
 *  and a hot package are both a 503, and cooling a machine with a broken sensor
 *  sends the operator after the wrong problem. */
export function fanRefusal(error: FanRefusalError): FanRefusalView {
	const key: FanRefusalKey =
		error.code ??
		(error.reason === "unknown_field"
			? "version_skew"
			: error.rawCode === undefined
				? "unclassified"
				: "unrecognised")
	return {
		key,
		status: error.status,
		detail: error.rawCode === error.code ? undefined : error.rawCode,
		reason: error.reason,
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
/** `unknown` separates "the request never got an answer" from "the server answered
 *  in a shape this panel cannot read". Both leave the operator without a result, but
 *  only the first is a claim about the network, and a 2xx with an unreadable body is
 *  a claim the server made: it may well have applied the write. */
export type FanWriteSettled =
	| { ok: true; outcome: FanWriteOutcome }
	| {
			ok: false
			offline: boolean
			unknown: boolean
			refusal: FanRefusalView | null
			dropBanner: boolean
	  }

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
			unknown: false,
			refusal,
			dropBanner: write.kind === "mode" || LOST_THE_LAST_WRITE.has(refusal.key),
		}
	}
	// A response that arrived but could not be read is not proof the request
	// failed to reach the hub. Say "unknown" and drop the banner: the write may
	// be in force, so the previous banner is not something to keep standing on.
	if (error instanceof UnreadableResponseError) {
		return { ok: false, offline: false, unknown: true, refusal: null, dropBanner: true }
	}
	return { ok: false, offline: true, unknown: false, refusal: null, dropBanner: true }
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

/** One band of the duty curve: the duty holds from the previous edge up to and
 *  including `toC`. Inclusive of the right edge, mirroring `WantFor` in
 *  agent/plus/curve.go, which resolves a temperature at an edge to that band's
 *  duty (`tempC <= c.T1` returns `c.L1`). Getting this wrong shifts every band by
 *  one and still draws a plausible-looking curve. */
export interface CurveBand {
	toC: number
	dutyPct: number
}

/** `WantFor` is a step function with inclusive right edges. Reading the curve file
 *  as (T1,L1) pairs and drawing a point per pair puts each duty one band too high,
 *  because at T1 the daemon still drives L1 and only switches to L2 above it.
 *
 *  So the bands are built from the axis edges, and each band's duty is taken from
 *  the temperature at its own right edge. The first band ends at the idle edge
 *  (where L0 still holds); the last ends at the axis maximum, which is where the
 *  emergency duty has taken over. `plus-fan-view.test.ts` checks every whole
 *  degree on the axis against a direct port of `WantFor`. */
export function curveBands(curve: FanCurve): CurveBand[] {
	const { maxC } = curveAxis(curve)
	const edges = [curve.idle, curve.t1, curve.t2, curve.t3, curve.t4, curve.t5, maxC]
	return edges.map((toC) => ({ toC, dutyPct: dutyAt(curve, toC) }))
}

/** Direct port of `WantFor`. Kept next to the drawing code because the shape is
 *  only correct if it agrees with this, and the tests compare the two. */
export function dutyAt(curve: FanCurve, tempC: number): number {
	if (tempC >= curve.emerg) return curve.e
	if (tempC <= curve.idle) return curve.l0
	if (tempC <= curve.t1) return curve.l1
	if (tempC <= curve.t2) return curve.l2
	if (tempC <= curve.t3) return curve.l3
	if (tempC <= curve.t4) return curve.l4
	if (tempC <= curve.t5) return curve.l5
	return curve.e
}

/** The temperature axis a curve is drawn over.
 *
 *  The low end sits below the idle edge because L0 holds at and below it, and an
 *  axis that started at the idle edge would give that band zero width. The high
 *  end is the emergency temperature, where the drawn curve stops carrying band
 *  duties. A degenerate range collapses to a 1-degree span rather than dividing by
 *  zero. */
export function curveAxis(curve: FanCurve): { minC: number; maxC: number } {
	const spread = Math.max(curve.emerg, curve.t5) - curve.idle
	const pad = Math.max(1, Math.round(spread * 0.08))
	const minC = curve.idle - pad
	// The axis has to extend PAST the emergency temperature, or the emergency duty
	// gets no tread of its own: `maxC = emerg` puts the transition on the right
	// frame, so the top plateau is a zero-width riser and a marker drawn at emerg
	// sits on the border rather than marking anything. The extra pad past emerg
	// gives the terminal duty visible width and keeps the emergency line interior.
	const maxC = Math.max(curve.emerg, curve.t5 + 1) + pad
	return maxC > minC ? { minC, maxC } : { minC, maxC: minC + 1 }
}

/** The step outline as an SVG path in a normalised 0..100 box.
 *
 *  Each band is a horizontal run carrying its duty; the riser to the next band
 *  sits at the band's right edge. `y` is inverted because SVG grows downward. */
export function curvePath(curve: FanCurve): string {
	const { minC, maxC } = curveAxis(curve)
	const span = maxC - minC
	const x = (tempC: number) => ((tempC - minC) / span) * 100
	const y = (dutyPct: number) => 100 - dutyPct

	const bands = curveBands(curve)
	const first = bands[0]
	// Run the tread at the current duty, THEN rise to the next band's duty.
	//
	// Emitting `H` then `V` per band (the obvious form) puts every tread at the
	// PREVIOUS band's duty, because the riser that follows a tread changes the
	// level for the NEXT one. Measured against WantFor at every whole degree on
	// the axis, that form was wrong for 50 of 55 degrees: the drawn curve showed
	// a 70% ceiling where the daemon drives 100%, and lost the terminal plateau
	// entirely to a zero-width riser on the right frame.
	const commands = [`M 0 ${y(first?.dutyPct ?? 0)}`]
	for (let index = 0; index < bands.length; index++) {
		const band = bands[index]
		if (!band) continue
		commands.push(`H ${x(band.toC)}`)
		const next = bands[index + 1]
		if (next) commands.push(`V ${y(next.dutyPct)}`)
	}
	return commands.join(" ")
}

/** Where the machine currently sits on the curve, for the live marker.
 *
 *  This answers the three questions an operator actually has when looking at a
 *  fan curve: which band am I in, what duty does it ask for, and how far is the
 *  next jump. `WantFor` resolves an edge to the band on its left, so the band
 *  that ENDS at or above the current temperature is the active one.
 *
 *  Returns null when the temperature is unreadable. That is deliberate: a marker
 *  plotted at 0 because the sensor failed would claim the fan is idling in the
 *  coldest band, which is a lie with a safety consequence. */
export interface OperatingPoint {
	/** Celsius, as reported. */
	tempC: number
	/** The duty `WantFor` returns here. */
	dutyPct: number
	/** Index into `curveBands`, or -1 if the point is above every band. */
	bandIndex: number
	/** The next band edge, or null when the emergency duty already applies. */
	nextEdgeC: number | null
	/** The duty the next band asks for, or null at the top of the curve. */
	nextDutyPct: number | null
}

export function operatingPoint(curve: FanCurve, tempC: number | null): OperatingPoint | null {
	if (tempC === null || !Number.isFinite(tempC)) return null
	const bands = curveBands(curve)
	const dutyPct = dutyAt(curve, tempC)
	const bandIndex = bands.findIndex((band) => tempC <= band.toC)
	const next = bandIndex >= 0 ? bands[bandIndex + 1] : undefined
	return {
		tempC,
		dutyPct,
		bandIndex,
		nextEdgeC: next ? next.toC + 1 : null,
		nextDutyPct: next ? next.dutyPct : null,
	}
}

/** The x position of a temperature in the normalised 0..100 plot box. Exported so
 *  a marker and the drawn path cannot disagree about where a degree sits. */
export function curveX(curve: FanCurve, tempC: number): number {
	const { minC, maxC } = curveAxis(curve)
	return ((tempC - minC) / (maxC - minC)) * 100
}
