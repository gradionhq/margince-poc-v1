/** @vitest-environment jsdom */
import { QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { StrictMode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "./App";
import { meFixture } from "./app/mefixture";
import { createQueryClient } from "./app/queryclient";
import { LocaleProvider } from "./i18n";
import { memoryStorage } from "./testing/appharness";

// Two places decide, on their own, where a session belongs when the
// installation has not described itself, and each rewrites the hash to say so:
// AuthedApp's onboarding gate sends every route back to onboarding while
// GET /company answers 404, and the conversational shell's restore
// (screens/onboarding-conversation/restore.ts) decides whether the journey is
// finished and leaves. Those are the only two automatic navigators in the app;
// every other navigate() answers a click.
//
// When they disagree they rewrite the hash AT each other, and that is not a
// cosmetic redirect flicker. `useRoute` is a useSyncExternalStore over
// location.hash, so a navigate() inside a passive effect makes React's own
// store-instance check force a synchronous re-render, which runs the effect
// again — nested updates until React trips its limit with "Maximum update
// depth exceeded" and unmounts the shell. The app's error boundary sits above
// every route, so the whole product then reads "This view stopped working",
// including the address "Try again" returns to.
//
// The disagreement is reachable on a brand-new installation and nowhere else:
// the wizard row and the company profile are separate writes, and the connect
// act persists `step: "complete"` without requiring a saved profile. Both
// cases below are mounted through the REAL App, because a unit test of either
// half alone passes while the pair loops.

type InstallShape = Readonly<{
  /** GET /company; null answers 404 — the profile the gate looks for is absent. */
  companySaved: boolean;
}>;

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

// The addresses the gate and the restore each send the reader to. A hash this
// app answers that neither of them chose is not part of the disagreement.
const GATE_TARGET = "#/onboarding/company";
const HOME = "#/home";

/**
 * An installation whose wizard row says `complete`, with or without the profile
 * that claim depends on.
 *
 * Everything past those two answers is the minimum the onboarding surface needs
 * to render: the assertion is about which ADDRESS the app settles on, and a
 * screen that threw for a missing fixture would fail these tests for an
 * unrelated reason.
 */
function completedWizardFetch(shape: InstallShape) {
  return async (input: Request | string | URL) => {
    const request = input instanceof Request ? input : null;
    const url = String(request ? request.url : input);
    const path = new URL(url, "http://localhost").pathname.replace(/^\/v1/, "");
    const method = request?.method ?? "GET";
    if (path === "/me") {
      return json(meFixture({ roles: ["admin"], seat: "full" }));
    }
    if (path === "/company") {
      return shape.companySaved
        ? json({
            organization_id: "018f3a1b-0000-7000-8000-0000000000a1",
            display_name: "Gradion",
            website: "gradion.com",
            offer_summary: "Revenue software for manufacturers",
            icp: "Mid-market manufacturers",
          })
        : json({ detail: "no company yet" }, 404);
    }
    if (path === "/onboarding/state" && method === "GET") {
      return json({
        path: "creator",
        step: "complete",
        source_mode: "website",
        website_url: "https://gradion.com",
        site_read_id: null,
        company_draft: {},
        selected_fact_keys: [],
        voice_skipped: false,
        connect_skipped: false,
        version: 4,
        completed_at: null,
        created_at: "2026-08-20T08:00:00Z",
        updated_at: "2026-08-20T09:00:00Z",
      });
    }
    if (path === "/company/context/capabilities") {
      return json({
        onboarding_enabled: true,
        read_enabled: true,
        rollout: "ga",
      });
    }
    if (path === "/ai/profile") {
      return json({
        name: "Margince",
        kind: "ai",
        state: "configured",
        inference_mode: "cloud",
        providers: ["gemini"],
        configured_models: [],
      });
    }
    if (path === "/voice-profiles" || path === "/connectors") {
      return json({ data: [], page: {} });
    }
    return json({ data: [], page: {} });
  };
}

/** Every address the app moved to, in order, so a ping-pong is legible as one. */
function recordHashChanges(): string[] {
  const seen: string[] = [];
  window.addEventListener("hashchange", () => {
    seen.push(window.location.hash);
  });
  return seen;
}

function renderApp() {
  render(
    // StrictMode, because that is what main.tsx mounts: the double-invoked
    // effects are part of the shape a redirect loop is discovered in.
    <StrictMode>
      <QueryClientProvider client={createQueryClient()}>
        <LocaleProvider initial="en">
          <App />
        </LocaleProvider>
      </QueryClientProvider>
    </StrictMode>,
  );
}

function mount(shape: InstallShape, at: string): string[] {
  window.location.hash = at;
  vi.stubGlobal("fetch", vi.fn(completedWizardFetch(shape)));
  const moves = recordHashChanges();
  renderApp();
  return moves;
}

beforeEach(() => {
  vi.stubGlobal("localStorage", memoryStorage());
  vi.stubGlobal("scrollTo", vi.fn());
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  window.location.hash = "";
});

// Both cases count the app's moves rather than comparing them to a literal
// sequence: jsdom announces the starting hash the test itself set, which is the
// reader's arrival and not a move the app made. A loop shows up as a target
// RECURRING, which is asserted separately from the settled address — a loop
// that happens to stop on the right hash is still the defect.
describe("the onboarding gate and the wizard's restore", () => {
  it("agree on onboarding while the company profile is absent", async () => {
    const moves = mount({ companySaved: false }, HOME);

    // The company act reopens rather than reporting a completion the profile
    // does not support, so the gate's destination is where the reader stays.
    expect(await screen.findByLabelText(/Your website address/)).toBeTruthy();
    await waitFor(() => {
      expect(window.location.hash).toBe(GATE_TARGET);
    });
    expect(moves.filter((hash) => hash === GATE_TARGET)).toHaveLength(1);
    expect(moves.at(-1)).toBe(GATE_TARGET);
  });

  it("agree on leaving once the profile backs the completion", async () => {
    // The mirror, and it is what keeps the guard above from being a way to
    // strand a finished installation on the onboarding screen: with the profile
    // saved, the completion is real and the restore leaves — once.
    const moves = mount({ companySaved: true }, GATE_TARGET);

    await waitFor(() => {
      expect(window.location.hash).toBe(HOME);
    });
    expect(moves.filter((hash) => hash === HOME)).toHaveLength(1);
    expect(moves.at(-1)).toBe(HOME);
  });
});
