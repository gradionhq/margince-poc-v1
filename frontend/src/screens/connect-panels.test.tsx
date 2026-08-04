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
    render(<OAuthConnectPanel provider="gmail" onDismiss={() => {}} />);
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
  render(<OAuthConnectPanel provider="graph" onDismiss={() => {}} />);
  await userEvent.click(
    screen.getByRole("button", { name: "Allow access to my Microsoft" }),
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

// The roster is provider-ordered, so "the first connected OAuth row" is Gmail
// whenever a workspace holds both. A Microsoft consent that lands on Gmail
// offers to import the wrong mailbox — the import the human is about to
// approve must be the mailbox they just connected.
it("OAuthReturnPanel offers the import for the mailbox the consent returned for", async () => {
  const statusReads: string[] = [];
  const connection = (provider: string) => ({
    id: provider,
    provider,
    status: "connected",
    scopes: ["read"],
  });
  installFetchStub({
    "GET /connectors": () =>
      jsonResponse({ data: [connection("gmail"), connection("graph")] }),
    "GET /connectors/gmail/backfill": () => {
      statusReads.push("gmail");
      return jsonResponse({ state: "idle" });
    },
    "GET /connectors/graph/backfill": () => {
      statusReads.push("graph");
      return jsonResponse({ state: "idle" });
    },
  });
  render(
    <OAuthReturnPanel outcome="ok" provider="graph" onComplete={vi.fn()} />,
  );
  await screen.findByText("Live and capturing");
  await waitFor(() => expect(statusReads).toEqual(["graph"]));
});

// A provider whose consent succeeded at the provider but whose row never
// landed is a confirm failure, not an invitation to import somebody else's
// mailbox.
it("OAuthReturnPanel reports a confirm-failure when the returning provider is not connected", async () => {
  installFetchStub({
    "GET /connectors": () =>
      jsonResponse({
        data: [
          { id: "g1", provider: "gmail", status: "connected", scopes: [] },
        ],
      }),
  });
  render(
    <OAuthReturnPanel outcome="ok" provider="graph" onComplete={vi.fn()} />,
  );
  expect(
    await screen.findByText("We couldn't confirm the connection."),
  ).toBeTruthy();
});

// A segment this build cannot resolve to a provider is NOT the same as one that
// is absent. Absent is deploy skew and falls back to the roster; unresolved
// names no mailbox, and falling back there would offer to import an inbox the
// human never just connected — the very swap the exact match exists to prevent.
it("OAuthReturnPanel refuses to offer an import for an unrecognized provider segment", async () => {
  const statusReads: string[] = [];
  installFetchStub({
    "GET /connectors": () =>
      jsonResponse({
        data: [
          { id: "g1", provider: "gmail", status: "connected", scopes: [] },
        ],
      }),
    "GET /connectors/gmail/backfill": () => {
      statusReads.push("gmail");
      return jsonResponse({ state: "idle" });
    },
  });
  render(
    <OAuthReturnPanel outcome="ok" provider="bogus" onComplete={vi.fn()} />,
  );
  expect(
    await screen.findByText("We couldn't confirm the connection."),
  ).toBeTruthy();
  expect(screen.queryByText("Live and capturing")).toBeNull();
  // The backfill panel reads the run for the mailbox it is offered for; the
  // connected Gmail row must never be that mailbox here.
  expect(statusReads).toEqual([]);
});

it("OAuthReturnPanel reports a confirm-failure when no connection came back", async () => {
  installFetchStub({ "GET /connectors": () => jsonResponse({ data: [] }) });
  render(<OAuthReturnPanel outcome="ok" onComplete={vi.fn()} />);
  expect(
    await screen.findByText("We couldn't confirm the connection."),
  ).toBeTruthy();
});

// Onboarding is the DEFAULT return surface for a consent round-trip, so it sees
// the same server outcome enum Settings does. A permanent failure that only
// Settings knows about would fall through to this panel's generic advice —
// "try connecting again" — which is the one thing that cannot work here.
it("OAuthReturnPanel names the remedy when the provider's API is not enabled", async () => {
  installFetchStub({ "GET /connectors": () => jsonResponse({ data: [] }) });
  render(<OAuthReturnPanel outcome="misconfigured" onComplete={vi.fn()} />);
  expect(
    await screen.findByText(/administrator needs to enable it/i),
  ).toBeTruthy();
  expect(screen.queryByText(/try connecting again/i)).toBeNull();
});

it("OAuthReturnPanel tells the reader what to accept when the provider declined", async () => {
  installFetchStub({ "GET /connectors": () => jsonResponse({ data: [] }) });
  render(<OAuthReturnPanel outcome="rejected" onComplete={vi.fn()} />);
  expect(await screen.findByText(/accept every permission/i)).toBeTruthy();
  // Retrying IS the right advice once the permissions are accepted, so this copy
  // may say so — what it must not do is fall back to the generic panel text that
  // names no remedy at all.
  expect(screen.queryByText(/Head to Settings/i)).toBeNull();
});

// An outcome this panel does not know must still land on the honest generic
// failure rather than rendering nothing.
it("OAuthReturnPanel keeps the generic failure for an unrecognized outcome", async () => {
  installFetchStub({ "GET /connectors": () => jsonResponse({ data: [] }) });
  render(<OAuthReturnPanel outcome="something-new" onComplete={vi.fn()} />);
  expect(
    await screen.findByText(/couldn't confirm the connection/i),
  ).toBeTruthy();
});
