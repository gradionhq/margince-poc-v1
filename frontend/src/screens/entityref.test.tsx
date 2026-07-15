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
import { EntityRef } from "./entityref";

// EntityRef (P-4 UUID-legibility): a cross-record reference resolves the
// target's id to its display name and backlinks to its 360, with the id as
// the honest fallback while loading or when the lookup can't resolve one.

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  window.location.hash = "";
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

describe("EntityRef", () => {
  it("renders the resolved name and backlinks to the record's 360 on click", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (request: Request) => {
        if (request.url.includes("/organizations/o-1")) {
          return jsonResponse({ id: "o-1", display_name: "Brandt GmbH" });
        }
        return jsonResponse({}, 404);
      }),
    );
    render(<EntityRef kind="organization" id="o-1" />);

    const link = await screen.findByRole("button", { name: "Brandt GmbH" });
    await userEvent.click(link);
    expect(window.location.hash).toBe("#/companies/o-1");
  });

  it("resolves a person to contacts/{id} and a deal to deals/{id}", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (request: Request) => {
        if (request.url.includes("/people/p-1")) {
          return jsonResponse({ id: "p-1", full_name: "Anna Weber" });
        }
        if (request.url.includes("/deals/d-1")) {
          return jsonResponse({ id: "d-1", name: "Q3 Renewal" });
        }
        return jsonResponse({}, 404);
      }),
    );
    const { rerender } = render(<EntityRef kind="person" id="p-1" />);
    await userEvent.click(
      await screen.findByRole("button", { name: "Anna Weber" }),
    );
    expect(window.location.hash).toBe("#/contacts/p-1");

    rerender(
      <QueryClientProvider client={new QueryClient()}>
        <LocaleProvider initial="en">
          <EntityRef kind="deal" id="d-1" />
        </LocaleProvider>
      </QueryClientProvider>,
    );
    await userEvent.click(
      await screen.findByRole("button", { name: "Q3 Renewal" }),
    );
    expect(window.location.hash).toBe("#/deals/d-1");
  });

  it("resolves a lead to leads/{id} (P-16: lead joins the ENTITY registry)", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (request: Request) => {
        if (request.url.includes("/leads/l-1")) {
          return jsonResponse({ id: "l-1", full_name: "Jordan Lee" });
        }
        return jsonResponse({}, 404);
      }),
    );
    render(<EntityRef kind="lead" id="l-1" />);
    await userEvent.click(
      await screen.findByRole("button", { name: "Jordan Lee" }),
    );
    expect(window.location.hash).toBe("#/leads/l-1");
  });

  it("falls back to the id (no link) when the lookup can't resolve a name", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => jsonResponse({}, 404)),
    );
    render(<EntityRef kind="organization" id="o-404" />);
    await waitFor(() => expect(screen.getByText("o-404")).toBeTruthy());
    expect(screen.queryByRole("button", { name: "o-404" })).toBeNull();
  });

  it("shows a dash and fetches nothing when the id is absent", () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    render(<EntityRef kind="person" id={null} />);
    expect(screen.getByText("—")).toBeTruthy();
    expect(fetchMock).not.toHaveBeenCalled();
  });
});
