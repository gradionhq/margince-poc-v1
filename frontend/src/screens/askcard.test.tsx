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
import { AskSection } from "./company360";

// "Ask Margince" answers a prepared question from this account's own
// records. A permission refusal on it is a settled boundary (RBAC∩Passport),
// not a hiccup, and a successful answer this build cannot read must say so
// rather than silently reverting the panel to "never asked".

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function render(ui: ReactNode) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return rtlRender(
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">{ui}</LocaleProvider>
    </QueryClientProvider>,
  );
}

function stubAsk(responder: () => Response) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (request: Request) => {
      if (
        request.method === "POST" &&
        new URL(request.url).pathname.endsWith("/ask")
      ) {
        return responder();
      }
      return jsonResponse({});
    }),
  );
}

async function askWhatsOpen() {
  await userEvent.click(
    screen.getByRole("button", { name: "What's open here?" }),
  );
}

describe("AskSection — a permission refusal is not a retryable failure", () => {
  it("states the restricted boundary, with no retry wording, on permission_denied", async () => {
    stubAsk(() =>
      jsonResponse({ title: "Forbidden", code: "permission_denied" }, 403),
    );
    render(<AskSection orgId="o-1" enabled />);
    await askWhatsOpen();

    await waitFor(() =>
      expect(
        screen.getByText("Hidden — your role cannot read this"),
      ).toBeTruthy(),
    );
    expect(
      screen.queryByText(/could not be answered — try it again/),
    ).toBeNull();
  });

  it("keeps the retry sentence for every other failure", async () => {
    stubAsk(() => jsonResponse({ title: "Internal", detail: "boom" }, 500));
    render(<AskSection orgId="o-1" enabled />);
    await askWhatsOpen();

    await waitFor(() =>
      expect(
        screen.getByText(/could not be answered — try it again/),
      ).toBeTruthy(),
    );
    expect(
      screen.queryByText("Hidden — your role cannot read this"),
    ).toBeNull();
  });
});

describe("AskSection — a malformed answer is not silence", () => {
  it("says the answer could not be read, rather than reverting to never-asked", async () => {
    stubAsk(() =>
      jsonResponse({
        organization_id: "o-1",
        question: "whats_open",
        generated_at: "2026-06-01T09:00:00Z",
        generated_by: "deterministic",
        // No `sentences` array at all — a shape this build cannot read.
      }),
    );
    render(<AskSection orgId="o-1" enabled />);
    await askWhatsOpen();

    await waitFor(() =>
      expect(screen.getByText("That answer could not be read.")).toBeTruthy(),
    );
    // Distinct from the honest empty-answer state, which is a real outcome
    // rather than a shape this build failed to read.
    expect(
      screen.queryByText("Nothing here that you can see would answer that."),
    ).toBeNull();
  });

  it("still renders the honest empty answer when sentences genuinely has none", async () => {
    stubAsk(() =>
      jsonResponse({
        organization_id: "o-1",
        question: "whats_open",
        generated_at: "2026-06-01T09:00:00Z",
        generated_by: "deterministic",
        sentences: [],
      }),
    );
    render(<AskSection orgId="o-1" enabled />);
    await askWhatsOpen();

    await waitFor(() =>
      expect(
        screen.getByText("Nothing here that you can see would answer that."),
      ).toBeTruthy(),
    );
    expect(screen.queryByText("That answer could not be read.")).toBeNull();
  });
});
