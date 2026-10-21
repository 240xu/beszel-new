import { describe, expect, test } from "vitest"
import { screen } from "@testing-library/react"
import { renderWithI18n } from "./render"
import { FormField } from "@/components/ui/form-field"

/**
 * `FormField` is the site's own aria wiring: it is the only thing that puts a label,
 * a hint and an error into relation with the control they describe. A `<Label>` alone
 * does not, so this component is where "the error is announced" is actually decided.
 */
describe("FormField", () => {
	test("ties the label to the control and describes it with the hint and the error", () => {
		renderWithI18n(
			<FormField id="port" label="Port" hint="Between 1 and 65535" error="That port is already taken">
				<input />
			</FormField>
		)

		const control = screen.getByLabelText("Port")
		expect(control.getAttribute("id")).toBe("port")
		// The control is described by both the hint and the error, and the error is
		// marked invalid so a screen reader says the field is bad, not just that it has
		// extra text.
		expect(control.getAttribute("aria-describedby")).toContain("-hint")
		expect(control.getAttribute("aria-describedby")).toContain("-error")
		expect(control.getAttribute("aria-invalid")).toBe("true")
	})

	test("announces the error and leaves the control valid when there is none", () => {
		const { rerender } = renderWithI18n(
			<FormField id="port" label="Port" hint="Between 1 and 65535">
				<input />
			</FormField>
		)

		expect(screen.queryByRole("alert")).toBeNull()
		const control = screen.getByLabelText("Port")
		expect(control.getAttribute("aria-invalid")).toBeNull()
		expect(control.getAttribute("aria-describedby")).toContain("-hint")

		rerender(
			<FormField id="port" label="Port" hint="Between 1 and 65535" error="That port is already taken">
				<input />
			</FormField>
		)
		expect(screen.getByRole("alert").textContent).toBe("That port is already taken")
	})

	test("marks a required control and hands the control props to a render function", () => {
		renderWithI18n(
			<FormField label="Port" required>
				{(control) => <input {...control} />}
			</FormField>
		)

		const control = screen.getByLabelText("Port")
		expect(control.getAttribute("aria-required")).toBe("true")
		// The generated id is stable enough to point a label at.
		expect(control.getAttribute("id")).toMatch(/control$/)
	})

	test("keeps an id the control already owns", () => {
		renderWithI18n(
			<FormField id="port" label="Port">
				<input id="port-input" />
			</FormField>
		)

		// The label must point at the element that actually renders, so the control's
		// own id wins over the field's.
		expect(screen.getByLabelText("Port").getAttribute("id")).toBe("port-input")
	})
})
