/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { components } from "../api/schema";
import { LocaleProvider } from "../i18n";
import { PersonTimelineTab } from "./persontabs";

// The chronology is exercised THROUGH the tab that renders it rather than by
// calling the hook: what the record page owes a reader is one list in one
// order, and a test that drove the hook alone would prove the merge and miss
// the thing the merge exists for.
//
// The three filters are three different reads, and each has its own way of
// being wrong: Activities can report the 360's capped page as the whole
// ledger, Changes can read a failed fetch as "nothing was ever changed", and
// All can drop one feed's rows on the floor while looking perfectly ordered.

type Person360 = components["schemas"]["Person360"];
type SectionActivity = NonNullable<Person360["activities"]>["data"][number];
type FieldChange = components["schemas"]["FieldHistoryEntry"];

const CAPTURED = {
  source: "manual",
  captured_by: "human:u-1",
  created_at: "2026-06-01T08:00:00Z",
  updated_at: "2026-08-01T08:00:00Z",
} as const;

function activity(
  row: Pick<SectionActivity, "id" | "kind" | "occurred_at"> &
    Partial<SectionActivity>,
): SectionActivity {
  return { is_done: false, ...CAPTURED, ...row };
}

const change: FieldChange = {
  id: "h-1",
  entity_type: "person",
  entity_id: "p-1",
  field: "owner_id",
  old_value: null,
  new_value: "Lena Fischer",
  // BETWEEN the two activities below, so a merge that ordered by time puts it
  // in the middle and one that merely concatenated the feeds does not.
  changed_at: "2026-08-10T09:00:00Z",
  actor_type: "human",
  actor_id: "u-1",
};

function viewWith(hasMore: boolean): Person360 {
  return {
    as_of: "2026-08-13T09:00:00Z",
    person: { id: "p-1", full_name: "Dana Buyer", ...CAPTURED },
    sections_omitted: [],
    activities: {
      data: [
        activity({
          id: "a-1",
          kind: "email",
          subject: "Fleet renewal",
          occurred_at: "2026-08-11T12:00:00Z",
        }),
        activity({
          id: "a-2",
          kind: "email",
          subject: "Depot access",
          occurred_at: "2026-08-09T08:00:00Z",
        }),
      ],
      page: { has_more: hasMore },
    },
  };
}

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

/**
 * changeFeed answers the field-history endpoint and nothing else. A stub that
 * answered every URL with the same body would let a test pass while the page
 * asked for something entirely different.
 */
function changeFeed(status = 200) {
  const calls: string[] = [];
  const fetcher = vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input instanceof Request ? input.url : input);
    calls.push(url);
    if (url.includes("/field-history")) {
      return jsonResponse(
        status === 200
          ? { data: [change], page: { has_more: false } }
          : { title: "boom" },
        status,
      );
    }
    return jsonResponse({});
  });
  return {
    fetcher,
    changesRead: () => calls.some((url) => url.includes("/field-history")),
  };
}

function withProviders(node: ReactNode) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  render(
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">{node}</LocaleProvider>
    </QueryClientProvider>,
  );
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("the record's chronology", () => {
  it("opens on the activities the record already holds, spending no second read", async () => {
    const feed = changeFeed();
    vi.stubGlobal("fetch", feed.fetcher);
    withProviders(<PersonTimelineTab personId="p-1" view={viewWith(false)} />);

    expect(screen.getByText("Fleet renewal")).toBeTruthy();
    // The changes feed is a read of its own, and a reader who never asked for
    // changes should not pay for one.
    expect(feed.changesRead()).toBe(false);
  });

  it("fetches the changes only once the reader asks for them", async () => {
    vi.stubGlobal("fetch", changeFeed().fetcher);
    withProviders(<PersonTimelineTab personId="p-1" view={viewWith(false)} />);

    await userEvent.click(screen.getByRole("button", { name: "Changes" }));

    await waitFor(() => expect(screen.getByText("owner id")).toBeTruthy());
    expect(screen.getByText("Lena Fischer")).toBeTruthy();
    // Changes is the CHANGES view: an activity showing here would mean the
    // filter narrowed nothing.
    expect(screen.queryByText("Fleet renewal")).toBeNull();
  });

  it("puts both feeds in one order under All, newest first", async () => {
    vi.stubGlobal("fetch", changeFeed().fetcher);
    withProviders(<PersonTimelineTab personId="p-1" view={viewWith(false)} />);

    await userEvent.click(screen.getByRole("button", { name: "All" }));

    await waitFor(() => expect(screen.getByText("owner id")).toBeTruthy());
    const rows = screen.getAllByRole("listitem");
    const text = rows.map((row) => row.textContent ?? "");
    const mailFirst = text.findIndex((row) => row.includes("Fleet renewal"));
    const changed = text.findIndex((row) => row.includes("owner id"));
    const mailLast = text.findIndex((row) => row.includes("Depot access"));
    // 11 Aug, then the 10 Aug change, then 9 Aug: interleaved by time rather
    // than one feed appended to the other.
    expect(mailFirst).toBeLessThan(changed);
    expect(changed).toBeLessThan(mailLast);
  });

  it("says a failed change read failed rather than reporting nothing was ever changed", async () => {
    vi.stubGlobal("fetch", changeFeed(500).fetcher);
    withProviders(<PersonTimelineTab personId="p-1" view={viewWith(false)} />);

    await userEvent.click(screen.getByRole("button", { name: "Changes" }));

    await waitFor(() =>
      expect(screen.getByText("This section did not load.")).toBeTruthy(),
    );
    expect(
      screen.queryByText(
        "No field on this record has been changed since it was created.",
      ),
    ).toBeNull();
  });

  it("states that a capped activities page is not the whole ledger", () => {
    withProviders(<PersonTimelineTab personId="p-1" view={viewWith(true)} />);

    expect(screen.getByText("Fleet renewal")).toBeTruthy();
    expect(
      screen.getByText(
        "There are more activities here than fit. Only the most recent ones are listed.",
      ),
    ).toBeTruthy();
  });

  it("keeps that notice off a page the server did not cut", () => {
    withProviders(<PersonTimelineTab personId="p-1" view={viewWith(false)} />);

    expect(screen.queryByText(/more activities here than fit/)).toBeNull();
  });
});
