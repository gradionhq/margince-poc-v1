/** @vitest-environment jsdom */
import "@testing-library/jest-dom/vitest";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { components } from "../api/schema";
import { meFixture } from "../app/mefixture";
import { LocaleProvider } from "../i18n";
import { DetailsGrid } from "./companyraildetails";

// The address is six rows of one fact, and a crawled record fills none of them:
// a team page names no registered address. Drawn inline, the panel opened with
// six consecutive invitations to type and pushed the account's actual facts
// below the fold — so the parts sit behind a disclosure that is closed exactly
// when there is nothing in them.
//
// Both cases assert on the browser's own state (`details.open`, and jest-dom's
// `toBeVisible`, which is the one matcher that knows a closed `<details>` hides
// its content): the six labels stay in the DOM either way, and dom-testing-
// library's role queries return them whether the disclosure is open or shut, so
// an absence assertion phrased as a missing node would pass on markup that
// shows the reader all six.

type Organization = components["schemas"]["Organization"];

const ORG = {
  id: "o-1",
  display_name: "Brandt Automotive GmbH",
  lifecycle: "customer",
  owner_id: "u-1",
  industry: "Automotive",
  captured_by: "human:u-author",
  source: "manual",
  version: 1,
  created_at: "2026-06-01T08:00:00Z",
  updated_at: "2026-06-01T08:00:00Z",
} as unknown as Organization;

// The six part labels, in the order the grid draws them.
const PART_LABELS = [
  "Street and number",
  "Address line 2",
  "Postal code",
  "City",
  "State / region",
  "Country",
];

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

// The grant is what makes the rows editable, which is the state the collapse is
// about: a viewer who may not write sees values, and an empty address draws no
// invitation to hide in the first place.
function stub() {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (request: Request) => {
      const { pathname } = new URL(request.url);
      const body = pathname.endsWith("/me")
        ? meFixture({ allow: { organization: ["read", "update"] } })
        : {
            data: [{ id: "u-1", display_name: "Mira Voss" }],
            page: { has_more: false, next_cursor: null },
          };
      return new Response(JSON.stringify(body), {
        status: 200,
        headers: { "content-type": "application/json" },
      });
    }),
  );
}

function renderGrid(organization: Organization) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  render(
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">
        <DetailsGrid organization={organization} />
      </LocaleProvider>
    </QueryClientProvider>,
  );
}

// Settles the grid on the grant read: until /me lands every row is read-only,
// which is a different rendering of the same record and not the one under test.
// The industry row is the anchor because it sits outside the disclosure and
// carries a value in every fixture here.
async function renderSettledGrid(organization: Organization) {
  stub();
  renderGrid(organization);
  await screen.findByRole("button", { name: "Change Industry" });
}

describe("the postal address, behind one line until it has something in it", () => {
  it("holds the six parts behind one line that invites the first of them", async () => {
    await renderSettledGrid(ORG);

    expect(screen.getByText("Add an address")).toBeVisible();
    expect(document.querySelector("details")?.open).toBe(false);
    for (const label of PART_LABELS) {
      expect(screen.getByText(label)).not.toBeVisible();
    }
    // The regression itself: six "Add …" pressables stacked above the facts a
    // reader came for.
    expect(screen.getByText("Add street and number")).not.toBeVisible();
    // What the panel is for is still on screen, unmoved.
    expect(screen.getByText("Automotive")).toBeVisible();
  });

  it("opens on a half-filled address and reads the part that is set", async () => {
    await renderSettledGrid({
      ...ORG,
      address: { city: "Berlin" },
    } as unknown as Organization);

    expect(document.querySelector("details")?.open).toBe(true);
    expect(screen.getByText("Address")).toBeVisible();
    expect(screen.queryByText("Add an address")).toBeNull();
    expect(screen.getByText("City")).toBeVisible();
    expect(screen.getByText("Berlin")).toBeVisible();
    // Open means all six, so the five still-empty parts keep inviting a value
    // exactly as they did before the collapse existed.
    for (const label of PART_LABELS) {
      expect(screen.getByText(label)).toBeVisible();
    }
  });
});
