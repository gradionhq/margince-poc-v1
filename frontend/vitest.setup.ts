// Node ≥23 ships its own global Web Storage: a `localStorage` getter that
// yields undefined unless the process was started with --localstorage-file.
// Because the key already exists on the Node global, vitest's populateGlobal
// keeps it instead of copying jsdom's Storage onto the test global (only keys
// on vitest's own KEYS allowlist override an existing global, and the storage
// keys are not on it). Tests in the jsdom environment then see undefined —
// while a runtime without the Node global (Node 22, today's CI) gets jsdom's
// working Storage and passes. Rebind the real jsdom Storage whenever the test
// global disagrees with the jsdom window, so both runtimes behave like CI.
const jsdomHost: { jsdom?: { window?: Record<string, unknown> } } = globalThis;
const jsdomWindow = jsdomHost.jsdom?.window;

if (jsdomWindow) {
	for (const key of ["localStorage", "sessionStorage"]) {
		const testGlobal: Record<string, unknown> = globalThis;
		if (testGlobal[key] !== jsdomWindow[key]) {
			Object.defineProperty(globalThis, key, {
				get: () => jsdomWindow[key],
				configurable: true,
			});
		}
	}
}
