/** @vitest-environment jsdom */
import "@testing-library/jest-dom/vitest";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  fireEvent,
  render as rtlRender,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider } from "../i18n";
import { ConsentPurposesCard, PrivacyInboxCard } from "./privacy";

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

const PURPOSES = {
  data: [
    {
      id: "p1",
      workspace_id: "w",
      key: "transactional",
      label: "Deal messages",
      requires_double_opt_in: false,
      created_at: "2026-01-01T00:00:00Z",
    },
    {
      id: "p2",
      workspace_id: "w",
      key: "marketing_email",
      label: "Marketing",
      requires_double_opt_in: true,
      created_at: "2026-01-01T00:00:00Z",
    },
  ],
  page: { next_cursor: null, has_more: false },
};

// Records every request so a test can assert what actually went to the
// server — the request body IS the contract for a purpose write (Task 6's
// consent.test.tsx harness shape, copied per-file per house convention).
type Sent = { key: string; url: string; body: unknown };

function stubRoutes(
  overrides: Record<string, () => Response> = {},
  sent: Sent[] = [],
) {
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
      let body: unknown = null;
      if (method !== "GET") {
        try {
          body = request
            ? await request.json()
            : JSON.parse(String(init?.body));
        } catch {
          body = null;
        }
      }
      sent.push({ key, url: url.pathname + url.search, body });
      const override = overrides[key];
      if (override) return override();
      if (key === "GET /consent-purposes") return jsonResponse(PURPOSES);
      // Falls back to DSRS (declared below, but already initialized by the
      // time any `it` callback runs — module evaluation finishes before test
      // execution starts) so every PrivacyInboxCard test gets seed rows
      // without repeating the override.
      if (key === "GET /data-subject-requests") return jsonResponse(DSRS);
      if (key === "GET /users")
        return jsonResponse({
          data: [],
          page: { next_cursor: null, has_more: false },
        });
      return jsonResponse({});
    }),
  );
  return sent;
}

beforeEach(() => localStorage.setItem("margince.workspaceSlug", "acme"));
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.useRealTimers();
  vi.restoreAllMocks();
});

describe("ConsentPurposesCard", () => {
  it("lists purposes and marks the ones needing double opt-in", async () => {
    stubRoutes();
    render(<ConsentPurposesCard />);
    expect(await screen.findByText(/Marketing/)).toBeInTheDocument();
    expect(screen.getByText(/DOI/)).toBeInTheDocument();
  });

  // G-3
  it("creates a purpose", async () => {
    const sent = stubRoutes({
      "POST /consent-purposes": () =>
        jsonResponse(
          {
            id: "p3",
            workspace_id: "w",
            key: "events",
            label: "Events",
            requires_double_opt_in: false,
            created_at: "2026-07-15T00:00:00Z",
          },
          201,
        ),
    });
    render(<ConsentPurposesCard />);
    await userEvent.click(
      await screen.findByRole("button", { name: /add purpose/i }),
    );
    await userEvent.type(screen.getByLabelText(/key/i), "events");
    await userEvent.type(screen.getByLabelText(/label/i), "Events");
    await userEvent.click(
      screen.getByRole("button", { name: /create purpose/i }),
    );
    await waitFor(() =>
      expect(
        sent.filter((s) => s.key === "POST /consent-purposes"),
      ).toHaveLength(1),
    );
    // The onSuccess invalidation refetches the purposes GET, appending to the
    // same `sent` array — filter for the POST specifically rather than
    // trusting it stayed last.
    const posts = sent.filter((s) => s.key === "POST /consent-purposes");
    expect(posts.at(-1)?.body).toEqual({
      key: "events",
      label: "Events",
      requires_double_opt_in: false,
    });
  });

  // A purpose has no PATCH and no DELETE — say so before they commit, not after.
  it("warns that a purpose cannot be renamed or removed", async () => {
    stubRoutes();
    render(<ConsentPurposesCard />);
    await userEvent.click(
      await screen.findByRole("button", { name: /add purpose/i }),
    );
    expect(
      screen.getByText(/cannot be renamed or removed/i),
    ).toBeInTheDocument();
  });

  it("refuses to submit without a key and a label", async () => {
    stubRoutes();
    render(<ConsentPurposesCard />);
    await userEvent.click(
      await screen.findByRole("button", { name: /add purpose/i }),
    );
    expect(
      screen.getByRole("button", { name: /create purpose/i }),
    ).toBeDisabled();
  });

  it("surfaces a create failure inline without losing the typed values", async () => {
    stubRoutes({
      "POST /consent-purposes": () =>
        jsonResponse(
          { title: "duplicate key", status: 422, code: "invalid" },
          422,
        ),
    });
    render(<ConsentPurposesCard />);
    await userEvent.click(
      await screen.findByRole("button", { name: /add purpose/i }),
    );
    await userEvent.type(screen.getByLabelText(/key/i), "transactional");
    await userEvent.type(screen.getByLabelText(/label/i), "Dupe");
    await userEvent.click(
      screen.getByRole("button", { name: /create purpose/i }),
    );
    expect(await screen.findByText(/duplicate key/i)).toBeInTheDocument();
    expect(screen.getByLabelText(/key/i)).toHaveValue("transactional");
  });
});

const DSRS = {
  data: [
    {
      id: "d1",
      kind: "erasure",
      subject_ref: "8f3a-person-uuid",
      status: "open",
      due_at: "2026-08-01T00:00:00Z",
      created_at: "2026-07-01T00:00:00Z",
    },
    {
      id: "d2",
      kind: "access",
      subject_ref: "anna@acme.test",
      status: "fulfilled",
      resolution: "sent by post",
      due_at: "2026-07-12T00:00:00Z",
      created_at: "2026-06-01T00:00:00Z",
    },
  ],
  page: { next_cursor: null, has_more: false },
};

// The facet bar stays visible for the whole queue (it is never hidden while
// a row is open), and its option labels are the very same status words a
// row's own transition buttons use ("in progress", "fulfilled" ⊇ "fulfil",
// "rejected" ⊇ "reject") — a bare getByRole/queryByRole for one of those
// words matches both the facet button and the row's own button. Scope to
// the row under test instead, same idiom as consent.test.tsx's
// findConsentRow.
async function findDsrRow(subjectRef: string) {
  // An expanded row repeats its own subject_ref (the collapsed toggle's
  // summary, then again inside the expanded detail panel) — both hits share
  // the same ancestor row, so take the first rather than assume there's
  // only one match.
  const [match] = await screen.findAllByText(subjectRef);
  const row = match.closest(".dsr-row");
  if (!(row instanceof HTMLElement)) {
    throw new Error(`dsr row for "${subjectRef}" not found`);
  }
  return row;
}

describe("PrivacyInboxCard", () => {
  it("binds the status filter server-side, never a client re-slice", async () => {
    const sent = stubRoutes({
      "GET /data-subject-requests": () => jsonResponse(DSRS),
    });
    render(<PrivacyInboxCard />);
    await screen.findByText(/anna@acme.test/);
    await userEvent.click(screen.getByRole("button", { name: /^open$/i }));
    await waitFor(() =>
      expect(sent.some((s) => s.url.includes("status=open"))).toBe(true),
    );
    // Both rows still came back from the stub; a client re-slice would have
    // hidden the fulfilled one without ever asking the server.
    expect(
      sent.filter((s) => s.key === "GET /data-subject-requests").length,
    ).toBeGreaterThan(1);
  });

  // The approved design is a queue: one row expands in place while its
  // siblings and the facet bar stay on screen, so an officer working a case
  // never loses sight of what else is waiting. Pins the invariant directly —
  // without it, filtering the row list down to just the expanded one (or
  // hiding the facet bar) would pass every other test in this file silently.
  it("keeps sibling rows and the facet bar visible while one row is expanded", async () => {
    stubRoutes();
    render(<PrivacyInboxCard />);
    await userEvent.click(
      await screen.findByRole("button", { name: /8f3a-person-uuid/i }),
    );
    expect(screen.getByText(/anna@acme.test/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /^open$/i })).toBeInTheDocument();
  });

  // FIX-1: due_at is a statutory deadline. Rendered in a hardcoded
  // Europe/Berlin it shows the wrong calendar day to anyone outside CET —
  // this due_at is 2026-08-01T00:00:00Z, which is 1 Aug in Berlin (+02:00)
  // but still 31 Jul in New York.
  it("renders the due date in the viewer's timezone, not a hardcoded one", async () => {
    // The card asks the platform which zone the viewer is in; pretend it's
    // New York. formatDate takes the zone as an argument and doesn't consult
    // resolvedOptions, so this spy only redirects the card's own lookup.
    vi.spyOn(Intl.DateTimeFormat.prototype, "resolvedOptions").mockReturnValue({
      timeZone: "America/New_York",
    } as Intl.ResolvedDateTimeFormatOptions);
    stubRoutes({ "GET /data-subject-requests": () => jsonResponse(DSRS) });
    render(<PrivacyInboxCard />);
    await screen.findByText(/8f3a-person-uuid/);
    // This codebase's locked locale convention (format.ts's INTL_LOCALE,
    // "A100: unconfigured English is en-GB, not en-US") renders numeric
    // dates DD/MM/YYYY: New York renders 31/07/2026; a hardcoded
    // Europe/Berlin would instead render 01/08/2026 — pin the one this
    // code actually emits, not every format it never does.
    expect(screen.getByText(/31\/07\/2026/)).toBeInTheDocument();
  });

  it("offers only the transitions the server would accept", async () => {
    stubRoutes();
    render(<PrivacyInboxCard />);
    await userEvent.click(
      await screen.findByRole("button", { name: /8f3a-person-uuid/i }),
    );
    const row = await findDsrRow("8f3a-person-uuid");
    expect(
      within(row).getByRole("button", { name: /in progress/i }),
    ).toBeInTheDocument();
    expect(
      within(row).getByRole("button", { name: /fulfil/i }),
    ).toBeInTheDocument();
  });

  it("offers no transition on a closed request — a closed request never reopens", async () => {
    stubRoutes();
    render(<PrivacyInboxCard />);
    await userEvent.click(
      await screen.findByRole("button", { name: /anna@acme.test/i }),
    );
    const row = await findDsrRow("anna@acme.test");
    expect(
      within(row).queryByRole("button", { name: /in progress/i }),
    ).not.toBeInTheDocument();
    expect(within(row).getByText(/closed/i)).toBeInTheDocument();
  });

  it("holds a close until a resolution is written — the server 422s without one", async () => {
    stubRoutes();
    render(<PrivacyInboxCard />);
    await userEvent.click(
      await screen.findByRole("button", { name: /8f3a-person-uuid/i }),
    );
    const row = await findDsrRow("8f3a-person-uuid");
    expect(within(row).getByRole("button", { name: /reject/i })).toBeDisabled();
    await userEvent.type(
      screen.getByLabelText(/resolution/i),
      "not a data subject",
    );
    expect(within(row).getByRole("button", { name: /reject/i })).toBeEnabled();
  });

  it("flags an overdue request against the injected clock", async () => {
    vi.setSystemTime(new Date("2026-08-02T00:00:00Z"));
    stubRoutes();
    render(<PrivacyInboxCard />);
    expect(await screen.findByText(/overdue/i)).toBeInTheDocument();
  });

  // The stale-row race: another admin moved it first, so our offered
  // transition is now illegal and the PATCH 422s. Note this is NOT the
  // approvals' 409 already_decided — isAlreadyDecided does not apply. The
  // stub mirrors the real wire shape (consent/dsr.go's UpdateDSR via
  // writeConsentErr → httperr.Validation("status", "invalid", reason)):
  // top-level code is always "validation_error", and the field that failed
  // rides in details.errors[0].field — NOT in the top-level code, which the
  // create-purpose test above shows is reused for every validation failure.
  it("re-reads and explains when the request moved on underneath us", async () => {
    stubRoutes({
      "PATCH /data-subject-requests/d1": () =>
        jsonResponse(
          {
            title: "Unprocessable Entity",
            detail: "open → fulfilled is not a legal transition",
            status: 422,
            code: "validation_error",
            details: {
              errors: [
                {
                  field: "status",
                  code: "invalid",
                  message: "open → fulfilled is not a legal transition",
                },
              ],
            },
          },
          422,
        ),
    });
    render(<PrivacyInboxCard />);
    await userEvent.click(
      await screen.findByRole("button", { name: /8f3a-person-uuid/i }),
    );
    const row = await findDsrRow("8f3a-person-uuid");
    await userEvent.type(screen.getByLabelText(/resolution/i), "done");
    await userEvent.click(within(row).getByRole("button", { name: /reject/i }));
    expect(await screen.findByText(/moved on/i)).toBeInTheDocument();
  });

  // A patch failure that is NOT the illegal-transition 422 must
  // never wear the "moved on" copy — that would tell the officer a colleague
  // made a decision that never happened. A 403 (no `details.errors`, code
  // "permission_denied") gets the server's own honest detail instead.
  it("tells the truth about a non-transition patch failure instead of claiming it moved on", async () => {
    stubRoutes({
      "PATCH /data-subject-requests/d1": () =>
        jsonResponse(
          {
            title: "Forbidden",
            detail: "assigning this request is outside your role",
            status: 403,
            code: "permission_denied",
          },
          403,
        ),
    });
    render(<PrivacyInboxCard />);
    await userEvent.click(
      await screen.findByRole("button", { name: /8f3a-person-uuid/i }),
    );
    const row = await findDsrRow("8f3a-person-uuid");
    await userEvent.type(screen.getByLabelText(/resolution/i), "done");
    await userEvent.click(within(row).getByRole("button", { name: /reject/i }));
    expect(
      await screen.findByText(/assigning this request is outside your role/i),
    ).toBeInTheDocument();
    expect(screen.queryByText(/moved on/i)).not.toBeInTheDocument();
  });

  it("assigns from the roster", async () => {
    const sent = stubRoutes({
      "GET /users": () =>
        jsonResponse({
          data: [
            {
              id: "u1",
              workspace_id: "w",
              email: "dpo@acme.test",
              display_name: "Dana DPO",
              status: "active",
              is_agent: false,
            },
            {
              id: "u2",
              workspace_id: "w",
              email: "bot@acme.test",
              display_name: "Bot",
              status: "active",
              is_agent: true,
            },
          ],
          page: { next_cursor: null, has_more: false },
        }),
      "PATCH /data-subject-requests/d1": () =>
        jsonResponse({ ...DSRS.data[0], assignee_id: "u1" }),
    });
    render(<PrivacyInboxCard />);
    await userEvent.click(
      await screen.findByRole("button", { name: /8f3a-person-uuid/i }),
    );
    const picker = await screen.findByLabelText(/assignee/i);
    expect(
      screen.queryByRole("option", { name: "Bot" }),
    ).not.toBeInTheDocument();
    await userEvent.selectOptions(picker, "u1");
    await waitFor(() =>
      expect(
        sent.filter((s) => s.key === "PATCH /data-subject-requests/d1"),
      ).toHaveLength(1),
    );
    // The load-bearing invariant: assignee_id: "u1" actually went on the
    // wire, and NOTHING ELSE did — not even an explicit null for status or
    // resolution. The server's UPDATE sets `coalesce($n, col)` for every
    // field, so a stray `status: null` or `resolution: null` in this body
    // would silently no-op those columns rather than leave them untouched;
    // toEqual (not objectContaining) proves no such key rode along.
    const patches = sent.filter(
      (s) => s.key === "PATCH /data-subject-requests/d1",
    );
    expect(patches[0]?.body).toEqual({ assignee_id: "u1" });
  });

  it("renders a scoped rep's 403 honestly", async () => {
    stubRoutes({
      "GET /data-subject-requests": () =>
        jsonResponse(
          {
            title: "permission denied",
            status: 403,
            code: "permission_denied",
          },
          403,
        ),
    });
    render(<PrivacyInboxCard />);
    expect(await screen.findByText(/permission denied/i)).toBeInTheDocument();
  });
});

describe("opening a DSR (G-2)", () => {
  // An erasure fulfils by resolving subject_ref to a person id. Free text
  // there means the server refuses (BE-2) — so the form must not offer it.
  it("requires a picked person for an erasure", async () => {
    stubRoutes();
    render(<PrivacyInboxCard />);
    await userEvent.click(
      await screen.findByRole("button", { name: /new request/i }),
    );
    await userEvent.selectOptions(screen.getByLabelText(/kind/i), "erasure");
    expect(screen.getByLabelText(/person/i)).toBeInTheDocument();
    expect(
      screen.queryByLabelText(/subject reference/i),
    ).not.toBeInTheDocument();
  });

  it("allows a free-text subject for an access request — the subject may not be in the CRM", async () => {
    stubRoutes();
    render(<PrivacyInboxCard />);
    await userEvent.click(
      await screen.findByRole("button", { name: /new request/i }),
    );
    await userEvent.selectOptions(screen.getByLabelText(/kind/i), "access");
    expect(screen.getByLabelText(/subject reference/i)).toBeInTheDocument();
  });

  it("says an access request is fulfilled by hand — nothing is exported", async () => {
    stubRoutes();
    render(<PrivacyInboxCard />);
    await userEvent.click(
      await screen.findByRole("button", { name: /new request/i }),
    );
    await userEvent.selectOptions(screen.getByLabelText(/kind/i), "access");
    expect(screen.getByText(/fulfilled by hand/i)).toBeInTheDocument();
  });

  it("requires a due date — the statutory clock is not optional", async () => {
    stubRoutes();
    render(<PrivacyInboxCard />);
    await userEvent.click(
      await screen.findByRole("button", { name: /new request/i }),
    );
    await userEvent.selectOptions(screen.getByLabelText(/kind/i), "access");
    await userEvent.type(
      screen.getByLabelText(/subject reference/i),
      "anna@acme.test",
    );
    expect(
      screen.getByRole("button", { name: /open request/i }),
    ).toBeDisabled();
  });

  // The load-bearing property (BE-2): the erasure fulfiller resolves
  // subject_ref to a person id, so an erasure request must be incapable of
  // naming a subject the server cannot erase. The form only enforces this by
  // construction (RecordPicker, no text input) — this proves the picked
  // person's uuid, not its display name, is what actually reaches the wire.
  it("sends the picked person's uuid as subject_ref for an erasure request", async () => {
    const sent = stubRoutes({
      "GET /people": () =>
        jsonResponse({
          data: [
            {
              id: "3fa85f64-5717-4562-b3fc-2c963f66afa6",
              full_name: "Anna Weber",
            },
          ],
          page: { next_cursor: null, has_more: false },
        }),
      "POST /data-subject-requests": () =>
        jsonResponse(
          {
            id: "d3",
            kind: "erasure",
            subject_ref: "3fa85f64-5717-4562-b3fc-2c963f66afa6",
            status: "open",
            due_at: "2026-08-01T00:00:00.000Z",
            created_at: "2026-07-15T00:00:00Z",
          },
          201,
        ),
    });
    render(<PrivacyInboxCard />);
    await userEvent.click(
      await screen.findByRole("button", { name: /new request/i }),
    );
    await userEvent.selectOptions(screen.getByLabelText(/kind/i), "erasure");
    await userEvent.type(screen.getByLabelText(/person/i), "anna");
    await userEvent.click(await screen.findByText("Anna Weber"));
    // type="date" only accepts a programmatic value change in jsdom (same
    // limitation tasks.test.tsx works around for its own due-date field).
    fireEvent.change(screen.getByLabelText(/due/i), {
      target: { value: "2026-08-01" },
    });
    await userEvent.click(
      screen.getByRole("button", { name: /open request/i }),
    );
    await waitFor(() =>
      expect(
        sent.filter((s) => s.key === "POST /data-subject-requests"),
      ).toHaveLength(1),
    );
    const posts = sent.filter((s) => s.key === "POST /data-subject-requests");
    // toEqual, not objectContaining: proves subject_ref is the picked uuid —
    // never "Anna Weber" — and that no stray field (e.g. an assignee_id) rode
    // along. due_at: new Date("2026-08-01").toISOString() — an ISO date-only
    // string parses as UTC midnight, so this is genuinely a date-time, not a
    // bare date, matching the contract's `format: date-time`.
    expect(posts[0]?.body).toEqual({
      kind: "erasure",
      subject_ref: "3fa85f64-5717-4562-b3fc-2c963f66afa6",
      due_at: "2026-08-01T00:00:00.000Z",
    });
  });

  // The sibling of the test above: access/rectify keep the free-text field,
  // so this pins that the two kinds genuinely diverge on the wire rather
  // than a stray shared code path silently reusing the person picker's value.
  it("sends the typed free text as subject_ref for an access request", async () => {
    const sent = stubRoutes({
      "POST /data-subject-requests": () =>
        jsonResponse(
          {
            id: "d4",
            kind: "access",
            subject_ref: "anna@acme.test",
            status: "open",
            due_at: "2026-08-01T00:00:00.000Z",
            created_at: "2026-07-15T00:00:00Z",
          },
          201,
        ),
    });
    render(<PrivacyInboxCard />);
    await userEvent.click(
      await screen.findByRole("button", { name: /new request/i }),
    );
    await userEvent.selectOptions(screen.getByLabelText(/kind/i), "access");
    await userEvent.type(
      screen.getByLabelText(/subject reference/i),
      "anna@acme.test",
    );
    fireEvent.change(screen.getByLabelText(/due/i), {
      target: { value: "2026-08-01" },
    });
    await userEvent.click(
      screen.getByRole("button", { name: /open request/i }),
    );
    await waitFor(() =>
      expect(
        sent.filter((s) => s.key === "POST /data-subject-requests"),
      ).toHaveLength(1),
    );
    const posts = sent.filter((s) => s.key === "POST /data-subject-requests");
    expect(posts[0]?.body).toEqual({
      kind: "access",
      subject_ref: "anna@acme.test",
      due_at: "2026-08-01T00:00:00.000Z",
    });
  });
});

describe("fulfilling an erasure", () => {
  it("holds the confirm until ERASE is typed", async () => {
    stubRoutes();
    render(<PrivacyInboxCard />);
    await userEvent.click(
      await screen.findByRole("button", { name: /8f3a-person-uuid/i }),
    );
    await userEvent.type(screen.getByLabelText(/resolution/i), "verified");
    // "Fulfil" also substring-matches the facet bar's "Fulfilled" filter
    // button — scope to the row under test, same idiom as findDsrRow's other
    // callers above.
    const row = await findDsrRow("8f3a-person-uuid");
    await userEvent.click(within(row).getByRole("button", { name: /fulfil/i }));
    const confirm = await screen.findByRole("button", {
      name: /erase \+ suppress/i,
    });
    expect(confirm).toBeDisabled();
    await userEvent.type(screen.getByLabelText(/type erase/i), "ERASE");
    expect(confirm).toBeEnabled();
  });

  // gobd.html: retention wins for the statutory window (Art. 17(3)(b)). The
  // 409 is not a generic error — it is a documented, explicit outcome. The
  // stub mirrors the real wire shape (erasure.go's fmt.Errorf-wrapped
  // ErrConflict, mapped by httperr.go's fixed sentinel table): no
  // retain_until — the legal-hold check is a bare boolean column, never a
  // retention-window timestamp — so the fixture must not invent one.
  it("renders a legal hold as a blocked state, not a red toast", async () => {
    stubRoutes({
      "PATCH /data-subject-requests/d1": () =>
        jsonResponse(
          {
            type: "https://errors.gradion.com/conflict",
            title: "Conflict",
            status: 409,
            code: "conflict",
            detail: "erasing a person under legal hold: conflict",
          },
          409,
        ),
    });
    render(<PrivacyInboxCard />);
    await userEvent.click(
      await screen.findByRole("button", { name: /8f3a-person-uuid/i }),
    );
    await userEvent.type(screen.getByLabelText(/resolution/i), "verified");
    const row = await findDsrRow("8f3a-person-uuid");
    await userEvent.click(within(row).getByRole("button", { name: /fulfil/i }));
    await userEvent.type(screen.getByLabelText(/type erase/i), "ERASE");
    await userEvent.click(
      screen.getByRole("button", { name: /erase \+ suppress/i }),
    );
    expect(await screen.findByText(/legal hold/i)).toBeInTheDocument();
    expect(screen.getByText(/no override/i)).toBeInTheDocument();
  });

  // The confirm modal's own mutation, distinct from the row's plain PATCH
  // above (submitTransition never reaches it for an erasure fulfil): proves
  // the wire body is exactly {status: "fulfilled"} — no assignee_id, no
  // resolution, nothing the typed-ERASE gate implies but the request never
  // actually carries.
  it("sends exactly {status: fulfilled} on the erasure fulfil PATCH", async () => {
    const sent = stubRoutes({
      "PATCH /data-subject-requests/d1": () =>
        jsonResponse({ ...DSRS.data[0], status: "fulfilled" }),
    });
    render(<PrivacyInboxCard />);
    await userEvent.click(
      await screen.findByRole("button", { name: /8f3a-person-uuid/i }),
    );
    await userEvent.type(screen.getByLabelText(/resolution/i), "verified");
    const row = await findDsrRow("8f3a-person-uuid");
    await userEvent.click(within(row).getByRole("button", { name: /fulfil/i }));
    await userEvent.type(screen.getByLabelText(/type erase/i), "ERASE");
    await userEvent.click(
      screen.getByRole("button", { name: /erase \+ suppress/i }),
    );
    await waitFor(() =>
      expect(
        sent.filter((s) => s.key === "PATCH /data-subject-requests/d1"),
      ).toHaveLength(1),
    );
    const patches = sent.filter(
      (s) => s.key === "PATCH /data-subject-requests/d1",
    );
    expect(patches[0]?.body).toEqual({ status: "fulfilled" });
  });
});
