// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

/** @vitest-environment jsdom */
import "@testing-library/jest-dom/vitest";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render as rtlRender, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { type GrantSpec, meFixture } from "../app/mefixture";
import { LocaleProvider } from "../i18n";
import { CaptureActivityTab } from "./capture-activity";

// What this surface must never do is state a fact it does not have. Every test
// here is about that: an absent subject is not the same as a subject the
// installation chose not to keep, and a funnel of zeros is not an absent one.

const ROW = {
  id: "01930000-0000-7000-8000-00000000c001",
  connector: "gmail",
  outcome: "internal",
  reason: "internal_only",
  activity_id: null,
  resolution: null,
  counterparty: null,
  subject: null,
  occurred_at: "2026-08-15T09:12:00Z",
};

function windowBody(over: Record<string, unknown> = {}) {
  return {
    funnel: { captured: 12, internal: 3, suppressed: 0, deferred: 5, fault: 0 },
    data: [ROW],
    page: { next_cursor: null },
    payload_capture_enabled: false,
    window_hours: 24,
    ...over,
  };
}

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

// A per-file harness, per house convention (jobhealth.test.tsx's shape): the
// routes this surface can call, and nothing else.
// `allow` rather than a role name: the fixture does not infer grants from
// roles, deliberately — a screen must gate on the grant the server actually
// sent, not on a role a test asserted.
function renderTab(body: Record<string, unknown>, allow: GrantSpec = {}) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const request = input instanceof Request ? input : null;
      const url = new URL(
        request ? request.url : String(input),
        "https://test.local",
      );
      const method = request?.method ?? init?.method ?? "GET";
      const key = `${method} ${url.pathname.replace(/^\/v1/, "")}`;
      if (key === "GET /me") return jsonResponse(meFixture({ allow }));
      if (key === "GET /capture/activity") return jsonResponse(body);
      if (key === "GET /capture/activity/workspace") {
        return jsonResponse(windowBody({ data: [], funnel: {} }));
      }
      return jsonResponse({});
    }),
  );
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const ui: ReactNode = (
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">
        <CaptureActivityTab />
      </LocaleProvider>
    </QueryClientProvider>
  );
  return rtlRender(ui);
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("capture activity", () => {
  it("counts every outcome, including the ones that are zero", async () => {
    renderTab(windowBody());
    // Zero is a reading: "nothing was dropped as internal today" is exactly
    // what somebody opens this page to confirm, so the slot must be drawn.
    expect(await screen.findByText("12")).toBeInTheDocument();
    expect(screen.getAllByText("0").length).toBeGreaterThan(0);
  });

  it("says content is not stored rather than showing an empty subject", async () => {
    renderTab(windowBody());
    expect(await screen.findByText(/content not stored/i)).toBeInTheDocument();
  });

  it("distinguishes an absent payload from a posture that stores none", async () => {
    // Payload capture is ON and this row still carries nothing — an erased
    // subject. Reporting that as "content not stored" would blame the operator's
    // posture for a deletion somebody requested.
    renderTab(windowBody({ payload_capture_enabled: true }));
    expect(await screen.findByText(/no sender recorded/i)).toBeInTheDocument();
    expect(screen.queryByText(/content not stored/i)).not.toBeInTheDocument();
  });

  it("explains a reason that changes what the outcome means", async () => {
    renderTab(
      windowBody({
        data: [{ ...ROW, outcome: "deferred", reason: "deferral_capped" }],
      }),
    );
    // "Waiting on a verdict" alone would tell the reader to wait for an answer
    // that is never coming.
    expect(
      await screen.findByText(/no verdict is coming/i),
    ).toBeInTheDocument();
  });

  it("says where the numbers start, so the funnel is not read as everything", async () => {
    renderTab(windowBody());
    expect(
      await screen.findByText(/filtered on its own side/i),
    ).toBeInTheDocument();
  });

  it("hides the shared-channel toggle from a seat without the grant", async () => {
    renderTab(windowBody());
    await screen.findByText(/content not stored/i);
    expect(screen.queryByText(/shared channels/i)).not.toBeInTheDocument();
  });

  it("offers the shared-channel toggle to a seat that holds capture_trace", async () => {
    renderTab(windowBody(), { capture_trace: ["read"] });
    expect(await screen.findByText(/shared channels/i)).toBeInTheDocument();
  });

  it("reports an empty window as empty rather than as a failure", async () => {
    renderTab(windowBody({ data: [], funnel: {} }));
    expect(
      await screen.findByText(/no capture activity in the last 24 hours/i),
    ).toBeInTheDocument();
  });
});
