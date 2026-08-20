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
import { type GrantSpec, meFixture } from "../app/mefixture";
import { LocaleProvider } from "../i18n";
import { MailSharingCard } from "./mail-sharing";

// The workspace mail-sharing posture (Settings → Connections): every role
// reads it, only capture_settings:update holders change it, and switching it
// off is a deliberate act — the danger callout and an explicit Save stand
// between the flip and the write.

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

const SETTINGS_EDITOR: GrantSpec = { capture_settings: ["update"] };

// backendFor answers /me with the given grants and /capture/settings with the
// given posture, capturing any PATCH body so the wire shape is assertable.
function backendFor(allow: GrantSpec, mailSharing = true) {
  let sharingState = mailSharing;
  let capturedPatch: unknown = null;
  const fetchMock = vi.fn(
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const req =
        input instanceof Request ? input : new Request(String(input), init);
      if (req.url.endsWith("/v1/me")) {
        return jsonResponse(meFixture({ allow }));
      }
      if (req.url.includes("/capture/settings")) {
        if (req.method === "PATCH") {
          capturedPatch = await req.json();
          sharingState = (capturedPatch as { mail_sharing: boolean })
            .mail_sharing;
        }
        return jsonResponse({ auto_enrich: true, mail_sharing: sharingState });
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

describe("MailSharingCard", () => {
  it("renders the stored ON posture with no warning and no Save", async () => {
    vi.stubGlobal("fetch", backendFor(SETTINGS_EDITOR, true).fetchMock);
    render(<MailSharingCard />);

    const toggle = await waitFor(() =>
      screen.getByTestId<HTMLButtonElement>("mail-sharing-toggle"),
    );
    expect(toggle.getAttribute("role")).toBe("switch");
    expect(toggle.getAttribute("aria-checked")).toBe("true");
    expect(toggle.disabled).toBe(false);
    // No unsaved change and nothing to warn about: the stored posture is ON.
    expect(screen.queryByText(/make usage of the CRM difficult/)).toBeNull();
    expect(screen.queryByRole("button", { name: "Save" })).toBeNull();
  });

  it("warns on flip-to-off and PATCHes only when Save is pressed", async () => {
    const backend = backendFor(SETTINGS_EDITOR, true);
    vi.stubGlobal("fetch", backend.fetchMock);
    render(<MailSharingCard />);

    const toggle = await waitFor(() =>
      screen.getByTestId<HTMLButtonElement>("mail-sharing-toggle"),
    );
    await userEvent.click(toggle);

    // The flip alone writes nothing — the cost is said out loud and the
    // change waits for an explicit Save.
    expect(
      await screen.findByText(/make usage of the CRM difficult/),
    ).toBeTruthy();
    expect(backend.getCapturedPatch()).toBeNull();

    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() =>
      expect(backend.getCapturedPatch()).toEqual({ mail_sharing: false }),
    );
    // Saved: the shown posture is the stored posture again, so Save retires.
    await waitFor(() =>
      expect(screen.queryByRole("button", { name: "Save" })).toBeNull(),
    );
  });

  it("disables the switch for a role without capture_settings update", async () => {
    vi.stubGlobal("fetch", backendFor({}, true).fetchMock);
    render(<MailSharingCard />);

    const toggle = await waitFor(() =>
      screen.getByTestId<HTMLButtonElement>("mail-sharing-toggle"),
    );
    expect(toggle.disabled).toBe(true);
    expect(screen.getByText(/Only an admin or ops/)).toBeTruthy();
  });
});
