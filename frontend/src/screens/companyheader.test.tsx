/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { components } from "../api/schema";
import { LocaleProvider } from "../i18n";
import { CompanyIdentityLine } from "./companyheader";

// Who wrote the record, beside when it was written. The tag has always been able
// to name the author — `ProvenanceTag` takes a `renderUser` — and the header has
// always had the roster in hand, because the owner control on the line above
// reads it. Nobody connected the two, so a record every colleague could see
// reported its author as "a person".
//
// The fallback is the half worth pinning: the roster is one page of 200 (#1247),
// and a name that cannot be resolved must go back to "typed by a person" rather
// than forward to the raw uuid. "typed by 3f2b8c…" is not more information than
// "typed by a person", it is the same non-answer with a reader-hostile spelling.

type Organization = components["schemas"]["Organization"];

// Typed, not asserted. A fixture cast into the contract type can drop a required
// field and still compile, so the test would go on passing after the wire shape
// moved under it — which is the one thing a fixture must not do.
const ORG: Organization = {
  id: "o-1",
  display_name: "Brandt Automotive GmbH",
  lifecycle: "customer",
  owner_id: "u-owner",
  captured_by: "human:u-author",
  source: "manual",
  version: 1,
  created_at: "2026-06-01T08:00:00Z",
  updated_at: "2026-06-01T08:00:00Z",
};

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

// `roster` is what /users answers with. An empty one is the honest shape of the
// author sitting outside the single page the roster reads, not a broken stub.
function stub(roster: ReadonlyArray<{ id: string; display_name: string }>) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (request: Request) => {
      const { pathname } = new URL(request.url);
      const body = pathname.endsWith("/me")
        ? { user: { id: "u-reader", display_name: "The Reader" }, allow: {} }
        : { data: roster, page: { has_more: false, next_cursor: null } };
      return new Response(JSON.stringify(body), {
        status: 200,
        headers: { "content-type": "application/json" },
      });
    }),
  );
}

// The tag is one span carrying "typed by" and the name as sibling text nodes, so
// the reading a human gets is the span's whole text — asserting on the name alone
// would pass on markup that never says what the name is doing there.
function provenanceText(): string {
  const tag = document.querySelector(".provenance-human");
  if (!tag) {
    throw new Error("the identity line rendered no human provenance tag");
  }
  return tag.textContent?.replace(/\s+/g, " ").trim() ?? "";
}

function renderLine() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  render(
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">
        <CompanyIdentityLine org={ORG} />
      </LocaleProvider>
    </QueryClientProvider>,
  );
}

describe("who wrote this record", () => {
  it("names the author the roster can resolve", async () => {
    stub([
      { id: "u-author", display_name: "Sofia Meier" },
      { id: "u-owner", display_name: "Mira Voss" },
    ]);
    renderLine();

    await waitFor(() => expect(provenanceText()).toBe("typed by Sofia Meier"));
    expect(screen.queryByText("typed by a person")).toBeNull();
  });

  it("says a person wrote it, not a uuid, when the roster cannot resolve them", async () => {
    stub([{ id: "u-owner", display_name: "Mira Voss" }]);
    renderLine();

    expect(await screen.findByText("typed by a person")).toBeTruthy();
    // The id must not reach the page in any form — neither whole nor truncated,
    // which is what the generic record reference would have rendered.
    expect(provenanceText()).toBe("typed by a person");
    expect(document.body.textContent).not.toContain("u-author");
  });
});
