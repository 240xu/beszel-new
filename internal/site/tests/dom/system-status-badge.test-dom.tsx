import { describe, expect, test } from "vitest"
import { screen } from "@testing-library/react"
import { renderWithI18n } from "./render"
import { SystemStatusBadge } from "@/components/system-status"
import { SystemStatus } from "@/lib/enums"

/**
 * The status chip is the one place the whole table reports a system's state, and its
 * label comes from `@lingui/react/macro` -- a compile-time transform. That is exactly
 * the kind of component a source-grep test cannot check: the macro decides both the
 * text and the catalog id, and only a render proves the text arrived.
 */
describe("SystemStatusBadge", () => {
	test("names the state in words, not only in colour", () => {
		renderWithI18n(<SystemStatusBadge status={SystemStatus.Up} />)
		expect(screen.getByText("Up")).toBeTruthy()
	})

	test("says Down for a system that is down", () => {
		renderWithI18n(<SystemStatusBadge status={SystemStatus.Down} />)
		expect(screen.getByText("Down")).toBeTruthy()
	})

	test("says Paused for a paused system", () => {
		renderWithI18n(<SystemStatusBadge status={SystemStatus.Paused} />)
		expect(screen.getByText("Paused")).toBeTruthy()
	})

	test("carries the dot as decoration, so it is not read out twice", () => {
		const { container } = renderWithI18n(<SystemStatusBadge status={SystemStatus.Up} />)
		const dot = container.querySelector("span[aria-hidden='true']")
		expect(dot).not.toBeNull()
	})
})
