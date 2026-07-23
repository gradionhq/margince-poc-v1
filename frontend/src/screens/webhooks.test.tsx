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
import { subscribableEventTypeValues } from "../api/public-events";
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

// The create-flow backend: GET answers an empty list (nothing to clutter the
// assertions with) and POST echoes the submitted body back as the created
// subscription plus a fixed one-time signing secret, capturing the request
// body so the test can assert the exact wire shape the create posts.
function backendForCreate(roles: string[]) {
  let capturedBody: unknown = null;
  const fetchMock = vi.fn(
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const req =
        input instanceof Request ? input : new Request(String(input), init);
      if (req.url.endsWith("/v1/me")) {
        return jsonResponse({
          user: { email: "admin@acme.test" },
          roles,
          teams: [],
        });
      }
      if (req.url.includes("/webhook-subscriptions") && req.method === "GET") {
        return jsonResponse({
          data: [],
          page: { next_cursor: null, has_more: false },
        });
      }
      if (req.url.includes("/webhook-subscriptions") && req.method === "POST") {
        capturedBody = await req.clone().json();
        const body = capturedBody as {
          target_url: string;
          event_types: string[];
        };
        return jsonResponse(
          {
            subscription: {
              id: "sub-new",
              workspace_id: "ws-1",
              owner_id: "user-1",
              target_url: body.target_url,
              event_types: body.event_types,
              state: "active",
              version: 1,
              created_at: "2026-07-22T00:00:00Z",
              updated_at: "2026-07-22T00:00:00Z",
              archived_at: null,
            },
            signing_secret: "whsec_abcDEF123==",
          },
          201,
        );
      }
      throw new Error(`unexpected request: ${req.method} ${req.url}`);
    },
  );
  return { fetchMock, getCapturedBody: () => capturedBody };
}

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

  it("sources event-type options from the generated SubscribableEventType catalog, never a hardcoded list", async () => {
    const user = userEvent.setup();
    const { fetchMock } = backendForCreate(["admin"]);
    vi.stubGlobal("fetch", fetchMock);
    render(<WebhooksCard />);

    await user.click(await screen.findByTestId("new-webhook-subscription"));

    // A couple of known values from across the published catalog families —
    // not the full count, so the assertion doesn't ossify into a second
    // hardcoded list the moment the backend catalog grows again.
    expect(screen.getByLabelText("deal.stage_changed")).toBeTruthy();
    expect(screen.getByLabelText("lead.promoted")).toBeTruthy();
    expect(screen.getByLabelText("person.merged")).toBeTruthy();
    // Every rendered checkbox is one of the generated catalog's values —
    // confirms the option list is DERIVED from subscribableEventTypeValues
    // (imported straight from the generated public-events module) rather
    // than independently maintained.
    for (const eventType of subscribableEventTypeValues) {
      expect(screen.getByLabelText(eventType)).toBeTruthy();
    }
  });

  it("creates a subscription posting {target_url, event_types[]} and reveals the signing secret exactly once", async () => {
    const user = userEvent.setup();
    const { fetchMock, getCapturedBody } = backendForCreate(["admin"]);
    vi.stubGlobal("fetch", fetchMock);
    render(<WebhooksCard />);

    await user.click(await screen.findByTestId("new-webhook-subscription"));
    await user.type(
      screen.getByLabelText(/target url/i),
      "https://example.test/inbound",
    );
    await user.click(screen.getByLabelText("deal.stage_changed"));
    await user.click(screen.getByLabelText("lead.promoted"));
    await user.click(screen.getByRole("button", { name: "Create" }));

    await waitFor(() =>
      expect(getCapturedBody()).toEqual({
        target_url: "https://example.test/inbound",
        event_types: ["deal.stage_changed", "lead.promoted"],
      }),
    );

    // The secret shows exactly once, right after create — never re-derived,
    // never re-fetched.
    await waitFor(() =>
      expect(screen.getByText("whsec_abcDEF123==")).toBeTruthy(),
    );
    expect(screen.getByText(/shown once/i)).toBeTruthy();

    // Closing the reveal modal is the only way out — the secret is gone from
    // the DOM afterwards, and the subsequent list refetch (triggered by the
    // ["webhook-subscriptions"] invalidation) never carries it either, since
    // the list wire (WebhookSubscription) never includes signing_secret.
    await user.click(screen.getByRole("button", { name: "Done" }));
    expect(screen.queryByText("whsec_abcDEF123==")).toBeNull();
    await waitFor(() =>
      expect(screen.getByText("Nothing here yet.")).toBeTruthy(),
    );
    expect(screen.queryByText(/whsec_/)).toBeNull();
  });

  it("hides the create trigger and reveal flow for a non-admin/ops role", async () => {
    const { fetchMock } = backendForCreate(["rep"]);
    vi.stubGlobal("fetch", fetchMock);
    render(<WebhooksCard />);

    await waitFor(() =>
      expect(screen.getByText("Nothing here yet.")).toBeTruthy(),
    );
    expect(screen.queryByTestId("new-webhook-subscription")).toBeNull();
  });
});
