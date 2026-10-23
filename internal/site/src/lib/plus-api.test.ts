import { afterAll, describe, expect, mock, test } from "bun:test"
import type { FanCurve, FanState } from "@/types"
import { buildCurvePayload, getFanState, isPendingRestart, saveCurve, setFanLevel, setFanMode } from "@/lib/plus-api"
import { FAN_REJECT_CODES, FanApiError, readFanRefusal } from "@/lib/plus-refusals"

const TOKEN = { value: "" }
mock.module("@/lib/api", () => ({
	pb: {
		authStore: {
			get token() {
				return TOKEN.value
			},
		},
	},
}))

afterAll(() => {
	TOKEN.value = ""
	delete (globalThis as { window?: unknown }).window
})

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

type FetchCall = { url: string; method: string; body: unknown; headers: Record<string, string> }

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
			headers: Object.fromEntries(new Headers(init?.headers).entries()),
		})
		return Promise.resolve(new Response(r.body, { status: r.status }))
	}) as typeof fetch
	return { calls }
}

function ok(obj: unknown): { status: number; body: string } {
	return { status: 200, body: JSON.stringify(obj) }
}

function browser() {
	Object.defineProperty(globalThis, "window", { configurable: true, writable: true, value: {} })
}

const STATE: FanState = {
	pfand_active: false,
	level_read: true,
	level: 70,
	level_label: "70%",
	pkg_temp_read: true,
	pkg_temp_c: 50,
	regress_pending: false,
	curve: CURVE,
}

describe("refusal reading", () => {
	test("takes the identifier from data.code, which is the only place it exists", () => {
		const err = readFanRefusal(
			503,
			JSON.stringify({
				status: 503,
				message: "The package is at or above the emergency edge.",
				data: { code: "package_too_hot" },
			})
		)
		expect(err).toBeInstanceOf(FanApiError)
		expect(err.code).toBe("package_too_hot")
		expect(err.status).toBe(503)
	})

	test("the envelope has no error field, so reading one can never succeed", () => {
		const body = JSON.parse(
			JSON.stringify({ status: 409, message: "The curve daemon owns the fan.", data: { code: "pfand_active" } })
		)
		expect(body.error).toBeUndefined()
		expect((body as { error?: unknown }).error).toBeUndefined()
		expect(readFanRefusal(409, JSON.stringify(body)).code).toBe("pfand_active")
	})

	test("a hot package and an unreadable sensor are different facts on one status", () => {
		const hot = readFanRefusal(
			503,
			JSON.stringify({
				status: 503,
				message: "The package is at or above the emergency edge.",
				data: { code: "package_too_hot" },
			})
		)
		const unknown = readFanRefusal(
			503,
			JSON.stringify({
				status: 503,
				message: "The package temperature could not be read.",
				data: { code: "temperature_unknown" },
			})
		)
		expect(hot.status).toBe(unknown.status)
		expect(hot.code).not.toBe(unknown.code)
		expect(hot.code).toBe("package_too_hot")
		expect(unknown.code).toBe("temperature_unknown")
	})

	test("the error message is not the server's text, so it cannot be a translation key", () => {
		const err = readFanRefusal(
			400,
			JSON.stringify({ status: 400, message: "Confirm is required.", data: { code: "confirm_required" } })
		)
		expect(err.message).toBe("fan request refused")
		expect(err.message).not.toContain("Confirm")
		expect(err.rawMessage).toBe("Confirm is required.")
	})

	test("a refusal the route made before classification carries no code", () => {
		const err = readFanRefusal(400, JSON.stringify({ status: 400, message: "Confirm is required.", data: {} }))
		expect(err.code).toBeUndefined()
		expect(err.status).toBe(400)
	})

	test("an identifier outside the known set is dropped, never bucketed by status", () => {
		const err = readFanRefusal(
			503,
			JSON.stringify({ status: 503, message: "Nope.", data: { code: "thermal_profile_t4" } })
		)
		expect(err.code).toBeUndefined()
		expect(err.rawCode).toBe("thermal_profile_t4")
	})

	test("an unparsable body is still a refusal, not a crash", () => {
		const err = readFanRefusal(502, "<html>bad gateway</html>")
		expect(err.code).toBeUndefined()
		expect(err.status).toBe(502)
	})

	test("every published code reads back as itself", () => {
		// What this list is made of is checked against fanapi in the drift test;
		// what it is for is this: a code that survived the mirror but not the
		// lookup would silently degrade every refusal carrying it.
		for (const code of FAN_REJECT_CODES) {
			const err = readFanRefusal(400, JSON.stringify({ message: "m", data: { code } }))
			expect(err.code).toBe(code)
		}
	})
})

describe("fan write results", () => {
	test("a pending write is never reported as applied, and names its outlet", async () => {
		const { calls } = stubFetch([
			ok({
				applied: false,
				needs_pfand_restart: true,
				status: "pending_restart",
				next_action: "restart_pfand",
				level: 0,
			}),
		])
		const res = await saveCurve(CURVE, true)
		expect(isPendingRestart(res)).toBe(true)
		expect(res.applied).toBe(false)
		expect(res.needs_pfand_restart).toBe(true)
		expect(calls[0].method).toBe("POST")
		expect(calls[0].url).toBe("/api/plus/fan/curve")
	})

	test("an applied write reports no outlet to take", async () => {
		stubFetch([ok({ applied: true, needs_pfand_restart: false, status: "applied", level: 60 })])
		const res = await setFanLevel(60, true)
		expect(isPendingRestart(res)).toBe(false)
		expect(res.applied).toBe(true)
	})
})

describe("request bodies", () => {
	test("a curve carries every field, so the server cannot refuse it as partial", () => {
		const payload = buildCurvePayload(CURVE, true)
		expect(Object.keys(payload).sort()).toEqual([...Object.keys(CURVE), "confirm"].sort())
		expect(payload.t4).toBe(84)
		expect(payload.emerg).toBe(95)
		expect(payload.confirm).toBe(true)
	})

	test("the confirm flag is carried verbatim, including a refusal", () => {
		expect(buildCurvePayload(CURVE, false).confirm).toBe(false)
	})

	test("level and mode send their confirm flag verbatim", async () => {
		const { calls } = stubFetch([ok({ applied: true, needs_pfand_restart: false, status: "applied", level: 25 })])
		await setFanLevel(25, false)
		expect(calls[0].body).toEqual({ level: 25, confirm: false })
		await setFanMode("auto", true)
		expect(calls[1].body).toEqual({ mode: "auto", confirm: true })
		expect(calls[1].url).toBe("/api/plus/fan/mode")
	})
})

describe("fan state", () => {
	test("an unreadable level stays null beside its unreadable flag", async () => {
		const state: FanState = {
			pfand_active: true,
			level_read: false,
			level: null,
			pkg_temp_read: true,
			pkg_temp_c: 48,
			regress_pending: false,
			curve: null,
		}
		stubFetch([ok(state)])
		const got = await getFanState()
		expect(got.level).toBeNull()
		expect(got.level_read).toBe(false)
		expect(got.level_label).toBeUndefined()
		expect(got.regress_pending).toBe(false)
	})

	test("a manual level with no deadline on record still claims no countdown", async () => {
		stubFetch([
			ok({
				pfand_active: false,
				level_read: true,
				level: 70,
				level_label: "70%",
				pkg_temp_read: true,
				pkg_temp_c: 50,
				regress_pending: false,
				curve: CURVE,
			}),
		])
		const got = await getFanState()
		expect(got.level).toBe(70)
		expect(got.regress_pending).toBe(false)
		expect(got.regress_at).toBeUndefined()
	})
})

describe("authorization", () => {
	test("a signed-in session sends the stored token, which the fan routes require", async () => {
		TOKEN.value = "session-token"
		browser()
		const { calls } = stubFetch([ok(STATE)])
		await getFanState()
		expect(calls[0].headers.authorization).toBe("session-token")
		expect(calls[0].headers["content-type"]).toBe("application/json")
	})

	test("no token sends no authorization header and keeps the JSON content type", async () => {
		TOKEN.value = ""
		browser()
		const { calls } = stubFetch([ok(STATE)])
		await getFanState()
		expect(calls[0].headers.authorization).toBeUndefined()
		expect(calls[0].headers["content-type"]).toBe("application/json")
	})

	test("a write carries the token too, so a write route cannot answer 401 behind a read", async () => {
		TOKEN.value = "session-token"
		browser()
		const { calls } = stubFetch([ok({ applied: true, needs_pfand_restart: false, status: "applied", level: 25 })])
		await setFanLevel(25, true)
		expect(calls[0].headers.authorization).toBe("session-token")
	})
})

test("a body fault exposes its reason without inventing a code", () => {
	const err = readFanRefusal(
		400,
		JSON.stringify({ status: 400, message: "invalid body", data: { reason: "unknown_field" } })
	)
	expect(err.code).toBeUndefined()
	expect(err.reason).toBe("unknown_field")
})

test("an unrecognised reason is dropped", () => {
	const err = readFanRefusal(
		400,
		JSON.stringify({ status: 400, message: "invalid body", data: { reason: "made_up" } })
	)
	expect(err.reason).toBeUndefined()
})
