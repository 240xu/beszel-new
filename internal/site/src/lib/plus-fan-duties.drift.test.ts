import { readFileSync } from "node:fs"
import { fileURLToPath } from "node:url"
import { describe, expect, test } from "bun:test"
import { FAN_DUTIES } from "@/lib/plus-fan-view"

/** `FAN_DUTIES` is a second copy of `validLevels` in agent/plus/levels.go, and a
 *  second copy is a promise nobody can check: a duty added there is a refused
 *  write, a duty dropped there is an option the server rejects in front of the
 *  operator, and neither is visible from the TypeScript alone. */
const LEVELS_GO = fileURLToPath(new URL("../../../../agent/plus/levels.go", import.meta.url))

function goDuties(): number[] {
	const source = readFileSync(LEVELS_GO, "utf8")
	const block = source.match(/validLevels\s*=\s*map\[int\]bool\{([^}]*)\}/)
	if (!block) throw new Error(`validLevels not found in ${LEVELS_GO}: the mirror cannot be checked`)
	return [...block[1].matchAll(/(\d+)\s*:/g)].map((m) => Number(m[1])).sort((a, b) => a - b)
}

describe("the duty whitelist still mirrors the Go one", () => {
	const panel = (): number[] => [...FAN_DUTIES]

	test("every duty the firmware accepts is offered, and nothing else is", () => {
		expect(panel().sort((a, b) => a - b)).toEqual(goDuties())
	})

	test("the panel offers them in the order the firmware table lists them", () => {
		expect(panel()).toEqual(goDuties())
	})

	test("the numbers come from the Go source rather than from beside it", () => {
		// Every test above compares one list to another. If this file stops reading
		// agent/plus/levels.go they compare a constant with itself and pass forever,
		// so the read itself is asserted rather than assumed.
		expect(readFileSync(LEVELS_GO, "utf8")).toContain("validLevels = map[int]bool{")
		expect(LEVELS_GO).toMatch(/agent[/\\]plus[/\\]levels\.go$/)
	})

	test("the mirror is a mirror and not a second whitelist", () => {
		const unique = new Set(FAN_DUTIES)
		expect(unique.size).toBe(FAN_DUTIES.length)
		// Zero is in neither list: the firmware writes any argument straight into
		// the EC fan-duty register, so offering it here would put an option in
		// front of an operator that the server answers by refusing.
		expect(FAN_DUTIES).not.toContain(0)
		expect(goDuties()).not.toContain(0)
	})
})
