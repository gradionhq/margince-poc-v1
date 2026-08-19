/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { components } from "../api/schema";
import { meFixture } from "../app/mefixture";
import { LocaleProvider } from "../i18n";
import { PersonRail } from "./personrail";

// The rail's words are short verdicts — "One-sided", "Never", "No inbound",
// "Thin", "Nothing stands out on this relationship." — which is exactly why the
// withheld case has to be pinned here rather than left to review. A verdict
// reads as a measured fact whatever produced it, and the 360 hands an
// unreadable section to the client by leaving it out: absent-because-empty and
// absent-because-forbidden arrive as the same undefined field, distinguished
// only by `sections_omitted`.
//
// So every test below comes in a PAIR with the granted one beside it. A test
// that only checks the withheld side would pass just as happily against a rail
// that hedged every reading, and a rail that says "Not shown" about a contact
// who genuinely never wrote is the same defect pointed the other way.

type Person360 = components["schemas"]["Person360"];
type Activity = components["schemas"]["Activity"];

const page = { has_more: false, next_cursor: null };

// Timestamps are stated as an age rather than as a date: the readings are
// derived from the distance to now, so a fixed date would assert a different
// number of days on every day the suite runs.
//
// "Now" is PINNED rather than read from the host clock. Ages derived from a
// moving now make the expected reading depend on the moment the suite runs —
// including on the midnight boundary, where the same fixture is 3 days old
// before and 4 days old after.
const NOW = new Date("2026-08-18T09:00:00Z");

function daysAgo(days: number): string {
  return new Date(NOW.getTime() - days * 86_400_000).toISOString();
}

const person: components["schemas"]["Person"] = {
  id: "p-1",
  full_name: "Dana Buyer",
  first_name: "Dana",
  source: "manual",
  captured_by: "human:u-1",
  created_at: "2026-06-01T08:00:00Z",
  updated_at: "2026-08-01T08:00:00Z",
};

function inboundEmail(): Activity {
  return {
    id: "a-1",
    kind: "email",
    direction: "inbound",
    subject: "Re: retrofit timeline",
    occurred_at: daysAgo(3),
    source: "gmail",
    captured_by: "connector:gmail",
    created_at: daysAgo(3),
    updated_at: daysAgo(3),
    is_done: false,
  };
}

// A reader holding every grant, looking at a relationship with something on it:
// they wrote three days ago, we wrote five days before that, one colleague
// knows them, and there is an open deal with a meeting booked against it.
const granted: Person360 = {
  as_of: "2026-08-18T09:00:00Z",
  person,
  sections_omitted: [],
  last_inbound_at: daysAgo(3),
  last_outbound_at: daysAgo(8),
  network: {
    colleagues: [
      {
        user_id: "u-2",
        display_name: "Sam Rivera",
        strength: 0.7,
        strength_bucket: "strong",
        interactions_90d: 6,
        last_at: daysAgo(3),
        inbound_90d: 3,
        outbound_90d: 3,
      },
    ],
  },
  employments: {
    data: [
      {
        relationship_id: "rel-1",
        organization_id: "o-1",
        organization_name: "Brandt Automotive GmbH",
        role: "Head of Fleet",
        is_current_primary: true,
        started_at: "2022-03-01T00:00:00Z",
        ended_at: null,
      },
    ],
    page,
  },
  activities: { data: [inboundEmail()], page },
  commercial: {
    role: "champion",
    committee: [
      { person_id: "p-2", full_name: "Ines Klaas", role: "economic_buyer" },
    ],
    deal: { deal_id: "d-1", title: "Fleet retrofit" },
  },
  next_meeting: {
    activity_id: "a-2",
    starts_at: daysAgo(-2),
    subject: "Fleet retrofit walkthrough",
  },
};

// The same reader, on a contact nobody has ever corresponded with. Every
// section came back and every one of them is genuinely empty — the only state
// entitled to the rail's negative verdicts.
const emptyButGranted: Person360 = {
  as_of: granted.as_of,
  person,
  sections_omitted: [],
  last_inbound_at: null,
  last_outbound_at: null,
  network: { colleagues: [] },
  employments: { data: [], page },
  activities: { data: [], page },
  commercial: { committee: [] },
};

// A reader whose grants cover none of the relationship sections. The fields are
// absent, which is how the server says it — the list beside them is the only
// thing that distinguishes this from the record above.
const withheld: Person360 = {
  as_of: granted.as_of,
  person,
  sections_omitted: [
    "last_touch",
    "activities",
    "commercial",
    "next_meeting",
    "network",
    "employments",
  ],
};

function json(body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}

// Every body this rail POSTs, in order, so a test can assert what was SENT
// rather than what the component happened to render afterwards. The employment
// modal's whole contract with the server is the shape of one request.
const sent: Array<{ path: string; body: unknown }> = [];

function mount(view: Person360) {
  sent.length = 0;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(
        input instanceof Request ? input.url : String(input),
        "https://test",
      );
      // openapi-fetch hands `fetch` a Request, not (url, init), so the body is
      // on the Request and reading it needs a clone — consuming the original
      // would leave the real call with an empty body.
      const request = input instanceof Request ? input : null;
      const method = request?.method ?? init?.method;
      if (method === "POST") {
        const raw = request
          ? await request.clone().text()
          : String(init?.body ?? "");
        if (raw !== "") {
          sent.push({ path: url.pathname, body: JSON.parse(raw) });
        }
      }
      // The session probe is the one route that must answer a real body: an
      // unroutable /me fails every grant closed, which changes the edit
      // affordances the rail draws and nothing about its readings.
      if (url.pathname.endsWith("/me")) {
        return json(meFixture({ allow: { person: ["read", "update"] } }));
      }
      // The employer picker searches organizations; without a candidate the
      // modal's Save stays disabled and there is no request to inspect.
      if (url.pathname.endsWith("/organizations")) {
        return json({
          data: [{ id: "o-9", display_name: "Employer GmbH" }],
          page,
        });
      }
      return json({ data: [], page });
    }),
  );
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  render(
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">
        <PersonRail
          view={view}
          guard={undefined}
          firstName="Dana"
          onExplain={() => {}}
        />
      </LocaleProvider>
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  vi.setSystemTime(NOW);
});

afterEach(() => {
  vi.useRealTimers();
  cleanup();
  vi.unstubAllGlobals();
});

// The label and its reading are two spans of one row, so a reading is read
// through its label rather than by matching a word that also appears in three
// other sections.
function railReading(label: string): string {
  const row = screen.getByText(label).closest<HTMLElement>(".pe-rail-row");
  if (!row) {
    throw new Error(`the pulse drew no row labelled "${label}"`);
  }
  const value = row.children[1];
  if (!value) {
    throw new Error(`the row labelled "${label}" drew no reading`);
  }
  return value.textContent ?? "";
}

function readingClass(label: string): string {
  const row = screen.getByText(label).closest<HTMLElement>(".pe-rail-row");
  if (!row) {
    throw new Error(`the pulse drew no row labelled "${label}"`);
  }
  return row.children[1]?.className ?? "";
}

// A section is scoped through its own heading: "Hidden — your role cannot read
// this" is one sentence the rail may say about four different sections, and an
// unscoped match cannot tell which one said it.
//
// Matched on the heading's PREFIX rather than on the whole summary, because a
// section whose verbs the reader holds carries them inside the same summary —
// so an exact match would find the section only while the capability probe was
// still in flight.
function section(heading: string): HTMLElement {
  const summaries = document.querySelectorAll<HTMLElement>(
    "details.pe-sect > summary",
  );
  for (const summary of summaries) {
    if ((summary.textContent ?? "").startsWith(heading)) {
      const details = summary.closest<HTMLElement>("details.pe-sect");
      if (details) {
        return details;
      }
    }
  }
  throw new Error(`the rail drew no section headed "${heading}"`);
}

const WITHHELD_SENTENCE = "Hidden — your role cannot read this";

describe("the relationship pulse", () => {
  it("reads the directional facts for a reader who may see them", () => {
    mount(granted);

    expect(railReading("Direction")).toBe("Two-way");
    expect(railReading("Last reply")).toBe("3 days");
    expect(railReading("Trend")).toBe("Warming");
    expect(railReading("Overall")).toBe("Strong");
    expect(railReading("Coverage")).toBe("1 colleague");
  });

  it("states the negative verdicts for a contact who genuinely never wrote", () => {
    mount(emptyButGranted);

    // These four words are the ones the withheld case must NOT borrow. They
    // are true here and only here: the sections came back, and they are empty.
    expect(railReading("Direction")).toBe("One-sided");
    expect(railReading("Last reply")).toBe("Never");
    expect(railReading("Trend")).toBe("No inbound");
    expect(railReading("Overall")).toBe("Thin");
    expect(railReading("Coverage")).toBe("0 colleagues");
  });

  // One case per reading rather than four assertions in one test: each is a
  // separate derivation, and a single test would stop at the first one that
  // regressed and say nothing about the other three.
  it.each([
    ["Direction", "One-sided"],
    ["Last reply", "Never"],
    ["Trend", "No inbound"],
    ["Overall", "Thin"],
  ])(
    "says %s is not shown, where an absent last touch would read as %s",
    (label) => {
      mount(withheld);

      expect(railReading(label)).toBe("Not shown");
    },
  );

  it("says the coverage reading is not shown when the network is withheld", () => {
    mount(withheld);

    // Nought colleagues and a colleague list nobody may read are the two
    // answers a rep would act on differently: one says ask nobody, the other
    // says ask somebody with the grant.
    expect(railReading("Coverage")).toBe("Not shown");
  });

  it("gives a withheld overall reading no verdict colour", () => {
    mount(withheld);

    // The overall row is the only reading drawn in the verdict tone, and a
    // withheld reading is not a verdict — green on "Not shown" says the
    // relationship is healthy in the one spot a reader glances at.
    expect(readingClass("Overall")).not.toContain("pe-rail-value-good");
    expect(readingClass("Overall")).toBe("pe-rail-value");
  });
});

describe("signals and risks", () => {
  it("says nothing stands out only when every rule could run", () => {
    mount(emptyButGranted);

    expect(
      within(section("Signals & risks")).getByText(
        "Nothing stands out on this relationship.",
      ),
    ).toBeTruthy();
  });

  it("says the signals are withheld rather than that nothing stands out", () => {
    mount(withheld);

    const signals = section("Signals & risks");
    expect(within(signals).getByText(WITHHELD_SENTENCE)).toBeTruthy();
    expect(
      within(signals).queryByText("Nothing stands out on this relationship."),
    ).toBeNull();
  });

  it("counts a withheld last touch as a rule it could not run", () => {
    mount({
      ...emptyButGranted,
      // Only the timestamps are withheld. Every other section came back empty,
      // so the two quiet-relationship rules are the only ones that had
      // anything to say and neither of them could run — which is a different
      // finding from a relationship with nothing remarkable about it.
      last_inbound_at: undefined,
      last_outbound_at: undefined,
      sections_omitted: ["last_touch"],
    });

    const signals = section("Signals & risks");
    expect(within(signals).getByText(WITHHELD_SENTENCE)).toBeTruthy();
    expect(
      within(signals).queryByText("Nothing stands out on this relationship."),
    ).toBeNull();
  });

  it("counts a withheld commercial section as a rule it could not run", () => {
    mount({
      ...emptyButGranted,
      // Two of the four rules read the deal — single-threading and the missing
      // meeting — so a reader without the deal grant is not looking at a
      // relationship with no risks on it.
      commercial: undefined,
      sections_omitted: ["commercial"],
    });

    const signals = section("Signals & risks");
    expect(within(signals).getByText(WITHHELD_SENTENCE)).toBeTruthy();
    expect(
      within(signals).queryByText("Nothing stands out on this relationship."),
    ).toBeNull();
  });

  it("derives no missing-meeting risk from a calendar it may not read", () => {
    mount({
      ...granted,
      // The deal is readable and single-threaded; the calendar is not. The
      // committee is emptied so the rule that CAN run still produces its
      // signal, which is what makes this section short rather than withheld.
      commercial: {
        role: "champion",
        committee: [],
        deal: { deal_id: "d-1", title: "Fleet retrofit" },
      },
      next_meeting: undefined,
      sections_omitted: ["next_meeting"],
    });

    const signals = section("Signals & risks");
    expect(within(signals).queryByText("No next meeting booked")).toBeNull();
    expect(
      within(signals).getByText("Single-threaded on this deal"),
    ).toBeTruthy();
    // One signal out of two reads as the whole finding unless the section says
    // otherwise, and the count of what is missing is not knowable here — the
    // rule never ran.
    expect(within(signals).getByText("Showing part of the list")).toBeTruthy();
  });

  it("books the missing-meeting risk when the calendar is readable and empty", () => {
    mount({
      ...granted,
      commercial: {
        role: "champion",
        committee: [],
        deal: { deal_id: "d-1", title: "Fleet retrofit" },
      },
      next_meeting: undefined,
    });

    const signals = section("Signals & risks");
    expect(within(signals).getByText("No next meeting booked")).toBeTruthy();
    expect(within(signals).queryByText("Showing part of the list")).toBeNull();
  });
});

describe("recent activity", () => {
  it("says nothing was captured when the timeline came back empty", () => {
    mount(emptyButGranted);

    expect(
      within(section("Recent activity")).getByText("Nothing captured yet."),
    ).toBeTruthy();
  });

  it("says the timeline is withheld rather than empty", () => {
    mount(withheld);

    // `view.activities?.data ?? []` is the shape this pins: the default
    // silently converts "you may not see this" into "there is none", and the
    // two are the same empty section on screen.
    const activity = section("Recent activity");
    expect(within(activity).getByText(WITHHELD_SENTENCE)).toBeTruthy();
    expect(within(activity).queryByText("Nothing captured yet.")).toBeNull();
  });

  it("still lists the rows a reader may see", () => {
    mount(granted);

    const activity = section("Recent activity");
    expect(within(activity).getByText("Re: retrofit timeline")).toBeTruthy();
    expect(within(activity).queryByText(WITHHELD_SENTENCE)).toBeNull();
  });
});

describe("the sibling sections governed by their own grants", () => {
  it("says who knows them is withheld rather than that nobody does", () => {
    mount(withheld);

    const who = section("Who knows Dana");
    expect(within(who).getByText(WITHHELD_SENTENCE)).toBeTruthy();
    expect(
      within(who).queryByText("Nobody here has corresponded with them yet."),
    ).toBeNull();
  });

  it("says nobody has corresponded when the network came back empty", () => {
    mount(emptyButGranted);

    expect(
      within(section("Who knows Dana")).getByText(
        "Nobody here has corresponded with them yet.",
      ),
    ).toBeTruthy();
  });

  it("says the employments are withheld rather than that there are none", () => {
    mount(withheld);

    expect(
      within(section("Companies")).getByText(WITHHELD_SENTENCE),
    ).toBeTruthy();
  });

  it("lists the employer a reader may see", () => {
    mount(granted);

    const companies = section("Companies");
    expect(within(companies).getByText("Brandt Automotive GmbH")).toBeTruthy();
    expect(within(companies).queryByText(WITHHELD_SENTENCE)).toBeNull();
  });
});

// What the modal SENDS, and whether the box the reader looked at agreed with it.
// The first attempt at this omitted the field for an untouched box, which fixed
// the server-side rule and broke something worse: the reader saw "not their
// current employer" unticked, saved, and got the opposite — with no way to say
// "no" except ticking and unticking again. So the box states an answer from the
// start, and the answer it states is the one the record takes.
describe("adding a company", () => {
  // Its own instance per test, and told about the fake timers this suite
  // installs: a shared one carries pointer and keyboard state between tests, so
  // the second test to run would inherit the first one's.
  function driver() {
    return userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
  }

  async function openAndPickEmployer(user: ReturnType<typeof driver>) {
    await user.click(screen.getByRole("button", { name: "Add company" }));
    await user.type(screen.getByRole("searchbox"), "emp");
    await user.click(await screen.findByText("Employer GmbH"));
  }

  // `.checked` directly rather than a jest-dom matcher: this suite does not
  // install them, and the assertion is about the box's own state.
  function currentEmployerTicked(): boolean {
    const box = screen.getByRole("checkbox", {
      name: "This is their current employer",
    });
    return (box as HTMLInputElement).checked;
  }

  // Narrowed, not asserted: the recorded body is `unknown` because it came off
  // the wire, and casting it would be this suite claiming a shape it has not
  // checked — which is the whole class of thing these tests exist to catch.
  function asObject(body: unknown, t: string): Record<string, unknown> {
    if (typeof body !== "object" || body === null || Array.isArray(body)) {
      throw new Error(`${t} was not a JSON object: ${JSON.stringify(body)}`);
    }
    return { ...body };
  }

  async function employmentBody(): Promise<Record<string, unknown>> {
    // The mutation is in flight when the click returns, so the assertion waits
    // for the request rather than for anything the modal renders — what was
    // SENT is the only thing under test here.
    await waitFor(() => {
      if (!sent.some((request) => request.path.endsWith("/relationships"))) {
        throw new Error(
          `no employment was posted; requests were ${JSON.stringify(sent)}`,
        );
      }
    });
    const post = sent.find((request) =>
      request.path.endsWith("/relationships"),
    );
    return asObject(post?.body, "the posted employment");
  }

  it("starts ticked for somebody with no current job, and sends that", async () => {
    const user = driver();
    mount(emptyButGranted);
    await screen.findByRole("button", { name: "Add company" });
    await user.click(screen.getByRole("button", { name: "Add company" }));

    // The state the reader sees BEFORE touching anything. This is the assertion
    // the omit-when-untouched version could not make: it showed unticked and
    // wrote primary.
    expect(currentEmployerTicked()).toBe(true);

    await user.type(screen.getByRole("searchbox"), "emp");
    await user.click(await screen.findByText("Employer GmbH"));
    await user.click(screen.getByRole("button", { name: "Create" }));

    expect((await employmentBody()).is_current_primary).toBe(true);
  });

  it("lets the reader say no, and sends that", async () => {
    const user = driver();
    mount(emptyButGranted);
    await screen.findByRole("button", { name: "Add company" });
    await openAndPickEmployer(user);
    await user.click(
      screen.getByRole("checkbox", { name: "This is their current employer" }),
    );
    expect(currentEmployerTicked()).toBe(false);
    await user.click(screen.getByRole("button", { name: "Create" }));

    // `false`, not absent. A reader who unticks a box has decided, and the
    // server must not derive over them.
    const body = await employmentBody();
    expect("is_current_primary" in body).toBe(true);
    expect(body.is_current_primary).toBe(false);
  });

  it("starts unticked for somebody who already has a current job", async () => {
    // The other half of the default, and the reason it is read off the rows
    // rather than hardcoded: which of two employers is the main one is not
    // this modal's to assume.
    const user = driver();
    mount(granted);
    await screen.findByRole("button", { name: "Add company" });
    await user.click(screen.getByRole("button", { name: "Add company" }));

    expect(currentEmployerTicked()).toBe(false);
  });
});
