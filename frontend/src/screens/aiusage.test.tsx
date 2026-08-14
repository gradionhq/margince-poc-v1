/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, expect, it, vi } from "vitest";
import { type GrantSpec, meFixture } from "../app/mefixture";
import { LocaleProvider } from "../i18n";
import { AiUsageCard } from "./aiusage";

const budget = { monthly_tokens: 1000, spent_tokens: 850, band: "degraded" };

// The card is gated on automation:update — the server treats the runtime's spend as
// operator information — so a stub that answers every request with the usage body
// leaves the caller holding no grant, and the card correctly says it is withheld
// instead of rendering. Routing /me is what makes these tests about the BODY again.
const OPERATOR: GrantSpec = { automation: ["read", "update"] };

function mount(body: unknown, status = 200, allow: GrantSpec = OPERATOR) {
  const seen: string[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input instanceof Request ? input.url : input);
      seen.push(url);
      if (url.endsWith("/v1/me")) {
        return new Response(JSON.stringify(meFixture({ allow })), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        });
      }
      return new Response(JSON.stringify(body), {
        status,
        headers: { "Content-Type": "application/json" },
      });
    }),
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
  return { seen };
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

it("withholds the spend from a principal without the automation grant, and asks the server for nothing", async () => {
  // Withheld, not absent: an absent spend card claims the installation spent
  // nothing. The card keeps its title and says whose figures these are — and the
  // usage read never fires, because the denial is already known.
  const { seen } = mount({ budget, days: [] }, 200, { automation: ["read"] });

  expect(
    await screen.findByText(
      /only an operator can see what the AI runtime spent/i,
    ),
  ).toBeTruthy();
  expect(screen.getByText("AI usage & budget")).toBeTruthy();
  expect(seen.some((url) => url.includes("/ai/usage"))).toBe(false);
});
