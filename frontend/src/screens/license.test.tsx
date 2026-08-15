/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  render as rtlRender,
  screen,
  waitFor,
} from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider } from "../i18n";
import { LicenseCard } from "./license";

// Settings → License: what the license grants and how much of it is used.
//
// The three readings this screen must keep apart, because collapsing any two of
// them tells an admin something untrue:
//
//   a seat cap        the meter reads used against granted
//   no seat cap       a license that limits nothing — no meter, and no "0"
//   over the cap      reported, with the workspace still working
//
// The second is the one a naive client gets wrong: `seats_granted` is absent
// rather than zero, and a screen rendering it as 0 would say the license permits
// nobody AND that every seat is over the limit.

type Entitlement = {
  state: "valid" | "absent" | "rejected";
  seats_used: number;
  over_limit: boolean;
  checked_at: string;
  seats_granted?: number;
  license?: {
    id: string;
    subject: string;
    expiry: string;
    in_grace: boolean;
    renewal_due: boolean;
    org?: string;
    contact_name?: string;
    contact_email?: string;
  };
};

// A licensee with every claim, a year from expiry. Tests that care about the
// seat meter take it as-is; tests about the licensee vary one field.
const HOLDER = {
  id: "0199c4f2-1d6e-7a41-9f0b-7b2a2c1d5e30",
  subject: "acme-prod",
  org: "Acme GmbH",
  contact_name: "Ada Lovelace",
  contact_email: "ada@acme.example",
  expiry: "2027-08-14T09:00:00Z",
  in_grace: false,
  renewal_due: false,
};

function backendFor(entitlement: Entitlement) {
  return vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const req =
      input instanceof Request ? input : new Request(String(input), init);
    const url = new URL(req.url, "http://localhost");
    if (url.pathname.endsWith("/installation/license")) {
      return new Response(JSON.stringify(entitlement), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    }
    throw new Error(`unexpected request: ${req.method} ${url.pathname}`);
  });
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

const checkedAt = "2026-08-14T09:00:00Z";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("LicenseCard", () => {
  it("reads used against granted when the license caps seats", async () => {
    vi.stubGlobal(
      "fetch",
      backendFor({
        state: "valid",
        seats_used: 9,
        seats_granted: 10,
        over_limit: false,
        checked_at: checkedAt,
      }),
    );
    render(<LicenseCard />);

    const meter = await waitFor(() => screen.getByRole("meter"));
    expect(meter.getAttribute("aria-valuenow")).toBe("9");
    expect(meter.getAttribute("aria-valuemax")).toBe("10");
    // A role="meter" takes no accessible name from the terms beside it, so the
    // reading has to be IN the name or a screen reader gets a bare number.
    expect(meter.getAttribute("aria-label")).toContain("9");
    expect(meter.getAttribute("aria-label")).toContain("10");
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("says a license with no seat count limits nothing, and draws no meter", async () => {
    vi.stubGlobal(
      "fetch",
      backendFor({
        state: "valid",
        seats_used: 40,
        over_limit: false,
        checked_at: checkedAt,
      }),
    );
    render(<LicenseCard />);

    expect(await waitFor(() => screen.getByText("No limit"))).toBeTruthy();
    // No meter, because there is no maximum to draw one against: a bar filled
    // against a limit nobody set would invent the limit.
    expect(screen.queryByRole("meter")).toBeNull();
    // And the count itself is still shown — the seats are known, only the cap
    // is not.
    expect(screen.getByText("40")).toBeTruthy();
    // Never rendered as a cap of zero, which would read as "permits nobody".
    expect(screen.queryByText("0")).toBeNull();
  });

  it("says an unlicensed installation is unlicensed rather than out of seats", async () => {
    vi.stubGlobal(
      "fetch",
      backendFor({
        state: "absent",
        seats_used: 12,
        over_limit: false,
        checked_at: checkedAt,
      }),
    );
    render(<LicenseCard />);

    expect(
      await waitFor(() => screen.getByText("No license configured")),
    ).toBeTruthy();
    expect(screen.queryByRole("meter")).toBeNull();
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("interrupts with the numbers and the way back when the installation is over its entitlement", async () => {
    vi.stubGlobal(
      "fetch",
      backendFor({
        state: "valid",
        seats_used: 11,
        seats_granted: 10,
        over_limit: true,
        checked_at: checkedAt,
      }),
    );
    render(<LicenseCard />);

    // `alert` rather than a quiet notice: being past the entitlement is
    // something the admin has to act on, not notice eventually.
    const alert = await waitFor(() => screen.getByRole("alert"));
    expect(alert.textContent).toContain("11");
    expect(alert.textContent).toContain("10");
    // The copy has to say the workspace keeps working, or an admin reads a
    // warning about seats as a lockout in progress (P7).
    expect(alert.textContent).toMatch(/nothing is blocked/i);
    // The meter still reads, clamped by the component rather than misreporting:
    // the value is the truth and the maximum is the entitlement.
    const meter = screen.getByRole("meter");
    expect(meter.getAttribute("aria-valuenow")).toBe("11");
    expect(meter.getAttribute("aria-valuemax")).toBe("10");
  });
});

describe("the licensee", () => {
  it("names the holder, the installation and the support reference", async () => {
    vi.stubGlobal(
      "fetch",
      backendFor({
        state: "valid",
        seats_used: 9,
        seats_granted: 10,
        over_limit: false,
        checked_at: checkedAt,
        license: HOLDER,
      }),
    );
    render(<LicenseCard />);

    expect(await waitFor(() => screen.getByText("Acme GmbH"))).toBeTruthy();
    expect(screen.getByText(/Ada Lovelace/)).toBeTruthy();
    expect(screen.getByText(/ada@acme.example/)).toBeTruthy();
    expect(screen.getByText("acme-prod")).toBeTruthy();
    expect(screen.getByText(HOLDER.id)).toBeTruthy();
  });

  // A license issued before those claims existed verifies like any other. Its
  // rows are absent, not empty: an empty row says something is missing from
  // THIS license rather than from the vocabulary it was issued under.
  it("renders no row for a claim the license does not carry", async () => {
    vi.stubGlobal(
      "fetch",
      backendFor({
        state: "valid",
        seats_used: 9,
        seats_granted: 10,
        over_limit: false,
        checked_at: checkedAt,
        license: {
          id: HOLDER.id,
          subject: HOLDER.subject,
          expiry: HOLDER.expiry,
          in_grace: false,
          renewal_due: false,
        },
      }),
    );
    render(<LicenseCard />);

    expect(await waitFor(() => screen.getByText("acme-prod"))).toBeTruthy();
    expect(screen.queryByText("Organization")).toBeNull();
    expect(screen.queryByText("Contact")).toBeNull();
  });

  it("asks for a renewal without interrupting when expiry is near", async () => {
    vi.stubGlobal(
      "fetch",
      backendFor({
        state: "valid",
        seats_used: 9,
        seats_granted: 10,
        over_limit: false,
        checked_at: checkedAt,
        license: { ...HOLDER, renewal_due: true },
      }),
    );
    render(<LicenseCard />);

    expect(
      await waitFor(() => screen.getByText("This license needs a renewal")),
    ).toBeTruthy();
    // Amber, not an alert: nothing has gone wrong yet.
    expect(screen.queryByRole("alert")).toBeNull();
  });

  // Past expiry and still accepted. This one interrupts, because the
  // installation will stop working.
  it("interrupts when the license runs on its grace period", async () => {
    vi.stubGlobal(
      "fetch",
      backendFor({
        state: "valid",
        seats_used: 9,
        seats_granted: 10,
        over_limit: false,
        checked_at: checkedAt,
        license: { ...HOLDER, in_grace: true, renewal_due: true },
      }),
    );
    render(<LicenseCard />);

    const alert = await waitFor(() => screen.getByRole("alert"));
    expect(alert.textContent).toMatch(/expired/i);
    expect(alert.textContent).toMatch(/still works/i);
    // One notice, not two: the grace state supersedes the renewal warning.
    expect(screen.queryByText("This license needs a renewal")).toBeNull();
  });

  it("shows no licensee card for an unlicensed installation", async () => {
    vi.stubGlobal(
      "fetch",
      backendFor({
        state: "absent",
        seats_used: 12,
        over_limit: false,
        checked_at: checkedAt,
      }),
    );
    render(<LicenseCard />);

    expect(
      await waitFor(() => screen.getByText("No license configured")),
    ).toBeTruthy();
    expect(screen.queryByText("Licensed to")).toBeNull();
  });
});
