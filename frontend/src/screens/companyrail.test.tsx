/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  render as rtlRender,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import "@testing-library/jest-dom/vitest";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { components } from "../api/schema";
import { LocaleProvider } from "../i18n";
import { CompanyRail } from "./companyrail";

type Organization360 = components["schemas"]["Organization360"];

// The rail's own honesty rules: a section the caller's role withheld says so
// rather than drawing the empty state that would read as "there is none",
// and a field the record does not carry still draws its row — an unfilled
// field is a fact worth showing, not one this grid hides.

const org = {
  id: "o-1",
  workspace_id: "w",
  display_name: "Brandt Automotive GmbH",
  legal_name: "Brandt Automotive GmbH",
  lifecycle: "customer" as const,
  owner_id: "u-1",
  industry: "Automotive",
  size_band: "51-200" as const,
  linkedin_url: "https://linkedin.com/company/brandt",
  address: { city: "Munich", country: "DE" },
  domains: [{ domain: "brandt.example", is_primary: true, source: "manual" }],
  captured_by: "human:u1",
  source: "manual",
  version: 1,
  created_at: "2026-06-01T08:00:00Z",
  updated_at: "2026-06-01T08:00:00Z",
};

const emptyPage = { has_more: false, next_cursor: null };

// Built loosely and cast once here, matching company360.test.tsx's own
// fixture: a hand-typed 360 payload restates the generated schema by hand,
// and the two would silently drift the moment the contract grows a field
// this suite never needed.
function view(overrides: Record<string, unknown> = {}): Organization360 {
  return {
    as_of: "2026-06-01T09:00:00Z",
    organization: org,
    sections_omitted: [],
    people: { data: [], page: emptyPage },
    tags: [],
    list_memberships: [],
    ...overrides,
  } as unknown as Organization360;
}

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json" },
  });
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

function render(ui: ReactNode) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return rtlRender(
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">{ui}</LocaleProvider>
    </QueryClientProvider>,
  );
}

// The one path every test needs, whatever it is asserting: the finance
// summary Health reads for its payment dimension, the roster the owner
// row resolves against, and the signals feed. `overrides` answers with
// whatever the test is actually about.
function stub(overrides: Record<string, (req: Request) => Response> = {}) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (request: Request) => {
      const pathname = new URL(request.url).pathname;
      for (const [suffix, respond] of Object.entries(overrides)) {
        if (pathname.endsWith(suffix)) {
          return respond(request);
        }
      }
      if (pathname.endsWith("/finance-summary")) {
        return jsonResponse({ organization_id: "o-1", state: "no_connection" });
      }
      if (pathname.endsWith("/users")) {
        return jsonResponse({
          data: [{ id: "u-1", display_name: "Mira Voss" }],
          page: emptyPage,
        });
      }
      if (pathname.endsWith("/signals")) {
        return jsonResponse({ data: [], page: emptyPage });
      }
      return jsonResponse({ data: [], page: emptyPage });
    }),
  );
}

describe("CompanyRail", () => {
  it("renders nothing while the composer holds the column", () => {
    stub();
    render(<CompanyRail orgId="o-1" view={view()} withPeople composerOpen />);
    expect(screen.queryByText("Details")).not.toBeInTheDocument();
  });

  it("draws the details grid from the fields the record actually carries", async () => {
    stub();
    render(
      <CompanyRail orgId="o-1" view={view()} withPeople composerOpen={false} />,
    );
    expect(screen.getByText("Brandt Automotive GmbH")).toBeInTheDocument();
    expect(screen.getByText("Automotive")).toBeInTheDocument();
    expect(screen.getByText("51-200")).toBeInTheDocument();
    expect(screen.getByText("Munich, DE")).toBeInTheDocument();
    expect(screen.getByText("brandt.example")).toBeInTheDocument();
    // The owner cell resolves through the roster read, same as EntityRef
    // does everywhere else: not shown until the read lands.
    await waitFor(() =>
      expect(screen.getByText("Mira Voss")).toBeInTheDocument(),
    );
  });

  it("still draws every known row when the record carries no value for it", () => {
    stub();
    const bare = {
      ...org,
      legal_name: null,
      industry: null,
      size_band: null,
      linkedin_url: null,
      address: undefined,
      domains: [],
      owner_id: null,
    };
    render(
      <CompanyRail
        orgId="o-1"
        view={view({ organization: bare })}
        withPeople
        composerOpen={false}
      />,
    );
    // Every row's LABEL still draws: an absent field is a fact about the
    // record, not a reason to hide the row that would say so. Both Industry
    // (InlineText, which draws no label of its own) and Company size
    // (InlineChoice with `hideLabel`, which suppresses its own visible
    // "label: value") get their visible label from FieldRow's own label
    // column and no other node — checked directly since neither wraps its
    // label into a combined string anymore.
    expect(screen.getByText("Industry")).toBeInTheDocument();
    expect(screen.getByText("Company size")).toBeInTheDocument();
    expect(screen.getByText("Address")).toBeInTheDocument();
    // The read-only rows (owner, domain, address) fall back to a stated
    // absence rather than an empty cell — "Unassigned"/"Not set" are facts,
    // not blanks.
    expect(screen.getByText("Unassigned")).toBeInTheDocument();
    expect(screen.getAllByText("Not set").length).toBeGreaterThan(0);
  });

  it("shows the account's rating in the Health summary rather than a count", () => {
    stub();
    render(
      <CompanyRail
        orgId="o-1"
        view={view({
          health: {
            relationship: { rating: "good", reason: "Replying steadily." },
          },
        })}
        withPeople
        composerOpen={false}
      />,
    );
    expect(screen.getByText("Good")).toBeInTheDocument();
    expect(screen.getByText("Relationship")).toBeInTheDocument();
    expect(screen.getByText("Replying steadily.")).toBeInTheDocument();
  });

  it("marks a withheld section restricted instead of drawing it empty", () => {
    stub();
    render(
      <CompanyRail
        orgId="o-1"
        view={view({ sections_omitted: ["health", "people"] })}
        withPeople
        composerOpen={false}
      />,
    );
    expect(
      screen.getAllByText("Hidden — your role cannot read this").length,
    ).toBeGreaterThan(0);
    // A withheld section carries no count badge: a "0" beside it would read
    // as a confirmed empty account rather than as a permission boundary.
    const peopleSummary = screen.getByText("People").closest("summary");
    expect(peopleSummary).not.toBeNull();
    expect(peopleSummary && within(peopleSummary).queryByText("0")).toBeNull();
  });

  it("draws one row per contact, naming the colleagues already in touch with them", () => {
    stub();
    render(
      <CompanyRail
        orgId="o-1"
        view={view({
          people: {
            data: [
              {
                person_id: "p-1",
                full_name: "Dana Buyer",
                title: "VP Procurement",
                strength: {
                  score: 40,
                  bucket: "warm",
                  factors: {},
                  inbound_90d: 1,
                },
                deal_roles: [],
                consent: {},
                routes: {
                  top: [
                    {
                      user_id: "u-2",
                      display_name: "Ravi Shah",
                      strength_bucket: "strong",
                    },
                  ],
                  remainder: 0,
                  untried: false,
                },
              },
            ],
            page: emptyPage,
          },
        })}
        withPeople
        composerOpen={false}
      />,
    );
    expect(screen.getByText("Dana Buyer")).toBeInTheDocument();
    expect(screen.getByText("VP Procurement")).toBeInTheDocument();
    // Named again for anyone not reading the stacked avatars as monograms:
    // the sr-only text beside them, not the face itself.
    expect(screen.getByText("Ravi Shah")).toBeInTheDocument();
  });

  it("draws a signal row with its severity dot, kind, summary and date", async () => {
    stub({
      "/signals": () =>
        jsonResponse({
          data: [
            {
              id: "s-1",
              workspace_id: "w",
              kind: "risk",
              source_channel: "derived",
              resolution_state: "resolved",
              severity: "urgent",
              summary: "No reply in three weeks.",
              evidence: [],
              status: "open",
              detected_at: "2026-06-01T00:00:00Z",
              source: "manual",
              captured_by: "human:u1",
              created_at: "2026-06-01T00:00:00Z",
              updated_at: "2026-06-01T00:00:00Z",
            },
          ],
          page: emptyPage,
        }),
    });
    render(
      <CompanyRail orgId="o-1" view={view()} withPeople composerOpen={false} />,
    );
    await waitFor(() =>
      expect(screen.getByText("No reply in three weeks.")).toBeInTheDocument(),
    );
    expect(screen.getByText("Risk")).toBeInTheDocument();
  });

  it("shows tags and list memberships as their own badges, counted together", () => {
    stub();
    render(
      <CompanyRail
        orgId="o-1"
        view={view({
          tags: [{ id: "t-1", workspace_id: "w", name: "Key account" }],
          list_memberships: [
            {
              id: "l-1",
              workspace_id: "w",
              name: "Renewal Q3",
              entity_type: "organization",
            },
          ],
        })}
        withPeople
        composerOpen={false}
      />,
    );
    expect(screen.getByText("Key account")).toBeInTheDocument();
    expect(screen.getByText("Renewal Q3")).toBeInTheDocument();
  });

  it("offers the add-tag and add-to-list verbs once each half has answered, on a writable record", async () => {
    stub();
    render(
      <CompanyRail orgId="o-1" view={view()} withPeople composerOpen={false} />,
    );
    // Both halves answered `empty` (view()'s tags/list_memberships default to
    // []), so both verbs render beside the half they act on.
    expect(
      await screen.findByRole("button", { name: /add to list/i }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /add tag/i }),
    ).toBeInTheDocument();
  });

  it("withholds the add-tag and add-to-list verbs on an archived record", async () => {
    stub();
    render(
      <CompanyRail
        orgId="o-1"
        view={view({
          organization: { ...org, archived_at: "2026-06-02T00:00:00Z" },
        })}
        withPeople
        composerOpen={false}
      />,
    );
    // The values themselves still draw (this suite's own empty-badges test
    // covers that); only the verbs stand down.
    await waitFor(() => {
      expect(
        screen.queryByRole("button", { name: /add to list/i }),
      ).not.toBeInTheDocument();
      expect(
        screen.queryByRole("button", { name: /add tag/i }),
      ).not.toBeInTheDocument();
    });
  });
});
