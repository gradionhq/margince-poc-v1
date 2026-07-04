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
import { LocaleProvider } from "../i18n";
import { HomeScreen } from "./home";
import { confidenceLevel, InboxScreen } from "./inbox";
import { groupTask, TasksScreen } from "./tasks";

// B-EP09.12a/b/d acceptance: approve and reject from the inbox rows, the
// inline editor sends edited_payload (re-admission is the server's job),
// Home ranks staged approvals first and reuses the same 12a rows, and the
// task grouping/complete transitions hold.

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  window.location.hash = "";
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

type Approval = components["schemas"]["Approval"];

const approval: Approval = {
  id: "ap-1",
  workspace_id: "w",
  kind: "send_email",
  status: "pending",
  proposed_by: "agent:runner",
  summary: "Send the follow-up to Anna Weber",
  proposed_change: {
    subject: "Follow-up",
    body: "Hi Anna — shall we sync next week?",
  },
  confidence: 0.62,
  evidence: [
    { evidence_snippet: "…shall we sync next week?…", source_type: "activity" },
  ],
  created_at: "2026-07-05T05:00:00Z",
} as Approval;

function inboxBackend(calls: { url: string; body: unknown }[]) {
  return vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const request = input instanceof Request ? input : null;
    const url = String(request ? request.url : input);
    const method = request ? request.method : (init?.method ?? "GET");
    if (method === "POST" && /approvals\/ap-1\/(approve|reject)/.test(url)) {
      let body: unknown = null;
      try {
        body = request ? await request.json() : JSON.parse(String(init?.body));
      } catch {
        body = null;
      }
      calls.push({ url, body });
      return jsonResponse({ ...approval, status: "approved" });
    }
    if (url.includes("/approvals")) {
      return jsonResponse({ data: [approval], page: { next_cursor: null } });
    }
    return jsonResponse({ data: [], page: { next_cursor: null } });
  });
}

describe("InboxScreen (B-EP09.12a)", () => {
  it("approves a queued item", async () => {
    const calls: { url: string; body: unknown }[] = [];
    vi.stubGlobal("fetch", inboxBackend(calls));
    render(<InboxScreen />);
    await waitFor(() => expect(screen.getByText("send_email")).toBeTruthy());
    expect(screen.getByText("agent: agent:runner")).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: "Accept" }));
    await waitFor(() => expect(calls).toHaveLength(1));
    expect(calls[0].url).toContain("/approve");
  });

  it("rejects a queued item", async () => {
    const calls: { url: string; body: unknown }[] = [];
    vi.stubGlobal("fetch", inboxBackend(calls));
    render(<InboxScreen />);
    await waitFor(() => expect(screen.getByText("send_email")).toBeTruthy());
    await userEvent.click(screen.getByRole("button", { name: "Reject" }));
    await waitFor(() => expect(calls).toHaveLength(1));
    expect(calls[0].url).toContain("/reject");
  });

  it("the inline editor approves with edited_payload (edit-then-send)", async () => {
    const calls: { url: string; body: unknown }[] = [];
    vi.stubGlobal("fetch", inboxBackend(calls));
    render(<InboxScreen />);
    await waitFor(() => expect(screen.getByText("send_email")).toBeTruthy());
    await userEvent.click(screen.getByRole("button", { name: "Edit" }));
    const subject = screen.getByRole("textbox", { name: "subject" });
    await userEvent.clear(subject);
    await userEvent.type(subject, "Follow-up (edited)");
    await userEvent.click(
      screen.getByRole("button", { name: "Approve edited" }),
    );
    await waitFor(() => expect(calls).toHaveLength(1));
    expect(calls[0].url).toContain("/approve");
    expect(calls[0].body).toMatchObject({
      edited_payload: { subject: "Follow-up (edited)" },
    });
  });
});

describe("HomeScreen (B-EP09.12b)", () => {
  it("ranks staged approvals first, reusing the inbox rows", async () => {
    vi.stubGlobal("fetch", inboxBackend([]));
    render(<HomeScreen />);
    await waitFor(() =>
      expect(screen.getByText("Waiting on you")).toBeTruthy(),
    );
    expect(screen.getByText("send_email")).toBeTruthy();
    expect(screen.getByText("Nothing sent yet")).toBeTruthy();
  });

  it("renders the honest quiet state when nothing is staged or stalled", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse({ data: [], page: { next_cursor: null } }),
      ),
    );
    render(<HomeScreen />);
    await waitFor(() =>
      expect(
        screen.getByText("All quiet. Nothing staged, nothing stalled."),
      ).toBeTruthy(),
    );
  });
});

describe("TasksScreen (B-EP09.12d)", () => {
  const now = new Date("2026-07-05T12:00:00Z");

  it("groups by due date honestly", () => {
    const base = {
      id: "t",
      workspace_id: "w",
      kind: "task" as const,
      occurred_at: "2026-07-01T00:00:00Z",
      is_done: false,
      source: "manual",
      captured_by: "human:u",
      created_at: "2026-07-01T00:00:00Z",
      updated_at: "2026-07-01T00:00:00Z",
    };
    expect(groupTask({ ...base, due_at: "2026-07-04T10:00:00Z" }, now)).toBe(
      "overdue",
    );
    expect(groupTask({ ...base, due_at: "2026-07-05T18:00:00Z" }, now)).toBe(
      "today",
    );
    expect(groupTask({ ...base, due_at: "2026-07-09T10:00:00Z" }, now)).toBe(
      "upcoming",
    );
    expect(groupTask({ ...base, due_at: null }, now)).toBe("undated");
  });

  it("completing a task PATCHes is_done", async () => {
    const patches: unknown[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const request = input instanceof Request ? input : null;
        const method = request ? request.method : (init?.method ?? "GET");
        if (method === "PATCH") {
          patches.push(
            request ? await request.json() : JSON.parse(String(init?.body)),
          );
          return jsonResponse({});
        }
        return jsonResponse({
          data: [
            {
              id: "t1",
              workspace_id: "w",
              kind: "task",
              subject: "Call Anna",
              occurred_at: "2026-07-01T00:00:00Z",
              due_at: "2026-07-04T10:00:00Z",
              is_done: false,
              source: "manual",
              captured_by: "human:u",
            },
          ],
          page: { next_cursor: null },
        });
      }),
    );
    render(<TasksScreen />);
    await waitFor(() => expect(screen.getByText("Call Anna")).toBeTruthy());
    await userEvent.click(screen.getByRole("button", { name: "Done" }));
    await waitFor(() => expect(patches).toHaveLength(1));
    expect(patches[0]).toMatchObject({ is_done: true });
  });
});

describe("confidenceLevel mapping", () => {
  it("maps numeric confidence to the three-glyph vocabulary", () => {
    expect(confidenceLevel(0.9)).toBe("high");
    expect(confidenceLevel(0.6)).toBe("med");
    expect(confidenceLevel(0.2)).toBe("low");
    expect(confidenceLevel(null)).toBeNull();
  });
});
