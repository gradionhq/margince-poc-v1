/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { components } from "../api/schema";
import { LocaleProvider } from "../i18n";
import { EnrichedFields } from "./personcorrections";

// The card where the page stops asserting and starts asking. Three things it
// owes the reader, none of which the surface enforced:
//
//   - a control is offered only to somebody the server will admit. `POST
//     /ai/feedback` demands `update` on the subject, so a read seat pressing
//     "That is right" was promised a verdict and handed a 403.
//   - a control is offered only where it can still apply. A claim a human has
//     already corrected has been settled by the only party who settles it.
//   - what the editor opens on is what the field says NOW. Text a reader
//     abandoned must not come back on the next open and be saved over the
//     value they kept.

type Person360 = components["schemas"]["Person360"];
type ProfileField = components["schemas"]["PersonProfileField"];

const person: components["schemas"]["Person"] = {
  id: "p-1",
  full_name: "Dana Buyer",
  source: "manual",
  captured_by: "human:u-1",
  created_at: "2026-06-01T08:00:00Z",
  updated_at: "2026-08-01T08:00:00Z",
};

// Typed against the contract rather than cast into it: a fixture that dropped a
// required field would still compile as a cast, and the test would keep passing
// after the wire shape moved under it.
function field(over: Partial<ProfileField>): ProfileField {
  return {
    field: "title",
    value: "Head of Procurement",
    evidence_snippet: "Head of Procurement, Brandt Automotive GmbH",
    source: "capture_enrich",
    captured_by: "agent:enrich",
    captured_at: "2026-08-01T08:00:00Z",
    ...over,
  };
}

function view(fields: ProfileField[]): Person360 {
  return {
    as_of: "2026-08-18T09:00:00Z",
    person,
    sections_omitted: [],
    profile_fields: fields,
  };
}

// /me refuses a payload without `user`, so a fixture carrying only the
// authorization block would leave every control hidden for a reason no test
// here is about.
function stubMe(authorization: unknown) {
  vi.stubGlobal(
    "fetch",
    vi.fn(
      async () =>
        new Response(
          JSON.stringify({
            user: { id: "u-1", email: "rep@example.com", name: "Demo Rep" },
            authorization,
          }),
          { status: 200, headers: { "content-type": "application/json" } },
        ),
    ),
  );
}

const MAY_CORRECT = {
  seat_type: "full",
  objects: { person: { update: true } },
};

function renderFields(fields: ProfileField[]) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">
        <EnrichedFields personId="p-1" view={view(fields)} />
      </LocaleProvider>
    </QueryClientProvider>,
  );
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("who may correct what a machine read", () => {
  it("offers the controls to a reader who holds the subject-update grant", async () => {
    stubMe(MAY_CORRECT);
    renderFields([field({})]);

    expect(await screen.findByRole("button", { name: "Correct" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "That is right" })).toBeTruthy();
  });

  it("shows the evidence but no controls to a reader without the grant", async () => {
    stubMe({ seat_type: "full", objects: {} });
    renderFields([field({})]);

    // The reading is not withheld — evidence is a read, and the whole point of
    // this card is that a claim can be checked against its source.
    expect(await screen.findByText("Head of Procurement")).toBeTruthy();
    await waitFor(() =>
      expect(screen.queryByRole("button", { name: "Correct" })).toBeNull(),
    );
    expect(screen.queryByRole("button", { name: "That is right" })).toBeNull();
  });

  it("shows no controls on a read seat, whatever the grant says", async () => {
    // The server clamps on the HTTP method before RBAC, so a read seat holding
    // every grant still cannot write. A control offered here is a refusal
    // waiting to happen.
    stubMe({ seat_type: "read", objects: { person: { update: true } } });
    renderFields([field({})]);

    expect(await screen.findByText("Head of Procurement")).toBeTruthy();
    await waitFor(() =>
      expect(screen.queryByRole("button", { name: "Correct" })).toBeNull(),
    );
    expect(screen.queryByRole("button", { name: "That is right" })).toBeNull();
  });
});

describe("which verdicts are still open", () => {
  it("offers Confirm only while nobody has ruled on the claim", async () => {
    stubMe(MAY_CORRECT);
    renderFields([
      field({ field: "title", value: "Head of Procurement" }),
      field({ field: "role", value: "Buyer", verdict: "corrected" }),
      field({ field: "phone", value: "+49 30 123", verdict: "confirmed" }),
    ]);

    // One open claim, so exactly one Confirm. A corrected field carries the
    // human's own value, and asking them to confirm a reading the page no
    // longer shows is asking about something that is not there.
    await waitFor(() =>
      expect(
        screen.getAllByRole("button", { name: "That is right" }),
      ).toHaveLength(1),
    );
    // Correct stays on every field: a value already settled once can still be
    // settled differently.
    expect(screen.getAllByRole("button", { name: "Correct" })).toHaveLength(3);
  });
});

describe("what the editor opens on", () => {
  it("opens on the value the field carries now, not on text the reader abandoned", async () => {
    const user = userEvent.setup();
    stubMe(MAY_CORRECT);
    renderFields([field({})]);

    await user.click(await screen.findByRole("button", { name: "Correct" }));
    const input = screen.getByRole("textbox", { name: "Title" });
    await user.clear(input);
    await user.type(input, "Typed by mistake");
    await user.click(screen.getByRole("button", { name: "Cancel" }));

    await user.click(screen.getByRole("button", { name: "Correct" }));
    // Reopening on the abandoned draft is worse than losing it: the reader is
    // shown text they discarded, and pressing Save writes it over the value
    // they chose to keep.
    expect(screen.getByDisplayValue("Head of Procurement")).toBeTruthy();
    expect(screen.queryByDisplayValue("Typed by mistake")).toBeNull();
  });
});
