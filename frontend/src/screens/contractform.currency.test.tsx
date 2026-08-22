/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { components } from "../api/schema";
import { LocaleProvider } from "../i18n";
import { ContractForm } from "./contractform";

// This form has no currency control, so whatever currency it holds is written to
// the agreement unseen — and a written currency is not a display default that a
// reader can discount later: every roll-up, every conversion and every figure on
// the record page is built on it afterwards.
//
// So the unit can only ever be one somebody stated: the agreement's own, or the
// installation's declared base currency. Never a literal, which would label one
// deployment's agreements in another country's money and be indistinguishable
// from a currency an operator chose.
//
// The currency also decides the SCALE, so these cases pin both. They used to
// assert 500_000 minor units for 5000 dong: the form multiplied by a hundred
// whatever the currency, and this file — whose installation is deliberately a
// dong one — wrote the hundredfold figure down as expected.

const SETTINGS = {
  organization_name: "Brandt Automotive GmbH",
  timezone: "Europe/Berlin",
  base_currency: "VND",
  base_currency_locked: false,
  max_upload_bytes: 26_214_400,
};

// An agreement on record with no money on it — the only shape the record's own
// CHECK allows a currency-less contract to have.
const CONTRACT: components["schemas"]["Contract"] = {
  id: "c-1",
  organization_id: "o-1",
  title: "MSA 2026",
  status: "active",
  under_contract: true,
  auto_renew: false,
  value_basis: "total",
  version: 1,
  source: "manual",
  captured_by: "human:u-1",
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

// Every request the form makes, so the created agreement can be read off the
// wire rather than off a spy on our own helper.
function stubApi(settings: { body: unknown; status?: number }): Request[] {
  const seen: Request[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const request =
        input instanceof Request ? input : new Request(String(input), init);
      seen.push(request);
      const path = new URL(request.url).pathname;
      if (path.endsWith("/installation/settings")) {
        return jsonResponse(settings.body, settings.status);
      }
      if (path.includes("/contracts")) {
        return jsonResponse({ id: "c-1" });
      }
      return jsonResponse({ data: [] });
    }),
  );
  return seen;
}

function show(ui: ReactNode) {
  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <LocaleProvider initial="en">{ui}</LocaleProvider>
    </QueryClientProvider>,
  );
}

// Filling in the one agreement this suite records: a title, because the form
// refuses without one, and an amount, because an amount is what needs a unit.
async function recordAgreement(user: ReturnType<typeof userEvent.setup>) {
  await user.type(screen.getByLabelText(/^Title/), "MSA 2026");
  await user.type(screen.getByLabelText("Value"), "5000");
  await user.click(screen.getByRole("button", { name: "Record agreement" }));
}

async function writtenBody(seen: Request[], method: "POST" | "PATCH") {
  const writes = seen.filter(
    (r) =>
      r.method === method && new URL(r.url).pathname.includes("/contracts"),
  );
  expect(writes.length).toBe(1);
  return await writes[0].clone().json();
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("recording an agreement's value", () => {
  it("writes the installation's own currency, not one this file chose", async () => {
    const user = userEvent.setup();
    const seen = stubApi({ body: SETTINGS });
    show(<ContractForm orgId="o-1" open onClose={() => {}} />);
    await waitFor(() => expect(screen.getByLabelText(/^Title/)).toBeTruthy());

    await recordAgreement(user);

    await waitFor(async () =>
      expect(await writtenBody(seen, "POST")).toMatchObject({
        // 5000 dong is 5000 minor units. VND has no minor unit at all, so the
        // scale is 1 — this file's whole premise is a dong installation, and
        // it used to assert the hundredfold figure the form produced.
        value_minor: 5_000,
        currency: "VND",
      }),
    );
  });

  it("states the amount and no currency when the installation has not answered", async () => {
    const user = userEvent.setup();
    // The installation read is the only source of a unit here, so a form that
    // cannot reach it holds none. It sends the amount anyway: the server refuses
    // half a money pair where the reader can see the refusal, whereas dropping
    // the amount would report a saved agreement whose value went nowhere.
    const seen = stubApi({ body: { detail: "unavailable" }, status: 503 });
    show(<ContractForm orgId="o-1" open onClose={() => {}} />);
    await waitFor(() => expect(screen.getByLabelText(/^Title/)).toBeTruthy());

    await recordAgreement(user);

    await waitFor(async () => {
      const body = await writtenBody(seen, "POST");
      // No currency reached this form, so the amount is scaled at ISO's own
      // default of two digits — the same assumption the field made
      // unconditionally before. It is stated here rather than left implicit,
      // because it is the one path where the figure's unit is genuinely
      // unknown and the server is what refuses the half-pair.
      expect(body.value_minor).toBe(500_000);
      expect(body).not.toHaveProperty("currency");
    });
  });
});

describe("correcting an agreement's value", () => {
  it("keeps the currency the agreement already records", async () => {
    const user = userEvent.setup();
    const seen = stubApi({ body: SETTINGS });
    // A dollar agreement on an installation that reports in dong. Re-labelling
    // it would restate what was signed, so a correction to any other field
    // leaves the unit exactly as recorded.
    show(
      <ContractForm
        orgId="o-1"
        contract={{ ...CONTRACT, value_minor: 700_000, currency: "USD" }}
        open
        onClose={() => {}}
      />,
    );
    await waitFor(() => expect(screen.getByLabelText(/^Title/)).toBeTruthy());

    await user.click(screen.getByRole("button", { name: "Save changes" }));

    await waitFor(async () =>
      expect(await writtenBody(seen, "PATCH")).toMatchObject({
        value_minor: 700_000,
        currency: "USD",
      }),
    );
  });

  it("prices an agreement recorded without a value in the installation's currency", async () => {
    const user = userEvent.setup();
    const seen = stubApi({ body: SETTINGS });
    // The record's two money columns are paired, so an agreement carrying no
    // currency carries no amount either: it is being priced here for the first
    // time, which is the same act as pricing a new one.
    show(
      <ContractForm orgId="o-1" contract={CONTRACT} open onClose={() => {}} />,
    );
    await waitFor(() => expect(screen.getByLabelText(/^Title/)).toBeTruthy());

    await user.type(screen.getByLabelText("Value"), "5000");
    await user.click(screen.getByRole("button", { name: "Save changes" }));

    await waitFor(async () =>
      expect(await writtenBody(seen, "PATCH")).toMatchObject({
        value_minor: 5_000,
        currency: "VND",
      }),
    );
  });
});
