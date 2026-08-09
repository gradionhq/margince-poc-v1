/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { components } from "../api/schema";
import { LocaleProvider } from "../i18n";
import { EvidenceModal } from "./companyevidence";

// The receipt is the affordance that makes the prose above it worth reading, so
// the things it must never do are what get tested: hide a gap, print a
// confidence nobody computed, or merge "read" with "confirmed".

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

type Receipt = components["schemas"]["ClaimEvidence"];

const SITE_READ: Receipt = {
  entity_type: "profile_field",
  entity_id: "p-1",
  source_kind: "site_read",
  produced_by: "site_read:crawler",
  label: "What they offer",
  value: "Load-shifting software",
  excerpt: "We build load-shifting software for industry.",
  identity: { source_url: "https://voltaq.example/about" },
  retrieved_at: "2026-08-01T09:00:00Z",
  confidence: 0.9,
};

function show(
  receipt: unknown,
  // The stepper the citing card supplies. Absent by default, which is the
  // ordinary case: a card with no ordering to give passes none.
  onStep?: (direction: -1 | 1) => void,
) {
  vi.stubGlobal(
    "fetch",
    vi.fn(
      async () =>
        new Response(JSON.stringify(receipt), {
          status: 200,
          headers: { "content-type": "application/json" },
        }),
    ),
  );
  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <LocaleProvider initial="en">
        <EvidenceModal
          orgId="o-1"
          cited={{ entityType: "profile_field", entityId: "p-1" }}
          onClose={() => {}}
          onStep={onStep}
        />
      </LocaleProvider>
    </QueryClientProvider>,
  );
}

describe("where a cited value came from", () => {
  it("shows the value, its origin, and the span it was read from", async () => {
    show(SITE_READ);

    expect(
      await screen.findByText(/What they offer: Load-shifting software/),
    ).toBeTruthy();
    expect(screen.getByText("Read from their website")).toBeTruthy();
    expect(
      screen.getByText(/We build load-shifting software for industry/),
    ).toBeTruthy();
    // A link, so the reader can go and check it rather than retype it.
    expect(
      screen.getByRole("link", { name: "https://voltaq.example/about" }),
    ).toBeTruthy();
  });

  it("names what was not recorded instead of leaving a blank", async () => {
    show({
      ...SITE_READ,
      excerpt: null,
      identity: {},
      gaps: ["source_url", "excerpt"],
    });

    // A claim the reader was told is checkable, with nothing to check it
    // against, is worth saying out loud.
    expect(
      await screen.findByText(/Not recorded: source_url, excerpt/),
    ).toBeTruthy();
  });

  it("prints no confidence for a value a person entered", async () => {
    show({
      entity_type: "profile_field",
      entity_id: "p-1",
      source_kind: "human",
      produced_by: "human:ada",
      label: "What they offer",
      value: "Load-shifting software",
      identity: { actor: "human:ada" },
      gaps: ["verified_at"],
    });

    expect(await screen.findByText("Entered by a person")).toBeTruthy();
    // A person's assertion carries no model confidence, and a percentage
    // beside it would be a number nobody computed.
    expect(screen.queryByText(/confident/)).toBeNull();
  });

  it("keeps read apart from confirmed", async () => {
    show({
      ...SITE_READ,
      retrieved_at: "2026-08-01T09:00:00Z",
      last_verified_at: "2026-08-06T09:00:00Z",
    });

    // Two different assurances: a machine fetched it, and a person agreed.
    // Merging them would let a re-read pass for an approval.
    // Anchored on a digit so this does not also match the "Read from their
    // website" origin badge, which is a different claim entirely.
    expect(await screen.findByText(/^Read \d/)).toBeTruthy();
    expect(screen.getByText(/^Confirmed by a person \d/)).toBeTruthy();
  });

  it("will not make a link out of a scheme the browser should not follow", async () => {
    // `source_url` is a plain text column the site-read pipeline fills from
    // crawled pages, and React does not sanitize href. A javascript: value
    // stored there would run on click — a scraped page choosing what our UI
    // executes.
    show({
      ...SITE_READ,
      identity: { source_url: "javascript:alert(1)" },
    });

    expect(await screen.findByText("javascript:alert(1)")).toBeTruthy();
    // Still shown, because the reader opened this to see where the claim came
    // from and hiding an odd source withholds exactly that. Just not clickable.
    expect(screen.queryByRole("link")).toBeNull();
  });

  it("reports a receipt it cannot read as exactly that", async () => {
    show({ entity_type: "profile_field", entity_id: "p-1" });

    expect(
      await screen.findByText(/This receipt could not be read/),
    ).toBeTruthy();
  });
});

describe("the receipt is a drawer beside the claim, not a box over it", () => {
  // The reader is comparing the receipt against the sentence that cited it.
  // A centred dialog covers that sentence; the drawer leaves it on screen.
  it("opens right-anchored", async () => {
    show(SITE_READ);
    const dialog = await screen.findByRole("dialog");
    expect(dialog.classList.contains("modal-drawer")).toBe(true);
  });

  // Prev/next walks the list the CITING CARD owns. A card that offered no
  // ordering gets no arrows, rather than arrows that step somewhere the
  // reader cannot predict.
  it("offers no stepping when the card gave it no order", async () => {
    show(SITE_READ);
    await screen.findByRole("dialog");
    expect(screen.queryByRole("button", { name: "Next claim" })).toBeNull();
  });

  it("steps forward and back when the card gave it one", async () => {
    const steps: number[] = [];
    show(SITE_READ, (direction) => steps.push(direction));
    await screen.findByRole("dialog");

    await userEvent.click(screen.getByRole("button", { name: "Next claim" }));
    await userEvent.click(
      screen.getByRole("button", { name: "Previous claim" }),
    );
    expect(steps).toEqual([1, -1]);
  });

  // The badge is DERIVED: a machine wrote it and no person has verified it
  // since. Not stored, so the predicate has to be right here.
  it("marks a machine-written claim nobody has confirmed", async () => {
    show(SITE_READ);
    expect(
      await screen.findByText("AI extracted · not yet confirmed"),
    ).toBeTruthy();
  });

  it("drops the mark once a person has verified it", async () => {
    show({ ...SITE_READ, last_verified_at: "2026-08-05T09:00:00Z" });
    await screen.findByRole("dialog");
    expect(screen.queryByText(/AI extracted/)).toBeNull();
  });

  // A person typed it. "AI extracted" would be false of it.
  it("never marks a human-written claim", async () => {
    show({ ...SITE_READ, source_kind: "human", produced_by: "human:u1" });
    await screen.findByRole("dialog");
    expect(screen.queryByText(/AI extracted/)).toBeNull();
  });
});
