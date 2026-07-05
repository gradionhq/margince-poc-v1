import { defineConfig } from "@playwright/test";

// The screen-acceptance harness (B-EP09.22a): AC-<screen>-N criteria run as
// named tests against the built app with the coherent seed fixture mocked at
// the network edge. Suites run at desktop AND 390px (§3.8) — the mobile
// checks set their viewport explicitly. Point BASE_URL at a live api+seed
// to run the same suite unmocked.
export default defineConfig({
  testDir: "./e2e",
  timeout: 30_000,
  fullyParallel: true,
  use: {
    baseURL: process.env.BASE_URL ?? "http://localhost:4317",
    viewport: { width: 1280, height: 800 },
    // the SW would compete with the network-edge seed mocks
    serviceWorkers: "block",
  },
  webServer: process.env.BASE_URL
    ? undefined
    : {
        command: "pnpm preview --port 4317 --strictPort",
        url: "http://localhost:4317",
        reuseExistingServer: true,
      },
});
