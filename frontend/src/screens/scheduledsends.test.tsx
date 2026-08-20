/** @vitest-environment jsdom */
// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { LocaleProvider } from "../i18n";
import { ScheduledSendsScreen } from "./scheduledsends";

type Send = {
  id: string;
  status: "scheduled" | "released" | "sent" | "cancelled" | "held";
  scheduled_at: string;
  scheduled_tz: string;
  subject: string;
  to: string[];
  version: number;
  created_at: string;
  updated_at: string;
  held_reason?: string;
};

// The reader's own zone, whatever machine this runs on. Every zone assertion
// below is written against it rather than a fixed name, so the suite proves the
// invariant ("the zone is named when it is not yours") instead of passing only
// on a laptop set to Berlin.
const readerZone = Intl.DateTimeFormat().resolvedOptions().timeZone;
const OTHER_ZONE = readerZone === "Asia/Tokyo" ? "Europe/Berlin" : "Asia/Tokyo";

const WAITING: Send = {
  id: "019f7e65-fbf7-7114-b114-40af4af63ae8",
  status: "scheduled",
  scheduled_at: "2026-09-01T07:00:00Z",
  scheduled_tz: readerZone,
  subject: "The renewal quote",
  to: ["ceo@acme.test"],
  version: 3,
  created_at: "2026-08-20T09:00:00Z",
  updated_at: "2026-08-20T09:00:00Z",
};

const HELD: Send = {
  ...WAITING,
  id: "019f7e65-fbf7-7114-b114-40af4af63ae9",
  status: "held",
  held_reason: "consent_withdrawn",
  subject: "The follow-up",
  to: ["ops@acme.test", "cfo@acme.test", "legal@acme.test"],
};

const SENT: Send = {
  ...WAITING,
  id: "019f7e65-fbf7-7114-b114-40af4af63aea",
  status: "sent",
  subject: "Last week's summary",
};

/** Every request the screen made, so a test can assert on the write itself. */
type Call = {
  method: string;
  path: string;
  body: unknown;
  ifMatch: string | null;
};

function mount(rows: Send[]) {
  const calls: Call[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const request = input instanceof Request ? input : null;
      const url = new URL(
        request ? request.url : String(input),
        "https://test",
      );
      const method = init?.method ?? request?.method ?? "GET";
      const headers = new Headers(init?.headers ?? request?.headers);
      // The client hands `fetch` a Request for a write, so the body is on the
      // Request rather than on `init`. Read both, or a write's payload reads as
      // absent and a test asserting on it passes for the wrong reason.
      const raw = init?.body
        ? String(init.body)
        : request
          ? await request.clone().text()
          : "";
      calls.push({
        method,
        path: url.pathname,
        body: raw === "" ? null : JSON.parse(raw),
        ifMatch: headers.get("If-Match"),
      });
      if (method === "PATCH" || url.pathname.endsWith("/cancel")) {
        return new Response(null, { status: 204 });
      }
      return new Response(JSON.stringify(rows), {
        headers: { "Content-Type": "application/json" },
      });
    }),
  );
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  render(
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">
        <ScheduledSendsScreen />
      </LocaleProvider>
    </QueryClientProvider>,
  );
  return { calls };
}

/** A 409 whose problem body carries the skew code, on the write named. */
function mountRefusing(rows: Send[], refuse: "PATCH" | "POST") {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const request = input instanceof Request ? input : null;
      const url = new URL(
        request ? request.url : String(input),
        "https://test",
      );
      const method = init?.method ?? request?.method ?? "GET";
      if (method === refuse) {
        return new Response(
          JSON.stringify({
            type: "about:blank",
            title: "Conflict",
            status: 409,
            code: "version_skew",
            detail: "row version 3 is not the current version",
          }),
          {
            status: 409,
            headers: { "Content-Type": "application/problem+json" },
          },
        );
      }
      if (url.pathname.endsWith("/scheduled-sends")) {
        return new Response(JSON.stringify(rows), {
          headers: { "Content-Type": "application/json" },
        });
      }
      return new Response(null, { status: 204 });
    }),
  );
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  render(
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">
        <ScheduledSendsScreen />
      </LocaleProvider>
    </QueryClientProvider>,
  );
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

it("puts what needs a person first, and says why it stopped", async () => {
  mount([SENT, WAITING, HELD]);
  const headings = await screen.findAllByRole("heading", { level: 2 });
  expect(headings.map((heading) => heading.textContent)).toEqual([
    "Stopped, waiting on you",
    "Waiting to send",
    "No longer waiting",
  ]);
  // The held reason is words on the row, not a wire token: a rep cannot act on
  // "consent_withdrawn".
  expect(screen.getByText(/withdrew their consent/i)).toBeTruthy();
  expect(screen.queryByText("consent_withdrawn")).toBeNull();
});

it("counts the recipients it is not listing", async () => {
  mount([HELD]);
  expect(await screen.findByText(/ops@acme\.test and 2 more/)).toBeTruthy();
});

it("offers no verb on a message that has already gone", async () => {
  mount([SENT]);
  expect(await screen.findByText("Sent")).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Change moment" })).toBeNull();
  expect(screen.queryByRole("button", { name: "Withdraw" })).toBeNull();
});

it("names the zone a moment was picked in when it is not the reader's", async () => {
  mount([{ ...WAITING, scheduled_tz: OTHER_ZONE }]);
  expect(await screen.findByText(new RegExp(`in ${OTHER_ZONE}`))).toBeTruthy();
});

it("stays silent about the zone when it is the reader's own", async () => {
  mount([WAITING]);
  await screen.findByText("The renewal quote");
  expect(screen.queryByText(new RegExp(`in ${readerZone}`))).toBeNull();
});

it("pins a move to the version the row was drawn from", async () => {
  const user = userEvent.setup();
  const { calls } = mount([WAITING]);
  await user.click(
    await screen.findByRole("button", { name: "Change moment" }),
  );
  const picker = screen.getByLabelText(/New moment for/);
  // Seeded from the send's own moment, so a rep who opens the control and saves
  // without touching it does not move the message.
  expect((picker as HTMLInputElement).value).toMatch(
    /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}$/,
  );
  await user.clear(picker);
  await user.type(picker, "2026-09-02T08:30");
  await user.click(screen.getByRole("button", { name: "Move it" }));
  const patch = calls.find((call) => call.method === "PATCH");
  expect(patch?.ifMatch).toBe("3");
  expect(patch?.body).toEqual({
    scheduled_at: new Date("2026-09-02T08:30").toISOString(),
    scheduled_tz: readerZone,
  });
});

it("refuses to move a message to nowhere", async () => {
  const user = userEvent.setup();
  const { calls } = mount([WAITING]);
  await user.click(
    await screen.findByRole("button", { name: "Change moment" }),
  );
  await user.clear(screen.getByLabelText(/New moment for/));
  expect(
    screen.getByRole("button", { name: "Move it" }).hasAttribute("disabled"),
  ).toBe(true);
  expect(calls.some((call) => call.method === "PATCH")).toBe(false);
});

it("asks before taking a message back, and sends nothing if the reader declines", async () => {
  const user = userEvent.setup();
  const { calls } = mount([WAITING]);
  await user.click(await screen.findByRole("button", { name: "Withdraw" }));
  expect(screen.getByText(/will not be sent/)).toBeTruthy();
  await user.keyboard("{Escape}");
  expect(calls.some((call) => call.method === "POST")).toBe(false);
});

it("takes the message back on confirm", async () => {
  const user = userEvent.setup();
  const { calls } = mount([WAITING]);
  await user.click(await screen.findByRole("button", { name: "Withdraw" }));
  await user.click(screen.getByRole("button", { name: "Withdraw it" }));
  const cancel = calls.find((call) => call.method === "POST");
  expect(cancel?.path).toBe(`/v1/scheduled-sends/${WAITING.id}/cancel`);
});

it("tells the reader to read the list again when the row is not the row on the server", async () => {
  const user = userEvent.setup();
  mountRefusing([WAITING], "PATCH");
  await user.click(
    await screen.findByRole("button", { name: "Change moment" }),
  );
  await user.click(screen.getByRole("button", { name: "Move it" }));
  expect(await screen.findByText(/This list is out of date/)).toBeTruthy();
  expect(screen.getByRole("button", { name: "Read it again" })).toBeTruthy();
  // The raw server detail is the one thing that must not reach the reader: it
  // names a row version, which says nothing about what to do next.
  expect(screen.queryByText(/is not the current version/)).toBeNull();
});

it("says nothing is scheduled with one sentence, not three", async () => {
  mount([]);
  expect(
    await screen.findByText("You have not scheduled a message yet."),
  ).toBeTruthy();
  expect(screen.queryByRole("heading", { level: 2 })).toBeNull();
});
