/** @vitest-environment jsdom */
import "@testing-library/jest-dom/vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { LocaleProvider } from "../../i18n";
import { installFetchStub, jsonResponse } from "../story-utils";
import { ConnectAct } from "./connect-act";
import { initialConversationState } from "./conversation-machine";

// The Microsoft chip must open the SAME live OAuth panel as Google (no more
// disabled "Soon" badge), and the post-consent return view no longer depends
// on which chip was open when the redirect left — it reads the roster fresh.
//
// The act also owns the finish: the backread that follows a confirmed
// connection can be left running or declined, and either way the step
// completes exactly once — CONNECT_DONE is the only event this act dispatches
// and a second one would move the machine out from under the wizard.

function renderConnectAct(outcome?: string) {
  const state = {
    ...initialConversationState,
    act: "connect" as const,
    phase: "cn.consent" as const,
  };
  const dispatch = vi.fn();
  const persist = vi.fn(async () => true);
  const view = render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <LocaleProvider initial="en">
        <ConnectAct
          state={state}
          dispatch={dispatch}
          persist={persist}
          outcome={outcome}
        />
      </LocaleProvider>
    </QueryClientProvider>,
  );
  return { ...view, dispatch, persist };
}

const rosterWith = (backfill: Record<string, unknown>) => () =>
  jsonResponse({
    data: [
      {
        id: "g1",
        provider: "gmail",
        status: "connected",
        scopes: ["read"],
        backfill,
      },
    ],
  });

beforeEach(() => {
  vi.stubGlobal("scrollTo", vi.fn());
});
// Explicit, because auto-cleanup only runs with vitest globals enabled and
// this suite does not use them: without it each test inherits the previous
// render's DOM and every chip query finds several matches.
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

it("offers Microsoft as a live chip and opens its connect panel", async () => {
  installFetchStub({ "GET /connectors": () => jsonResponse({ data: [] }) });
  renderConnectAct();
  const ms = screen.getByRole("button", { name: "Microsoft" });
  expect(ms).not.toBeDisabled();
  await userEvent.click(ms);
  expect(
    await screen.findByRole("button", { name: "Connect Microsoft" }),
  ).toBeTruthy();
});

it("renders the provider-agnostic return view on OAuth return", async () => {
  installFetchStub({
    "GET /connectors": () =>
      jsonResponse({
        data: [
          {
            id: "g1",
            provider: "graph",
            status: "connected",
            scopes: ["read"],
            backfill: { state: "done" },
          },
        ],
      }),
  });
  renderConnectAct("ok");
  expect(await screen.findByText("Live and capturing")).toBeTruthy();
});

it("asks how far back to read once the mailbox is confirmed", async () => {
  installFetchStub({
    "GET /connectors": rosterWith({ state: "none" }),
    "POST /connectors/gmail/backfill/preview": () =>
      jsonResponse({
        window: "6m",
        estimated_messages: 900,
        computed_at: "2026-07-31T10:00:00Z",
      }),
  });
  renderConnectAct("ok");
  expect(
    await screen.findByRole("heading", {
      name: "How far back should I read?",
    }),
  ).toBeTruthy();
});

it("finishes the step once, leaving a running backread to the server", async () => {
  installFetchStub({
    "GET /connectors": rosterWith({
      state: "running",
      counts: { messages_scanned: 12 },
    }),
  });
  const { dispatch, persist } = renderConnectAct("ok");

  await userEvent.click(
    await screen.findByRole("button", { name: "Explore Margince meanwhile" }),
  );

  await waitFor(() => expect(dispatch).toHaveBeenCalledTimes(1));
  expect(dispatch).toHaveBeenCalledWith({ type: "CONNECT_DONE" });
  // The mailbox IS connected on this path, so the connect step was not skipped.
  expect(persist).toHaveBeenCalledWith(
    expect.objectContaining({ connectSkipped: false }),
  );
});

it("finishes the step once when the history read is declined", async () => {
  const starts: unknown[] = [];
  installFetchStub({
    "GET /connectors": rosterWith({ state: "none" }),
    "POST /connectors/gmail/backfill/preview": () =>
      jsonResponse({
        window: "6m",
        estimated_messages: 900,
        computed_at: "2026-07-31T10:00:00Z",
      }),
    "POST /connectors/gmail/backfill": (body) => {
      starts.push(body);
      return jsonResponse({ state: "queued" }, 202);
    },
  });
  const { dispatch } = renderConnectAct("ok");

  await userEvent.click(
    await screen.findByRole("button", { name: "Do not read history now" }),
  );

  await waitFor(() => expect(dispatch).toHaveBeenCalledTimes(1));
  expect(dispatch).toHaveBeenCalledWith({ type: "CONNECT_DONE" });
  expect(starts).toEqual([]);
});

// "Skip connecting" is offered while connecting is still the open question.
it("offers to skip connecting before any consent round trip", () => {
  installFetchStub({ "GET /connectors": () => jsonResponse({ data: [] }) });
  renderConnectAct();
  expect(
    screen.getByRole("button", { name: "Skip connecting for now" }),
  ).toBeTruthy();
});

// After a successful consent it is no longer true, and recording the step as
// skipped would persist a fact contradicted by the roster.
it("stops offering to skip connecting once consent has returned", async () => {
  installFetchStub({
    "GET /connectors": rosterWith({ state: "done" }),
  });
  renderConnectAct("ok");
  await screen.findByText("Live and capturing");
  expect(
    screen.queryByRole("button", { name: "Skip connecting for now" }),
  ).toBeNull();
});
