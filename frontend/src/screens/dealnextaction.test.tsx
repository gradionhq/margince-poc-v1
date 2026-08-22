/** @vitest-environment jsdom */
import "@testing-library/jest-dom/vitest";
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
import { DealNextAction } from "./dealnextaction";

// The card performs the server's recommendation through the verb it names,
// with the arguments the server prepared — never a body it derived itself.

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

type Sent = { key: string; body: unknown };

function stub(nba: unknown, sent: Sent[] = []) {
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
      sent.push({ key, body });
      if (key === "GET /deals/deal-1/next-best-action") {
        return jsonResponse(nba);
      }
      if (key === "POST /tasks") {
        return jsonResponse({ id: "act-9", kind: "task" }, 201);
      }
      return jsonResponse({});
    }),
  );
  return sent;
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

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("DealNextAction", () => {
  it("creates the task the server prepared, with its body as it came", async () => {
    const args = {
      subject: "Agree the next step on Acme rollout",
      links: [{ entity_type: "deal", entity_id: "deal-1" }],
      source: "ui",
    };
    const sent = stub({
      deal_id: "deal-1",
      action: "create_task",
      reason: "Nothing has happened on this deal yet — decide the first step.",
      arguments: args,
      evidence: [],
      computed_at: "2026-08-22T12:00:00Z",
    });
    const user = userEvent.setup();
    render(<DealNextAction dealId="deal-1" />);

    await screen.findByText(/decide the first step/);
    await user.click(screen.getByRole("button", { name: "Add this task" }));
    await waitFor(() =>
      expect(sent.find((s) => s.key === "POST /tasks")).toBeTruthy(),
    );
    expect(sent.find((s) => s.key === "POST /tasks")?.body).toEqual(args);
  });

  it("says why there is nothing to do, and offers no button", async () => {
    stub({
      deal_id: "deal-1",
      action: "none",
      reason: 'An open task already says what is next: "Send the redline".',
      evidence: [{ text: "Open task: Send the redline" }],
      computed_at: "2026-08-22T12:00:00Z",
    });
    render(<DealNextAction dealId="deal-1" />);

    await screen.findByText("Open task: Send the redline");
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
    expect(screen.getByText("Nothing to add right now.")).toBeInTheDocument();
  });
});
