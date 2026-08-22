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
import { afterEach, expect, it, vi } from "vitest";
import type { components } from "../api/schema";
import { LocaleProvider } from "../i18n";
import { DealRoomAside } from "./dealroom";

// The shared to-do list as a rep meets it: the two sides' items are told apart,
// ticking one writes with the row's version, and a finished room says why it
// takes no more changes rather than failing the click.

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

type DealRoom = components["schemas"]["DealRoom"];
type DealRoomTask = components["schemas"]["DealRoomTask"];

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

function task(over: Partial<DealRoomTask>): DealRoomTask {
  return {
    id: "task-1",
    room_id: "room-1",
    side: "buyer",
    title: "Return the signed NDA",
    position: 0,
    done: false,
    source: "manual",
    version: 3,
    created_at: "2026-08-22T09:00:00Z",
    updated_at: "2026-08-22T09:00:00Z",
    ...over,
  } as DealRoomTask;
}

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

// Routes the two GETs the aside makes, and records every write so a test can
// assert what actually went on the wire rather than what the UI drew.
function stubApi(
  rooms: DealRoom[],
  tasks: DealRoomTask[],
): { calls: Request[] } {
  const calls: Request[] = [];
  // openapi-fetch hands `fetch` a Request rather than a url string, so the url
  // and the method are read off that. Stringifying the argument yields
  // "[object Request]", which matches no route and quietly answers every call
  // with the same payload.
  vi.stubGlobal("fetch", (input: Request) => {
    if (input.method !== "GET") {
      calls.push(input.clone());
      return Promise.resolve(jsonResponse(task({ done: true, version: 4 })));
    }
    if (new URL(input.url).pathname.endsWith("/tasks")) {
      return Promise.resolve(jsonResponse({ data: tasks, page: {} }));
    }
    return Promise.resolve(jsonResponse({ data: rooms, page: {} }));
  });
  return { calls };
}

it("tells the two sides' to-dos apart", async () => {
  stubApi(
    [room("live")],
    [
      task({ id: "t1", side: "buyer", title: "Return the signed NDA" }),
      task({ id: "t2", side: "seller", title: "Send the pricing sheet" }),
    ],
  );
  render(<DealRoomAside dealId="deal-1" />);

  const theirs = await screen.findByRole("switch", {
    name: /Return the signed NDA/,
  });
  const ours = screen.getByRole("switch", { name: /Send the pricing sheet/ });
  // Who owes an item is the whole point of a shared list — an item with no side
  // is one neither party chases.
  expect(theirs).toHaveAccessibleDescription(/buyer/i);
  expect(ours).toHaveAccessibleDescription(/We owe/i);
});

it("ticking an item sends the row's version, so a concurrent edit is refused", async () => {
  const { calls } = stubApi([room("live")], [task({ version: 3 })]);
  const user = userEvent.setup();
  render(<DealRoomAside dealId="deal-1" />);

  await user.click(
    await screen.findByRole("switch", { name: /Return the signed NDA/ }),
  );

  await waitFor(() => expect(calls).toHaveLength(1));
  const write = calls[0];
  expect(write.url).toContain("/deal-rooms/room-1/tasks/task-1");
  expect(write.method).toBe("PATCH");
  expect(await write.json()).toEqual({ done: true });
  // Unpinned, the write lands on top of an edit it never saw and reports
  // success to both editors.
  expect(write.headers.get("If-Match")).toBe("3");
});

it("a finished room says why its list takes no more changes", async () => {
  stubApi([room("closed")], [task({})]);
  render(<DealRoomAside dealId="deal-1" />);

  const toggle = await screen.findByRole("switch", {
    name: /Return the signed NDA/,
  });
  // The refusal reaches a screen reader through the control's own description,
  // rather than sitting beside it as decoration.
  expect(toggle).toHaveAccessibleDescription(/record/i);
  // A stated reason IS the refusal rather than a note beside one: Switch sets
  // the native disabled attribute for it, so the click never reaches the
  // server. A reader told why they may not change something, who then can, is
  // worse off than one told nothing.
  expect(toggle).toBeDisabled();
  // And the add form is gone entirely: an input that refuses every submission
  // is worse than no input.
  expect(screen.queryByTestId("room-task-new")).not.toBeInTheDocument();
});

it("renders nothing at all when the deal has no room", async () => {
  stubApi([], []);
  const { container } = render(<DealRoomAside dealId="deal-1" />);

  await waitFor(() => expect(container).toBeEmptyDOMElement());
});

it("says an empty list is empty rather than drawing a bare card", async () => {
  stubApi([room("live")], []);
  render(<DealRoomAside dealId="deal-1" />);

  expect(
    await screen.findByText(/Nothing outstanding between you and the buyer/),
  ).toBeInTheDocument();
});
