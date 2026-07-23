/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render as rtlRender, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider } from "../i18n";
import { GoogleConnectPanel } from "./onboarding-connect-panels";

// The Google panel's pre-connect state must reassure a first-time user before
// the redirect: an unverified dev app shows Google's "unverified app" notice,
// and without a heads-up a founder abandons the flow there.

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
    render(<GoogleConnectPanel onComplete={async () => {}} />);
    expect(
      screen.getByText(/unverified app.*Advanced.*Continue/i),
    ).toBeTruthy();
    // The reassurance is honest about scope: read-only, never sends.
    expect(screen.getByText(/only ever reads/i)).toBeTruthy();
  });
});
