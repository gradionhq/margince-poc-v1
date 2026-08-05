/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { components } from "../api/schema";
import { thinRecord } from "./person360";
import { PersonMoments } from "./personmoments";

type Person360 = components["schemas"]["Person360"];

const person = {
  id: "p-1",
  full_name: "Anna Weber",
  version: 1,
} as Person360["person"];

function pageWith(moments: Person360["moments"]): Person360 {
  return {
    as_of: "2026-08-04T09:00:00Z",
    person,
    sections_omitted: [],
    moments,
  } as Person360;
}

const repliedAfterGap: NonNullable<Person360["moments"]>[number] = {
  claim_key: "moment:replied_after_gap",
  kind: "replied_after_gap",
  headline: "They replied after 41 quiet days",
  why_now: "A conversation that had stopped has restarted.",
  confidence: "observed_fact",
  evidence: [
    {
      type: "relationship_change",
      label: "Their reply ended a 41-day silence",
      observed_at: "2026-08-01T09:00:00Z",
    },
  ],
  recommended_action: {
    kind: "draft_reply",
    label: "Draft a reply",
    state: "will_confirm",
  },
};

function renderMoments(view: Person360) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <PersonMoments personId="p-1" view={view} />
    </QueryClientProvider>,
  );
}

describe("PersonMoments", () => {
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  // The card's whole job is to lead with a reason. If the headline and its
  // evidence do not both reach the screen, what renders is an assertion the
  // reader cannot check.
  it("shows the reason, why it is timely, and the evidence behind it", () => {
    renderMoments(pageWith([repliedAfterGap]));

    expect(screen.getByText("They replied after 41 quiet days")).toBeTruthy();
    expect(
      screen.getByText("A conversation that had stopped has restarted."),
    ).toBeTruthy();
    expect(screen.getByText(/Their reply ended a 41-day silence/)).toBeTruthy();
  });

  // A 🟡 action says so BEFORE it is taken. Learning that an action needs
  // approval by pressing it is the surprise the confirm-first posture exists
  // to prevent.
  it("says an action will ask for confirmation before it is taken", () => {
    renderMoments(pageWith([repliedAfterGap]));
    expect(screen.getByText(/will ask you to confirm/)).toBeTruthy();
  });

  // Dismissal has to reach the ledger keyed on the moment's PATH. Keyed on
  // anything derived from the evidence, the moment would come back the next
  // time a mail arrived — which is the version of "the page remembers" that
  // does not.
  it("dismisses by writing a suppressed verdict keyed on the claim path", async () => {
    // The request itself is captured rather than read back off the mock's
    // call tuple, so the assertions below are about what the client SENT.
    let sent: Request | undefined;
    vi.stubGlobal(
      "fetch",
      vi.fn(async (request: Request) => {
        sent = request;
        return new Response(null, { status: 204 });
      }),
    );

    renderMoments(pageWith([repliedAfterGap]));
    fireEvent.click(screen.getByText("Not now"));

    await waitFor(() => expect(sent).toBeDefined());
    const request = sent as Request;
    expect(new URL(request.url).pathname).toBe("/v1/ai/feedback");
    expect(await request.json()).toMatchObject({
      subject_type: "person",
      subject_id: "p-1",
      claim_kind: "signal",
      claim_path: "moment:replied_after_gap",
      verdict: "suppressed",
    });
  });

  // Nothing at all when there is nothing to say. A card reading "no reasons
  // found" takes the most prominent position on the page in order to report
  // that it has nothing to report.
  it("renders nothing rather than an empty card", () => {
    const { container } = renderMoments(pageWith([]));
    expect(container.textContent).toBe("");
  });
});

describe("thinRecord", () => {
  // A withheld section is not an empty one. The thin state SUPPRESSES the
  // ordinary modules, so misreading "you may not see this" as "there is
  // nothing here" costs the reader the rest of the page over data that may
  // well exist.
  it("does not call a record thin when the sections were withheld", () => {
    expect(
      thinRecord({
        as_of: "2026-08-04T09:00:00Z",
        person,
        sections_omitted: ["activities", "network"],
      } as Person360),
    ).toBe(false);
  });

  // Genuinely empty, both sections returned: that IS thin.
  it("calls a record thin when both sections came back empty", () => {
    expect(
      thinRecord({
        as_of: "2026-08-04T09:00:00Z",
        person,
        sections_omitted: [],
        activities: { data: [], page: { has_more: false } },
        network: { colleagues: [] },
      } as unknown as Person360),
    ).toBe(true);
  });

  // A section absent for any other reason is not evidence either way.
  it("does not guess when a section is missing entirely", () => {
    expect(
      thinRecord({
        as_of: "2026-08-04T09:00:00Z",
        person,
        sections_omitted: [],
      } as Person360),
    ).toBe(false);
  });
});
