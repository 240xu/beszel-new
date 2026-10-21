import type { ReactElement } from "react"
import { render, type RenderOptions, type RenderResult } from "@testing-library/react"
import { I18nProvider } from "@lingui/react"
import { i18n } from "@lingui/core"

/**
 * The application mounts its tree inside `I18nProvider` (src/main.tsx), and
 * `@lingui/react`'s `useLingui` throws without one. Rendering inside the same
 * provider is what makes the components under test behave the way they do on a
 * screen instead of the way they do in a vacuum.
 */
export function renderWithI18n(ui: ReactElement, options?: Omit<RenderOptions, "wrapper">): RenderResult {
	return render(<I18nProvider i18n={i18n}>{ui}</I18nProvider>, options)
}
