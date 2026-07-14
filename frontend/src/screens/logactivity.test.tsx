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
import { LocaleProvider } from "../i18n";
import { LogActivity } from "./logactivity";
import { PersonScreen } from "./people";

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
      return jsonResponse(emptyPage);
    }),
  );
}

const person = {
  id: "p1",
  workspace_id: "w",
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
      workspace_id: "w",
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
    const { occurred_at } = post?.body as { occurred_at: string };
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

  it("reveals the due-date input only for a task and posts due_at", async () => {
    const captured: Captured[] = [];
    stubApi({ "POST /activities": createdActivity }, captured);
    render(<LogActivity entityType="organization" entityId="o1" />);
    expect(screen.queryByLabelText("Due date")).toBeNull();
    await userEvent.selectOptions(screen.getByLabelText("Type"), "task");
    await userEvent.type(screen.getByLabelText("Due date"), "2026-07-10");
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
    const { due_at } = post?.body as { due_at: string };
    expect(new Date(due_at).toISOString()).toBe(
      new Date("2026-07-10").toISOString(),
    );
  });
});
