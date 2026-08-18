/** @vitest-environment jsdom */
import { cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";
import type { components } from "../api/schema";
import { PersonPageV2 } from "./personpage";
import { PERSON_TABS } from "./persontab";
import {
  installFetchStub,
  jsonResponse,
  meRoute,
  StoryProviders,
} from "./story-utils";

// Every tab of the contact record was a placeholder panel once, and the way
// that regresses is silent: a tab whose content stops rendering looks exactly
// like a tab with nothing on it. These mount the REAL page — the tab bar, the
// panel switch and the routing between them — because the bug this file exists
// to catch lived in the seam between them, not inside any one tab.

type Person360 = components["schemas"]["Person360"];

const CAPTURED = {
  source: "manual",
  captured_by: "human:u-1",
  created_at: "2026-06-01T08:00:00Z",
  updated_at: "2026-08-01T08:00:00Z",
} as const;

const view: Person360 = {
  as_of: "2026-08-13T09:00:00Z",
  person: { id: "p-1", full_name: "Dana Buyer", ...CAPTURED },
  sections_omitted: [],
  activities: {
    data: [
      {
        id: "a-1",
        kind: "email",
        subject: "Fleet renewal",
        occurred_at: "2026-08-11T12:00:00Z",
        is_done: false,
        ...CAPTURED,
      },
    ],
    page: { has_more: false },
  },
  deal_roles: { data: [], page: { has_more: false } },
  profile_fields: [],
};

function mount(tab: (typeof PERSON_TABS)[number]) {
  installFetchStub({
    "GET /me": meRoute({ person: ["read", "update"] }),
    "GET /people/p-1/360": () => jsonResponse(view),
    "GET /people/p-1/brief": () =>
      jsonResponse({ person_id: "p-1", sentences: [], generated_by: "rules" }),
    "GET /people/p-1/consent/guard": () =>
      jsonResponse({ person_id: "p-1", entries: [] }),
  });
  render(
    <StoryProviders>
      <PersonPageV2 id="p-1" tab={tab} />
    </StoryProviders>,
  );
}

afterEach(() => {
  cleanup();
});

describe("the contact record's tabs", () => {
  it.each(PERSON_TABS.filter((tab) => tab !== "overview"))(
    "draws real content on the %s tab rather than an empty column",
    async (tab) => {
      mount(tab);
      // Whatever the tab's own state turns out to be, it must render a panel
      // of its own: an empty main column is the failure this pins.
      const panels = await screen.findAllByRole("heading", { level: 2 });
      expect(panels.length).toBeGreaterThan(0);
    },
  );

  it("sends the header's Call verb to the tab that holds the conversation", async () => {
    // The tab ids are URL segments and `navigate` takes them as a bare string,
    // so a rename can leave a verb pointing at an id nothing serves — the
    // router then falls back to Overview and says nothing about it.
    mount("overview");
    // The header's verb, not the rail's: the rail offers its own Call, and
    // the two land in different places on purpose.
    const name = await screen.findByRole("heading", {
      level: 1,
      name: "Dana Buyer",
    });
    const header = name.closest("header");
    if (!header) {
      throw new Error("the record's header is not around its own heading");
    }
    await userEvent.click(within(header).getByRole("button", { name: "Call" }));
    expect(window.location.hash).toBe("#/contacts/p-1/timeline");
  });
});
