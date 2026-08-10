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
import { InstallationSettingsCard } from "./installation-settings";

// Settings → Installation: the organization's name, reporting zone and base
// currency. Every role reads them; only installation_settings:update changes
// them, so the fields disable (never hide) for everyone else.
//
// The case worth proving beyond that is the base currency's lock: once deals
// have converted against it the server reports it frozen WITH a reason, and
// the field must go read-only showing that reason — otherwise an operator
// types a value they cannot save and learns why only from a 422.

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

const SETTINGS_EDITOR: GrantSpec = { installation_settings: ["update"] };

type BackendOptions = {
  locked?: boolean;
  lockedReason?: string;
};

// backendFor answers /me with the given grants and /installation/settings with
// the given state, capturing any PATCH body so the wire shape is assertable.
function backendFor(allow: GrantSpec, opts: BackendOptions = {}) {
  let state = {
    name: "Brandt Automotive",
    timezone: "Europe/Berlin",
    base_currency: "EUR",
    base_currency_locked: opts.locked ?? false,
    ...(opts.lockedReason
      ? { base_currency_locked_reason: opts.lockedReason }
      : {}),
  };
  let capturedPatch: unknown = null;
  const fetchMock = vi.fn(
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const req =
        input instanceof Request ? input : new Request(String(input), init);
      const url = new URL(req.url, "http://localhost");
      if (url.pathname.endsWith("/me")) {
        return jsonResponse(meFixture({ allow }));
      }
      if (url.pathname.endsWith("/installation/settings")) {
        if (req.method === "PATCH") {
          capturedPatch = await req.json();
          state = { ...state, ...(capturedPatch as object) };
        }
        return jsonResponse(state);
      }
      throw new Error(`unexpected request: ${req.method} ${url.pathname}`);
    },
  );
  return { fetchMock, patch: () => capturedPatch };
}

function render(node: ReactNode) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return rtlRender(
    <QueryClientProvider client={client}>
      <LocaleProvider>{node}</LocaleProvider>
    </QueryClientProvider>,
  );
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("InstallationSettingsCard", () => {
  it("shows the installation's values to a role that cannot change them, disabled", async () => {
    const { fetchMock } = backendFor({});
    vi.stubGlobal("fetch", fetchMock);

    render(<InstallationSettingsCard />);

    const name = (await screen.findByLabelText(
      /organization name/i,
    )) as HTMLInputElement;
    expect(name.value).toBe("Brandt Automotive");
    // Disabled, not hidden: a rep reading amounts benefits from knowing the
    // currency, and hiding it would buy nothing the server does not enforce.
    expect(name.disabled).toBe(true);
    const currency = (await screen.findByLabelText(
      /base currency/i,
    )) as HTMLInputElement;
    expect(currency.disabled).toBe(true);
    expect(screen.queryByRole("button", { name: /save/i })).toBeNull();
  });

  it("sends only the fields that changed", async () => {
    const { fetchMock, patch } = backendFor(SETTINGS_EDITOR);
    vi.stubGlobal("fetch", fetchMock);

    render(<InstallationSettingsCard />);

    const name = await screen.findByLabelText(/organization name/i);
    await userEvent.clear(name);
    await userEvent.type(name, "Brandt Group");
    await userEvent.click(screen.getByRole("button", { name: /save/i }));

    // The currency and zone were never touched, so they must not appear in the
    // patch: re-sending an unchanged base currency would ask the server to
    // write a value that may be frozen, for a field the operator never edited.
    await waitFor(() => expect(patch()).toEqual({ name: "Brandt Group" }));
  });

  it("renders the base currency read-only with the server's reason once it is locked", async () => {
    const reason =
      "3 deal(s) have already frozen a conversion rate against it, so changing the base would re-mean every roll-up built on them";
    const { fetchMock } = backendFor(SETTINGS_EDITOR, {
      locked: true,
      lockedReason: reason,
    });
    vi.stubGlobal("fetch", fetchMock);

    render(<InstallationSettingsCard />);

    const currency = (await screen.findByLabelText(
      /base currency/i,
    )) as HTMLInputElement;
    expect(currency.disabled).toBe(true);
    // The server's own sentence, not a generic "locked" — it names how much
    // history is at stake, which is what tells the operator why.
    expect(await screen.findByText(reason)).not.toBeNull();
    // The editor can still change everything else.
    const name = (await screen.findByLabelText(
      /organization name/i,
    )) as HTMLInputElement;
    expect(name.disabled).toBe(false);
  });

  // An editable field must LOOK editable. `.input` is what carries the border,
  // fill and padding that separate a control from the static text beside it, so
  // a field that renders without it reads as a caption: the value is there, the
  // affordance is not, and an operator concludes the setting is read-only. The
  // class is the observable proof the control came from the design system
  // rather than being hand-rolled again.
  it("renders every field as a design-system input", async () => {
    const { fetchMock } = backendFor(SETTINGS_EDITOR);
    vi.stubGlobal("fetch", fetchMock);

    render(<InstallationSettingsCard />);

    for (const label of [
      /organization name/i,
      /reporting timezone/i,
      /base currency/i,
    ]) {
      const control = await screen.findByLabelText(label);
      expect(control.className.split(/\s+/)).toContain("input");
    }
  });
});
