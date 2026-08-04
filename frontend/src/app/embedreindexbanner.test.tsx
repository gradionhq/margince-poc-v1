/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { LocaleProvider } from "../i18n";
import { EmbedReindexBanner } from "./embedreindexbanner";
import { type GrantSpec, meFixture } from "./mefixture";

// The banner's status read is embedding_reindex:read server-side, granted to
// admin and ops alone — so the fixtures name that grant rather than the roles
// that happen to hold it today.
const REINDEX_READER: GrantSpec = { embedding_reindex: ["read"] };

function mount(
  allow: GrantSpec,
  status: {
    configured_identity: string;
    populated_identity: string;
    reindex_needed: boolean;
    entities_pending: number;
    status?: string;
  },
) {
  const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
    const path = new URL(
      input instanceof Request ? input.url : String(input),
      "https://test",
    ).pathname;
    if (path.endsWith("/me")) {
      return new Response(JSON.stringify(meFixture({ allow })), {
        headers: { "Content-Type": "application/json" },
      });
    }
    return new Response(
      JSON.stringify({
        status: "idle",
        per_workspace: [],
        ...status,
      }),
      { headers: { "Content-Type": "application/json" } },
    );
  });
  vi.stubGlobal("fetch", fetchMock);
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  render(
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">
        <EmbedReindexBanner />
      </LocaleProvider>
    </QueryClientProvider>,
  );
  return { fetchMock };
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

it('shows "Reindex needed" for an admin when the embed binding changed', async () => {
  mount(REINDEX_READER, {
    configured_identity: "anthropic/voyage-3@1024",
    populated_identity: "anthropic/voyage-2@1024",
    reindex_needed: true,
    entities_pending: 42,
  });
  expect(await screen.findByText("Reindex needed")).toBeTruthy();
});

it('shows "Reindex needed" for ops too', async () => {
  mount(REINDEX_READER, {
    configured_identity: "anthropic/voyage-3@1024",
    populated_identity: "anthropic/voyage-2@1024",
    reindex_needed: true,
    entities_pending: 42,
  });
  expect(await screen.findByText("Reindex needed")).toBeTruthy();
});

it("renders nothing without the read grant, even when the binding changed", async () => {
  // The GRANT is what denies here, not the role or the seat — the fixture below
  // holds a full seat and says nothing about roles. A principal with nothing
  // actionable on this surface never even probes the status read.
  const { fetchMock } = mount(
    { embedding_reindex: [] },
    {
      configured_identity: "anthropic/voyage-3@1024",
      populated_identity: "anthropic/voyage-2@1024",
      reindex_needed: true,
      entities_pending: 42,
    },
  );
  await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
  expect(
    fetchMock.mock.calls.some(([input]) =>
      String(input).includes("/embeddings/reindex/status"),
    ),
  ).toBe(false);
  expect(screen.queryByText("Reindex needed")).toBeNull();
  expect(screen.queryByRole("status")).toBeNull();
});

it("renders nothing for identity-matched drift, even with reindex_needed true and entities pending", async () => {
  // ADR-0069 §3a: matched identities + pending entities means the bus lost
  // embed events and the worker drift sweep is healing them — nothing here
  // asks a human to act, so keying off reindex_needed would wrongly fire.
  const { fetchMock } = mount(REINDEX_READER, {
    configured_identity: "anthropic/voyage-3@1024",
    populated_identity: "anthropic/voyage-3@1024",
    reindex_needed: true,
    entities_pending: 42,
  });
  await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
  expect(screen.queryByText("Reindex needed")).toBeNull();
  expect(screen.queryByRole("status")).toBeNull();
});

it("keeps showing during status=reembedding — a stuck marker must stay visible", async () => {
  // Deliberately NOT suppressed while a rebuild runs: a drift-cancelled
  // job leaves the marker at "reembedding" with no live worker, and
  // hiding the banner there would bury the one state that needs the
  // settings card's recovery affordance (SEARCH-AC-13: mismatch alone).
  mount(REINDEX_READER, {
    configured_identity: "anthropic/voyage-3@1024",
    populated_identity: "anthropic/voyage-2@1024",
    reindex_needed: true,
    entities_pending: 42,
    status: "reembedding",
  });
  expect(await screen.findByText("Reindex needed")).toBeTruthy();
});

it("renders nothing when the store is current", async () => {
  const { fetchMock } = mount(REINDEX_READER, {
    configured_identity: "anthropic/voyage-3@1024",
    populated_identity: "anthropic/voyage-3@1024",
    reindex_needed: false,
    entities_pending: 0,
  });
  await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
  expect(screen.queryByText("Reindex needed")).toBeNull();
  expect(screen.queryByRole("status")).toBeNull();
});

it("renders nothing while the status probe is pending or errors", async () => {
  const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
    const path = new URL(
      input instanceof Request ? input.url : String(input),
      "https://test",
    ).pathname;
    if (path.endsWith("/me")) {
      return new Response(
        JSON.stringify(meFixture({ allow: REINDEX_READER })),
        {
          headers: { "Content-Type": "application/json" },
        },
      );
    }
    return new Response(null, { status: 500 });
  });
  vi.stubGlobal("fetch", fetchMock);
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  render(
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">
        <EmbedReindexBanner />
      </LocaleProvider>
    </QueryClientProvider>,
  );
  await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
  expect(screen.queryByText("Reindex needed")).toBeNull();
});
