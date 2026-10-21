import { mergeConfig, defineConfig, type UserConfig } from "vitest/config"
import viteConfig from "./vite.config"

/**
 * DOM component suite.
 *
 * This suite exists because the site's components are written against the Lingui
 * *macro* (`@lingui/core/macro`, `@lingui/react/macro`), which is a compile-time
 * transform, not a runtime function. Bun's test runner has no way to apply it:
 * importing a macro-using module throws "The macro you imported from
 * "@lingui/core/macro" ... is being executed outside the context of compilation".
 * That is the whole reason `src/lib/plus-fan-tab.guard.test.ts` greps the *source
 * text* of the panel instead of rendering it -- a grep proves the file contains a
 * string, never that the string reaches a screen.
 *
 * Vitest runs the same Vite pipeline the production build uses, and `vite.config.ts`
 * already wires `@lingui/swc-plugin` into `@vitejs/plugin-react-swc`. Reusing that
 * config is the entire trick: the code under test is transformed exactly as it is
 * for `vite build`, so the macro compiles away and the component really renders.
 *
 * The pure-logic suites stay on bun (`bun test --preload ./tests/bun-preload.ts`);
 * this config only owns `tests/dom/**`, so nothing that is green today has to move.
 */
export default mergeConfig(
	viteConfig as UserConfig,
	defineConfig({
		test: {
			environment: "happy-dom",
			globals: false,
			include: ["tests/dom/**/*.test-dom.tsx"],
			setupFiles: ["./tests/vitest-setup.ts"],
			restoreMocks: true,
			clearMocks: true,
		},
	})
)
