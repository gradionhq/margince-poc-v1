/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { components } from "../api/schema";
import { LocaleProvider } from "../i18n";
import { CompanyActionBadges, CompanyIdentityLine } from "./companyheader";

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

// /me answers, /users does not — the header on the first frames of a page load,
// held still. Nothing waits on a clock: the resolvers are collected so a test
// can let the roster answer when it wants to assert the settled reading.
function stubRosterInFlight(): Array<(response: Response) => void> {
  const answer: Array<(response: Response) => void> = [];
  vi.stubGlobal(
    "fetch",
    vi.fn((request: Request) => {
      if (new URL(request.url).pathname.endsWith("/me")) {
        return Promise.resolve(
          new Response(
            JSON.stringify({
              user: { id: "u-reader", display_name: "The Reader" },
              allow: {},
            }),
            { status: 200, headers: { "content-type": "application/json" } },
          ),
        );
      }
      return new Promise<Response>((resolve) => {
        answer.push(resolve);
      });
    }),
  );
  return answer;
}

// /me answers, /users is refused — the reading a reader gets when the roster
// read comes back with nothing to say about anyone.
function stubRosterRefused() {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (request: Request) => {
      if (new URL(request.url).pathname.endsWith("/me")) {
        return new Response(
          JSON.stringify({
            user: { id: "u-reader", display_name: "The Reader" },
            allow: {},
          }),
          { status: 200, headers: { "content-type": "application/json" } },
        );
      }
      return new Response(JSON.stringify({ title: "Forbidden", status: 403 }), {
        status: 403,
        headers: { "content-type": "application/problem+json" },
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

function renderInApp(ui: ReactNode) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  render(
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">{ui}</LocaleProvider>
    </QueryClientProvider>,
  );
}

function renderLine() {
  renderInApp(<CompanyIdentityLine org={ORG} />);
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

// Who OWNS the record, on the same line and off the same roster read. The owner
// has three states and the header used to have two: it read the owner through
// the generic record reference, which paints the id whenever it has no name in
// hand — so every company page opened with a uuid in its header and swapped it
// for a name a moment later.
describe("who owns this record", () => {
  it("names the owner the roster can resolve", async () => {
    stub([{ id: "u-owner", display_name: "Mira Voss" }]);
    renderLine();

    expect(await screen.findByText("Mira Voss")).toBeTruthy();
    expect(document.body.textContent).not.toContain("u-owner");
  });

  it("does not call the owner gone while the roster read is still in flight", async () => {
    const answer = stubRosterInFlight();
    renderLine();

    // "no longer in the user list" is a claim about a read that came back. Said
    // over one still running, it reports an owner as departed on the evidence
    // of nothing having arrived yet.
    expect(await screen.findByText("Loading…")).toBeTruthy();
    expect(
      screen.queryByText("Current owner (no longer in the user list)"),
    ).toBeNull();
    expect(document.body.textContent).not.toContain("u-owner");

    for (const resolve of answer) {
      resolve(
        new Response(
          JSON.stringify({
            data: [{ id: "u-owner", display_name: "Mira Voss" }],
            page: { has_more: false, next_cursor: null },
          }),
          { status: 200, headers: { "content-type": "application/json" } },
        ),
      );
    }
    expect(await screen.findByText("Mira Voss")).toBeTruthy();
  });

  it("does not call the owner gone when the roster read failed", async () => {
    stubRosterRefused();
    renderLine();

    // A refused read excludes nobody. Reading it as "no longer in the user
    // list" turns a 403 into a fact about who owns this account, which is the
    // one thing this control is here to get right.
    expect(await screen.findByText("Name didn't load")).toBeTruthy();
    expect(
      screen.queryByText("Current owner (no longer in the user list)"),
    ).toBeNull();
    expect(document.body.textContent).not.toContain("u-owner");
  });

  it("says the owner is outside the user list once the roster has answered without them", async () => {
    stub([]);
    renderLine();

    expect(
      await screen.findByText("Current owner (no longer in the user list)"),
    ).toBeTruthy();
    // An owner the roster cannot name is still not shown as a uuid: waiting
    // will not resolve them, and their id answers no question a reader has.
    expect(document.body.textContent).not.toContain("u-owner");
  });
});

// The edit form prefills the same owner off the same roster read, so it made
// the same claim one control over: a reader who opened the form to check what
// the header said was told "no longer in the user list" a second time, by a
// read that had excluded nobody.
describe("the owner the edit form prefills", () => {
  it("does not call the owner gone when the roster read failed", async () => {
    stubRosterRefused();
    const user = userEvent.setup();
    renderInApp(
      <CompanyActionBadges
        org={ORG}
        onOpenHistory={() => undefined}
        onSetUpPartner={() => undefined}
      />,
    );

    await user.click(
      await screen.findByRole("button", { name: "More actions" }),
    );
    await user.click(await screen.findByTestId("edit-record"));

    expect(await screen.findByText("Name didn't load")).toBeTruthy();
    expect(
      screen.queryByText("Current owner (no longer in the user list)"),
    ).toBeNull();
  });
});
