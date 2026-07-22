/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { LocaleProvider } from "../i18n";
import { EmbedReindexBanner } from "./embedreindexbanner";

function mount(
  roles: string[],
  status: { reindex_needed: boolean; entities_pending: number },
) {
  const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
    const path = new URL(
      input instanceof Request ? input.url : String(input),
      "https://test",
    ).pathname;
    if (path.endsWith("/me")) {
      return new Response(
        JSON.stringify({
          user: { id: "u1", email: "a@example.test", display_name: "A" },
          roles,
        }),
        { headers: { "Content-Type": "application/json" } },
      );
    }
    return new Response(
      JSON.stringify({
        configured_identity: "anthropic/voyage-3@1024",
        populated_identity: "anthropic/voyage-2@1024",
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

it('shows "Reindex needed" for an admin when the status read reports reindex_needed: true', async () => {
  mount(["admin"], { reindex_needed: true, entities_pending: 42 });
  expect(await screen.findByText("Reindex needed")).toBeTruthy();
});

it('shows "Reindex needed" for ops too', async () => {
  mount(["ops"], { reindex_needed: true, entities_pending: 42 });
  expect(await screen.findByText("Reindex needed")).toBeTruthy();
});

it("renders nothing for a non-ops role even when reindex_needed is true", async () => {
  // Gated the same as EconomyBanner: a rep has nothing actionable to do
  // with this surface, so the status read is never even probed.
  const { fetchMock } = mount(["rep"], {
    reindex_needed: true,
    entities_pending: 42,
  });
  await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
  expect(
    fetchMock.mock.calls.some(([input]) =>
      String(input).includes("/embeddings/reindex/status"),
    ),
  ).toBe(false);
  expect(screen.queryByText("Reindex needed")).toBeNull();
  expect(screen.queryByRole("status")).toBeNull();
});

it("renders nothing when reindex_needed is false, even with entities_pending stale-nonzero", async () => {
  // The brief's own key requirement: keyed off reindex_needed, never
  // entities_pending alone — a naive entities_pending > 0 check would wrongly
  // fire here.
  const { fetchMock } = mount(["admin"], {
    reindex_needed: false,
    entities_pending: 7,
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
        JSON.stringify({
          user: { id: "u1", email: "a@example.test", display_name: "A" },
          roles: ["admin"],
        }),
        { headers: { "Content-Type": "application/json" } },
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
