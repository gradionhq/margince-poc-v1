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
import { OfferTemplatesScreen } from "./offertemplates";

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

const template = {
  id: "t-1",
  workspace_id: "w",
  name: "Standard DE",
  locale: "de-DE",
  is_default: true,
  layout: {},
  version: 1,
  created_at: "2026-06-01T08:00:00Z",
  updated_at: "2026-06-01T08:00:00Z",
};

describe("OfferTemplatesScreen", () => {
  it("renders a template row with its locale and a default badge", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse({
          data: [template],
          page: { next_cursor: null, has_more: false },
        }),
      ),
    );
    render(<OfferTemplatesScreen />);
    expect(await screen.findByText("Standard DE")).toBeTruthy();
    expect(screen.getByText("de-DE")).toBeTruthy();
    expect(
      screen.getByText(
        (content, element) =>
          element?.tagName === "SPAN" && content === "Default for locale",
      ),
    ).toBeTruthy();
  });

  it("surfaces a 409 offer_template_default_conflict detail verbatim on create", async () => {
    const fetchMock = vi.fn(
      async (input: RequestInfo | URL, init?: RequestInit) => {
        const method =
          (input instanceof Request ? input.method : init?.method) ?? "GET";
        if (method === "POST") {
          return jsonResponse(
            {
              title: "conflict",
              detail: "a default template already exists for this locale",
              code: "offer_template_default_conflict",
            },
            409,
          );
        }
        return jsonResponse({
          data: [],
          page: { next_cursor: null, has_more: false },
        });
      },
    );
    vi.stubGlobal("fetch", fetchMock);
    render(<OfferTemplatesScreen />);
    await userEvent.click(await screen.findByTestId("new-record"));
    await waitFor(() => screen.getByLabelText(/Name/));
    await userEvent.type(screen.getByLabelText(/Name/), "Standard DE");
    await userEvent.click(screen.getByText("Create"));
    expect(
      await screen.findByText(
        "a default template already exists for this locale",
      ),
    ).toBeTruthy();
  });
});
