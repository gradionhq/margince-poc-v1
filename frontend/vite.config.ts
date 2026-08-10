import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";
import { serveMcpApps } from "./scripts/vite-inline-views";

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
  server: {
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
