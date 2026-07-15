/** @vitest-environment jsdom */
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

beforeEach(() => localStorage.setItem("margince.workspaceSlug", "acme"));
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
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
    await userEvent.click(
      await screen.findByRole("button", { name: /proof log/i }),
    );
    expect(await screen.findByText(/booking form/i)).toBeInTheDocument();
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
    expect(sent.at(-1)?.body).toEqual({
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
    // An empty-string token must not be sent — the server would reject it as
    // "not a currently issued double opt-in token" rather than treat it as absent.
    expect(sent.at(-1)?.body).toEqual({
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
    expect(sent.at(-1)?.body).toEqual({
      purpose_id: "p1",
      new_state: "granted",
    });
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
  // so this surface must disclose it or the round-trip dead-ends. Also pins
  // the expiry display people.test.tsx's old DOI test asserted.
  it("discloses the one-time token and its expiry when issuing a double opt-in", async () => {
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
    expect(screen.getByText(/2026-08-01T00:00:00Z/)).toBeInTheDocument();
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
});
