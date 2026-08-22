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

function tasksBackend(
  tasks: Activity[],
  mutations: Mutation[],
  // The records a row's links point at, keyed by the path that reads one
  // (`/leads/l-1`). A named row resolves its record through EntityRef, off that
  // record's own GET — unanswered, the row falls back to the raw id and an
  // assertion about the name would be asserting the fallback.
  records: Record<string, unknown> = {},
  // The caller's own scheduled messages, for the one case that asserts on the
  // link this page offers into them.
  scheduled: unknown[] = [],
) {
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
    const record = Object.entries(records).find(([path]) => url.includes(path));
    if (record) {
      return jsonResponse(record[1]);
    }
    // The page also carries the scheduled-send queue's front door, and that
    // endpoint answers with a bare ARRAY rather than a paginated envelope. It is
    // answered here — with an empty queue, so the link does not appear unless a
    // test asks for one — because the alternative is every case in this file
    // silently reading the task envelope as a list of scheduled messages.
    if (url.includes("/scheduled-sends")) {
      return jsonResponse(scheduled);
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
    // The formatter itself is pinned in format.test.ts; here the row must show
    // the stored instant in the READER's zone — the same zone the due date
    // beside it uses, because one row stating two zones is worse than either.
    expect(
      screen.getByText(
        formatDateTime(
          "2026-07-05T07:30:00Z",
          "en",
          Intl.DateTimeFormat().resolvedOptions().timeZone,
        ),
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

// Task subjects are generated ("Follow up with the new lead"), so a queue of
// them is a column of the same sentence and the only way to tell two apart is
// to open both. The row names WHICH record it is about — the first link the app
// can route to, which since the project page exists is every link kind but
// `activity`.
describe("TaskRow — when the task is due, in the reader's own zone", () => {
  const due = "2026-07-04T22:30:00Z";

  // A due date is a personal deadline: the row renders it in the VIEWER's zone,
  // not a zone the product picked. Pinned to Europe/Berlin it told a reader in
  // Ho Chi Minh City the 4th when their task is due on the 5th.
  //
  // The test controls the zone rather than reading the machine's, so it asserts
  // the rule instead of wherever CI happens to run.
  it("renders the due date in the zone the reader resolves to", async () => {
    const resolved = Intl.DateTimeFormat.prototype.resolvedOptions;
    vi.spyOn(
      Intl.DateTimeFormat.prototype,
      "resolvedOptions",
    ).mockImplementation(function mocked(this: Intl.DateTimeFormat) {
      return { ...resolved.call(this), timeZone: "Asia/Ho_Chi_Minh" };
    });
    vi.stubGlobal("fetch", tasksBackend([openTask({ due_at: due })], []));
    render(<TasksScreen />);

    await waitFor(() => expect(screen.getByText("Call Anna")).toBeTruthy());
    const cell = screen.getByText("Call Anna").parentElement;
    if (!cell) {
      throw new Error("the task subject rendered outside a row");
    }
    expect(cell.textContent).toContain(
      formatDateTime(due, "en", "Asia/Ho_Chi_Minh"),
    );
    expect(cell.textContent).not.toContain(
      formatDateTime(due, "en", "Europe/Berlin"),
    );
  });
});

// This page carries the scheduled-send queue's front door, because a message the
// rep told the product to send later is work of theirs that has not happened
// yet — and the door only appears when there is something behind it.
describe("TasksScreen — the scheduled-send queue's front door", () => {
  const waitingSend = {
    id: "019f7e65-fbf7-7114-b114-40af4af63ae8",
    status: "scheduled",
    scheduled_at: "2026-09-01T07:00:00Z",
    scheduled_tz: Intl.DateTimeFormat().resolvedOptions().timeZone,
    subject: "The renewal quote",
    to: ["ceo@acme.test"],
    version: 1,
    created_at: "2026-08-20T09:00:00Z",
    updated_at: "2026-08-20T09:00:00Z",
  };

  it("offers the queue, and counts what is in it", async () => {
    vi.stubGlobal("fetch", tasksBackend([], [], {}, [waitingSend]));
    render(<TasksScreen />);
    expect(
      await screen.findByText("One message is waiting to send."),
    ).toBeTruthy();
    const link = screen.getByRole("link", { name: "Scheduled messages" });
    expect(link.getAttribute("href")).toBe("#/scheduled");
  });

  it("counts only what is still waiting, not what has gone", async () => {
    vi.stubGlobal(
      "fetch",
      tasksBackend([], [], {}, [
        waitingSend,
        { ...waitingSend, id: "b", status: "held" },
        { ...waitingSend, id: "c", status: "sent" },
        { ...waitingSend, id: "d", status: "cancelled" },
      ]),
    );
    render(<TasksScreen />);
    expect(
      await screen.findByText("2 messages are waiting to send."),
    ).toBeTruthy();
  });

  // An always-present row reading "0 messages waiting to send" is a line every
  // rep learns to skip, and following it would land them on an empty page.
  it("offers nothing when nothing is waiting", async () => {
    vi.stubGlobal("fetch", tasksBackend([openTask({})], [], {}, []));
    render(<TasksScreen />);
    await waitFor(() => expect(screen.getByText("Call Anna")).toBeTruthy());
    expect(
      screen.queryByRole("link", { name: "Scheduled messages" }),
    ).toBeNull();
  });
});
