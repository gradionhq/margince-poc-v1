/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  fireEvent,
  render as rtlRender,
  screen,
  waitFor,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { components } from "../api/schema";
import { formatDateTime } from "../format/format";
import { LocaleProvider } from "../i18n";
import { TasksScreen } from "./tasks";

// B-E16.1 acceptance: the New-task modal posts kind=task with the picked
// due/remind instants, a stored remind_at renders on the row, and the inline
// bell control PATCHes remind_at — set and cleared. (The grouping and
// complete/snooze behaviour is pinned in inbox.test.tsx.)

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

const render = (ui: ReactNode) => {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return rtlRender(
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">{ui}</LocaleProvider>
    </QueryClientProvider>,
  );
};

type Activity = components["schemas"]["Activity"];

function openTask(overrides: Partial<Activity>): Activity {
  return {
    id: "t1",
    workspace_id: "w",
    kind: "task",
    subject: "Call Anna",
    occurred_at: "2026-07-01T00:00:00Z",
    is_done: false,
    source: "manual",
    captured_by: "human:u",
    created_at: "2026-07-01T00:00:00Z",
    updated_at: "2026-07-01T00:00:00Z",
    ...overrides,
  };
}

type Mutation = { method: string; url: string; body: unknown };

function tasksBackend(tasks: Activity[], mutations: Mutation[]) {
  return vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const request = input instanceof Request ? input : null;
    const url = String(request ? request.url : input);
    const method = request ? request.method : (init?.method ?? "GET");
    if (method === "POST" || method === "PATCH") {
      const body = request
        ? await request.json()
        : JSON.parse(String(init?.body));
      mutations.push({ method, url, body });
      return jsonResponse(
        openTask(method === "POST" ? { id: "t-new" } : {}),
        method === "POST" ? 201 : 200,
      );
    }
    return jsonResponse({ data: tasks, page: { next_cursor: null } });
  });
}

describe("TasksScreen reminders (B-E16.1)", () => {
  it("creating a task posts kind=task with the picked due/remind instants", async () => {
    const mutations: Mutation[] = [];
    vi.stubGlobal("fetch", tasksBackend([], mutations));
    render(<TasksScreen />);
    await userEvent.click(screen.getByText("New task"));
    await userEvent.type(
      screen.getByLabelText("Subject *"),
      "Prepare the quote",
    );
    // date / datetime-local inputs only accept programmatic value changes in
    // jsdom; the picked values are local wall time, so the expected wire
    // instants below run through the same local→UTC conversion the screen
    // performs (due = local end of the picked day).
    fireEvent.change(screen.getByLabelText("Due date"), {
      target: { value: "2026-07-10" },
    });
    fireEvent.change(screen.getByLabelText("Remind me at"), {
      target: { value: "2026-07-10T09:30" },
    });
    await userEvent.click(screen.getByRole("button", { name: "Create" }));
    await waitFor(() => expect(mutations).toHaveLength(1));
    expect(mutations[0].method).toBe("POST");
    expect(mutations[0].url).toContain("/activities");
    expect(mutations[0].body).toMatchObject({
      kind: "task",
      subject: "Prepare the quote",
      source: "manual",
      due_at: new Date("2026-07-10T23:59:59").toISOString(),
      remind_at: new Date("2026-07-10T09:30").toISOString(),
    });
  });

  it("a task created without dates posts explicit nulls, not empty strings", async () => {
    const mutations: Mutation[] = [];
    vi.stubGlobal("fetch", tasksBackend([], mutations));
    render(<TasksScreen />);
    await userEvent.click(screen.getByText("New task"));
    await userEvent.type(screen.getByLabelText("Subject *"), "Send the deck");
    await userEvent.click(screen.getByRole("button", { name: "Create" }));
    await waitFor(() => expect(mutations).toHaveLength(1));
    expect(mutations[0].body).toMatchObject({
      kind: "task",
      subject: "Send the deck",
      due_at: null,
      remind_at: null,
    });
  });

  it("renders a stored remind_at as the bell time on the row", async () => {
    vi.stubGlobal(
      "fetch",
      tasksBackend([openTask({ remind_at: "2026-07-05T07:30:00Z" })], []),
    );
    render(<TasksScreen />);
    await waitFor(() => expect(screen.getByText("Call Anna")).toBeTruthy());
    // The formatter itself is pinned in format.test.ts; here the row must
    // show the stored instant rendered for the en locale in Europe/Berlin.
    expect(
      screen.getByText(
        formatDateTime("2026-07-05T07:30:00Z", "en", "Europe/Berlin"),
      ),
    ).toBeTruthy();
    expect(screen.getByRole("button", { name: "Clear reminder" })).toBeTruthy();
  });

  it("setting a reminder PATCHes remind_at with the picked instant", async () => {
    const mutations: Mutation[] = [];
    vi.stubGlobal("fetch", tasksBackend([openTask({})], mutations));
    render(<TasksScreen />);
    await waitFor(() => expect(screen.getByText("Call Anna")).toBeTruthy());
    await userEvent.click(screen.getByRole("button", { name: "Remind me" }));
    fireEvent.change(screen.getByLabelText("Remind me at"), {
      target: { value: "2026-07-08T09:00" },
    });
    await userEvent.click(screen.getByRole("button", { name: "Set reminder" }));
    await waitFor(() => expect(mutations).toHaveLength(1));
    expect(mutations[0].method).toBe("PATCH");
    expect(mutations[0].url).toContain("/activities/t1");
    expect(mutations[0].body).toMatchObject({
      remind_at: new Date("2026-07-08T09:00").toISOString(),
    });
  });

  it("clearing a reminder PATCHes remind_at back to null", async () => {
    const mutations: Mutation[] = [];
    vi.stubGlobal(
      "fetch",
      tasksBackend(
        [openTask({ remind_at: "2026-07-05T07:30:00Z" })],
        mutations,
      ),
    );
    render(<TasksScreen />);
    await waitFor(() => expect(screen.getByText("Call Anna")).toBeTruthy());
    await userEvent.click(
      screen.getByRole("button", { name: "Clear reminder" }),
    );
    await waitFor(() => expect(mutations).toHaveLength(1));
    expect(mutations[0].method).toBe("PATCH");
    expect(mutations[0].url).toContain("/activities/t1");
    expect(mutations[0].body).toMatchObject({ remind_at: null });
  });
});
