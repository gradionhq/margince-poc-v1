/** @vitest-environment jsdom */
// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import "@testing-library/jest-dom/vitest";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  render as rtlRender,
  screen,
  waitFor,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { meFixture } from "../app/mefixture";
import { LocaleProvider } from "../i18n";
import { JobHealthCard } from "./jobhealth";

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function render(ui: ReactNode) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return rtlRender(
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">{ui}</LocaleProvider>
    </QueryClientProvider>,
  );
}

// Every request this card could make, recorded, so a test can assert what
// actually went to the server — the absence of a call is the whole point of the
// non-admin case below (privacy.test.tsx's harness shape, copied per file per
// house convention).
type Sent = { key: string };

function stubRoutes(overrides: Record<string, () => Response> = {}) {
  const sent: Sent[] = [];
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
      sent.push({ key });
      const override = overrides[key];
      if (override) return override();
      if (key === "GET /admin/job-health") return jsonResponse(HEALTH);
      // The endpoint gates on the admin ROLE server-side, so the default
      // principal here holds it. A test asserting the refusal overrides this.
      if (key === "GET /me")
        return jsonResponse(meFixture({ roles: ["admin"] }));
      return jsonResponse({});
    }),
  );
  return sent;
}

const HEALTH = {
  generated_at: "2026-08-13T09:30:00Z",
  kinds: [
    {
      kind: "capture_classify",
      queue: "default",
      fleet_wide: false,
      waiting: 12,
      running: 1,
      retrying: 2,
      dead: 0,
      oldest_waiting_age_seconds: 4_500,
    },
    {
      kind: "retention_sweep_dispatch",
      queue: "periodic",
      fleet_wide: true,
      waiting: 0,
      running: 0,
      retrying: 0,
      dead: 0,
      oldest_waiting_age_seconds: null,
    },
  ],
  recent_failures: [
    {
      kind: "capture_classify",
      state: "retryable",
      attempt: 2,
      max_attempts: 5,
      failed_at: "2026-08-13T09:20:00Z",
      reason: "the model provider refused the request",
    },
  ],
};

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("JobHealthCard", () => {
  it("reports every state of every kind, and which rows carry no workspace", async () => {
    stubRoutes();
    render(<JobHealthCard />);
    // All four counts, zeros included: "0 dead" is the reassurance an operator
    // opened this card for, and a card that only rendered non-zero counts would
    // read the same whether the queue was healthy or unread. Two rows report a
    // dead count, so the workspace kind and the dispatcher each contribute one.
    expect(await screen.findByText("12 waiting")).toBeInTheDocument();
    expect(screen.getByText("1 running")).toBeInTheDocument();
    expect(screen.getByText("2 retrying")).toBeInTheDocument();
    expect(screen.getAllByText("0 dead").length).toBe(2);
    // The kind is the identifier River persists, shown verbatim — it names both
    // the count row and the failure below it, which is how an operator ties the
    // two together.
    expect(screen.getAllByText("capture_classify").length).toBe(2);
    // The dispatcher row is separated from the workspace's own work, and says
    // whose counts they are.
    expect(screen.getByText("retention_sweep_dispatch")).toBeInTheDocument();
    expect(screen.getByText(/carry no workspace/i)).toBeInTheDocument();
    // The stall signal, in a unit that survives the sub-hour case: 4500s is
    // "1 hours", never format.ts's "0 hr" flooring.
    expect(screen.getByText(/waited 1 hours/)).toBeInTheDocument();
  });

  it("states nothing is runnable rather than claiming a wait of zero", async () => {
    stubRoutes();
    render(<JobHealthCard />);
    await screen.findByText("retention_sweep_dispatch");
    // The dispatcher's oldest_waiting_age_seconds is null — nothing of that
    // kind is queued now, so the row names its queue and stops there. A "waited
    // 0 seconds" note would read as a queue that had only just started.
    expect(screen.getByText("queue periodic")).toBeInTheDocument();
    expect(screen.queryByText(/waited 0 seconds/)).not.toBeInTheDocument();
  });

  it("surfaces the vetted failure text with the attempt it died on", async () => {
    stubRoutes();
    render(<JobHealthCard />);
    expect(
      await screen.findByText("the model provider refused the request"),
    ).toBeInTheDocument();
    expect(screen.getByText(/attempt 2 of 5/)).toBeInTheDocument();
    expect(screen.getByText(/job layer's own wording/i)).toBeInTheDocument();
  });

  it("gives a dead job the danger treatment and says what it means", async () => {
    stubRoutes({
      "GET /admin/job-health": () =>
        jsonResponse({
          ...HEALTH,
          kinds: [{ ...HEALTH.kinds[0], dead: 3, retrying: 0 }],
          recent_failures: [
            { ...HEALTH.recent_failures[0], state: "discarded", attempt: 5 },
          ],
        }),
    });
    render(<JobHealthCard />);
    // An interrupting notice, not a quiet pill: dead work does not resume on
    // its own, so it must not be something a reader can scroll past.
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveClass("callout-danger");
    expect(alert).toHaveTextContent(/will not happen without intervention/i);
    expect(alert).toHaveTextContent(/3 jobs/);
    // And the count itself carries the tone on the row it belongs to.
    expect(screen.getByText("3 dead")).toHaveClass("badge-danger");
    expect(screen.getByText("discarded")).toHaveClass("badge-danger");
  });

  it("keeps a healthy report free of the dead-work alert", async () => {
    stubRoutes();
    render(<JobHealthCard />);
    await screen.findByText("12 waiting");
    // The fixture's dead counts are all zero, so nothing may interrupt. Without
    // this, an unconditional callout would pass the test above unnoticed.
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("withholds the report from a non-admin instead of asking for it", async () => {
    const sent = stubRoutes({
      "GET /me": () => jsonResponse(meFixture({ roles: ["ops"] })),
    });
    render(<JobHealthCard />);
    await screen.findByText(/only an admin can see background-job health/i);
    expect(screen.queryByText("capture_classify")).not.toBeInTheDocument();
    // And it never issued the call the server would only refuse. An ops seat
    // reaching this page for its other sections must not generate a 403.
    expect(sent.some((entry) => entry.key === "GET /admin/job-health")).toBe(
      false,
    );
  });

  it("offers a retry on failure, and re-reads when it is taken", async () => {
    const sent = stubRoutes({
      "GET /admin/job-health": () =>
        jsonResponse(
          {
            title: "Internal Server Error",
            detail: "the job store could not be read",
            status: 500,
            code: "internal_error",
          },
          500,
        ),
    });
    render(<JobHealthCard />);
    expect(
      await screen.findByText("the job store could not be read"),
    ).toBeInTheDocument();
    const before = sent.filter(
      (entry) => entry.key === "GET /admin/job-health",
    ).length;
    await userEvent.click(screen.getByRole("button", { name: /retry/i }));
    await waitFor(() =>
      expect(
        sent.filter((entry) => entry.key === "GET /admin/job-health").length,
      ).toBeGreaterThan(before),
    );
  });

  it("says the queue is idle rather than showing an empty card", async () => {
    stubRoutes({
      "GET /admin/job-health": () =>
        jsonResponse({
          generated_at: "2026-08-13T09:30:00Z",
          kinds: [],
          recent_failures: [],
        }),
    });
    render(<JobHealthCard />);
    expect(
      await screen.findByText(/nothing in the background queue/i),
    ).toBeInTheDocument();
  });
});
