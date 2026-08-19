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
