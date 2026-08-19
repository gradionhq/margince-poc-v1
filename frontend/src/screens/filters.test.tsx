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
function mount(preview?: {
  match_count: number;
  columns?: readonly string[];
  rows?: readonly Record<string, unknown>[];
}) {
  const seen: string[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input instanceof Request ? input.url : input);
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
  return { seen, wrapper };
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
