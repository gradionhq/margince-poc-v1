/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { components } from "../api/schema";
import { RECORD_ZONE } from "../format/timezone";
import { LocaleProvider } from "../i18n";
import { LoadMoreButton } from "../screens/common";
import { groupChronology } from "../screens/timelinegroups";
import { activityTimeline } from "./activitytimeline";
import { GroupedTimelineList } from "./composed";
import {
  dayStartIso,
  TIMELINE_PAGE_SIZE,
  useRecordTimeline,
  useTimelineFilters,
} from "./recordtimeline";
import { pickOption } from "./select-testing";
import { TimelineFilterBar } from "./timelinefilterbar";

// The record timeline is exercised the way a record page composes it — the
// hook, the filter bar, the grouped list and the Load more button together —
// because what a page owes the reader is one list that grows and narrows on
// the SERVER, and a test that drove the hook alone would prove the wiring and
// miss the request it exists to send.

type Activity = components["schemas"]["Activity"];
type ActivityPage = components["schemas"]["ActivityListResponse"];

const CAPTURED = {
  source: "manual",
  captured_by: "human:u-1",
  created_at: "2026-06-01T08:00:00Z",
  updated_at: "2026-08-01T08:00:00Z",
} as const;

function activity(
  row: Pick<Activity, "id" | "subject" | "occurred_at"> & Partial<Activity>,
): Activity {
  return { kind: "email", is_done: false, ...CAPTURED, ...row };
}

const NEWEST = activity({
  id: "a-1",
  subject: "Fleet renewal",
  occurred_at: "2026-08-12T09:00:00Z",
});
const OLDER = activity({
  id: "a-2",
  subject: "Invoice question",
  occurred_at: "2026-07-02T09:00:00Z",
});

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

/**
 * activityFeed answers the activity list by cursor and records every URL it
 * was asked, so a test can say exactly which parameters the page sent.
 */
function activityFeed(pages: Record<string, ActivityPage>) {
  const calls: URL[] = [];
  const fetcher = vi.fn(async (input: RequestInfo | URL) => {
    const url = new URL(String(input instanceof Request ? input.url : input));
    if (!url.pathname.endsWith("/activities")) {
      return jsonResponse({});
    }
    calls.push(url);
    const cursor = url.searchParams.get("cursor") ?? "first";
    return jsonResponse(
      pages[cursor] ?? { data: [], page: { has_more: false } },
    );
  });
  return {
    fetcher,
    calls,
    last: () => calls[calls.length - 1],
  };
}

function Harness({ firstPage }: Readonly<{ firstPage?: ActivityPage }>) {
  const [filters, setFilters] = useTimelineFilters("p-1");
  const timeline = useRecordTimeline("person", "p-1", { filters, firstPage });
  const entries = activityTimeline(timeline.activities, "human:u-1", (row) => (
    <button type="button">relink {row.id}</button>
  ));
  return (
    <>
      <TimelineFilterBar value={filters} onChange={setFilters} />
      <GroupedTimelineList
        groups={groupChronology(entries, timeline.hasNextPage)}
        zone={RECORD_ZONE}
      />
      <LoadMoreButton query={timeline} />
    </>
  );
}

function mount(firstPage?: ActivityPage) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  render(
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">
        <Harness firstPage={firstPage} />
      </LocaleProvider>
    </QueryClientProvider>,
  );
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("a record timeline you can work in", () => {
  it("opens on one page of the size the mobile budget is measured against, then loads the next by cursor and appends it", async () => {
    const user = userEvent.setup();
    const feed = activityFeed({
      first: { data: [NEWEST], page: { has_more: true, next_cursor: "c-1" } },
      "c-1": { data: [OLDER], page: { has_more: false } },
    });
    vi.stubGlobal("fetch", feed.fetcher);
    mount();

    expect(await screen.findByText("Fleet renewal")).toBeTruthy();
    // The open is exactly one request, with nothing a filter would add.
    expect(feed.calls).toHaveLength(1);
    expect([...feed.last().searchParams.keys()].sort()).toEqual([
      "entity_id",
      "entity_type",
      "limit",
    ]);
    expect(feed.last().searchParams.get("limit")).toBe(
      String(TIMELINE_PAGE_SIZE),
    );

    await user.click(screen.getByRole("button", { name: "Load more" }));
    expect(await screen.findByText("Invoice question")).toBeTruthy();
    expect(feed.last().searchParams.get("cursor")).toBe("c-1");
    // Appended, not replaced.
    expect(screen.getByText("Fleet renewal")).toBeTruthy();
    // The server said that was the last page.
    expect(screen.queryByRole("button", { name: "Load more" })).toBeNull();
  });

  it("offers Load more only while the server holds another page", async () => {
    const feed = activityFeed({
      first: { data: [NEWEST], page: { has_more: false } },
    });
    vi.stubGlobal("fetch", feed.fetcher);
    mount();

    expect(await screen.findByText("Fleet renewal")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Load more" })).toBeNull();
  });

  it("seeded from the 360's own page, fetches nothing on open and continues from that page's cursor", async () => {
    const user = userEvent.setup();
    const feed = activityFeed({
      "c-360": { data: [OLDER], page: { has_more: false } },
    });
    vi.stubGlobal("fetch", feed.fetcher);
    mount({ data: [NEWEST], page: { has_more: true, next_cursor: "c-360" } });

    expect(screen.getByText("Fleet renewal")).toBeTruthy();
    expect(feed.calls).toHaveLength(0);

    await user.click(screen.getByRole("button", { name: "Load more" }));
    expect(await screen.findByText("Invoice question")).toBeTruthy();
    expect(feed.calls).toHaveLength(1);
    expect(feed.last().searchParams.get("cursor")).toBe("c-360");
  });

  it("sends the kind to the server rather than filtering the page it holds", async () => {
    const user = userEvent.setup();
    const feed = activityFeed({
      first: { data: [NEWEST], page: { has_more: false } },
    });
    vi.stubGlobal("fetch", feed.fetcher);
    mount();
    await screen.findByText("Fleet renewal");

    await pickOption(user, screen.getByLabelText("Activity kind"), "Tasks");
    await waitFor(() =>
      expect(feed.last().searchParams.get("kind")).toBe("task"),
    );
  });

  it("sends a search as q and says that limited conversations are left out", async () => {
    const user = userEvent.setup();
    const feed = activityFeed({
      first: { data: [NEWEST], page: { has_more: false } },
    });
    vi.stubGlobal("fetch", feed.fetcher);
    mount();
    await screen.findByText("Fleet renewal");
    const note = /Conversations whose content you may not open/;
    expect(screen.queryByText(note)).toBeNull();

    await user.type(
      screen.getByLabelText("Search this timeline"),
      "cutover{Enter}",
    );
    await waitFor(() =>
      expect(feed.last().searchParams.get("q")).toBe("cutover"),
    );
    expect(screen.getByText(note)).toBeTruthy();
  });

  it("sends a date range as an inclusive start and an exclusive next-day end", async () => {
    const user = userEvent.setup();
    const feed = activityFeed({
      first: { data: [NEWEST], page: { has_more: false } },
    });
    vi.stubGlobal("fetch", feed.fetcher);
    mount();
    await screen.findByText("Fleet renewal");

    await user.type(screen.getByLabelText("From"), "2026-03-03");
    await user.type(screen.getByLabelText("To"), "2026-03-05");
    await waitFor(() =>
      expect(feed.last().searchParams.get("occurred_before")).not.toBeNull(),
    );
    const params = feed.last().searchParams;
    expect(params.get("occurred_after")).toBe(dayStartIso("2026-03-03"));
    expect(params.get("occurred_before")).toBe(dayStartIso("2026-03-05", 1));
  });

  it("folds a conversation into one row that opens, with the conversation's own verbs on it", async () => {
    const user = userEvent.setup();
    const thread = ["a-1", "a-2", "a-3"].map((id, index) =>
      activity({
        id,
        subject: index === 0 ? "Re: Cutover plan" : "Cutover plan",
        occurred_at: `2026-08-1${index}T09:00:00Z`,
        thread_key: "t-1",
      }),
    );
    const feed = activityFeed({
      first: { data: thread, page: { has_more: false } },
    });
    vi.stubGlobal("fetch", feed.fetcher);
    mount();

    expect(await screen.findByText("3 messages")).toBeTruthy();
    // One row for three mails, titled by the newest.
    expect(screen.getAllByText(/Cutover plan/)).toHaveLength(1);
    // Relink is reachable from the group without opening it.
    expect(screen.getByRole("button", { name: "relink a-1" })).toBeTruthy();

    await user.click(screen.getByRole("button", { name: "Open" }));
    expect(screen.getAllByText(/Cutover plan/)).toHaveLength(4);
  });
});
