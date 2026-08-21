/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  act,
  cleanup,
  fireEvent,
  render as rtlRender,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { pickOption } from "../design-system/select-testing";
import { LocaleProvider } from "../i18n";
import {
  LeadScreen,
  LeadsScreen,
  promoteEligible,
  scoreTone,
  terminalBadge,
} from "./leads";

// The status/score-override/assign-to-me controls (Phase 4) resolve the
// session principal via /v1/me, which needs a workspace slug before it will
// even ask — set on every test, cleared after (mirrors automations.test.tsx).
beforeEach(() => {
  globalThis.localStorage.setItem("margince.workspaceSlug", "acme");
});

// Leads (B-EP09.10a/b, §3.5 segregation): visually SEGREGATED from the
// contact graph — the ≥60/40–59/<40 score thresholds, eligibility-gated
// promote, and a lead row navigating to the LEAD detail (never the person
// screen). Below that: the same P-14/15/16/1 shared-block wiring as contacts
// (people.test.tsx) and companies (organizations.test.tsx) — search/sort/
// pagination + a status filter, the rich create modal (full_name/email/
// linkedin_url/company_name), the lead-360 If-Match edit
// (Promote + badges preserved), and the duplicate_email dedupe link.

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  globalThis.localStorage.clear();
  window.location.hash = "";
});

// The six shipped lead sources, as GET /lead-sources serves them on a fresh
// installation.
const SHIPPED_LEAD_SOURCES = {
  data: [
    ["manual", "Created manually", "neutral"],
    ["inbound", "Inbound", "high"],
    ["webform", "Web form", "high"],
    ["referral", "Referral", "high"],
    ["import", "Import", "low"],
    ["crawl", "Web research", "low"],
  ].map(([key, label, intent], i) => ({
    id: `src-${key}`,
    key,
    label,
    intent,
    sort_order: (i + 1) * 10,
    active: true,
    system: true,
    lead_count: 0,
    version: 1,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  })),
  discovered: [],
};

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

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

const anna = {
  id: "p-1",
  full_name: "Anna Weber",
  captured_by: "human:u-1",
  source: "manual",
  version: 1,
};

const lead = {
  id: "l-1",
  full_name: "Jonas Petersen",
  email: "jonas@nordwind.example",
  company_name: "Nordwind Logistik",
  status: "contacted" as const,
  score: 72,
  captured_by: "human:u-1",
  source: "manual",
  version: 1,
  created_at: "2026-06-01T08:00:00Z",
  updated_at: "2026-06-20T08:00:00Z",
};

describe("score thresholds (AC-leads colour bands)", () => {
  it("maps ≥60 accent-strong, 40–59 medium, <40 low", () => {
    expect(scoreTone(60)).toBe("success");
    expect(scoreTone(95)).toBe("success");
    expect(scoreTone(59)).toBe("warn");
    expect(scoreTone(40)).toBe("warn");
    expect(scoreTone(39)).toBeUndefined();
  });
});

describe("promote eligibility gate", () => {
  it("requires an open status and an email", () => {
    expect(promoteEligible(lead)).toBe(true);
    expect(promoteEligible({ ...lead, status: "promoted" })).toBe(false);
    expect(promoteEligible({ ...lead, status: "disqualified" })).toBe(false);
    expect(promoteEligible({ ...lead, email: null })).toBe(false);
  });
});

describe("LeadsScreen + LeadScreen (B-EP09.10b, §3.5 segregation)", () => {
  it("names the owner on each row, the way the people and company lists do", async () => {
    // The column this replaced rendered "typed by a person" for every
    // human-captured row — the bug #1577 fixed on People and Companies while
    // this list kept its own copy of the column. Same column, same test.
    vi.stubGlobal(
      "fetch",
      vi.fn(async (request: Request) => {
        if (request.url.endsWith("/v1/me")) {
          return jsonResponse({
            user: { id: "u-9", display_name: "Me" },
            roles: ["rep"],
            teams: [],
          });
        }
        if (request.url.includes("/users")) {
          return jsonResponse({
            data: [
              { id: "u-9", email: "lena@x.test", display_name: "Lena F." },
            ],
            page: { next_cursor: null },
          });
        }
        return jsonResponse({
          data: [{ ...lead, owner_id: "u-9" }],
          page: { next_cursor: null },
        });
      }),
    );
    render(<LeadsScreen />);
    await waitFor(() => expect(screen.getByText("Lena F.")).toBeTruthy());
    expect(screen.queryByText(/typed by a person/i)).toBeNull();
  });

  it("a lead row navigates to the LEAD detail, not the person screen", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (request: Request) =>
        request.url.endsWith("/v1/me")
          ? jsonResponse({
              user: { id: "u-9", display_name: "Me" },
              roles: ["rep"],
              teams: [],
            })
          : jsonResponse({ data: [lead], page: { next_cursor: null } }),
      ),
    );
    render(<LeadsScreen />);
    await waitFor(() =>
      expect(screen.getByText("Jonas Petersen")).toBeTruthy(),
    );
    await userEvent.click(screen.getByText("Jonas Petersen"));
    expect(window.location.hash).toBe("#/leads/l-1");
  });

  // The app shell yields its page heading on a record route (app/shell.tsx:
  // PageTitle renders nothing there, and the trail stands in the top bar), so
  // the lead's own surface is the only thing that can name this page.
  it("names the page after the lead, at heading level one", async () => {
    stubFetch(async () => jsonResponse(lead));
    render(<LeadScreen id="l-1" />);
    expect(
      await screen.findByRole("heading", { level: 1, name: "Jonas Petersen" }),
    ).toBeTruthy();
  });

  it("the qualify dialog derives the reason from what was captured, and asks for no trigger", async () => {
    stubFetch(async () => jsonResponse(lead));
    render(<LeadScreen id="l-1" />);
    await userEvent.click(
      await screen.findByRole("button", { name: "Qualify…" }),
    );
    // A lead with no captured engagement is qualified by the rep's own
    // judgement — said as a sentence, not picked from a technical list.
    expect(await screen.findByText("Reason: qualified by you.")).toBeTruthy();
    expect(screen.queryByLabelText("Promotion trigger")).toBeNull();
  });

  it("the qualify dialog names the captured reply as the reason and sends that trigger with its activity", async () => {
    let promoteBody: unknown = null;
    stubFetch(async (url, method, request) => {
      if (method === "POST" && url.includes("/leads/l-1/promote")) {
        promoteBody = JSON.parse(await request.text());
        return jsonResponse({ person: anna, merged: false, lead_id: "l-1" });
      }
      return jsonResponse({
        ...lead,
        status: "engaged",
        qualification_evidence: {
          trigger: "inbound_reply",
          activity_id: "a-77",
          occurred_at: "2026-08-14T09:00:00Z",
        },
      });
    });
    render(<LeadScreen id="l-1" />);
    await userEvent.click(
      await screen.findByRole("button", { name: "Qualify…" }),
    );
    expect(await screen.findByText(/Reason: they replied on/)).toBeTruthy();
    // Engaged leads start with the deal block ticked; untick it here so the
    // request is the bare promotion.
    await userEvent.click(screen.getByTestId("lead-qualify-with-deal"));
    await userEvent.click(screen.getByTestId("lead-qualify-confirm"));
    await waitFor(() => expect(promoteBody).toBeTruthy());
    expect(promoteBody).toMatchObject({
      trigger: "inbound_reply",
      evidence: { activity_id: "a-77" },
    });
  });

  it("qualify posts the note, stays on the lead page and says who the lead became", async () => {
    const user = userEvent.setup();
    let promoteBody: unknown = null;
    stubFetch(async (url, method, request) => {
      if (method === "POST" && url.includes("/leads/l-1/promote")) {
        promoteBody = JSON.parse(await request.text());
        return jsonResponse({ person: anna, merged: false, lead_id: "l-1" });
      }
      if (url.includes("/people/")) {
        return jsonResponse(anna);
      }
      return jsonResponse(lead);
    });
    window.location.hash = "#/leads/l-1";
    render(<LeadScreen id="l-1" />);
    await user.click(await screen.findByRole("button", { name: "Qualify…" }));
    await user.type(
      screen.getByLabelText("Evidence note (optional)"),
      "Booked via calendly",
    );
    await user.click(screen.getByTestId("lead-qualify-confirm"));
    await waitFor(() => expect(promoteBody).toBeTruthy());
    expect(promoteBody).toMatchObject({
      trigger: "human_qualify",
      evidence: { note: "Booked via calendly" },
    });
    // The page stays (ADR-0119): the outcome is said here, with the contact.
    expect(window.location.hash).toBe("#/leads/l-1");
    expect(
      await screen.findByText(/Jonas Petersen is now a contact:/),
    ).toBeTruthy();
  });

  it("qualify with a deal sends the deal block with the chosen stage and the base currency", async () => {
    const user = userEvent.setup();
    let promoteBody: unknown = null;
    stubFetch(async (url, method, request) => {
      if (method === "POST" && url.includes("/leads/l-1/promote")) {
        promoteBody = JSON.parse(await request.text());
        return jsonResponse({
          person: anna,
          merged: false,
          lead_id: "l-1",
          deal_id: "d-1",
        });
      }
      if (url.endsWith("/v1/pipelines")) {
        return jsonResponse({
          data: [
            {
              id: "pl-1",
              name: "Sales",
              is_default: true,
              position: 0,
              stages: [
                {
                  id: "st-1",
                  pipeline_id: "pl-1",
                  name: "Qualified",
                  position: 1,
                  semantic: "open",
                  win_probability: 10,
                },
                {
                  id: "st-2",
                  pipeline_id: "pl-1",
                  name: "Discovery",
                  position: 2,
                  semantic: "open",
                  win_probability: 25,
                },
                {
                  id: "st-9",
                  pipeline_id: "pl-1",
                  name: "Won",
                  position: 9,
                  semantic: "won",
                  win_probability: 100,
                },
              ],
            },
          ],
          page: { has_more: false },
        });
      }
      if (url.endsWith("/v1/installation/settings")) {
        return jsonResponse({
          name: "Nordwind",
          timezone: "Europe/Berlin",
          base_currency: "EUR",
          base_currency_locked: false,
          max_upload_bytes: 1,
        });
      }
      return jsonResponse(lead);
    });
    render(<LeadScreen id="l-1" />);
    await user.click(await screen.findByRole("button", { name: "Qualify…" }));
    await user.click(screen.getByTestId("lead-qualify-with-deal"));
    await user.type(
      await screen.findByTestId("lead-qualify-deal-name"),
      "Nordwind — Pilot",
    );
    await user.type(screen.getByLabelText("Amount (EUR)"), "1200");
    await user.click(
      screen.getByRole("button", { name: "Qualify and open deal" }),
    );
    await waitFor(() => expect(promoteBody).toBeTruthy());
    expect(promoteBody).toMatchObject({
      trigger: "human_qualify",
      deal: {
        pipeline_id: "pl-1",
        stage_id: "st-1",
        name: "Nordwind — Pilot",
        amount_minor: 120000,
        currency: "EUR",
      },
    });
  });

  it("a 409 already_promoted is shown in the dialog rather than swallowed", async () => {
    const fetchMock = vi.fn(
      async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input instanceof Request ? input.url : input);
        const method =
          input instanceof Request ? input.method : (init?.method ?? "GET");
        if (method === "POST" && url.includes("/leads/l-1/promote")) {
          return jsonResponse(
            {
              title: "already promoted",
              code: "already_promoted",
              details: { promoted_person_id: "p-9" },
            },
            409,
          );
        }
        if (new URL(url).pathname.endsWith("/context")) {
          return jsonResponse({
            anchor: { type: "lead", id: "l-1" },
            sections: [],
          });
        }
        return jsonResponse(lead);
      },
    );
    vi.stubGlobal("fetch", fetchMock);
    render(<LeadScreen id="l-1" />);
    await userEvent.click(
      await screen.findByRole("button", { name: "Qualify…" }),
    );
    await userEvent.click(screen.getByTestId("lead-qualify-confirm"));
    // The refusal is shown where the reader is; the page re-reads the lead
    // and its outcome card then says what it became.
    expect(await screen.findByRole("alert")).toBeTruthy();
  });

  it("the board moves a lead between the two live statuses, with If-Match", async () => {
    // Only new and working: promoted and disqualified are reachable through
    // their own audited verbs, and a board column for either would offer a
    // drag that ends in a 422 — or worse, imply a lead can be promoted by
    // moving a card, which is what ADR-0008's trigger set exists to prevent.
    type Patched = { body: unknown; ifMatch: string | null };
    const patched: Patched[] = [];
    stubFetch(async (input: RequestInfo | URL, method: string, request) => {
      const url = String(input);
      if (method === "PATCH" && url.includes("/leads/l-1")) {
        patched.push({
          body: await request.json(),
          ifMatch: request.headers.get("If-Match"),
        });
        return jsonResponse({ ...lead, status: "contacted" });
      }
      if (url.includes("/leads?") || url.endsWith("/leads")) {
        return jsonResponse({
          data: [{ ...lead, status: "new", version: 7 }],
          page: { next_cursor: null, has_more: false },
        });
      }
      return emptyPage();
    });
    render(<LeadsScreen />);

    await userEvent.click(await screen.findByRole("button", { name: "Board" }));
    const card = await screen.findByText("Jonas Petersen");
    const workingColumn = screen.getByRole("region", { name: "Contacted" });

    // jsdom ships no DataTransfer, so the drag carries the same two methods
    // the handlers actually use — setData on the way out, getData on the way
    // in. Anything richer would be a mock of a thing this code never touches.
    const carried = new Map<string, string>();
    const data = {
      setData: (key: string, value: string) => carried.set(key, value),
      getData: (key: string) => carried.get(key) ?? "",
    };
    fireEvent.dragStart(card.closest("button") as HTMLElement, {
      dataTransfer: data,
    });
    fireEvent.drop(workingColumn, { dataTransfer: data });

    await waitFor(() => expect(patched.length).toBe(1));
    expect(patched[0].body).toMatchObject({ status: "contacted" });
    // The version rides the variables, so the write cannot clobber a change
    // made since this card rendered.
    expect(patched[0].ifMatch).toBe("7");
  });

  it("the board keeps the filter bar it obeys, and offers the rest of the page", async () => {
    // The board renders inside the list surface. Swapping the whole surface out
    // for it took the chips and the search with it — leaving the reader looking
    // at a narrowed answer with no way to see or change what narrowed it — and
    // showed page one while looking like the whole pipeline.
    stubFetch(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("/leads?") || url.endsWith("/leads")) {
        return jsonResponse({
          data: [{ ...lead, status: "new", version: 7 }],
          page: { next_cursor: "page-2", has_more: true },
        });
      }
      return emptyPage();
    });
    render(<LeadsScreen />);

    await userEvent.click(await screen.findByRole("button", { name: "Board" }));

    // The dials the board obeys are still on screen.
    expect(screen.getByRole("button", { name: "Add a filter" })).toBeTruthy();
    expect(screen.getByRole("searchbox")).toBeTruthy();
    // And it admits there is more than it is showing.
    expect(screen.getByRole("button", { name: "Load more" })).toBeTruthy();
  });

  it("edits a lead field in place, sending If-Match", async () => {
    // The Edit modal is four clicks and a context switch to fix a misspelled
    // company name. It stays for wholesale edits; the fields a rep corrects
    // while reading are corrected while reading.
    type Patched = { body: unknown; ifMatch: string | null };
    const patched: Patched[] = [];
    stubFetch(async (_url: string, method: string, request) => {
      if (method === "PATCH") {
        patched.push({
          body: await request.json(),
          ifMatch: request.headers.get("If-Match"),
        });
        return jsonResponse({ ...lead, company_name: "Nordwind GmbH" });
      }
      return jsonResponse(lead);
    });
    render(<LeadScreen id="l-1" />);

    // The row reads as its value; pressing it opens the field.
    await userEvent.click(
      await screen.findByRole("button", { name: "Change Company" }),
    );
    const field = await screen.findByDisplayValue("Nordwind Logistik");
    await userEvent.clear(field);
    await userEvent.type(field, "Nordwind GmbH");
    await userEvent.keyboard("{Enter}");

    await waitFor(() => expect(patched.length).toBe(1));
    expect(patched[0].body).toMatchObject({ company_name: "Nordwind GmbH" });
    expect(patched[0].ifMatch).toBe("1");
  });

  it("a terminal lead's fields are read-only with the reason, not missing", async () => {
    // STATE-4a: the reason is the information. A field that simply vanished on
    // a closed lead would hide the fact the reader came for.
    stubFetch(async () =>
      jsonResponse({
        ...lead,
        status: "disqualified",
        archived_at: "2026-07-13T00:00:00Z",
      }),
    );
    render(<LeadScreen id="l-1" />);

    // The value is still shown — as text, with no control to press.
    expect(
      (await screen.findAllByText("Nordwind Logistik")).length,
    ).toBeGreaterThan(0);
    expect(screen.queryByRole("button", { name: "Change Company" })).toBeNull();
    expect(screen.queryByDisplayValue("Nordwind Logistik")).toBeNull();
  });

  it("a promoted lead keeps its page and says what the promotion did", async () => {
    // AC-leaddetail-5 (ADR-0119/A170). The page used to redirect here, which
    // told the reader the lead had ceased to exist — untrue of a record this
    // product keeps, audits and can reverse — and left the reversal with
    // nowhere to start from. It also hid whether promotion merged into a
    // contact we already knew or created a new one.
    const promoted = {
      ...lead,
      status: "promoted",
      promoted_person_id: "p-42",
      promoted_at: "2026-06-20T08:00:00Z",
      archived_at: "2026-06-20T08:00:00Z",
    };
    stubFetch(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("/records/lead/")) {
        return jsonResponse({
          data: [
            {
              id: "a-1",
              actor_type: "human",
              actor_id: "human:u-9",
              action: "promote",
              occurred_at: "2026-06-20T08:00:00Z",
              after: {
                dedupe_outcome: "merged",
                trigger: "inbound_reply",
                evidence_note: "Replied asking for a quote.",
              },
            },
          ],
          page: { next_cursor: null, has_more: false },
        });
      }
      return jsonResponse(promoted);
    });
    render(<LeadScreen id="l-1" />);

    // It stays on the lead's own page.
    expect(await screen.findByText("Promoted to a contact")).toBeTruthy();
    expect(window.location.hash).not.toBe("#/contacts/p-42");
    // And it names WHICH outcome, which is the whole reason a rep opens it.
    // Awaited: the outcome comes from the audit read, a second request that
    // lands after the lead itself.
    expect(
      await screen.findByText(
        "This lead merged into a contact we already knew — no duplicate was created.",
      ),
    ).toBeTruthy();
    expect(screen.getByText(/Inbound reply/)).toBeTruthy();
    expect(screen.getByText(/Replied asking for a quote\./)).toBeTruthy();
  });

  it("the promote dialog says what promotion will do before the rep commits", async () => {
    // ADR-0119/A170: merge-into-existing vs create is the difference between
    // "my prospect is now a contact" and "my prospect was already someone we
    // knew". The preview runs the same ladder the promotion runs.
    stubFetch(async (url) => {
      if (url.includes("/promote-preview")) {
        return jsonResponse({ outcome: "merge", person: anna });
      }
      if (url.includes("/people/")) {
        return jsonResponse(anna);
      }
      return jsonResponse(lead);
    });
    render(<LeadScreen id="l-1" />);
    await userEvent.click(
      await screen.findByRole("button", { name: "Qualify…" }),
    );
    expect(
      await screen.findByText(/Promoting will merge into the existing contact/),
    ).toBeTruthy();
  });

  it("a withheld merge target is never read as 'create'", async () => {
    // An absent person on a merge means "outside your row scope", not "no
    // match": promising a new contact here would be the wrong half to guess.
    stubFetch(async (url) => {
      if (url.includes("/promote-preview")) {
        return jsonResponse({
          outcome: "merge",
          person_withheld: true,
        });
      }
      return jsonResponse(lead);
    });
    render(<LeadScreen id="l-1" />);
    await userEvent.click(
      await screen.findByRole("button", { name: "Qualify…" }),
    );
    expect(
      await screen.findByText(
        "Promoting will merge into an existing contact you cannot see.",
      ),
    ).toBeTruthy();
    expect(
      screen.queryByText("Promoting will create a new contact."),
    ).toBeNull();
  });

  it("a promoted lead can be demoted from its own page, with a recorded reason", async () => {
    // The reversal ADR-0008 §4 promises, from the one page that is a fact about
    // the promotion. The reason is required — the button stays disabled until
    // one is typed — and the request carries it verbatim.
    const promoted = {
      ...lead,
      status: "promoted",
      promoted_person_id: "p-42",
      promoted_at: "2026-06-20T08:00:00Z",
      archived_at: "2026-06-20T08:00:00Z",
    };
    let demoteBody: unknown = null;
    stubFetch(async (url, method, request) => {
      if (method === "POST" && url.includes("/leads/l-1/demote")) {
        demoteBody = JSON.parse(await request.text());
        return jsonResponse({
          lead: { ...lead, status: "contacted" },
          unwind: "reversed",
          person_id: "p-42",
        });
      }
      if (url.includes("/records/lead/")) {
        return jsonResponse({
          data: [],
          page: { next_cursor: null, has_more: false },
        });
      }
      return jsonResponse(promoted);
    });
    render(<LeadScreen id="l-1" />);
    await userEvent.click(
      await screen.findByRole("button", { name: "Reverse promotion" }),
    );
    const confirm = screen.getByRole("button", { name: "Reverse" });
    expect((confirm as HTMLButtonElement).disabled).toBe(true);
    await userEvent.type(
      screen.getByLabelText("Reason (recorded in the audit trail)"),
      "Promoted the wrong prospect",
    );
    await userEvent.click(confirm);
    await waitFor(() => expect(demoteBody).not.toBeNull());
    expect(demoteBody).toEqual({ reason: "Promoted the wrong prospect" });
  });

  it("a promoted lead reads as promoted, not disqualified", async () => {
    // Both closures archive the row, so a page keying its terminal sentence off
    // archived_at alone told every promoted lead it had been disqualified. The
    // redirect hid that until ADR-0119/A170 removed it.
    stubFetch(async (input: RequestInfo | URL) => {
      if (String(input).includes("/records/lead/")) {
        return jsonResponse({
          data: [],
          page: { next_cursor: null, has_more: false },
        });
      }
      return jsonResponse({
        ...lead,
        status: "promoted",
        promoted_person_id: "p-42",
        archived_at: "2026-06-20T08:00:00Z",
      });
    });
    render(<LeadScreen id="l-1" />);

    expect(
      await screen.findByText("Promoted — this lead is now read-only."),
    ).toBeTruthy();
    expect(screen.queryByText(/Disqualified — this lead/)).toBeNull();
  });

  it("finds the promotion when earlier audit rows push it onto a later page", async () => {
    // The history is served OLDEST FIRST, 20 to a page, and `promote` is the
    // LAST thing that happens to a lead. So a lead worked long enough to
    // collect a page of earlier rows carries its promotion on a later one, and
    // reading only the first page reported "we cannot tell" on exactly the
    // leads someone worked hardest.
    const filler = Array.from({ length: 20 }, (_, i) => ({
      id: `a-${i}`,
      actor_type: "human",
      actor_id: "human:u-9",
      action: "update",
      occurred_at: "2026-06-01T08:00:00Z",
      after: { status: "contacted" },
    }));
    stubFetch(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("/records/lead/")) {
        // The second page is asked for by cursor; the first hands one back.
        if (url.includes("cursor=")) {
          return jsonResponse({
            data: [
              {
                id: "a-99",
                actor_type: "human",
                actor_id: "human:u-9",
                action: "promote",
                occurred_at: "2026-06-20T08:00:00Z",
                after: {
                  dedupe_outcome: "merged",
                  trigger: "inbound_reply",
                },
              },
            ],
            page: { next_cursor: null, has_more: false },
          });
        }
        return jsonResponse({
          data: filler,
          page: { next_cursor: "page-2", has_more: true },
        });
      }
      return jsonResponse({
        ...lead,
        status: "promoted",
        promoted_person_id: "p-42",
        archived_at: "2026-06-20T08:00:00Z",
      });
    });
    render(<LeadScreen id="l-1" />);

    expect(
      await screen.findByText(
        "This lead merged into a contact we already knew — no duplicate was created.",
      ),
    ).toBeTruthy();
  });

  it("stops walking when a later page fails, and says so", async () => {
    // The pages already read stay cached, so hasNextPage stays true and
    // isFetchingNextPage falls back to false the moment the failure settles.
    // Without a stop that re-arms the walk forever — and `pending` outranking
    // `failed` would hide the error behind a waiting line the whole time.
    let historyCalls = 0;
    const filler = Array.from({ length: 20 }, (_, i) => ({
      id: `a-${i}`,
      actor_type: "human",
      actor_id: "human:u-9",
      action: "update",
      occurred_at: "2026-06-01T08:00:00Z",
      after: { status: "contacted" },
    }));
    stubFetch(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("/records/lead/")) {
        historyCalls += 1;
        if (url.includes("cursor=")) {
          return jsonResponse({ title: "boom" }, 500);
        }
        return jsonResponse({
          data: filler,
          page: { next_cursor: "page-2", has_more: true },
        });
      }
      return jsonResponse({
        ...lead,
        status: "promoted",
        promoted_person_id: "p-42",
        archived_at: "2026-06-20T08:00:00Z",
      });
    });
    render(<LeadScreen id="l-1" />);

    expect(
      await screen.findByText(
        "We cannot show whether this merged or created a contact.",
      ),
    ).toBeTruthy();
    // And it stopped rather than hammering the endpoint: page 1 plus a bounded
    // number of failed attempts, not an unbounded retry storm.
    expect(historyCalls).toBeLessThan(6);
  });

  it("re-reads the history after promoting, so the new audit row is not missed", async () => {
    // A reader who opened the History tab BEFORE promoting holds a cached last
    // page saying there is nothing more to fetch. Promotion writes the audit
    // row the panel reads its outcome from, so without invalidating that cache
    // the panel walks no further and reports the outcome unavailable while the
    // row sits one page away.
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const invalidated: unknown[] = [];
    const realInvalidate = client.invalidateQueries.bind(client);
    client.invalidateQueries = ((filters?: { queryKey?: unknown }) => {
      invalidated.push(filters?.queryKey);
      return realInvalidate(filters as never);
    }) as typeof client.invalidateQueries;

    stubFetch(async (input: RequestInfo | URL, method: string) => {
      const url = String(input);
      if (method === "POST" && url.includes("/promote")) {
        return jsonResponse({
          person: { id: "p-42", full_name: "Jonas Petersen" },
          merged: true,
          lead_id: "l-1",
        });
      }
      if (url.includes("/records/lead/")) {
        return jsonResponse({
          data: [],
          page: { next_cursor: null, has_more: false },
        });
      }
      return jsonResponse(lead);
    });
    rtlRender(
      <QueryClientProvider client={client}>
        <LocaleProvider initial="en">
          <LeadScreen id="l-1" />
        </LocaleProvider>
      </QueryClientProvider>,
    );

    await userEvent.click(
      await screen.findByRole("button", { name: "Qualify…" }),
    );
    await userEvent.click(screen.getByTestId("lead-qualify-confirm"));

    await waitFor(() =>
      expect(
        invalidated.some(
          (key) =>
            Array.isArray(key) &&
            key[0] === "record-history" &&
            key[1] === "lead",
        ),
      ).toBe(true),
    );
  });

  it("says it cannot tell the outcome rather than guessing 'created'", async () => {
    // An empty, unreadable, or unrecognised audit row is not a merge and not a
    // creation. Reporting one would be a confident claim about something
    // nobody recorded — and "created" is the wrong half to guess, because it
    // tells a rep no duplicate check happened.
    stubFetch(async (input: RequestInfo | URL) => {
      if (String(input).includes("/records/lead/")) {
        return jsonResponse({
          data: [
            {
              id: "a-1",
              actor_type: "human",
              actor_id: "human:u-9",
              action: "promote",
              occurred_at: "2026-06-20T08:00:00Z",
              after: { dedupe_outcome: "something_new" },
            },
          ],
          page: { next_cursor: null, has_more: false },
        });
      }
      return jsonResponse({
        ...lead,
        status: "promoted",
        promoted_person_id: "p-42",
        archived_at: "2026-06-20T08:00:00Z",
      });
    });
    render(<LeadScreen id="l-1" />);

    expect(
      await screen.findByText(
        "We cannot show whether this merged or created a contact.",
      ),
    ).toBeTruthy();
    expect(screen.queryByText("This lead became a new contact.")).toBeNull();
  });

  it("promote is disabled for an ineligible lead, and the button says why", async () => {
    // A LIVE lead with no email: ineligible, but still on screen. A promoted
    // lead is terminal and carries no promote control at all, so it cannot
    // stand in for "ineligible" (ADR-0119/A170).
    stubFetch(async () => jsonResponse({ ...lead, email: null }));
    render(<LeadScreen id="l-1" />);
    const button = await screen.findByRole("button", { name: "Qualify…" });
    await waitFor(() =>
      expect((button as HTMLButtonElement).disabled).toBe(true),
    );
    // The reason is wired to the control with aria-describedby, not stuffed
    // into a title a screen reader never announces on a disabled button.
    const describedBy = button.getAttribute("aria-describedby");
    expect(describedBy).toBeTruthy();
    expect(document.getElementById(describedBy as string)?.textContent).toBe(
      "needs an email address and an open status",
    );
  });
});

// A URL-capturing fetch stub shared across the P-14/15/16 wiring tests
// below: every request is recorded so a test can assert the params it
// carried, and a caller-supplied responder decides what comes back.
function stubFetch(
  responder: (
    url: string,
    method: string,
    request: Request,
  ) => Promise<Response>,
): { fetchMock: ReturnType<typeof vi.fn>; urls: string[] } {
  const urls: string[] = [];
  const fetchMock = vi.fn(async (request: Request) => {
    urls.push(request.url);
    if (new URL(request.url).pathname.endsWith("/context")) {
      return jsonResponse({
        anchor: { type: "lead", id: "l-1" },
        sections: [],
      });
    }
    // The administered vocabularies, as a fresh installation ships them:
    // the screens read their labels and pick lists off these.
    if (request.method === "GET" && request.url.endsWith("/v1/lead-sources")) {
      return jsonResponse(SHIPPED_LEAD_SOURCES);
    }
    if (
      request.method === "GET" &&
      request.url.endsWith("/v1/leads/settings")
    ) {
      return jsonResponse({
        first_response_enabled: true,
        first_response_target_minutes: 240,
      });
    }
    const answer = await responder(request.url, request.method, request);
    if (request.url.endsWith("/v1/me")) {
      const body: unknown = await answer.clone().json();
      if (typeof body === "object" && body !== null && "user" in body) {
        return answer;
      }
      return jsonResponse({
        user: { id: "u-9", display_name: "Me" },
        roles: ["rep"],
        teams: [],
      });
    }
    if (request.method === "GET" && request.url.includes("/manual-signals")) {
      const body: unknown = await answer.clone().json();
      if (
        typeof body !== "object" ||
        body === null ||
        !("data" in body) ||
        !Array.isArray(body.data)
      ) {
        return jsonResponse({ data: [] });
      }
    }
    return answer;
  });
  vi.stubGlobal("fetch", fetchMock);
  return { fetchMock, urls };
}

// The columns picker also offers a "Status" checkbox (the status column
// shares its header text with the filter attribute's label), so a plain
// name match is ambiguous — scope both the attribute pick and the value
// pick to the Filter button's own open menu.
// The menu names the step it is on — "Filter" while it lists attributes, then
// the attribute once one is picked — so each click is scoped to the menu as it
// stands at that moment rather than to a class name.
async function pickFilter(attribute: string, value: string) {
  await userEvent.click(
    await screen.findByRole("button", { name: "Add a filter" }),
  );
  const step = (name: string) => screen.getByRole("group", { name });
  await userEvent.click(
    within(step("Filter")).getByRole("button", { name: attribute }),
  );
  await userEvent.click(
    within(step(attribute)).getByRole("button", { name: value }),
  );
}

function emptyPage() {
  return jsonResponse({
    data: [],
    page: { next_cursor: null, has_more: false },
  });
}

describe("LeadsScreen — search/sort/pagination + status filter (P-14)", () => {
  it("carries the debounced search term into the next fetch", async () => {
    const { urls } = stubFetch(async () => emptyPage());
    render(<LeadsScreen />);
    const search = await screen.findByPlaceholderText("Search");

    vi.useFakeTimers();
    try {
      fireEvent.change(search, {
        target: { value: "jonas" },
      });
      act(() => {
        vi.advanceTimersByTime(250);
      });
    } finally {
      vi.useRealTimers();
    }

    await waitFor(() =>
      expect(urls.some((url) => url.includes("q=jonas"))).toBe(true),
    );
  });

  it("sends status=contacted when the status filter is set", async () => {
    const { urls } = stubFetch(async () => emptyPage());
    render(<LeadsScreen />);
    await waitFor(() => expect(urls.length).toBeGreaterThan(0));

    await pickFilter("Status", "Contacted");

    await waitFor(() =>
      expect(urls.some((url) => url.includes("status=contacted"))).toBe(true),
    );
  });

  it("an overdue lead wears the badge, and the Overdue view asks for sla_state=breached", async () => {
    // formulas §18.1: the first-response clock as the list shows it. A lead
    // within target carries no badge, so the list stays quiet until something
    // needs a hand.
    const { urls } = stubFetch(async () =>
      jsonResponse({
        data: [
          {
            ...lead,
            sla_state: "breached",
            sla_deadline_at: "2026-06-01T08:00:00Z",
          },
          {
            ...lead,
            id: "l-2",
            full_name: "Otto Fischer",
            sla_state: "within_target",
          },
        ],
        page: { next_cursor: null, has_more: false },
      }),
    );
    render(<LeadsScreen />);
    await waitFor(() => expect(screen.getByText("Otto Fischer")).toBeTruthy());
    expect(screen.getAllByText("Overdue").length).toBeGreaterThan(0);
    expect(screen.queryByText("On time")).toBeNull();

    await userEvent.click(screen.getByRole("button", { name: "Overdue" }));
    await waitFor(() =>
      expect(urls.some((url) => url.includes("sla_state=breached"))).toBe(true),
    );
  });

  it("the lead page says when the first response is due, and when it was given", async () => {
    stubFetch(async () =>
      jsonResponse({
        ...lead,
        sla_state: "at_risk",
        sla_deadline_at: "2026-06-01T08:00:00Z",
      }),
    );
    render(<LeadScreen id="l-1" />);
    expect(await screen.findByText(/First response due by/)).toBeTruthy();
    expect(screen.getByText("Due soon")).toBeTruthy();
  });

  it("bulk-assigns selected leads one PATCH each, every row with its own If-Match, and names the row that refused", async () => {
    // A naive fan-out sends one version to every row and 428s/409s on all but
    // the row it came from. Each row carries the version the list holds; a
    // row that moved under the reader is reported by name, not swallowed.
    const patches: Array<{
      id: string;
      ifMatch: string | null;
      body: unknown;
    }> = [];
    stubFetch(async (url, method, request) => {
      if (url.includes("/users")) {
        return jsonResponse({
          data: [{ id: "u-9", email: "lena@x.test", display_name: "Lena F." }],
          page: { next_cursor: null },
        });
      }
      if (method === "PATCH") {
        const id = url.split("/leads/")[1] ?? "";
        patches.push({
          id,
          ifMatch: request.headers.get("If-Match"),
          body: JSON.parse(await request.text()),
        });
        if (id === "l-2") {
          return new Response(
            JSON.stringify({
              title: "Conflict",
              status: 409,
              code: "version_skew",
              detail: "moved",
            }),
            {
              status: 409,
              headers: { "content-type": "application/problem+json" },
            },
          );
        }
        return jsonResponse({ ...lead, id, owner_id: "u-9", version: 8 });
      }
      return jsonResponse({
        data: [
          { ...lead, version: 3 },
          { ...lead, id: "l-2", full_name: "Otto Fischer", version: 7 },
        ],
        page: { next_cursor: null, has_more: false },
      });
    });
    render(<LeadsScreen />);
    await waitFor(() => expect(screen.getByText("Otto Fischer")).toBeTruthy());
    await userEvent.click(
      screen.getByRole("checkbox", { name: "Select Jonas Petersen" }),
    );
    await userEvent.click(
      screen.getByRole("checkbox", { name: "Select Otto Fischer" }),
    );
    expect(screen.getByText("2 selected")).toBeTruthy();

    await userEvent.click(screen.getByLabelText("New owner"));
    await userEvent.click(
      await screen.findByRole("option", { name: "Lena F." }),
    );
    await userEvent.click(screen.getByRole("button", { name: "Assign" }));

    await waitFor(() => expect(patches).toHaveLength(2));
    expect(patches.map((p) => [p.id, p.ifMatch])).toEqual([
      ["l-1", "3"],
      ["l-2", "7"],
    ]);
    expect(
      patches.every(
        (p) => JSON.stringify(p.body) === JSON.stringify({ owner_id: "u-9" }),
      ),
    ).toBe(true);
    // The row that refused is named, with the server's reason.
    expect(await screen.findByText(/1 not applied/)).toBeTruthy();
    expect(screen.getByText(/Otto Fischer: /)).toBeTruthy();
  });

  it("fetches the next cursor page when the pager steps past the loaded page", async () => {
    const { urls } = stubFetch(async (url) => {
      if (url.includes("cursor=c1")) {
        return jsonResponse({
          data: [{ ...lead, id: "l-2", full_name: "Otto Fischer" }],
          page: { next_cursor: null, has_more: false },
        });
      }
      return jsonResponse({
        data: [lead],
        page: { next_cursor: "c1", has_more: true },
      });
    });
    render(<LeadsScreen />);
    await waitFor(() =>
      expect(screen.getByText("Jonas Petersen")).toBeTruthy(),
    );

    const next = screen.getByRole("button", { name: "Next ›" });
    expect((next as HTMLButtonElement).disabled).toBe(false);
    await userEvent.click(next);

    await waitFor(() => expect(screen.getByText("Otto Fischer")).toBeTruthy());
    expect(urls.some((url) => url.includes("cursor=c1"))).toBe(true);
  });
});

describe("LeadsScreen — rich create (P-15)", () => {
  it("creates a manual lead owned by the current rep", async () => {
    let posted: unknown = null;
    stubFetch(async (url, method, request) => {
      if (method === "POST" && url.includes("/leads")) {
        posted = JSON.parse(await request.text());
        return jsonResponse({ ...lead, id: "l-new" }, 201);
      }
      return emptyPage();
    });
    render(<LeadsScreen />);
    await userEvent.click(await screen.findByTestId("new-record"));
    await userEvent.type(screen.getByLabelText("Full name *"), "Otto Fischer");
    await userEvent.type(screen.getByLabelText("Email"), "otto@example.test");
    await userEvent.type(
      screen.getByLabelText("LinkedIn URL"),
      "https://linkedin.com/in/otto",
    );
    await userEvent.type(screen.getByLabelText("Company"), "Otto Fischer GmbH");
    await userEvent.click(screen.getByRole("button", { name: "Create" }));

    await waitFor(() => expect(posted).toBeTruthy());
    expect(posted).toMatchObject({
      full_name: "Otto Fischer",
      email: "otto@example.test",
      linkedin_url: "https://linkedin.com/in/otto",
      company_name: "Otto Fischer GmbH",
      owner_id: "u-9",
      source: "manual",
      status: "new",
    });
  });

  it("finds an exact email duplicate before POST and links to it", async () => {
    let postCount = 0;
    stubFetch(async (url, method) => {
      if (method === "POST" && url.includes("/leads")) {
        postCount += 1;
      }
      if (
        method === "GET" &&
        url.includes("/leads?") &&
        url.includes("q=duplicate%40example.test")
      ) {
        return jsonResponse({
          data: [
            { ...lead, id: "existing-lead", email: "duplicate@example.test" },
          ],
          page: { next_cursor: null, has_more: false },
        });
      }
      return emptyPage();
    });
    render(<LeadsScreen />);
    await userEvent.click(await screen.findByTestId("new-record"));
    await userEvent.type(screen.getByLabelText("Full name *"), "Duplicate");
    await userEvent.type(
      screen.getByLabelText("Email"),
      "duplicate@example.test",
    );
    await userEvent.click(screen.getByRole("button", { name: "Create" }));

    expect(await screen.findByText("View existing record")).toBeTruthy();
    expect(postCount).toBe(0);
    await userEvent.click(screen.getByText("View existing record"));
    expect(window.location.hash).toBe("#/leads/existing-lead");
  });
});

describe("LeadScreen — edit with If-Match (P-1)", () => {
  it("PATCHes /leads/{id} with If-Match:<version> and only the update fields", async () => {
    let patchHeader: string | null = null;
    let patchBody: unknown = null;
    stubFetch(async (_url, method, request) => {
      if (method === "PATCH") {
        patchHeader = request.headers.get("If-Match");
        patchBody = JSON.parse(await request.text());
        return jsonResponse({ ...lead, title: "VP Sales", version: 2 });
      }
      return jsonResponse(lead);
    });
    render(<LeadScreen id="l-1" />);

    await waitFor(() => expect(screen.getByTestId("edit-record")).toBeTruthy());
    await userEvent.click(screen.getByTestId("edit-record"));
    const title = await screen.findByLabelText("Title");
    await userEvent.type(title, "VP Sales");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => expect(patchBody).toBeTruthy());
    expect(patchHeader).toBe("1");
    expect(patchBody).toMatchObject({ title: "VP Sales" });
    expect(patchBody).not.toHaveProperty("status");
    expect(patchBody).not.toHaveProperty("score");
  });

  it("preserves the Qualify button and score/status/company badges", async () => {
    stubFetch(async () => jsonResponse(lead));
    render(<LeadScreen id="l-1" />);
    await waitFor(() => expect(screen.getByTestId("edit-record")).toBeTruthy());
    expect(screen.getByRole("button", { name: "Qualify…" })).toBeTruthy();
    // The score reads in the band AND on the folded score section's summary.
    expect(screen.getAllByText("Score: 72").length).toBeGreaterThan(0);
    // The badge and the status control now read the SAME word, which is the
    // point: a German reader saw "In Bearbeitung" on the chip and the raw
    // enum "contacted" in the cell. The badge is the one asserted here.
    expect(
      screen
        .getAllByText("Contacted")
        .some((el) => el.classList.contains("badge")),
    ).toBe(true);
    expect(
      screen
        .getAllByText("Nordwind Logistik")
        .some((el) => el.classList.contains("badge")),
    ).toBe(true);
  });
});

describe("LeadScreen — disqualify (P-3)", () => {
  it("disqualify asks for a reason from the administered list, DELETEs with it, and keeps the page", async () => {
    const user = userEvent.setup();
    let deleteBody: unknown = null;
    stubFetch(async (url, method, request) => {
      if (method === "DELETE" && url.includes("/leads/l-1")) {
        deleteBody = JSON.parse(await request.text());
        return jsonResponse({
          ...lead,
          status: "disqualified",
          archived_at: "2026-07-13T00:00:00Z",
          disqualify_reason: "Bad timing",
        });
      }
      if (url.endsWith("/v1/lead-disqualify-reasons")) {
        return jsonResponse({
          data: [
            {
              id: "r-1",
              label: "Not a good fit",
              sort_order: 10,
              active: true,
              system: true,
              lead_count: 0,
              version: 1,
              created_at: "2026-01-01T00:00:00Z",
              updated_at: "2026-01-01T00:00:00Z",
            },
            {
              id: "r-2",
              label: "Bad timing",
              sort_order: 20,
              active: true,
              system: true,
              lead_count: 0,
              version: 1,
              created_at: "2026-01-01T00:00:00Z",
              updated_at: "2026-01-01T00:00:00Z",
            },
            {
              id: "r-3",
              label: "Retired",
              sort_order: 30,
              active: false,
              system: false,
              lead_count: 0,
              version: 1,
              created_at: "2026-01-01T00:00:00Z",
              updated_at: "2026-01-01T00:00:00Z",
            },
          ],
        });
      }
      return jsonResponse(lead);
    });
    window.location.hash = "#/leads/l-1";
    render(<LeadScreen id="l-1" />);

    await user.click(await screen.findByTestId("lead-disqualify"));
    // Nothing is sent without a reason, and the button says so.
    const confirm = screen.getByTestId(
      "lead-disqualify-confirm",
    ) as HTMLButtonElement;
    expect(confirm.disabled).toBe(true);
    await pickOption(user, screen.getByLabelText("Reason *"), "Bad timing");
    expect(screen.queryByRole("option", { name: "Retired" })).toBeNull();
    await user.type(
      screen.getByLabelText("Note (optional)"),
      "Call back in Q4",
    );
    await user.click(screen.getByTestId("lead-disqualify-confirm"));

    await waitFor(() => expect(deleteBody).toBeTruthy());
    expect(deleteBody).toEqual({ reason_id: "r-2", note: "Call back in Q4" });
    expect(window.location.hash).toBe("#/leads/l-1");
  });
});

describe("LeadScreen — overlay mode write affordances", () => {
  // The mirror's own write-back seam serves update for a lead
  // (overlay/provider_writes.go SupportsWrite), so Edit renders here.
  // DELETE /leads/{id} is disqualify_lead, a cross-type lifecycle
  // transition the seam refuses outright — Disqualify stays hidden.
  function meResponse() {
    return jsonResponse({
      user: { id: "u1", email: "me@nordwind.example", locale: "en-US" },
      roles: ["admin"],
      teams: [],
      system_of_record: { mode: "overlay" },
    });
  }

  it("serves Edit, hides Disqualify", async () => {
    stubFetch(async (url, method) => {
      if (url.includes("/me")) {
        return meResponse();
      }
      if (method === "PATCH") {
        return jsonResponse(lead);
      }
      return jsonResponse(lead);
    });
    render(<LeadScreen id="l-1" />);

    await waitFor(() => expect(screen.getByTestId("edit-record")).toBeTruthy());
    expect(screen.queryByTestId("lead-disqualify")).toBeNull();
  });

  it("Edit's real click path PATCHes and the 360 shows the saved name", async () => {
    // Mutable so the refetch after a successful save (useUpdateRecord
    // invalidates the record query) reflects the write, not a stale echo —
    // the same "mirror re-read reflects write-back" shape
    // overlay.Provider.Update gives via mirrorWriteResult.
    let current = lead;
    stubFetch(async (url, method, request) => {
      if (url.includes("/me")) {
        return meResponse();
      }
      if (method === "PATCH") {
        const body = JSON.parse(await request.text());
        current = { ...current, ...body };
        return jsonResponse(current);
      }
      return jsonResponse(current);
    });
    render(<LeadScreen id="l-1" />);

    await waitFor(() => expect(screen.getByTestId("edit-record")).toBeTruthy());
    await userEvent.click(screen.getByTestId("edit-record"));
    const fullName = await screen.findByLabelText("Full name *");
    await userEvent.clear(fullName);
    await userEvent.type(fullName, "Jonas Petersen-Berg");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    // The saved name now reads in two places — the record header and the
    // inline Details row — which is the point of the grid, not a duplicate.
    expect(
      (await screen.findAllByText("Jonas Petersen-Berg")).length,
    ).toBeGreaterThan(0);
  });

  it("names the partial write-back in the edit form", async () => {
    stubFetch(async (url) => {
      if (url.includes("/me")) {
        return meResponse();
      }
      return jsonResponse(lead);
    });
    render(<LeadScreen id="l-1" />);

    await waitFor(() => expect(screen.getByTestId("edit-record")).toBeTruthy());
    await userEvent.click(screen.getByTestId("edit-record"));
    expect(
      screen.getByText(/Only the fields HubSpot accepts are written back/),
    ).toBeTruthy();
  });
});

describe("LeadsScreen — archived marking (P-3)", () => {
  it("shows a Disqualified badge on a row with archived_at set", async () => {
    stubFetch(async () =>
      jsonResponse({
        data: [
          {
            ...lead,
            status: "disqualified",
            archived_at: "2026-07-01T00:00:00Z",
          },
        ],
        page: { next_cursor: null, has_more: false },
      }),
    );
    render(<LeadsScreen />);
    await waitFor(() =>
      expect(screen.getByText("Jonas Petersen")).toBeTruthy(),
    );
    expect(
      screen.getByText("Disqualified", { selector: "span.badge-warn" }),
    ).toBeTruthy();
  });
});

describe("LeadsScreen — the one ownership dial (DM-VOCAB-OWN-1)", () => {
  it("opens on my queue and sends the source filter to the server", async () => {
    const { urls } = stubFetchWithMe(async (url) => {
      if (url.includes("/leads")) {
        return jsonResponse({
          data: [lead],
          page: { next_cursor: null, has_more: false },
        });
      }
      if (url.includes("/teams")) {
        return jsonResponse({ data: [], page: { next_cursor: null } });
      }
      return undefined;
    });
    render(<LeadsScreen />);
    await waitFor(() =>
      expect(screen.getByText("Jonas Petersen")).toBeTruthy(),
    );

    const leadReads = urls.filter((url) => url.includes("/leads?"));
    expect(leadReads.length).toBeGreaterThan(0);
    expect(leadReads.every((url) => url.includes("owner_id=u-9"))).toBe(true);

    await pickFilter("Source", "Web form");
    await waitFor(() =>
      expect(
        urls.some(
          (url) =>
            url.includes("/leads?") &&
            url.includes("owner_id=u-9") &&
            url.includes("source=webform"),
        ),
      ).toBe(true),
    );
  });

  it("offers the unowned queue and asks the server for it as unassigned=true", async () => {
    // The lead list once carried its own owner chip with only "mine", because
    // listLeads lacked owner_team_id/unassigned. It binds the SAME dial the
    // person and company lists use now — the fork is gone.
    const user = userEvent.setup();
    const { urls } = stubFetchWithMe(async (url) => {
      if (url.includes("/leads")) {
        return jsonResponse({
          data: [lead],
          page: { next_cursor: null, has_more: false },
        });
      }
      if (url.includes("/teams")) {
        return jsonResponse({ data: [], page: { next_cursor: null } });
      }
      return undefined;
    });
    render(<LeadsScreen />);
    await waitFor(() =>
      expect(screen.getByText("Jonas Petersen")).toBeTruthy(),
    );

    const owner = await screen.findByRole("group", { name: /Owner:/ });
    const valueButton = within(owner)
      .getAllByRole("button", { name: "My records" })
      .find((button) => button.hasAttribute("aria-expanded"));
    if (!valueButton) throw new Error("owner value control is missing");
    await user.click(valueButton);
    await user.click(within(owner).getByRole("button", { name: "Unassigned" }));

    await waitFor(() =>
      expect(
        urls.some(
          (u) => u.includes("/leads?") && u.includes("unassigned=true"),
        ),
      ).toBe(true),
    );
  });
});

describe("LeadsScreen — dedupe view-existing link (P-16)", () => {
  it("renders a link to the collided record on a duplicate_email 409", async () => {
    stubFetch(async (url, method) => {
      if (method === "POST" && url.includes("/leads")) {
        return jsonResponse(
          {
            type: "about:blank",
            title: "Conflict",
            detail: "email already in use",
            code: "duplicate_email",
            details: { existing_id: "01X" },
          },
          409,
        );
      }
      return emptyPage();
    });
    render(<LeadsScreen />);
    await userEvent.click(await screen.findByTestId("new-record"));
    await userEvent.type(screen.getByLabelText("Full name *"), "Dup Lead");
    await userEvent.type(screen.getByLabelText("Email"), "dup@example.test");
    await userEvent.click(screen.getByRole("button", { name: "Create" }));

    await waitFor(() =>
      expect(screen.getByText("View existing record")).toBeTruthy(),
    );
    await userEvent.click(screen.getByText("View existing record"));
    expect(window.location.hash).toBe("#/leads/01X");
  });
});

// A URL-capturing fetch stub that also answers /v1/me — the three Phase-4
// lifecycle controls (status, score override, assign-to-me) all need the
// session principal resolved, so every test below sets a workspace slug and
// serves /v1/me alongside the lead responses.
function stubFetchWithMe(
  responder: (
    url: string,
    method: string,
    request: Request,
  ) => Promise<Response | undefined>,
  meId = "u-9",
): { urls: string[] } {
  const urls: string[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (request: Request) => {
      urls.push(request.url);
      if (request.url.endsWith("/v1/me")) {
        return jsonResponse({
          user: { id: meId, display_name: "Me" },
          roles: ["rep"],
          teams: [],
        });
      }
      if (new URL(request.url).pathname.endsWith("/context")) {
        return jsonResponse({
          anchor: { type: "lead", id: "l-1" },
          sections: [],
        });
      }
      if (
        request.method === "GET" &&
        request.url.endsWith("/v1/lead-sources")
      ) {
        return jsonResponse(SHIPPED_LEAD_SOURCES);
      }
      if (
        request.method === "GET" &&
        request.url.endsWith("/v1/leads/settings")
      ) {
        return jsonResponse({
          first_response_enabled: true,
          first_response_target_minutes: 240,
        });
      }
      const answer = await responder(request.url, request.method, request);
      if (
        request.method === "GET" &&
        request.url.includes("/manual-signals") &&
        answer
      ) {
        const body: unknown = await answer.clone().json();
        if (
          typeof body !== "object" ||
          body === null ||
          !("data" in body) ||
          !Array.isArray(body.data)
        ) {
          return jsonResponse({ data: [] });
        }
      }
      return answer ?? jsonResponse(lead);
    }),
  );
  return { urls };
}

describe("LeadScreen — status control (P-12)", () => {
  it("the ladder PATCHes the step the rep clicks, with If-Match", async () => {
    let patchHeader: string | null = null;
    let patchBody: unknown = null;
    stubFetchWithMe(async (url, method, request) => {
      if (method === "PATCH" && url.includes("/leads/l-1")) {
        patchHeader = request.headers.get("If-Match");
        patchBody = JSON.parse(await request.text());
        return jsonResponse({ ...lead, status: "contacted", version: 2 });
      }
      return undefined;
    });
    render(<LeadScreen id="l-1" />);

    // The fixture lead is already contacted; the next rung up is the click.
    await waitFor(() =>
      expect(screen.getByTestId("lead-step-engaged")).toBeTruthy(),
    );
    expect(
      (screen.getByTestId("lead-step-contacted") as HTMLButtonElement).disabled,
    ).toBe(true);
    await userEvent.click(screen.getByTestId("lead-step-engaged"));

    await waitFor(() => expect(patchBody).toBeTruthy());
    expect(patchHeader).toBe("1");
    expect(patchBody).toMatchObject({ status: "engaged" });
  });

  it("freezes the ladder on a promoted/disqualified lead, with the reason", async () => {
    stubFetchWithMe(async () =>
      jsonResponse({
        ...lead,
        status: "promoted",
        archived_at: "2026-07-13T00:00:00Z",
      }),
    );
    render(<LeadScreen id="l-1" />);

    await waitFor(() => expect(screen.getByTestId("edit-record")).toBeTruthy());
    for (const step of ["new", "contacted", "engaged", "disqualified"]) {
      const control = screen.getByTestId(
        `lead-step-${step}`,
      ) as HTMLButtonElement;
      expect(control.disabled).toBe(true);
    }
    expect(
      (screen.getByTestId("lead-step-promoted") as HTMLButtonElement).disabled,
    ).toBe(true);
  });
});

describe("LeadScreen — score explain + override (P-10)", () => {
  it("shows Override score for a non-overridden lead; submit requires a reason and PATCHes score + reason", async () => {
    let patchBody: unknown = null;
    stubFetchWithMe(async (url, method, request) => {
      if (method === "PATCH" && url.includes("/leads/l-1")) {
        patchBody = JSON.parse(await request.text());
        return jsonResponse({
          ...lead,
          score: 90,
          score_override_reason: "Strong buying signal",
          score_computed: 72,
          version: 2,
        });
      }
      return undefined;
    });
    render(<LeadScreen id="l-1" />);

    await waitFor(() =>
      expect(
        screen.getByRole("button", { name: "Override score" }),
      ).toBeTruthy(),
    );
    await userEvent.click(
      screen.getByRole("button", { name: "Override score" }),
    );

    const submit = screen.getByRole("button", { name: "Save override" });
    expect((submit as HTMLButtonElement).disabled).toBe(true);

    const scoreInput = screen.getByLabelText("Score");
    const reasonInput = screen.getByLabelText("Reason");
    await userEvent.clear(scoreInput);
    await userEvent.type(scoreInput, "90");
    await userEvent.type(reasonInput, "Strong buying signal");
    expect((submit as HTMLButtonElement).disabled).toBe(false);

    await userEvent.click(submit);

    await waitFor(() => expect(patchBody).toBeTruthy());
    expect(patchBody).toMatchObject({
      score: 90,
      score_override_reason: "Strong buying signal",
    });
  });

  it("disables Save override for an out-of-range or non-integer score even with a reason filled in", async () => {
    let patchBody: unknown = null;
    stubFetchWithMe(async (url, method, request) => {
      if (method === "PATCH" && url.includes("/leads/l-1")) {
        patchBody = JSON.parse(await request.text());
        return jsonResponse({ ...lead, version: 2 });
      }
      return undefined;
    });
    render(<LeadScreen id="l-1" />);

    await waitFor(() =>
      expect(
        screen.getByRole("button", { name: "Override score" }),
      ).toBeTruthy(),
    );
    await userEvent.click(
      screen.getByRole("button", { name: "Override score" }),
    );

    const submit = screen.getByRole("button", { name: "Save override" });
    const scoreInput = screen.getByLabelText("Score");
    const reasonInput = screen.getByLabelText("Reason");
    await userEvent.type(reasonInput, "Strong buying signal");

    await userEvent.clear(scoreInput);
    await userEvent.type(scoreInput, "150");
    expect((submit as HTMLButtonElement).disabled).toBe(true);

    await userEvent.clear(scoreInput);
    await userEvent.type(scoreInput, "-5");
    expect((submit as HTMLButtonElement).disabled).toBe(true);

    await userEvent.clear(scoreInput);
    await userEvent.type(scoreInput, "90.5");
    expect((submit as HTMLButtonElement).disabled).toBe(true);

    await userEvent.clear(scoreInput);
    await userEvent.type(scoreInput, "90");
    expect((submit as HTMLButtonElement).disabled).toBe(false);

    await userEvent.click(submit);
    await waitFor(() => expect(patchBody).toBeTruthy());
    expect(patchBody).toMatchObject({ score: 90 });
  });

  it("shows the reason + machine value and Clear override for an overridden lead", async () => {
    let patchBody: unknown = null;
    const overridden = {
      ...lead,
      score: 90,
      score_override_reason: "Strong buying signal",
      score_computed: 72,
    };
    stubFetchWithMe(async (url, method, request) => {
      if (method === "PATCH" && url.includes("/leads/l-1")) {
        patchBody = JSON.parse(await request.text());
        return jsonResponse({ ...lead, score: 72, version: 2 });
      }
      return jsonResponse(overridden);
    });
    render(<LeadScreen id="l-1" />);

    await waitFor(() =>
      expect(screen.getByText(/Strong buying signal/)).toBeTruthy(),
    );
    expect(screen.getByText(/72/)).toBeTruthy();
    const clear = screen.getByRole("button", { name: "Clear override" });
    await userEvent.click(clear);

    await waitFor(() => expect(patchBody).toBeTruthy());
    expect(patchBody).toMatchObject({ score: null });
  });
});

describe("LeadScreen — owner display + assign to me (P-11)", () => {
  it("shows Unassigned and assigning to yourself PATCHes owner_id to the current user", async () => {
    let patchBody: unknown = null;
    stubFetchWithMe(async (url, method, request) => {
      if (method === "PATCH" && url.includes("/leads/l-1")) {
        patchBody = JSON.parse(await request.text());
        return jsonResponse({ ...lead, owner_id: "u-9", version: 2 });
      }
      if (url.includes("/users")) {
        // The viewer is an ordinary roster entry now — the picker offers them
        // first rather than a separate button doing it.
        return jsonResponse({
          data: [{ id: "u-9", display_name: "Me" }],
          page: { next_cursor: null, has_more: false },
        });
      }
      return undefined;
    }, "u-9");
    render(<LeadScreen id="l-1" />);

    await waitFor(() => expect(screen.getByText("Unassigned")).toBeTruthy());
    // ONE control, not a self-assign button beside a picker: the viewer is
    // simply its first option (ADR-0108 §5).
    await userEvent.click(
      await screen.findByRole("button", { name: "Assign" }),
    );
    await userEvent.click(
      await screen.findByRole("combobox", { name: "Assign this lead to" }),
    );
    await userEvent.click(
      await screen.findByRole("option", { name: "Assign to me" }),
    );

    await waitFor(() => expect(patchBody).toBeTruthy());
    expect(patchBody).toMatchObject({ owner_id: "u-9" });
  });

  it("hides Assign to me when the lead is already owned by the current user", async () => {
    const { urls } = stubFetchWithMe(
      async () => jsonResponse({ ...lead, owner_id: "u-9" }),
      "u-9",
    );
    render(<LeadScreen id="l-1" />);

    // Owned by the current user settles to the "You" owner line once /me
    // resolves (LeadScreen subscribes to the probe up front); waiting on that
    // settled state — rather than the transient raw owner id — is what makes
    // the self-assign assertion below reliable.
    await waitFor(() => expect(screen.getByText("You")).toBeTruthy());
    await waitFor(() =>
      expect(urls.some((url) => url.endsWith("/v1/me"))).toBe(true),
    );
    expect(screen.queryByRole("button", { name: "Assign to me" })).toBeNull();
  });
});

describe("LeadScreen — History tab", () => {
  it("shows a History tab that lists record changes", async () => {
    stubFetchWithMe(async (url) => {
      if (url.includes("/history")) {
        return jsonResponse({
          data: [
            {
              id: "h1",
              actor_type: "human",
              actor_id: "u1",
              action: "update",
              occurred_at: "2026-07-13T10:00:00Z",
              summary: "Lead score changed",
            },
          ],
          page: { next_cursor: null },
        });
      }
      return undefined;
    });
    render(<LeadScreen id="l-1" />);

    await waitFor(() =>
      expect(screen.getByRole("button", { name: /history/i })).toBeTruthy(),
    );
    await userEvent.click(screen.getByRole("button", { name: /history/i }));

    await waitFor(() =>
      expect(screen.getByText("Lead score changed")).toBeTruthy(),
    );
    // The identity header (name) must stay visible on the History tab, not
    // just the overview — it lives in LeadScreen above the tab switch now,
    // matching person/company/deal's persistent RecordView header.
    expect(screen.getByText(lead.full_name)).toBeTruthy();
  });
});

describe("terminalBadge (archived/terminal labelling)", () => {
  it("labels disqualified and promoted distinctly and leaves open leads unbadged", () => {
    expect(terminalBadge("disqualified")).toEqual({
      label: "lead.disqualified",
      tone: "warn",
    });
    // A promoted lead IS archived, but reads "Archived" — never "Disqualified".
    expect(terminalBadge("promoted")).toEqual({
      label: "record.archived",
      tone: "warn",
    });
    expect(terminalBadge("new")).toBeNull();
    expect(terminalBadge("contacted")).toBeNull();
  });
});

describe("LeadScreen — archived/terminal is read-only (P-3)", () => {
  it("a disqualified lead exposes no enabled mutation anywhere on the page", async () => {
    // The stop-gate caught what the header-only check missed: Edit, Disqualify
    // and Share were disabled while the score override, the clear-override and
    // the owner picker stayed live, each able to fire a PATCH the server
    // refuses. Asserted over EVERY button on the page rather than a list of
    // the ones I remembered, so a control added later is covered by
    // construction.
    stubFetchWithMe(async (url) => {
      if (url.includes("/score")) {
        return jsonResponse({ score: 72, explained: false });
      }
      return jsonResponse({
        ...lead,
        status: "disqualified",
        score_override_reason: "set by hand",
        score_computed: 46,
        archived_at: "2026-07-13T00:00:00Z",
      });
    });
    render(<LeadScreen id="l-1" />);

    await waitFor(() =>
      // Two elements legitimately say "Disqualified" now — the status badge
      // and the terminal badge — so the wait names the badge it meant.
      expect(screen.getAllByText("Disqualified").length).toBeGreaterThan(0),
    );
    // The tab bar is navigation, not mutation; everything else must be dead.
    const navigation = new Set(["Overview", "History"]);
    for (const button of screen.getAllByRole("button")) {
      if (navigation.has(button.textContent?.trim() ?? "")) {
        continue;
      }
      expect(
        (button as HTMLButtonElement).disabled,
        `"${button.textContent}" is still live on a terminal lead`,
      ).toBe(true);
    }
  });

  it("a disqualified lead keeps its controls DISABLED with the reason, never hidden", async () => {
    // STATE-4a: blocked by state rather than permission means visible and
    // disabled with the reason — hiding the control hides the fact the
    // reader needs. (A PROMOTED lead never reaches this page; it redirects
    // to the person it became.)
    stubFetchWithMe(async () =>
      jsonResponse({
        ...lead,
        status: "disqualified",
        archived_at: "2026-07-13T00:00:00Z",
      }),
    );
    render(<LeadScreen id="l-1" />);

    await waitFor(() =>
      // Two elements legitimately say "Disqualified" now — the status badge
      // and the terminal badge — so the wait names the badge it meant.
      expect(screen.getAllByText("Disqualified").length).toBeGreaterThan(0),
    );
    const reason = "Disqualified — this lead is now read-only.";
    for (const testId of ["edit-record", "lead-disqualify"]) {
      const control = screen.getByTestId(testId) as HTMLButtonElement;
      expect(control.disabled).toBe(true);
      const describedBy = control.getAttribute("aria-describedby");
      expect(describedBy).toBeTruthy();
      expect(document.getElementById(describedBy as string)?.textContent).toBe(
        reason,
      );
    }
    // Qualify is gone rather than disabled: a disqualified lead is not a
    // qualifiable one, and the header's primary action is for live leads.
    expect(screen.queryByRole("button", { name: "Qualify…" })).toBeNull();
  });

  it("shows an 'overridden' badge when the score is human-overridden", async () => {
    stubFetchWithMe(async () =>
      jsonResponse({
        ...lead,
        score_override_reason: "Strong buying signal",
        score_computed: 50,
      }),
    );
    render(<LeadScreen id="l-1" />);
    await waitFor(() => expect(screen.getByText("overridden")).toBeTruthy());
  });

  it("explains the score with its factors and the arithmetic that reconciles them", async () => {
    stubFetchWithMe(async (url) => {
      if (url.includes("/score")) {
        return jsonResponse({
          score: 46,
          explained: true,
          current: {
            score: 46,
            score_computed: 46,
            raw_sum: 45.6,
            rounded_sum: 46,
            computed_at: "2026-06-04T00:00:00Z",
            factors: [
              { factor: "decision_maker_title", points: 15 },
              { factor: "reply", points: 22.6, base_points: 25 },
            ],
          },
        });
      }
      return jsonResponse(lead);
    });
    render(<LeadScreen id="l-1" />);

    await waitFor(() =>
      expect(screen.getByText("Decision-maker title")).toBeTruthy(),
    );
    expect(screen.getByText("They replied")).toBeTruthy();
    // The decay is shown as arithmetic a reader can check, not asserted.
    expect(screen.getByText("25 halving every 14 days")).toBeTruthy();
    expect(
      screen.getByText("45.60 adds up, rounds to 46, scored 46"),
    ).toBeTruthy();
  });

  it("shows a manual signal from the breakdown, and offers the questions that add one", async () => {
    // S-E13.6 / ADR-0105 §4: the human half of the score reads its exact
    // stored band and provenance, rather than guessing them from points.
    //
    // What the FORM sends is pinned in leadsignals.test.tsx, which drives every
    // one of its states. What only this test can prove is that the panel reaches
    // the lead page at all: the stored signal, its reason and its author render
    // here, and the questions that add another are offered beside them.
    stubFetchWithMe(async (url) => {
      if (url.includes("/users")) {
        return jsonResponse({
          data: [{ id: "u-9", email: "lena@x.test", display_name: "Lena F." }],
          page: { next_cursor: null },
        });
      }
      if (url.includes("/manual-signals")) {
        return jsonResponse({
          data: [
            {
              factor: "budget_hint",
              band: "some",
              points: 4,
              signal_kind: "fact",
              confidence: 0.9,
              reason: "CFO named a Q4 line item",
              set_by: "u-9",
              set_at: "2026-06-04T00:00:00Z",
            },
          ],
        });
      }
      return jsonResponse(lead);
    });
    render(<LeadScreen id="l-1" />);
    await waitFor(() =>
      expect(screen.getByText("CFO named a Q4 line item")).toBeTruthy(),
    );
    for (const question of ["Website traffic?", "Company size?", "Budget?"]) {
      expect(screen.getByLabelText(question)).toBeTruthy();
    }
    expect(
      screen.getByRole("button", { name: "Add to the score" }),
    ).toBeTruthy();
  });

  it("a closed lead shows its manual signals read-only, with the reason", async () => {
    stubFetchWithMe(async (url) => {
      if (url.includes("/manual-signals")) {
        return jsonResponse({ data: [] });
      }
      if (url.includes("/score")) {
        return jsonResponse({ score: 0, explained: false });
      }
      return jsonResponse({
        ...lead,
        status: "disqualified",
        archived_at: "2026-06-20T08:00:00Z",
      });
    });
    render(<LeadScreen id="l-1" />);
    await waitFor(() =>
      expect(screen.getByText("What you know about this lead")).toBeTruthy(),
    );
    expect(
      screen.queryByRole("button", { name: "Add to the score" }),
    ).toBeNull();
    expect(
      screen.getAllByText("This lead is closed and takes no changes.").length,
    ).toBeGreaterThan(0);
  });

  it("never prints an absent source into the sentence about it", async () => {
    // The suite let this ship: a lead with no source interpolated the missing
    // value and rendered "Came in as undefined" at a rep.
    stubFetchWithMe(async (url) => {
      if (url.includes("/score")) {
        return jsonResponse({ score: 0, explained: false });
      }
      return jsonResponse({ ...lead, score: 0, source: null, title: null });
    });
    render(<LeadScreen id="l-1" />);

    await waitFor(() =>
      expect(
        screen.getAllByText(
          "No source on record — nothing says where this lead came from.",
        ).length,
      ).toBeTruthy(),
    );
    expect(screen.queryByText(/undefined/)).toBeNull();
  });

  it("never claims nothing counts when the score is not zero", async () => {
    // The render caught this: a lead scoring 72 with no retained breakdown
    // was told "Nothing counts toward this score yet", which is the opposite
    // of true. Something counted; this client cannot say what.
    stubFetchWithMe(async (url) => {
      if (url.includes("/score")) {
        return jsonResponse({ score: 72, explained: false });
      }
      return jsonResponse({ ...lead, score: 72 });
    });
    render(<LeadScreen id="l-1" />);

    await waitFor(() =>
      expect(
        screen.getByText(
          "The breakdown for this score isn\u2019t stored yet — the next update will show it.",
        ),
      ).toBeTruthy(),
    );
    expect(screen.queryByText("What this score has to work with:")).toBeNull();
  });

  it("says why a lead scores nothing rather than explaining our storage history", async () => {
    stubFetchWithMe(async (url) => {
      if (url.includes("/score")) {
        return jsonResponse({ score: 0, explained: false });
      }
      return jsonResponse({ ...lead, score: 0, title: null });
    });
    render(<LeadScreen id="l-1" />);

    // The reasons a lead earns nothing are derivable from the lead itself, and
    // they are what the reader came for. "This score predates the breakdown"
    // answered a question nobody asked and left a 0 looking like a bad
    // prospect rather than an unassessed one (ADR-0108 §4).
    await waitFor(() =>
      expect(
        screen.getByText("What this score has to work with:"),
      ).toBeTruthy(),
    );
    // Deliberately NOT "no reply yet": engagement lives in linked activities
    // this client never reads, so the page states what MOVES the score rather
    // than asserting the prospect has done nothing.
    expect(
      screen.getByText("A reply or a meeting is what moves it most."),
    ).toBeTruthy();
  });

  it("names the owner 'You' when the lead is owned by the current user", async () => {
    stubFetchWithMe(
      async () => jsonResponse({ ...lead, owner_id: "u-9" }),
      "u-9",
    );
    render(<LeadScreen id="l-1" />);
    await waitFor(() => expect(screen.getByText("You")).toBeTruthy());
  });

  it("names an owner who is not the viewer, rather than printing their id", async () => {
    stubFetchWithMe(async (url) => {
      if (url.includes("/users")) {
        return jsonResponse({
          data: [{ id: "u-42", display_name: "Dana Fischer" }],
          page: { next_cursor: null, has_more: false },
        });
      }
      return jsonResponse({ ...lead, owner_id: "u-42" });
    }, "u-9");
    render(<LeadScreen id="l-1" />);

    await waitFor(() => expect(screen.getByText("Dana Fischer")).toBeTruthy());
    // The defect this replaced: an owner who was not the viewer rendered as a
    // bare uuid, which names nobody a reader can recognize. The id survives as
    // the `title` (and as the fallback face until the roster lands), so what is
    // asserted is that no id is left standing as the visible TEXT.
    expect(screen.queryByText("u-42")).toBeNull();
  });

  it("assigns the lead to another user picked from the roster", async () => {
    let patchBody: Record<string, unknown> | null = null;
    stubFetchWithMe(async (url, method, request) => {
      if (url.includes("/users")) {
        return jsonResponse({
          data: [{ id: "u-42", display_name: "Dana Fischer" }],
          page: { next_cursor: null, has_more: false },
        });
      }
      if (method === "PATCH") {
        patchBody = (await request.json()) as Record<string, unknown>;
        return jsonResponse({ ...lead, owner_id: "u-42" });
      }
      return undefined;
    }, "u-9");
    render(<LeadScreen id="l-1" />);

    await userEvent.click(
      await screen.findByRole("button", { name: "Assign" }),
    );
    // The listbox is opened ONCE and the option awaited inside it: clicking
    // the trigger again would toggle it shut, and the popup re-renders in
    // place when the roster lands.
    await userEvent.click(
      await screen.findByRole("combobox", { name: "Assign this lead to" }),
    );
    await userEvent.click(
      await screen.findByRole("option", { name: "Dana Fischer" }),
    );

    await waitFor(() => expect(patchBody).toBeTruthy());
    expect(patchBody).toMatchObject({ owner_id: "u-42" });
  });
});
