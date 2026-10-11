/** Bun's runner has no DOM, but `src/lib/stores.ts` reads `localStorage` at module scope,
 *  so every suite importing it aborts between tests and reports zero assertions. */
const entries = new Map<string, string>()

Object.defineProperty(globalThis, "localStorage", {
	configurable: true,
	writable: true,
	value: {
		getItem: (key: string) => entries.get(key) ?? null,
		setItem: (key: string, value: string) => void entries.set(key, String(value)),
		removeItem: (key: string) => void entries.delete(key),
		clear: () => entries.clear(),
		key: (index: number) => [...entries.keys()][index] ?? null,
		get length() {
			return entries.size
		},
	} satisfies Storage,
})
