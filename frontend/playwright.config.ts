import { defineConfig } from "@playwright/test";

// The screen-acceptance harness (B-EP09.22a): AC-<screen>-N criteria run as
// named tests against the built app with the coherent seed fixture mocked at
// the network edge. Suites run at desktop AND 390px (§3.8) — the mobile
// checks set their viewport explicitly. Point BASE_URL at a live api+seed
// to run the same suite unmocked.

// The throttled mobile benchmark (MOBILE-PARAM-2 / MOBILE-AC-2) is a run of its
// own, by request: it is a MEASUREMENT rather than a gate, and it is slow by
// construction because a Fast-3G profile is slow on purpose.
//
// The switch is an env var rather than a project filter because a filter still
// leaves the spec discoverable — `pnpm e2e` would collect it, and one forgotten
// `--project` would put a throttled benchmark back in the lane it was taken out
// of. With this, the two runs cannot see each other's specs at all: the normal
// run does not collect the benchmark, and `make bench-mobile` collects NOTHING
// else. It is the same posture as the Go `bench` build tag.
const benchMobile = process.env.MARGINCE_BENCH_MOBILE === "1";

export default defineConfig({
  testDir: "./e2e",
  ...(benchMobile
    ? { testMatch: ["**/perf-mobile.spec.ts"] }
    : { testIgnore: ["**/perf-mobile.spec.ts"] }),
  // A throttled profile spends most of its budget waiting, and the default 30s
  // is a per-test bound that a 20-sample loop over a slow link will exceed for
  // reasons that are the point rather than a fault.
  timeout: benchMobile ? 300_000 : 30_000,
  fullyParallel: true,
  use: {
    baseURL: process.env.BASE_URL ?? "http://localhost:4317",
    // MOBILE-AC-2 measures the mobile viewport (§3.8's 390px), so the benchmark
    // run does not inherit the desktop default the AC suites are written at.
    viewport: benchMobile
      ? { width: 390, height: 844 }
      : { width: 1280, height: 800 },
    // The AC criteria assert the German chrome; detectLocale reads the
    // browser's language, so pin it — otherwise the suite only passes on a
    // machine whose system locale happens to be German.
    locale: "de-DE",
    // And pin the ZONE for the same reason, which only became visible once the
    // screens stopped hard-coding one. A surface that renders a moment on the
    // reader's own clock now reads it from the browser, so an unpinned zone made
    // every clock-time assertion in this suite depend on where the runner sits —
    // AC-book-public-409 asserts a 12:00 slot, and it passed in Berlin and
    // failed in Asia/Ho_Chi_Minh. Europe/Berlin because that is also RECORD_ZONE,
    // so the record-clock and viewer-clock surfaces agree here and a test that
    // means to tell them apart has to say so itself.
    timezoneId: "Europe/Berlin",
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
