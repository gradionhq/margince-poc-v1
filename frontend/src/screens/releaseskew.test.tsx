/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { LocaleProvider } from "../i18n";
import { ReleaseSkewScreen, useSkewedApiRelease } from "./releaseskew";

// The gate, exercised the way App uses it: a probe answer goes in, and either the
// app renders or this screen does. The api's release is the only input that can
// block, so the cases below are the answers the probe can actually give.

// A component whose whole output IS the gate's decision, so a test reads the
// verdict rather than inferring it from which screen mounted.
function Gate({ mine }: Readonly<{ mine: string }>) {
  const skewed = useSkewedApiRelease(mine);
  return <p>{skewed === null ? "app renders" : `blocked on ${skewed}`}</p>;
}

function mount(mine: string, capabilities: unknown, status = 200) {
  vi.stubGlobal(
    "fetch",
    vi.fn(
      async () =>
        new Response(JSON.stringify(capabilities), {
          status,
          headers: { "Content-Type": "application/json" },
        }),
    ),
  );
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  render(
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">
        <Gate mine={mine} />
      </LocaleProvider>
    </QueryClientProvider>,
  );
}

const caps = (release?: string) => ({
  password: true,
  password_reset: false,
  oidc_providers: [],
  ...(release === undefined ? {} : { release_version: release }),
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

it("blocks the app when the api reports another release", async () => {
  mount("1970.41", caps("1970.42"));
  expect(await screen.findByText("blocked on 1970.42")).toBeTruthy();
});

it("renders the app when the api reports the same release", async () => {
  mount("1970.42", caps("1970.42"));
  await waitFor(() => {
    expect(screen.getByText("app renders")).toBeTruthy();
  });
});

// The three shapes of "we do not know", each of which would take a healthy
// installation down if the gate treated it as a difference. The pending case is
// the one that matters most in practice: it is true of every cold load, and a
// gate that blocked on it would flash this screen at every reader.
it("renders the app while the probe is still in flight", () => {
  mount("1970.41", caps("1970.42"));
  // Synchronously, before the fetch resolves — no await, deliberately.
  expect(screen.getByText("app renders")).toBeTruthy();
});

it("renders the app when the probe fails", async () => {
  mount("1970.41", { title: "Service Unavailable" }, 503);
  await waitFor(() => {
    expect(screen.getByText("app renders")).toBeTruthy();
  });
});

it("renders the app when the api reports no release at all", async () => {
  mount("1970.41", caps(undefined));
  await waitFor(() => {
    expect(screen.getByText("app renders")).toBeTruthy();
  });
});

it("names both releases and the way out", () => {
  render(
    <LocaleProvider initial="en">
      <ReleaseSkewScreen app="1970.41" server="1970.42" />
    </LocaleProvider>,
  );
  // The operator's two facts, on the page rather than in a console: without them
  // nobody can tell which of the two images is the odd one out.
  expect(screen.getByText("app 1970.41 · server 1970.42")).toBeTruthy();
  expect(screen.getByRole("button", { name: "Reload" })).toBeTruthy();
  // role=alert, because this replaces the app rather than decorating it: a
  // reader who cannot see the screen must still be told.
  expect(screen.getByRole("alert")).toBeTruthy();
});
