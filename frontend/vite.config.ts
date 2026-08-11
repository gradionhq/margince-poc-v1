import { existsSync } from "node:fs";
import { join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";
import { serveMcpApps } from "./scripts/vite-inline-views";

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
for (const artifact of [
  "extensions.gen.ts",
  "extscreens.gen.ts",
  "extlocales.gen.ts",
]) {
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
  // serveMcpApps is `apply: "serve"` — a dev-only middleware that adds nothing
  // to the SPA build. It has to live HERE rather than in vite.mcp-apps.config.ts
  // because `make dev` starts this config: without it, a request for a view
  // falls through the SPA fallback below to a dev index.html carrying `src=`
  // module scripts and /@vite/client, which the api's admission check refuses by
  // name — so both views would be permanently unadvertised in every dev stack.
  plugins: [react(), tailwindcss(), serveMcpApps()],
  resolve: {
    // An ARRAY rather than a record, because one entry must match a pattern:
    // the unit-package mapping below is a family of names, not one name.
    alias: [
      {
        find: "@composition/extensions",
        replacement: join(compositionDir, "extensions.gen.ts"),
      },
      // The unit SCREENS, selected by the same switch and for the same reason.
      // Both sides are GENERATED now, and the composed one imports each unit's
      // own workspace package: a screen calls routes only a composed
      // installation serves, so the vanilla bundle resolves an empty registry
      // and never pulls a unit into the graph at all.
      {
        find: "@composition/screens",
        replacement: join(compositionDir, "extscreens.gen.ts"),
      },
      // A unit's own copy, merged into the catalogue. On a vanilla tree it is
      // an empty object, so `useT` resolves exactly the core keys it always did.
      {
        find: "@composition/copy",
        replacement: join(compositionDir, "extlocales.gen.ts"),
      },
      // Every enabled unit's screen package, by the name the generated registry
      // imports it under. The compile-time half is tsconfig.composed.json's
      // "@margince-ext/*" mapping.
      //
      // Resolved by NAME rather than installed as a dependency of the SPA: pnpm
      // links a member into its DEPENDENTS' node_modules, so installing it
      // would mean frontend/package.json listing every enabled unit — an
      // upstream-owned file that adding a unit would then have to edit.
      // Presence under extensions/ is the enablement here exactly as it is on
      // the Go side.
      {
        find: /^@margince-ext\/(.+)$/,
        replacement: join(frontendRoot, "..", "extensions", "$1", "frontend"),
      },
    ],
    // ONE copy of every package that keeps state the HOST owns: React's hook
    // dispatcher, and react-query's QueryClient context. A second copy is a
    // second, empty one — a unit's hooks throw because the host never
    // dispatched them, or its first useQuery reports no QueryClient on a page
    // that plainly has one.
    //
    // gen-composition refuses these as DIRECT dependencies of a unit; this is
    // the half that holds when one of a unit's own dependencies pulls a second
    // copy in transitively.
    dedupe: ["react", "react-dom", "@tanstack/react-query"],
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
      "/.well-known": {
        target: proxyTarget,
        changeOrigin: false,
        secure: false,
      },
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
