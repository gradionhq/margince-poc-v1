/** @vitest-environment jsdom */
import "@testing-library/jest-dom/vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { LocaleProvider } from "../../i18n";
import { installFetchStub, jsonResponse } from "../story-utils";
import { ConnectAct } from "./connect-act";
import { initialConversationState } from "./conversation-machine";

// The Microsoft chip must open the SAME live OAuth panel as Google (no more
// disabled "Soon" badge), and the post-consent return view no longer depends
// on which chip was open when the redirect left — it reads the roster fresh.

function renderConnectAct(outcome?: string) {
  const state = {
    ...initialConversationState,
    act: "connect" as const,
    phase: "cn.consent" as const,
  };
  return render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <LocaleProvider initial="en">
        <ConnectAct
          state={state}
          dispatch={vi.fn()}
          persist={vi.fn(async () => true)}
          outcome={outcome}
        />
      </LocaleProvider>
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  vi.stubGlobal("scrollTo", vi.fn());
});
afterEach(() => vi.unstubAllGlobals());

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
