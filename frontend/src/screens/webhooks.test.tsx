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

  it("hides the manage row (edit/rotate/archive) for a non-admin/ops role", async () => {
    vi.stubGlobal("fetch", backendFor(["rep"]));
    render(<WebhooksCard />);

    await waitFor(() =>
      expect(
        screen.getByText("https://example.test/hooks/margince"),
      ).toBeTruthy(),
    );
    expect(screen.queryByTestId("edit-record")).toBeNull();
    expect(screen.queryByTestId("rotate-webhook-secret")).toBeNull();
    expect(screen.queryByTestId("archive-record")).toBeNull();
  });
});

// Task 9 (B-E10.14): pause/resume + re-target (EditAction, If-Match), archive
// (ArchiveAction, DELETE), and rotate-secret (ConfirmModal → the shared
// SecretRevealModal). Each mutation invalidates the list + record queries.
describe("WebhooksCard — pause/resume + re-target (EditAction)", () => {
  function backendForEdit(patchResponder: (body: unknown) => Response) {
    const calls: { ifMatch: string | null; body: unknown }[] = [];
    const fetchMock = vi.fn(
      async (input: RequestInfo | URL, init?: RequestInit) => {
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
          return jsonResponse(SUBSCRIPTIONS);
        }
        if (req.url.includes("/sub-1") && req.method === "PATCH") {
          const body = await req.clone().json();
          calls.push({ ifMatch: req.headers.get("If-Match"), body });
          return patchResponder(body);
        }
        throw new Error(`unexpected request: ${req.method} ${req.url}`);
      },
    );
    return { fetchMock, calls };
  }

  it("sends If-Match: version with {state, event_types} on save", async () => {
    const user = userEvent.setup();
    const { fetchMock, calls } = backendForEdit((body) =>
      jsonResponse({
        ...SUBSCRIPTIONS.data[0],
        state: (body as { state: string }).state,
        event_types: (body as { event_types: string[] }).event_types,
        version: 2,
      }),
    );
    vi.stubGlobal("fetch", fetchMock);
    render(<WebhooksCard />);

    await user.click(await screen.findByTestId("edit-record"));
    // Flip state to paused via the select control; event_types stays as the
    // subscription's current, prefilled selection.
    const stateSelect = screen.getByLabelText(/^State/);
    await user.selectOptions(stateSelect, "paused");
    await user.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => expect(calls.length).toBe(1));
    expect(calls[0].ifMatch).toBe("1");
    expect(calls[0].body).toMatchObject({
      state: "paused",
      event_types: ["deal.stage_changed", "lead.promoted"],
    });
  });

  it("shows the version-skew copy on a 409 code:version_skew", async () => {
    const user = userEvent.setup();
    const { fetchMock } = backendForEdit(() =>
      jsonResponse(
        {
          type: "about:blank",
          title: "Conflict",
          detail: "if-match version 1 does not match current version 2",
          code: "version_skew",
        },
        409,
      ),
    );
    vi.stubGlobal("fetch", fetchMock);
    render(<WebhooksCard />);

    await user.click(await screen.findByTestId("edit-record"));
    await user.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() =>
      expect(
        screen.getByText(
          "This record changed since you opened it — reload and try again.",
        ),
      ).toBeTruthy(),
    );
    expect(
      screen.queryByText("if-match version 1 does not match current version 2"),
    ).toBeNull();
  });
});

describe("WebhooksCard — archive", () => {
  it("confirms then DELETEs /webhook-subscriptions/{id}", async () => {
    const user = userEvent.setup();
    let deleted = false;
    const fetchMock = vi.fn(
      async (input: RequestInfo | URL, init?: RequestInit) => {
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
          return jsonResponse(SUBSCRIPTIONS);
        }
        if (req.url.includes("/sub-1") && req.method === "DELETE") {
          deleted = true;
          return jsonResponse({
            ...SUBSCRIPTIONS.data[0],
            archived_at: "2026-07-23T00:00:00Z",
          });
        }
        throw new Error(`unexpected request: ${req.method} ${req.url}`);
      },
    );
    vi.stubGlobal("fetch", fetchMock);
    render(<WebhooksCard />);

    await user.click(await screen.findByTestId("archive-record"));
    await user.click(screen.getByTestId("archive-confirm"));

    await waitFor(() => expect(deleted).toBe(true));
  });
});

// Task 10 (B-E10.14/B-E10.15): the per-subscription deliveries + dead-letter
// inspection panel — lists the subscription's attempt log (newest-first, as
// the endpoint already orders it), honest `has_more` pagination via
// LoadMoreButton (the backend contract only carries a `limit` — there is no
// cursor query param on this endpoint, so "load more" honestly means
// re-asking for a bigger page, never a fabricated next_cursor), and a
// per-row replay action gated on canConfigureAutomations.
describe("WebhooksCard — deliveries panel (Task 10)", () => {
  const DELIVERED_DELIVERY = {
    id: "del-2",
    subscription_id: "sub-1",
    event_id: "evt-2",
    event_type: "offer.accepted",
    status: "delivered",
    attempts: 1,
    last_status_code: 200,
    last_error: null,
    next_retry_at: null,
    delivered_at: "2026-07-21T12:00:00Z",
    dead_lettered_at: null,
    created_at: "2026-07-21T11:59:00Z",
    updated_at: "2026-07-21T12:00:00Z",
  };
  const DEAD_LETTERED_DELIVERY = {
    id: "del-1",
    subscription_id: "sub-1",
    event_id: "evt-1",
    event_type: "organization.updated",
    status: "dead_lettered",
    attempts: 6,
    last_status_code: 500,
    last_error: "connection refused",
    next_retry_at: null,
    delivered_at: null,
    dead_lettered_at: "2026-07-20T10:00:00Z",
    created_at: "2026-07-20T09:00:00Z",
    updated_at: "2026-07-20T10:00:00Z",
  };

  function backendForDeliveries(options: {
    hasMore: boolean;
    onReplay?: () => void;
  }) {
    const getDeliveryCalls: string[] = [];
    let replayed = false;
    const fetchMock = vi.fn(
      async (input: RequestInfo | URL, init?: RequestInit) => {
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
          !req.url.includes("/deliveries") &&
          req.method === "GET"
        ) {
          return jsonResponse(SUBSCRIPTIONS);
        }
        if (
          req.url.includes("/sub-1/deliveries/del-1/replay") &&
          req.method === "POST"
        ) {
          replayed = true;
          options.onReplay?.();
          return jsonResponse({
            ...DEAD_LETTERED_DELIVERY,
            status: "delivered",
          });
        }
        if (req.url.includes("/sub-1/deliveries") && req.method === "GET") {
          getDeliveryCalls.push(req.url);
          const dead = replayed
            ? { ...DEAD_LETTERED_DELIVERY, status: "delivered" }
            : DEAD_LETTERED_DELIVERY;
          return jsonResponse({
            data: [DELIVERED_DELIVERY, dead],
            page: { next_cursor: null, has_more: options.hasMore },
          });
        }
        throw new Error(`unexpected request: ${req.method} ${req.url}`);
      },
    );
    return { fetchMock, getDeliveryCalls: () => getDeliveryCalls };
  }

  function jsonResponse(body: unknown, status = 200) {
    return new Response(JSON.stringify(body), {
      status,
      headers: { "Content-Type": "application/json" },
    });
  }

  it("lists deliveries newest-first with status badges, grouping dead-lettered rows", async () => {
    const user = userEvent.setup();
    const { fetchMock } = backendForDeliveries({ hasMore: false });
    vi.stubGlobal("fetch", fetchMock);
    render(<WebhooksCard />);

    await user.click(await screen.findByTestId("view-deliveries"));

    await waitFor(() =>
      expect(screen.getByText("offer.accepted")).toBeTruthy(),
    );
    expect(screen.getByText("organization.updated")).toBeTruthy();
    expect(screen.getByText("500")).toBeTruthy();
    expect(screen.getByText("connection refused")).toBeTruthy();
    expect(screen.getByText("Delivered")).toBeTruthy();
    expect(screen.getByText("Dead-lettered")).toBeTruthy();
    // Dead-lettered rows read as a visually distinct group, not just a badge
    // buried in an undifferentiated list.
    expect(screen.getByTestId("dead-letter-group")).toBeTruthy();
  });

  it("shows LoadMoreButton honestly off the real has_more, and fetches a bigger page on click", async () => {
    const user = userEvent.setup();
    const { fetchMock, getDeliveryCalls } = backendForDeliveries({
      hasMore: true,
    });
    vi.stubGlobal("fetch", fetchMock);
    render(<WebhooksCard />);

    await user.click(await screen.findByTestId("view-deliveries"));
    await waitFor(() =>
      expect(screen.getByText("offer.accepted")).toBeTruthy(),
    );

    const loadMore = screen.getByRole("button", { name: "Load more" });
    expect(loadMore).toBeTruthy();

    await user.click(loadMore);
    await waitFor(() => expect(getDeliveryCalls().length).toBe(2));
    expect(getDeliveryCalls()[1]).not.toBe(getDeliveryCalls()[0]);
  });

  it("hides LoadMoreButton when has_more is false", async () => {
    const user = userEvent.setup();
    const { fetchMock } = backendForDeliveries({ hasMore: false });
    vi.stubGlobal("fetch", fetchMock);
    render(<WebhooksCard />);

    await user.click(await screen.findByTestId("view-deliveries"));
    await waitFor(() =>
      expect(screen.getByText("offer.accepted")).toBeTruthy(),
    );
    expect(screen.queryByRole("button", { name: "Load more" })).toBeNull();
  });

  it("replays a dead-lettered delivery via confirm, then refreshes the row", async () => {
    const user = userEvent.setup();
    const { fetchMock } = backendForDeliveries({ hasMore: false });
    vi.stubGlobal("fetch", fetchMock);
    render(<WebhooksCard />);

    await user.click(await screen.findByTestId("view-deliveries"));
    await waitFor(() =>
      expect(screen.getByText("organization.updated")).toBeTruthy(),
    );

    await user.click(await screen.findByTestId("replay-delivery"));
    await user.click(screen.getByRole("button", { name: "Confirm" }));

    // The dead-lettered row refreshes to reflect the replay's outcome — the
    // list-invalidation contract (["webhook-deliveries", id]).
    await waitFor(() => {
      const badges = screen.getAllByText("Delivered");
      expect(badges.length).toBe(2);
    });
    expect(screen.queryByText("Dead-lettered")).toBeNull();
  });

  it("hides the replay action for a non-admin/ops role", async () => {
    const user = userEvent.setup();
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const req =
          input instanceof Request ? input : new Request(String(input), init);
        if (req.url.endsWith("/v1/me")) {
          return jsonResponse({
            user: { email: "rep@acme.test" },
            roles: ["rep"],
            teams: [],
          });
        }
        if (
          req.url.includes("/webhook-subscriptions") &&
          !req.url.includes("/deliveries") &&
          req.method === "GET"
        ) {
          return jsonResponse(SUBSCRIPTIONS);
        }
        if (req.url.includes("/sub-1/deliveries") && req.method === "GET") {
          return jsonResponse({
            data: [DEAD_LETTERED_DELIVERY],
            page: { next_cursor: null, has_more: false },
          });
        }
        throw new Error(`unexpected request: ${req.method} ${req.url}`);
      }),
    );
    render(<WebhooksCard />);

    await user.click(await screen.findByTestId("view-deliveries"));
    await waitFor(() =>
      expect(screen.getByText("organization.updated")).toBeTruthy(),
    );
    expect(screen.queryByTestId("replay-delivery")).toBeNull();
  });
});

describe("WebhooksCard — rotate secret", () => {
  it("confirms, calls rotate-secret, and reveals the new secret via SecretRevealModal", async () => {
    const user = userEvent.setup();
    const fetchMock = vi.fn(
      async (input: RequestInfo | URL, init?: RequestInit) => {
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
          return jsonResponse(SUBSCRIPTIONS);
        }
        if (req.url.includes("/sub-1/rotate-secret") && req.method === "POST") {
          return jsonResponse({
            subscription: { ...SUBSCRIPTIONS.data[0], version: 2 },
            signing_secret: "whsec_rotatedNEW123==",
          });
        }
        throw new Error(`unexpected request: ${req.method} ${req.url}`);
      },
    );
    vi.stubGlobal("fetch", fetchMock);
    render(<WebhooksCard />);

    await user.click(await screen.findByTestId("rotate-webhook-secret"));
    await user.click(screen.getByRole("button", { name: "Confirm" }));

    await waitFor(() =>
      expect(screen.getByText("whsec_rotatedNEW123==")).toBeTruthy(),
    );

    await user.click(screen.getByRole("button", { name: "Done" }));
    expect(screen.queryByText("whsec_rotatedNEW123==")).toBeNull();
  });
});
