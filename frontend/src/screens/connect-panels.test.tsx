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
import {
  OAuthConnectPanel,
  OAuthReturnPanel,
} from "./onboarding-connect-panels";
import { installFetchStub, jsonResponse } from "./story-utils";

// The Google panel's pre-connect state must reassure a first-time user before
// the redirect: an unverified dev app shows Google's "unverified app" notice,
// and without a heads-up a founder abandons the flow there. Both the OAuth
// connect panel (provider-parametrized) and the provider-agnostic return
// view share this file's harness.

function render(ui: ReactNode) {
  return rtlRender(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <LocaleProvider initial="en">{ui}</LocaleProvider>
    </QueryClientProvider>,
  );
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("the Google connect panel", () => {
  it("warns about the unverified-app notice and how to get past it", () => {
    render(<OAuthConnectPanel provider="gmail" onComplete={async () => {}} />);
    expect(
      screen.getByText(/unverified app.*Advanced.*Continue/i),
    ).toBeTruthy();
    // The reassurance is honest about scope: read-only, never sends.
    expect(screen.getByText(/only ever reads/i)).toBeTruthy();
  });
});

it("OAuthConnectPanel posts the given provider and redirects", async () => {
  const assign = vi.fn();
  vi.stubGlobal("location", { ...globalThis.location, assign });
  installFetchStub({
    "POST /connectors/graph/connect": () =>
      jsonResponse({ authorize_url: "https://login.microsoftonline/x" }),
  });
  render(<OAuthConnectPanel provider="graph" onComplete={vi.fn()} />);
  await userEvent.click(
    screen.getByRole("button", { name: "Connect Microsoft" }),
  );
  await waitFor(() =>
    expect(assign).toHaveBeenCalledWith("https://login.microsoftonline/x"),
  );
});

it("OAuthReturnPanel shows the live OAuth mailbox after consent", async () => {
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
  render(<OAuthReturnPanel outcome="ok" onComplete={vi.fn()} />);
  expect(await screen.findByText("Live and capturing")).toBeTruthy();
});

it("OAuthReturnPanel reports a confirm-failure when no connection came back", async () => {
  installFetchStub({ "GET /connectors": () => jsonResponse({ data: [] }) });
  render(<OAuthReturnPanel outcome="ok" onComplete={vi.fn()} />);
  expect(
    await screen.findByText("We couldn't confirm the connection."),
  ).toBeTruthy();
});
