/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { components } from "../api/schema";
import { LocaleProvider } from "../i18n";
import { CoverageExplorer } from "./coverageexplorer";

// The grid exists to answer "where are we thin" without becoming a contact ×
// every-colleague matrix, and to never let a blank cell mean two things.

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

type Contact = components["schemas"]["Organization360Contact"];

const CONTACTS = [
  {
    person_id: "p-1",
    full_name: "Dana Buyer",
    strength: {},
    deal_roles: [],
    consent: {},
  },
  {
    person_id: "p-2",
    full_name: "Sam Silent",
    strength: {},
    deal_roles: [],
    consent: {},
  },
] as unknown as Contact[];

function stubGraph(dropped = 0) {
  vi.stubGlobal(
    "fetch",
    vi.fn(
      async () =>
        new Response(
          JSON.stringify({
            nodes: [
              { id: "u-1", kind: "user", label: "Mira", root: false },
              { id: "p-1", kind: "person", label: "Dana Buyer", root: false },
            ],
            edges: [
              {
                from: "u-1",
                to: "p-1",
                kind: "in_contact_with",
                strength: 90,
                strength_bucket: "strong",
              },
            ],
            groups_omitted: [],
            dropped_count: dropped,
          }),
          { status: 200, headers: { "content-type": "application/json" } },
        ),
    ),
  );
}

function show(ui: ReactNode) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">{ui}</LocaleProvider>
    </QueryClientProvider>,
  );
}

async function open() {
  await userEvent.click(
    screen.getByRole("button", { name: "Compare coverage" }),
  );
}

describe("comparing the colleagues a reader chooses", () => {
  it("reads a cell with no connection as Untried, not as a blank", async () => {
    stubGraph();
    show(<CoverageExplorer orgId="o-1" contacts={CONTACTS} />);
    await open();

    // Sam Silent has no edge. "Untried" says nobody has written to them, which
    // is a different instruction from a cold band — and a blank cell says
    // neither, leaving the reader to guess which.
    expect(await screen.findByText("Sam Silent")).toBeTruthy();
    expect(screen.getByText("Untried")).toBeTruthy();
  });

  it("offers only colleagues who have actually reached this account", async () => {
    stubGraph();
    show(<CoverageExplorer orgId="o-1" contacts={CONTACTS} />);
    await open();

    // A column the reader has to rule out is worse than no column, so a
    // colleague with no edge to this account never appears at all.
    expect(await screen.findByRole("button", { name: "Mira" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Nobody" })).toBeNull();
  });

  it("says a grid built from a capped read may be short", async () => {
    stubGraph(7);
    show(<CoverageExplorer orgId="o-1" contacts={CONTACTS} />);
    await open();

    // "No connection" and "the read stopped short" are different claims, and a
    // reader told nobody covers a contact would stop looking.
    expect(await screen.findByText(/partial read/)).toBeTruthy();
  });

  it("filters the contact rows without touching the columns", async () => {
    stubGraph();
    show(<CoverageExplorer orgId="o-1" contacts={CONTACTS} />);
    await open();
    await screen.findByText("Dana Buyer");

    await userEvent.type(
      screen.getByRole("searchbox", { name: "Find a contact" }),
      "Sam",
    );
    expect(screen.queryByText("Dana Buyer")).toBeNull();
    expect(screen.getByText("Sam Silent")).toBeTruthy();
  });
});
