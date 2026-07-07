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
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider } from "../i18n";
import { OnboardingScreen } from "./onboarding";

// Onboarding honesty pins: cold-start fields render HUMAN labels (never raw
// snake_case keys), and the step-4 results tell the truth about a skipped
// voice step — the neutral-starter copy, not "drafts sound like you", with
// the canned sample draft visibly tagged as an illustrative example.

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  window.location.hash = "";
});

beforeEach(() => {
  vi.stubGlobal("scrollTo", vi.fn());
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

const coldstart = {
  fields: [
    {
      field: "legal_name",
      value: "Gradion",
      evidence_snippet: "© 2026 Gradion",
      source_url: "https://gradion.com",
      confidence: 0.9,
    },
    {
      field: "registered_address",
      value: "Munich",
      evidence_snippet: "Europe Munich",
      source_url: "https://gradion.com",
      confidence: 0.8,
    },
  ],
};

async function readBusiness() {
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => jsonResponse(coldstart)),
  );
  render(<OnboardingScreen />);
  await userEvent.type(
    screen.getByRole("textbox", { name: "Website" }),
    "gradion.com",
  );
  await userEvent.click(
    screen.getByRole("button", { name: /Read my business/ }),
  );
  await waitFor(() => expect(screen.getByText("Company name")).toBeTruthy());
}

describe("cold-start read-back labels", () => {
  it("renders the human label for every returned field, never the raw key", async () => {
    await readBusiness();
    expect(screen.getByText("Company name")).toBeTruthy();
    expect(screen.getByText("Registered address")).toBeTruthy();
    expect(screen.queryByText("legal_name")).toBeNull();
    expect(screen.queryByText("registered_address")).toBeNull();
  });

  it("carries the same human labels into the editable confirm step", async () => {
    await readBusiness();
    await userEvent.click(screen.getByRole("button", { name: /Continue/ }));
    expect(screen.getByLabelText(/Company name/)).toBeTruthy();
    expect(screen.getByLabelText(/Registered address/)).toBeTruthy();
  });
});

describe("connect step is skippable", () => {
  it("the mailbox-connect step offers a skip beside the connect CTA that exits to home", async () => {
    await readBusiness();
    await userEvent.click(screen.getByRole("button", { name: /Continue/ }));
    await userEvent.click(screen.getByRole("button", { name: /Continue/ }));
    await userEvent.click(
      screen.getByRole("button", { name: "Skip this step" }),
    );
    await userEvent.click(
      screen.getByRole("button", { name: /Connect my inbox/ }),
    );
    const skip = screen.getByRole("button", {
      name: /Skip for now — I'll connect later/,
    });
    await userEvent.click(skip);
    expect(window.location.hash).toBe("#/home");
  });
});

describe("step-4 honesty about the voice step", () => {
  it("a skipped voice step gets the neutral-starter copy and the example tag — not 'sounds like you'", async () => {
    await readBusiness();
    await userEvent.click(screen.getByRole("button", { name: /Continue/ }));
    await userEvent.click(screen.getByRole("button", { name: /Continue/ }));
    // now on step 3 (voice) — skip it
    await userEvent.click(
      screen.getByRole("button", { name: "Skip this step" }),
    );
    expect(screen.getByText(/You skipped the voice step/)).toBeTruthy();
    expect(
      screen.queryByText(/Drafts will sound like you from day one/),
    ).toBeNull();
    expect(screen.getByText("A sample draft")).toBeTruthy();
    expect(
      screen.getByText(/Illustrative example — not written from your data/),
    ).toBeTruthy();
  });
});
