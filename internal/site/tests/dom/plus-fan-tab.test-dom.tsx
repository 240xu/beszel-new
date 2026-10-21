import { beforeEach, describe, expect, test, vi } from "vitest"
import { fireEvent, screen } from "@testing-library/react"
import { renderWithI18n } from "./render"
import { PlusFanTab } from "@/components/routes/system/plus-fan-tab"
import { FAN_DUTIES } from "@/lib/plus-fan-view"
import type { FanState } from "@/types"

/**
 * The fan panel, rendered.
 *
 * Everything here used to be unprovable: `src/lib/plus-fan-tab.guard.test.ts` reads the
 * *source text* of `plus-fan-tab.tsx` and asserts that a string is present in the file.
 * That proves the file contains a sentence, never that the sentence reaches an operator.
 * These tests drive the real component through the real macro transform, so a claim
 * about what the panel says has to survive the DOM.
 *
 * The panel is reached through `PlusFanTab` rather than through an exported
 * `DutyLadder`, on purpose: the ladder is only reachable through the mode switch and
 * the read state, so the test also proves the panel *wires* the ladder, which is what
 * the source-grep suite could not do.
 */

const { getFanState, setFanLevel } = vi.hoisted(() => ({
	getFanState: vi.fn(),
	setFanLevel: vi.fn(),
}))

vi.mock("@/lib/plus-api", async (importOriginal) => ({
	...(await importOriginal<typeof import("@/lib/plus-api")>()),
	getFanState,
	setFanLevel,
}))

const LOW = FAN_DUTIES[0]
const HIGH = FAN_DUTIES[FAN_DUTIES.length - 1]

const fanState = (over: Partial<FanState> = {}): FanState => ({
	pfand_active: false,
	level_read: true,
	level: 60,
	level_label: "60%",
	pkg_temp_read: true,
	pkg_temp_c: 45,
	regress_pending: false,
	curve: null,
	...over,
})

/** happy-dom does no layout, so the slider's box is all zeroes and every pointer
 *  would land on the same duty. The panel reads the box off the event target, so the
 *  stub has to be on the element itself, not on the prototype. */
const stubBox = (element: Element, left = 0, width = 200) => {
	element.getBoundingClientRect = () =>
		({
			left,
			right: left + width,
			width,
			top: 0,
			bottom: 48,
			height: 48,
			x: left,
			y: 0,
			toJSON: () => ({}),
		}) as DOMRect
}

const renderPanel = (over: Partial<FanState> = {}) => {
	getFanState.mockResolvedValue(fanState(over))
	renderWithI18n(<PlusFanTab grid={false} dataEmpty={false} />)
	return screen.findByRole("slider")
}

beforeEach(() => {
	getFanState.mockReset()
	setFanLevel.mockReset()
	document.documentElement.dir = "ltr"
})

describe("the duty control", () => {
	test("is a slider that reports its range, its value and its value as text", async () => {
		const slider = await renderPanel()

		// The ARIA contract a screen reader needs: the range, the current value, and
		// the same value in the units an operator reads.
		expect(slider.getAttribute("aria-valuemin")).toBe(String(LOW))
		expect(slider.getAttribute("aria-valuemax")).toBe(String(HIGH))
		expect(slider.getAttribute("aria-valuenow")).toBe("60")
		expect(slider.getAttribute("aria-valuetext")).toBe("60%")
		// Focusable, because a slider that cannot be reached by keyboard is not a slider.
		expect(slider.getAttribute("tabindex")).toBe("0")
		expect(slider.getAttribute("aria-disabled")).toBe("false")
	})

	test("moves one duty per arrow key and jumps on Home, End, PageUp and PageDown", async () => {
		const slider = await renderPanel()
		slider.focus()

		fireEvent.keyDown(slider, { key: "ArrowRight" })
		expect(slider.getAttribute("aria-valuenow")).toBe("70")
		fireEvent.keyDown(slider, { key: "ArrowUp" })
		expect(slider.getAttribute("aria-valuenow")).toBe("80")
		fireEvent.keyDown(slider, { key: "ArrowLeft" })
		expect(slider.getAttribute("aria-valuenow")).toBe("70")
		fireEvent.keyDown(slider, { key: "ArrowDown" })
		expect(slider.getAttribute("aria-valuenow")).toBe("60")

		fireEvent.keyDown(slider, { key: "Home" })
		expect(slider.getAttribute("aria-valuenow")).toBe(String(LOW))
		fireEvent.keyDown(slider, { key: "End" })
		expect(slider.getAttribute("aria-valuenow")).toBe(String(HIGH))

		// Page moves by two duties and stops at the ends of the table.
		fireEvent.keyDown(slider, { key: "PageDown" })
		expect(slider.getAttribute("aria-valuenow")).toBe("90")
		fireEvent.keyDown(slider, { key: "PageUp" })
		expect(slider.getAttribute("aria-valuenow")).toBe(String(HIGH))
	})

	test("takes the duty nearest the pointer", async () => {
		const slider = await renderPanel()
		stubBox(slider)

		fireEvent.pointerDown(slider, { clientX: 100 })
		expect(slider.getAttribute("aria-valuenow")).toBe("50")

		fireEvent.pointerDown(slider, { clientX: 0 })
		expect(slider.getAttribute("aria-valuenow")).toBe(String(LOW))

		fireEvent.pointerDown(slider, { clientX: 200 })
		expect(slider.getAttribute("aria-valuenow")).toBe(String(HIGH))
	})

	test("is refused while the curve daemon owns the fan, and refuses the keyboard too", async () => {
		// `pfand_active` means the daemon is driving the fan from the curve. A duty
		// cannot be set by hand while that holds, so the control must say so and must
		// not move even if a key arrives.
		const slider = await renderPanel({ pfand_active: true })

		expect(slider.getAttribute("aria-disabled")).toBe("true")
		expect(slider.getAttribute("tabindex")).toBe("-1")

		fireEvent.keyDown(slider, { key: "ArrowRight" })
		expect(slider.getAttribute("aria-valuenow")).toBe("60")
	})

	test("is locked while a write is in flight", async () => {
		// A duty that is being written must not be moved again: two writes racing for
		// one register is the failure the confirm dialog exists to prevent.
		// A write that never answers, so the panel stays busy for the rest of the test.
		const neverAnswers = async () => {
			await new Promise(() => {})
		}
		setFanLevel.mockImplementation(neverAnswers)
		const slider = await renderPanel()

		fireEvent.click(screen.getByRole("button", { name: "Write this duty to the firmware" }))
		fireEvent.click(await screen.findByRole("button", { name: "Confirm" }))

		expect(slider.getAttribute("aria-disabled")).toBe("true")
		fireEvent.keyDown(slider, { key: "ArrowRight" })
		expect(slider.getAttribute("aria-valuenow")).toBe("60")
	})
})

describe("the duty control in a right-to-left document", () => {
	// `main.tsx` sets `document.documentElement.dir` from the locale's direction and
	// wraps the tree in Radix's `DirectionProvider`, so this is the same signal the
	// application gives the page. `ar`, `fa` and `he` are the locales that set it
	// (src/lib/i18n.ts).
	const rtl = () => {
		document.documentElement.dir = "rtl"
	}

	test("mirrors the ladder, so zero percent is at the inline start", async () => {
		rtl()
		const slider = await renderPanel()

		// The markers are placed with a logical property, so the ladder itself mirrors
		// and stays consistent with the scale printed under it.
		const markers = [...slider.querySelectorAll<HTMLElement>("[style]")]
		expect(markers.length).toBeGreaterThan(0)
		for (const marker of markers) {
			expect(marker.getAttribute("style")).toContain("inset-inline-start")
		}
	})

	// The pointer test below was a known failure (direction-blind ladder) and is
	// now a normal assertion.
	//
	// The keyboard test asserts the WAI-ARIA APG Slider Pattern rule: Right
	// increases the value and is NOT mirrored in RTL.
	test("takes the duty at the end the pointer is actually over", async () => {
		rtl()
		const slider = await renderPanel()
		stubBox(slider)

		// The left edge of the ladder is 100 percent in a right-to-left document.
		fireEvent.pointerDown(slider, { clientX: 0 })
		expect(slider.getAttribute("aria-valuenow")).toBe(String(HIGH))

		// And the right edge is zero percent.
		fireEvent.pointerDown(slider, { clientX: 200 })
		expect(slider.getAttribute("aria-valuenow")).toBe(String(LOW))
	})

	test("keeps the APG keyboard direction in RTL (Right = increase)", async () => {
		rtl()
		const slider = await renderPanel()
		slider.focus()

		fireEvent.keyDown(slider, { key: "ArrowRight" })
		expect(slider.getAttribute("aria-valuenow")).toBe("70")
	})
})
