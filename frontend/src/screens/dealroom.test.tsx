/** @vitest-environment jsdom */
import "@testing-library/jest-dom/vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  render as rtlRender,
  screen,
  waitFor,
} from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, expect, it, vi } from "vitest";
import type { components } from "../api/schema";
import { LocaleProvider } from "../i18n";
import { DealRoomAside } from "./dealroom";

// The Deal Room aside as a rep meets it: the room's state is named, a finished
// room says why it takes no more content rather than failing the click, and a
// deal without a room gets no card at all.

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

type DealRoom = components["schemas"]["DealRoom"];

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

function room(state: string): DealRoom {
  return {
    id: "room-1",
    deal_id: "deal-1",
    title: "Acme Expansion — Deal Room",
    state,
    source: "manual",
    version: 1,
    created_at: "2026-08-22T09:00:00Z",
    updated_at: "2026-08-22T09:00:00Z",
    release_count: 0,
  } as DealRoom;
}

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

// The principal the controls ask about. `useCanWrite` folds two questions —
// does this role hold the grant, and is this a full seat — so a double that
// answers only one of them would let a control through that the real page
// refuses.
function me(mayWrite: boolean) {
  return {
    user: { id: "u1" },
    authorization: {
      seat_type: mayWrite ? "full" : "read_only",
      objects: {
        deal_room: {
          create: mayWrite,
          read: true,
          update: mayWrite,
          delete: mayWrite,
        },
      },
    },
  };
}

// Routes the GETs the aside makes, and records every write so a test can
// assert what actually went on the wire rather than what the UI drew.
function stubApi(rooms: DealRoom[], mayWrite = true): { calls: Request[] } {
  const calls: Request[] = [];
  // openapi-fetch hands `fetch` a Request rather than a url string, so the url
  // and the method are read off that. Stringifying the argument yields
  // "[object Request]", which matches no route and quietly answers every call
  // with the same payload.
  vi.stubGlobal("fetch", (input: Request) => {
    if (input.method !== "GET") {
      calls.push(input.clone());
      return Promise.resolve(jsonResponse({}));
    }
    const path = new URL(input.url).pathname;
    if (path.endsWith("/me")) {
      return Promise.resolve(jsonResponse(me(mayWrite)));
    }
    if (path.endsWith("/documents") || path.endsWith("/threads")) {
      return Promise.resolve(jsonResponse({ data: [], page: {} }));
    }
    if (path.endsWith("/decisions")) {
      return Promise.resolve(jsonResponse({ data: [] }));
    }
    return Promise.resolve(jsonResponse({ data: rooms, page: {} }));
  });
  return { calls };
}

it("names the room's state on the card", async () => {
  stubApi([room("live")]);
  render(<DealRoomAside dealId="deal-1" />);

  expect(await screen.findByText("Live")).toBeInTheDocument();
  expect(screen.getByText("Documents")).toBeInTheDocument();
});

it("a finished room says why it takes no more content", async () => {
  stubApi([room("closed")]);
  render(<DealRoomAside dealId="deal-1" />);

  // The refusal IS the control's replacement: the add form is gone entirely,
  // because an input that refuses every submission is worse than no input.
  expect(
    (await screen.findAllByText(/finished, so what it shared is now a record/))
      .length,
  ).toBeGreaterThan(0);
  expect(
    screen.queryByLabelText("File from this deal"),
  ).not.toBeInTheDocument();
});

it("renders nothing at all when the deal has no room", async () => {
  stubApi([]);
  const { container } = render(<DealRoomAside dealId="deal-1" />);

  await waitFor(() => expect(container).toBeEmptyDOMElement());
});

it("a reader who may not write is told so instead of being offered the controls", async () => {
  // The server refuses a read-only seat's write regardless, but only after the
  // click. A control that looks live and comes back 403 teaches a reader that
  // the product is broken rather than that the permission is.
  stubApi([room("live")], false);
  render(<DealRoomAside dealId="deal-1" />);

  expect(
    (await screen.findAllByText(/read this room but not change/i)).length,
  ).toBeGreaterThan(0);
  expect(
    screen.queryByLabelText("File from this deal"),
  ).not.toBeInTheDocument();
});
