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
import { afterEach, describe, expect, it, vi } from "vitest";
import { activityTimeline } from "../design-system/activitytimeline";
import { LocaleProvider } from "../i18n";
import { isTranscriptActivity, TranscriptReadCard } from "./transcriptread";

// Reading a meeting transcript for the next steps it states (S-E04.3).
//
// The three outcomes are the whole point of the surface and are asserted apart
// on purpose: still reading, read it and it stated nothing, and could not read
// it are different answers about the transcript, and a rep who cannot tell the
// last two apart either distrusts a correct empty answer or trusts a broken
// one.

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  window.location.hash = "";
});

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

type Report = {
  read_id: string;
  activity_id: string;
  status: "queued" | "running" | "done" | "failed";
  status_detail?: string | null;
  line_count: number;
  proposal_ids: string[];
  created_at: string;
};

function report(overrides: Partial<Report>): Report {
  return {
    read_id: "rd-1",
    activity_id: "a-1",
    status: "done",
    line_count: 48,
    proposal_ids: [],
    created_at: "2026-08-01T09:00:00Z",
    ...overrides,
  };
}

/**
 * Stubs the three transcript-reading endpoints.
 *
 * `stored` is the reading this activity already has, served by BOTH the latest
 * read and the read-by-id — one reading behind two doors, which is what the
 * card joins them into. Absent, the latest read answers 404: the honest "never
 * read", and the state a rep meets on a transcript nobody has looked at.
 */
function stubReads(options: {
  stored?: Report;
  read?: () => Response;
  post?: () => Response;
}) {
  const calls: string[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const request = input instanceof Request ? input : null;
      const url = new URL(
        request ? request.url : String(input),
        "https://test.local",
      );
      const method = request?.method ?? init?.method ?? "GET";
      calls.push(`${method} ${url.pathname}`);
      if (method === "POST") {
        return (
          options.post ??
          (() => jsonResponse({ read_id: "rd-1", status: "queued" }, 202))
        )();
      }
      if (url.pathname.endsWith("/transcript-proposals/latest")) {
        return options.stored
          ? jsonResponse(options.stored)
          : jsonResponse({}, 404);
      }
      return (
        options.read ?? (() => jsonResponse(options.stored ?? report({})))
      )();
    }),
  );
  return { calls };
}

describe("reading a transcript for its next steps", () => {
  it("starts a reading on click and shows what it staged, with a way into the inbox", async () => {
    const user = userEvent.setup();
    const { calls } = stubReads({
      read: () =>
        jsonResponse(report({ proposal_ids: ["ap-1", "ap-2", "ap-3"] })),
    });
    render(<TranscriptReadCard activityId="a-1" />);

    await user.click(screen.getByRole("button", { name: "Read transcript" }));
    await waitFor(() =>
      expect(
        calls.some(
          (call) =>
            call === "POST /v1/activities/a-1/transcript-proposals" ||
            call.endsWith("/activities/a-1/transcript-proposals"),
        ),
      ).toBe(true),
    );

    // How much was read, so a cited line reads against the size of the whole.
    expect(await screen.findByText("48 lines read")).toBeTruthy();
    expect(
      screen.getByText("3 next steps waiting for your review"),
    ).toBeTruthy();
    // The 🟡 tier is drawn, never spelled as an emoji.
    expect(screen.getByRole("img", { name: "confirm-first" })).toBeTruthy();

    await user.click(screen.getByRole("button", { name: "Open inbox" }));
    expect(window.location.hash).toContain("inbox");
  });

  it("says a transcript stated nothing, in the server's own words, and offers no inbox trip", async () => {
    stubReads({
      stored: report({
        status_detail: "Nobody committed to anything in this call.",
      }),
    });
    render(<TranscriptReadCard activityId="a-1" />);

    expect(
      await screen.findByText("Nobody committed to anything in this call."),
    ).toBeTruthy();
    expect(screen.getByText("48 lines read")).toBeTruthy();
    expect(screen.getByText("Done")).toBeTruthy();
    // A correct empty answer is not a queue of work: nothing to review, and
    // nowhere to go.
    expect(screen.queryByRole("button", { name: "Open inbox" })).toBeNull();
    expect(screen.queryByRole("img", { name: "confirm-first" })).toBeNull();
  });

  it("explains a reading it could not finish, and never as an empty result", async () => {
    stubReads({
      stored: report({
        status: "failed",
        line_count: 0,
        status_detail: "The model refused: this transcript is too long.",
      }),
    });
    render(<TranscriptReadCard activityId="a-1" />);

    expect(
      await screen.findByText(
        "The model refused: this transcript is too long.",
      ),
    ).toBeTruthy();
    expect(screen.getByText("Failed")).toBeTruthy();
    expect(screen.queryByText("Done")).toBeNull();
    expect(
      screen.queryByText(
        "Read in full. This conversation states no next steps.",
      ),
    ).toBeNull();
    expect(screen.queryByRole("button", { name: "Open inbox" })).toBeNull();
  });

  it("falls back to its own words when a terminal reading carries no detail", async () => {
    stubReads({ stored: report({ status: "failed" }) });
    render(<TranscriptReadCard activityId="a-1" />);

    expect(
      await screen.findByText(
        "This transcript could not be read. Nothing was staged.",
      ),
    ).toBeTruthy();
  });

  it("says a reading is still going, and claims no outcome while it is", async () => {
    stubReads({ stored: report({ status: "running", line_count: 0 }) });
    render(<TranscriptReadCard activityId="a-1" />);

    expect(await screen.findByText("Reading…")).toBeTruthy();
    // A reading in flight has read nothing yet as far as the reader is
    // concerned: no line count, no verdict, no empty-result notice.
    expect(screen.queryByText("0 lines read")).toBeNull();
    expect(
      screen.queryByText(
        "Read in full. This conversation states no next steps.",
      ),
    ).toBeNull();
    expect(screen.queryByRole("button", { name: "Open inbox" })).toBeNull();
  });

  it("shows a reading that finished after the rep navigated away", async () => {
    stubReads({ stored: report({ proposal_ids: ["ap-1"] }) });
    render(<TranscriptReadCard activityId="a-1" />);

    // Nobody pressed anything in this tab; the latest read is what makes a
    // finished reading findable at all.
    expect(
      await screen.findByText("1 next step waiting for your review"),
    ).toBeTruthy();
  });

  it("offers a first reading, and no outcome, on a transcript nobody has read", async () => {
    stubReads({});
    render(<TranscriptReadCard activityId="a-1" />);

    expect(
      await screen.findByRole("button", { name: "Read transcript" }),
    ).toBeTruthy();
    await waitFor(() => expect(screen.queryByText("Done")).toBeNull());
    expect(screen.queryByText("48 lines read")).toBeNull();
  });

  it("surfaces an unconfigured model as its own cause, not a generic failure", async () => {
    const user = userEvent.setup();
    stubReads({ post: () => jsonResponse({ title: "not implemented" }, 501) });
    render(<TranscriptReadCard activityId="a-1" />);

    await user.click(screen.getByRole("button", { name: "Read transcript" }));
    expect(
      await screen.findByText(
        "Transcript reading is not configured on this server.",
      ),
    ).toBeTruthy();
  });
});

// Which rows carry the offer. `source_system` is an idempotency-key part on
// every activity kind, so the gate is the pair — a mail connector stamping
// some future value there must not put "read this transcript" on an email.
describe("which activity carries a transcript", () => {
  const base = {
    id: "a-1",
    occurred_at: "2026-08-01T09:00:00Z",
    is_done: false,
    source: "manual",
    captured_by: "human:u-1",
    created_at: "2026-08-01T09:00:00Z",
    updated_at: "2026-08-01T09:00:00Z",
  };

  it("recognises a call or a meeting logged as a transcript", () => {
    expect(
      isTranscriptActivity({
        ...base,
        kind: "call",
        source_system: "transcript",
      }),
    ).toBe(true);
    expect(
      isTranscriptActivity({
        ...base,
        kind: "meeting",
        source_system: "transcript",
      }),
    ).toBe(true);
  });

  it("leaves an ordinary meeting and a mail-sourced row alone", () => {
    expect(isTranscriptActivity({ ...base, kind: "meeting" })).toBe(false);
    expect(
      isTranscriptActivity({
        ...base,
        kind: "email",
        source_system: "transcript",
      }),
    ).toBe(false);
  });

  it("attaches the reading card to the transcript row of a timeline, and only it", () => {
    const [transcript, note] = activityTimeline([
      { ...base, kind: "call", source_system: "transcript" },
      { ...base, id: "a-2", kind: "note" },
    ]);
    expect(transcript.detail).toBeTruthy();
    expect(note.detail).toBeUndefined();
  });
});
