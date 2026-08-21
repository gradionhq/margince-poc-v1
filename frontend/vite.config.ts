import { existsSync } from "node:fs";
import { join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";
import { serveMcpApps } from "./scripts/vite-inline-views.ts";
import { TEST_TIMEOUT_MS } from "./vitest.budget.ts";

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

// Hostnames a tunnel (cloudflared, ngrok, a reverse proxy) puts in front of
// this dev server. Vite rejects a request whose Host header it does not
// recognise, so exposing `pnpm dev` publicly needs the tunnel's hostname
// listed. That hostname is minted fresh per tunnel and dies with it, which is
// why it comes from the environment and is never committed:
//
//   VITE_ALLOWED_HOSTS=abc-def.trycloudflare.com pnpm dev
//
// Comma-separated for more than one. Unset (the normal case) leaves the list
// empty, so the dev server keeps refusing every host but localhost.
const allowedHosts = (process.env.VITE_ALLOWED_HOSTS ?? "")
  .split(",")
  .map((host) => host.trim())
  .filter((host) => host.length > 0);

export default defineConfig({
  // serveMcpApps is `apply: "serve"` — a dev-only middleware that adds nothing
  // to the SPA build. It has to live HERE rather than in vite.mcp-apps.config.ts
  // because `make dev` starts this config: without it, a request for a view
  // falls through the SPA fallback below to a dev index.html carrying `src=`
  // module scripts and /@vite/client, which the api's admission check refuses by
  // name — so both views would be permanently unadvertised in every dev stack.
  plugins: [react(), tailwindcss(), serveMcpApps()],
  // The release this bundle was built from, compiled IN.
  //
  // It has to be in the bundle rather than read at run time, because the reader
  // of this value is a browser: the SPA compares its own release against the one
  // the api reports and refuses to render when they differ (src/app/release.ts).
  // A customer pulls each role image by tag, two tag pulls are two requests, and
  // a publish landing between them serves a web tier and an api from different
  // releases — a set the registry cannot refuse at the pull.
  //
  // A NARROW define rather than `envPrefix: ["MARGINCE_"]`: the prefix form
  // would compile every MARGINCE_* variable in the build environment into the
  // bundle, which is one careless deploy away from shipping a secret to every
  // browser. Empty is the local default, and empty disables the comparison.
  define: {
    __MARGINCE_RELEASE_VERSION__: JSON.stringify(
      process.env.MARGINCE_RELEASE_VERSION ?? "",
    ),
  },
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
      // The host surface, by the three subpaths frontend/package.json's "exports"
      // map publishes. These exist because a unit's frontend/ is no longer a
      // member of the ROOT workspace, so pnpm no longer links
      // extensions/<unit>/frontend/node_modules/@margince/frontend into place.
      // Membership moved to the generated composed workspace
      // (build/composition-frontend/workspace/) — that install resolves a unit's
      // OWN dependencies, and the host it must NOT install, because
      // frontend/node_modules belongs to the root workspace. So the surface is
      // an alias and the unit's dependencies are a membership; each half does
      // the thing the other cannot.
      {
        find: "@margince/frontend/design-system",
        replacement: join(frontendRoot, "src", "surface", "design-system.ts"),
      },
      {
        find: "@margince/frontend/api",
        replacement: join(frontendRoot, "src", "surface", "api.ts"),
      },
      {
        find: "@margince/frontend/app",
        replacement: join(frontendRoot, "src", "surface", "index.ts"),
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
    dedupe: [
      "react",
      "react-dom",
      "@tanstack/react-query",
      // The test lane's half of the same argument, and it is load-bearing now
      // that a unit installs its own dev dependencies in the composed
      // workspace: a unit's node_modules holds its own @testing-library and
      // vitest, and two copies of a test renderer mean a unit's render tree is
      // not the one the lane's environment set up.
      "@testing-library/react",
      "@testing-library/user-event",
      "vitest",
    ],
  },
  server: {
    allowedHosts,
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
      // The claim surface (ADR-0105). It sits beside the probes rather than
      // under /v1 because it must answer before an organization exists, so it
      // needs its own proxy entry — without one the SPA's first-run screen
      // gets the dev server's index.html instead of the api.
      "/setup": { target: proxyTarget, changeOrigin: false, secure: false },
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
    // Derived in vitest.budget.ts, which carries the measurement and the
    // arithmetic. The short version: a test of N sequential waits may spend N
    // seconds without a single wait failing, and the longest chain here is six —
    // so a five-second ceiling failed tests whose every assertion passed.
    testTimeout: TEST_TIMEOUT_MS,
    // Rebinds jsdom's Web Storage over the Node ≥23 global stub — see the file.
    setupFiles: ["./vitest.setup.ts"],
    // Playwright owns e2e/ — vitest must not collect its specs
    exclude: ["**/node_modules/**", "e2e/**"],
    // The lcov report is written for ONE reader, the SonarCloud scanner, and
    // the scanner resolves an `SF:` path against ITS base directory — the repo
    // root. Vitest's root is frontend/, so the default report says
    // `SF:src/App.tsx`, which names no file the scanner can find. Nothing
    // reports the mismatch: the report parses, every record resolves to
    // nothing, and the project carries backend coverage only while every
    // frontend file sits outside the measurement entirely. `projectRoot` is
    // what the lcov reporter writes those paths relative to, so naming the
    // repo root here is what makes the file addressable by the tool that
    // consumes it. frontend/scripts/check-lcov-paths.sh holds the invariant.
    coverage: {
      provider: "v8",
      reporter: [["lcov", { projectRoot: resolve(frontendRoot, "..") }]],
    },
  },
});
