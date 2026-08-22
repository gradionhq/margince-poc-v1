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
import { DealNextMeeting, nextBookedMeeting } from "./dealmeeting";

// The card shows the nearest booked meeting still ahead and nothing else; a
// held or cancelled one, or one in the past, is not "next".

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

type Activity = components["schemas"]["Activity"];

function meeting(
  id: string,
  at: string,
  status?: Activity["meeting_status"],
): Activity {
  return {
    id,
    kind: "meeting",
    subject: `Meeting ${id}`,
    occurred_at: at,
    meeting_status: status,
    source: "manual",
    created_at: at,
    updated_at: at,
  } as Activity;
}

const render = (ui: ReactNode) =>
  rtlRender(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <LocaleProvider initial="en">{ui}</LocaleProvider>
    </QueryClientProvider>,
  );

it("picks the nearest booked meeting ahead", () => {
  const now = new Date("2026-08-23T09:00:00Z");
  const next = nextBookedMeeting(
    [
      meeting("past", "2026-08-20T09:00:00Z", "held"),
      meeting("far", "2026-09-10T09:00:00Z", "booked"),
      meeting("cancelled", "2026-08-24T09:00:00Z", "canceled"),
      meeting("near", "2026-08-25T09:00:00Z"),
    ],
    now,
  );
  expect(next?.id).toBe("near");
});

it("leaves out a withheld meeting, whose brief would not open", () => {
  const now = new Date("2026-08-23T09:00:00Z");
  const withheld = {
    ...meeting("secret", "2026-08-24T09:00:00Z", "booked"),
    content_state: "withheld",
  } as Activity;
  expect(nextBookedMeeting([withheld], now)).toBeNull();
});

function stubMeetings(rows: Activity[]) {
  vi.stubGlobal("fetch", (input: Request) => {
    const url = new URL(input.url);
    if (
      url.pathname.endsWith("/activities") &&
      url.searchParams.get("kind") === "meeting"
    ) {
      return Promise.resolve(
        new Response(JSON.stringify({ data: rows, page: {} }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      );
    }
    return Promise.resolve(new Response("{}", { status: 200 }));
  });
}

it("renders nothing when no meeting is booked", async () => {
  stubMeetings([meeting("past", "2020-01-01T09:00:00Z")]);
  const { container } = render(<DealNextMeeting dealId="deal-1" />);
  await waitFor(() => expect(container).toBeEmptyDOMElement());
});

it("offers the brief for the next meeting", async () => {
  stubMeetings([meeting("soon", "2999-01-01T09:00:00Z")]);
  render(<DealNextMeeting dealId="deal-1" />);
  expect(await screen.findByText("Meeting soon")).toBeInTheDocument();
  expect(
    screen.getByRole("button", { name: "Open the brief" }),
  ).toBeInTheDocument();
});
