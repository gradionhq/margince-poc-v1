/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen } from "@testing-library/react";
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

function show(receipt: unknown) {
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

  it("reports a receipt it cannot read as exactly that", async () => {
    show({ entity_type: "profile_field", entity_id: "p-1" });

    expect(
      await screen.findByText(/This receipt could not be read/),
    ).toBeTruthy();
  });
});
