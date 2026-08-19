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
import { companyBackstop, jsonResponse, stubFetch } from "./company.fixtures";
import { CompanyScreen } from "./organizations";

// "Ask about this account" spent months reachable only from a test file: the
// panel existed, its round-trip worked, and no route rendered it. So the claim
// this file makes is deliberately the one mounting the component in isolation
// cannot make — that a reader who opens a company finds the questions on the
// page, and that pressing one answers there.

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  window.location.hash = "";
});

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

const answer = {
  organization_id: "o-1",
  question: "whats_open",
  generated_at: "2026-06-01T09:00:00Z",
  generated_by: "model",
  sentences: [
    {
      text: "Two open deals, worth about 57000 EUR.",
      evidence: [{ entity_type: "deal", entity_id: "d-1" }],
    },
  ],
};

describe("the company overview carries the ask surface", () => {
  it("offers the prepared questions on the page a reader opens the account to", async () => {
    stubFetch(companyBackstop);

    render(<CompanyScreen id="o-1" />);

    // The panel's own title, so this passes only while something on the
    // overview actually draws AssistantPanel — a question button alone would
    // still be found if the panel were mounted anywhere else on the record.
    expect(
      await screen.findByRole("heading", { name: "Ask about this account" }),
    ).toBeTruthy();
    expect(
      screen.getByRole("button", { name: "What's open here?" }),
    ).toBeTruthy();
  });

  it("answers the question it was asked, from the page", async () => {
    const user = userEvent.setup();
    let asked: unknown;
    stubFetch(async (url, method, request) => {
      if (method === "POST" && url.endsWith("/organizations/o-1/ask")) {
        asked = await request.json();
        return jsonResponse(answer);
      }
      return companyBackstop(url);
    });

    render(<CompanyScreen id="o-1" />);

    await user.click(
      await screen.findByRole("button", { name: "What's open here?" }),
    );

    await waitFor(() => expect(asked).toEqual({ question: "whats_open" }));
    expect(
      await screen.findByText("Two open deals, worth about 57000 EUR."),
    ).toBeTruthy();
  });
});
