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
import { BackfillPanel } from "./backfill";

// The connect-time backfill is the coldstart payoff: the scope must auto-load
// (honest scope before any click), the spend must still wait for the explicit
// start (ADR-0020 preview-before-spend), and the run must render the three
// headline figures — captured mail, people, companies — from real persisted
// counts as they climb. Every number here is a server number.

type BackfillStatus = components["schemas"]["BackfillStatus"];
type BackfillPreview = components["schemas"]["BackfillPreview"];

const previewOf = (messages: number): BackfillPreview => ({
  window: "6m",
  estimated_messages: messages,
  computed_at: "2026-07-23T10:00:00Z",
});

const statusNone: BackfillStatus = { state: "none" };

function countsStatus(
  state: BackfillStatus["state"],
  counts: NonNullable<BackfillStatus["counts"]>,
  estimated = 400,
): BackfillStatus {
  return {
    state,
    backfill_id: "018f3a1b-0000-7000-8000-0000000000b1",
    window: "6m",
    estimated_messages: estimated,
    counts,
  };
}

type StubOptions = {
  /** Status rows served per GET, consumed one at a time (last repeats). */
  statuses: BackfillStatus[];
  preview?: BackfillPreview;
  /** The status the start POST flips the next GET to. */
  onStart?: BackfillStatus;
};

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function stubApi(options: StubOptions) {
  const calls: Request[] = [];
  const statuses = [...options.statuses];
  let started = false;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (request: Request) => {
      calls.push(request);
      const url = new URL(request.url);
      const path = url.pathname;
      if (path.endsWith("/backfill/preview")) {
        return jsonResponse(options.preview ?? previewOf(400));
      }
      if (path.endsWith("/backfill") && request.method === "POST") {
        started = true;
        return jsonResponse(options.onStart ?? statusNone, 202);
      }
      if (path.endsWith("/backfill") && request.method === "GET") {
        if (started && options.onStart) {
          const row = statuses.length > 1 ? statuses.shift() : statuses[0];
          return jsonResponse(row ?? options.onStart);
        }
        return jsonResponse(statuses[0] ?? statusNone);
      }
      throw new Error(`unstubbed: ${request.method} ${path}`);
    }),
  );
  return calls;
}

function render(ui: ReactNode) {
  return rtlRender(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <LocaleProvider initial="en">{ui}</LocaleProvider>
    </QueryClientProvider>,
  );
}

function requestsTo(calls: Request[], suffix: string, method: string) {
  return calls.filter(
    (r) => new URL(r.url).pathname.endsWith(suffix) && r.method === method,
  );
}

beforeEach(() => {
  vi.stubGlobal("scrollTo", vi.fn());
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("the connect-time backfill payoff", () => {
  it("auto-loads the scope estimate without a click, and does not spend until start", async () => {
    const calls = stubApi({ statuses: [statusNone], preview: previewOf(1234) });
    render(<BackfillPanel provider="gmail" />);

    // The estimate appears with no user interaction — honest scope up front.
    expect(await screen.findByText(/~1,234/)).toBeTruthy();
    expect(requestsTo(calls, "/backfill/preview", "POST").length).toBe(1);
    // But nothing has been imported: no start POST fired on its own.
    expect(requestsTo(calls, "/backfill", "POST").length).toBe(0);
  });

  it("starts the import only on the explicit consent click", async () => {
    const calls = stubApi({
      statuses: [statusNone],
      preview: previewOf(400),
      onStart: countsStatus("running", { captured: 0 }),
    });
    render(<BackfillPanel provider="gmail" />);

    await screen.findByText(/~400/);
    await userEvent.click(
      screen.getByRole("button", { name: /Start the import/ }),
    );

    await waitFor(() =>
      expect(requestsTo(calls, "/backfill", "POST").length).toBe(1),
    );
  });

  it("renders the three headline figures — captured, people, companies — from the run counts", async () => {
    stubApi({
      statuses: [
        countsStatus("running", {
          captured: 128,
          people_created: 47,
          organizations_created: 12,
          messages_scanned: 150,
        }),
      ],
    });
    render(<BackfillPanel provider="gmail" />);

    expect(await screen.findByText("128")).toBeTruthy();
    expect(screen.getByText("47")).toBeTruthy();
    expect(screen.getByText("12")).toBeTruthy();
    expect(screen.getByText("Emails captured")).toBeTruthy();
    expect(screen.getByText("People")).toBeTruthy();
    expect(screen.getByText("Companies")).toBeTruthy();
  });

  it("shows the celebratory done state when the run completes", async () => {
    stubApi({
      statuses: [
        countsStatus("done", {
          captured: 512,
          people_created: 90,
          organizations_created: 20,
          messages_scanned: 600,
        }),
      ],
    });
    render(<BackfillPanel provider="gmail" />);

    expect(await screen.findByText(/History import complete/i)).toBeTruthy();
    expect(screen.getByText("512")).toBeTruthy();
  });

  it("surfaces an honest error class without hiding the counts captured so far", async () => {
    stubApi({
      statuses: [countsStatus("error", { captured: 40, people_created: 9 })],
    });
    render(<BackfillPanel provider="gmail" />);

    expect(await screen.findByText("40")).toBeTruthy();
    expect(
      screen.getByText(/everything captured so far is kept/i),
    ).toBeTruthy();
  });
});
