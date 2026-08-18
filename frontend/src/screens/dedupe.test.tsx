/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider } from "../i18n";
import { DedupeScreen } from "./dedupe";

// The queue tells a reviewer what the detector saw. Each field carries one of
// three signals, and the reviewer's decision turns on which — so the signal has
// to be readable, and readable by more than colour: red text alone reaches
// nobody who cannot see the difference, and it left the other two signals told
// apart by nothing at all.

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

const jsonResponse = (body: unknown) =>
  new Response(JSON.stringify(body), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });

const render_ = (ui: ReactNode) =>
  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <LocaleProvider initial="en">{ui}</LocaleProvider>
    </QueryClientProvider>,
  );

function stubQueue(evidence: Record<string, unknown>[]) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: Request | string | URL) => {
      const url = String(input instanceof Request ? input.url : input);
      if (url.includes("/dedupe/candidates")) {
        return jsonResponse({
          data: [
            {
              id: "dc1",
              entity_type: "person",
              left_id: "l1",
              right_id: "r1",
              confidence: 0.87,
              evidence,
              status: "open",
            },
          ],
          page: { next_cursor: null },
        });
      }
      return jsonResponse({ data: [], page: { next_cursor: null } });
    }),
  );
}

describe("the duplicates queue names each signal in words", () => {
  it("distinguishes all three signals by their words, not only by colour", async () => {
    stubQueue([
      {
        field: "full_name",
        left_value: "A",
        right_value: "A",
        signal: "agree",
      },
      {
        field: "email",
        left_value: "a@x",
        right_value: "b@x",
        signal: "collide",
      },
      {
        field: "phone",
        left_value: "+49",
        right_value: null,
        signal: "one_sided",
      },
    ]);
    render_(<DedupeScreen />);
    expect(await screen.findByText("conflict")).toBeTruthy();
    expect(screen.getByText("agree")).toBeTruthy();
    expect(screen.getByText("one side only")).toBeTruthy();
  });

  // The wire types the field as a plain string, not a closed enum. A signal this
  // release has no word for is still one the detector acted on, so it renders as
  // itself rather than as a blank cell that reads as no signal.
  it("renders a signal it has no word for as the wire's own value", async () => {
    stubQueue([
      {
        field: "vat_id",
        left_value: "DE1",
        right_value: "DE1",
        signal: "normalised_match",
      },
    ]);
    render_(<DedupeScreen />);
    expect(await screen.findByText("normalised_match")).toBeTruthy();
  });
});
