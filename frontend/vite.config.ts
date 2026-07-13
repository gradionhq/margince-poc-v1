import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";

// The frontend talks only to the /v1 contract surface (architecture/01:
// frontend depends on the generated contract, never Go internals). In dev,
// Vite proxies /v1 to the local api role; the workspace header comes from
// the app, the session cookie from the browser (localhost is a secure-context,
// so the Secure session cookie survives over plain http — no TLS needed).
// `make dev` (scripts/dev.sh) runs the api on a port matching the FE port and
// sets BACKEND_PORT so this /v1 proxy follows it. With no BACKEND_PORT the
// proxy falls back to the base api port :8080 (bare `make dev`).
const backendPort = process.env.BACKEND_PORT ?? "8080";
const proxyTarget = `http://localhost:${backendPort}`;

export default defineConfig({
  plugins: [react(), tailwindcss()],
  server: {
    proxy: {
      "/v1": {
        target: proxyTarget,
        changeOrigin: false,
        secure: false,
      },
    },
  },
  test: {
    environment: "node",
    // Playwright owns e2e/ — vitest must not collect its specs
    exclude: ["**/node_modules/**", "e2e/**"],
  },
});
