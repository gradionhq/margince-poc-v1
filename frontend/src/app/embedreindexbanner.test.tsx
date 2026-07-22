/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { LocaleProvider } from "../i18n";
import { EmbedReindexBanner } from "./embedreindexbanner";

function mount(status: { reindex_needed: boolean; entities_pending: number }) {
  const fetchMock = vi.fn(
    async () =>
      new Response(
        JSON.stringify({
          configured_identity: "anthropic/voyage-3@1024",
          populated_identity: "anthropic/voyage-2@1024",
          status: "idle",
          per_workspace: [],
          ...status,
        }),
        { headers: { "Content-Type": "application/json" } },
      ),
  );
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

it('shows "Reindex needed" when the status read reports reindex_needed: true', async () => {
  mount({ reindex_needed: true, entities_pending: 42 });
  expect(await screen.findByText("Reindex needed")).toBeTruthy();
});

it("renders nothing when reindex_needed is false, even with entities_pending stale-nonzero", async () => {
  // The brief's own key requirement: keyed off reindex_needed, never
  // entities_pending alone — a naive entities_pending > 0 check would wrongly
  // fire here.
  const { fetchMock } = mount({ reindex_needed: false, entities_pending: 7 });
  await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
  expect(screen.queryByText("Reindex needed")).toBeNull();
  expect(screen.queryByRole("status")).toBeNull();
});

it("renders nothing while the status probe is pending or errors", async () => {
  const fetchMock = vi.fn(async () => new Response(null, { status: 500 }));
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
  await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
  expect(screen.queryByText("Reindex needed")).toBeNull();
});
