// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

/** @vitest-environment jsdom */
import "@testing-library/jest-dom/vitest";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  render as rtlRender,
  screen,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
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

// The ladder the drawer reads. Two rungs the client knows and ONE it does not,
// because the surface's whole evolution claim is that a stage added by a newer
// server still renders rather than vanishing.
function ladderBody(over: Record<string, unknown> = {}) {
  return {
    activity_id: null,
    connector: "gmail",
    payload_capture_enabled: false,
    retention_hours: 24,
    stages: [
      {
        stage: "internal_drop",
        order: 40,
        subject_kind: "message",
        status: "skipped",
        reason: "internal_only",
      },
      {
        stage: "attention_label",
        order: 100,
        subject_kind: "message",
        status: "skipped",
        reason: "transport_not_read",
      },
      {
        stage: "a_stage_from_the_future",
        order: 130,
        subject_kind: "message",
        status: "done",
        label: "Sentiment scoring",
        reason: "invented_reason",
        reason_text: "the sentiment pass read it",
      },
    ],
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
      if (key === "GET /channel-providers") {
        return jsonResponse({
          data: [
            {
              provider: "gmail",
              label: "Gmail",
              credential_model: "per_member",
              supplies_transport: true,
            },
          ],
        });
      }
      if (key.startsWith("GET /capture/traces/")) {
        return jsonResponse(ladderBody());
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

  it("renders a suppression reason as a sentence, never as a raw key", async () => {
    // The catalog falls back to the KEY when one is missing, so a missing entry
    // is invisible until somebody sees a row. This one shipped that way.
    renderTab(
      windowBody({
        data: [
          { ...ROW, outcome: "suppressed", reason: "transactional_infra" },
        ],
      }),
    );
    expect(await screen.findByText(/mail infrastructure/i)).toBeInTheDocument();
    expect(screen.queryByText(/captureActivity\./)).not.toBeInTheDocument();
  });

  it("renders nothing for a reason it does not know", async () => {
    // A row written by a newer binary. Rendering the key would show a member an
    // identifier; the honest answer is that this screen does not know.
    renderTab(
      windowBody({
        data: [{ ...ROW, outcome: "captured", reason: "teleported" }],
      }),
    );
    await screen.findByText(/content not stored/i);
    expect(screen.queryByText(/teleported/)).not.toBeInTheDocument();
  });

  it("does not say a capped deferral is waiting for a verdict", async () => {
    // The outcome and its own explanation must not argue: nothing is queued and
    // no verdict is coming, so "Waiting on a verdict" above "no verdict is
    // coming" is the screen contradicting itself.
    renderTab(
      windowBody({
        data: [{ ...ROW, outcome: "deferred", reason: "deferral_capped" }],
      }),
    );
    const row = within(await screen.findByRole("list"));
    expect(row.getByText(/not queued/i)).toBeInTheDocument();
    expect(row.queryByText(/waiting on a verdict/i)).not.toBeInTheDocument();
  });

  it("labels the deferred bucket with what every message in it has in common", async () => {
    // A bucket is one number over both the settled and the still-waiting, so a
    // tense in its label is a claim it cannot support. "Waiting on a verdict"
    // over a strip whose rows each say the verdict landed is the same
    // contradiction the row avoids, one level up.
    renderTab(
      windowBody({
        data: [
          {
            ...ROW,
            outcome: "deferred",
            reason: null,
            resolution: {
              status: "real",
              kind: "person",
              resolved_at: "2026-08-15T09:13:00Z",
            },
          },
        ],
      }),
    );
    const funnel = within(await screen.findByTestId("capture-activity-funnel"));
    expect(funnel.getByText(/sent for a verdict/i)).toBeInTheDocument();
    expect(funnel.queryByText(/waiting on a verdict/i)).not.toBeInTheDocument();
  });

  it("does not say a settled deferral is still waiting for its verdict", async () => {
    // The same contradiction as the capped case, from the other direction: the
    // ledger has answered, the answer is on the row, and the outcome beside it
    // must not still be asking the question.
    renderTab(
      windowBody({
        data: [
          {
            ...ROW,
            outcome: "deferred",
            reason: null,
            resolution: {
              status: "real",
              kind: "person",
              resolved_at: "2026-08-15T09:13:00Z",
            },
          },
        ],
      }),
    );
    const row = within(await screen.findByRole("list"));
    expect(row.getByText(/sent for a verdict/i)).toBeInTheDocument();
    expect(row.getByText(/judged a real contact/i)).toBeInTheDocument();
    expect(row.queryByText(/waiting on a verdict/i)).not.toBeInTheDocument();
  });

  it("still says a deferral with no answer yet is waiting", async () => {
    // The control for the test above: the label is settled-vs-waiting, not
    // "deferrals never say waiting".
    renderTab(
      windowBody({
        data: [
          {
            ...ROW,
            outcome: "deferred",
            reason: null,
            resolution: { status: "pending", kind: null, resolved_at: null },
          },
        ],
      }),
    );
    const row = within(await screen.findByRole("list"));
    expect(row.getByText(/waiting on a verdict/i)).toBeInTheDocument();
  });

  // The row language: the counters and the log are the SUBJECT of this card
  // rather than answers to a question that would fit beside them, so each is
  // named and then given the whole width below its naming. A log squeezed into
  // a right-hand column is the shape this replaced.
  it("gives the log a named row of its own, at the card's full width", async () => {
    renderTab(windowBody());
    const naming = await screen.findByText("Messages");
    const row = naming.closest(".settingrow");
    expect(row).not.toBeNull();
    if (row instanceof HTMLElement) {
      expect(row.className).toContain("settingrow-stack");
      expect(within(row).getByRole("list")).toBeInTheDocument();
    }
    // And the counters are their own row, with what they are counting stated as
    // that row's description rather than as a paragraph between the two.
    const counters = screen.getByText("Outcomes").closest(".settingrow");
    expect(counters).not.toBeNull();
    if (counters instanceof HTMLElement) {
      expect(
        within(counters).getByTestId("capture-activity-funnel"),
      ).toBeInTheDocument();
      expect(
        within(counters).getByText(/filtered on its own side/i),
      ).toBeInTheDocument();
    }
  });

  it("reports an empty window as empty rather than as a failure", async () => {
    renderTab(windowBody({ data: [], funnel: {} }));
    expect(
      await screen.findByText(/no capture activity in the last 24 hours/i),
    ).toBeInTheDocument();
  });
});

describe("the pipeline drill-down", () => {
  it("resolves a transport id to its name", async () => {
    // The contract forbids storing a label — two deploys would disagree about
    // the same transport — so the screen resolves it. Before this it printed
    // `ext:dispact-connector:dispact` at a member.
    renderTab(windowBody());
    expect(await screen.findByText("Gmail")).toBeInTheDocument();
    expect(screen.queryByText("gmail")).not.toBeInTheDocument();
  });

  it("says both numbers when a counter is used as a filter", async () => {
    // The counters count the WINDOW; the filter narrows what is LOADED. A bare
    // count under a counter reading 3 would look like the counter was wrong.
    const user = userEvent.setup();
    renderTab(windowBody());
    await screen.findByText(/content not stored/i);
    await user.click(
      screen.getByRole("button", { name: /dropped as internal/i }),
    );
    expect(await screen.findByText(/showing 1 of 3/i)).toBeInTheDocument();
  });

  it("filters the rows to the outcome that was clicked", async () => {
    const user = userEvent.setup();
    renderTab(
      windowBody({
        data: [
          ROW,
          {
            ...ROW,
            id: "01930000-0000-7000-8000-00000000c002",
            outcome: "captured",
            reason: null,
          },
        ],
      }),
    );
    // Settle on a row-only string: "Dropped as internal" appears in the funnel
    // slot AND in the row, so waiting on it would be ambiguous.
    await screen.findAllByText(/content not stored/i);
    await user.click(screen.getByRole("button", { name: /^captured/i }));
    const list = within(await screen.findByRole("list"));
    expect(list.getByText("Captured")).toBeInTheDocument();
    expect(list.queryByText("Dropped as internal")).not.toBeInTheDocument();
  });

  it("opens one message's whole path from its row", async () => {
    const user = userEvent.setup();
    renderTab(windowBody());
    await user.click(
      await screen.findByRole("button", { name: /every step this message/i }),
    );
    expect(
      await screen.findByText(/how this message was handled/i),
    ).toBeInTheDocument();
    // The rung that motivated the whole surface: a step that DECLINED, saying
    // why, where before there was nothing at all.
    expect(await screen.findByText(/reads email only/i)).toBeInTheDocument();
  });

  it("renders a stage this build has never heard of", async () => {
    // The evolution seam. A newer server adds a step; this client has no
    // catalog entry for it and must still show it, from the server's own words
    // — because a stage that vanished would be exactly the silence this
    // surface exists to end.
    const user = userEvent.setup();
    renderTab(windowBody());
    await user.click(
      await screen.findByRole("button", { name: /every step this message/i }),
    );
    expect(await screen.findByText("Sentiment scoring")).toBeInTheDocument();
    expect(screen.getByText(/the sentiment pass read it/i)).toBeInTheDocument();
    // And never the raw key, which is how a missing entry shipped here once.
    expect(screen.queryByText(/pipeline\./)).not.toBeInTheDocument();
  });

  it("says once that no step stored content, rather than per step", async () => {
    const user = userEvent.setup();
    renderTab(windowBody());
    await user.click(
      await screen.findByRole("button", { name: /every step this message/i }),
    );
    expect(
      await screen.findByText(/did not turn payload capture on/i),
    ).toBeInTheDocument();
  });
});
