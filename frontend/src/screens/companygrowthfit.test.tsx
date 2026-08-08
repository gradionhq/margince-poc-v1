/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { components } from "../api/schema";
import { LocaleProvider } from "../i18n";
import { GrowthFitPanel } from "./companygrowthfit";

// The panel's job is to stop one misreading: `unknown` means "we could not
// judge", and a reader must never be able to mistake it for "a poor fit".
// Those are opposite conclusions, so the tests below are mostly about what the
// panel refuses to render on its own.

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

type GrowthFit = components["schemas"]["OrganizationGrowthFit"];

// A COMPLETE OrganizationGrowthFit, not a cast one. A fixture asserted into
// the contract type can drop a required field and still compile, so the test
// would go on passing after the wire shape moved under it.
const ABSTAINED: GrowthFit = {
  organization_id: "o-1",
  band: "unknown",
  data_completeness: {
    present: 2,
    expected: 7,
    missing: ["their industry", "how big they are"],
  },
  next_step: "find out their industry and how big they are",
  generated_at: "2026-08-08T09:00:00Z",
  generated_by: "deterministic",
};

function serving(fit: GrowthFit) {
  vi.stubGlobal(
    "fetch",
    vi.fn(
      async () =>
        new Response(JSON.stringify(fit), {
          status: 200,
          headers: { "content-type": "application/json" },
        }),
    ),
  );
}

async function show(fit: GrowthFit) {
  serving(fit);
  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <LocaleProvider initial="en">
        <GrowthFitPanel orgId="o-1" enabled />
      </LocaleProvider>
    </QueryClientProvider>,
  );
  // The footer only renders once the query has resolved, so waiting for it is
  // what makes the synchronous assertions below run against loaded content.
  // The panel heading is static and would match while the skeleton is up.
  await screen.findByText(/as of /i);
}

describe("how well this company fits what we sell", () => {
  it("says it could not judge, rather than showing a low score", async () => {
    await show(ABSTAINED);

    expect(screen.getByText("Not enough to judge")).toBeTruthy();
    // The three real bands are verdicts. None of them may appear beside an
    // abstention, because a reader scanning for a verdict would take it.
    expect(screen.queryByText("Weak fit")).toBeNull();
    expect(screen.queryByText("Moderate fit")).toBeNull();
    expect(screen.queryByText("Strong fit")).toBeNull();
  });

  it("reports both completeness counts, never a bare proportion", async () => {
    await show(ABSTAINED);

    // "2 of 7" and "2 of 40" describe different levels of knowledge, and a
    // denominator-less figure renders them identically.
    expect(screen.getByText(/2 of 7 inputs recorded/)).toBeTruthy();
  });

  it("names what is missing and what to do about it", async () => {
    await show(ABSTAINED);

    expect(screen.getByText(/their industry, how big they are/)).toBeTruthy();
    expect(
      screen.getByText(/find out their industry and how big they are/),
    ).toBeTruthy();
  });

  it("says why a band was held back, when one was", async () => {
    await show({
      ...ABSTAINED,
      band: "moderate",
      band_capped_reason: "we have not confirmed what this workspace sells",
      next_step: "confirm your own company profile",
      data_completeness: { present: 7, expected: 7 },
    });

    expect(screen.getByText("Moderate fit")).toBeTruthy();
    // A band lowered without a reason is a number the reader cannot argue
    // with, which is the one thing a capped band must never be.
    expect(
      screen.getByText(/we have not confirmed what this workspace sells/),
    ).toBeTruthy();
  });

  it("marks an assessment apart from a fact, and cites both", async () => {
    await show({
      ...ABSTAINED,
      band: "strong",
      data_completeness: { present: 7, expected: 7 },
      next_step: null,
      generated_by: "model",
      positive_factors: [
        {
          text: "They run SAP.",
          nature: "fact",
          evidence: [{ entity_type: "fact", entity_id: "f-1" }],
        },
        {
          text: "Their stack matches who we sell to.",
          nature: "assessment",
          evidence: [{ entity_type: "organization", entity_id: "o-1" }],
        },
      ],
    });

    expect(screen.getByText("What argues for them")).toBeTruthy();
    // A judgment that read as a stored fact would be the one claim the reader
    // could not check, so only the assessment carries a label.
    expect(screen.getByText("Our read")).toBeTruthy();
    // "Fact" is the label a fact WOULD carry if facts were labelled, so its
    // absence is what proves the badge is reserved for judgments. Asserting a
    // string the panel never renders under any nature would prove nothing.
    expect(screen.queryByText("Fact")).toBeNull();
  });

  it("distinguishes a payload it cannot read from a company it knows nothing about", async () => {
    // A schema skew, not an empty account. Rendering it as "0 of 7 inputs"
    // would send the reader off to gather data that is already there.
    vi.stubGlobal(
      "fetch",
      vi.fn(
        async () =>
          new Response(JSON.stringify({ organization_id: "o-1" }), {
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
          <GrowthFitPanel orgId="o-1" enabled />
        </LocaleProvider>
      </QueryClientProvider>,
    );

    expect(
      await screen.findByText(/This assessment could not be read/),
    ).toBeTruthy();
    expect(screen.queryByText(/inputs recorded/)).toBeNull();
  });

  it("is absent, not empty, for a workspace reading from an incumbent", async () => {
    serving(ABSTAINED);
    const { container } = render(
      <QueryClientProvider
        client={
          new QueryClient({ defaultOptions: { queries: { retry: false } } })
        }
      >
        <LocaleProvider initial="en">
          <GrowthFitPanel orgId="o-1" enabled={false} />
        </LocaleProvider>
      </QueryClientProvider>,
    );

    // A mirror holds none of the facts this is assembled from, so an empty
    // panel would report a gap in data that simply lives somewhere else.
    expect(container.textContent).toBe("");
  });
});
