import { readFileSync } from "node:fs"
import { fileURLToPath } from "node:url"
import { describe, expect, test } from "bun:test"
import { FAN_REJECT_CODES } from "@/lib/plus-refusals"

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
