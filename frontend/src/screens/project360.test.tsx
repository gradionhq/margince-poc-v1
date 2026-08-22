// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

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
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider } from "../i18n";
import { jsonResponse } from "./company.fixtures";
import { ProjectScreen } from "./project360";
import { project, project360 } from "./projects.fixtures";
import { projectsBackend } from "./projects.testing";

// The project page: every section drawn from one Project360, a withheld
// section saying so rather than reading as empty, and the close dialog that
// holds its Confirm until a reason is given and then posts exactly
// `{to_phase: "closed", reason}` under the project's version.

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

describe("ProjectScreen", () => {
  it("renders the header, the stepper, the rollups, the coverage line and the sections", async () => {
    projectsBackend({
      view: project360({
        deals: {
          data: [
            {
              id: "d-1",
              name: "Phase one licence",
              pipeline_id: "pl",
              stage_id: "s1",
              status: "won",
              amount_minor: 450_000,
              currency: "EUR",
              source: "manual",
              captured_by: "u-me",
              created_at: "2026-06-02T09:00:00Z",
              updated_at: "2026-06-02T09:00:00Z",
            },
          ],
          page: { next_cursor: null, has_more: false },
        },
      }),
    });
    render(<ProjectScreen id="pr-1" />);
    expect(
      await screen.findByRole("heading", { name: "CRM rollout" }),
    ).toBeTruthy();
    expect(screen.getByText("ACME-CRM")).toBeTruthy();
    expect(await screen.findByText("Brandt Automotive")).toBeTruthy();

    // The stepper: the current phase is a marker, the other three are moves.
    const stepper = screen.getByRole("group", { name: "Phase" });
    expect(within(stepper).getByText("Initiative").tagName).toBe("SPAN");
    expect(screen.getByTestId("project-step-closed").tagName).toBe("BUTTON");

    // The readings plate and the filing line.
    const rollups = screen.getByTestId("project-rollups");
    expect(within(rollups).getByText("€12,000.00")).toBeTruthy();
    expect(within(rollups).getByText("€4,500.00")).toBeTruthy();
    expect(within(rollups).getByText("4")).toBeTruthy();
    expect(within(rollups).getByText("142")).toBeTruthy();
    expect(screen.getByTestId("project-coverage").textContent).toBe(
      "142 attributed · 8 awaiting a decision · 23 on this project's people and deals not attributed",
    );

    // The sections: a linked deal row, a seated person with their role, the
    // phase history with its duration, and honest empty states elsewhere.
    expect(
      screen.getByRole("button", { name: "Phase one licence" }),
    ).toBeTruthy();
    expect(screen.getByText("Anna Weber")).toBeTruthy();
    expect(screen.getByText("Project lead")).toBeTruthy();
    expect(screen.getByText("Started in Initiative")).toBeTruthy();
    expect(screen.getByText(/31d · current/)).toBeTruthy();
    expect(
      screen.getByText(/No agreement is filed under this project/),
    ).toBeTruthy();
    expect(
      screen.getByText(/No file is attached to this project/),
    ).toBeTruthy();
    expect(
      screen.getByText(/No open task is filed under this project/),
    ).toBeTruthy();
    expect(
      screen.getByText(/Nothing is filed under this project yet/),
    ).toBeTruthy();
  });

  it("offers Relink and Reply on a timeline row", async () => {
    const user = userEvent.setup();
    projectsBackend({
      view: project360({
        activities: {
          data: [
            {
              id: "act-1",
              kind: "email",
              subject: "Kickoff agenda",
              occurred_at: "2026-07-01T09:00:00Z",
              is_done: false,
              source: "manual",
              captured_by: "human:u-1",
              created_at: "2026-07-01T09:00:00Z",
              updated_at: "2026-07-01T09:00:00Z",
            },
          ],
          page: { next_cursor: null, has_more: false },
        },
      }),
    });
    render(<ProjectScreen id="pr-1" />);
    await screen.findByText("Kickoff agenda");

    expect(screen.getByRole("button", { name: "Reply" })).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "Relink" }));
    expect(await screen.findByRole("dialog")).toBeTruthy();
  });

  it("says a withheld section is hidden rather than drawing it empty", async () => {
    projectsBackend({
      view: project360({
        sections_omitted: [
          "contracts",
          "rollups",
          "coverage",
          "organization",
          "activities",
        ],
        contracts: undefined,
        rollups: undefined,
        coverage: undefined,
        organization: undefined,
        activities: undefined,
      }),
    });
    render(<ProjectScreen id="pr-1" />);
    await screen.findByRole("heading", { name: "CRM rollout" });
    const withheld = screen.getAllByText("Hidden — your role cannot read this");
    // The contracts card, the rollups plate, the coverage line, the company
    // in the subtitle and the timeline: five withheld sections, five
    // sentences, no empty state standing in for any of them.
    expect(withheld).toHaveLength(5);
    expect(
      screen.queryByText(/No agreement is filed under this project/),
    ).toBeNull();
    expect(screen.queryByTestId("project-coverage")).toBeNull();
    expect(screen.getByTestId("project-coverage-withheld")).toBeTruthy();
    expect(screen.getByTestId("project-company-withheld")).toBeTruthy();
    expect(screen.queryByText("Brandt Automotive")).toBeNull();
    expect(
      screen.queryByText(/Nothing is filed under this project yet/),
    ).toBeNull();
  });

  it("closing asks for a reason, holds Confirm until it is given, and posts the move", async () => {
    const user = userEvent.setup();
    let posted: { body: unknown; ifMatch: string | null } | null = null;
    projectsBackend({
      respond: async (url, method, request) => {
        if (method === "POST" && url.endsWith("/projects/pr-1/advance")) {
          posted = {
            body: JSON.parse(await request.text()),
            ifMatch: request.headers.get("If-Match"),
          };
          return jsonResponse(project({ phase: "closed" }));
        }
        return null;
      },
    });
    render(<ProjectScreen id="pr-1" />);
    await screen.findByRole("heading", { name: "CRM rollout" });

    await user.click(screen.getByTestId("project-step-closed"));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText("Move to Closed")).toBeTruthy();
    const confirm = within(dialog).getByRole("button", {
      name: "Close project",
    });
    expect(confirm.hasAttribute("disabled")).toBe(true);

    await user.type(
      within(dialog).getByLabelText("Reason *"),
      "Delivered and signed off",
    );
    expect(confirm.hasAttribute("disabled")).toBe(false);
    await user.click(confirm);

    await waitFor(() => expect(posted).toBeTruthy());
    expect(posted).toEqual({
      body: { to_phase: "closed", reason: "Delivered and signed off" },
      ifMatch: "3",
    });
  });

  it("a move that is not a close needs no reason", async () => {
    const user = userEvent.setup();
    let posted: unknown = null;
    projectsBackend({
      respond: async (url, method, request) => {
        if (method === "POST" && url.endsWith("/advance")) {
          posted = JSON.parse(await request.text());
          return jsonResponse(project({ phase: "pursuing" }));
        }
        return null;
      },
    });
    render(<ProjectScreen id="pr-1" />);
    await screen.findByRole("heading", { name: "CRM rollout" });
    await user.click(screen.getByTestId("project-step-pursuing"));
    const dialog = await screen.findByRole("dialog");
    await user.click(within(dialog).getByRole("button", { name: "Move" }));
    await waitFor(() => expect(posted).toBeTruthy());
    expect(posted).toEqual({ to_phase: "pursuing", reason: null });
  });
});
