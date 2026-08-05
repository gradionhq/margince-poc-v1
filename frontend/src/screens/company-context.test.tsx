/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { components } from "../api/schema";
import { LocaleProvider } from "../i18n";
import { CompanyContextCard } from "./company-context";

type Capabilities = components["schemas"]["CompanyContextCapabilities"];
type CompanyProfile = components["schemas"]["CompanyProfile"];
type Comparison = components["schemas"]["CompanySiteReadComparison"];
type SiteRead = components["schemas"]["CompanySiteRead"];

// read_enabled is the card's own gate — it renders nothing below the `read`
// rollout stage, so the fixture has to clear it before any assertion is
// reachable.
const CAPABILITIES: Capabilities = {
  rollout: "onboarding",
  read_enabled: true,
  tasks_enabled: true,
  onboarding_enabled: true,
};

// A website is what arms the refresh control; without one the button stays
// disabled and no comparison ever reaches the screen.
const COMPANY: CompanyProfile = {
  organization_id: "00000000-0000-4000-8000-000000000010",
  display_name: "Acme",
  website: "acme.test",
  offer_summary: "We sell field service software",
  icp: "Operations leads at mid-market installers",
};

// One comparison per classification. The two selectable ones deliberately
// carry field keys whose labels read nothing alike ("What do you sell?" vs
// "Registered legal name"), because the defect this guards — one shared,
// field-less label on every checkbox — is invisible whenever a row is
// inspected on its own.
const COMPARISONS: readonly Comparison[] = [
  {
    key: "offer_summary",
    value_kind: "profile_field",
    classification: "new",
    current_value: null,
    current_source: null,
    proposed_value: "Field service software for installers",
  },
  {
    key: "legal_name",
    value_kind: "profile_field",
    classification: "machine_change",
    current_value: "Acme Ltd",
    current_source: "site_read",
    proposed_value: "Acme GmbH",
  },
  {
    key: "icp",
    value_kind: "profile_field",
    classification: "human_conflict",
    current_value: "Operations leads at mid-market installers",
    current_source: "human",
    proposed_value: "Enterprise facility managers",
  },
  {
    key: "industry",
    value_kind: "profile_field",
    classification: "unchanged",
    current_value: "Software",
    current_source: "human",
    proposed_value: "Software",
  },
];

// `ready` is terminal for the poller: the review renders once and the query
// stops refetching, so the suite never waits on a clock.
const SITE_READ: SiteRead = {
  id: "00000000-0000-4000-8000-000000000020",
  target_kind: "onboarding",
  root_url: "https://acme.test",
  status: "ready",
  status_code: null,
  status_detail: null,
  next_attempt_at: null,
  pages: [{ url: "https://acme.test", status: "fetched", kind: "home" }],
  profile_fields: [],
  facts: [],
  comparisons: [...COMPARISONS],
  people: [],
  warnings: [],
  draft_version: 3,
  proposal_hash: "sha256:acme",
  created_at: "2026-01-05T09:00:00Z",
  updated_at: "2026-01-05T09:04:00Z",
};

function backend() {
  return vi.fn(async (input: RequestInfo | URL) => {
    const url = new URL(String(input instanceof Request ? input.url : input));
    const body = routeBody(url.pathname);
    return new Response(JSON.stringify(body), {
      headers: { "Content-Type": "application/json" },
    });
  });
}

function routeBody(path: string): unknown {
  if (path === "/v1/company/context/capabilities") {
    return CAPABILITIES;
  }
  // Starting a read and polling it both answer with the whole read, so one
  // body serves POST /site-reads and GET /site-reads/{id}.
  if (path.startsWith("/v1/company/site-reads")) {
    return SITE_READ;
  }
  if (path === "/v1/company") {
    return COMPANY;
  }
  throw new Error(`unstubbed request: ${path}`);
}

function Providers({ children }: Readonly<{ children: ReactNode }>) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return (
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">{children}</LocaleProvider>
    </QueryClientProvider>
  );
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

// Reaching the review costs two sequential round trips — the POST that starts
// the read, then the GET that fetches it — each landing through react-query's
// own state cycle. Nothing here waits on a clock: `ready` is terminal, so the
// poller's refetchInterval is false and the chain settles as fast as the
// scheduler runs it. What varies is only how quickly that is, and the default
// one-second budget is not enough when the whole suite runs in parallel, so
// this states a budget that survives a loaded machine.
const SETTLE_MS = 10_000;

// Renders the card and drives it to the review step, where the comparison
// cards live.
async function renderReview() {
  vi.stubGlobal("fetch", backend());
  render(
    <Providers>
      <CompanyContextCard />
    </Providers>,
  );
  const refresh = await screen.findByRole(
    "button",
    { name: "Refresh from website" },
    { timeout: SETTLE_MS },
  );
  fireEvent.click(refresh);
  await screen.findByRole(
    "heading",
    { name: "Review what changed" },
    { timeout: SETTLE_MS },
  );
}

describe("CompanyContextCard refresh review", () => {
  it("names the field each change checkbox selects", async () => {
    await renderReview();

    // Two checkboxes, two accessible names. getByRole is exact and unique, so
    // a shared label — every row announcing the same words — fails both
    // lookups rather than passing one of them.
    expect(
      screen.getByRole("checkbox", {
        name: "Select the What do you sell? change",
      }),
    ).toBeTruthy();
    expect(
      screen.getByRole("checkbox", {
        name: "Select the Registered legal name change",
      }),
    ).toBeTruthy();

    const names = screen
      .getAllByRole("checkbox")
      .map((box) => box.getAttribute("aria-label"));
    expect(new Set(names).size).toBe(names.length);
  });

  it("offers a checkbox only for a change that can be selected", async () => {
    await renderReview();

    // A human conflict is decided by radio, not selected by checkbox, and an
    // unchanged value has nothing to apply — a checkbox on either would write
    // a change the reviewer never chose.
    expect(screen.getAllByRole("checkbox")).toHaveLength(2);
    expect(
      screen.queryByRole("checkbox", { name: /Ideal customer/ }),
    ).toBeNull();
    expect(screen.queryByRole("checkbox", { name: /Industry/ })).toBeNull();
    expect(screen.getByRole("radio", { name: "Keep current" })).toBeTruthy();
  });
});
