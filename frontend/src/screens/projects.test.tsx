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
import { pickOption } from "../design-system/select-testing";
import { LocaleProvider } from "../i18n";
import { jsonResponse } from "./company.fixtures";
import { ProjectsScreen } from "./projects";
import { project } from "./projects.fixtures";
import { projectsBackend } from "./projects.testing";

// The projects list: rows from GET /projects, the phase dial sent to the
// server, the instructional first-run plate, and the create dialog's own
// refusal of a key the contract would refuse.

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

describe("ProjectsScreen", () => {
  it("renders a row per project with its key, company, phase and owner", async () => {
    projectsBackend({
      projects: [
        project({ id: "pr-1", name: "CRM rollout", key: "ACME-CRM" }),
        project({
          id: "pr-2",
          name: "Fleet retrofit",
          key: null,
          phase: "delivering",
          owner_id: null,
        }),
      ],
    });
    render(<ProjectsScreen />);
    expect(await screen.findByText("CRM rollout")).toBeTruthy();
    expect(screen.getByText("ACME-CRM")).toBeTruthy();
    expect(screen.getByText("Fleet retrofit")).toBeTruthy();
    expect(screen.getByText("Initiative")).toBeTruthy();
    expect(screen.getByText("Delivering")).toBeTruthy();
    expect(await screen.findAllByText("Brandt Automotive")).toHaveLength(2);
    expect(screen.getByText("Unassigned")).toBeTruthy();
    // The row is a link to the project's own page.
    expect(
      screen.getByRole("link", { name: /CRM rollout/ }).getAttribute("href"),
    ).toBe("#/projects/pr-1");
  });

  it("narrows to one phase through the server", async () => {
    const user = userEvent.setup();
    const { urls } = projectsBackend({
      projects: [project({ id: "pr-1", name: "CRM rollout" })],
    });
    render(<ProjectsScreen />);
    await screen.findByText("CRM rollout");

    await user.click(screen.getByRole("button", { name: "Filter" }));
    // The menu names its step: the attribute list first, then that
    // attribute's values under its own heading.
    await user.click(
      within(screen.getByRole("group", { name: "Filter" })).getByRole(
        "button",
        { name: "Phase" },
      ),
    );
    await user.click(
      within(screen.getByRole("group", { name: "Phase" })).getByRole("button", {
        name: "Delivering",
      }),
    );

    await waitFor(() =>
      expect(urls.some((url) => url.includes("phase=delivering"))).toBe(true),
    );
    // The list opens sorted by the timeline's clock, newest activity first.
    const firstRead = urls.find((url) => url.includes("/projects?"));
    expect(firstRead).toContain("sort=-last_activity_at");
  });

  it("shows the instructional plate on a first run", async () => {
    projectsBackend({ projects: [] });
    render(<ProjectsScreen />);
    expect(await screen.findByText("No projects yet")).toBeTruthy();
    expect(
      screen.getByText(/starts during the deal, in the initiative phase/),
    ).toBeTruthy();
    expect(screen.getByText(/\[KEY\]/)).toBeTruthy();
    expect(screen.getByTestId("new-record")).toBeTruthy();
    expect(screen.queryByRole("table")).toBeNull();
  });

  it("refuses a key the contract would refuse before anything is posted, then creates", async () => {
    const user = userEvent.setup();
    let posted: unknown = null;
    projectsBackend({
      projects: [],
      respond: async (url, method, request) => {
        if (method === "POST" && url.endsWith("/projects")) {
          posted = JSON.parse(await request.text());
          return jsonResponse(project({ id: "pr-new" }), 201);
        }
        return null;
      },
    });
    render(<ProjectsScreen />);
    await user.click(await screen.findByTestId("new-record"));
    await user.type(screen.getByLabelText("Project name *"), "CRM rollout");
    await user.type(screen.getByLabelText("Key"), "2026");
    expect(
      screen.getByText(
        "A key starts with a letter and is 2–24 letters, digits, _ or -.",
      ),
    ).toBeTruthy();
    const save = screen.getByRole("button", { name: "Create" });
    expect(save.hasAttribute("disabled")).toBe(true);
    // The hint says what the key is for, beside the refusal.
    expect(screen.getByText(/\[KEY\] in an email subject/)).toBeTruthy();

    await user.clear(screen.getByLabelText("Key"));
    await user.type(screen.getByLabelText("Key"), "ACME-CRM");
    await pickOption(
      user,
      screen.getByLabelText("Company *"),
      "Brandt Automotive",
    );
    await user.click(screen.getByRole("button", { name: "Create" }));

    await waitFor(() => expect(posted).toBeTruthy());
    expect(posted).toEqual({
      name: "CRM rollout",
      key: "ACME-CRM",
      organization_id: "o-1",
      owner_id: null,
      description: null,
      target_end_date: null,
      source: "manual",
    });
    expect(window.location.hash).toBe("#/projects/pr-new");
  });
});
