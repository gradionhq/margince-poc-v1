// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

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
import { afterEach, describe, expect, it, vi } from "vitest";
import type { components } from "../api/schema";
import { meFixture } from "../app/mefixture";
import { pickOption } from "../design-system/select-testing";
import { LocaleProvider } from "../i18n";
import { jsonResponse } from "./company.fixtures";
import { DealScreen, DealsScreen } from "./deals";
import { project } from "./projects.fixtures";

// Where a deal meets its project: the picker on the create form posts the
// chosen project's id, the inline "new project" answer is born on the deal's
// company before the deal is, the deal page draws its project as a linked
// chip, and a won deal without one is offered the company's single open
// project once.

type Deal = components["schemas"]["Deal"];

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  window.location.hash = "";
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

const stages = [
  {
    id: "s1",
    pipeline_id: "pl",
    name: "Qualify",
    position: 1,
    semantic: "open" as const,
    win_probability: 20,
  },
  {
    id: "s3",
    pipeline_id: "pl",
    name: "Won",
    position: 3,
    semantic: "won" as const,
    win_probability: 100,
  },
];

function deal(overrides: Partial<Deal> = {}): Deal {
  return {
    id: "d1",
    name: "Fleet retrofit",
    pipeline_id: "pl",
    stage_id: "s1",
    status: "open",
    organization_id: "o-1",
    source: "manual",
    captured_by: "u-me",
    version: 7,
    created_at: "2026-06-01T09:00:00Z",
    updated_at: "2026-06-01T09:00:00Z",
    ...overrides,
  };
}

type Call = {
  method: string;
  url: string;
  body: unknown;
  ifMatch: string | null;
};

/** The deal surfaces' reads, plus a record of every write. */
function dealBackend(opts: {
  deals?: Deal[];
  projects?: ReturnType<typeof project>[];
  // What an advance answers; null for the default success.
  onAdvance?: () => Response | null;
  companies?: { id: string; display_name: string }[];
  // What the deal reads back as once a PATCH has landed.
  afterPatch?: (row: Deal) => Deal;
}): Call[] {
  const writes: Call[] = [];
  let current: Deal = opts.deals?.[0] ?? deal();
  vi.stubGlobal(
    "fetch",
    vi.fn(async (request: Request) => {
      const url = request.url;
      const pathname = new URL(url).pathname;
      const method = request.method;
      if (method !== "GET") {
        // A report is a read that travels as a POST; it is not a write and
        // is not recorded as one.
        if (pathname.endsWith("/reports/deals-by-stage")) {
          return jsonResponse({
            report: "deals-by-stage",
            plan: {},
            columns: [],
            rows: [],
          });
        }
        const text = await request.text();
        writes.push({
          method,
          url,
          body: text ? JSON.parse(text) : null,
          ifMatch: request.headers.get("If-Match"),
        });
        if (method === "POST" && pathname.endsWith("/projects")) {
          return jsonResponse(
            project({ id: "pr-born", name: "Born here" }),
            201,
          );
        }
        if (method === "POST" && pathname.endsWith("/deals")) {
          return jsonResponse(deal({ id: "d-new" }), 201);
        }
        if (method === "POST" && pathname.endsWith("/advance")) {
          const answer = opts.onAdvance?.();
          if (answer) {
            return answer;
          }
        }
        if (method === "PATCH" && opts.afterPatch) {
          current = opts.afterPatch(current);
        }
        return jsonResponse(current);
      }
      if (pathname.endsWith("/me")) {
        return jsonResponse(meFixture());
      }
      if (pathname.endsWith("/pipelines")) {
        return jsonResponse({
          data: [
            { id: "pl", name: "Sales", is_default: true, position: 0, stages },
          ],
          page: { next_cursor: null },
        });
      }
      if (pathname.endsWith("/organizations")) {
        return jsonResponse({
          data: opts.companies ?? [
            { id: "o-1", display_name: "Brandt Automotive" },
          ],
          page: { next_cursor: null },
        });
      }
      if (pathname.endsWith("/organizations/o-1")) {
        return jsonResponse({ id: "o-1", display_name: "Brandt Automotive" });
      }
      if (pathname.endsWith("/projects")) {
        return jsonResponse({
          data: opts.projects ?? [],
          page: { next_cursor: null, has_more: false },
        });
      }
      if (/\/projects\/[^/]+$/.test(pathname)) {
        const id = pathname.split("/").pop();
        const found = (opts.projects ?? []).find((row) => row.id === id);
        return found ? jsonResponse(found) : jsonResponse({}, 404);
      }
      if (/\/deals\/[^/]+$/.test(pathname)) {
        return jsonResponse(current);
      }
      if (pathname.endsWith("/deals")) {
        return jsonResponse({
          data: opts.deals ?? [],
          page: { next_cursor: null, has_more: false },
        });
      }
      return jsonResponse({ data: [], page: { next_cursor: null } });
    }),
  );
  return writes;
}

describe("the deal form's project picker", () => {
  it("posts the picked project's id with the deal", async () => {
    const user = userEvent.setup();
    const writes = dealBackend({
      projects: [
        project({ id: "pr-1", name: "CRM rollout" }),
        project({ id: "pr-closed", name: "Old one", phase: "closed" }),
        project({ id: "pr-other", name: "Elsewhere", organization_id: "o-2" }),
      ],
    });
    render(<DealsScreen />);
    await user.click(await screen.findByTestId("new-record"));
    await user.type(screen.getByLabelText("Deal name *"), "Phase two");
    await pickOption(
      user,
      screen.getByLabelText("Company"),
      "Brandt Automotive",
    );
    // Only the chosen company's open projects are offered.
    await user.click(screen.getByLabelText("Project"));
    expect(screen.queryByRole("option", { name: /Old one/ })).toBeNull();
    expect(screen.queryByRole("option", { name: "Elsewhere" })).toBeNull();
    await user.click(screen.getByRole("option", { name: "CRM rollout" }));
    await user.click(screen.getByRole("button", { name: "Create" }));

    await waitFor(() =>
      expect(writes.some((write) => write.url.endsWith("/deals"))).toBe(true),
    );
    const posted = writes.find((write) => write.url.endsWith("/deals"));
    expect(posted?.body).toMatchObject({
      name: "Phase two",
      organization_id: "o-1",
      project_id: "pr-1",
    });
    expect(writes.some((write) => write.url.endsWith("/projects"))).toBe(false);
  });

  it("starts a new project on the deal's company before the deal is born", async () => {
    const user = userEvent.setup();
    const writes = dealBackend({ projects: [] });
    render(<DealsScreen />);
    await user.click(await screen.findByTestId("new-record"));
    await user.type(screen.getByLabelText("Deal name *"), "Phase two");
    await pickOption(
      user,
      screen.getByLabelText("Company"),
      "Brandt Automotive",
    );
    await pickOption(user, screen.getByLabelText("Project"), "New project…");
    await user.type(screen.getByLabelText("Project name *"), "Born here");
    await user.type(screen.getByLabelText("Key"), "BORN");
    await user.click(screen.getByRole("button", { name: "Create" }));

    await waitFor(() => expect(writes).toHaveLength(2));
    expect(writes[0]).toMatchObject({
      method: "POST",
      body: {
        name: "Born here",
        key: "BORN",
        organization_id: "o-1",
        source: "manual",
      },
    });
    expect(writes[0].url.endsWith("/projects")).toBe(true);
    expect(writes[1].url.endsWith("/deals")).toBe(true);
    expect(writes[1].body).toMatchObject({ project_id: "pr-born" });
  });

  it("holds the picker until a company is chosen, and drops a pick the new company does not own", async () => {
    const user = userEvent.setup();
    dealBackend({
      projects: [
        project({ id: "pr-1", name: "CRM rollout" }),
        project({ id: "pr-other", name: "Elsewhere", organization_id: "o-2" }),
      ],
      companies: [
        { id: "o-1", display_name: "Brandt Automotive" },
        { id: "o-2", display_name: "Other GmbH" },
      ],
    });
    render(<DealsScreen />);
    await user.click(await screen.findByTestId("new-record"));
    const picker = screen.getByLabelText("Project");
    expect(
      picker.hasAttribute("disabled") ||
        picker.getAttribute("aria-disabled") === "true",
    ).toBe(true);

    await pickOption(
      user,
      screen.getByLabelText("Company"),
      "Brandt Automotive",
    );
    await pickOption(user, screen.getByLabelText("Project"), "CRM rollout");
    expect(screen.getByLabelText("Project").textContent).toContain(
      "CRM rollout",
    );

    await pickOption(user, screen.getByLabelText("Company"), "Other GmbH");
    expect(screen.getByLabelText("Project").textContent).not.toContain(
      "CRM rollout",
    );
    await user.click(screen.getByLabelText("Project"));
    expect(screen.getByRole("option", { name: "Elsewhere" })).toBeTruthy();
    expect(screen.queryByRole("option", { name: "CRM rollout" })).toBeNull();
  });
});

describe("the deal page", () => {
  it("draws the deal's project as a chip linking to the project page", async () => {
    dealBackend({
      deals: [deal({ project_id: "pr-1" })],
      projects: [project({ id: "pr-1", name: "CRM rollout" })],
    });
    render(<DealScreen id="d1" />);
    const chip = await screen.findByTestId("deal-project");
    await waitFor(() => expect(chip.textContent).toBe("CRM rollout"));
    expect(chip.getAttribute("href")).toBe("#/projects/pr-1");
  });

  it("offers a won deal with no project the company's one open project, and attaches it", async () => {
    const user = userEvent.setup();
    const writes = dealBackend({
      deals: [deal({ status: "won", stage_id: "s3", project_id: null })],
      projects: [project({ id: "pr-1", name: "CRM rollout", version: 3 })],
    });
    render(<DealScreen id="d1" />);
    const start = await screen.findByTestId("deal-start-delivery");
    expect(
      screen.getByText(/Attach it to CRM rollout and move the project/),
    ).toBeTruthy();
    await user.click(start);

    await waitFor(() => expect(writes).toHaveLength(2));
    expect(writes[0]).toMatchObject({
      method: "PATCH",
      body: { project_id: "pr-1" },
      ifMatch: "7",
    });
    expect(writes[0].url.endsWith("/deals/d1")).toBe(true);
    expect(writes[1]).toMatchObject({
      method: "POST",
      body: { to_phase: "delivering", reason: null },
      ifMatch: "3",
    });
    expect(writes[1].url.endsWith("/projects/pr-1/advance")).toBe(true);
    await waitFor(() => expect(window.location.hash).toBe("#/projects/pr-1"));
  });

  it("makes no offer when the project is withheld or the deal is archived", async () => {
    dealBackend({
      deals: [
        deal({
          status: "won",
          stage_id: "s3",
          project_id: null,
          masked_fields: ["project_id"],
        }),
      ],
      projects: [project({ id: "pr-1", name: "CRM rollout" })],
    });
    render(<DealScreen id="d1" />);
    await screen.findByRole("heading", { name: "Fleet retrofit" });
    expect(screen.queryByTestId("deal-start-delivery")).toBeNull();
    cleanup();

    dealBackend({
      deals: [
        deal({
          status: "won",
          stage_id: "s3",
          project_id: null,
          archived_at: "2026-07-01T09:00:00Z",
        }),
      ],
      projects: [project({ id: "pr-1", name: "CRM rollout" })],
    });
    render(<DealScreen id="d1" />);
    await screen.findByRole("heading", { name: "Fleet retrofit" });
    expect(screen.queryByTestId("deal-start-delivery")).toBeNull();
  });

  it("after a failed advance, re-reads both records and offers only the advance", async () => {
    const user = userEvent.setup();
    let advances = 0;
    const writes = dealBackend({
      deals: [deal({ status: "won", stage_id: "s3", project_id: null })],
      projects: [project({ id: "pr-1", name: "CRM rollout", version: 3 })],
      onAdvance: () => {
        advances += 1;
        return advances === 1
          ? jsonResponse({ title: "version skew", code: "version_skew" }, 409)
          : null;
      },
      afterPatch: (row) => ({ ...row, project_id: "pr-1", version: 8 }),
    });
    render(<DealScreen id="d1" />);
    await user.click(await screen.findByTestId("deal-start-delivery"));
    // The PATCH landed, the advance did not: the deal re-reads as attached,
    // and the offer now says only the project is still owed.
    expect(
      await screen.findByText(/attached to CRM rollout, but the project/),
    ).toBeTruthy();
    await user.click(screen.getByTestId("deal-start-delivery"));
    await waitFor(() => expect(advances).toBe(2));
    // One PATCH in total: the retry never attaches a second time.
    expect(writes.filter((write) => write.method === "PATCH")).toHaveLength(1);
  });

  it("makes no offer when the company has two open projects", async () => {
    dealBackend({
      deals: [deal({ status: "won", stage_id: "s3", project_id: null })],
      projects: [
        project({ id: "pr-1", name: "One" }),
        project({ id: "pr-2", name: "Two" }),
      ],
    });
    render(<DealScreen id="d1" />);
    await screen.findByRole("heading", { name: "Fleet retrofit" });
    expect(screen.queryByTestId("deal-start-delivery")).toBeNull();
  });
});
