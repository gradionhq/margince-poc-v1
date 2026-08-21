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
import { meFixture } from "../app/mefixture";
import { pickOption } from "../design-system/select-testing";
import { LocaleProvider } from "../i18n";
import { en } from "../i18n/en";
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
      key: "transactional",
      label: "Deal messages",
      requires_double_opt_in: false,
      created_at: "2026-01-01T00:00:00Z",
    },
    {
      id: "p2",
      key: "marketing_email",
      label: "Marketing",
      requires_double_opt_in: true,
      created_at: "2026-01-01T00:00:00Z",
    },
  ],
  page: { next_cursor: null, has_more: false },
};

// Records every request so a test can assert what actually went to the
// server — the request body IS the contract for a purpose write
// (consent.test.tsx's harness shape, copied per-file per house convention).
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
      // The subject-request queue is admin-gated (its rows name the people who
      // filed), so the default principal here holds the role that may read it.
      // A test asserting the refusal overrides this key.
      if (key === "GET /me")
        return jsonResponse(meFixture({ roles: ["admin"] }));
      return jsonResponse({});
    }),
  );
  return sent;
}

// Drives one of this screen's dropdowns by the label a reader sees. `pickOption`
// needs a userEvent session, so it gets a fresh one — the same thing every bare
// `userEvent.*` call in this file does internally.
function choose(control: HTMLElement, optionLabel: string) {
  return pickOption(userEvent.setup(), control, optionLabel);
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

  // Appending to the registry is admin/ops, so a rep's card carries no write
  // control at all. Withheld, not absent: the card keeps its place and says
  // which of the two it is, or the missing button reads as a broken one.
  it("states its read-only posture to a seat that cannot add a purpose", async () => {
    stubRoutes({
      "GET /me": () => jsonResponse(meFixture({ roles: ["rep"] })),
    });
    render(<ConsentPurposesCard />);
    const posture = await screen.findByText(
      /only an admin or ops can add a purpose/i,
    );
    // On the registry ROW rather than as a paragraph of its own between the
    // card's description and the list: the posture is about the registry, and a
    // row's description is where the row language puts a sentence about a row.
    const registry = screen
      .getByText(/registered purposes/i)
      .closest(".settingrow");
    expect(registry).not.toBeNull();
    expect(registry?.contains(posture)).toBe(true);
    expect(
      screen.queryByRole("button", { name: /add purpose/i }),
    ).not.toBeInTheDocument();
    // Withholding the write never withholds the read — the registry a rep
    // consults when tagging a consent is still on screen.
    expect(screen.getByText(/Marketing/)).toBeInTheDocument();
  });

  // The other direction, without which the assertion above passes on a card
  // that shows the line to everybody: an ops seat holds the grant, so the
  // posture is not its posture and the sentence would be a false statement.
  it("withholds the read-only line from a seat that can add a purpose", async () => {
    stubRoutes({
      "GET /me": () => jsonResponse(meFixture({ roles: ["ops"] })),
    });
    render(<ConsentPurposesCard />);
    expect(
      await screen.findByRole("button", { name: /add purpose/i }),
    ).toBeInTheDocument();
    expect(
      screen.queryByText(/only an admin or ops can add a purpose/i),
    ).not.toBeInTheDocument();
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
  it("withholds the queue from a non-admin instead of asking for it", async () => {
    // The rows name the people who exercised an Art. 15/17 right, so the read
    // is the admin's. An ops seat reaches this tab for the consent registry
    // beside it, and must find the card in its place saying why it is empty —
    // an absent card would read as "no requests", a different claim entirely.
    const sent = stubRoutes({
      "GET /me": () => jsonResponse(meFixture({ roles: ["ops"] })),
    });
    render(<PrivacyInboxCard />);
    await screen.findByText(/only an admin can see subject requests/i);
    expect(screen.queryByText(/anna@acme.test/)).not.toBeInTheDocument();
    // And it never issued the call the server would only refuse.
    expect(
      sent.some((entry) => entry.key === "GET /data-subject-requests"),
    ).toBe(false);
  });

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
    // "in progress" alone would still pass if this row offered Fulfil or
    // Reject — nextStatuses(open|in_progress) is the DAG under test here,
    // so pin all three targets it could have offered, not just one of them.
    expect(
      within(row).queryByRole("button", { name: /^fulfil$/i }),
    ).not.toBeInTheDocument();
    expect(
      within(row).queryByRole("button", { name: /^reject$/i }),
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
    // Both fixture rows are before "now"; d2 (anna@acme.test) is fulfilled
    // and isTerminal short-circuits isOverdue regardless of its due date, so
    // a bare page-wide query could pass even if this pinned it to the wrong
    // row (or to none, if it matched the facet bar instead). Scope to the
    // one row this clock genuinely makes overdue: d1, still open.
    vi.setSystemTime(new Date("2026-08-02T00:00:00Z"));
    stubRoutes();
    render(<PrivacyInboxCard />);
    const row = await findDsrRow("8f3a-person-uuid");
    expect(within(row).getByText(/overdue/i)).toBeInTheDocument();
    const closedRow = await findDsrRow("anna@acme.test");
    expect(within(closedRow).queryByText(/overdue/i)).not.toBeInTheDocument();
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
  // made a decision that never happened. A 403 (code "permission_denied") gets
  // the catalog's refusal sentence instead, and never the server's own detail:
  // every producer of that sentinel wraps it in internals — the RBAC object and
  // verb here — which name nothing the reader can act on.
  it("tells the truth about a non-transition patch failure instead of claiming it moved on", async () => {
    stubRoutes({
      "PATCH /data-subject-requests/d1": () =>
        jsonResponse(
          {
            title: "Forbidden",
            // What auth.Require actually sends: the RBAC object and verb
            // wrapped around the sentinel. Internals, not copy — so the screen
            // must show the catalog sentence instead of echoing it.
            detail: "data_subject_request.update: permission denied",
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
      await screen.findByText(en["common.permissionDenied"]),
    ).toBeInTheDocument();
    expect(screen.queryByText(/data_subject_request/i)).not.toBeInTheDocument();
    expect(screen.queryByText(/moved on/i)).not.toBeInTheDocument();
  });

  it("assigns from the roster", async () => {
    const sent = stubRoutes({
      "GET /users": () =>
        jsonResponse({
          data: [
            {
              id: "u1",
              email: "dpo@acme.test",
              display_name: "Dana DPO",
              status: "active",
              is_agent: false,
            },
            {
              id: "u2",
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
    // The agent seat's absence is a property of the LIST, and the list only
    // exists while the popup is open — so the picker is opened here and the pick
    // lands on the very list under assertion (which is why this drives the two
    // steps by hand rather than through pickOption).
    await userEvent.click(picker);
    const roster = screen.getByRole("listbox");
    expect(
      within(roster).queryByRole("option", { name: "Bot" }),
    ).not.toBeInTheDocument();
    await userEvent.click(
      within(roster).getByRole("option", { name: "Dana DPO" }),
    );
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

  // The unassigned entry is a state, not an action: the server's update
  // coalesces an omitted assignee onto the stored one, so nothing an empty
  // selection sent could unassign anybody. It stays in the list, disabled, so
  // the state is legible without being offered.
  it("shows the unassigned entry but does not offer it as a choice", async () => {
    const sent = stubRoutes({
      "GET /users": () =>
        jsonResponse({
          data: [
            {
              id: "u1",
              email: "dpo@acme.test",
              display_name: "Dana DPO",
              status: "active",
              is_agent: false,
            },
          ],
          page: { next_cursor: null, has_more: false },
        }),
    });
    render(<PrivacyInboxCard />);
    await userEvent.click(
      await screen.findByRole("button", { name: /8f3a-person-uuid/i }),
    );
    await userEvent.click(await screen.findByLabelText(/assignee/i));
    const unassigned = within(screen.getByRole("listbox")).getByRole("option", {
      name: "—",
    });
    expect(unassigned).toHaveAttribute("aria-disabled", "true");
    await userEvent.click(unassigned);
    expect(
      sent.filter((s) => s.key === "PATCH /data-subject-requests/d1"),
    ).toHaveLength(0);
  });

  // The assignee select and the row's own status-transition buttons share
  // one `patch` mutation, so a failed assignment must be exactly as visible
  // as a failed transition — this pins that it is, using the assignee path
  // specifically (not the transition path other tests already cover).
  it("renders an honest error when an assignee PATCH fails", async () => {
    stubRoutes({
      "GET /users": () =>
        jsonResponse({
          data: [
            {
              id: "u1",
              email: "dpo@acme.test",
              display_name: "Dana DPO",
              status: "active",
              is_agent: false,
            },
          ],
          page: { next_cursor: null, has_more: false },
        }),
      "PATCH /data-subject-requests/d1": () =>
        jsonResponse(
          {
            title: "Forbidden",
            // What auth.Require actually sends: the RBAC object and verb
            // wrapped around the sentinel. Internals, not copy — so the screen
            // must show the catalog sentence instead of echoing it.
            detail: "data_subject_request.update: permission denied",
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
    const picker = await screen.findByLabelText(/assignee/i);
    await choose(picker, "Dana DPO");
    const row = await findDsrRow("8f3a-person-uuid");
    expect(
      await within(row).findByText(en["common.permissionDenied"]),
    ).toBeInTheDocument();
    expect(
      within(row).queryByText(/data_subject_request/i),
    ).not.toBeInTheDocument();
  });

  // The refusal reaching the reader is the catalog's, whatever the body carried:
  // a bare sentinel here, a sentinel wrapped in the RBAC object and verb in the
  // two tests above. Both land on the same sentence, because it is the only one
  // that says who can widen the access — there is no case where the server's own
  // detail is the words to keep.
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
    expect(
      await screen.findByText(en["common.permissionDenied"]),
    ).toBeInTheDocument();
    expect(screen.queryByText(/^permission denied$/i)).not.toBeInTheDocument();
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
    await choose(screen.getByLabelText(/kind/i), "erasure");
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
    await choose(screen.getByLabelText(/kind/i), "access");
    expect(screen.getByLabelText(/subject reference/i)).toBeInTheDocument();
  });

  it("says an access request is fulfilled by hand — nothing is exported", async () => {
    stubRoutes();
    render(<PrivacyInboxCard />);
    await userEvent.click(
      await screen.findByRole("button", { name: /new request/i }),
    );
    await choose(screen.getByLabelText(/kind/i), "access");
    expect(screen.getByText(/fulfilled by hand/i)).toBeInTheDocument();
  });

  it("requires a due date — the statutory clock is not optional", async () => {
    stubRoutes();
    render(<PrivacyInboxCard />);
    await userEvent.click(
      await screen.findByRole("button", { name: /new request/i }),
    );
    await choose(screen.getByLabelText(/kind/i), "access");
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
    // Pinned to a negative-offset zone (not the host machine's own, which
    // this suite never controls): due_at must mint at end-of-day THERE, not
    // at UTC midnight — the two disagree on which calendar day Aug 1 even is.
    vi.spyOn(Intl.DateTimeFormat.prototype, "resolvedOptions").mockReturnValue({
      timeZone: "America/New_York",
    } as Intl.ResolvedDateTimeFormatOptions);
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
            due_at: "2026-08-02T03:59:59.999Z",
            created_at: "2026-07-15T00:00:00Z",
          },
          201,
        ),
    });
    render(<PrivacyInboxCard />);
    await userEvent.click(
      await screen.findByRole("button", { name: /new request/i }),
    );
    await choose(screen.getByLabelText(/kind/i), "erasure");
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
    // along. due_at is 1 Aug 23:59:59.999 New York time, expressed as the UTC
    // instant that actually is (2 Aug UTC) — never the UTC-midnight reading
    // of the bare date, which would land on an entirely different day there.
    expect(posts[0]?.body).toEqual({
      kind: "erasure",
      subject_ref: "3fa85f64-5717-4562-b3fc-2c963f66afa6",
      due_at: "2026-08-02T03:59:59.999Z",
    });
  });

  // The sibling of the test above: access/rectify keep the free-text field,
  // so this pins that the two kinds genuinely diverge on the wire rather
  // than a stray shared code path silently reusing the person picker's value.
  it("sends the typed free text as subject_ref for an access request", async () => {
    vi.spyOn(Intl.DateTimeFormat.prototype, "resolvedOptions").mockReturnValue({
      timeZone: "America/New_York",
    } as Intl.ResolvedDateTimeFormatOptions);
    const sent = stubRoutes({
      "POST /data-subject-requests": () =>
        jsonResponse(
          {
            id: "d4",
            kind: "access",
            subject_ref: "anna@acme.test",
            status: "open",
            due_at: "2026-08-02T03:59:59.999Z",
            created_at: "2026-07-15T00:00:00Z",
          },
          201,
        ),
    });
    render(<PrivacyInboxCard />);
    await userEvent.click(
      await screen.findByRole("button", { name: /new request/i }),
    );
    await choose(screen.getByLabelText(/kind/i), "access");
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
      due_at: "2026-08-02T03:59:59.999Z",
    });
  });

  // I3: minting and rendering must agree, in a zone where the old
  // UTC-midnight minting silently rolled the picked day back by one. Picking
  // 15 Jul mints the UTC instant for 23:59:59.999 New York time (16 Jul UTC);
  // the row must still read the date back as 15 Jul, not 14.
  it("mints the due date at end-of-day in the viewer's zone, matching what the row later shows", async () => {
    vi.spyOn(Intl.DateTimeFormat.prototype, "resolvedOptions").mockReturnValue({
      timeZone: "America/New_York",
    } as Intl.ResolvedDateTimeFormatOptions);
    const created = {
      id: "d5",
      kind: "access",
      subject_ref: "anna@acme.test",
      status: "open",
      due_at: "2026-07-16T03:59:59.999Z",
      created_at: "2026-07-15T00:00:00Z",
    };
    const sent = stubRoutes({
      "POST /data-subject-requests": () => jsonResponse(created, 201),
      "GET /data-subject-requests": () =>
        jsonResponse({
          data: [created],
          page: { next_cursor: null, has_more: false },
        }),
    });
    render(<PrivacyInboxCard />);
    await userEvent.click(
      await screen.findByRole("button", { name: /new request/i }),
    );
    await choose(screen.getByLabelText(/kind/i), "access");
    await userEvent.type(
      screen.getByLabelText(/subject reference/i),
      "anna@acme.test",
    );
    fireEvent.change(screen.getByLabelText(/due/i), {
      target: { value: "2026-07-15" },
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
      due_at: "2026-07-16T03:59:59.999Z",
    });
    expect(await screen.findByText(/15\/07\/2026/)).toBeInTheDocument();
    expect(screen.queryByText(/14\/07\/2026/)).not.toBeInTheDocument();
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

  // Unlike the legal hold above, a stale-transition 422 is not a lawful
  // refusal of THIS confirm — it means someone else already decided the
  // request, so retrying the same confirm could only 422 again. The modal
  // must disarm itself and the row list behind it must re-read, exactly
  // like DsrRow's own plain PATCH already does for this same race.
  it("disarms the confirm and re-reads when the fulfil moved on underneath us", async () => {
    const sent = stubRoutes({
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
    await userEvent.type(screen.getByLabelText(/resolution/i), "verified");
    const row = await findDsrRow("8f3a-person-uuid");
    await userEvent.click(within(row).getByRole("button", { name: /fulfil/i }));
    await userEvent.type(screen.getByLabelText(/type erase/i), "ERASE");
    const confirm = screen.getByRole("button", { name: /erase \+ suppress/i });
    await userEvent.click(confirm);

    expect(await screen.findByText(/moved on/i)).toBeInTheDocument();
    expect(confirm).toBeDisabled();
    // The refusal must trigger the same re-read as the row's own plain
    // PATCH race — a second GET beyond the initial page load — so the row
    // behind this modal reflects what actually happened server-side.
    await waitFor(() =>
      expect(
        sent.filter((s) => s.key === "GET /data-subject-requests").length,
      ).toBeGreaterThan(1),
    );
  });

  // The confirm modal's own mutation, distinct from the row's plain PATCH
  // above (submitTransition never reaches it for an erasure fulfil): the
  // server now validates a DSR update BEFORE erasing (fulfilling an erasure
  // without a resolution 422s), so the resolution the operator wrote in the
  // row must ride along into this PATCH too — the modal has no field of its
  // own to collect it a second time. The stub mirrors what the server would
  // actually persist and echo back: a fixture starting at status "open" with
  // no stored resolution cannot legitimately reply 200 to a fulfil that
  // carried no resolution, so the response includes the one just sent.
  it("sends {status: fulfilled, resolution} on the erasure fulfil PATCH", async () => {
    const sent = stubRoutes({
      "PATCH /data-subject-requests/d1": () =>
        jsonResponse({
          ...DSRS.data[0],
          status: "fulfilled",
          resolution: "verified",
        }),
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
    expect(patches[0]?.body).toEqual({
      status: "fulfilled",
      resolution: "verified",
    });
  });

  // A fulfilled request is terminal, so the whole actions branch the confirm was
  // opened from — the resolution field and every transition button — is gone once
  // it succeeds. Handing focus back to the button that staged it is a silent
  // no-op that leaves the officer on <body>, one Tab from the top of the page.
  it("returns focus to the row's summary after the fulfil, never to the document", async () => {
    let fulfilled = false;
    const closed = { status: "fulfilled", resolution: "verified" };
    stubRoutes({
      // The queue the server would really answer with afterwards: without this
      // the row comes back open, the staging button is still there to take focus
      // back, and the case under test never happens.
      "GET /data-subject-requests": () =>
        jsonResponse({
          ...DSRS,
          data: DSRS.data.map((dsr) =>
            dsr.id === "d1" && fulfilled ? { ...dsr, ...closed } : dsr,
          ),
        }),
      "PATCH /data-subject-requests/d1": () => {
        fulfilled = true;
        return jsonResponse({ ...DSRS.data[0], ...closed });
      },
    });
    render(<PrivacyInboxCard />);
    const summary = await screen.findByRole("button", {
      name: /8f3a-person-uuid/i,
    });
    await userEvent.click(summary);
    await userEvent.type(screen.getByLabelText(/resolution/i), "verified");
    const row = await findDsrRow("8f3a-person-uuid");
    const opener = within(row).getByRole("button", { name: /fulfil/i });
    await userEvent.click(opener);
    await userEvent.type(screen.getByLabelText(/type erase/i), "ERASE");
    await userEvent.click(
      screen.getByRole("button", { name: /erase \+ suppress/i }),
    );

    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(opener.isConnected).toBe(false);
    // The row's own summary, which now reads back the status the erasure left.
    expect(document.activeElement).toBe(summary);
    expect(summary.textContent).toMatch(/fulfilled/i);
  });
});
