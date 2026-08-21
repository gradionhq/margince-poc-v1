/** @vitest-environment jsdom */
// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, expect, it, vi } from "vitest";
import { meFixture } from "../app/mefixture";
import { LocaleProvider } from "../i18n";
import { FiltersScreen } from "./filters";

// What this screen owns is the WIRING and one judgement: how a count that is a
// moment behind should read. So these tests are about which request went out for
// which object, and about the three readings of the count — answered, stale, and
// not-yet-asked, which are three different things and must not collapse into one.

const PERSON_VOCAB = {
  resource: "person",
  fields: [
    {
      name: "full_name",
      type: "text",
      operators: ["eq", "neq", "in", "contains", "exists"],
      custom: false,
    },
  ],
};

const DEAL_VOCAB = {
  resource: "deal",
  fields: [
    {
      name: "stage_id",
      type: "id",
      operators: ["eq", "neq", "in", "exists"],
      custom: false,
    },
  ],
};

/** Every request the screen made, so a test can assert what it asked rather than
 *  inferring it from what rendered. */
function mount(
  preview?: {
    match_count: number;
    columns?: readonly string[];
    rows?: readonly Record<string, unknown>[];
  },
  views: readonly Record<string, unknown>[] = [],
) {
  const seen: string[] = [];
  const written: unknown[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const request = input instanceof Request ? input : null;
      const url = String(request ? request.url : input);
      const method = request?.method ?? init?.method ?? "GET";
      seen.push(url);
      const json = (body: unknown) =>
        new Response(JSON.stringify(body), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        });
      if (url.endsWith("/v1/me")) {
        return json(meFixture({}));
      }
      if (url.includes("/filters/vocabulary")) {
        return json(url.includes("resource=deal") ? DEAL_VOCAB : PERSON_VOCAB);
      }
      if (url.includes("/filters/preview")) {
        return json({
          resource: "person",
          match_count: preview?.match_count ?? 0,
          columns: preview?.columns ?? ["id"],
          rows: preview?.rows ?? [],
          truncated: false,
        });
      }
      if (url.includes("/exports")) {
        written.push(
          request ? await request.json() : JSON.parse(String(init?.body)),
        );
        // A rendered file, not a document: served as text with the name the
        // client is supposed to take its filename from.
        //
        // Deliberately NOT the name the client would compose for itself
        // (`person-export.csv`): identical strings would make the assertion pass
        // whether the header was read or ignored.
        return new Response("id,full_name\np1,Ann Lee\n", {
          status: 200,
          headers: {
            "Content-Type": "text/csv",
            "Content-Disposition": 'attachment; filename="people-slice.csv"',
          },
        });
      }
      if (url.includes("/lists")) {
        written.push(
          request ? await request.json() : JSON.parse(String(init?.body)),
        );
        return json({
          id: "l-new",
          name: "Anns",
          entity_type: "person",
          list_type: "dynamic",
        });
      }
      if (url.includes("/views")) {
        // A save is recorded rather than answered with a fixture: what the
        // screen STORES is the thing worth asserting, and a canned view row
        // would prove nothing about the blob that produced it.
        if (method === "POST") {
          written.push(
            request ? await request.json() : JSON.parse(String(init?.body)),
          );
          return json({ id: "v-new", ...(views[0] ?? {}) });
        }
        return json({
          data: views,
          page: { next_cursor: null, has_more: false },
        });
      }
      return json({});
    }),
  );
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>
      <LocaleProvider>{children}</LocaleProvider>
    </QueryClientProvider>
  );
  return { seen, written, wrapper };
}

/** A stored view row, with whatever `query` blob the test is about. */
function viewRow(name: string, query: unknown) {
  return {
    id: `v-${name}`,
    owner_id: "u-1",
    resource: "people",
    name,
    query,
    version: 1,
  };
}

afterEach(cleanup);

it("reads the vocabulary for the object the route names", async () => {
  const { seen, wrapper } = mount();
  render(<FiltersScreen id="deals" />, { wrapper });

  await waitFor(() => {
    expect(seen.some((url) => url.includes("resource=deal"))).toBe(true);
  });
  // And not the default: a route naming deals must not read the person
  // vocabulary, or the picker offers fields the deal engine refuses.
  expect(seen.some((url) => url.includes("resource=person"))).toBe(false);
});

it("falls back to contacts when the route names something unknown", async () => {
  const { seen, wrapper } = mount();
  render(<FiltersScreen id="widgets" />, { wrapper });

  await waitFor(() => {
    expect(seen.some((url) => url.includes("resource=person"))).toBe(true);
  });
});

it("asks for no preview until a clause is complete", async () => {
  const { seen, wrapper } = mount();
  render(<FiltersScreen />, { wrapper });

  await waitFor(() => {
    expect(seen.some((url) => url.includes("/filters/vocabulary"))).toBe(true);
  });
  // The tree starts as an empty group, which the engine refuses as
  // filter_shape_invalid — asking would spend a request to be told so.
  expect(seen.some((url) => url.includes("/filters/preview"))).toBe(false);
  // And the count says nothing has been asked, which is NOT the same as zero.
  expect(screen.getByText("Add a clause to see what it selects")).toBeTruthy();
  // Nor is there a results table: an empty one would say "no records match this
  // filter" about a filter nobody has written.
  expect(screen.queryByText("Matching records")).toBeNull();
});

// Which columns get chosen is proved directly against `previewColumnNames`; what
// this asserts is the wiring — that the rows behind the count actually arrive on
// the screen, keyed to the object being filtered.
it("shows the rows behind the count", async () => {
  const { wrapper } = mount({
    match_count: 1,
    columns: ["id", "full_name", "city", "created_at"],
    rows: [
      {
        id: "p1",
        full_name: "Ann Lee",
        city: "Berlin",
        created_at: "2026-08-01T00:00:00Z",
      },
    ],
  });
  const user = userEvent.setup();
  render(<FiltersScreen />, { wrapper });

  await screen.findByRole("button", { name: "Add clause" });
  await user.click(screen.getByRole("button", { name: "Add clause" }));
  await user.type(screen.getByLabelText("Value"), "ann");

  // The identity column, and the row behind the count — a number alone cannot be
  // checked, which is what AC-5's table is for.
  expect(await screen.findByText("Ann Lee")).toBeTruthy();
  expect(screen.getByRole("columnheader", { name: /full name/ })).toBeTruthy();
});

it("says how many match once a clause is complete", async () => {
  const { wrapper } = mount({ match_count: 12 });
  const user = userEvent.setup();
  render(<FiltersScreen />, { wrapper });

  await screen.findByRole("button", { name: "Add clause" });
  await user.click(screen.getByRole("button", { name: "Add clause" }));
  // The clause starts on full_name with `eq` and an empty value, so it is still
  // incomplete — typing a value is what makes it askable.
  await user.type(screen.getByLabelText("Value"), "ann");

  expect(await screen.findByText("12 contacts match")).toBeTruthy();
});

it("names the count after the object, not after a placeholder", async () => {
  const { wrapper } = mount({ match_count: 3 });
  const user = userEvent.setup();
  render(<FiltersScreen id="deals" />, { wrapper });

  await screen.findByRole("button", { name: "Add clause" });
  await user.click(screen.getByRole("button", { name: "Add clause" }));
  await user.type(screen.getByLabelText("Value"), "s1");

  // "3 deals match", not "3 contacts match" and not "3 match" — the object is
  // part of the sentence, which is why the copy is keyed per object.
  expect(await screen.findByText("3 deals match")).toBeTruthy();
});

it("restores a saved filter, count and all, without a clause being retyped", async () => {
  const { wrapper } = mount({ match_count: 7 }, [
    viewRow("Berliners", {
      filter: { and: [{ field: "full_name", op: "contains", value: "ann" }] },
    }),
  ]);
  const user = userEvent.setup();
  render(<FiltersScreen />, { wrapper });

  await user.click(
    await screen.findByRole("button", { name: "Load a saved filter" }),
  );
  await user.click(screen.getByRole("button", { name: "Berliners" }));

  // The stored clause is on screen AND askable: a view that restores a tree the
  // engine would refuse is a view that fails the moment it is opened.
  expect(await screen.findByDisplayValue("ann")).toBeTruthy();
  expect(await screen.findByText("7 contacts match")).toBeTruthy();
});

it("does not offer a view whose stored filter it cannot read", async () => {
  const { wrapper } = mount({ match_count: 1 }, [
    // A row written by an older build, or by hand: the operator is not one this
    // engine has. An entry that lights up and restores nothing is worse than no
    // entry, so the menu has nothing to show and does not render at all.
    viewRow("Stale", {
      filter: { and: [{ field: "c", op: "like", value: "x" }] },
    }),
    viewRow("List state", { list: { q: "ann", sort: "", filters: {} } }),
  ]);
  render(<FiltersScreen />, { wrapper });

  await screen.findByRole("button", { name: "Add clause" });
  expect(
    screen.queryByRole("button", { name: "Load a saved filter" }),
  ).toBeNull();
});

it("offers no save until the filter is one the engine would accept", async () => {
  const { wrapper } = mount({ match_count: 2 });
  const user = userEvent.setup();
  render(<FiltersScreen />, { wrapper });

  await user.click(await screen.findByRole("button", { name: "Add clause" }));
  // A clause with an empty value is refused per-leaf as filter_value_invalid, so
  // saving it would store a view nobody can open.
  expect(screen.queryByRole("button", { name: "Save view" })).toBeNull();

  await user.type(screen.getByLabelText("Value"), "ann");

  expect(await screen.findByRole("button", { name: "Save view" })).toBeTruthy();
});

it("saves the tree under the key the server validates as a filter", async () => {
  const { written, wrapper } = mount({ match_count: 2 });
  const user = userEvent.setup();
  render(<FiltersScreen />, { wrapper });

  await user.click(await screen.findByRole("button", { name: "Add clause" }));
  await user.type(screen.getByLabelText("Value"), "ann");
  await user.click(await screen.findByRole("button", { name: "Save view" }));
  await user.type(screen.getByLabelText("Name"), "Anns");
  await user.click(screen.getByRole("button", { name: "Save" }));

  await waitFor(() => {
    expect(written).toHaveLength(1);
  });
  // `people`, not `person` — the two endpoint families spell the same object
  // differently, and sending the filter vocabulary's word here would 422.
  // And the tree goes under `filter`, carrying no editor ids.
  expect(written[0]).toEqual({
    resource: "people",
    name: "Anns",
    query: {
      filter: { and: [{ field: "full_name", op: "eq", value: "ann" }] },
    },
  });
});

it("exports the filter on screen, under the name the server gave it", async () => {
  const createObjectURL = vi.fn(() => "blob:test");
  const revokeObjectURL = vi.fn();
  Object.defineProperties(URL, {
    createObjectURL: { configurable: true, value: createObjectURL },
    revokeObjectURL: { configurable: true, value: revokeObjectURL },
  });
  const anchors: HTMLAnchorElement[] = [];
  vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (
    this: HTMLAnchorElement,
  ) {
    anchors.push(this);
  });
  const { written, wrapper } = mount({ match_count: 2 });
  const user = userEvent.setup();
  render(<FiltersScreen />, { wrapper });

  await user.click(await screen.findByRole("button", { name: "Add clause" }));
  await user.type(screen.getByLabelText("Value"), "ann");
  await user.click(await screen.findByRole("button", { name: "Export CSV" }));

  await waitFor(() => {
    expect(written).toHaveLength(1);
  });
  // The tree on screen, not a saved view's id: what gets exported is what the
  // count above the button just said, through the one filter engine.
  expect(written[0]).toEqual({
    object: "person",
    filter: { and: [{ field: "full_name", op: "eq", value: "ann" }] },
    format: "csv",
  });
  expect(anchors[0]?.download).toBe("people-slice.csv");
});

it("says so when an export is refused, instead of leaving the reader waiting", async () => {
  const { wrapper } = mount({ match_count: 2 });
  const user = userEvent.setup();
  render(<FiltersScreen />, { wrapper });

  await user.click(await screen.findByRole("button", { name: "Add clause" }));
  await user.type(screen.getByLabelText("Value"), "ann");
  // The stub answers /exports with a 200 above; this test replaces just that
  // route's answer so the failure path is the real one — a Problem body the
  // screen has to read, not a thrown string.
  vi.stubGlobal(
    "fetch",
    vi.fn(
      async () =>
        new Response(
          JSON.stringify({
            title: "Export refused",
            status: 403,
            detail: "Bulk record read is human-only.",
          }),
          {
            status: 403,
            headers: { "Content-Type": "application/problem+json" },
          },
        ),
    ),
  );
  await user.click(await screen.findByRole("button", { name: "Export JSON" }));

  // The SERVER's reason, not "request failed". A refused bulk read can be
  // refused for something a reader can act on, and a generic line tells them
  // nothing — which is what handing a ProblemError to the body reader produces.
  expect((await screen.findByRole("alert")).textContent).toBe(
    "Bulk record read is human-only.",
  );
});

// The outcome #1468 was filed for: a human turning an authored filter into a
// dynamic list, which until now was reachable only by hand-writing the JSON.
it("creates a dynamic list from the filter on screen", async () => {
  const { written, wrapper } = mount({ match_count: 2 });
  const user = userEvent.setup();
  render(<FiltersScreen />, { wrapper });

  await user.click(await screen.findByRole("button", { name: "Add clause" }));
  await user.type(screen.getByLabelText("Value"), "ann");
  await user.click(await screen.findByRole("button", { name: "Save as list" }));
  await user.type(screen.getByLabelText("List name"), "Anns");
  await user.click(screen.getByRole("button", { name: "Create list" }));

  await waitFor(() => {
    expect(written).toHaveLength(1);
  });
  // `person`, not `people` — /lists shares the FILTER vocabulary's spelling,
  // where the saved-view rail takes the plural. And the tree goes to
  // `definition` unwrapped, carrying no editor ids, because that is the shape
  // the server validates against this resource's vocabulary.
  expect(written[0]).toEqual({
    name: "Anns",
    entity_type: "person",
    list_type: "dynamic",
    definition: { and: [{ field: "full_name", op: "eq", value: "ann" }] },
  });
});

it("offers no list until the filter is one the engine would accept", async () => {
  const { wrapper } = mount({ match_count: 2 });
  const user = userEvent.setup();
  render(<FiltersScreen />, { wrapper });

  await user.click(await screen.findByRole("button", { name: "Add clause" }));
  // A dynamic list is validated on create, so an incomplete definition is not a
  // list that exists and reads oddly — it is no list at all.
  expect(screen.queryByRole("button", { name: "Save as list" })).toBeNull();

  await user.type(screen.getByLabelText("Value"), "ann");

  expect(
    await screen.findByRole("button", { name: "Save as list" }),
  ).toBeTruthy();
});

it("keeps the naming dialog open, with the name, when the list is refused", async () => {
  const { wrapper } = mount({ match_count: 2 });
  const user = userEvent.setup();
  render(<FiltersScreen />, { wrapper });

  await user.click(await screen.findByRole("button", { name: "Add clause" }));
  await user.type(screen.getByLabelText("Value"), "ann");
  await user.click(await screen.findByRole("button", { name: "Save as list" }));
  await user.type(screen.getByLabelText("List name"), "Anns");

  vi.stubGlobal(
    "fetch",
    vi.fn(
      async () =>
        new Response(
          JSON.stringify({
            title: "Duplicate name",
            status: 409,
            detail: "A list called Anns already exists.",
          }),
          {
            status: 409,
            headers: { "Content-Type": "application/problem+json" },
          },
        ),
    ),
  );
  await user.click(screen.getByRole("button", { name: "Create list" }));

  // The reason, and the typed name still there to correct — a dialog that closed
  // on failure would lose the name and leave the reader unsure whether the list
  // was created.
  expect(await screen.findByRole("alert")).toBeTruthy();
  expect((screen.getByLabelText("List name") as HTMLInputElement).value).toBe(
    "Anns",
  );
});

/**
 * A stub answering `/filters/preview` with a problem body and everything else
 * normally.
 *
 * Only that route changes, so the failure path is the real one: the vocabulary
 * still arrives, the clause on screen is still complete, and what the screen
 * says about it is the whole assertion.
 */
function previewRefused(body: unknown, status: number) {
  const json = (payload: unknown) =>
    new Response(JSON.stringify(payload), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    });
  return vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input instanceof Request ? input.url : input);
    if (url.includes("/filters/preview")) {
      return new Response(JSON.stringify(body), {
        status,
        headers: { "Content-Type": "application/problem+json" },
      });
    }
    if (url.includes("/filters/vocabulary")) {
      return json(PERSON_VOCAB);
    }
    if (url.endsWith("/v1/me")) {
      return json(meFixture({}));
    }
    return json({ data: [], page: { next_cursor: null, has_more: false } });
  });
}

/**
 * Add a second clause, which is a new query key and therefore a new request.
 *
 * The refusal has to be the answer ON SCREEN, and react-query keeps the
 * previous key's success: re-asking the same one would read a cache hit.
 */
async function addSecondClause(user: ReturnType<typeof userEvent.setup>) {
  await user.click(screen.getByRole("button", { name: "Add clause" }));
  await user.type(screen.getAllByLabelText("Value")[1], "lee");
}

it("reads a refused preview as a failure, not as an unwritten filter", async () => {
  const { wrapper } = mount({ match_count: 2 });
  const user = userEvent.setup();
  render(<FiltersScreen />, { wrapper });

  await user.click(await screen.findByRole("button", { name: "Add clause" }));
  await user.type(screen.getByLabelText("Value"), "ann");
  await screen.findByText("2 contacts match");

  // A bodiless 502 — a proxy between the client and the app — is the failure
  // with the least to say, so what the screen says instead is all its own.
  vi.stubGlobal("fetch", previewRefused({}, 502));
  await addSecondClause(user);

  // Not "Add a clause to see what it selects": two complete clauses are on
  // screen, and blaming the reader for the server's refusal is exactly what hid
  // this refusal.
  expect(await screen.findByText("Count unavailable")).toBeTruthy();
  expect(screen.queryByText("Add a clause to see what it selects")).toBeNull();
  // The reason and the way out land where a sentence fits.
  expect(
    screen.getByRole("heading", { name: "Matching records" }),
  ).toBeTruthy();
  expect(screen.getByRole("button", { name: "Retry" })).toBeTruthy();
  // A 502 from a proxy carries no reader copy, so the shared line stands in
  // rather than the proxy's own words.
  expect((await screen.findByRole("alert")).textContent).toContain(
    "The request failed. No cause reported.",
  );
});

// `/filters/preview` is a POST, and the seat ceiling refuses every mutating
// method for a read seat BEFORE any role is consulted. So a read-seat member
// reads the vocabulary (granted to all seeded roles), builds a clause, and can
// never get a count — the one refusal on this screen a reader cannot see coming.
it("names the seat when a read seat is refused a preview", async () => {
  const { wrapper } = mount({ match_count: 2 });
  const user = userEvent.setup();
  render(<FiltersScreen />, { wrapper });

  await user.click(await screen.findByRole("button", { name: "Add clause" }));
  await user.type(screen.getByLabelText("Value"), "ann");
  await screen.findByText("2 contacts match");

  vi.stubGlobal(
    "fetch",
    previewRefused(
      {
        title: "Forbidden",
        status: 403,
        code: "seat_tier_insufficient",
        detail: "seat tier insufficient",
      },
      403,
    ),
  );
  await addSecondClause(user);

  // The server's own detail is the bare sentinel: it names a concept no reader
  // has met and offers nothing to do about it, so the catalog copy replaces it.
  const alert = await screen.findByRole("alert");
  expect(alert.textContent).toContain("This seat is read-only");
  expect(alert.textContent).toContain("Ask an operator to raise the seat.");
  expect(alert.textContent).not.toContain("seat tier insufficient");
});

it("starts a fresh tree when the object changes", async () => {
  const { wrapper } = mount({ match_count: 4 });
  const user = userEvent.setup();
  render(<FiltersScreen />, { wrapper });

  await screen.findByRole("button", { name: "Add clause" });
  await user.click(screen.getByRole("button", { name: "Add clause" }));
  expect(screen.getByLabelText("Value")).toBeTruthy();

  await user.click(screen.getByRole("button", { name: "Deals" }));

  // The person clause is gone rather than carried onto deals, where the field it
  // names does not exist — a filter the new vocabulary would refuse.
  expect(screen.queryByLabelText("Value")).toBeNull();
  expect(screen.getByText("Add a clause to see what it selects")).toBeTruthy();
});

// The shell's page head names every rail destination and prints the subtitle
// under it. A screen that named itself as well would put two page titles in one
// document, and a reader navigating by heading could not tell which was the page.
it("leaves the page's own name to the shell", async () => {
  const { wrapper } = mount({ match_count: 4 });
  render(<FiltersScreen />, { wrapper });

  await screen.findByRole("button", { name: "Add clause" });
  expect(screen.queryByRole("heading", { level: 1 })).toBeNull();
  expect(
    screen.queryByText(
      "Build a filter, watch what it selects, and save it as a view.",
    ),
  ).toBeNull();
  // The object choice stays, because it is the screen's own state rather than
  // the page's name: everything below it reads from it.
  expect(screen.getByRole("button", { name: "Contacts" })).toBeTruthy();
});
