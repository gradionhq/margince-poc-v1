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
import { ConsentPurposesCard } from "./privacy";

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
      if (key === "GET /data-subject-requests")
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
