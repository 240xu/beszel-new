import { afterEach } from "vitest"
import { i18n } from "@lingui/core"
import { cleanup } from "@testing-library/react"

/**
 * The DOM suite's shared setup.
 *
 * Two jobs, both of them things happy-dom does not do on its own.
 */

// ---------------------------------------------------------------------------
// 1. A locale, so the macro-compiled calls have somewhere to land.
//
// `i18n._()` refuses to run before a locale is active, and the components under
// test call it for every piece of text they render. Activating the source locale
// is what the application itself does before it mounts (src/main.tsx), so this is
// the same state a real screen is in -- not a stub.
//
// The compiled English catalog is a build artifact (`lingui compile`), so it is
// loaded when it is there and skipped when it is not: the macro already carries
// the English text of every message, so the words on screen are the same either
// way. The gate compiles the catalogs before it runs the suites, so in CI the
// catalog is always present.
// ---------------------------------------------------------------------------
try {
	const { messages } = await import("@/locales/en/en")
	i18n.load("en", messages)
} catch {
	// Not compiled. The English text still renders.
}
i18n.activate("en")

// ---------------------------------------------------------------------------
// 2. The DOM gaps happy-dom leaves.
//
// happy-dom implements the DOM but not layout, and not the observer APIs the
// Radix primitives the fan panel is built on reach for. Every shim below is the
// minimum a component needs to mount; none of them decides anything the
// component is being tested for. The one that *could* decide something -- the
// slider's `getBoundingClientRect`, which happy-dom reports as all zeroes
// because it does no layout -- is stubbed per test instead of here.
// ---------------------------------------------------------------------------
if (!globalThis.ResizeObserver) {
	globalThis.ResizeObserver = class {
		observe() {}
		unobserve() {}
		disconnect() {}
	} as unknown as typeof globalThis.ResizeObserver
}

// Radix reads the pointer capture API to keep a drag anchored.
if (!Element.prototype.hasPointerCapture) {
	Element.prototype.hasPointerCapture = () => false
}
if (!Element.prototype.setPointerCapture) {
	Element.prototype.setPointerCapture = () => {}
}
if (!Element.prototype.releasePointerCapture) {
	Element.prototype.releasePointerCapture = () => {}
}

// Radix scrolls the highlighted item into view when a list opens.
if (!Element.prototype.scrollIntoView) {
	Element.prototype.scrollIntoView = () => {}
}

afterEach(() => {
	cleanup()
})
