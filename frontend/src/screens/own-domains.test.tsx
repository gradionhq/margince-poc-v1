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
import { en } from "../i18n/en";
import { OwnDomainsCard } from "./own-domains";

// Settings → Capture: the domains this installation treats as its own. Two
// lists with two different owners share ONE card — the company profile claims
// the first and this surface cannot touch them, the second is managed here — so
// each is its own named row and only the managed one carries verbs. A remove
// button beside a domain nobody can remove here is the defect that separation
// exists to prevent.

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

// One settings row, by the id it names itself with: an assertion that counted
// rows would pass for the wrong reason the moment a row is reordered.
function rowOf(testId: string): Promise<HTMLElement> {
  return screen.findByTestId(testId);
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("OwnDomainsCard", () => {
  it("keeps the company-claimed domains and the managed ones in separate rows", async () => {
    const { fetchMock } = backendFor(CAPTURE_EDITOR, {
      anchors: ["brandt-automotive.de"],
      domains: [{ domain: "brandt.de", source: "admin", verified: true }],
    });
    vi.stubGlobal("fetch", fetchMock);

    render(<OwnDomainsCard />);

    const company = await rowOf("own-domains-company-row");
    expect(within(company).getByText(/company domains/i)).toBeTruthy();
    expect(within(company).getByText("brandt-automotive.de")).toBeTruthy();
    // Read-only means read-only: nothing in this row offers to change a list
    // the company profile owns.
    expect(within(company).queryByRole("button")).toBeNull();
    expect(within(company).queryByRole("textbox")).toBeNull();

    const managed = await rowOf("own-domains-curated-row");
    expect(within(managed).getByText(/managed here/i)).toBeTruthy();
    // The note about what registering a domain does travels with the row that
    // offers the acts it describes.
    expect(
      within(managed).getByText(/takes effect from the next message/i),
    ).toBeTruthy();
    expect(
      within(managed).getByRole("button", { name: /remove brandt\.de/i }),
    ).toBeTruthy();
    // The add form is behind ONE verb, in the card's header rather than in a
    // row that repeats its own label — and the field is not on the card until
    // the verb is pressed, so every row stays an answer.
    expect(
      screen.getByRole("button", { name: en["ownDomains.addOpen"] }),
    ).toBeTruthy();
    expect(screen.queryByLabelText(/add an own domain/i)).toBeNull();
  });

  it("shows no company row when the company profile claims no domain", async () => {
    const { fetchMock } = backendFor(CAPTURE_EDITOR);
    vi.stubGlobal("fetch", fetchMock);

    render(<OwnDomainsCard />);

    // The empty managed list still states itself; a row whose whole content
    // would be an empty read-only list says nothing worth naming.
    expect(await screen.findByTestId("own-domains-empty")).toBeTruthy();
    expect(screen.queryByTestId("own-domains-company-row")).toBeNull();
  });

  it("adds a domain through the dialog the add verb opens", async () => {
    const user = userEvent.setup();
    const { fetchMock, post } = backendFor(CAPTURE_EDITOR);
    vi.stubGlobal("fetch", fetchMock);

    render(<OwnDomainsCard />);

    await screen.findByTestId("own-domains-empty");
    await user.click(
      screen.getByRole("button", { name: en["ownDomains.addOpen"] }),
    );
    const dialog = screen.getByRole("dialog");
    await user.type(
      within(dialog).getByLabelText(/add an own domain/i),
      "brandt.de",
    );
    await user.click(within(dialog).getByRole("button", { name: /^add$/i }));

    await waitFor(() => expect(post()).toEqual({ domain: "brandt.de" }));
    // The dialog is the form; a write that landed leaves nothing to submit
    // again, and the card behind it is what reports the new list.
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });

  it("refuses both verbs for a role that cannot change the list, and says why once", async () => {
    const { fetchMock } = backendFor(
      {},
      { domains: [{ domain: "brandt.de", source: "admin", verified: true }] },
    );
    vi.stubGlobal("fetch", fetchMock);

    render(<OwnDomainsCard />);

    // Refused, not hidden: a rep who cannot find a thread should be able to
    // see which domains explain that.
    const remove = await screen.findByRole("button", {
      name: /remove brandt\.de/i,
    });
    const add = screen.getByRole("button", { name: en["ownDomains.addOpen"] });
    expect(remove.hasAttribute("disabled")).toBe(true);
    expect(add.hasAttribute("disabled")).toBe(true);
    // One sentence, and both refused verbs point at it — a reason a screen
    // reader only reaches by wandering into the paragraph is no reason at all.
    const denial = screen.getByText(/only an admin or ops/i);
    expect(remove.getAttribute("aria-describedby")).toBe(denial.id);
    expect(add.getAttribute("aria-describedby")).toBe(denial.id);
  });
});
