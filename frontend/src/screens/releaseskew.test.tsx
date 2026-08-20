/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { useAuthCapabilities } from "../app/capabilities";
import { LocaleProvider } from "../i18n";
import { ReleaseSkewScreen, useSkewedApiRelease } from "./releaseskew";

// The gate, exercised the way App uses it: a probe answer goes in, and either the
// app renders or this screen does.
//
// EVERY "the app renders" ASSERTION NAMES THE PROBE'S STATE, and that is the
// point of the harness below rather than a detail of it. "The app renders" is
// also true before the probe has answered anything, so an assertion that only
// looked for it would pass while the probe was still in flight — and would keep
// passing if the comparison were deleted outright. So the component reports both
// facts, and each test says which settlement it means.
function Gate({ mine }: Readonly<{ mine: string }>) {
  const skewed = useSkewedApiRelease(mine);
  // The same query key the gate reads, so this shares one fetch with it rather
  // than making a second.
  const probe = useAuthCapabilities();
  const settled = probe.isSuccess || probe.isError;
  return (
    <p>
      {`settled:${settled} ${skewed === null ? "app renders" : `blocked on ${skewed}`}`}
    </p>
  );
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

// Set by the reload test alone, because window.location has to be put back by
// hand — vi.unstubAllGlobals does not reach a property defined with
// Object.defineProperty.
let restoreLocation: (() => void) | null = null;

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  restoreLocation?.();
  restoreLocation = null;
});

it("blocks the app when the api reports another release", async () => {
  mount("1970.41", caps("1970.42"));
  expect(
    await screen.findByText("settled:true blocked on 1970.42"),
  ).toBeTruthy();
});

it("renders the app when the api reports the same release", async () => {
  mount("1970.42", caps("1970.42"));
  expect(await screen.findByText("settled:true app renders")).toBeTruthy();
});

// The three shapes of "we do not know", each of which would take a healthy
// installation down if the gate treated it as a difference. Each waits for the
// probe to SETTLE first, so none of them can pass on the pending state.

// The probe's own retry is a TIMER, and this is the one test that has to outlast
// it: useAuthCapabilities retries once, so the query reaches its error state only
// after react-query's backoff (1s for the first attempt). Fake timers skip that
// deterministically. Waiting it out on the real clock would be a wall-clock
// dependency in a suite that shares a scheduler with 248 other files.
it("renders the app when the probe fails", async () => {
  vi.useFakeTimers();
  try {
    mount("1970.41", { title: "Service Unavailable" }, 503);
    await vi.advanceTimersByTimeAsync(2000);
    expect(screen.getByText("settled:true app renders")).toBeTruthy();
  } finally {
    vi.useRealTimers();
  }
});

it("renders the app when the api reports no release at all", async () => {
  mount("1970.41", caps(undefined));
  expect(await screen.findByText("settled:true app renders")).toBeTruthy();
});

// The pending case, asserted as pending rather than by omission. It is true of
// every cold load, and a gate that blocked on it would flash this screen at every
// reader.
it("renders the app while the probe is still in flight", () => {
  mount("1970.41", caps("1970.42"));
  expect(screen.getByText("settled:false app renders")).toBeTruthy();
});

it("names both releases and the way out", () => {
  render(
    <LocaleProvider initial="en">
      <ReleaseSkewScreen app="1970.41" server="1970.42" />
    </LocaleProvider>,
  );
  // The operator's two facts, on the page rather than in a console: without them
  // nobody can tell which of the two releases is the odd one out.
  expect(screen.getByText("app 1970.41 · server 1970.42")).toBeTruthy();
  // role=alert, because this replaces the app rather than decorating it: a
  // reader who cannot see the screen must still be told.
  expect(screen.getByRole("alert")).toBeTruthy();
});

// Reload is the whole remedy the screen offers, so a test that only proved the
// button exists proved nothing that a missing onClick would fail.
//
// The whole location object is replaced, not just its `reload`: jsdom defines
// `reload` non-configurable, so a spy on it throws. The replacement keeps every
// other field, and afterEach puts the original back — otherwise the next file to
// read an origin would inherit this one's stub.
it("reloads the document when Reload is pressed", async () => {
  const real = window.location;
  const reload = vi.fn();
  Object.defineProperty(window, "location", {
    configurable: true,
    writable: true,
    value: { ...real, reload },
  });
  restoreLocation = () =>
    Object.defineProperty(window, "location", {
      configurable: true,
      writable: true,
      value: real,
    });
  const user = userEvent.setup();
  render(
    <LocaleProvider initial="en">
      <ReleaseSkewScreen app="1970.41" server="1970.42" />
    </LocaleProvider>,
  );
  await user.click(screen.getByRole("button", { name: "Reload" }));
  expect(reload).toHaveBeenCalledTimes(1);
});
