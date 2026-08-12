// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  render as rtlRender,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { type GrantSpec, meFixture } from "../app/mefixture";
import { LocaleProvider } from "../i18n";
import { OwnDomainsCard } from "./own-domains";

// Settings → Capture: the domains this installation treats as its own. Two
// lists with two different owners share the screen — the company profile claims
// the first and this surface cannot touch them, the second is curated here — so
// each states its own heading and only the curated one carries controls. A
// remove button beside a domain nobody can remove here is the defect this
// separation exists to prevent.

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

const CAPTURE_EDITOR: GrantSpec = { capture_settings: ["update"] };

type BackendOptions = {
  anchors?: string[];
  domains?: {
    domain: string;
    source: "admin" | "mailbox";
    verified: boolean;
  }[];
};

// backendFor answers /me with the given grants and /capture/email-domains with
// the given lists, capturing any POST body so the wire shape is assertable.
function backendFor(allow: GrantSpec, opts: BackendOptions = {}) {
  const state = {
    data: opts.domains ?? [],
    anchor_domains: opts.anchors ?? [],
  };
  let capturedPost: unknown = null;
  const fetchMock = vi.fn(
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const req =
        input instanceof Request ? input : new Request(String(input), init);
      const url = new URL(req.url, "http://localhost");
      if (url.pathname.endsWith("/me")) {
        return jsonResponse(meFixture({ allow }));
      }
      if (url.pathname.endsWith("/capture/email-domains")) {
        if (req.method === "POST") {
          capturedPost = await req.json();
          return jsonResponse(
            { domain: "brandt.de", source: "admin", verified: true },
            201,
          );
        }
        return jsonResponse(state);
      }
      throw new Error(`unexpected request: ${req.method} ${url.pathname}`);
    },
  );
  return { fetchMock, post: () => capturedPost };
}

function render(node: ReactNode) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return rtlRender(
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">{node}</LocaleProvider>
    </QueryClientProvider>,
  );
}

// The card a testid sits in, read through the DOM rather than by index: an
// assertion that counts sections passes for the wrong reason the moment a card
// is reordered.
async function cardOf(testId: string): Promise<HTMLElement> {
  const card = (await screen.findByTestId(testId)).closest("section");
  if (!card) {
    throw new Error(`${testId} is not inside a card`);
  }
  return card;
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("OwnDomainsCard", () => {
  it("puts the company-claimed domains and the curated ones in separate cards", async () => {
    const { fetchMock } = backendFor(CAPTURE_EDITOR, {
      anchors: ["brandt-automotive.de"],
      domains: [{ domain: "brandt.de", source: "admin", verified: true }],
    });
    vi.stubGlobal("fetch", fetchMock);

    render(<OwnDomainsCard />);

    const company = await cardOf("own-domains-from-company");
    expect(
      within(company).getByRole("heading", { name: /company domains/i }),
    ).toBeTruthy();
    expect(within(company).getByText("brandt-automotive.de")).toBeTruthy();
    // Read-only means read-only: nothing in this card offers to change a list
    // the company profile owns.
    expect(within(company).queryByRole("button")).toBeNull();
    expect(within(company).queryByRole("textbox")).toBeNull();

    const curated = await cardOf("own-domains-list");
    expect(
      within(curated).getByRole("heading", { name: /own email domains/i }),
    ).toBeTruthy();
    // The note about what registering a domain does travels with the acts it
    // describes, which only this card offers.
    expect(
      within(curated).getByText(/takes effect from the next message/i),
    ).toBeTruthy();
    expect(
      within(curated).getByRole("button", { name: /remove brandt\.de/i }),
    ).toBeTruthy();
    expect(within(curated).getByLabelText(/add an own domain/i)).toBeTruthy();
  });

  it("shows no company card when the company profile claims no domain", async () => {
    const { fetchMock } = backendFor(CAPTURE_EDITOR);
    vi.stubGlobal("fetch", fetchMock);

    render(<OwnDomainsCard />);

    // The empty curated list still states itself; a card whose whole content
    // would be an empty read-only list says nothing worth a heading.
    expect(await screen.findByTestId("own-domains-empty")).toBeTruthy();
    expect(
      screen.queryByRole("heading", { name: /company domains/i }),
    ).toBeNull();
  });

  it("adds a domain through the curated card's form", async () => {
    const { fetchMock, post } = backendFor(CAPTURE_EDITOR);
    vi.stubGlobal("fetch", fetchMock);

    render(<OwnDomainsCard />);

    const field = await screen.findByLabelText(/add an own domain/i);
    await userEvent.type(field, "brandt.de");
    await userEvent.click(screen.getByRole("button", { name: /^add$/i }));

    await waitFor(() => expect(post()).toEqual({ domain: "brandt.de" }));
  });

  it("disables the controls for a role that cannot change the list", async () => {
    const { fetchMock } = backendFor(
      {},
      { domains: [{ domain: "brandt.de", source: "admin", verified: true }] },
    );
    vi.stubGlobal("fetch", fetchMock);

    render(<OwnDomainsCard />);

    // Disabled, not hidden: a rep who cannot find a thread should be able to
    // see which domains explain that.
    const field = await screen.findByLabelText(/add an own domain/i);
    expect(field.hasAttribute("disabled")).toBe(true);
    expect(
      screen
        .getByRole("button", { name: /remove brandt\.de/i })
        .hasAttribute("disabled"),
    ).toBe(true);
    expect(screen.getByText(/only an admin or ops/i)).toBeTruthy();
  });
});
