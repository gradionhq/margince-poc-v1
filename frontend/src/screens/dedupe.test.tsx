/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { components } from "../api/schema";
import { LocaleProvider } from "../i18n";
import { DedupeScreen } from "./dedupe";

// The review queue's two obligations. First, a decision is a WRITE with a
// winner, and which record wins is the reader's pick rather than the order the
// detector happened to return — a merge that guessed would destroy the wrong
// row. Second, two writes must never run at once: a disposition and an undo
// reset each other's state in their own `onSuccess`, so whichever landed second
// would leave the screen describing a decision that is no longer the truth.
//
// The evidence rows are deliberately NOT re-derived here. They are what the
// detector saw at detection time, so the screen renders them and this file only
// asserts they reach the reader intact.

type Candidate = components["schemas"]["DedupeCandidate"];

function pair(overrides: Partial<Candidate> = {}): Candidate {
  return {
    id: "dc-1",
    entity_type: "person",
    left_id: "p-1",
    right_id: "p-2",
    confidence: 0.92,
    evidence: [
      {
        field: "full_name",
        left_value: "Katharina Brandt",
        right_value: "Katharina Brandt",
        signal: "agree",
        score: 1,
      },
      {
        field: "email",
        left_value: "k.brandt@nordwerk.test",
        right_value: null,
        signal: "collide",
      },
    ],
    status: "open",
    created_at: "2026-08-11T09:20:00Z",
    ...overrides,
  };
}

type Sent = { method: string; path: string; body?: unknown };

// One stub for the whole screen: the queue read, the two writes, and the record
// reads the pair line resolves its names through. `sent` is what the assertions
// about the WRITE read, because a decision that renders correctly and posts the
// wrong winner is the failure this screen cannot afford.
function backend(
  candidates: Candidate[],
  options: { hangDisposition?: boolean } = {},
): { sent: Sent[] } {
  const sent: Sent[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const request = input instanceof Request ? input : null;
      const url = new URL(
        request ? request.url : String(input),
        "http://localhost",
      );
      const method = (request?.method ?? init?.method ?? "GET").toUpperCase();
      const raw = request ? await request.clone().text() : String(init?.body);
      const json = (body: unknown) =>
        new Response(JSON.stringify(body), {
          status: 200,
          headers: { "content-type": "application/json" },
        });

      if (url.pathname.endsWith("/dedupe/candidates") && method === "GET") {
        return json({ data: candidates, page: { next_cursor: null } });
      }
      if (url.pathname.includes("/disposition")) {
        sent.push({
          method,
          path: url.pathname,
          body: raw ? JSON.parse(raw) : undefined,
        });
        if (options.hangDisposition) {
          return new Promise<Response>(() => {});
        }
        return json({ ...candidates[0], status: "not_a_duplicate" });
      }
      if (url.pathname.includes("/undo")) {
        sent.push({ method, path: url.pathname });
        return json({ ...candidates[0], status: "open" });
      }
      // The pair line resolves both records by name; an unanswered read would
      // fall back to the raw id and say nothing about the decision under test.
      return json({ id: "p-1", full_name: "Katharina Brandt" });
    }),
  );
  return { sent };
}

function show() {
  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <LocaleProvider initial="en">
        <DedupeScreen />
      </LocaleProvider>
    </QueryClientProvider>,
  );
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("the duplicate review queue", () => {
  it("draws the pair as one card: what it is, how sure, and what the detector saw", async () => {
    backend([pair()]);
    show();

    expect(await screen.findByText("Person")).toBeTruthy();
    // The confidence is a reading beside the title, not a bare number.
    expect(screen.getByText(/Match confidence:\s*92%/)).toBeTruthy();
    // The evidence reaches the reader as the detector recorded it, including
    // the side that holds nothing — an absent value is a fact about the pair.
    expect(screen.getByText("k.brandt@nordwerk.test")).toBeTruthy();
    expect(screen.getByText("full_name")).toBeTruthy();
    // A colliding field is marked as such in the row, for the CSS to speak to.
    const colliding = document.querySelector('tr[data-signal="collide"]');
    expect(colliding).toBeTruthy();
  });

  it("merges into the record the reader picked, not the one listed first", async () => {
    const { sent } = backend([pair()]);
    show();
    const user = userEvent.setup();

    await user.click(await screen.findByRole("radio", { name: /keep right/i }));
    await user.click(
      screen.getByRole("button", { name: /merge into selected/i }),
    );

    await waitFor(() => expect(sent).toHaveLength(1));
    expect(sent[0].path).toContain("/dedupe/candidates/dc-1/disposition");
    expect(sent[0].body).toEqual({
      disposition: "merge",
      winner_id: "p-2",
    });
  });

  it("dismissing a pair names no winner — nothing is being merged into anything", async () => {
    const { sent } = backend([pair()]);
    show();
    const user = userEvent.setup();

    await user.click(
      await screen.findByRole("button", { name: /not a duplicate/i }),
    );

    await waitFor(() => expect(sent).toHaveLength(1));
    expect(sent[0].body).toEqual({
      disposition: "not_a_duplicate",
      winner_id: undefined,
    });
  });

  it("offers the way back once a decision lands, and takes it", async () => {
    const { sent } = backend([pair()]);
    show();
    const user = userEvent.setup();

    await user.click(
      await screen.findByRole("button", { name: /not a duplicate/i }),
    );
    await user.click(await screen.findByRole("button", { name: "Undo" }));

    await waitFor(() => expect(sent).toHaveLength(2));
    expect(sent[1].path).toContain("/dedupe/candidates/dc-1/undo");
  });

  // The invariant the two mutations' `onSuccess` handlers make necessary: while
  // one decision is in flight nothing else may start, or the second to land
  // resets the first's state and the screen describes a decision that is not
  // the truth. The pressed verb reads as pending — full ink, focus kept — and
  // everything else reads as refused, which are deliberately not the same.
  it("refuses every other verb while one decision is in flight, and only the pressed one turns", async () => {
    backend([pair(), pair({ id: "dc-2", left_id: "p-3", right_id: "p-4" })], {
      hangDisposition: true,
    });
    show();
    const user = userEvent.setup();

    const merges = await screen.findAllByRole("button", {
      name: /merge into selected/i,
    });
    await user.click(merges[0]);

    const pressed = merges[0];
    const sibling = within(
      pressed.closest("article") ?? document.body,
    ).getByRole("button", { name: /not a duplicate/i });

    await waitFor(() => expect(pressed.getAttribute("aria-busy")).toBe("true"));
    // Pending is not refusal: the reader keeps focus on what they pressed.
    expect(pressed.hasAttribute("disabled")).toBe(false);
    // Its sibling on the same pair, and the other pair entirely, are refused.
    expect(sibling.hasAttribute("disabled")).toBe(true);
    const other = (
      await screen.findAllByRole("button", { name: /merge into selected/i })
    )[1];
    expect(other.hasAttribute("disabled")).toBe(true);
    expect(other.getAttribute("aria-busy")).not.toBe("true");
  });

  it("says the queue is clear rather than drawing an empty card", async () => {
    backend([]);
    show();

    expect(await screen.findByText(/No duplicates waiting/i)).toBeTruthy();
    expect(
      screen.queryByRole("button", { name: /merge into selected/i }),
    ).toBeNull();
  });
});

// The signal is what a reviewer's decision turns on, so it has to be readable —
// and readable by more than colour. Red text alone reaches nobody who cannot see
// the difference, and it left the other two signals told apart by nothing at all.
describe("the queue names each signal in words", () => {
  it("distinguishes all three signals by their words, not only by colour", async () => {
    backend([
      pair({
        evidence: [
          {
            field: "full_name",
            left_value: "A",
            right_value: "A",
            signal: "agree",
          },
          {
            field: "email",
            left_value: "a@x.test",
            right_value: "b@x.test",
            signal: "collide",
          },
          {
            field: "phone",
            left_value: "+49",
            right_value: null,
            signal: "one_sided",
          },
        ],
      }),
    ]);
    show();
    expect(await screen.findByText("conflict")).toBeTruthy();
    expect(screen.getByText("agree")).toBeTruthy();
    expect(screen.getByText("one side only")).toBeTruthy();
  });

  // The wire types the field as a plain string, not a closed enum. A signal this
  // release has no word for is still one the detector acted on, so it renders as
  // itself rather than as a blank cell that reads as no signal.
  it("renders a signal it has no word for as the wire's own value", async () => {
    backend([
      pair({
        evidence: [
          {
            field: "vat_id",
            left_value: "DE1",
            right_value: "DE1",
            signal: "normalised_match",
          },
        ],
      }),
    ]);
    show();
    expect(await screen.findByText("normalised_match")).toBeTruthy();
  });
});
