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
import { EntityRef } from "./entityref";
import { SetTargetAction } from "./quotas.forms";

// EntityRef (P-4 UUID-legibility): a cross-record reference resolves the
// target's id to its display name and backlinks to its 360.
//
// A reference with no name has three readings and they are different facts. A
// read still in flight is going to answer; an id a roster read to its end does
// not carry, or one the API answers 404 for, never will; a read that came
// back 403 or 500 answered nothing at all. Painting the id for all three made
// every page load show a uuid for a moment — which a reader takes for corrupt
// data rather than for a page still loading — and made a refused read look
// exactly like a record that genuinely carries no name.

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

describe("EntityRef", () => {
  it("renders the resolved name and backlinks to the record's 360 on click", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (request: Request) => {
        if (request.url.includes("/organizations/o-1")) {
          return jsonResponse({ id: "o-1", display_name: "Brandt GmbH" });
        }
        return jsonResponse({}, 404);
      }),
    );
    render(<EntityRef kind="organization" id="o-1" />);

    const link = await screen.findByRole("button", { name: "Brandt GmbH" });
    await userEvent.click(link);
    expect(window.location.hash).toBe("#/companies/o-1");
  });

  it("resolves a person to contacts/{id} and a deal to deals/{id}", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (request: Request) => {
        if (request.url.includes("/people/p-1")) {
          return jsonResponse({ id: "p-1", full_name: "Anna Weber" });
        }
        if (request.url.includes("/deals/d-1")) {
          return jsonResponse({ id: "d-1", name: "Q3 Renewal" });
        }
        return jsonResponse({}, 404);
      }),
    );
    const { rerender } = render(<EntityRef kind="person" id="p-1" />);
    await userEvent.click(
      await screen.findByRole("button", { name: "Anna Weber" }),
    );
    expect(window.location.hash).toBe("#/contacts/p-1");

    rerender(
      <QueryClientProvider client={new QueryClient()}>
        <LocaleProvider initial="en">
          <EntityRef kind="deal" id="d-1" />
        </LocaleProvider>
      </QueryClientProvider>,
    );
    await userEvent.click(
      await screen.findByRole("button", { name: "Q3 Renewal" }),
    );
    expect(window.location.hash).toBe("#/deals/d-1");
  });

  it("resolves a lead to leads/{id} (P-16: lead joins the ENTITY registry)", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (request: Request) => {
        if (request.url.includes("/leads/l-1")) {
          return jsonResponse({ id: "l-1", full_name: "Jordan Lee" });
        }
        return jsonResponse({}, 404);
      }),
    );
    render(<EntityRef kind="lead" id="l-1" />);
    await userEvent.click(
      await screen.findByRole("button", { name: "Jordan Lee" }),
    );
    expect(window.location.hash).toBe("#/leads/l-1");
  });

  it("falls back to the id (no link) once the lookup has settled without a name", async () => {
    // The record answers; it simply carries no display name. That is the one
    // reading the id belongs to, and the answer has to come back for it.
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => jsonResponse({ id: "o-nameless" })),
    );
    render(<EntityRef kind="organization" id="o-nameless" />);
    await waitFor(() => expect(screen.getByText("o-nameless")).toBeTruthy());
    expect(screen.queryByRole("button", { name: "o-nameless" })).toBeNull();
  });

  it("says the name could not be read when the record lookup fails, and never links it", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => jsonResponse({ title: "Forbidden" }, 403)),
    );
    render(<EntityRef kind="organization" id="o-403" />);

    expect(await screen.findByText("Name didn't load")).toBeTruthy();
    // Painting the id here would report the reference as settled: a reader
    // cannot tell a record with no name from one they were refused, and the
    // second is the one worth acting on.
    expect(screen.queryByText("o-403")).toBeNull();
    // A name that failed to load is not a safe link target either.
    expect(screen.queryByRole("button")).toBeNull();
  });

  it("keeps the id when the record is gone or hidden from this reader", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => jsonResponse({ code: "not_found" }, 404)),
    );
    render(<EntityRef kind="deal" id="d-gone" />);

    // A 404 is an answer: row-scope hides a record it will not admit exists,
    // and retrying produces the same one. The id is what a reader is left to
    // trace, so this is the settled reading rather than the failed one.
    await waitFor(() => expect(screen.getByText("d-gone")).toBeTruthy());
    expect(screen.queryByText("Name didn't load")).toBeNull();
  });

  it("says the name could not be read when the roster lookup fails", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => jsonResponse({ title: "Server error" }, 500)),
    );
    render(<EntityRef kind="user" id="u-500" />);

    expect(await screen.findByText("Name didn't load")).toBeTruthy();
    expect(screen.queryByText("u-500")).toBeNull();
  });

  it("says a name is on its way while the record read is in flight, and shows the id only once it has settled", async () => {
    // The resolvers are held rather than the clock: the test decides when the
    // read answers, so both readings are asserted without waiting on time.
    const answer: Array<(response: Response) => void> = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(
        () =>
          new Promise<Response>((resolve) => {
            answer.push(resolve);
          }),
      ),
    );
    render(<EntityRef kind="organization" id="o-slow" />);

    expect(await screen.findByText("Loading…")).toBeTruthy();
    expect(screen.queryByText("o-slow")).toBeNull();

    for (const resolve of answer) {
      resolve(jsonResponse({ id: "o-slow" }));
    }

    // Settled, the id is what is left, and it is a fact a reader can trace —
    // unlike the same id shown for a read that had simply not answered yet.
    await waitFor(() => expect(screen.getByText("o-slow")).toBeTruthy());
    expect(screen.queryByText("Loading…")).toBeNull();
  });

  it("says a name is on its way while the roster read is in flight, rather than showing the user's id", async () => {
    const answer: Array<(response: Response) => void> = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(
        () =>
          new Promise<Response>((resolve) => {
            answer.push(resolve);
          }),
      ),
    );
    render(<EntityRef kind="user" id="u-slow" />);

    expect(await screen.findByText("Loading…")).toBeTruthy();
    expect(document.body.textContent).not.toContain("u-slow");

    for (const resolve of answer) {
      resolve(
        jsonResponse({
          data: [{ id: "u-slow", display_name: "Priya Shah" }],
          page: { next_cursor: null, has_more: false },
        }),
      );
    }

    await waitFor(() => expect(screen.getByText("Priya Shah")).toBeTruthy());
  });

  it("shows a dash and fetches nothing when the id is absent", () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    render(<EntityRef kind="person" id={null} />);
    expect(screen.getByText("—")).toBeTruthy();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("resolves a user from the roster list and renders the name as plain text (no link)", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (request: Request) => {
        if (request.url.includes("/users")) {
          return jsonResponse({
            data: [
              { id: "u-1", display_name: "Priya Shah" },
              { id: "u-2", display_name: "Someone Else" },
            ],
            page: { next_cursor: null, has_more: false },
          });
        }
        return jsonResponse({}, 404);
      }),
    );
    render(<EntityRef kind="user" id="u-1" />);

    await waitFor(() => expect(screen.getByText("Priya Shah")).toBeTruthy());
    expect(screen.queryByRole("button", { name: "Priya Shah" })).toBeNull();
  });

  it("resolves a team from the roster list and renders the name as plain text (no link)", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (request: Request) => {
        if (request.url.includes("/teams")) {
          return jsonResponse({
            data: [{ id: "t-1", name: "Platform Team" }],
            page: { next_cursor: null, has_more: false },
          });
        }
        return jsonResponse({}, 404);
      }),
    );
    render(<EntityRef kind="team" id="t-1" />);

    await waitFor(() => expect(screen.getByText("Platform Team")).toBeTruthy());
    expect(screen.queryByRole("button", { name: "Platform Team" })).toBeNull();
  });

  it("falls back to the mono id (no link) when the settled roster does not carry the user", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (request: Request) => {
        if (request.url.includes("/users")) {
          return jsonResponse({
            data: [{ id: "u-1", display_name: "Priya Shah" }],
            page: { next_cursor: null, has_more: false },
          });
        }
        return jsonResponse({}, 404);
      }),
    );
    render(<EntityRef kind="user" id="u-missing" />);

    await waitFor(() => expect(screen.getByText("u-missing")).toBeTruthy());
    expect(screen.queryByRole("button", { name: "u-missing" })).toBeNull();
  });

  it("resolves a lead and backlinks to the leads screen on click", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (request: Request) => {
        if (request.url.includes("/leads/l-1")) {
          return jsonResponse({
            id: "l-1",
            full_name: "Jonas Keller",
            email: "jonas@example.com",
          });
        }
        return jsonResponse({}, 404);
      }),
    );
    render(<EntityRef kind="lead" id="l-1" />);

    await userEvent.click(
      await screen.findByRole("button", { name: "Jonas Keller" }),
    );
    expect(window.location.hash).toBe("#/leads/l-1");
  });

  it("uses a caller-supplied user name without reading the roster at all", async () => {
    const fetched: string[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (request: Request) => {
        fetched.push(new URL(request.url).pathname);
        return jsonResponse({ data: [] });
      }),
    );
    render(<EntityRef kind="user" id="u-1" name="Lars Jankowfsky" />);

    // A read that returns its own labels (the connection graph) must not have
    // its reader shown a raw uuid while /users is in flight — or forever, if
    // the caller cannot list the roster.
    expect(await screen.findByText("Lars Jankowfsky")).toBeTruthy();
    expect(fetched).toHaveLength(0);
  });

  it("looks the record up when the supplied name is only whitespace, rather than linking a label nobody can read", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (request: Request) => {
        if (request.url.includes("/organizations/o-1")) {
          return jsonResponse({ id: "o-1", display_name: "Brandt GmbH" });
        }
        return jsonResponse({}, 404);
      }),
    );
    render(<EntityRef kind="organization" id="o-1" name="   " />);

    // Whitespace is the caller saying it has nothing, exactly as an empty
    // string is. Taken at face value it becomes a button with no readable
    // label — a link a reader can neither read nor find.
    expect(
      await screen.findByRole("button", { name: "Brandt GmbH" }),
    ).toBeTruthy();
  });

  it("looks the user up when the supplied name is only whitespace", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (request: Request) => {
        if (request.url.includes("/users")) {
          return jsonResponse({
            data: [{ id: "u-1", display_name: "Priya Shah" }],
            page: { next_cursor: null, has_more: false },
          });
        }
        return jsonResponse({}, 404);
      }),
    );
    render(<EntityRef kind="user" id="u-1" name=" " />);

    expect(await screen.findByText("Priya Shah")).toBeTruthy();
  });
});

// The roster walk: `/users` and `/teams` are keyset-paged, so ONE page is not
// the roster. Every consumer of `useRoster` inherits what the walk sees — a
// name resolution, an owner column, a subject picker — which is why the walk
// carries whether it reached the end. Three facts are asserted here: the walk
// follows the cursor it was given, it stops at a bound rather than trusting a
// cursor forever, and neither the stop nor a failed page is allowed to read as
// a complete roster.

// Pages of a workspace roster, served the way the contract serves them: each
// response hands back the cursor of the NEXT page, and the last hands back
// none. The cursor is the page's index, so a request that forgot to echo it
// would read page one forever and the walk would never terminate.
function stubPagedRoster(
  pages: ReadonlyArray<ReadonlyArray<{ id: string; display_name: string }>>,
) {
  const cursors: Array<string | null> = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (request: Request) => {
      const url = new URL(request.url);
      if (!url.pathname.endsWith("/users")) {
        // The teams roster answers as an empty workspace: a 404 here would put
        // a failed read on the surface under test for a list it never asked
        // about.
        return jsonResponse({
          data: [],
          page: { next_cursor: null, has_more: false },
        });
      }
      const cursor = url.searchParams.get("cursor");
      cursors.push(cursor);
      const index = cursor ? Number(cursor) : 0;
      const last = index === pages.length - 1;
      return jsonResponse({
        data: pages[index],
        page: {
          next_cursor: last ? null : String(index + 1),
          has_more: !last,
        },
      });
    }),
  );
  return cursors;
}

// A server that never stops offering another page — the shape the bound exists
// for. Each page carries one member, named after the page it came from.
function stubEndlessRoster() {
  const cursors: Array<string | null> = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (request: Request) => {
      const url = new URL(request.url);
      if (!url.pathname.endsWith("/users")) {
        return jsonResponse({
          data: [],
          page: { next_cursor: null, has_more: false },
        });
      }
      const cursor = url.searchParams.get("cursor");
      cursors.push(cursor);
      const index = cursor ? Number(cursor) : 0;
      return jsonResponse({
        data: [{ id: `u-${index}`, display_name: `Member ${index}` }],
        page: { next_cursor: String(index + 1), has_more: true },
      });
    }),
  );
  return cursors;
}

describe("the roster walk", () => {
  it("follows the cursor to the last page, so a user beyond the first still resolves to a name", async () => {
    const cursors = stubPagedRoster([
      [{ id: "u-1", display_name: "Priya Shah" }],
      [{ id: "u-2", display_name: "Mor Adler" }],
      [{ id: "u-3", display_name: "Dana Fischer" }],
    ]);
    render(<EntityRef kind="user" id="u-3" />);

    // The reference the reader is looking at is on the third page. Read one
    // page deep it rendered a raw uuid — the same non-answer the id fallback
    // exists to avoid handing anyone.
    expect(await screen.findByText("Dana Fischer")).toBeTruthy();
    // Each page asked with the cursor the previous one minted, first page with
    // none.
    expect(cursors).toEqual([null, "1", "2"]);
  });

  it("offers a picker candidate who sits on the second page", async () => {
    stubPagedRoster([
      [{ id: "u-1", display_name: "Priya Shah" }],
      [{ id: "u-2", display_name: "Mor Adler" }],
    ]);
    const user = userEvent.setup();
    render(<SetTargetAction label="Set target" />);

    await user.click(screen.getByTestId("quota-create"));
    await user.click(await screen.findByRole("combobox"));

    // A quota is written against ONE subject, so a subject the picker never
    // offers is a quota nobody can create.
    expect(
      await screen.findByRole("option", { name: "Mor Adler" }),
    ).toBeTruthy();
  });

  it("surfaces a page that fails mid-walk as a failed read rather than a roster that ends there", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (request: Request) => {
        const url = new URL(request.url);
        if (url.searchParams.get("cursor") === "1") {
          return jsonResponse({ title: "Server error" }, 500);
        }
        return jsonResponse({
          data: [{ id: "u-1", display_name: "Priya Shah" }],
          page: { next_cursor: "1", has_more: true },
        });
      }),
    );
    render(<EntityRef kind="user" id="u-2" />);

    // The first page arrived and the second did not. Kept as the entries that
    // did load, this reads as a roster that simply does not carry `u-2` — and
    // the id would be printed as the settled answer for a read that never
    // finished.
    expect(await screen.findByText("Name didn't load")).toBeTruthy();
    expect(screen.queryByText("u-2")).toBeNull();
  });

  it("stops at its page budget and tells a picker the list is only part of one", async () => {
    const cursors = stubEndlessRoster();
    const user = userEvent.setup();
    render(<SetTargetAction label="Set target" />);

    await user.click(screen.getByTestId("quota-create"));

    // The bound is what makes an unstoppable cursor a short list instead of a
    // page that never paints — and the picker states the shortfall rather than
    // presenting what it read as the whole workspace.
    expect(await screen.findByText("Showing part of the list")).toBeTruthy();
    // Ten pages, once: the entries and the truncation flag are two readings of
    // ONE cache entry, so a surface asking for both does not walk twice.
    expect(cursors).toHaveLength(10);
  });

  it("says a name did not load, rather than printing the id, when the roster it stopped short of might hold it", async () => {
    stubEndlessRoster();
    render(<EntityRef kind="user" id="u-far" />);

    // A roster walked to its end answers about this id: nobody the reader may
    // list holds it, and the id is what is left to trace. A roster that ran out
    // of pages has answered nothing about it, and printing the id would state
    // that non-answer as settled fact.
    expect(await screen.findByText("Name didn't load")).toBeTruthy();
    expect(screen.queryByText("u-far")).toBeNull();
  });
});
