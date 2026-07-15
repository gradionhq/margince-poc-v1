/** @vitest-environment jsdom */
import "@testing-library/jest-dom/vitest";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  render as rtlRender,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider } from "../i18n";
import { ConsentSection } from "./consent";

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

const CONSENT = {
  state: [
    {
      purpose_id: "p1",
      purpose_key: "transactional",
      state: "granted",
      updated_at: "2026-05-01T10:00:00Z",
    },
    { purpose_id: "p2", purpose_key: "marketing_email", state: "unknown" },
  ],
  events: [
    {
      id: "e1",
      purpose_id: "p1",
      new_state: "granted",
      source: "booking form",
      actor_type: "human",
      actor_id: "u1",
      occurred_at: "2026-05-01T10:00:00Z",
    },
  ],
};

// Records every request so a test can assert what actually went to the
// server — the request body IS the contract for a consent write.
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
      if (key === "GET /people/person-1/consent") return jsonResponse(CONSENT);
      return jsonResponse({});
    }),
  );
  return sent;
}

// Both fixture purposes render a row with identically-named controls
// ("Proof log", "Grant"), so tests that need one specific row's control
// scope the query to that row rather than assuming which one a bare
// findByRole/getByRole call lands on.
async function findConsentRow(label: string) {
  const row = (await screen.findByText(label)).closest(".consent-row");
  if (!(row instanceof HTMLElement)) {
    throw new Error(`consent row for "${label}" not found`);
  }
  return row;
}

beforeEach(() => localStorage.setItem("margince.workspaceSlug", "acme"));
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("ConsentSection", () => {
  it("renders unknown distinctly from withdrawn — no record is not a withdrawal", async () => {
    stubRoutes();
    render(<ConsentSection personId="person-1" />);
    expect(await screen.findByText(/no record/i)).toBeInTheDocument();
  });

  // G-4: the events[] the Person 360 currently drops. Art. 7 demonstrability.
  it("shows the append-only proof log for a purpose", async () => {
    stubRoutes();
    render(<ConsentSection personId="person-1" />);
    const row = await findConsentRow("Deal messages");
    await userEvent.click(
      within(row).getByRole("button", { name: /proof log/i }),
    );
    expect(await screen.findByText(/booking form/i)).toBeInTheDocument();
  });

  // C3: the log's actor line must name the ACTUAL actor, never resolve to a
  // claim about the viewer — this fixture's event was captured by u1, a
  // human who is not necessarily whoever is looking at this proof.
  it("names the actual human actor rather than claiming the viewer typed it", async () => {
    stubRoutes();
    render(<ConsentSection personId="person-1" />);
    const row = await findConsentRow("Deal messages");
    await userEvent.click(
      within(row).getByRole("button", { name: /proof log/i }),
    );
    expect(await screen.findByText("u1")).toBeInTheDocument();
    expect(screen.queryByText(/typed by you/i)).not.toBeInTheDocument();
  });

  // The second leg of the same defect: an event that omits actor_type
  // entirely must not default into the human branch either — an actor the
  // wire never named is unknown, never a positive claim about the viewer.
  it("does not default a missing actor_type to a claim about the viewer", async () => {
    stubRoutes({
      "GET /people/person-1/consent": () =>
        jsonResponse({
          state: CONSENT.state,
          events: [
            {
              id: "e2",
              purpose_id: "p1",
              new_state: "granted",
              source: "import",
              occurred_at: "2026-05-01T10:00:00Z",
            },
          ],
        }),
    });
    render(<ConsentSection personId="person-1" />);
    const row = await findConsentRow("Deal messages");
    await userEvent.click(
      within(row).getByRole("button", { name: /proof log/i }),
    );
    expect(await screen.findByText(/actor not recorded/i)).toBeInTheDocument();
    expect(screen.queryByText(/typed by you/i)).not.toBeInTheDocument();
  });

  // A purpose with no consent record has no events by construction — the
  // log must still be reachable and say so honestly, not hide the toggle.
  it("shows the honest empty state for a purpose with no consent record", async () => {
    stubRoutes();
    render(<ConsentSection personId="person-1" />);
    const row = await findConsentRow("Marketing");
    await userEvent.click(
      within(row).getByRole("button", { name: /proof log/i }),
    );
    expect(
      await screen.findByText(/no consent decision recorded/i),
    ).toBeInTheDocument();
  });

  // G-5: a DOI purpose needs the one-time token; the row must have a field
  // for it. Without one, granting a DOI purpose can only 422.
  it("offers a token field only on a purpose that requires double opt-in", async () => {
    stubRoutes();
    render(<ConsentSection personId="person-1" />);
    await screen.findByText("Marketing");
    expect(screen.getByLabelText(/confirmation token/i)).toBeInTheDocument();
    expect(screen.getAllByLabelText(/confirmation token/i)).toHaveLength(1);
  });

  it("sends the redeemed token with the grant", async () => {
    const sent = stubRoutes({
      "POST /people/person-1/consent": () =>
        jsonResponse({
          purpose_id: "p2",
          purpose_key: "marketing_email",
          state: "granted",
        }),
    });
    render(<ConsentSection personId="person-1" />);
    await userEvent.type(
      await screen.findByLabelText(/confirmation token/i),
      "doi-tok-123",
    );
    await userEvent.click(
      screen.getAllByRole("button", { name: /^grant$/i })[0],
    );
    await waitFor(() =>
      expect(
        sent.filter((s) => s.key === "POST /people/person-1/consent"),
      ).toHaveLength(1),
    );
    // The onSuccess invalidation refetches the consent GET, appending to the
    // same `sent` array — filter for the POST specifically rather than
    // trusting it stayed last.
    const posts = sent.filter((s) => s.key === "POST /people/person-1/consent");
    expect(posts.at(-1)?.body).toEqual({
      purpose_id: "p2",
      new_state: "granted",
      double_opt_in_token: "doi-tok-123",
    });
  });

  it("omits the token key entirely when none was typed", async () => {
    const sent = stubRoutes({
      "POST /people/person-1/consent": () =>
        jsonResponse({
          purpose_id: "p1",
          purpose_key: "transactional",
          state: "withdrawn",
        }),
    });
    render(<ConsentSection personId="person-1" />);
    await userEvent.click(
      await screen.findByRole("button", { name: /^withdraw$/i }),
    );
    await waitFor(() =>
      expect(
        sent.filter((s) => s.key === "POST /people/person-1/consent"),
      ).toHaveLength(1),
    );
    const posts = sent.filter((s) => s.key === "POST /people/person-1/consent");
    // An empty-string token must not be sent — the server would reject it as
    // "not a currently issued double opt-in token" rather than treat it as absent.
    expect(posts.at(-1)?.body).toEqual({
      purpose_id: "p1",
      new_state: "withdrawn",
    });
  });

  // Preserves people.test.tsx's former "shows Grant for a non-granted purpose"
  // coverage: a plain (non-DOI) grant must send no token key either, and this
  // exercises the ternary badge's `granted` branch for what the fixture's p1
  // otherwise never reaches (it starts already granted).
  it("sends a plain grant with no token key for a purpose that does not require one", async () => {
    const sent = stubRoutes({
      "GET /people/person-1/consent": () =>
        jsonResponse({
          state: [
            {
              purpose_id: "p1",
              purpose_key: "transactional",
              state: "withdrawn",
            },
            {
              purpose_id: "p2",
              purpose_key: "marketing_email",
              state: "unknown",
            },
          ],
          events: [],
        }),
      "POST /people/person-1/consent": () =>
        jsonResponse({
          purpose_id: "p1",
          purpose_key: "transactional",
          state: "granted",
        }),
    });
    render(<ConsentSection personId="person-1" />);
    // Both p1 (withdrawn) and p2 (unknown) show a Grant button here; [0] is
    // p1's — rows render in the order GET /people/{id}/consent lists them.
    await screen.findByText("Deal messages");
    await userEvent.click(
      screen.getAllByRole("button", { name: /^grant$/i })[0],
    );
    await waitFor(() =>
      expect(
        sent.filter((s) => s.key === "POST /people/person-1/consent"),
      ).toHaveLength(1),
    );
    const posts = sent.filter((s) => s.key === "POST /people/person-1/consent");
    expect(posts.at(-1)?.body).toEqual({
      purpose_id: "p1",
      new_state: "granted",
    });
  });

  // Defect-1 regression guard: the write endpoint's response can't carry the
  // new consent_event, so the proof log can only pick up the transition just
  // made by re-reading GET /people/{id}/consent. Proves the refetch actually
  // happens (not just that the badge flips) by having the second GET return
  // an event the first GET never had, then asserting it renders.
  it("re-reads the consent GET after a write so the proof log includes the new decision", async () => {
    let getCalls = 0;
    const sent = stubRoutes({
      "GET /people/person-1/consent": () => {
        getCalls += 1;
        if (getCalls === 1) return jsonResponse(CONSENT);
        return jsonResponse({
          state: [
            { ...CONSENT.state[0], state: "withdrawn" },
            CONSENT.state[1],
          ],
          events: [
            ...CONSENT.events,
            {
              id: "e2",
              purpose_id: "p1",
              new_state: "withdrawn",
              source: "person 360",
              actor_type: "human",
              actor_id: "u1",
              occurred_at: "2026-06-01T00:00:00Z",
            },
          ],
        });
      },
      "POST /people/person-1/consent": () =>
        jsonResponse({
          purpose_id: "p1",
          purpose_key: "transactional",
          state: "withdrawn",
        }),
    });
    render(<ConsentSection personId="person-1" />);
    const row = await findConsentRow("Deal messages");
    await userEvent.click(
      within(row).getByRole("button", { name: /proof log/i }),
    );
    expect(await screen.findByText(/booking form/i)).toBeInTheDocument();

    await userEvent.click(
      within(row).getByRole("button", { name: /^withdraw$/i }),
    );

    await waitFor(() =>
      expect(
        sent.filter((s) => s.key === "GET /people/person-1/consent"),
      ).toHaveLength(2),
    );
    expect(await screen.findByText(/person 360/i)).toBeInTheDocument();
  });

  it("asks the server not to deliver — this surface owns the token disclosure", async () => {
    const sent = stubRoutes({
      "POST /people/person-1/consent/double-opt-in": () =>
        jsonResponse(
          { token: "mgd_x", expires_at: "2026-08-01T00:00:00Z" },
          201,
        ),
    });
    render(<ConsentSection personId="person-1" />);
    await userEvent.click(
      await screen.findByRole("button", { name: /issue double opt-in/i }),
    );
    await waitFor(() =>
      expect(
        sent.some(
          (s) => s.key === "POST /people/person-1/consent/double-opt-in",
        ),
      ).toBe(true),
    );
    expect(sent.at(-1)?.body).toEqual({ purpose_id: "p2", deliver: false });
  });

  // The DOI token is minted but never delivered (doi.go has no queue call),
  // so this surface must disclose it or the round-trip dead-ends. The expiry
  // goes through the same shared formatter (formatDateTime) every other
  // timestamp on this branch does — a raw ISO string here would be the one
  // exception, in the viewer's own zone rather than a hardcoded one.
  it("discloses the one-time token and its expiry when issuing a double opt-in", async () => {
    vi.spyOn(Intl.DateTimeFormat.prototype, "resolvedOptions").mockReturnValue({
      timeZone: "Europe/Berlin",
    } as Intl.ResolvedDateTimeFormatOptions);
    stubRoutes({
      "POST /people/person-1/consent/double-opt-in": () =>
        jsonResponse(
          { token: "mgd_one_time_abc", expires_at: "2026-08-01T00:00:00Z" },
          201,
        ),
    });
    render(<ConsentSection personId="person-1" />);
    await userEvent.click(
      await screen.findByRole("button", { name: /issue double opt-in/i }),
    );
    expect(await screen.findByText("mgd_one_time_abc")).toBeInTheDocument();
    // Berlin is +02:00 in August: 00:00 UTC reads back as 02:00 local.
    expect(screen.getByText(/01\/08\/2026, 02:00/)).toBeInTheDocument();
  });

  it("renders an honest empty state when the workspace tracks no purposes", async () => {
    stubRoutes({
      "GET /consent-purposes": () =>
        jsonResponse({
          data: [],
          page: { next_cursor: null, has_more: false },
        }),
      "GET /people/person-1/consent": () =>
        jsonResponse({ state: [], events: [] }),
    });
    render(<ConsentSection personId="person-1" />);
    expect(await screen.findByText(/no consent purposes/i)).toBeInTheDocument();
  });

  it("surfaces a load failure with a retry rather than a blank card", async () => {
    stubRoutes({
      "GET /people/person-1/consent": () =>
        jsonResponse({ title: "boom", status: 500 }, 500),
    });
    render(<ConsentSection personId="person-1" />);
    expect(
      await screen.findByRole("button", { name: /retry/i }),
    ).toBeInTheDocument();
  });

  // I6: requires_double_opt_in lives only on ConsentPurpose, so a failed
  // purposes fetch must not fall back to rendering every row as freely
  // grantable — share.tsx's RosterPicker gates its two roster fetches the
  // same explicit way, for the same reason (a collapsed-to-[] failure must
  // never be mistaken for a real empty list).
  it("shows an error instead of quietly dropping the DOI gate when purposes fail to load", async () => {
    stubRoutes({
      "GET /consent-purposes": () => jsonResponse({ title: "boom" }, 500),
    });
    render(<ConsentSection personId="person-1" />);
    expect(
      await screen.findByText(/couldn't load the consent purpose catalogue/i),
    ).toBeInTheDocument();
    // The DOI-required "Marketing" row must not render as freely grantable
    // with no sign anything failed.
    expect(screen.queryByText("Marketing")).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /issue double opt-in/i }),
    ).not.toBeInTheDocument();
  });
});
