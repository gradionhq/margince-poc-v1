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
import { pickOption } from "../design-system/select-testing";
import { calendarDay } from "../format/calendarday";
import { LocaleProvider } from "../i18n";
import { LogActivity } from "./logactivity";
import { PersonScreen } from "./people";
import { groupTask } from "./tasks";

// Logging from a 360 (the "you can actually add to the timeline" acceptance):
// the POST body carries the contract's shape (kind, subject, the viewed
// record as the link, source stamped manual), a success refetches the
// screen's activities query, a 422 renders its RFC 7807 detail verbatim, and
// the due-date input exists only for a task.

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

const emptyPage = { data: [], page: { next_cursor: null } };

// The dormant/no-interactions strength response — the default backstop for
// any test below that doesn't itself register a "GET .../strength" route:
// the Person Overview now fires this GET unconditionally (P-4).
const dormantStrength = {
  score: 0,
  bucket: "dormant",
  factors: { recency: 0, frequency: 0, reciprocity: 0, direction: 0 },
  last_interaction: null,
};

type Captured = { key: string; body: unknown };

// One day, picked in the form and asserted against — and the zone the machine
// running this suite is in, because the invariant is "the day the writer picked
// is the day the reader reads", which has to hold on every laptop rather than
// only on one that runs UTC.
const PICKED_DAY = "2026-07-10";
const READER_ZONE = Intl.DateTimeFormat().resolvedOptions().timeZone;

// The task the form has just logged, as the tasks list receives it — everything
// but the due date, which is the field under test.
const LOGGED_TASK = {
  id: "a-new",
  kind: "task" as const,
  subject: "Send proposal",
  occurred_at: "2026-07-06T09:00:00Z",
  is_done: false,
  source: "manual",
  captured_by: "human:u1",
  created_at: "2026-07-06T09:00:00Z",
  updated_at: "2026-07-06T09:00:00Z",
};

// The due_at off a captured request body. A narrowing read rather than a cast:
// the body is genuinely unknown here, and a request that carried no due date at
// all would otherwise pass the assertions below as `undefined`.
function postedDueAt(body: unknown): string {
  if (typeof body !== "object" || body === null || !("due_at" in body)) {
    throw new Error("expected the logged activity to carry a due_at");
  }
  const dueAt = body.due_at;
  if (typeof dueAt !== "string") {
    throw new Error(`expected due_at to be an instant, got ${typeof dueAt}`);
  }
  return dueAt;
}

function stubApi(
  routes: Record<string, (body: unknown) => Response>,
  captured?: Captured[],
) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const request = input instanceof Request ? input : null;
      const url = new URL(
        request ? request.url : String(input),
        "https://test.local",
      );
      const method = request?.method ?? init?.method ?? "GET";
      const key = `${method} ${url.pathname.replace(/^\/v1/, "")}`;
      let body: unknown = null;
      if (method !== "GET") {
        try {
          body = request
            ? await request.json()
            : JSON.parse(String(init?.body));
        } catch {
          body = null;
        }
      }
      captured?.push({ key, body });
      const handler = routes[key];
      if (handler) {
        return handler(body);
      }
      if (url.pathname.endsWith("/strength")) {
        return jsonResponse(dormantStrength);
      }
      if (url.pathname.endsWith("/context")) {
        return jsonResponse({
          anchor: { type: "person", id: "p1" },
          sections: [],
        });
      }
      return jsonResponse(emptyPage);
    }),
  );
}

const person = {
  id: "p1",
  full_name: "Petra Muster",
  captured_by: "human:u1",
  source: "manual",
  version: 1,
  created_at: "2026-07-06T08:00:00Z",
  updated_at: "2026-07-06T08:00:00Z",
};

const createdActivity = (body: unknown) =>
  jsonResponse(
    {
      id: "a-new",
      kind: (body as { kind: string }).kind,
      subject: (body as { subject: string }).subject,
      occurred_at: "2026-07-06T09:00:00Z",
      captured_by: "human:u1",
      source: "manual",
      version: 1,
      created_at: "2026-07-06T09:00:00Z",
      updated_at: "2026-07-06T09:00:00Z",
    },
    201,
  );

describe("log activity from a 360", () => {
  it("posts a note linked to the viewed person and refetches the timeline", async () => {
    const captured: Captured[] = [];
    stubApi(
      {
        "GET /people/p1": () => jsonResponse(person),
        "POST /activities": createdActivity,
      },
      captured,
    );
    render(<PersonScreen id="p1" />);
    await userEvent.type(
      await screen.findByLabelText("Subject *"),
      "Call recap",
    );
    await userEvent.type(screen.getByLabelText("Details"), "Agreed next step");
    await userEvent.click(screen.getByRole("button", { name: "Log" }));

    await waitFor(() =>
      expect(captured.some((entry) => entry.key === "POST /activities")).toBe(
        true,
      ),
    );
    const post = captured.find((entry) => entry.key === "POST /activities");
    expect(post?.body).toMatchObject({
      kind: "note",
      subject: "Call recap",
      body: "Agreed next step",
      links: [{ entity_type: "person", entity_id: "p1" }],
      source: "manual",
    });
    if (!post) throw new Error("expected a POST /activities to be captured");
    const { occurred_at } = post.body as { occurred_at: string };
    expect(occurred_at).toMatch(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}/);
    // the invalidation refetches the timeline the screen already loaded once
    await waitFor(() =>
      expect(
        captured.filter((entry) => entry.key === "GET /activities").length,
      ).toBeGreaterThanOrEqual(2),
    );
    // a successful log clears the draft for the next entry
    expect((screen.getByLabelText("Subject *") as HTMLInputElement).value).toBe(
      "",
    );
  });

  it("renders the server's 422 detail verbatim", async () => {
    stubApi({
      "POST /activities": () =>
        jsonResponse(
          { title: "Unprocessable", detail: "subject must not be blank" },
          422,
        ),
    });
    render(<LogActivity entityType="deal" entityId="d1" />);
    await userEvent.type(screen.getByLabelText("Subject *"), "x");
    await userEvent.click(screen.getByRole("button", { name: "Log" }));
    await waitFor(() =>
      expect(screen.getByText("subject must not be blank")).toBeTruthy(),
    );
  });

  it("keeps the due-date input's space for a note and enables it for a task", async () => {
    stubApi({ "POST /activities": createdActivity });
    render(<LogActivity entityType="organization" entityId="o1" />);
    // The field stays in place for a note, disabled rather than hidden.
    // Mounting it on the kind switch moved every control below it down
    // mid-form.
    const due = screen.getByLabelText("Due date", { selector: "input" });
    expect(due.hasAttribute("disabled")).toBe(true);
    // Reserved means VISIBLE-but-disabled. `hidden` is display:none, which
    // collapses the row and reintroduces the reflow this test exists to stop.
    expect(due.closest("div")?.hasAttribute("hidden")).toBe(false);
    await pickOption(userEvent.setup(), screen.getByLabelText("Type"), "Task");
    const enabled = screen.getByLabelText<HTMLInputElement>("Due date", {
      selector: "input",
    });
    expect(enabled.hasAttribute("disabled")).toBe(false);
    // Becoming a task fills the empty field with the writer's own today —
    // the date that would otherwise be assumed invisibly at submit.
    expect(enabled.value).toBe(
      calendarDay(new Date(), Intl.DateTimeFormat().resolvedOptions().timeZone),
    );
  });

  it("posts a task's due_at as the END of the picked day in the writer's own zone", async () => {
    const captured: Captured[] = [];
    stubApi({ "POST /activities": createdActivity }, captured);
    render(<LogActivity entityType="organization" entityId="o1" />);
    await pickOption(userEvent.setup(), screen.getByLabelText("Type"), "Task");
    fireEvent.change(screen.getByLabelText("Due date"), {
      target: { value: PICKED_DAY },
    });
    await userEvent.type(screen.getByLabelText("Subject *"), "Send proposal");
    await userEvent.click(screen.getByRole("button", { name: "Log" }));
    await waitFor(() =>
      expect(captured.some((entry) => entry.key === "POST /activities")).toBe(
        true,
      ),
    );
    const post = captured.find((entry) => entry.key === "POST /activities");
    expect(post?.body).toMatchObject({
      kind: "task",
      subject: "Send proposal",
      links: [{ entity_type: "organization", entity_id: "o1" }],
      source: "manual",
    });
    if (!post) throw new Error("expected a POST /activities to be captured");
    const dueAt = new Date(postedDueAt(post.body));
    // The instant has to fall on the day the writer picked, read where the
    // writer is — the bare `yyyy-mm-dd` handed to `new Date` is UTC midnight,
    // which is the previous calendar day for every writer west of UTC.
    expect(calendarDay(dueAt, READER_ZONE)).toBe(PICKED_DAY);
    // And at that day's END, because a task picked for today is due all day.
    expect(dueAt.getHours()).toBe(23);
    expect(dueAt.getMinutes()).toBe(59);
  });

  it("posts a due date the tasks list then buckets as today, not as overdue", async () => {
    const captured: Captured[] = [];
    stubApi({ "POST /activities": createdActivity }, captured);
    render(<LogActivity entityType="organization" entityId="o1" />);
    await pickOption(userEvent.setup(), screen.getByLabelText("Type"), "Task");
    fireEvent.change(screen.getByLabelText("Due date"), {
      target: { value: PICKED_DAY },
    });
    await userEvent.type(screen.getByLabelText("Subject *"), "Send proposal");
    await userEvent.click(screen.getByRole("button", { name: "Log" }));
    await waitFor(() =>
      expect(captured.some((entry) => entry.key === "POST /activities")).toBe(
        true,
      ),
    );
    const post = captured.find((entry) => entry.key === "POST /activities");
    if (!post) throw new Error("expected a POST /activities to be captured");
    // The two halves of the same contract, checked against each other: what
    // this form mints is what the tasks screen groups. A writer filing a task
    // for today at midday must find it under Today — with the day sent as UTC
    // midnight it read as already overdue, which is the state the screen puts
    // in red at the top of the list.
    const middayOnThePickedDay = new Date(`${PICKED_DAY}T12:00:00`);
    expect(
      groupTask(
        { ...LOGGED_TASK, due_at: postedDueAt(post.body) },
        middayOnThePickedDay,
        READER_ZONE,
      ),
    ).toBe("today");
  });

  it("keeps ordinary meeting notes as notes: unchecked, the field stays Details and no source_system is sent", async () => {
    const captured: Captured[] = [];
    stubApi({ "POST /activities": createdActivity }, captured);
    render(<LogActivity entityType="deal" entityId="d1" />);
    await pickOption(
      userEvent.setup(),
      screen.getByLabelText("Type"),
      "Meeting",
    );
    // The checkbox is unchecked by default: this is still a note field, not
    // a transcript field, even though the kind is "meeting" — typing here
    // must never silently carry source_system: transcript.
    expect(screen.queryByLabelText("Transcript")).toBeNull();
    await userEvent.type(screen.getByLabelText("Subject *"), "Quick sync");
    await userEvent.type(
      screen.getByLabelText("Details"),
      "discussed pricing, follow up Tuesday",
    );
    await userEvent.click(screen.getByRole("button", { name: "Log" }));
    await waitFor(() =>
      expect(captured.some((entry) => entry.key === "POST /activities")).toBe(
        true,
      ),
    );
    const post = captured.find((entry) => entry.key === "POST /activities");
    expect(post?.body).not.toHaveProperty("source_system");
    expect(post?.body).toMatchObject({
      kind: "meeting",
      body: "discussed pricing, follow up Tuesday",
    });
  });

  it("posts source_system: transcript only once the writer checks 'this text is a transcript'", async () => {
    const captured: Captured[] = [];
    stubApi({ "POST /activities": createdActivity }, captured);
    render(<LogActivity entityType="deal" entityId="d1" />);
    await pickOption(
      userEvent.setup(),
      screen.getByLabelText("Type"),
      "Meeting",
    );
    await userEvent.click(screen.getByLabelText("This text is a transcript"));
    // The label swaps once checked — pasting is a transcript, not "details".
    expect(screen.queryByLabelText("Details")).toBeNull();
    await userEvent.type(screen.getByLabelText("Subject *"), "Kickoff call");
    await userEvent.type(
      screen.getByLabelText("Transcript"),
      "Anna: hello\nBen: hi",
    );
    await userEvent.click(screen.getByRole("button", { name: "Log" }));
    await waitFor(() =>
      expect(captured.some((entry) => entry.key === "POST /activities")).toBe(
        true,
      ),
    );
    const post = captured.find((entry) => entry.key === "POST /activities");
    expect(post?.body).toMatchObject({
      kind: "meeting",
      subject: "Kickoff call",
      body: "Anna: hello\nBen: hi",
      source_system: "transcript",
      links: [{ entity_type: "deal", entity_id: "d1" }],
    });
  });

  it("sends a checked transcript's text raw, not trimmed, so an agent posting the identical paste normalizes it the same way", async () => {
    const captured: Captured[] = [];
    stubApi({ "POST /activities": createdActivity }, captured);
    render(<LogActivity entityType="deal" entityId="d1" />);
    await pickOption(
      userEvent.setup(),
      screen.getByLabelText("Type"),
      "Meeting",
    );
    await userEvent.click(screen.getByLabelText("This text is a transcript"));
    await userEvent.type(screen.getByLabelText("Subject *"), "Kickoff call");
    // fireEvent rather than userEvent.type: a leading space/newline is
    // exactly the thing under test, and userEvent.type mangles literal
    // leading whitespace in a textarea.
    fireEvent.change(screen.getByLabelText("Transcript"), {
      target: { value: "  Anna: hello" },
    });
    await userEvent.click(screen.getByRole("button", { name: "Log" }));
    await waitFor(() =>
      expect(captured.some((entry) => entry.key === "POST /activities")).toBe(
        true,
      ),
    );
    const post = captured.find((entry) => entry.key === "POST /activities");
    expect(post?.body).toMatchObject({ body: "  Anna: hello" });
  });

  it("reads an uploaded .txt transcript into the transcript field", async () => {
    stubApi({ "POST /activities": createdActivity });
    render(<LogActivity entityType="deal" entityId="d1" />);
    await pickOption(
      userEvent.setup(),
      screen.getByLabelText("Type"),
      "Meeting",
    );
    await userEvent.click(screen.getByLabelText("This text is a transcript"));
    const file = new File(["Anna: hello from a file"], "meeting.txt", {
      type: "text/plain",
    });
    const input = screen.getByLabelText("Or upload a file") as HTMLInputElement;
    await userEvent.upload(input, file);
    await waitFor(() =>
      expect(
        (screen.getByLabelText("Transcript") as HTMLTextAreaElement).value,
      ).toBe("Anna: hello from a file"),
    );
  });

  it("rejects a non-.txt upload with a visible reason, and leaves the transcript field untouched", async () => {
    stubApi({ "POST /activities": createdActivity });
    render(<LogActivity entityType="deal" entityId="d1" />);
    await pickOption(
      userEvent.setup(),
      screen.getByLabelText("Type"),
      "Meeting",
    );
    await userEvent.click(screen.getByLabelText("This text is a transcript"));
    const file = new File(
      ["WEBVTT\n\n00:00.000 --> 00:01.000\nhi"],
      "meeting.vtt",
      {
        type: "text/vtt",
      },
    );
    const input = screen.getByLabelText("Or upload a file") as HTMLInputElement;
    // fireEvent rather than userEvent.upload: accept is advisory (a real
    // browser's file picker can be switched to "All files"), and
    // userEvent.upload refuses to fire a change event for a file it judges
    // against `accept` itself — which would test user-event, not the
    // component's own rejection path.
    Object.defineProperty(input, "files", { value: [file] });
    fireEvent.change(input);
    await waitFor(() =>
      expect(screen.getByText("Only a .txt file is accepted.")).toBeTruthy(),
    );
    expect(
      (screen.getByLabelText("Transcript") as HTMLTextAreaElement).value,
    ).toBe("");
  });

  it("surfaces a failed file read instead of leaving the writer guessing", async () => {
    stubApi({ "POST /activities": createdActivity });
    render(<LogActivity entityType="deal" entityId="d1" />);
    await pickOption(
      userEvent.setup(),
      screen.getByLabelText("Type"),
      "Meeting",
    );
    await userEvent.click(screen.getByLabelText("This text is a transcript"));
    const file = new File(["Anna: hello"], "meeting.txt", {
      type: "text/plain",
    });
    vi.spyOn(file, "text").mockRejectedValue(new Error("unreadable"));
    const input = screen.getByLabelText("Or upload a file") as HTMLInputElement;
    await userEvent.upload(input, file);
    await waitFor(() =>
      expect(
        screen.getByText(
          "Could not read that file — try pasting the text instead.",
        ),
      ).toBeTruthy(),
    );
    expect(
      (screen.getByLabelText("Transcript") as HTMLTextAreaElement).value,
    ).toBe("");
  });
});
