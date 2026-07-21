/** @vitest-environment jsdom */

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  render as rtlRender,
  screen,
  waitFor,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { components } from "../api/schema";
import { LocaleProvider } from "../i18n";
import {
  AutomationPreview,
  AutomationRuns,
  OutcomeBadge,
} from "./automationdetail";

type AutomationRun = components["schemas"]["AutomationRun"];

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

beforeEach(() => localStorage.setItem("margince.workspaceSlug", "acme"));
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  window.location.hash = "";
});

const run = (over: Partial<AutomationRun>): AutomationRun => ({
  id: "r1",
  automation_id: "au-1",
  occurred_at: "2026-07-14T10:00:00Z",
  outcome: "fired",
  tier: "green",
  ...over,
});

describe("OutcomeBadge", () => {
  const cases: ReadonlyArray<[AutomationRun["outcome"], string, string]> = [
    ["fired", "badge-success", "fired"],
    ["failed", "badge-danger", "failed"],
    ["blocked", "badge-danger", "blocked"],
    ["skipped", "badge-warn", "skipped"],
    ["queued_for_approval", "badge-warn", "queued"],
  ];

  it.each(
    cases,
  )("renders %s with the right tone and label", (outcome, toneClass, label) => {
    const { container } = render(<OutcomeBadge outcome={outcome} />);
    const badge = container.querySelector(`.${toneClass}`);
    expect(badge).not.toBeNull();
    expect(badge?.textContent).toContain(label);
  });
});

describe("AutomationRuns", () => {
  it("renders only the present fields — a bare run shows no empty label rows", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse({
          data: [run({ outcome: "fired", tier: "green" })],
          page: { next_cursor: null },
        }),
      ),
    );
    render(<AutomationRuns automationId="au-1" />);
    // the badge label carries a leading glyph ("✓ fired"), so match the token
    await waitFor(() => expect(screen.getByText(/fired/)).toBeTruthy());
    // no fabricated blank label rows for the null optional fields (T7)
    expect(screen.queryByText("Why")).toBeNull();
    expect(screen.queryByText("Target")).toBeNull();
    expect(screen.queryByText("Result")).toBeNull();
    expect(screen.queryByText("Reason")).toBeNull();
  });

  it("surfaces a failed run's reason and all populated fields", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse({
          data: [
            run({
              id: "r2",
              outcome: "failed",
              tier: "yellow",
              trigger_evidence: "no activity 14d on deal BÄR Pharma",
              target_ref: "deal:BÄR Pharma",
              action_result: "send failed",
              reason: "provider error",
              approval_required: true,
            }),
          ],
          page: { next_cursor: null },
        }),
      ),
    );
    render(<AutomationRuns automationId="au-1" />);
    await waitFor(() =>
      expect(screen.getByText("provider error")).toBeTruthy(),
    );
    expect(screen.getByText("no activity 14d on deal BÄR Pharma")).toBeTruthy();
    expect(screen.getByText("deal:BÄR Pharma")).toBeTruthy();
    expect(screen.getByText("send failed")).toBeTruthy();
    expect(screen.getByText("needs approval")).toBeTruthy();
  });

  it("keyset-pages: Load more appends the second page", async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input instanceof Request ? input.url : input);
      if (url.includes("cursor=c2")) {
        return jsonResponse({
          data: [run({ id: "r-page2", trigger_evidence: "page-two-run" })],
          page: { next_cursor: null },
        });
      }
      return jsonResponse({
        data: [run({ id: "r-page1", trigger_evidence: "page-one-run" })],
        page: { next_cursor: "c2" },
      });
    });
    vi.stubGlobal("fetch", fetchMock);
    render(<AutomationRuns automationId="au-1" />);
    await waitFor(() => expect(screen.getByText("page-one-run")).toBeTruthy());
    expect(screen.queryByText("page-two-run")).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: /load more/i }));
    await waitFor(() => expect(screen.getByText("page-two-run")).toBeTruthy());
    expect(screen.getByText("page-one-run")).toBeTruthy();
  });

  it("outcome filter re-queries with ?outcome= and shows the filtered-empty copy", async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input instanceof Request ? input.url : input);
      if (url.includes("outcome=failed")) {
        return jsonResponse({ data: [], page: { next_cursor: null } });
      }
      return jsonResponse({
        data: [run({ id: "r-all", trigger_evidence: "unfiltered-run" })],
        page: { next_cursor: null },
      });
    });
    vi.stubGlobal("fetch", fetchMock);
    render(<AutomationRuns automationId="au-1" />);
    await waitFor(() =>
      expect(screen.getByText("unfiltered-run")).toBeTruthy(),
    );
    await userEvent.click(screen.getByRole("button", { name: "Failed" }));
    await waitFor(() =>
      expect(screen.getByText("No runs with this outcome.")).toBeTruthy(),
    );
    // the request that produced the empty state carried the outcome filter
    expect(
      fetchMock.mock.calls.some(([input]) =>
        String(input instanceof Request ? input.url : input).includes(
          "outcome=failed",
        ),
      ),
    ).toBe(true);
  });

  it("shows the never-fired empty state distinct from an error", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse({ data: [], page: { next_cursor: null } }),
      ),
    );
    render(<AutomationRuns automationId="au-1" />);
    await waitFor(() =>
      expect(
        screen.getByText("This automation hasn't fired yet."),
      ).toBeTruthy(),
    );
  });

  it("surfaces the RFC 7807 detail on error", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => jsonResponse({ detail: "automation not found" }, 404)),
    );
    render(<AutomationRuns automationId="au-1" />);
    await waitFor(() =>
      expect(screen.getByText("automation not found")).toBeTruthy(),
    );
  });
});

type PreviewBody = { window_days: number };

// A POST stub that records the request bodies and answers from the requested
// window, so the tests can assert both what was sent and what rendered.
function previewBackend(
  responder: (body: PreviewBody) => Response,
  bodies: PreviewBody[],
) {
  return vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const request = input instanceof Request ? input : null;
    const raw: unknown = request
      ? await request.json()
      : JSON.parse(String(init?.body));
    const body = raw as PreviewBody;
    bodies.push(body);
    return responder(body);
  });
}

describe("AutomationPreview", () => {
  it("posts window_days:30 on open and shows matches-now + would-fire", async () => {
    const bodies: PreviewBody[] = [];
    vi.stubGlobal(
      "fetch",
      previewBackend(
        (body) =>
          jsonResponse({
            matches_now: 12,
            would_have_fired: 34,
            window_days: body.window_days,
          }),
        bodies,
      ),
    );
    render(<AutomationPreview automationId="au-1" />);
    await waitFor(() =>
      expect(screen.getByText("Matches now: 12")).toBeTruthy(),
    );
    expect(screen.getByText("Would fire: ~34 / 30d")).toBeTruthy();
    expect(bodies).toEqual([{ window_days: 30 }]);
  });

  it("re-posts with the chosen window when a window button is clicked", async () => {
    const bodies: PreviewBody[] = [];
    vi.stubGlobal(
      "fetch",
      previewBackend(
        (body) =>
          jsonResponse({
            matches_now: body.window_days === 7 ? 3 : 12,
            would_have_fired: body.window_days === 7 ? 5 : 34,
            window_days: body.window_days,
          }),
        bodies,
      ),
    );
    render(<AutomationPreview automationId="au-1" />);
    await waitFor(() =>
      expect(screen.getByText("Matches now: 12")).toBeTruthy(),
    );
    await userEvent.click(screen.getByRole("button", { name: "7d" }));
    await waitFor(() =>
      expect(screen.getByText("Would fire: ~5 / 7d")).toBeTruthy(),
    );
    expect(bodies).toContainEqual({ window_days: 7 });
  });

  it("reads null would_have_fired as not-computable, never a fabricated 0", async () => {
    vi.stubGlobal(
      "fetch",
      previewBackend(
        (body) =>
          jsonResponse({
            matches_now: 5,
            would_have_fired: null,
            window_days: body.window_days,
          }),
        [],
      ),
    );
    render(<AutomationPreview automationId="au-1" />);
    await waitFor(() =>
      expect(screen.getByText("Trailing estimate not computable")).toBeTruthy(),
    );
    expect(screen.queryByText(/Would fire/)).toBeNull();
  });

  it("omits the hidden line at 0 and shows it above 0", async () => {
    vi.stubGlobal(
      "fetch",
      previewBackend(
        (body) =>
          jsonResponse({
            matches_now: 5,
            would_have_fired: 5,
            window_days: body.window_days,
            excluded_by_permission: 0,
          }),
        [],
      ),
    );
    const zero = render(<AutomationPreview automationId="au-1" />);
    await waitFor(() =>
      expect(screen.getByText("Matches now: 5")).toBeTruthy(),
    );
    expect(screen.queryByText(/hidden/)).toBeNull();
    zero.unmount();

    vi.stubGlobal(
      "fetch",
      previewBackend(
        (body) =>
          jsonResponse({
            matches_now: 5,
            would_have_fired: 5,
            window_days: body.window_days,
            excluded_by_permission: 2,
          }),
        [],
      ),
    );
    render(<AutomationPreview automationId="au-1" />);
    await waitFor(() =>
      expect(screen.getByText("2 hidden — no access")).toBeTruthy(),
    );
  });

  it("shows a loading state before the estimate resolves, then the result", async () => {
    let release: (r: Response) => void = () => {};
    const inFlight = new Promise<Response>((resolve) => {
      release = resolve;
    });
    vi.stubGlobal(
      "fetch",
      vi.fn(() => inFlight),
    );
    render(<AutomationPreview automationId="au-1" />);
    // while pending the result is not painted (QueryStates renders its
    // skeleton) — never an empty first frame nor a fabricated figure.
    expect(screen.queryByText(/Matches now/)).toBeNull();
    release(
      jsonResponse({ matches_now: 1, would_have_fired: 1, window_days: 30 }),
    );
    await waitFor(() =>
      expect(screen.getByText("Matches now: 1")).toBeTruthy(),
    );
  });

  it("surfaces the 422 window-validation detail honestly", async () => {
    vi.stubGlobal(
      "fetch",
      previewBackend(
        () => jsonResponse({ detail: "window_days must be 1..90" }, 422),
        [],
      ),
    );
    render(<AutomationPreview automationId="au-1" />);
    await waitFor(() =>
      expect(screen.getByText("window_days must be 1..90")).toBeTruthy(),
    );
  });

  it("recovers via Retry after a transient preview failure", async () => {
    let attempt = 0;
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => {
        attempt += 1;
        return attempt === 1
          ? jsonResponse({ detail: "temporary failure" }, 503)
          : jsonResponse({
              matches_now: 7,
              would_have_fired: 7,
              window_days: 30,
            });
      }),
    );
    render(<AutomationPreview automationId="au-1" />);
    await waitFor(() =>
      expect(screen.getByText("temporary failure")).toBeTruthy(),
    );
    await userEvent.click(screen.getByRole("button", { name: "Retry" }));
    await waitFor(() =>
      expect(screen.getByText("Matches now: 7")).toBeTruthy(),
    );
  });

  it("always shows the no-side-effects explainer", async () => {
    vi.stubGlobal(
      "fetch",
      previewBackend(
        (body) =>
          jsonResponse({
            matches_now: 1,
            would_have_fired: 1,
            window_days: body.window_days,
          }),
        [],
      ),
    );
    render(<AutomationPreview automationId="au-1" />);
    await waitFor(() =>
      expect(
        screen.getByText(
          "A read-only dry run — no records are changed and nothing is sent.",
        ),
      ).toBeTruthy(),
    );
  });
});
