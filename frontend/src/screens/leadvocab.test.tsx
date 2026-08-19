/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { type GrantSpec, meFixture } from "../app/mefixture";
import { LocaleProvider } from "../i18n";
import {
  LeadDisqualifyReasonsCard,
  LeadHandlingCard,
  LeadSourcesCard,
} from "./leadvocab";

// Settings › Data model: the lead vocabularies and the lead-handling posture.
// Every role reads them; the custom_field write verbs decide who may change
// them, and the cards disable rather than hide.

const ADMIN: GrantSpec = {
  custom_field: ["read", "create", "update", "delete"],
};
const READER: GrantSpec = { custom_field: ["read"] };

function source(
  key: string,
  label: string,
  extra: Partial<{
    system: boolean;
    lead_count: number;
    active: boolean;
    intent: string;
  }> = {},
) {
  return {
    id: `src-${key}`,
    key,
    label,
    intent: "neutral",
    sort_order: 10,
    active: true,
    system: false,
    lead_count: 0,
    version: 1,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    ...extra,
  };
}

type Call = { url: string; method: string; body: unknown };

function backend(allow: GrantSpec, calls: Call[] = []) {
  return vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const request = input instanceof Request ? input : null;
    const url = String(request ? request.url : input);
    const method = request ? request.method : (init?.method ?? "GET");
    const raw = request ? await request.text() : String(init?.body ?? "");
    calls.push({ url, method, body: raw ? JSON.parse(raw) : undefined });
    let body: unknown;
    let status = 200;
    if (url.endsWith("/v1/me")) {
      body = meFixture({ allow });
    } else if (url.includes("/lead-sources") && method === "GET") {
      body = {
        data: [
          source("manual", "Created manually", { system: true, lead_count: 3 }),
          source("trade_show", "Trade show", { intent: "high" }),
        ],
        discovered: [{ key: "connector:apollo", lead_count: 7 }],
      };
    } else if (url.includes("/lead-sources") && method === "DELETE") {
      body = null;
      status = 204;
    } else if (url.includes("/lead-sources")) {
      body = source("trade_show", "Messe");
    } else if (url.includes("/lead-disqualify-reasons") && method === "GET") {
      body = {
        data: [
          { ...source("r1", "Bad timing", { system: true, lead_count: 2 }) },
          { ...source("r2", "Went quiet") },
        ],
      };
    } else if (url.includes("/lead-disqualify-reasons")) {
      body = source("r2", "Went quiet");
    } else if (url.endsWith("/leads/settings")) {
      body = {
        first_response_enabled: false,
        first_response_target_minutes: 240,
      };
    }
    return new Response(status === 204 ? null : JSON.stringify(body), {
      status,
      headers: { "Content-Type": "application/json" },
    });
  });
}

function Providers({ children }: { children: ReactNode }) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return (
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">{children}</LocaleProvider>
    </QueryClientProvider>
  );
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("LeadSourcesCard", () => {
  it("lists the administered sources with their counts, marks built-ins, and offers removal only where the server would allow it", async () => {
    vi.stubGlobal("fetch", backend(ADMIN));
    render(
      <Providers>
        <LeadSourcesCard />
      </Providers>,
    );
    await waitFor(() =>
      expect(screen.getByDisplayValue("Created manually")).toBeTruthy(),
    );
    expect(screen.getByText("built-in")).toBeTruthy();
    expect(screen.getByText("3 leads")).toBeTruthy();
    // The built-in, in-use source says "switch off instead"; the unused
    // custom one gets the Remove button.
    const manual = screen.getByTestId("lead-source-manual");
    expect(manual.textContent).toContain("switch off instead");
    const trade = screen.getByTestId("lead-source-trade_show");
    expect(within(trade).getByRole("button", { name: "Remove" })).toBeTruthy();
    expect(within(manual).queryByRole("button", { name: "Remove" })).toBeNull();
  });

  it("renames on Enter and re-weights through the intent select, one PATCH each", async () => {
    const calls: Call[] = [];
    vi.stubGlobal("fetch", backend(ADMIN, calls));
    render(
      <Providers>
        <LeadSourcesCard />
      </Providers>,
    );
    const input = (await screen.findByDisplayValue(
      "Trade show",
    )) as HTMLInputElement;
    await userEvent.clear(input);
    await userEvent.type(input, "Messe{Enter}");
    await waitFor(() =>
      expect(
        calls.some(
          (c) =>
            c.method === "PATCH" &&
            c.url.endsWith("/lead-sources/src-trade_show") &&
            JSON.stringify(c.body) === JSON.stringify({ label: "Messe" }),
        ),
      ).toBe(true),
    );
    // The select is a combobox: open it, pick the option.
    await userEvent.click(
      screen.getByRole("combobox", { name: "Intent of Trade show" }),
    );
    await userEvent.click(screen.getByRole("option", { name: "Low interest" }));
    await waitFor(() =>
      expect(
        calls.some(
          (c) =>
            c.method === "PATCH" &&
            c.url.endsWith("/lead-sources/src-trade_show") &&
            JSON.stringify(c.body) === JSON.stringify({ intent: "low" }),
        ),
      ).toBe(true),
    );
  });

  it("adds a source with a label and intent, and adopts a discovered connector family", async () => {
    const calls: Call[] = [];
    vi.stubGlobal("fetch", backend(ADMIN, calls));
    render(
      <Providers>
        <LeadSourcesCard />
      </Providers>,
    );
    await userEvent.type(
      await screen.findByTestId("lead-source-new-label"),
      "Webinar",
    );
    await userEvent.click(screen.getByRole("button", { name: "Add source" }));
    await waitFor(() =>
      expect(
        calls.some(
          (c) =>
            c.method === "POST" &&
            c.url.endsWith("/lead-sources") &&
            JSON.stringify(c.body) ===
              JSON.stringify({ label: "Webinar", intent: "neutral" }),
        ),
      ).toBe(true),
    );
    expect(screen.getByText("7 leads")).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: "Add to list" }));
    await waitFor(() =>
      expect(
        calls.some(
          (c) =>
            c.method === "POST" &&
            c.url.endsWith("/lead-sources") &&
            (c.body as { key?: string }).key === "connector:apollo",
        ),
      ).toBe(true),
    );
  });

  it("leaves every control inert for a reader and says why", async () => {
    vi.stubGlobal("fetch", backend(READER));
    render(
      <Providers>
        <LeadSourcesCard />
      </Providers>,
    );
    const input = (await screen.findByDisplayValue(
      "Trade show",
    )) as HTMLInputElement;
    expect(input.disabled).toBe(true);
    expect(
      screen.getByText("Only an admin or ops seat changes this list."),
    ).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Add source" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Remove" })).toBeNull();
  });
});

describe("LeadDisqualifyReasonsCard", () => {
  it("lists the reasons and keeps the built-in, in-use one from removal", async () => {
    vi.stubGlobal("fetch", backend(ADMIN));
    render(
      <Providers>
        <LeadDisqualifyReasonsCard />
      </Providers>,
    );
    await waitFor(() =>
      expect(screen.getByDisplayValue("Bad timing")).toBeTruthy(),
    );
    expect(screen.getByTestId("lead-reason-src-r1").textContent).toContain(
      "switch off instead",
    );
    expect(
      within(screen.getByTestId("lead-reason-src-r2")).getByRole("button", {
        name: "Remove",
      }),
    ).toBeTruthy();
  });
});

describe("LeadHandlingCard", () => {
  it("shows the target off by default, flips it through one PATCH, and keeps the minutes field inert while off", async () => {
    const calls: Call[] = [];
    vi.stubGlobal("fetch", backend(ADMIN, calls));
    render(
      <Providers>
        <LeadHandlingCard />
      </Providers>,
    );
    const toggle = await screen.findByTestId("lead-first-response-switch");
    expect(toggle.getAttribute("aria-checked")).toBe("false");
    const minutes = screen.getByTestId(
      "lead-first-response-target",
    ) as HTMLInputElement;
    expect(minutes.disabled).toBe(true);
    expect(minutes.value).toBe("240");
    await userEvent.click(toggle);
    await waitFor(() =>
      expect(
        calls.some(
          (c) =>
            c.method === "PATCH" &&
            c.url.endsWith("/leads/settings") &&
            JSON.stringify(c.body) ===
              JSON.stringify({ first_response_enabled: true }),
        ),
      ).toBe(true),
    );
  });

  it("refuses the flip for a reader and says why", async () => {
    vi.stubGlobal("fetch", backend(READER));
    render(
      <Providers>
        <LeadHandlingCard />
      </Providers>,
    );
    const toggle = (await screen.findByTestId(
      "lead-first-response-switch",
    )) as HTMLButtonElement;
    expect(toggle.disabled).toBe(true);
    expect(
      screen.getByText("Only an admin or ops seat changes this list."),
    ).toBeTruthy();
  });
});
