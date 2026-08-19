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
import type { UserEvent } from "@testing-library/user-event";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider } from "../i18n";
import {
  companyBackstop,
  emptyPage,
  emptySection,
  jsonResponse,
  org,
  org360,
  stubFetch,
} from "./company.fixtures";
import { CompanyScreen } from "./organizations";

// The company record's Tasks tab: tick-to-complete without leaving the
// account, a withheld section that says so, an archived account that offers no
// write it cannot honour, and the detail modal's id surviving no longer than
// the tab that renders it.

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

// One open, dated task, as the 360 serves it on the Tasks tab.
const openTask = {
  activity_id: "a-1",
  subject: "Follow up on contract renewal",
  due_at: "2026-08-20T00:00:00Z",
  overdue: false,
  assignee_id: null,
  linked_deal_id: null,
  linked_person_id: null,
};

// The same task as the ACTIVITY read the detail modal fires when a row is
// expanded — the composite's summary shape carries no version or done flag, so
// the modal reads the record itself.
const openTaskActivity = {
  id: openTask.activity_id,
  organization_id: "o-1",
  type: "task",
  subject: openTask.subject,
  occurred_at: "2026-08-01T09:00:00Z",
  due_at: openTask.due_at,
  is_done: false,
  captured_by: "human:u1",
  source: "manual",
  version: 1,
};

const org360WithOpenTask = {
  ...org360,
  next_steps: { ...emptySection, data: [openTask] },
};

// The section the reader's role cannot read: absent from the payload and named
// in `sections_omitted`, which is a different fact from an empty one.
const org360WithheldTasks = {
  ...org360,
  next_steps: undefined,
  sections_omitted: ["next_steps"],
};

// Driven through the reader's own instance: a second implicit one forgets
// which keys and buttons the first left held.
async function openTasksTab(user: UserEvent) {
  await user.click(await screen.findByRole("button", { name: "Tasks" }));
}

describe("CompanyScreen — the Tasks tab", () => {
  it("completes a task without leaving the account", async () => {
    const user = userEvent.setup();
    let patched: unknown;
    stubFetch(
      async (url, method, request) => {
        if (method === "PATCH" && url.endsWith("/activities/a-1")) {
          patched = await request.json();
          return jsonResponse({});
        }
        return companyBackstop(url);
      },
      { org360: org360WithOpenTask },
    );
    render(<CompanyScreen id="o-1" />);
    await openTasksTab(user);

    await waitFor(() =>
      expect(screen.getByText(openTask.subject)).toBeTruthy(),
    );
    await user.click(screen.getByRole("checkbox", { name: "Done" }));

    await waitFor(() => expect(patched).toEqual({ is_done: true }));
  });

  it("says the section is withheld rather than rendering it as empty", async () => {
    const user = userEvent.setup();
    stubFetch(companyBackstop, { org360: org360WithheldTasks });
    render(<CompanyScreen id="o-1" />);
    await openTasksTab(user);

    await waitFor(() =>
      expect(
        screen.getByText("Hidden — your role cannot read this"),
      ).toBeTruthy(),
    );
    expect(screen.queryByText("No open task on this account.")).toBeNull();
  });

  it("shows no task-completing verb on an archived account", async () => {
    const user = userEvent.setup();
    // The server refuses a write on an archived account, so the tab omits the
    // verb rather than offering a button that can only 404.
    stubFetch(
      async (url) => {
        if (url.endsWith("/organizations/o-1")) {
          return jsonResponse({ ...org, archived_at: "2026-07-13T00:00:00Z" });
        }
        if (url.endsWith("/activities/a-1")) {
          return jsonResponse(openTaskActivity);
        }
        return emptyPage();
      },
      { org360: org360WithOpenTask },
    );
    render(<CompanyScreen id="o-1" />);
    await openTasksTab(user);

    await waitFor(() =>
      expect(screen.getByText(openTask.subject)).toBeTruthy(),
    );
    expect(screen.queryByRole("checkbox", { name: "Done" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Snooze 1d" })).toBeNull();

    // The same withheld verb holds inside the detail modal, not only the row.
    await user.click(screen.getByText(openTask.subject));
    await waitFor(() =>
      expect(
        screen.getByRole("dialog", { name: openTask.subject }),
      ).toBeTruthy(),
    );
    expect(screen.queryByRole("button", { name: "Done" })).toBeNull();
  });

  // The detail modal renders on this tab and nowhere else, so a tab change
  // takes it off screen without its own onClose ever running. An open id that
  // survived that would be waiting the next time the reader came back to
  // Tasks — a dialog reopening itself, having been closed by nobody.
  //
  // Driven through the tab pill on purpose. A reader cannot reach the pill
  // while the dialog covers it, and that is a fact about Modal's backdrop
  // rather than about this page: the id must not depend on it.
  it("opens no dialog when the reader returns to Tasks after changing tab", async () => {
    const user = userEvent.setup();
    stubFetch(
      async (url) => {
        if (url.endsWith("/activities/a-1")) {
          return jsonResponse(openTaskActivity);
        }
        return companyBackstop(url);
      },
      { org360: org360WithOpenTask },
    );
    render(<CompanyScreen id="o-1" />);
    await openTasksTab(user);
    await user.click(await screen.findByText(openTask.subject));
    await waitFor(() =>
      expect(
        screen.getByRole("dialog", { name: openTask.subject }),
      ).toBeTruthy(),
    );

    await user.click(screen.getByRole("button", { name: "Deals" }));
    await waitFor(() =>
      expect(
        screen.queryByRole("dialog", { name: openTask.subject }),
      ).toBeNull(),
    );
    await user.click(screen.getByRole("button", { name: "Tasks" }));

    await waitFor(() =>
      expect(screen.getByText(openTask.subject)).toBeTruthy(),
    );
    expect(screen.queryByRole("dialog", { name: openTask.subject })).toBeNull();
  });
});
