/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  render as rtlRender,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
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
function stub(
  overrides: Record<
    string,
    (req: Request) => Response | Promise<Response>
  > = {},
) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (request: Request) => {
      const pathname = new URL(request.url).pathname;
      for (const [suffix, respond] of Object.entries(overrides)) {
        if (pathname.endsWith(suffix)) {
          return await respond(request);
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
    render(
      <CompanyRail
        orgId="o-1"
        view={view()}
        withPeople
        loading={false}
        composerOpen
      />,
    );
    expect(screen.queryByText("Details")).not.toBeInTheDocument();
  });

  it("draws the details grid from the fields the record actually carries", async () => {
    stub();
    render(
      <CompanyRail
        orgId="o-1"
        view={view()}
        withPeople
        loading={false}
        composerOpen={false}
      />,
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
        loading={false}
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

  it("opens and saves the LinkedIn URL inline even when one is already set", async () => {
    let patchBody: unknown;
    stub({
      "/me": () =>
        jsonResponse({
          user: { id: "u-1", display_name: "Mira Voss" },
          authorization: { objects: { organization: { update: true } } },
        }),
      "/organizations/o-1": async (request) => {
        if (request.method === "PATCH") {
          patchBody = await request.json();
          return jsonResponse({ ...org, version: 2 });
        }
        return jsonResponse(org);
      },
    });
    render(
      <CompanyRail
        orgId="o-1"
        view={view()}
        withPeople
        loading={false}
        composerOpen={false}
      />,
    );
    // The value is already set, which is exactly the case that used to render
    // a bare link with no control in any branch — there was nothing to open.
    // Waits for /me's grant to resolve first: the button exists only once
    // `canEdit` turns true, same as every other inline control here.
    await userEvent.click(
      await screen.findByRole("button", { name: "Change LinkedIn URL" }),
    );
    const input = screen.getByLabelText("LinkedIn URL");
    await userEvent.clear(input);
    await userEvent.type(input, "https://linkedin.com/company/brandt-gmbh");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() =>
      expect(patchBody).toMatchObject({
        linkedin_url: "https://linkedin.com/company/brandt-gmbh",
      }),
    );
  });

  it("edits owner through the same roster-backed control the header uses", async () => {
    let patchBody: unknown;
    stub({
      "/me": () =>
        jsonResponse({
          user: { id: "u-1", display_name: "Mira Voss" },
          authorization: { objects: { organization: { update: true } } },
        }),
      "/users": () =>
        jsonResponse({
          data: [
            { id: "u-1", display_name: "Mira Voss" },
            { id: "u-2", display_name: "Ravi Shah" },
          ],
          page: emptyPage,
        }),
      "/organizations/o-1": async (request) => {
        if (request.method === "PATCH") {
          patchBody = await request.json();
          return jsonResponse({ ...org, version: 2 });
        }
        return jsonResponse(org);
      },
    });
    render(
      <CompanyRail
        orgId="o-1"
        view={view()}
        withPeople
        loading={false}
        composerOpen={false}
      />,
    );
    await waitFor(() =>
      expect(screen.getByText("Mira Voss")).toBeInTheDocument(),
    );
    await userEvent.click(screen.getByRole("button", { name: "Change Owner" }));
    await userEvent.click(screen.getByRole("combobox"));
    await userEvent.click(screen.getByRole("option", { name: "Ravi Shah" }));
    await waitFor(() => expect(patchBody).toMatchObject({ owner_id: "u-2" }));
  });

  it("surfaces the server's refusal on the owner control rather than swallowing it", async () => {
    // A stale roster entry (a user removed between page load and save) is the
    // one way an accepted-looking choice still fails: the wire FK on
    // organization.owner_id (core 0019) rejects it as a reference the server
    // cannot resolve, and the rail's owner control must show that sentence
    // next to itself — the same generic refusal path InlineChoice already
    // proves in design-system/inlinechoice.test.tsx, exercised here through
    // the shared control this grid now reuses.
    stub({
      "/me": () =>
        jsonResponse({
          user: { id: "u-1", display_name: "Mira Voss" },
          authorization: { objects: { organization: { update: true } } },
        }),
      "/users": () =>
        jsonResponse({
          data: [
            { id: "u-1", display_name: "Mira Voss" },
            { id: "u-2", display_name: "Ravi Shah" },
          ],
          page: emptyPage,
        }),
      "/organizations/o-1": (request) => {
        if (request.method === "PATCH") {
          return jsonResponse(
            {
              type: "about:blank",
              title: "Unprocessable Entity",
              status: 422,
              code: "reference_not_found",
              detail: "The referenced record was not found.",
            },
            422,
          );
        }
        return jsonResponse(org);
      },
    });
    render(
      <CompanyRail
        orgId="o-1"
        view={view()}
        withPeople
        loading={false}
        composerOpen={false}
      />,
    );
    await waitFor(() =>
      expect(screen.getByText("Mira Voss")).toBeInTheDocument(),
    );
    await userEvent.click(screen.getByRole("button", { name: "Change Owner" }));
    await userEvent.click(screen.getByRole("combobox"));
    await userEvent.click(screen.getByRole("option", { name: "Ravi Shah" }));
    await waitFor(() =>
      expect(screen.getByRole("alert").textContent).toContain(
        "The referenced record was not found.",
      ),
    );
    // The picker stays open on the reader's choice rather than snapping back
    // to the old owner, the same rule InlineChoice keeps for every field.
    expect(screen.getByRole("combobox")).toBeInTheDocument();
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
        loading={false}
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
        loading={false}
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
        loading={false}
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
      <CompanyRail
        orgId="o-1"
        view={view()}
        withPeople
        loading={false}
        composerOpen={false}
      />,
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
        loading={false}
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
      <CompanyRail
        orgId="o-1"
        view={view()}
        withPeople
        loading={false}
        composerOpen={false}
      />,
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
        loading={false}
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

  // sectionState (company360.tsx) reads an undefined `view` as "loading" only
  // when told the composite read is still running — otherwise it reads the
  // exact same undefined `view` as "unavailable", the words a real outage
  // shows. Before `loading` was threaded through, every ordinary page open
  // flashed "could not be loaded" on Health/People/Tags for as long as the
  // read was in flight. Pinned here as two DIFFERENT renders rather than one
  // happy-path check, because a fix that only makes the loading case not
  // crash is not the fix — it has to render DIFFERENTLY from the failed case.
  it("reads an in-flight composite read as loading, not unavailable", () => {
    stub();
    render(
      <CompanyRail
        orgId="o-1"
        view={undefined}
        loading={true}
        withPeople
        composerOpen={false}
      />,
    );
    // The skeleton placeholder, not the "could not be loaded" sentence.
    expect(
      screen.queryByText(
        "Could not be loaded — this may not be the whole picture",
      ),
    ).not.toBeInTheDocument();
  });

  it("reads a failed composite read (view undefined, not loading) as unavailable", () => {
    stub();
    render(
      <CompanyRail
        orgId="o-1"
        view={undefined}
        loading={false}
        withPeople
        composerOpen={false}
      />,
    );
    // The SAME undefined `view` as the loading test above, but with
    // `loading={false}` — the honest "could not be loaded" sentence, not a
    // skeleton pretending a read is still running.
    expect(
      screen.getAllByText(
        "Could not be loaded — this may not be the whole picture",
      ).length,
    ).toBeGreaterThan(0);
  });
});
