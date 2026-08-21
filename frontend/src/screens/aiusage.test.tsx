/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, expect, it, vi } from "vitest";
import { type GrantSpec, meFixture } from "../app/mefixture";
import { LocaleProvider } from "../i18n";
import { en } from "../i18n/en";
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
  // The caveat and the total are what the table says taken TOGETHER, so they
  // stand in the row's naming as its description rather than under the table as
  // a caption: a sentence in a control column reads as that control's answer.
  const note = screen.getByText(/Costs are estimates/);
  expect(note.closest(".settingrow-naming")).not.toBeNull();
  expect(note.textContent).toContain("€1.23");
});

// An empty window and a refused read are different answers, and the card owes
// each its own: "nothing was spent" is a fact about the month, while a 403 is a
// fact about the reader. The refusal reads as catalog copy because that is all a
// 403 has to offer — its detail is the permission sentinel, which says less than
// the code it arrives with.
it("distinguishes an empty window from a refused read", async () => {
  mount({ budget, days: [] });
  expect(await screen.findByText("No AI calls in this window.")).toBeTruthy();
  cleanup();
  mount(
    {
      title: "Forbidden",
      detail: "automation.update: permission denied",
      status: 403,
      code: "permission_denied",
    },
    403,
  );
  await waitFor(() =>
    expect(screen.getByText(en["common.permissionDenied"])).toBeTruthy(),
  );
  // The RBAC object and verb the gate wrapped its refusal with never reach the
  // reader: that is the authority model's shape, not an explanation.
  expect(screen.queryByText(/automation.update/)).toBeNull();
});

it("names every row, and puts the per-day breakdown behind one disclosure", async () => {
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
            tokens_in: 100,
            tokens_out: 20,
          },
        ],
      },
    ],
  });
  // The month is one decision, so the row names it once and each arrow keeps
  // its own name: a glyph announces as nothing, and "‹" is not a direction.
  expect(await screen.findByText("Month")).toBeTruthy();
  expect(screen.getByRole("button", { name: "Previous month" })).toBeTruthy();
  expect(screen.getByRole("button", { name: "Next month" })).toBeTruthy();
  expect(screen.getByText("Spend by task")).toBeTruthy();
  // Diagnostic, so it is a section the reader opens — and a disclosure carries
  // its own state, which is why there is no second "hide" label to keep in
  // step with it.
  const summary = screen.getByText("Show days");
  const disclosure = summary.closest("details");
  expect(disclosure).toBeTruthy();
  expect(disclosure?.textContent).toContain("2026-07-20");
  expect(screen.queryByText("Hide days")).toBeNull();
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
