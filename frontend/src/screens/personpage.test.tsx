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
type PersonConsentGuardEntry = components["schemas"]["PersonConsentGuardEntry"];

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

// The gone-quiet rung, carrying the reason the server put in its destination.
// The label promises a follow-up; the prefill is the only place the reason it
// fired for is written down.
const quietMoment: Person360["moment"] = {
  claim_key: "gone_quiet:p-1",
  evidence_fingerprint: "fp-1",
  rule: "gone_quiet",
  headline: "Dana has gone quiet",
  why_now: "Six weeks since her last reply",
  confidence: "observed_fact",
  evidence: [{ type: "activity", label: "Fleet renewal", id: "a-1" }],
  recommended_action: {
    kind: "draft_reply",
    label: "Draft a follow-up",
    state: "available",
    destination: { surface: "composer", prefill: { intent: "follow_up" } },
  },
};

// A guard that permits mail, so the header's own Email verb is pressable: the
// hero button is disabled until some purpose says yes, and the empty-composer
// case can only be read through a button a reader can press.
const mailAllowed: PersonConsentGuardEntry = {
  purpose_key: "business_correspondence",
  purpose_class: "business_correspondence",
  channel: "email",
  verdict: "allowed",
  reason: "she wrote to you on 11 August",
};

function mount(
  tab: (typeof PERSON_TABS)[number],
  page: Person360 = view,
  guardEntries: readonly PersonConsentGuardEntry[] = [],
) {
  installFetchStub({
    "GET /me": meRoute({ person: ["read", "update"] }),
    "GET /people/p-1/360": () => jsonResponse(page),
    "GET /people/p-1/brief": () =>
      jsonResponse({ person_id: "p-1", sentences: [], generated_by: "rules" }),
    "GET /people/p-1/consent/guard": () =>
      jsonResponse({ person_id: "p-1", entries: guardEntries }),
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
    const user = userEvent.setup();
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
    await user.click(within(header).getByRole("button", { name: "Call" }));
    expect(window.location.hash).toBe("#/contacts/p-1/timeline");
  });
});

describe("a moment action that opens the composer", () => {
  // The steering field's own value, read off the element rather than through a
  // matcher: this file carries no jest-dom, and narrowing beats asserting.
  async function intentValue(): Promise<string> {
    const field = await screen.findByRole("textbox", {
      name: "What should it be about?",
    });
    if (!(field instanceof HTMLInputElement)) {
      throw new Error("the composer's steering field is not a text input");
    }
    return field.value;
  }

  it("opens it about the reason the rung fired for", async () => {
    // The rung computes WHY it fired and says so in its destination. A composer
    // that opened empty threw that away and left a labelled verb doing exactly
    // what the generic Write-an-email button does.
    const user = userEvent.setup();
    mount("overview", { ...view, moment: quietMoment }, [mailAllowed]);

    await user.click(
      await screen.findByRole("button", { name: "Draft a follow-up" }),
    );

    expect(await intentValue()).toBe("follow up — it has gone quiet");
  });

  it("opens an empty one for the generic Email verb, whatever a rung asked for before", async () => {
    // The two buttons share one drawer, so the rung's reason has to be dropped
    // on the way in: inherited, it would draft a follow-up for a reader who
    // asked for a blank sheet.
    const user = userEvent.setup();
    mount("overview", { ...view, moment: quietMoment }, [mailAllowed]);

    await user.click(
      await screen.findByRole("button", { name: "Draft a follow-up" }),
    );
    expect(await intentValue()).toBe("follow up — it has gone quiet");
    await user.keyboard("{Escape}");

    const name = await screen.findByRole("heading", {
      level: 1,
      name: "Dana Buyer",
    });
    const header = name.closest("header");
    if (!header) {
      throw new Error("the record's header is not around its own heading");
    }
    await user.click(within(header).getByRole("button", { name: "Email" }));

    expect(await intentValue()).toBe("");
  });
});
