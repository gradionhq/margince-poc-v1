/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  render as rtlRender,
  screen,
  waitFor,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { components } from "../api/schema";
import { meFixture } from "../app/mefixture";
import { LocaleProvider } from "../i18n";
import { AccountBrief, AskSection } from "./company360";
import { CompanyScreen } from "./organizations";
import { PersonMeetingBrief } from "./persondrawers";

// Every AI surface can be told which project it is about, through the one
// picker, and every scoped output says so in one line the server's counts
// fill. These pin the wire: the request carries `project_id` exactly when a
// project is chosen, and the line renders from the response rather than from
// anything the page worked out for itself.

type Organization360 = components["schemas"]["Organization360"];
type ProjectScope = components["schemas"]["ProjectScope"];

const CAPTURED = {
  source: "manual",
  captured_by: "human:u-1",
  created_at: "2026-06-01T08:00:00Z",
  updated_at: "2026-08-01T08:00:00Z",
} as const;

const emptyPage = { has_more: false, next_cursor: null };

const erp = {
  project_id: "p-erp",
  name: "ERP rollout",
  key: "ERP-27",
  phase: "delivering",
} as const;
const migration = {
  project_id: "p-dc",
  name: "Datacentre migration",
  key: "DC-4",
  phase: "pursuing",
} as const;

const view: Organization360 = {
  as_of: "2026-08-13T09:00:00Z",
  organization: { id: "o-1", display_name: "Brandt", ...CAPTURED },
  sections_omitted: [],
  projects: [erp, migration],
};

const erpScope: ProjectScope = {
  project_id: "p-erp",
  name: "ERP rollout",
  key: "ERP-27",
  in_scope: 4,
  total: 11,
};

const emptyBrief = {
  organization_id: "o-1",
  generated_at: "2026-08-13T09:00:00Z",
  generated_by: "deterministic",
  sections: [],
};

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json" },
  });
}

// One recorded request: the path with its query string, and the parsed
// write body when there was one.
type Seen = { method: string; url: string; body: unknown };

// A stub that keeps the QUERY STRING — the thing under test — and answers
// from a handler keyed on method + path.
function stubFetch(
  routes: Record<string, (seen: Seen) => Response>,
  fallback: () => Response = () => jsonResponse({ data: [], page: emptyPage }),
): Seen[] {
  const seen: Seen[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (request: Request) => {
      const url = new URL(request.url);
      let body: unknown = null;
      if (request.method !== "GET") {
        try {
          body = await request.json();
        } catch {
          body = null;
        }
      }
      const record = {
        method: request.method,
        url: url.pathname.replace(/^\/v1/, "") + url.search,
        body,
      };
      seen.push(record);
      const handler =
        routes[`${request.method} ${url.pathname.replace(/^\/v1/, "")}`];
      return handler ? handler(record) : fallback();
    }),
  );
  return seen;
}

function providers(client: QueryClient, ui: ReactNode) {
  return (
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">{ui}</LocaleProvider>
    </QueryClientProvider>
  );
}

// Returns a rerender that keeps the same providers, so a test can change the
// props a page would change on a refetch — the project list, say — without
// remounting the surface and losing its state.
function render(ui: ReactNode) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const mounted = rtlRender(providers(client, ui));
  return {
    ...mounted,
    rerender: (next: ReactNode) => mounted.rerender(providers(client, next)),
  };
}

beforeEach(() => {
  globalThis.localStorage.setItem("margince.workspaceSlug", "acme");
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  globalThis.localStorage.clear();
});

describe("Ask about this account, scoped to a project", () => {
  it("sends the chosen project and renders the server's scope line", async () => {
    const seen = stubFetch({
      "POST /organizations/o-1/ask": () =>
        jsonResponse({
          organization_id: "o-1",
          question: "whats_open",
          generated_at: "2026-08-13T09:00:00Z",
          generated_by: "deterministic",
          scope: erpScope,
          sentences: [],
        }),
    });
    render(<AskSection orgId="o-1" enabled projects={view.projects} />);
    const user = userEvent.setup();
    await user.click(screen.getByRole("combobox", { name: "Project" }));
    await user.click(screen.getByRole("option", { name: /ERP-27/ }));
    await user.click(screen.getByRole("button", { name: "What's open here?" }));

    await waitFor(() =>
      expect(seen.filter((s) => s.method === "POST")).toHaveLength(1),
    );
    expect(seen[0].body).toEqual({
      question: "whats_open",
      project_id: "p-erp",
    });
    expect(
      await screen.findByText("Scoped to ERP-27 · 4 of 11 activities"),
    ).toBeTruthy();
  });

  it("forgets the previous answer when the project changes", async () => {
    stubFetch({
      "POST /organizations/o-1/ask": (s) => {
        const body = s.body as { project_id?: string };
        return jsonResponse({
          organization_id: "o-1",
          question: "whats_open",
          generated_at: "2026-08-13T09:00:00Z",
          generated_by: "deterministic",
          ...(body.project_id === "p-erp" ? { scope: erpScope } : {}),
          sentences: [
            {
              text: "The ERP cutover is waiting on you.",
              evidence: [{ entity_type: "organization", entity_id: "o-1" }],
            },
          ],
        });
      },
    });
    render(<AskSection orgId="o-1" enabled projects={view.projects} />);
    const user = userEvent.setup();
    await user.click(screen.getByRole("combobox", { name: "Project" }));
    await user.click(screen.getByRole("option", { name: /ERP-27/ }));
    await user.click(screen.getByRole("button", { name: "What's open here?" }));
    expect(await screen.findByText(/ERP cutover is waiting/)).toBeTruthy();

    await user.click(screen.getByRole("combobox", { name: "Project" }));
    await user.click(screen.getByRole("option", { name: /DC-4/ }));
    // One engagement's text never stands under another's scope line.
    expect(screen.queryByText(/ERP cutover is waiting/)).toBeNull();
    expect(screen.queryByText(/4 of 11 activities/)).toBeNull();
    expect(screen.getByText("Scoped to DC-4")).toBeTruthy();
  });

  it("drops a chosen project the list no longer offers, and defaults to the one left", async () => {
    const seen = stubFetch({
      "POST /organizations/o-1/ask": () =>
        jsonResponse({
          organization_id: "o-1",
          question: "whats_open",
          generated_at: "2026-08-13T09:00:00Z",
          generated_by: "deterministic",
          sentences: [],
        }),
    });
    const { rerender } = render(
      <AskSection orgId="o-1" enabled projects={view.projects} />,
    );
    const user = userEvent.setup();
    await user.click(screen.getByRole("combobox", { name: "Project" }));
    await user.click(screen.getByRole("option", { name: /ERP-27/ }));
    expect(screen.getByText("Scoped to ERP-27")).toBeTruthy();

    // The ERP project closes; the page's refetched list no longer carries it.
    rerender(<AskSection orgId="o-1" enabled projects={[migration]} />);
    await waitFor(() =>
      expect(
        screen.getByRole("combobox", { name: "Project" }).textContent,
      ).toContain("DC-4"),
    );
    await user.click(screen.getByRole("button", { name: "What's open here?" }));
    await waitFor(() => expect(seen).toHaveLength(1));
    expect(seen[0].body).toEqual({
      question: "whats_open",
      project_id: "p-dc",
    });

    // No project left at all: the hidden id must not keep travelling.
    rerender(<AskSection orgId="o-1" enabled projects={[]} />);
    await user.click(screen.getByRole("button", { name: "What's open here?" }));
    await waitFor(() => expect(seen).toHaveLength(2));
    expect(seen[1].body).toEqual({ question: "whats_open" });
  });

  it("omits the project when none is chosen", async () => {
    const seen = stubFetch({
      "POST /organizations/o-1/ask": () =>
        jsonResponse({
          organization_id: "o-1",
          question: "whats_open",
          generated_at: "2026-08-13T09:00:00Z",
          generated_by: "deterministic",
          sentences: [],
        }),
    });
    render(<AskSection orgId="o-1" enabled projects={view.projects} />);
    await userEvent
      .setup()
      .click(screen.getByRole("button", { name: "What's open here?" }));

    await waitFor(() => expect(seen).toHaveLength(1));
    expect(seen[0].body).toEqual({ question: "whats_open" });
    expect(screen.queryByText(/Scoped to/)).toBeNull();
  });
});

describe("the account brief, scoped to a project", () => {
  it("reads and rewrites the brief under the chosen project", async () => {
    const seen = stubFetch({
      "GET /organizations/o-1/brief": (s) =>
        jsonResponse(
          s.url.includes("project_id=p-erp")
            ? { ...emptyBrief, scope: erpScope }
            : emptyBrief,
        ),
      "POST /organizations/o-1/brief": () =>
        jsonResponse({ ...emptyBrief, scope: erpScope }),
    });
    render(<AccountBrief orgId="o-1" view={view} enabled />);
    // The standing brief is the whole account's: no project until chosen.
    await waitFor(() => expect(seen).toHaveLength(1));
    expect(seen[0].url).toBe("/organizations/o-1/brief");

    const user = userEvent.setup();
    await user.click(screen.getByRole("combobox", { name: "Project" }));
    await user.click(screen.getByRole("option", { name: /ERP-27/ }));
    await waitFor(() => expect(seen).toHaveLength(2));
    expect(seen[1].url).toBe("/organizations/o-1/brief?project_id=p-erp");
    expect(
      await screen.findByText("Scoped to ERP-27 · 4 of 11 activities"),
    ).toBeTruthy();

    await user.click(screen.getByRole("button", { name: "Write it again" }));
    await waitFor(() => expect(seen).toHaveLength(3));
    expect(seen[2].method).toBe("POST");
    expect(seen[2].url).toBe("/organizations/o-1/brief?project_id=p-erp");
  });
});

describe("the meeting brief, scoped to a project", () => {
  const meetingBrief = (scope?: ProjectScope) => ({
    activity_id: "a-1",
    generated_at: "2026-08-13T09:00:00Z",
    generated_by: "deterministic",
    sections: [],
    ...(scope ? { scope } : {}),
  });

  it("offers the picker for an unattributed meeting and sends the choice", async () => {
    const seen = stubFetch({
      "GET /activities/a-1/meeting-brief": (s) =>
        jsonResponse(
          meetingBrief(
            s.url.includes("project_id=p-erp") ? erpScope : undefined,
          ),
        ),
    });
    render(
      <PersonMeetingBrief
        activityId="a-1"
        open
        onClose={() => {}}
        projects={view.projects}
      />,
    );
    await waitFor(() => expect(seen).toHaveLength(1));
    expect(seen[0].url).toBe("/activities/a-1/meeting-brief");

    const user = userEvent.setup();
    await user.click(await screen.findByRole("combobox", { name: "Project" }));
    await user.click(screen.getByRole("option", { name: /ERP-27/ }));
    await waitFor(() => expect(seen).toHaveLength(2));
    expect(seen[1].url).toBe("/activities/a-1/meeting-brief?project_id=p-erp");
    expect(
      await screen.findByText("Scoped to ERP-27 · 4 of 11 activities"),
    ).toBeTruthy();
  });

  it("shows the line alone for a meeting filed under a project", async () => {
    stubFetch({
      "GET /activities/a-1/meeting-brief": () =>
        jsonResponse(meetingBrief({ ...erpScope, in_scope: 3, total: 9 })),
    });
    render(
      <PersonMeetingBrief
        activityId="a-1"
        open
        onClose={() => {}}
        projects={view.projects}
      />,
    );
    expect(
      await screen.findByText("Scoped to ERP-27 · 3 of 9 activities"),
    ).toBeTruthy();
    // The meeting's own filing decides; there is nothing to pick.
    expect(screen.queryByRole("combobox", { name: "Project" })).toBeNull();
  });

  it("names the project alone when the server sent no counts", async () => {
    stubFetch({
      "GET /activities/a-1/meeting-brief": () =>
        jsonResponse(
          meetingBrief({ project_id: "p-erp", name: "ERP rollout", key: null }),
        ),
    });
    render(<PersonMeetingBrief activityId="a-1" open onClose={() => {}} />);
    expect(await screen.findByText("Scoped to ERP rollout")).toBeTruthy();
  });
});

describe("Prepare meeting on the company page", () => {
  it("opens the meeting brief drawer, not the composer", async () => {
    const withMeeting: Organization360 = {
      ...view,
      next_meeting: {
        activity_id: "a-1",
        starts_at: "2026-08-20T13:00:00Z",
        subject: "Renewal review",
        participants: [{ person_id: "p-1", display_name: "Dana Buyer" }],
      },
    };
    const seen = stubFetch({
      "GET /me": () =>
        jsonResponse(meFixture({ allow: { organization: ["read"] } })),
      "GET /organizations/o-1": () => jsonResponse(view.organization),
      "GET /organizations/o-1/360": () => jsonResponse(withMeeting),
      "GET /organizations/o-1/brief": () => jsonResponse(emptyBrief),
      "GET /organizations/o-1/finance-summary": () =>
        jsonResponse({ organization_id: "o-1", state: "no_connection" }),
      "GET /activities/a-1/meeting-brief": () =>
        jsonResponse({
          activity_id: "a-1",
          generated_at: "2026-08-13T09:00:00Z",
          generated_by: "deterministic",
          sections: [],
        }),
    });
    render(<CompanyScreen id="o-1" />);
    await userEvent
      .setup()
      .click(await screen.findByRole("button", { name: "Prepare meeting" }));

    expect(
      await screen.findByRole("heading", { name: "Meeting brief" }),
    ).toBeTruthy();
    await waitFor(() =>
      expect(seen.some((s) => s.url === "/activities/a-1/meeting-brief")).toBe(
        true,
      ),
    );
    expect(screen.queryByRole("heading", { name: /Reply/ })).toBeNull();
  });
});
