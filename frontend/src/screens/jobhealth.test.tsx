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
      job_id: 4711,
      first_failed_at: "2026-08-13T09:08:00Z",
      failure_class: "provider_unavailable",
      remedy:
        "check the provider status page; the retry ladder rides out a brief outage",
      reason: "the model provider refused the request",
    },
  ],
};

// A failure whose stored text the job layer could not vet: it keeps the fixed
// substitute sentence and asserts NO class, so neither a class nor a remedy is
// on the wire. The endpoint nulls the pair together, which is why the tests
// below drop both at once rather than one at a time.
const UNCLASSIFIED = {
  kind: "capture_classify",
  state: "retryable",
  attempt: 2,
  max_attempts: 5,
  failed_at: "2026-08-13T09:20:00Z",
  job_id: 4711,
  first_failed_at: null,
  failure_class: null,
  remedy: null,
  reason: "the job failed for a reason it could not classify",
};

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("JobHealthCard", () => {
  it("reports every state of every kind, and which rows carry no organization", async () => {
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
    expect(screen.getByText(/carry no organization/i)).toBeInTheDocument();
    // Each of the three readings is a NAMED row. The counts and the failures
    // are the same shape on screen, so a reading that lost its naming would
    // leave an operator reading fleet work as this organization's.
    expect(screen.getByText("This organization")).toBeInTheDocument();
    expect(screen.getByText("Fleet dispatchers")).toBeInTheDocument();
    expect(screen.getByText("Recent failures")).toBeInTheDocument();
    // The stall signal, in a unit that survives the sub-hour case: 4500s reads as
    // one hour, never format.ts's "0 hr" flooring — and in the singular, which is
    // the whole reason the four duration keys carry a .one form.
    expect(screen.getByText(/waited 1 hour\b/)).toBeInTheDocument();
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

  it("names the class, the remedy, the row and how long it has been failing", async () => {
    stubRoutes();
    render(<JobHealthCard />);
    // The class is the token an alert is keyed on and the log is grepped by, so
    // it is rendered verbatim and mono — the same treatment as the kind — and
    // never as a second status pill beside the state badge.
    const shownClass = await screen.findByText("provider_unavailable");
    expect(shownClass).toHaveClass("t-mono");
    expect(shownClass).not.toHaveClass("badge");
    // What to do about it, which is the half a failure list is useless without.
    expect(
      screen.getByText(/check the provider status page/),
    ).toBeInTheDocument();
    // River's own row id, ungrouped: "4,711" would not find the log line that
    // carries job_id=4711.
    const note = screen.getByText(/attempt 2 of 5/);
    expect(note.textContent).toContain("job 4711");
    // Failing SINCE, which the attempt counter cannot say: rung 2 of 5 is the
    // same reading whether the first failure was a minute or a day ago.
    //
    // The formatted VALUE, not just the label. Matching the label alone passes
    // when the renderer interpolates nothing, which is the same dangling
    // "failing since" this screen is careful everywhere else not to draw. The
    // year is the stable part across locales; the fixture's is 2026-08-13.
    expect(note.textContent).toMatch(/failing since .*2026/);
  });

  it("asserts no class, no remedy and no span for a failure nobody could vet", async () => {
    stubRoutes({
      "GET /admin/job-health": () =>
        jsonResponse({ ...HEALTH, recent_failures: [UNCLASSIFIED] }),
    });
    render(<JobHealthCard />);
    // The vetted substitute still reports what little is known.
    const note = await screen.findByText(/attempt 2 of 5/);
    expect(
      screen.getByText("the job failed for a reason it could not classify"),
    ).toBeInTheDocument();
    // Absent stays absent. An unclassified failure has no class token and no
    // remedy line, and first_failed_at is null here — which the endpoint sends
    // only for a job that recorded NO attempt error at all. A single recorded
    // error does produce a first_failed_at, so null is absence and not "one".
    expect(screen.queryByText("provider_unavailable")).not.toBeInTheDocument();
    expect(screen.queryByText(/What to do/)).not.toBeInTheDocument();
    expect(note.textContent).not.toMatch(/failing since/);
    // And nothing is drawn as an empty shell: no leading, doubled or trailing
    // separator, which is what a dropped part leaves behind when it is rendered
    // as a blank rather than omitted.
    expect(note.textContent).not.toMatch(/^\s*·/);
    expect(note.textContent).not.toMatch(/·\s*·/);
    expect(note.textContent).not.toMatch(/·\s*$/);
  });

  it("reports the class without a span when only the first failure is missing", async () => {
    stubRoutes({
      "GET /admin/job-health": () =>
        jsonResponse({
          ...HEALTH,
          recent_failures: [
            { ...HEALTH.recent_failures[0], first_failed_at: null },
          ],
        }),
    });
    render(<JobHealthCard />);
    // The class and remedy do not depend on the span: a job cancelled before it
    // ran records no attempt error, and it is still a classified failure.
    expect(await screen.findByText("provider_unavailable")).toBeInTheDocument();
    expect(
      screen.getByText(/check the provider status page/),
    ).toBeInTheDocument();
    const note = screen.getByText(/attempt 2 of 5/);
    expect(note.textContent).not.toMatch(/failing since/);
    expect(note.textContent).not.toMatch(/·\s*$/);
  });

  it("omits the job id rather than inventing one when the row carries none", async () => {
    stubRoutes({
      "GET /admin/job-health": () =>
        jsonResponse({
          ...HEALTH,
          recent_failures: [
            { ...HEALTH.recent_failures[0], job_id: undefined },
          ],
        }),
    });
    render(<JobHealthCard />);
    // job_id is optional on the wire, so a report from a build that predates it
    // must lose the label with the value rather than print a bare "job".
    const note = await screen.findByText(/attempt 2 of 5/);
    expect(note.textContent).not.toMatch(/job\b/);
    // The span still renders with its value: dropping the id must cost the id
    // and nothing standing next to it.
    expect(note.textContent).toMatch(/failing since .*2026/);
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
    // In its own words, INSTEAD of the readings — not three named rows each
    // saying it has nothing. That the background system is idle is one finding,
    // and a list of empty rows reports it three times as three.
    expect(screen.queryByText("This organization")).not.toBeInTheDocument();
    expect(screen.queryByText("Recent failures")).not.toBeInTheDocument();
    // The stamp stands even here. An operator acting on "nothing is queued" is
    // trusting a reading, and a reading with no time on it cannot be trusted.
    expect(screen.getByText(/read at/i)).toBeInTheDocument();
  });

  it("dates the report in the card's own footer, and only when there is one", async () => {
    stubRoutes({
      "GET /me": () => jsonResponse(meFixture({ roles: ["ops"] })),
    });
    render(<JobHealthCard />);
    await screen.findByText(/only an admin can see background-job health/i);
    // No report, no stamp: a time under a withheld body would date a reading
    // this card is not showing.
    expect(screen.queryByText(/read at/i)).not.toBeInTheDocument();
    cleanup();

    stubRoutes();
    render(<JobHealthCard />);
    // When the report was read belongs to the whole card rather than to any one
    // reading in it, so it stands in the panel's own footer band rather than as
    // one more line after the last row.
    const stamp = await screen.findByText(/read at/i);
    expect(stamp.closest("footer")).not.toBeNull();
  });
});
