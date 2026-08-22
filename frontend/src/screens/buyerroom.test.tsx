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
import { BuyerRoomScreen } from "./buyerroom";

// The buyer's Deal Room: the credential in the hash is exchanged once and
// scrubbed, the session rides as a Bearer on every call, and each access
// state gets its own screen. What went out on the wire is the contract.

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

type Sent = { key: string; authorization: string | null; body: unknown };

function stubRoom(
  overrides: Record<string, () => Response> = {},
  sent: Sent[] = [],
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
      sent.push({
        key,
        authorization: request?.headers.get("Authorization") ?? null,
        body,
      });
      const override = overrides[key];
      if (override) return override();
      if (key === "POST /public/rooms/exchange") {
        return jsonResponse({
          session_token: "mdrs_session",
          expires_at: "2026-08-29T00:00:00Z",
        });
      }
      if (key === "GET /public/rooms/me") {
        return jsonResponse(LIVE);
      }
      if (key === "GET /public/rooms/documents") {
        return jsonResponse({
          data: [
            {
              id: "d-1",
              group_key: "legal",
              title: "Data processing agreement",
              position: 0,
              filename: "DPA_v7.pdf",
            },
          ],
        });
      }
      if (key === "GET /public/rooms/threads") {
        return jsonResponse({
          data: [
            {
              id: "th-1",
              room_id: "r-1",
              document_id: "d-1",
              required_change: false,
              state: "open",
              author: { side: "seller", name: "Ada Admin" },
              created_at: "2026-08-22T09:00:00Z",
              comments: [
                {
                  id: "c-1",
                  thread_id: "th-1",
                  body: "Does clause 4 work for you?",
                  author: { side: "seller", name: "Ada Admin" },
                  created_at: "2026-08-22T09:00:00Z",
                },
              ],
            },
          ],
        });
      }
      if (key === "POST /public/rooms/threads/th-1/comments") {
        return jsonResponse({ id: "th-1" });
      }
      return jsonResponse({});
    }),
  );
  return sent;
}

const LIVE = {
  access: "live",
  participant: {
    id: "p-1",
    full_name: "Laura Buyer",
    email: "laura@buyer.example",
    capability: "comment",
  },
  steward_name: "Ada Admin",
  room: {
    title: "Acme rollout",
    welcome_message: "Welcome, Laura.",
    release_no: 1,
    released_at: "2026-08-22T09:00:00Z",
    steward_name: "Ada Admin",
  },
};

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
  globalThis.sessionStorage.clear();
  globalThis.location.hash = "";
});

describe("BuyerRoomScreen", () => {
  it("exchanges the credential from the link, scrubs it, and reads the room as a Bearer", async () => {
    globalThis.location.hash = "#/room?c=mdr_secret";
    const sent = stubRoom();
    render(<BuyerRoomScreen />);

    await screen.findByRole("heading", { name: "Acme rollout" });
    expect(screen.getByText("Welcome, Laura.")).toBeInTheDocument();
    await screen.findByText("Data processing agreement");
    expect(screen.getByText("Legal")).toBeInTheDocument();

    expect(globalThis.location.hash).toBe("#/room");
    const exchange = sent.find((s) => s.key === "POST /public/rooms/exchange");
    expect(exchange?.body).toEqual({ credential: "mdr_secret" });
    const me = sent.find((s) => s.key === "GET /public/rooms/me");
    expect(me?.authorization).toBe("Bearer mdrs_session");
    expect(globalThis.sessionStorage.getItem("margince.room.session")).toBe(
      "mdrs_session",
    );
  });

  it("the buyer reads the seller's question and answers it with the Bearer", async () => {
    globalThis.sessionStorage.setItem("margince.room.session", "mdrs_session");
    const sent = stubRoom();
    const user = userEvent.setup();
    render(<BuyerRoomScreen />);

    await screen.findByText("Does clause 4 work for you?");
    await user.type(screen.getByLabelText("Reply"), "Thirty days would.");
    await user.click(screen.getByRole("button", { name: "Reply" }));
    await waitFor(() =>
      expect(
        sent.find((s) => s.key === "POST /public/rooms/threads/th-1/comments"),
      ).toBeTruthy(),
    );
    const posted = sent.find(
      (s) => s.key === "POST /public/rooms/threads/th-1/comments",
    );
    expect(posted?.body).toEqual({ body: "Thirty days would." });
    expect(posted?.authorization).toBe("Bearer mdrs_session");
  });

  it("a paused room shows the paused notice and no content", async () => {
    globalThis.sessionStorage.setItem("margince.room.session", "mdrs_session");
    stubRoom({
      "GET /public/rooms/me": () =>
        jsonResponse({ ...LIVE, access: "paused", room: undefined }),
    });
    render(<BuyerRoomScreen />);

    await screen.findByText("Access is paused");
    expect(
      screen.getByText(/Ada Admin has paused this room/),
    ).toBeInTheDocument();
    expect(screen.queryByText("Acme rollout")).not.toBeInTheDocument();
  });

  it("a dead link offers a fresh one by email, and the session is dropped on a 401", async () => {
    globalThis.location.hash = "#/room?c=mdr_used";
    const sent = stubRoom({
      "POST /public/rooms/exchange": () =>
        jsonResponse({ status: 404, code: "not_found" }, 404),
    });
    const user = userEvent.setup();
    render(<BuyerRoomScreen />);

    await screen.findByText("This link no longer works");
    await user.type(
      screen.getByLabelText("Your email address"),
      "laura@buyer.example",
    );
    await user.click(
      screen.getByRole("button", { name: /send me a new link/i }),
    );
    await screen.findByText(/a new link is on its way/i);
    const request = sent.find(
      (s) => s.key === "POST /public/rooms/link-request",
    );
    expect(request?.body).toEqual({ email: "laura@buyer.example" });
  });

  it("a session the server refuses is forgotten", async () => {
    globalThis.sessionStorage.setItem("margince.room.session", "mdrs_stale");
    stubRoom({
      "GET /public/rooms/me": () =>
        jsonResponse({ status: 401, code: "unauthorized" }, 401),
    });
    render(<BuyerRoomScreen />);

    await screen.findByText("This link no longer works");
    expect(globalThis.sessionStorage.getItem("margince.room.session")).toBe(
      null,
    );
  });
});
