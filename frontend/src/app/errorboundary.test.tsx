/** @vitest-environment jsdom */
import {
  QueryClient,
  QueryClientProvider,
  useQuery,
} from "@tanstack/react-query";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider } from "../i18n";
import { AppErrorBoundary } from "./errorboundary";

// Without a boundary the first throw in any screen takes the entire document
// with it, and the reader gets a white page. These cases are the difference
// between that and a view they can retry.

const THROWN = "cannot read properties of undefined";
const SHELL = "the routed shell";
const TITLE = "This view stopped working.";
const RETRY = "Try again";

let broken = true;

function Fragile() {
  if (broken) {
    throw new Error(THROWN);
  }
  return <p>{SHELL}</p>;
}

function renderBoundary(child: ReactNode) {
  return render(
    <LocaleProvider initial="en">
      <AppErrorBoundary>{child}</AppErrorBoundary>
    </LocaleProvider>,
  );
}

beforeEach(() => {
  broken = true;
  // React logs every error a boundary catches, with its component stack. The
  // throws below are the point of the suite, so that output is noise — and a
  // suite whose output is noise is one nobody reads when it matters.
  vi.spyOn(console, "error").mockImplementation(() => {});
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("the app-level error boundary", () => {
  it("replaces a thrown render with a surface that says what to do", () => {
    renderBoundary(<Fragile />);

    expect(screen.getByRole("alert").textContent).toContain(TITLE);
    expect(screen.getByRole("button", { name: RETRY })).toBeTruthy();
    expect(screen.queryByText(SHELL)).toBeNull();
  });

  it("shows the reader nothing of the error itself", () => {
    renderBoundary(<Fragile />);

    expect(document.body.textContent).not.toContain(THROWN);
    expect(screen.queryByText(/Error|undefined|at Fragile/)).toBeNull();
  });

  it("renders the screen again when the reader retries", async () => {
    const user = userEvent.setup();
    renderBoundary(<Fragile />);

    broken = false;
    await user.click(screen.getByRole("button", { name: RETRY }));

    expect(await screen.findByText(SHELL)).toBeTruthy();
    expect(screen.queryByText(TITLE)).toBeNull();
  });

  // The pairing with the query cache's reset boundary is what makes the retry
  // real: a query keeps its failed result, so without the reset the button
  // would clear the boundary's state and be handed the same error back.
  it("clears a failed query on retry, so the data is fetched again", async () => {
    const user = userEvent.setup();
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });

    function FragileQuery() {
      const { data } = useQuery({
        queryKey: ["boundary-pairing"],
        queryFn: () => {
          if (broken) {
            throw new Error(THROWN);
          }
          return Promise.resolve(SHELL);
        },
        throwOnError: true,
      });
      return <p>{data}</p>;
    }

    renderBoundary(
      <QueryClientProvider client={client}>
        <FragileQuery />
      </QueryClientProvider>,
    );

    expect(await screen.findByRole("button", { name: RETRY })).toBeTruthy();

    broken = false;
    await user.click(screen.getByRole("button", { name: RETRY }));

    expect(await screen.findByText(SHELL)).toBeTruthy();
  });
});
