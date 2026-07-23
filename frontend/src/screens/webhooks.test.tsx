/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  render as rtlRender,
  screen,
  waitFor,
} from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider } from "../i18n";
import { WebhooksCard } from "./webhooks";

// The Settings → Integrations subscription list: renders from the typed
// listWebhookSubscriptions seam, gates the create/manage affordance on
// canConfigureAutomations (the server stays the RBAC authority), and reads
// the deployment's 503 webhooks_not_configured as an honest "not enabled"
// state rather than an error.

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

const SUBSCRIPTIONS = {
  data: [
    {
      id: "sub-1",
      workspace_id: "ws-1",
      owner_id: "user-1",
      target_url: "https://example.test/hooks/margince",
      event_types: ["deal.stage_changed", "lead.promoted"],
      state: "active",
      version: 1,
      created_at: "2026-07-01T00:00:00Z",
      updated_at: "2026-07-01T00:00:00Z",
      archived_at: null,
    },
  ],
  page: { next_cursor: null, has_more: false },
};

function backendFor(roles: string[], subscriptionsStatus = 200) {
  return vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const req =
      input instanceof Request ? input : new Request(String(input), init);
    if (req.url.endsWith("/v1/me")) {
      return jsonResponse({
        user: { email: "person@acme.test" },
        roles,
        teams: [],
      });
    }
    if (req.url.includes("/webhook-subscriptions") && req.method === "GET") {
      if (subscriptionsStatus === 503) {
        return jsonResponse(
          {
            title: "Service Unavailable",
            code: "webhooks_not_configured",
            detail:
              "outbound webhooks require a deployment signing key that is not configured",
          },
          503,
        );
      }
      return jsonResponse(SUBSCRIPTIONS, subscriptionsStatus);
    }
    throw new Error(`unexpected request: ${req.method} ${req.url}`);
  });
}

const render = (ui: ReactNode) => {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return rtlRender(
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">{ui}</LocaleProvider>
    </QueryClientProvider>,
  );
};

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("WebhooksCard", () => {
  it("renders a subscription list from the typed seam", async () => {
    vi.stubGlobal("fetch", backendFor(["admin"]));
    render(<WebhooksCard />);

    await waitFor(() =>
      expect(
        screen.getByText("https://example.test/hooks/margince"),
      ).toBeTruthy(),
    );
    expect(screen.getByText("deal.stage_changed")).toBeTruthy();
    expect(screen.getByText("lead.promoted")).toBeTruthy();
  });

  it("hides the create affordance for a non-admin/ops role", async () => {
    vi.stubGlobal("fetch", backendFor(["rep"]));
    render(<WebhooksCard />);

    await waitFor(() =>
      expect(
        screen.getByText("https://example.test/hooks/margince"),
      ).toBeTruthy(),
    );
    expect(screen.queryByTestId("new-webhook-subscription")).toBeNull();
  });

  it("shows the create affordance for an admin/ops role", async () => {
    vi.stubGlobal("fetch", backendFor(["admin"]));
    render(<WebhooksCard />);

    await waitFor(() =>
      expect(screen.getByTestId("new-webhook-subscription")).toBeTruthy(),
    );
  });

  it("renders an honest not-enabled state on 503 webhooks_not_configured", async () => {
    vi.stubGlobal("fetch", backendFor(["admin"], 503));
    render(<WebhooksCard />);

    await waitFor(() =>
      expect(screen.getByText(/not enabled on this deployment/i)).toBeTruthy(),
    );
    expect(screen.queryByTestId("new-webhook-subscription")).toBeNull();
  });

  it("renders the empty state when no subscriptions exist", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const req =
          input instanceof Request ? input : new Request(String(input), init);
        if (req.url.endsWith("/v1/me")) {
          return jsonResponse({
            user: { email: "admin@acme.test" },
            roles: ["admin"],
            teams: [],
          });
        }
        if (
          req.url.includes("/webhook-subscriptions") &&
          req.method === "GET"
        ) {
          return jsonResponse({
            data: [],
            page: { next_cursor: null, has_more: false },
          });
        }
        throw new Error(`unexpected request: ${req.method} ${req.url}`);
      }),
    );
    render(<WebhooksCard />);

    await waitFor(() =>
      expect(screen.getByText("Nothing here yet.")).toBeTruthy(),
    );
  });
});
