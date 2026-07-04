import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";

// The frontend talks only to the /v1 contract surface (architecture/01:
// frontend depends on the generated contract, never Go internals). In dev,
// Vite proxies /v1 to the local api role; the workspace header comes from
// the app, the session cookie from the browser.
export default defineConfig({
  plugins: [react(), tailwindcss()],
  server: {
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
  },
});
