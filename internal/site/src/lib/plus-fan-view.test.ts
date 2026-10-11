import { describe, expect, test } from "bun:test"
import type { FanCurve, FanState } from "@/types"
import { FAN_REJECT_CODES, readFanRefusal } from "@/lib/plus-refusals"
import {
	confirmed,
	curveWrite,
	FAN_DUTIES,
	fanLevelLabel,
	fanRefusal,
	fanRegressView,
	fanWriteOutcome,
	issueFanWrite,
	levelWrite,
	modeWrite,
	type CurveSaveResult,
} from "@/lib/plus-fan-view"

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

	test("zero reads as the handover, because that is what the firmware calls it", () => {
		expect(fanLevelLabel(state({ level: 0, level_label: "auto" }))).toBe("auto")
		expect(fanLevelLabel(state({ level: 0, level_label: "auto" }))).not.toMatch(/[\d]/)
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

	test("the wording is pinned once here, and the wording rule holds under any re-spelling", () => {
		expect(fanWriteOutcome(APPLIED).text).toBe("In force — the fan is running this now.")
		expect(fanWriteOutcome(PENDING).text).toBe("Written to disk; the curve daemon has not picked it up.")
		// A write nothing is running must not borrow any wording that says it is
		// running. This is the assertion that survives a translator rewriting the
		// other half of the table.
		expect(fanWriteOutcome(PENDING).text).not.toMatch(/in force|saved|applied|running|生效|已保存/i)
		expect(fanWriteOutcome(APPLIED).text).not.toBe(fanWriteOutcome(PENDING).text)
	})
})

describe("refusals are read by code, never by status", () => {
	test("a hot package and a dead sensor are different answers on one status", () => {
		const hot = fanRefusal(readFanRefusal(503, JSON.stringify(HOT)))
		const sensor = fanRefusal(readFanRefusal(503, JSON.stringify(SENSOR)))
		expect(hot.key).not.toBe(sensor.key)
		expect(hot.text).not.toBe(sensor.text)
	})

	test("one code under two statuses is one answer, so the status cannot be doing the work", () => {
		const a = fanRefusal(readFanRefusal(400, JSON.stringify(HOT)))
		const b = fanRefusal(readFanRefusal(503, JSON.stringify(HOT)))
		expect(a.key).toBe(b.key)
		expect(a.text).toBe(b.text)
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

	test("every classified refusal the server can raise reaches a text of its own", () => {
		const texts = FAN_REJECT_CODES.map(
			(code) => fanRefusal(readFanRefusal(500, JSON.stringify({ data: { code } }))).text
		)
		expect(texts.some((text) => text.length === 0)).toBe(false)
		expect(new Set(texts).size).toBe(FAN_REJECT_CODES.length)
	})

	test("the three ways a write fails after reaching the device are told apart", () => {
		const code = (c: string) => fanRefusal(readFanRefusal(500, JSON.stringify({ data: { code: c } }))).key
		expect(new Set([code("rollback_failed"), code("write_unconfirmed"), code("write_failed")]).size).toBe(3)
	})

	test("the hot and the sensor answer are pinned once each, with the reverse assertion", () => {
		// One reader-visible string pinned in one place; the invariant that
		// matters is the second line, which no re-spelling can satisfy.
		expect(fanRefusal(readFanRefusal(503, JSON.stringify(HOT))).text).toBe(
			"The package is at or above the emergency edge in the curve, so no duty was written."
		)
		expect(fanRefusal(readFanRefusal(503, JSON.stringify(SENSOR))).text).toBe(
			"The package temperature could not be read, so no duty was written and the reading cannot be trusted."
		)
		expect(fanRefusal(readFanRefusal(503, JSON.stringify(SENSOR))).text).not.toMatch(/too hot|at or above/i)
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
		expect(FAN_DUTIES).toContain(0)
		expect(FAN_DUTIES).toContain(100)
		expect(FAN_DUTIES.length).toBe(12)
	})

	test("a duty the firmware refuses is not offered", () => {
		expect(FAN_DUTIES).not.toContain(37)
		expect(FAN_DUTIES).not.toContain(1)
	})
})
