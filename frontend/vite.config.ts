import { existsSync } from "node:fs";
import { join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";

// The composition alias — the runtime half of the two-lane type story whose
// compile-time half is tsconfig.app.json / tsconfig.composed.json.
//
// Default (vanilla): the committed empty-tree registry under src/composition,
// so a fresh clone builds and `pnpm dev` runs with no generator having been
// invoked. Composed: MARGINCE_COMPOSITION_FRONTEND names the generated
// directory, exactly as GOWORK names the generated workspace for the Go lane.
const frontendRoot = fileURLToPath(new URL(".", import.meta.url));
const composedComposition = process.env.MARGINCE_COMPOSITION_FRONTEND;
const compositionDir = composedComposition
  ? resolve(composedComposition)
  : join(frontendRoot, "src", "composition");
// Fail LOUDLY when a lane asks for the composed registry and the generation
// step did not run. Falling back to the vanilla stub would build a bundle that
// silently routes no extension at all — the failure would surface as a missing
// screen in a deployed image, long after the build that caused it went green.
for (const artifact of ["extensions.gen.ts", "extscreens.gen.ts"]) {
  if (composedComposition && !existsSync(join(compositionDir, artifact))) {
    throw new Error(
      `MARGINCE_COMPOSITION_FRONTEND=${composedComposition} holds no ${artifact} — run 'make -C backend composition' before building the composed frontend lane`,
    );
  }
}

// The frontend talks only to the /v1 contract surface (architecture/01:
// frontend depends on the generated contract, never Go internals). In dev,
// Vite proxies /v1 to the local api role; the workspace header comes from
// the app, the session cookie from the browser (localhost is a secure-context,
// so the Secure session cookie survives over plain http — no TLS needed).
// `make dev` (scripts/dev.sh) serves this app on :8080 — the ONE port a human
// opens — and runs the api behind it, passing BACKEND_PORT so the proxy
// follows. With no BACKEND_PORT the proxy falls back to the base api port.
const backendPort = process.env.BACKEND_PORT ?? "18080";
const proxyTarget = `http://localhost:${backendPort}`;

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: {
      "@composition/extensions": join(compositionDir, "extensions.gen.ts"),
      // The unit SCREENS, selected by the same switch and for the same reason.
      // Both sides are GENERATED now, and the composed one imports each unit's
      // own workspace package: a screen calls routes only a composed
      // installation serves, so the vanilla bundle resolves an empty registry
      // and never pulls a unit into the graph at all.
      "@composition/screens": join(compositionDir, "extscreens.gen.ts"),
    },
    // ONE React, whichever copy a unit package's own dependency tree would
    // otherwise resolve. React's hook dispatcher is per-instance: a second copy
    // in the bundle gives a unit's screen a dispatcher its host never rendered
    // through, and every hook it calls throws with a message naming neither the
    // unit nor the cause. The package rule refuses react as a DIRECT dependency
    // (gen-composition's collectUnitFrontend); this is the half that holds when
    // one of a unit's own dependencies pulls it in transitively.
    dedupe: ["react", "react-dom"],
  },
  server: {
    // build/composition/ sits OUTSIDE the Vite root (frontend/), and Vite's
    // dev server refuses to serve a file it cannot prove is inside the
    // workspace. Listing the resolved directory keeps `pnpm dev` working in
    // the composed lane; in the vanilla lane it names src/composition, which
    // was already allowed, so the entry is inert rather than a widening.
    fs: {
      allow: [
        frontendRoot,
        compositionDir,
        // The unit trees. A composed dev server resolves a screen out of
        // extensions/<name>/frontend, which is outside the Vite root, and
        // without this `pnpm dev` serves the registry and then 403s the very
        // module it imports.
        join(frontendRoot, "..", "extensions"),
      ],
    },
    // Everything the api owns is reachable through this origin, so
    // `curl localhost:8080/v1/...` and the operational probes keep working
    // against the port a human already has open — the app's port IS the
    // product's port, and the api's own is an implementation detail.
    proxy: {
      "/v1": { target: proxyTarget, changeOrigin: false, secure: false },
      "/readyz": { target: proxyTarget, changeOrigin: false, secure: false },
      "/healthz": { target: proxyTarget, changeOrigin: false, secure: false },
      "/metrics": { target: proxyTarget, changeOrigin: false, secure: false },
      // The MCP connector's three route groups proxy together, never
      // separately: RFC 9728 discovery is a chain rooted at the resource
      // server's 401, so the transport (/mcp), the authorization server
      // (/oauth) and the discovery documents (/.well-known) must all answer
      // on the SAME origin a client typed, or the handshake cannot resolve.
      "/mcp": { target: proxyTarget, changeOrigin: false, secure: false },
      "/oauth": { target: proxyTarget, changeOrigin: false, secure: false },
      "/.well-known": { target: proxyTarget, changeOrigin: false, secure: false },
    },
  },
  test: {
    environment: "node",
    // Rebinds jsdom's Web Storage over the Node ≥23 global stub — see the file.
    setupFiles: ["./vitest.setup.ts"],
    // Playwright owns e2e/ — vitest must not collect its specs
    exclude: ["**/node_modules/**", "e2e/**"],
  },
});
