// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

/** @vitest-environment jsdom */
import { cleanup, render as rtlRender, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { components } from "../api/schema";
import { LocaleProvider } from "../i18n";
import { EMPTY_DRAFT } from "./onboarding";
import { CompanyActArtifact } from "./onboarding-conversation/artifact";
import { CoverageCard, DossierCard } from "./onboarding-live-panel";

// The coverage card's contract with the reader: a collapsed card states its
// own count so shutting it is informed, and every count is the length of the
// array beside it.

type SitePage = components["schemas"]["CompanySiteReadPage"];
type CompanySiteRead = components["schemas"]["CompanySiteRead"];

function render(ui: ReactNode) {
  return rtlRender(<LocaleProvider initial="en">{ui}</LocaleProvider>);
}

type ColdStartField = components["schemas"]["ColdStartField"];

function coldField(
  name: ColdStartField["field"],
  value: string,
): ColdStartField {
  return {
    field: name,
    value,
    confidence: 0.9,
    evidence_snippet: `the site says ${value}`,
    source_kind: "url",
    source_url: "https://example.test",
  };
}

function read(overrides: Partial<CompanySiteRead> = {}): CompanySiteRead {
  return {
    id: "11111111-1111-4111-8111-111111111111",
    target_kind: "onboarding",
    root_url: "https://example.test",
    status: "ready",
    status_code: null,
    status_detail: null,
    next_attempt_at: null,
    pages: [{ url: "https://example.test", status: "fetched" }],
    profile_fields: [],
    facts: [],
    comparisons: [],
    people: [],
    warnings: [],
    draft_version: 1,
    proposal_hash: "h1",
    created_at: "2026-07-01T00:00:00Z",
    updated_at: "2026-07-01T00:05:00Z",
    ...overrides,
  };
}

afterEach(cleanup);

describe("DossierCard", () => {
  it("starts collapsed and flips aria-expanded when opened", async () => {
    render(
      <DossierCard title="Company identity" count="3 fields">
        <p>Example Holding GmbH</p>
      </DossierCard>,
    );

    const toggle = screen.getByRole("button", { name: /Review/ });
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(screen.queryByText("Example Holding GmbH")).toBeNull();
    // The count is the whole reason a shut card is safe to leave shut.
    expect(screen.getByText("3 fields")).toBeTruthy();

    await userEvent.click(toggle);

    expect(
      screen
        .getByRole("button", { name: /Hide/ })
        .getAttribute("aria-expanded"),
    ).toBe("true");
    expect(screen.getByText("Example Holding GmbH")).toBeTruthy();
  });
});

describe("CoverageCard", () => {
  const pages: readonly SitePage[] = [
    { url: "https://example.test", status: "fetched" },
    {
      url: "https://example.test/private",
      status: "skipped",
      reason: "robots.txt disallows it",
    },
    { url: "https://example.test/jobs", status: "failed" },
  ];

  it("derives a row from every warning, skipped page and failed page", async () => {
    render(<CoverageCard pages={pages} warnings={["Sitemap was empty"]} />);

    expect(screen.getByText("1 read · 1 skipped")).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: /Review/ }));

    expect(screen.getByText("Warning")).toBeTruthy();
    expect(screen.getByText("Sitemap was empty")).toBeTruthy();
    expect(screen.getByText("Skipped")).toBeTruthy();
    expect(screen.getByText("robots.txt disallows it")).toBeTruthy();
    expect(screen.getByText("Could not read")).toBeTruthy();
    expect(screen.getByText("https://example.test/jobs")).toBeTruthy();
    // A failed page with no stated reason says so rather than showing blank.
    expect(screen.getByText("no reason recorded")).toBeTruthy();
  });

  it("says the read was cut short, so the page counts cannot read as the whole site", async () => {
    render(
      <CoverageCard pages={pages} warnings={[]} stoppedReason="page_cap" />,
    );
    await userEvent.click(screen.getByRole("button", { name: /Review/ }));

    expect(screen.getByText("Stopped early")).toBeTruthy();
    expect(
      screen.getByText(
        "I reached the page limit for one read, so there is more of your site I did not open.",
      ),
    ).toBeTruthy();
  });

  it("claims no early stop for a read that simply ran out of site to open", async () => {
    render(<CoverageCard pages={pages} warnings={[]} />);
    await userEvent.click(screen.getByRole("button", { name: /Review/ }));

    expect(screen.queryByText("Stopped early")).toBeNull();
  });

  it("marks each gap with its own kind, so a skip is not painted as a failure", async () => {
    render(<CoverageCard pages={pages} warnings={["Sitemap was empty"]} />);
    await userEvent.click(screen.getByRole("button", { name: /Review/ }));

    const kinds = Array.from(
      document.querySelectorAll(".ob-live-coverage"),
    ).map((row) => row.getAttribute("data-kind"));
    // Warnings first, then skipped pages, then failed ones — and the three
    // never collapse into one alarm.
    expect(kinds).toEqual(["warn", "skip", "fail"]);
  });

  it("names which page was missed when the read says what kind it was", async () => {
    render(
      <CoverageCard
        pages={[
          {
            url: "https://example.test/team",
            status: "skipped",
            kind: "team",
            reason: "robots.txt disallows it",
          },
        ]}
        warnings={[]}
      />,
    );
    await userEvent.click(screen.getByRole("button", { name: /Review/ }));

    // The page kind is what a reader can act on; the URL stays as the
    // corroborating detail beside it.
    expect(screen.getByText("Team")).toBeTruthy();
    expect(screen.getByText("https://example.test/team")).toBeTruthy();
    expect(screen.getByText("Skipped")).toBeTruthy();
  });

  it("falls back to the status label for a page kind that names nothing", async () => {
    render(
      <CoverageCard
        pages={[
          { url: "https://example.test/a", status: "skipped", kind: null },
          { url: "https://example.test/b", status: "skipped", kind: "other" },
        ]}
        warnings={[]}
      />,
    );
    await userEvent.click(screen.getByRole("button", { name: /Review/ }));

    // "Other" and an absent kind say nothing the status label does not, so no
    // name line is rendered at all rather than one reading "Other".
    expect(document.querySelectorAll(".ob-live-coverage")).toHaveLength(2);
    expect(document.querySelector(".ob-live-coverage-name")).toBeNull();
    expect(screen.getAllByText("Skipped")).toHaveLength(2);
  });

  it("says so plainly when nothing was skipped and nothing failed", async () => {
    render(
      <CoverageCard
        pages={[{ url: "https://example.test", status: "fetched" }]}
        warnings={[]}
      />,
    );

    expect(screen.getByText("1 read · 0 skipped")).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: /Review/ }));
    expect(
      screen.getByText(
        "Every page I tried came back. Nothing was skipped and nothing failed.",
      ),
    ).toBeTruthy();
  });
});

// The panel's production host, exercised for one thing only: which cards the
// dossier is composed of. Everything else about the artifact is covered where
// it lives.
describe("CompanyActArtifact", () => {
  function artifact(site: CompanySiteRead) {
    return (
      <CompanyActArtifact
        mode="dossier"
        manual={false}
        read={site}
        draft={EMPTY_DRAFT}
        setField={vi.fn()}
        onPickEntity={vi.fn()}
        selectedFactKeys={[]}
        setSelectedFactKeys={vi.fn()}
        missingRequired={[]}
        highlight={null}
        onSwitchMode={vi.fn()}
        onConfirm={vi.fn()}
        confirmPending={false}
        confirmDisabled={false}
        saveError={null}
      />
    );
  }

  it("waits for the review instead of staging a dossier of its own", () => {
    // One scene at a time: a finished read whose review scene has not been
    // handed over yet is a wait, never a second surface the reader is about
    // to be pulled away from. The facts live in the review card.
    render(
      artifact(
        read({
          profile_fields: [coldField("display_name", "Example")],
          facts: [],
        }),
      ),
    );

    expect(screen.getByRole("status")).toBeTruthy();
    expect(screen.queryByRole("heading", { name: "Facts" })).toBeNull();
  });
});
