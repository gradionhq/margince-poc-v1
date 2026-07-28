// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { LocaleProvider } from "../i18n";
import { SorModeChip } from "./sormodechip";

function mount(mode: "native" | "overlay" | undefined) {
  const fetchMock = vi.fn(async () => {
    const systemOfRecord = mode ? { system_of_record: { mode } } : {};
    return new Response(
      JSON.stringify({
        user: { id: "u1", email: "a@example.test", display_name: "A" },
        roles: ["admin"],
        teams: [],
        ...systemOfRecord,
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
        <SorModeChip />
      </LocaleProvider>
    </QueryClientProvider>,
  );
  return { fetchMock };
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

it("renders nothing in native mode", async () => {
  const { fetchMock } = mount("native");
  await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
  expect(screen.queryByRole("link")).toBeNull();
});

it("renders nothing when /me omits the field (pre-overlay server)", async () => {
  const { fetchMock } = mount(undefined);
  await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
  expect(screen.queryByRole("link")).toBeNull();
});

it("links to Settings → Overlay and explains the mode in its label", async () => {
  mount("overlay");
  const link = await screen.findByRole("link");
  // The chip is the one affordance that tells any seat where the connection
  // is managed, so the target is part of its contract: it must be the tab
  // that actually holds the overlay cards.
  expect(link.getAttribute("href")).toBe("#/settings/overlay");
  // The chip text itself is too small to carry the explanation — it rides
  // title/aria-label instead. Both must actually name the mode, not just
  // exist, or a screen reader / hover user gets no more than sighted users
  // scanning the two-word chip.
  const explanation = link.getAttribute("aria-label");
  expect(explanation).toBe(link.getAttribute("title"));
  expect(explanation).toMatch(/hubspot/i);
  // The copy names the destination, so it has to name the same tab the href
  // opens — a chip that says one place and goes to another is worse than
  // silent.
  expect(explanation).toMatch(/Settings → Overlay/);
  expect(explanation?.length ?? 0).toBeGreaterThan(20);
});
