// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

/** @vitest-environment jsdom */
import "@testing-library/jest-dom/vitest";

import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import {
  cleanup,
  render as rtlRender,
  screen,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { components } from "../../api/schema";
import { Avatar } from "../../design-system/atoms";
import { LocaleProvider } from "../../i18n";
import { EMPTY_DRAFT } from "../onboarding";
import { CompanyConfirmCard } from "./confirm-card";

// The review board's three honesty claims, each of which reads as a bug to a
// human the moment it stops holding:
//  - a field the read came back without is NAMED as omitted with a reason the
//    read can support, so "not published", "never looked" and "withheld" are
//    three different sentences rather than three identical blank boxes;
//  - the anchor company draws the same deterministic mark here as everywhere
//    else it appears;
//  - the one screen that renders the blocking tier beside the advisory one
//    does not paint the advisory one in the blocking tier's red.

type CompanySiteRead = components["schemas"]["CompanySiteRead"];
type Proposal = components["schemas"]["OnboardingCompanyProposal"];

const COMPANY = "Gradion GmbH";

// The backend's own abstention sentence, verbatim off the wire: the legal
// gate drops the legal block and says so in `warnings`, and nothing on the
// wire ties that sentence to a field — which is exactly why the board may
// quote it and must not paraphrase it into a cause of its own.
const GATE_WARNING =
  "disagreeing legal pages: the domain hosts more than one entity — the legal-field override was dropped";

function read(over: Partial<CompanySiteRead> = {}): CompanySiteRead {
  return {
    id: "00000000-0000-4000-8000-000000000001",
    target_kind: "onboarding",
    root_url: "https://gradion.test",
    status: "ready",
    status_code: null,
    status_detail: null,
    next_attempt_at: null,
    pages: [
      { url: "https://gradion.test/", status: "fetched", kind: "home" },
      {
        url: "https://gradion.test/impressum",
        status: "fetched",
        kind: "impressum",
      },
    ],
    profile_fields: [],
    facts: [],
    comparisons: [],
    people: [],
    warnings: [],
    draft_version: 1,
    proposal_hash: "hash",
    created_at: "2026-08-05T09:00:00Z",
    updated_at: "2026-08-05T09:00:00Z",
    ...over,
  };
}

// One weakly-grounded field on the board (industry, at a score the shared
// thresholds band as "low") plus the company name, so the surface carries the
// advisory tier and the identity card at once.
const PROPOSAL: Proposal = {
  ready: true,
  fields: [
    {
      field: "industry",
      value: "Logistics software",
      confidence: 0.3,
      evidence_snippet: "We build software for freight forwarders.",
      source_url: "https://gradion.test/about",
    },
  ],
  facts: [],
  open_questions: [],
  remaining_required_fields: [],
  draft_version: 1,
  proposal_hash: "hash",
};

const DRAFT = {
  ...EMPTY_DRAFT,
  values: {
    ...EMPTY_DRAFT.values,
    display_name: COMPANY,
    industry: "Logistics software",
  },
};

function renderCard(site: CompanySiteRead | null) {
  return render(
    <>
      <CompanyConfirmCard
        proposal={PROPOSAL}
        draft={DRAFT}
        answers={[]}
        read={site}
        selectedFactKeys={[]}
        setSelectedFactKeys={vi.fn()}
        missingRequired={[]}
        setField={vi.fn()}
        onAcceptAll={vi.fn()}
        pending={false}
        authorizing={false}
        error={null}
      />
      {/* The same mark the organizations list and the connections graph draw
          for this company, rendered beside the board so the claim under test
          is "the two agree" rather than a hash recomputed in the test. */}
      <span data-testid="reference-mark">
        <Avatar name={COMPANY} tinted />
      </span>
    </>,
  );
}

function render(ui: ReactNode) {
  return rtlRender(<LocaleProvider initial="en">{ui}</LocaleProvider>);
}

function row(field: string): HTMLElement {
  const node = document.getElementById(`ob-triage-row-${field}`);
  if (node === null) {
    throw new Error(`the board rendered no review row for ${field}`);
  }
  return node;
}

function only(selector: string): Element {
  const node = document.querySelector(selector);
  if (node === null) {
    throw new Error(`nothing matched ${selector}`);
  }
  return node;
}

// The tone class Avatar derives from the name. Read off the element rather
// than recomputed here: a test that re-implements the hash passes even when
// the two surfaces disagree, which is the only thing worth asserting.
function toneOf(node: Element): string {
  const tone = [...node.classList].find((name) => /^avatar-t\d+$/.test(name));
  if (tone === undefined) {
    throw new Error(`no deterministic tone on ${node.className}`);
  }
  return tone;
}

afterEach(cleanup);

describe("a field the read did not return", () => {
  it("names it as omitted and gives the reason the crawl can support", () => {
    renderCard(read());

    // The read fetched an imprint page and the field still came back empty:
    // the site does not state it, which is a different fact from not having
    // looked — and both are different from an unexplained empty box.
    expect(row("legal_name")).toHaveTextContent("Omitted, not guessed");
    expect(
      screen.getByText(
        "Registered legal name: Not stated on your legal or imprint page. Yours to add.",
      ),
    ).toBeInTheDocument();
    expect(
      screen.getByText(
        "Registered address: Not stated on your legal or imprint page. Yours to add.",
      ),
    ).toBeInTheDocument();
    expect(
      screen.getByText(
        "Register / VAT ID: Not stated on your legal or imprint page. Yours to add.",
      ),
    ).toBeInTheDocument();
  });

  it("says only that nothing was read when no imprint page was reached", () => {
    renderCard(
      read({
        pages: [{ url: "https://gradion.test/", status: "fetched" }],
      }),
    );

    expect(
      screen.getByText(
        "Registered address: I did not find a legal or imprint page on your site to check. Yours to add.",
      ),
    ).toBeInTheDocument();
  });

  it("hangs no crawl-wide warning on a field, and still gives the field its own reason", () => {
    renderCard(read({ warnings: [GATE_WARNING] }));

    // The warning is about the crawl, not about any one field: nothing on the
    // wire ties it to legal_name rather than to register_vat or to a page
    // neither of them came from. Quoting it beside a field would name a cause
    // for that field the read never gave.
    for (const field of ["legal_name", "registered_address", "register_vat"]) {
      expect(row(field)).not.toHaveTextContent(GATE_WARNING);
    }
    expect(row("history")).not.toHaveTextContent(GATE_WARNING);
    // The reason the row CAN support is still there.
    expect(row("legal_name")).toHaveTextContent(
      "Not stated on your legal or imprint page.",
    );
  });

  it("still reports the warning in full, under the coverage card that owns it", async () => {
    const user = userEvent.setup();
    renderCard(read({ warnings: [GATE_WARNING] }));

    // Nothing is swallowed by keeping it off the fields: the read's own
    // account of what it could not settle sits below the board, one click
    // into the card that exists to carry it, whole and under its own heading.
    await user.click(
      screen.getByRole("button", { name: /What I read, and what I skipped/ }),
    );

    const quoted = screen.getByText(GATE_WARNING);
    expect(quoted.closest(".ob-triage-readmore")).not.toBeNull();
    expect(quoted.closest(".ob-live-coverage")).toHaveTextContent("Warning");
  });

  it("claims no omission at all when no read ever ran", () => {
    renderCard(null);

    expect(screen.queryAllByText("Omitted, not guessed")).toHaveLength(0);
    // The field is still on the board and still fillable — it is simply not
    // something anyone looked for yet.
    expect(row("legal_name")).toBeInTheDocument();
  });

  // The notice is a claim about what the crawl DID, and the wire accounts for
  // exactly one group of fields: the legal trio, through the pages and
  // candidates the read reports. For every other blank field it carries
  // silence — which covers a crawl that stopped early, an extraction that
  // abstained, and a value the read proposed that the human then cleared. A
  // box over that silence would name a cause the read never gave, which is
  // the one thing the notice exists to prevent.
  it("names as omitted only the fields the read itself accounts for", () => {
    renderCard(read());

    const omitted = [...document.querySelectorAll(".ob-triage-omitted")].map(
      (notice) => notice.closest("li")?.id,
    );
    expect(omitted.sort()).toEqual([
      "ob-triage-row-legal_name",
      "ob-triage-row-register_vat",
      "ob-triage-row-registered_address",
    ]);
  });

  it("leaves a field the read has no account of as plainly empty, and fillable", async () => {
    const user = userEvent.setup();
    renderCard(read());

    // Nothing in the read speaks to why the offer summary, specifically, came
    // back missing, so the row states nothing about the site at all.
    const offer = row("offer_summary");
    expect(offer.querySelector(".ob-triage-omitted")).toBeNull();
    expect(within(offer).getByRole("textbox")).toHaveValue("");

    // Collapsed, the placeholder standing in for the value says only that the
    // row is empty — the same line the manual path shows, where nothing ever
    // read a site to have missed it.
    await user.click(within(offer).getByRole("button", { name: "Show less" }));
    expect(offer).toHaveTextContent("Nothing here yet. Yours to add.");
  });
});

describe("the identity card's mark", () => {
  it("draws the same deterministic monogram the rest of the app draws", () => {
    renderCard(read());

    const card = only(".ob-company-card .avatar");
    const reference = only('[data-testid="reference-mark"] .avatar');

    expect(toneOf(card)).toBe(toneOf(reference));
    expect(card).toHaveTextContent("GG");
  });
});

describe("the advisory tier beside the blocking one", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const sheet = readFileSync(join(here, "conversation.css"), "utf8");

  it("keeps the weak-confidence dot out of the board rather than in danger red", () => {
    // The selector the rule needs to reach: the shared meter renders inside
    // the row, so a row-scoped rule governs it without touching the eight
    // other ConfidenceMeter call sites.
    renderCard(read());
    expect(only(".ob-triage-row .confidence-low")).toBeInTheDocument();

    // jsdom loads no stylesheet, so the rule is read off the file rather than
    // off the element. Matched by its selector and its one declaration, in any
    // spacing a formatter may choose: reindenting the sheet is not a change to
    // what it does, and deleting the rule or the `display: none` still fails.
    expect(sheet).toMatch(
      /\.ob-triage-row\s+\.confidence-low\s+\.dot\s*\{[^}]*display\s*:\s*none\s*[;}]/,
    );
  });

  it("still states the weak band in words and in figures", async () => {
    const user = userEvent.setup();
    renderCard(read());

    // Nothing is hidden by dropping the dot. The open row names the band;
    // collapsing it back to a summary line prints the score beside it.
    expect(row("industry")).toHaveTextContent("low");
    await user.click(
      within(row("industry")).getByRole("button", { name: "Show less" }),
    );

    expect(row("industry")).toHaveTextContent("low");
    expect(row("industry")).toHaveTextContent("30");
  });
});

// A value the human settled by picking one of the read's own legal-entity
// candidates came off a page — so its words are quotable — but nothing ever
// scored it: the entity lane carries no confidence on the wire. The board owes
// that value the quote and NO band; a meter here would be a number the product
// minted for itself, and an empty chip would be evidence it does not have.
describe("a value chosen from the read's own candidates", () => {
  function renderChosen(snippet: string | undefined) {
    return render(
      <CompanyConfirmCard
        proposal={PROPOSAL}
        draft={{
          ...DRAFT,
          values: { ...DRAFT.values, legal_name: COMPANY },
          grounded: {
            legal_name: {
              field: "legal_name",
              value: COMPANY,
              evidence_snippet: snippet,
              source_kind: "url",
              source_url: "https://gradion.test/impressum",
            },
          },
        }}
        answers={[]}
        read={read()}
        selectedFactKeys={[]}
        setSelectedFactKeys={vi.fn()}
        missingRequired={[]}
        setField={vi.fn()}
        onAcceptAll={vi.fn()}
        pending={false}
        authorizing={false}
        error={null}
      />,
    );
  }

  // The row settles, so it opens on demand rather than by default.
  async function openLegalName() {
    await userEvent
      .setup()
      .click(within(row("legal_name")).getByRole("button"));
  }

  it("shows the page's own words with no confidence meter beside them", async () => {
    renderChosen("Gradion GmbH · HRB 12345 B");
    await openLegalName();

    const legalName = row("legal_name");
    expect(
      within(legalName).getByRole("button", { name: /gradion.test/ }),
    ).toBeInTheDocument();
    // Not a band, not a zero: nothing graded this value, so the row says who
    // it came from instead of how sure anything is.
    expect(legalName.querySelector(".confidence")).toBeNull();
    expect(legalName).toHaveTextContent("read from your legal notice");
  });

  it("shows no evidence line at all when the candidate printed no quote", async () => {
    renderChosen(undefined);
    await openLegalName();

    const legalName = row("legal_name");
    expect(legalName.querySelector(".evidence-chip")).toBeNull();
    expect(legalName.querySelector(".confidence")).toBeNull();
    expect(legalName).toHaveTextContent("read from your legal notice");
  });
});
