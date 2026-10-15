import { readFileSync } from "node:fs"
import { fileURLToPath } from "node:url"
import { describe, expect, test } from "bun:test"
import { FAN_DECLINE_CODES, FAN_REJECT_CODES } from "@/lib/plus-refusals"

/** `FAN_REJECT_CODES` mirrors the closed set fanapi publishes. Nobody checks a mirror and
 *  it becomes a promise nobody can keep: a code added to the guard is refused with a reason
 *  this drops, and a code added here is a branch on a refusal that never happens. */
const WIRE_GO = fileURLToPath(new URL("../../../../internal/fanapi/wire.go", import.meta.url))

/** Raises rather than yielding nothing: an empty parse facing an empty list is the failure
 *  this guard exists to catch, wearing the shape of a pass. */
function blockOf(source: string, what: string, re: RegExp): string {
	const block = source.match(re)
	if (!block) throw new Error(`${what} is unreadable in ${WIRE_GO}: the mirror cannot be checked`)
	return block[1]
}

function goRefusalCodes(source: string): string[] {
	const rows = /Code:\s*"([^"]*)"/g
	const codes = (body: string): string[] => [...body.matchAll(rows)].map((m) => m[1])
	const classified = blockOf(source, "classifications", /classifications\s*=\s*\[\]classification\{([\s\S]*?)\n\}/)
	// The catch-all sits outside the table, and Classify falls back to it, so it is
	// published whether or not a row names it.
	const catchAll = blockOf(source, "InternalFault", /InternalFault\s*=\s*Fault\{([\s\S]*?)\n\}/)
	const all = [...codes(classified), ...codes(catchAll)]
	if (all.length === 0) throw new Error("no Code literals parsed: the mirror cannot be checked")
	return all
}

const wire = readFileSync(WIRE_GO, "utf8")

describe("the panel's closed set still mirrors the guard's", () => {
	test("every code the guard publishes is one the panel can branch on", () => {
		expect(goRefusalCodes(wire)).toEqual([...FAN_REJECT_CODES])
	})

	// The test above is one list against another, so a reader that ignored its input and
	// returned this list would pass it forever. Handing it an edited source shows it moves.
	test("the reader follows its input, so it cannot be a fixed answer", () => {
		const edited = wire.replace('Code: "curve_rejected"', 'Code: "mutant_only_code"')
		expect(edited).not.toBe(wire)
		const after = goRefusalCodes(edited)
		expect(after).not.toEqual(goRefusalCodes(wire))
		expect(after).toContain("mutant_only_code")
	})

	test("the mirror is read from the guard's source and not from beside it", () => {
		expect(WIRE_GO).toMatch(/internal[/\\]fanapi[/\\]wire\.go$/)
		expect(wire).toContain("classifications = []classification{")
	})
})

const GUARD_GO = fileURLToPath(new URL("../../../../internal/fanapi/guard.go", import.meta.url))

/** The regress decline codes: the same mirror discipline as the refusal codes, read from
 *  the guard's own closed set. A decline added to the guard would otherwise be audited on
 *  a code this list drops, so the trail reports a regress declined with a reason the panel
 *  cannot branch on. */
function goDeclineCodes(source: string): string[] {
	const codes = [...source.matchAll(/Decline\w+\s*=\s*"([^"]*)"/g)].map((m) => m[1] ?? "")
	if (codes.length === 0) throw new Error("no Decline literals parsed: the decline mirror cannot be checked")
	return codes
}

describe("the panel's decline set still mirrors the guard's", () => {
	const guard = readFileSync(GUARD_GO, "utf8")

	test("every regress decline the guard can name is one the panel keeps", () => {
		expect(goDeclineCodes(guard)).toEqual([...FAN_DECLINE_CODES])
	})

	// Handing it an edited source shows it moves, so it cannot be a fixed answer that
	// ignores the file and would pass forever.
	test("the decline reader follows its input, so it cannot be a fixed answer", () => {
		const edited = guard.replace(
			'DeclineAtEmergency = "regress_at_emergency_edge"',
			'DeclineAtEmergency = "regress_mutant_only"'
		)
		expect(edited).not.toBe(guard)
		expect(goDeclineCodes(edited)).toContain("regress_mutant_only")
	})
})
