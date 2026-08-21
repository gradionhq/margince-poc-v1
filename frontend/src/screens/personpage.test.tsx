/** @vitest-environment jsdom */
import {
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
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
  // With an address on file, so the header's lead verb is the Email one. The
  // verb names the transport the composer would pick, so a contact with no way
  // to be reached carries a different button — which is its own suite below.
  person: {
    id: "p-1",
    full_name: "Dana Buyer",
    emails: [
      {
        id: "pe-1",
        person_id: "p-1",
        email: "dana@brandt.example",
        email_type: "work",
        is_primary: true,
        position: 0,
        ...CAPTURED,
      },
    ],
    ...CAPTURED,
  },
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
    // Which messaging providers exist is a deployment fact, so a channel has
    // no name until the directory supplies one.
    "GET /channel-providers": () =>
      jsonResponse({
        data: [
          {
            provider: "zalo_oa",
            label: "Zalo OA",
            credential_model: "workspace_bot",
            supplies_transport: true,
          },
        ],
      }),
  });
  render(
    <StoryProviders>
      <PersonPageV2 id="p-1" tab={tab} />
    </StoryProviders>,
  );
}

// The record's header, which is where the page's verbs live. Found through the
// heading it names rather than by class, because the header is a landmark for
// the record and the rail offers verbs of its own that must not be mistaken
// for it.
async function recordHeader(): Promise<HTMLElement> {
  const name = await screen.findByRole("heading", {
    level: 1,
    name: "Dana Buyer",
  });
  const header = name.closest("header");
  if (!(header instanceof HTMLElement)) {
    throw new Error("the record's header is not around its own heading");
  }
  return header;
}

// The page's ONE primary action, once the reads it depends on have arrived: the
// consent guard decides whether it may be pressed and the transport directory
// names a channel, so the first paint is not the answer. Waiting on the LABEL
// is what makes that wait a condition rather than a duration.
//
// Asserting the primary variant is half the point of every case below: what is
// under test is what the page's ONE green verb claims, not that some button
// somewhere carries the word.
async function leadVerb(label: string): Promise<HTMLButtonElement> {
  const header = await recordHeader();
  const lead = await within(header).findByRole("button", { name: label });
  if (!(lead instanceof HTMLButtonElement)) {
    throw new Error(`the header's "${label}" verb is not a button`);
  }
  if (!lead.classList.contains("btn-primary")) {
    throw new Error(`"${label}" is not the page's primary action`);
  }
  return lead;
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
    const header = await recordHeader();
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

    const header = await recordHeader();
    await user.click(within(header).getByRole("button", { name: "Email" }));

    expect(await intentValue()).toBe("");
  });
});

// The reachability the record carries, and the conversation a reply would
// continue. Both are needed before a channel is a way to reach anybody: the
// first says the identity is live, the second that there is something to answer.
const reachableOnZalo = [
  { provider: "zalo_oa", reachable: true, since: "2026-07-01T08:00:00Z" },
];

// `kind` is annotated rather than left to inference: the contract's activity
// kind is a closed union, and a bare object literal widens it to `string`, which
// the timeline's own type then refuses.
const chatMessage: components["schemas"]["Activity"] = {
  id: "a-chat",
  kind: "message",
  channel_provider: "zalo_oa",
  occurred_at: "2026-08-12T12:00:00Z",
  is_done: false,
  ...CAPTURED,
};

// A contact captured over a chat channel: no address, one conversation.
const chatOnly: Person360 = {
  ...view,
  person: { ...view.person, emails: [], reachability: reachableOnZalo },
  activities: { data: [chatMessage], page: { has_more: false } },
};

// Reachable both ways, which is the case where the composer must ASK.
const mailAndChat: Person360 = {
  ...view,
  person: { ...view.person, reachability: reachableOnZalo },
  activities: {
    data: [...(view.activities?.data ?? []), chatMessage],
    page: { has_more: false },
  },
};

// Nothing to write to: no address, and no channel conversation to answer. The
// mail thread on the record is not a way to reach them — an address is.
const unreachable: Person360 = {
  ...view,
  person: { ...view.person, emails: [] },
};

// A guard that refuses mail. Consent is decided per purpose, so this is one
// refused purpose and no allowed one — the state the hero button reads as "may
// not write".
const mailBlocked: PersonConsentGuardEntry = {
  purpose_key: "marketing",
  purpose_class: "marketing",
  channel: "email",
  verdict: "blocked",
  reason: "opt-out recorded 12 July",
};

describe("the page's one primary action", () => {
  // The two sentences a refusal can say. Held as constants because the point of
  // the last case is that these two are never the same sentence.
  const NO_TRANSPORT = "No address, and no conversation to reply to.";
  const CONSENT_REFUSED = "No purpose currently permits writing to them.";

  it("names mail when an address is the only way to reach them", async () => {
    mount("overview", view, [mailAllowed]);

    await waitFor(async () => {
      expect((await leadVerb("Email")).disabled).toBe(false);
    });
    const lead = await leadVerb("Email");
    expect(lead.querySelector(".lucide-mail")).toBeTruthy();
  });

  it("names the channel when a chat conversation is the only way", async () => {
    // The reported symptom: a green Email on a contact the CRM can only reach
    // on a chat channel. The provider is named by the directory, so this reads
    // as itself for a unit this build has never heard of too.
    mount("overview", chatOnly, [mailAllowed]);

    await waitFor(async () => {
      expect((await leadVerb("Message on Zalo OA")).disabled).toBe(false);
    });
    const lead = await leadVerb("Message on Zalo OA");
    expect(lead.querySelector(".lucide-message-square")).toBeTruthy();
    expect(lead.querySelector(".lucide-mail")).toBeNull();
  });

  it("promises neither transport when the composer will ask which", async () => {
    // Two ways to reach them means the drawer opens a picker, so a button
    // reading Email would have named one of several and then asked.
    const user = userEvent.setup();
    mount("overview", mailAndChat, [mailAllowed]);

    await waitFor(async () => {
      expect((await leadVerb("Write")).disabled).toBe(false);
    });
    const lead = await leadVerb("Write");
    expect(lead.querySelector(".lucide-pen-line")).toBeTruthy();
    expect(lead.querySelector(".lucide-mail")).toBeNull();
    expect(lead.querySelector(".lucide-message-square")).toBeNull();

    // And the picker it stayed neutral for is really there.
    await user.click(lead);
    expect(await screen.findByLabelText("How to send")).toBeTruthy();
  });

  it("refuses, and says why, when there is no way to write to them", async () => {
    // The case the consent verdict cannot catch: the guard is happily `allowed`
    // for a contact who has no address at all, so an enabled button would open
    // a composer that can only fail at the send.
    mount("overview", unreachable, [mailAllowed]);

    expect(await screen.findByText(NO_TRANSPORT)).toBeTruthy();
    const lead = await leadVerb("Write");
    expect(lead.disabled).toBe(true);
    expect(lead.getAttribute("aria-describedby")).toBeTruthy();
  });

  it("refuses under the consent verdict on its own reason", async () => {
    mount("overview", view, [mailBlocked]);

    expect(await screen.findByText(CONSENT_REFUSED)).toBeTruthy();
    const lead = await leadVerb("Email");
    expect(lead.disabled).toBe(true);
    // The verb still names the transport it would have opened: what changed is
    // whether it may be pressed, not what pressing it would do.
    expect(lead.querySelector(".lucide-mail")).toBeTruthy();
  });

  it("keeps the two refusals apart when both apply", async () => {
    // A rep told the wrong one goes looking in the wrong record. Reachability
    // is the sentence to show, because it is the half that no consent decision
    // can lift.
    mount("overview", unreachable, [mailBlocked]);

    expect(await screen.findByText(NO_TRANSPORT)).toBeTruthy();
    expect(screen.queryByText(CONSENT_REFUSED)).toBeNull();
  });
});

// The seam the meeting brief regressed at: the page decides WHICH meeting the
// drawer briefs. Mounting the tab alone proves only that a callback fires with
// an id — the page could still ignore it and ask for the next meeting, which
// is exactly the bug the drawer used to have. So this asserts the URL that
// actually goes out.
describe("which meeting the brief drawer asks about", () => {
  const heldMeeting = {
    id: "a-held",
    kind: "meeting" as const,
    subject: "Depot walkthrough",
    occurred_at: "2026-08-09T08:00:00Z",
    is_done: false,
    ...CAPTURED,
  };

  const withMeetings: Person360 = {
    ...view,
    activities: { data: [heldMeeting], page: { has_more: false } },
    next_meeting: {
      activity_id: "a-booked",
      starts_at: "2026-08-20T13:00:00Z",
      subject: "Contract review",
      participants: [{ person_id: "p-1", full_name: "Dana Buyer" }],
    },
  };

  it("requests the brief for the meeting the reader picked", async () => {
    const asked: string[] = [];
    installFetchStub({
      "GET /me": meRoute({ person: ["read", "update"], activity: ["read"] }),
      "GET /people/p-1/360": () => jsonResponse(withMeetings),
      "GET /people/p-1/brief": () =>
        jsonResponse({
          person_id: "p-1",
          sentences: [],
          generated_by: "rules",
        }),
      "GET /people/p-1/consent/guard": () =>
        jsonResponse({ person_id: "p-1", entries: [] }),
      "GET /channel-providers": () => jsonResponse({ data: [] }),
      "GET /activities/a-held/meeting-brief": () => {
        asked.push("a-held");
        return jsonResponse({
          activity_id: "a-held",
          generated_at: "2026-08-13T09:00:00Z",
          generated_by: "deterministic",
          sections: [],
        });
      },
      "GET /activities/a-booked/meeting-brief": () => {
        asked.push("a-booked");
        return jsonResponse({
          activity_id: "a-booked",
          generated_at: "2026-08-13T09:00:00Z",
          generated_by: "deterministic",
          sections: [],
        });
      },
    });
    render(
      <StoryProviders>
        <PersonPageV2 id="p-1" tab="meetings" />
      </StoryProviders>,
    );

    const actions = await screen.findAllByRole("button", { name: "Brief me" });
    // The booked meeting leads the tab and the held one follows it, so the
    // second verb is the one that used to be unreachable.
    await userEvent.setup().click(actions[1]);

    await waitFor(() => expect(asked.length).toBe(1));
    expect(asked).toEqual(["a-held"]);
  });
});
