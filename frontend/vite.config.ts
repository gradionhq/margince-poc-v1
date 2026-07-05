import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";

// The frontend talks only to the /v1 contract surface (architecture/01:
// frontend depends on the generated contract, never Go internals). In dev,
// Vite proxies /v1 to the local api role; the workspace header comes from
// the app, the session cookie from the browser.
// MARGINCE_DEV_TLS=1 (set by dev/dev.sh) means we are served behind the local
// HTTPS front door on :8080, not hit directly on :5173. In that mode the page
// origin is https://localhost:8080, so the HMR websocket must dial the front
// door (which proxies it back here) and localhost:8080 must be an allowed host.
// The /v1 proxy below is only used when Vite is hit directly (:5173); behind
// the front door the api is reached same-origin, so it is inert.
const behindFrontDoor = process.env.MARGINCE_DEV_TLS === "1";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  server: {
    allowedHosts: behindFrontDoor ? ["localhost"] : undefined,
    hmr: behindFrontDoor
      ? { protocol: "wss", host: "localhost", clientPort: 8080 }
      : undefined,
    proxy: {
      "/v1": {
        target: "https://localhost:8080",
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
