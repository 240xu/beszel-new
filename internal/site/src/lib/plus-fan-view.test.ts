import { describe, expect, test } from "bun:test"
import type { FanCurve, FanState } from "@/types"
import { FAN_REJECT_CODES, type FanRejectCode, readFanRefusal } from "@/lib/plus-refusals"
import {
	confirmed,
	curveAxis,
	curveBands,
	curvePath,
	curveX,
	operatingPoint,
	curveWrite,
	dutyAt,
	FAN_DUTIES,
	fanLevelLabel,
	fanRefusal,
	fanRegressView,
	fanWriteOutcome,
	fanWriteRefused,
	fanWriteSettled,
	issueFanWrite,
	levelWrite,
	modeWrite,
	type CurveSaveResult,
	type FanWrite,
	type FanWriteSettled,
} from "@/lib/plus-fan-view"

/** Narrowing that does not depend on a test having passed: `expect(x.ok)` is a
 *  runtime claim, and the types here are the guarantee that survives a re-spelling. */
function outcomeOf(settled: FanWriteSettled) {
	if (!settled.ok) throw new Error("expected a write that was answered")
	return settled.outcome
}

function refusalOf(settled: FanWriteSettled) {
	if (settled.ok) throw new Error("expected a write that was refused")
	return settled
}

/** The two wire bodies that carry the most dangerous disagreement. They share a
 *  status on purpose: a status-only client reports a broken sensor as a hot
 *  machine, and an operator who believes that cools a host that needs nothing. */
const HOT = {
	status: 503,
	data: { code: "package_too_hot" },
	message: "The package is at or above the emergency edge.",
}
const SENSOR = {
	status: 503,
	data: { code: "temperature_unknown" },
	message: "The package temperature could not be read.",
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

const CURVE: FanCurve = {
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

type FetchCall = { url: string; method: string; body: unknown }

function stubFetch(responses: { status: number; body: string }[]): { calls: FetchCall[] } {
	const calls: FetchCall[] = []
	let i = 0
	globalThis.fetch = ((input: RequestInfo | URL, init?: RequestInit) => {
		const r = responses[Math.min(i, responses.length - 1)]
		i++
		calls.push({
			url: String(input),
			method: init?.method ?? "GET",
			body: init?.body === undefined ? undefined : JSON.parse(String(init.body)),
		})
		return Promise.resolve(new Response(r.body, { status: r.status }))
	}) as typeof fetch
	return { calls }
}

function ok(obj: unknown): { status: number; body: string } {
	return { status: 200, body: JSON.stringify(obj) }
}

function state(over: Partial<FanState> = {}): FanState {
	return {
		pfand_active: false,
		level_read: true,
		level: 70,
		level_label: "70%",
		pkg_temp_read: true,
		pkg_temp_c: 50,
		regress_pending: false,
		curve: CURVE,
		...over,
	}
}

describe("the duty label is the server's, not one rebuilt here", () => {
	test("an inconsistent pair is resolved in favour of the server's label", () => {
		// The firmware table can be re-tuned; a label rebuilt from the number
		// would answer 30% for a duty the firmware calls something else.
		expect(fanLevelLabel(state({ level: 30, level_label: "quiet" }))).toBe("quiet")
	})

	test("zero reads as zero percent, because that is what the firmware calls it", () => {
		// `_FSL 0` is the firmware's zero-percent performance state. It is not a
		// handover and nothing on this stack performs one, so a label calling it
		// `auto` would claim an event the reader cannot observe. This suite used to
		// assert the opposite, from a fixture the server has not produced since
		// LevelLabel stopped answering `auto` for a zero.
		expect(fanLevelLabel(state({ level: 0, level_label: "0%" }))).toBe("0%")
		expect(fanLevelLabel(state({ level: 0, level_label: "0%" }))).not.toMatch(/auto/i)
	})

	test("a duty nobody could read has no label rather than a fabricated one", () => {
		const unknown = fanLevelLabel(state({ level_read: false, level: null, level_label: undefined }))
		expect(unknown).toBe(fanLevelLabel(state({ level_read: false, level: null, level_label: undefined })))
		expect(unknown).not.toBe(fanLevelLabel(state({ level: 70, level_label: "70%" })))
		expect(unknown).not.toMatch(/[\d]/)
	})
})

describe("confirming a write", () => {
	test("the confirm flag is the literal true, never a runtime boolean", () => {
		expect(levelWrite(60)).toEqual({ level: 60, confirm: true })
		expect(modeWrite("auto")).toEqual({ mode: "auto", confirm: true })
		expect(curveWrite(CURVE).confirm).toBe(true)
		expect(confirmed()).toBe(true)
	})

	test("a curve body carries every field, so the server cannot read it as partial", () => {
		expect(Object.keys(curveWrite(CURVE)).sort()).toEqual([...Object.keys(CURVE), "confirm"].sort())
	})

	test("every write that leaves the panel leaves with the confirm flag set", async () => {
		const { calls } = stubFetch([ok({ applied: true, needs_pfand_restart: false, status: "applied", level: 60 })])
		await issueFanWrite({ kind: "level", level: 60 })
		await issueFanWrite({ kind: "mode", mode: "auto" })
		await issueFanWrite({ kind: "curve", curve: CURVE })
		expect(calls).toHaveLength(3)
		for (const call of calls) {
			expect(call.method).toBe("POST")
			expect((call.body as { confirm: unknown }).confirm).toBe(true)
		}
		expect(calls.map((c) => c.url)).toEqual(["/api/plus/fan/level", "/api/plus/fan/mode", "/api/plus/fan/curve"])
	})
})

describe("what a write reported", () => {
	test("a write nothing is running is never reported as in force", () => {
		const out = fanWriteOutcome(PENDING)
		expect(out.kind).toBe("pending_restart")
		expect(out.inForce).toBe(false)
		expect(out.needsRestart).toBe(true)
	})

	test("an in-force write is the only kind that offers no restart", () => {
		const out = fanWriteOutcome(APPLIED)
		expect(out.kind).toBe("applied")
		expect(out.inForce).toBe(true)
		expect(out.needsRestart).toBe(false)
	})

	test("a pending write always leaves the operator an outlet", () => {
		expect(fanWriteOutcome(PENDING).nextAction).toBe("restart_pfand")
		expect(fanWriteOutcome(APPLIED).nextAction).toBeNull()
	})

	test("the two kinds can never answer the same way to the same question", () => {
		// The question a panel actually asks: may this be called saved?
		expect(fanWriteOutcome(APPLIED).inForce).toBe(!fanWriteOutcome(PENDING).inForce)
		expect(fanWriteOutcome(APPLIED).needsRestart).toBe(!fanWriteOutcome(PENDING).needsRestart)
	})

	test("a write nothing is running is distinguishable from one that is in force", () => {
		// The wording itself moved to plus-fan-tab.tsx, where it is translated; what
		// stays here is the fact the panel branches on, and the two arms disagree.
		expect(fanWriteOutcome(PENDING).kind).toBe("pending_restart")
		expect(fanWriteOutcome(APPLIED).kind).toBe("applied")
		expect(fanWriteOutcome(PENDING).inForce).toBe(false)
	})

	test("no two outcomes are told apart by the same kind", () => {
		const kinds = [
			fanWriteOutcome(APPLIED).kind,
			fanWriteOutcome(PENDING).kind,
			fanWriteOutcome({ mode: "auto", pfand_active: true }).kind,
			fanWriteOutcome({ mode: "manual", pfand_active: false }).kind,
		]
		expect(new Set(kinds).size).toBe(kinds.length)
	})
})

describe("a daemon change is answered like any other write", () => {
	test("a started daemon produces an outcome, so no banner can be left behind", async () => {
		const { calls } = stubFetch([ok({ mode: "auto", pfand_active: true })])
		const outcome = outcomeOf(fanWriteSettled(await issueFanWrite({ kind: "mode", mode: "auto" })))
		expect(calls[0].url).toBe("/api/plus/fan/mode")
		expect(outcome.kind).toBe("daemon_running")
		expect(outcome.inForce).toBe(true)
		expect(outcome.needsRestart).toBe(false)
		expect(outcome.nextAction).toBeNull()
	})

	test("what is reported is what the server says runs, not what was asked for", async () => {
		// systemctl can exit 0 and leave the daemon down. The request said auto; the
		// answer did not, and the answer is the one the panel may restate.
		stubFetch([ok({ mode: "auto", pfand_active: false })])
		const outcome = outcomeOf(fanWriteSettled(await issueFanWrite({ kind: "mode", mode: "auto" })))
		expect(outcome.kind).toBe("daemon_stopped")
		expect(outcome.inForce).toBe(false)
	})

	test("a daemon change reports no duty, because it programs none", () => {
		expect(fanWriteOutcome({ mode: "auto", pfand_active: true }).level).toBeNull()
		expect(fanWriteOutcome({ mode: "auto", pfand_active: true }).level).not.toBe(0)
	})

	test("a daemon that has answered is never reported as still needing one", () => {
		for (const pfand_active of [true, false]) {
			expect(fanWriteOutcome({ mode: "auto", pfand_active }).needsRestart).toBe(false)
		}
	})

	test("a stopped daemon is not reported as the curve running", () => {
		const stopped = fanWriteOutcome({ mode: "manual", pfand_active: false })
		expect(stopped.kind).toBe("daemon_stopped")
		expect(stopped.inForce).toBe(false)
	})
})

describe("a refused write says whether the last banner still holds", () => {
	const REFUSALS: { write: FanWrite; code: FanRejectCode }[] = [
		{ write: { kind: "mode", mode: "auto" }, code: "internal" },
		{ write: { kind: "level", level: 70 }, code: "package_too_hot" },
		{ write: { kind: "curve", curve: CURVE }, code: "curve_rejected" },
	]

	test("a failed restart drops the banner that asked for the restart", () => {
		const failure = refusalOf(
			fanWriteRefused(REFUSALS[0].write, readFanRefusal(500, JSON.stringify({ data: { code: "internal" } })))
		)
		expect(failure.dropBanner).toBe(true)
		expect(failure.offline).toBe(false)
		expect(failure.refusal?.key).toBe("internal")
	})

	test("a refused duty keeps the banner of the write before it, which is still in force", () => {
		const failure = refusalOf(fanWriteRefused(REFUSALS[1].write, readFanRefusal(503, JSON.stringify(HOT))))
		expect(failure.dropBanner).toBe(false)
		expect(failure.refusal?.key).toBe("package_too_hot")
	})

	test("a duty whose old value could not be restored drops the banner that claimed it", () => {
		// The fan is now holding a duty nobody asked for, so "In force" over it
		// describes a state the server has just said it could not produce.
		const failure = refusalOf(
			fanWriteRefused(REFUSALS[1].write, readFanRefusal(500, JSON.stringify({ data: { code: "rollback_failed" } })))
		)
		expect(failure.refusal?.key).toBe("rollback_failed")
		expect(failure.dropBanner).toBe(true)
	})

	test("an unconfirmed duty drops the banner too, because the fan did not keep it", () => {
		const failure = refusalOf(
			fanWriteRefused(REFUSALS[1].write, readFanRefusal(500, JSON.stringify({ data: { code: "write_unconfirmed" } })))
		)
		expect(failure.refusal?.key).toBe("write_unconfirmed")
		expect(failure.dropBanner).toBe(true)
	})

	test("a curve whose old value could not be restored drops the banner as well", () => {
		for (const code of ["rollback_failed", "write_unconfirmed"] as const) {
			const failure = refusalOf(
				fanWriteRefused(REFUSALS[2].write, readFanRefusal(500, JSON.stringify({ data: { code } })))
			)
			expect(failure.refusal?.key).toBe(code)
			expect(failure.dropBanner).toBe(true)
		}
	})

	test("a device that refused the write outright leaves the last write standing", () => {
		// Nothing moved, so the banner from the write before it still describes
		// the duty in the firmware; dropping it would hide a real state.
		const failure = refusalOf(
			fanWriteRefused(REFUSALS[1].write, readFanRefusal(500, JSON.stringify({ data: { code: "write_failed" } })))
		)
		expect(failure.refusal?.key).toBe("write_failed")
		expect(failure.dropBanner).toBe(false)
	})

	test("a write that lands still settles in force with no banner to drop", () => {
		const settled = fanWriteSettled(APPLIED)
		expect(outcomeOf(settled).kind).toBe("applied")
		expect("dropBanner" in settled).toBe(false)
	})

	test("a request that never left has nothing to say about the last one either", () => {
		const failure = refusalOf(fanWriteRefused(REFUSALS[0].write, new TypeError("fetch failed")))
		expect(failure.offline).toBe(true)
		expect(failure.refusal).toBeNull()
		expect(failure.dropBanner).toBe(true)
	})

	test("every refusal names the key the panel would render", () => {
		for (const { write, code } of REFUSALS) {
			const failure = refusalOf(fanWriteRefused(write, readFanRefusal(500, JSON.stringify({ data: { code } }))))
			expect(failure.refusal?.key).toBe(code)
		}
	})
})

describe("refusals are read by code, never by status", () => {
	test("a hot package and a dead sensor are different answers on one status", () => {
		const hot = fanRefusal(readFanRefusal(503, JSON.stringify(HOT)))
		const sensor = fanRefusal(readFanRefusal(503, JSON.stringify(SENSOR)))
		expect(hot.key).not.toBe(sensor.key)
	})

	test("one code under two statuses is one answer, so the status cannot be doing the work", () => {
		const a = fanRefusal(readFanRefusal(400, JSON.stringify(HOT)))
		const b = fanRefusal(readFanRefusal(503, JSON.stringify(HOT)))
		expect(a.key).toBe(b.key)
		// The one field besides the key that could vary with the status it arrived
		// on, and the only thing a log reader still has to go on.
		expect(a.status).not.toBe(b.status)
		expect(a.detail ?? "").toBe(b.detail ?? "")
	})

	test("one status under two codes is two answers, so the code is doing the work", () => {
		const codes = FAN_REJECT_CODES.map(
			(code) => fanRefusal(readFanRefusal(503, JSON.stringify({ ...HOT, data: { code } }))).key
		)
		expect(new Set(codes).size).toBe(FAN_REJECT_CODES.length)
	})

	test("an identifier this build does not know is not borrowed from its status", () => {
		const seen = fanRefusal(readFanRefusal(503, JSON.stringify({ ...HOT, data: { code: "thermal_profile_t4" } })))
		expect(seen.key).not.toBe(fanRefusal(readFanRefusal(503, JSON.stringify(HOT))).key)
		expect(seen.detail).toContain("thermal_profile_t4")
	})

	test("a refusal raised before classification keeps the status for the log", () => {
		const early = fanRefusal(readFanRefusal(400, JSON.stringify({ status: 400, message: "bad body", data: {} })))
		expect(early.status).toBe(400)
		expect(early.detail).toBeUndefined()
	})

	test("every classified refusal the server can raise reaches a key of its own", () => {
		const keys = FAN_REJECT_CODES.map((code) => fanRefusal(readFanRefusal(500, JSON.stringify({ data: { code } }))).key)
		expect(keys.some((key) => key.length === 0)).toBe(false)
		expect(new Set(keys).size).toBe(FAN_REJECT_CODES.length)
	})

	test("the three ways a write fails after reaching the device are told apart", () => {
		const code = (c: string) => fanRefusal(readFanRefusal(500, JSON.stringify({ data: { code: c } }))).key
		expect(new Set([code("rollback_failed"), code("write_unconfirmed"), code("write_failed")]).size).toBe(3)
	})

	test("the hot and the sensor answer reach keys that are not each other's", () => {
		// The sentences moved to plus-fan-tab.tsx, where they are translated; the
		// guarantee that survives a re-spelling or a translation is that the two
		// codes this module already distinguishes stay distinguished.
		expect(fanRefusal(readFanRefusal(503, JSON.stringify(HOT))).key).toBe("package_too_hot")
		expect(fanRefusal(readFanRefusal(503, JSON.stringify(SENSOR))).key).toBe("temperature_unknown")
	})
})

describe("a refusal with nothing to branch on", () => {
	const STATUSES = [400, 401, 403, 404, 409, 422, 500, 502, 503]
	const bare = (status: number) =>
		fanRefusal(readFanRefusal(status, JSON.stringify({ status, message: "something went wrong" })))

	test("it lands on no known code's key, on any status it might arrive on", () => {
		const known = new Set<string>(FAN_REJECT_CODES)
		for (const status of STATUSES) {
			expect(known.has(bare(status).key)).toBe(false)
		}
	})

	test("every status without a code reaches the same key, so the status cannot be doing the work", () => {
		expect(new Set(STATUSES.map((status) => bare(status).key)).size).toBe(1)
	})

	test("it is told apart from a reason this build simply does not know", () => {
		const unseen = fanRefusal(readFanRefusal(500, JSON.stringify({ data: { code: "thermal_profile_t4" } })))
		expect(bare(500).key).not.toBe(unseen.key)
		expect(unseen.detail).toBe("thermal_profile_t4")
	})

	test("it is one key on every status, so no status can be read as a different fault", () => {
		// The sentence for this key says nothing about the fan, because the wire
		// cannot tell a refusal raised before it read the request from one raised
		// after it acted. That claim is pinned against the wording where the
		// wording now lives, in plus-fan-tab.guard.test.ts.
		expect(new Set(STATUSES.map((status) => bare(status).key))).toEqual(new Set(["unclassified"]))
	})
})

describe("the regress deadline", () => {
	const now = Date.parse("2026-10-19T12:00:00Z")

	test("no regress on record means no countdown at all", () => {
		const view = fanRegressView(state({ regress_pending: false }), now)
		expect(view.kind).toBe("none")
		expect(view.countdown).toBeNull()
	})

	test("a deadline that is absent is not a deadline, and is not counted down", () => {
		// The backend omits regress_at whenever none exists; a zero would
		// serialise as year one and invite a countdown towards it.
		const view = fanRegressView(state({ regress_pending: true, regress_at: undefined }), now)
		expect(view.countdown).toBeNull()
		expect(view.kind).toBe("no_deadline")
	})

	test("a deadline that cannot be read is not counted down either", () => {
		for (const at of ["", "not-a-time", "0000-00-00T00:00:00Z"]) {
			expect(fanRegressView(state({ regress_pending: true, regress_at: at }), now).countdown).toBeNull()
		}
	})

	test("a deadline already past has nothing left to count", () => {
		const view = fanRegressView(state({ regress_pending: true, regress_at: "2026-10-19T11:00:00Z" }), now)
		expect(view.countdown).toBeNull()
		expect(view.kind).toBe("no_deadline")
	})

	test("a real future deadline counts down", () => {
		const view = fanRegressView(state({ regress_pending: true, regress_at: "2026-10-19T12:04:30Z" }), now)
		expect(view.kind).toBe("counting_down")
		expect(view.countdown).toBe("4m 30s")
	})

	test("a countdown is never produced without a pending regress", () => {
		for (const regress_at of [undefined, "2026-10-19T12:04:30Z", "2026-10-19T11:00:00Z"]) {
			expect(fanRegressView(state({ regress_pending: false, regress_at }), now).countdown).toBeNull()
		}
	})
})

describe("the duty whitelist", () => {
	test("every duty the firmware accepts is offered", () => {
		expect(FAN_DUTIES).toContain(100)
		expect(FAN_DUTIES.length).toBe(11)
	})

	test("a duty the firmware refuses is not offered", () => {
		expect(FAN_DUTIES).not.toContain(37)
		expect(FAN_DUTIES).not.toContain(1)
	})
})

test("a newer panel against an older hub is reported as a version skew", () => {
	const view = fanRefusal(
		readFanRefusal(
			400,
			JSON.stringify({ status: 400, message: "invalid body", data: { reason: "unknown_field" } })
		)
	)
	expect(view.key).toBe("version_skew")
	expect(view.detail).toBeUndefined()
})

test("a malformed body is not called a version skew", () => {
	const view = fanRefusal(
		readFanRefusal(400, JSON.stringify({ status: 400, message: "invalid body", data: { reason: "not_json" } }))
	)
	expect(view.key).toBe("unclassified")
})

// A 2xx whose body cannot be read is not proof the request never reached the hub.
// It must not be reported as "offline": the write may have been applied.
test("an unreadable success response is unknown, not offline", async () => {
	const { UnreadableResponseError } = await import("@/lib/plus-api")
	const settled = fanWriteRefused({ kind: "level" } as never, new UnreadableResponseError(200))
	expect(settled).toMatchObject({ ok: false, offline: false, unknown: true })
	expect(settled.ok === false && settled.refusal).toBeNull()
})

test("a genuine network failure stays offline", () => {
	const settled = fanWriteRefused({ kind: "level" } as never, new TypeError("Failed to fetch"))
	expect(settled).toMatchObject({ ok: false, offline: true, unknown: false })
})

// WantFor in agent/plus/curve.go is a step function with inclusive right edges.
// Drawing (T1,L1) pairs as points shifts every band up one, which still looks
// like a curve. These tests compare the drawn shape against a direct port of
// WantFor at every whole degree on the axis, so that class of error cannot ship.
describe("curve outline", () => {
	const curve: FanCurve = {
		idle: 45,
		l0: 25,
		t1: 55,
		l1: 30,
		t2: 65,
		l2: 40,
		t3: 72,
		l3: 50,
		t4: 80,
		l4: 60,
		t5: 88,
		l5: 70,
		emerg: 95,
		e: 100,
		emerg_rel: 5,
		cap: 100,
		trim: 0,
		gain: 0,
		hyst: 2,
		dwell: 5,
		dead: 3,
		iv: 10,
	}

	/** What the drawn bands say the duty is at a temperature. */
	const drawnDutyAt = (tempC: number): number => {
		const bands = curveBands(curve)
		return bands.find((band) => tempC <= band.toC)?.dutyPct ?? bands[bands.length - 1]?.dutyPct ?? 0
	}

	test("the drawn bands agree with WantFor at every whole degree on the axis", () => {
		const { minC, maxC } = curveAxis(curve)
		for (let t = minC; t <= maxC; t++) {
			expect({ t, duty: drawnDutyAt(t) }).toEqual({ t, duty: dutyAt(curve, t) })
		}
	})

	test("a temperature sitting exactly on an edge keeps the lower band's duty", () => {
		// The inclusive-right-edge rule, stated as its own case: at T1 WantFor
		// still returns L1, so the riser to L2 belongs just above it.
		expect(dutyAt(curve, curve.idle)).toBe(curve.l0)
		expect(dutyAt(curve, curve.t1)).toBe(curve.l1)
		expect(dutyAt(curve, curve.t2)).toBe(curve.l2)
		expect(dutyAt(curve, curve.t3)).toBe(curve.l3)
		expect(dutyAt(curve, curve.t4)).toBe(curve.l4)
		expect(dutyAt(curve, curve.t5)).toBe(curve.l5)
	})

	test("the emergency duty takes over at and above the emergency temperature", () => {
		expect(dutyAt(curve, curve.emerg)).toBe(curve.e)
		expect(dutyAt(curve, curve.emerg + 10)).toBe(curve.e)
	})

	test("the axis gives the idle band a width, since L0 holds below the idle edge", () => {
		const axis = curveAxis(curve)
		expect(axis.minC).toBeLessThan(curve.idle)
		expect(axis.maxC).toBeGreaterThan(curve.emerg)
	})

	test("the emergency transition sits inside the plot, not on its right frame", () => {
		// `maxC = emerg` put the transition at x=100, so the terminal duty was a
		// zero-width riser hidden by the frame and an emergency marker drawn there
		// marked the axis end rather than a transition. The axis must extend past it.
		const axis = curveAxis(curve)
		const emergencyX = ((curve.emerg - axis.minC) / (axis.maxC - axis.minC)) * 100
		expect(emergencyX).toBeLessThan(100)
		expect(emergencyX).toBeGreaterThan(0)
	})

	test("a degenerate curve does not divide by zero", () => {
		const flat: FanCurve = { ...curve, idle: 90, t1: 90, t2: 90, t3: 90, t4: 90, t5: 90, emerg: 90 }
		const axis = curveAxis(flat)
		expect(axis.maxC).toBeGreaterThan(axis.minC)
		expect(curvePath(flat)).not.toContain("NaN")
	})

	test("the outline uses right angles only, so it reads as a step function", () => {
		const commands = curvePath(curve).match(/[A-Z]/g) ?? []
		expect(new Set(commands)).toEqual(new Set(["M", "H", "V"]))
	})

	// The test above passed while the chart was wrong for 50 of 55 degrees: it
	// checked the commands were right-angled, and never checked the treads were
	// at the right HEIGHT. This parses the path back into horizontal treads and
	// compares each one against WantFor, which is the only check that would have
	// caught the riser being emitted before the tread instead of after it.
	test("every tread in the drawn path sits at the duty WantFor returns there", () => {
		const path = curvePath(curve)
		const treads: Array<{ x0: number; x1: number; y: number }> = []
		let px = 0
		let py = 0
		for (const token of path.match(/[MHV][^MHV]*/g) ?? []) {
			const numbers = token.slice(1).trim().split(/\s+/).map(Number)
			if (token[0] === "M") {
				px = numbers[0] ?? 0
				py = numbers[1] ?? 0
			} else if (token[0] === "H") {
				treads.push({ x0: px, x1: numbers[0] ?? 0, y: py })
				px = numbers[0] ?? 0
			} else {
				py = numbers[0] ?? 0
			}
		}
		expect(treads.length).toBeGreaterThan(0)

		// The duty drawn at a temperature: the leftmost tread covering that x wins,
		// because the riser sits at the band's right edge and WantFor resolves that
		// edge to the band on its left.
		const dutyDrawnAt = (tempC: number): number | null => {
			const at = xOf(tempC)
			let best: { x0: number; x1: number; y: number } | null = null
			for (const tread of treads) {
				const lo = Math.min(tread.x0, tread.x1)
				const hi = Math.max(tread.x0, tread.x1)
				if (at >= lo - 1e-9 && at <= hi + 1e-9) {
					if (!best || lo < Math.min(best.x0, best.x1)) best = tread
				}
			}
			return best ? 100 - best.y : null
		}

		const axis = curveAxis(curve)
		const span = axis.maxC - axis.minC
		const xOf = (tempC: number) => ((tempC - axis.minC) / span) * 100
		const wrong: Array<{ tempC: number; want: number; drawn: number | null }> = []
		for (let tempC = axis.minC; tempC <= axis.maxC; tempC++) {
			const want = dutyAt(curve, tempC)
			const drawn = dutyDrawnAt(tempC)
			if (drawn !== want) wrong.push({ tempC, want, drawn })
		}
		expect(wrong).toEqual([])
	})

	test("the drawn path ends at the emergency duty, not at the last band", () => {
		// A zero-width riser on the right frame hides the terminal plateau: the
		// chart then shows the last BAND duty as the ceiling when the daemon drives
		// the emergency duty. The path must reach the top of the axis at its right
		// end, not only on the frame border.
		const treads = (curvePath(curve).match(/[MHV][^MHV]*/g) ?? [])
			.filter((token) => token[0] === "H")
			.map((token) => Number(token.slice(1)))
		const topDuty = Math.max(...(curvePath(curve).match(/[MHV][^MHV]*/g) ?? [])
			.filter((token) => token[0] === "V")
			.map((token) => 100 - Number(token.slice(1))))
		expect(topDuty).toBe(curve.e)
		expect(treads.length).toBeGreaterThan(1)
	})

	test("every coordinate stays inside the 0..100 box", () => {
		const numbers = (curvePath(curve).match(/-?\d+(\.\d+)?/g) ?? []).map(Number)
		expect(numbers.length).toBeGreaterThan(0)
		for (const n of numbers) {
			expect(Number.isFinite(n)).toBe(true)
			expect(n).toBeGreaterThanOrEqual(0)
			expect(n).toBeLessThanOrEqual(100)
		}
	})
})

describe("operating point", () => {
	const curve: FanCurve = {
		idle: 45, l0: 25, t1: 55, l1: 30, t2: 65, l2: 40, t3: 72, l3: 50,
		t4: 80, l4: 60, t5: 88, l5: 70, emerg: 95, e: 100,
		emerg_rel: 5, cap: 100, trim: 0, gain: 0, hyst: 2, dwell: 5, dead: 3, iv: 10,
	}

	test("names the band the temperature falls in, and the next jump", () => {
		const at62 = operatingPoint(curve, 62)
		expect(at62).toMatchObject({ tempC: 62, dutyPct: 40, bandIndex: 2 })
		// 62 C is inside the band ending at 65, so the NEXT band ends at 72 and the
		// jump lands at 73 (the first degree that resolves to the higher duty).
		expect(at62?.nextEdgeC).toBe(73)
		expect(at62?.nextDutyPct).toBe(50)
	})

	test("a temperature on an edge stays in the band that ends there", () => {
		// WantFor is inclusive on the right edge: 65 still asks for L2, and the jump
		// to L3 happens at 66. The marker and the caption must agree with that.
		expect(operatingPoint(curve, 65)?.dutyPct).toBe(40)
		expect(operatingPoint(curve, 66)?.dutyPct).toBe(50)
	})

	test("above the last band there is no next edge", () => {
		const hot = operatingPoint(curve, 96)
		expect(hot?.dutyPct).toBe(100)
		expect(hot?.nextEdgeC).toBeNull()
		expect(hot?.nextDutyPct).toBeNull()
	})

	test("an unreadable temperature yields no point rather than zero", () => {
		expect(operatingPoint(curve, null)).toBeNull()
		expect(operatingPoint(curve, Number.NaN)).toBeNull()
	})

	test("marker x and the drawn path use the same mapping", () => {
		const axis = curveAxis(curve)
		for (const tempC of [45, 55, 70, 88, 95]) {
			const expected = ((tempC - axis.minC) / (axis.maxC - axis.minC)) * 100
			expect(curveX(curve, tempC)).toBeCloseTo(expected, 10)
		}
	})
})
