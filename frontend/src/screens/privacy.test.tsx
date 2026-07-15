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
    // A hardcoded Europe/Berlin renders 1 Aug; New York renders 31 Jul. The
    // brief's regex assumed a month-name or MM/DD rendering; this codebase's
    // locked locale convention (format.ts's INTL_LOCALE, "A100: unconfigured
    // English is en-GB, not en-US") renders numeric dates DD/MM/YYYY with
    // slashes, so "31/07" is the real, correct substring for 31 Jul here —
    // added alongside the brief's original alternatives rather than in
    // place of them.
    expect(
      screen.getByText(/Jul 31|31 Jul|07\/31|31\.07|31\/07/),
    ).toBeInTheDocument();
  });

  it("offers only the transitions the server would accept", async () => {
    stubRoutes();
    render(<PrivacyInboxCard />);
    await userEvent.click(
      await screen.findByRole("button", { name: /8f3a-person-uuid/i }),
    );
    expect(
      screen.getByRole("button", { name: /in progress/i }),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /fulfil/i })).toBeInTheDocument();
  });

  it("offers no transition on a closed request — a closed request never reopens", async () => {
    stubRoutes();
    render(<PrivacyInboxCard />);
    await userEvent.click(
      await screen.findByRole("button", { name: /anna@acme.test/i }),
    );
    expect(
      screen.queryByRole("button", { name: /in progress/i }),
    ).not.toBeInTheDocument();
    expect(screen.getByText(/closed/i)).toBeInTheDocument();
  });

  it("holds a close until a resolution is written — the server 422s without one", async () => {
    stubRoutes();
    render(<PrivacyInboxCard />);
    await userEvent.click(
      await screen.findByRole("button", { name: /8f3a-person-uuid/i }),
    );
    expect(screen.getByRole("button", { name: /reject/i })).toBeDisabled();
    await userEvent.type(
      screen.getByLabelText(/resolution/i),
      "not a data subject",
    );
    expect(screen.getByRole("button", { name: /reject/i })).toBeEnabled();
  });

  it("flags an overdue request against the injected clock", async () => {
    vi.setSystemTime(new Date("2026-08-02T00:00:00Z"));
    stubRoutes();
    render(<PrivacyInboxCard />);
    expect(await screen.findByText(/overdue/i)).toBeInTheDocument();
  });

  // The stale-row race: another admin moved it first, so our offered
  // transition is now illegal and the PATCH 422s. Note this is NOT the
  // approvals' 409 already_decided — isAlreadyDecided does not apply.
  it("re-reads and explains when the request moved on underneath us", async () => {
    stubRoutes({
      "PATCH /data-subject-requests/d1": () =>
        jsonResponse(
          {
            title: "open → fulfilled is not a legal transition",
            status: 422,
            code: "invalid",
          },
          422,
        ),
    });
    render(<PrivacyInboxCard />);
    await userEvent.click(
      await screen.findByRole("button", { name: /8f3a-person-uuid/i }),
    );
    await userEvent.type(screen.getByLabelText(/resolution/i), "done");
    await userEvent.click(screen.getByRole("button", { name: /reject/i }));
    expect(await screen.findByText(/moved on/i)).toBeInTheDocument();
  });

  it("assigns from the roster", async () => {
    const patch = vi.fn(() =>
      jsonResponse({ ...DSRS.data[0], assignee_id: "u1" }),
    );
    stubRoutes({
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
      "PATCH /data-subject-requests/d1": patch,
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
    await waitFor(() => expect(patch).toHaveBeenCalledTimes(1));
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
