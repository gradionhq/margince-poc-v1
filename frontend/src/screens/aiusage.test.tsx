/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, expect, it, vi } from "vitest";
import { LocaleProvider } from "../i18n";
import { AiUsageCard } from "./aiusage";

const budget = { monthly_tokens: 1000, spent_tokens: 850, band: "degraded" };

function mount(body: unknown, status = 200) {
  vi.stubGlobal(
    "fetch",
    vi.fn(
      async () =>
        new Response(JSON.stringify(body), {
          status,
          headers: { "Content-Type": "application/json" },
        }),
    ),
  );
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">{children}</LocaleProvider>
    </QueryClientProvider>
  );
  render(<AiUsageCard />, { wrapper });
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

it("renders the budget meter and economy band without inventing cost", async () => {
  mount({
    budget,
    days: [
      {
        date: "2026-07-20",
        tasks: [
          {
            task: "enrich",
            tier: "cheap_cloud",
            calls: 2,
            cached_hits: 1,
            tokens_in: 100,
            tokens_out: 20,
          },
        ],
      },
    ],
  });
  expect(await screen.findByText("economy mode")).toBeTruthy();
  expect(screen.getByText("850 of 1,000 tokens · 85%")).toBeTruthy();
  expect(screen.queryByText("Est. cost")).toBeNull();
});

it("renders queued and lights up estimated cost only when present", async () => {
  mount({
    budget: { ...budget, band: "queued", spent_tokens: 1000, currency: "EUR" },
    days: [
      {
        date: "2026-07-20",
        tasks: [
          {
            task: "enrich",
            tier: "premium",
            calls: 1,
            tokens_in: 10,
            tokens_out: 2,
            cost_est_minor: 123,
          },
        ],
      },
    ],
  });
  expect(
    await screen.findByText("budget reached — background AI queued"),
  ).toBeTruthy();
  expect(screen.getByText("Est. cost")).toBeTruthy();
  expect(screen.getAllByText(/€1\.23/).length).toBeGreaterThan(0);
});

it("distinguishes an empty window and exposes a denied problem detail", async () => {
  mount({ budget, days: [] });
  expect(await screen.findByText("No AI calls in this window.")).toBeTruthy();
  cleanup();
  mount(
    {
      title: "Permission denied",
      detail: "automation-config grant required",
      status: 403,
      code: "permission_denied",
    },
    403,
  );
  await waitFor(() =>
    expect(screen.getByText("automation-config grant required")).toBeTruthy(),
  );
});

it("surfaces an unknown budget band", async () => {
  mount({ budget: { ...budget, band: "future-band" }, days: [] });
  expect(await screen.findByText("unknown budget state")).toBeTruthy();
});
