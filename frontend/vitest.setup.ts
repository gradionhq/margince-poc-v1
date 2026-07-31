import { beforeEach, vi } from "vitest";

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

// The two DOM stubs below are guarded on there BEING a DOM: this setup file runs
// for every suite, and most of them are node-environment (jsdom is opted into
// per file with `@vitest-environment jsdom`). Unguarded, they threw
// "window is not defined" at setup time and took 20 unrelated suites with them.
if (typeof window !== "undefined") {
	// jsdom ships no matchMedia, and every motion-aware component asks it for
	// prefers-reduced-motion on first render. Default to "no preference" so the
	// animated path is what the tests exercise; a test that wants the reduced path
	// overrides this per case.
	if (!window.matchMedia) {
		window.matchMedia = ((query: string) => ({
			matches: false,
			media: query,
			onchange: null,
			addEventListener: () => {},
			removeEventListener: () => {},
			addListener: () => {},
			removeListener: () => {},
			dispatchEvent: () => false,
		})) as typeof window.matchMedia;
	}

	// The Margince Core draws its liquid on a WebGL canvas; jsdom has no GL
	// context. Returning null is the same signal a browser without WebGL gives,
	// and the Core has a REQUIRED CSS rendering of every state for exactly that
	// case (WDS-CORE-3) — so this stub is what makes the suite exercise the
	// fallback rung of the ladder rather than the shader.
	//
	// Assigned UNCONDITIONALLY, and that is the fix rather than the style choice:
	// jsdom DOES define getContext, as a method that throws "Not implemented". An
	// `if (!…)` guard therefore never fires, and every render of a screen carrying
	// the Core prints a twelve-line jsdom stack to stderr — noise that trains a
	// reader to ignore test output, which is where the next real error hides.
	//
	// Re-applied before EVERY case, not once at setup: a suite whose `afterEach`
	// calls `vi.restoreAllMocks()` (auth.test.tsx does) hands getContext back to
	// jsdom after its first case, and every later render brings the stack trace
	// back. The install at setup time covers a render that happens while a test
	// file is still being imported, before any hook has run.
	const stubCanvasContext = () => {
		vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockReturnValue(null);
	};

	stubCanvasContext();
	beforeEach(stubCanvasContext);
}
