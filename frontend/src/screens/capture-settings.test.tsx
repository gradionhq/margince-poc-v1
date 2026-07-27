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
import { CaptureSettingsCard } from "./capture-settings";

// The Settings → Integrations capture-settings toggle: reads the auto-enrich
// posture for every role, but only admin/ops can change it — the server stays
// the RBAC authority and the client mirrors it by disabling (never hiding) the
// toggle for other roles.

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

// backendFor answers /me with the given roles and /capture/settings with the
// given auto_enrich, capturing any PATCH body so the wire shape is assertable.
function backendFor(roles: string[], autoEnrich = true) {
  let autoState = autoEnrich;
  let capturedPatch: unknown = null;
  const fetchMock = vi.fn(
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const req =
        input instanceof Request ? input : new Request(String(input), init);
      if (req.url.endsWith("/v1/me")) {
        return jsonResponse({
          user: { email: "p@acme.test" },
          roles,
          teams: [],
        });
      }
      if (req.url.includes("/capture/settings")) {
        if (req.method === "PATCH") {
          capturedPatch = await req.json();
          autoState = (capturedPatch as { auto_enrich: boolean }).auto_enrich;
        }
        return jsonResponse({ auto_enrich: autoState });
      }
      throw new Error(`unexpected request: ${req.method} ${req.url}`);
    },
  );
  return { fetchMock, getCapturedPatch: () => capturedPatch };
}

const render = (ui: ReactNode) => {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return rtlRender(
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">{ui}</LocaleProvider>
    </QueryClientProvider>,
  );
};

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("CaptureSettingsCard", () => {
  it("shows the current posture and an enabled toggle for admin", async () => {
    vi.stubGlobal("fetch", backendFor(["admin"], true).fetchMock);
    render(<CaptureSettingsCard />);

    const toggle = await waitFor(() =>
      screen.getByTestId<HTMLInputElement>("capture-auto-enrich-toggle"),
    );
    expect(toggle.checked).toBe(true);
    expect(toggle.disabled).toBe(false);
  });

  it("disables the toggle for a non-admin role", async () => {
    vi.stubGlobal("fetch", backendFor(["rep"], true).fetchMock);
    render(<CaptureSettingsCard />);

    const toggle = await waitFor(() =>
      screen.getByTestId<HTMLInputElement>("capture-auto-enrich-toggle"),
    );
    expect(toggle.disabled).toBe(true);
    expect(screen.getByText(/Only an admin or ops/)).toBeTruthy();
  });

  it("PATCHes the new value when admin toggles it off", async () => {
    const backend = backendFor(["ops"], true);
    vi.stubGlobal("fetch", backend.fetchMock);
    render(<CaptureSettingsCard />);

    const toggle = await waitFor(() =>
      screen.getByTestId<HTMLInputElement>("capture-auto-enrich-toggle"),
    );
    await userEvent.click(toggle);

    await waitFor(() =>
      expect(backend.getCapturedPatch()).toEqual({ auto_enrich: false }),
    );
  });
});
