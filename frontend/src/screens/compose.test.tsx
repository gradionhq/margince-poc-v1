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
import type { components } from "../api/schema";
import { LocaleProvider } from "../i18n";
import { RelinkModal } from "./compose";

type Activity = components["schemas"]["Activity"];

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
  ],
  page: { next_cursor: null, has_more: false },
};

// Records every request so a test can assert what actually went to the server
// — the request body and headers ARE the contract for a send/relink.
type Sent = { key: string; body: unknown; headers: Headers };

function stubRoutes(overrides: Record<string, () => Response> = {}) {
  const sent: Sent[] = [];
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
            ? await request.clone().json()
            : JSON.parse(String(init?.body));
        } catch {
          body = null;
        }
      }
      const headers = request
        ? request.headers
        : new Headers(init?.headers ?? {});
      sent.push({ key, body, headers });
      const override = overrides[key];
      if (override) return override();
      if (key === "GET /consent-purposes") return jsonResponse(PURPOSES);
      return jsonResponse({});
    }),
  );
  return sent;
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

const activity202: Activity = {
  id: "act-1",
  workspace_id: "w",
  kind: "email",
  subject: "Re: Q3",
  occurred_at: "2026-07-01T00:00:00Z",
  source: "manual",
  captured_by: "human:u1",
};

describe("RelinkModal", () => {
  it("relinks the search-picked target and closes on 200", async () => {
    const onClose = vi.fn();
    const sent = stubRoutes({
      "GET /search": () =>
        jsonResponse({
          data: [{ type: "deal", id: "d-9", title: "Acme renewal" }],
          page: { has_more: false },
        }),
      "POST /activities/act-1/relink": () => jsonResponse(activity202),
    });
    render(
      <RelinkModal
        activityId="act-1"
        entityType="person"
        entityId="p-1"
        open
        onClose={onClose}
      />,
    );

    await userEvent.type(screen.getByRole("searchbox"), "Acme");
    const candidate = await screen.findByRole("button", {
      name: "Acme renewal",
    });
    await userEvent.click(candidate);
    await userEvent.click(screen.getByRole("button", { name: "Relink" }));

    await waitFor(() => expect(onClose).toHaveBeenCalled());
    const relink = sent.find((r) => r.key === "POST /activities/act-1/relink");
    expect(relink?.body).toEqual({
      entity_type: "deal",
      entity_id: "d-9",
      replace_existing_of_type: false,
    });
    // Relink is idempotency-keyed (its no-dup-on-replay contract).
    expect(relink?.headers.get("Idempotency-Key")).toBeTruthy();
  });

  it("sends replace_existing_of_type when the move toggle is on", async () => {
    const onClose = vi.fn();
    const sent = stubRoutes({
      "GET /search": () =>
        jsonResponse({
          data: [{ type: "organization", id: "o-2", title: "Globex" }],
          page: { has_more: false },
        }),
      "POST /activities/act-1/relink": () => jsonResponse(activity202),
    });
    render(
      <RelinkModal
        activityId="act-1"
        entityType="deal"
        entityId="d-1"
        open
        onClose={onClose}
      />,
    );

    await userEvent.type(screen.getByRole("searchbox"), "Globex");
    await userEvent.click(
      await screen.findByRole("button", { name: "Globex" }),
    );
    await userEvent.click(screen.getByRole("checkbox"));
    await userEvent.click(screen.getByRole("button", { name: "Relink" }));

    await waitFor(() => expect(onClose).toHaveBeenCalled());
    const relink = sent.find((r) => r.key === "POST /activities/act-1/relink");
    expect(relink?.body).toEqual({
      entity_type: "organization",
      entity_id: "o-2",
      replace_existing_of_type: true,
    });
  });

  it("drops activity results — relink has no activity target", async () => {
    stubRoutes({
      "GET /search": () =>
        jsonResponse({
          data: [
            { type: "activity", id: "a-x", title: "Some email" },
            { type: "person", id: "pp-1", title: "Jane Doe" },
          ],
          page: { has_more: false },
        }),
    });
    render(
      <RelinkModal
        activityId="act-1"
        entityType="deal"
        entityId="d-1"
        open
        onClose={vi.fn()}
      />,
    );

    await userEvent.type(screen.getByRole("searchbox"), "e");
    expect(
      await screen.findByRole("button", { name: "Jane Doe" }),
    ).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Some email" })).toBeNull();
  });
});
