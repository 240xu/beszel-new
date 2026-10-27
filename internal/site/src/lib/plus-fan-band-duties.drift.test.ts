import { readFileSync } from "node:fs"
import { fileURLToPath } from "node:url"
import { describe, expect, test } from "bun:test"
import { CURVE_BAND_DUTIES } from "@/lib/plus-fan-view"

/** `CURVE_BAND_DUTIES` is a second copy of `curveBandDuty` in
 *  agent/plus/curve.go, and a second copy is a promise nobody can check. The Go
 *  map is what Validate consults for L0..L5, so a duty added there but missing
 *  here is an option the operator cannot pick, and one dropped there but kept
 *  here is a curve the daemon refuses after the whole form has been filled in.
 *
 *  This is deliberately separate from the FAN_DUTIES mirror beside it: the two
 *  sets differ (80/85/90/95/100 are writable but not bandable), and collapsing
 *  them into one check would hide exactly the difference that matters. */
const CURVE_GO = fileURLToPath(new URL("../../../../agent/plus/curve.go", import.meta.url))

function goBandDuties(): number[] {
	const source = readFileSync(CURVE_GO, "utf8")
	const block = source.match(/curveBandDuty\s*=\s*map\[int\]bool\{([^}]*)\}/)
	if (!block) throw new Error(`curveBandDuty not found in ${CURVE_GO}: the mirror cannot be checked`)
	return [...block[1].matchAll(/(\d+)\s*:/g)].map((m) => Number(m[1])).sort((a, b) => a - b)
}

describe("the curve band duty set still mirrors the Go one", () => {
	test("every duty the daemon can band on is offered, and nothing else", () => {
		const panel: number[] = [...CURVE_BAND_DUTIES]
		expect(panel.sort((a, b) => a - b)).toEqual(goBandDuties())
	})

	test("the band set is a strict subset of the writable duties", async () => {
		// If these ever became equal, the distinction this whole file exists to
		// protect would have quietly disappeared.
		const { FAN_DUTIES } = await import("@/lib/plus-fan-view")
		for (const duty of CURVE_BAND_DUTIES) expect(FAN_DUTIES).toContain(duty)
		expect(CURVE_BAND_DUTIES.length).toBeLessThan(FAN_DUTIES.length)
	})
})
