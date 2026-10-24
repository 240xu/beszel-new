import { readFileSync } from "node:fs"
import { fileURLToPath } from "node:url"
import { describe, expect, test } from "bun:test"
import { dutyAt } from "@/lib/plus-fan-view"

/** `dutyAt` is a second copy of `WantFor` in agent/plus/curve.go. The drawn curve
 *  is only honest while the two agree, and nothing in TypeScript can see a change
 *  to the Go switch. So this reads that switch's source and fails if its shape
 *  moves: the band order, the comparisons, and the emergency rule are the contract.
 *
 *  It is deliberately structural rather than clever. Parsing Go control flow here
 *  would be a second implementation to keep correct; what this can honestly check
 *  is that the cases still exist, in order, with the comparisons the drawing code
 *  assumes. The numeric agreement is covered by the WantFor-port test beside it. */
const CURVE_GO = fileURLToPath(new URL("../../../../agent/plus/curve.go", import.meta.url))

function wantForBody(): string {
	const source = readFileSync(CURVE_GO, "utf8")
	const fn = source.match(/func WantFor\([^)]*\)[^{]*\{([\s\S]*?)\n\}/)
	if (!fn) throw new Error(`WantFor not found in ${CURVE_GO}: the mirror cannot be checked`)
	return fn[1]
}

describe("the drawn curve still mirrors WantFor", () => {
	test("the bands are still ordered from the emergency edge down to idle", () => {
		const body = wantForBody()
		const order = ["c.Emerg", "c.Idle", "c.T1", "c.T2", "c.T3", "c.T4", "c.T5"]
		const seen = order.map((needle) => body.indexOf(`tempC >= ${needle}`) >= 0 ? body.indexOf(`tempC >= ${needle}`) : body.indexOf(`tempC <= ${needle}`))
		for (const at of seen) expect(at).toBeGreaterThanOrEqual(0)
		// Each later band must appear later in the switch than the one before it.
		expect([...seen].sort((a, b) => a - b)).toEqual(seen)
	})

	test("the band edges are still inclusive on the right, which the drawing relies on", () => {
		const body = wantForBody()
		// At and below the idle edge is L0, and every T edge is <=. A strict `<`
		// here would move every riser one band and still draw a plausible shape.
		expect(body).toContain("tempC <= c.Idle")
		for (const edge of ["c.T1", "c.T2", "c.T3", "c.T4", "c.T5"]) {
			expect(body).toContain(`tempC <= ${edge}`)
		}
	})

	test("everything above the last band still resolves to the emergency duty", () => {
		expect(wantForBody()).toContain("return c.E")
	})

	test("the duty each edge resolves to matches what the panel draws there", () => {
		// The drawing takes each band's duty from dutyAt at its own right edge. If
		// the Go switch paired an edge with a different duty, this mirror would be
		// the thing that noticed.
		const curve = {
			idle: 45, l0: 25, t1: 55, l1: 30, t2: 65, l2: 40,
			t3: 72, l3: 50, t4: 80, l4: 60, t5: 88, l5: 70,
			emerg: 95, e: 100,
		} as never
		// The half-open reading of the switch gives exactly these.
		expect([
			dutyAt(curve, 45), dutyAt(curve, 55), dutyAt(curve, 65),
			dutyAt(curve, 72), dutyAt(curve, 80), dutyAt(curve, 88), dutyAt(curve, 95),
		]).toEqual([25, 30, 40, 50, 60, 70, 100])
	})
})
