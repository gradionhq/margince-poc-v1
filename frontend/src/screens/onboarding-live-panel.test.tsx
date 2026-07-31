// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

/** @vitest-environment jsdom */
import {
  cleanup,
  fireEvent,
  render as rtlRender,
  screen,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { components } from "../api/schema";
import { LocaleProvider } from "../i18n";
import {
  CoverageCard,
  DossierCard,
  EntityDecisionCard,
  FieldRow,
  OnboardingLivePanel,
  type PanelField,
  PeopleCard,
  StepBlock,
} from "./onboarding-live-panel";

// The live panel's contract with the reader: a collapsed card states its own
// count so shutting it is informed; a step nobody has reached is absent
// rather than empty; the one card a human must answer cannot be folded away;
// and every count is the length of the array beside it.

type SitePage = components["schemas"]["CompanySiteReadPage"];
type SitePerson = components["schemas"]["CompanySiteReadPerson"];
type LegalEntity = components["schemas"]["CompanySiteReadLegalEntity"];
type CompanySiteRead = components["schemas"]["CompanySiteRead"];

function render(ui: ReactNode) {
  return rtlRender(<LocaleProvider initial="en">{ui}</LocaleProvider>);
}

afterEach(cleanup);

function field(overrides: Partial<PanelField> = {}): PanelField {
  return {
    field: "offer_summary",
    value: "Fixed-price migrations for mid-market ERP",
    confidence: 0.91,
    evidence_snippet: "We move mid-market ERP estates on a fixed price.",
    source_url: "https://example.test/services",
    ...overrides,
  };
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

const PERSON: SitePerson = {
  name: "Ada Ritter",
  role: "Head of Delivery",
  published_email: "ada@example.test",
  linkedin_url: null,
  evidence_snippet: "Ada Ritter, Head of Delivery",
  evidence_url: "https://example.test/team",
};

const ENTITIES: readonly LegalEntity[] = [
  {
    name: "Example Holding GmbH",
    registered_address: "Hauptstrasse 1, Berlin",
    register_number: "HRB 12345",
    evidence_snippet: "Example Holding GmbH, Hauptstrasse 1",
    source_url: "https://example.test/impressum",
  },
  {
    name: "Example Services GmbH",
    register_number: "HRB 67890",
    source_url: "https://example.test/impressum",
  },
];

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

  it("gives the needs-a-decision variant no toggle and leaves it open", () => {
    render(
      <DossierCard title="Which legal entity should I use?" needsDecision>
        <p>Pick one</p>
      </DossierCard>,
    );

    expect(screen.getByText("Pick one")).toBeTruthy();
    expect(screen.queryByRole("button")).toBeNull();
  });
});

describe("StepBlock", () => {
  it("renders nothing for a step the conversation has not reached", () => {
    const { container } = render(
      <StepBlock n={2} title="Your writing voice" state="waiting">
        <p>Voice body</p>
      </StepBlock>,
    );

    expect(container.innerHTML).toBe("");
    expect(screen.queryByText("Voice body")).toBeNull();
  });

  it("names the step and its state when it is in progress", () => {
    render(
      <StepBlock n={2} title="Your writing voice" state="now">
        <p>Voice body</p>
      </StepBlock>,
    );

    expect(screen.getByText("Your writing voice")).toBeTruthy();
    expect(screen.getByText("in progress")).toBeTruthy();
    expect(screen.getByText("Voice body")).toBeTruthy();
  });
});

describe("FieldRow", () => {
  it("quotes the evidence behind a value it did find", () => {
    render(<FieldRow field={field()} />);

    expect(
      screen.getByText("Fixed-price migrations for mid-market ERP"),
    ).toBeTruthy();
    expect(
      screen.getByText(/We move mid-market ERP estates on a fixed price\./),
    ).toBeTruthy();
    expect(screen.getByText("91%")).toBeTruthy();
  });

  it("marks a field with no value instead of showing a blank line", () => {
    render(<FieldRow field={field({ value: "  " })} />);

    expect(screen.getByText("—")).toBeTruthy();
  });

  it("renders no evidence chip when the evidence has no source to cite", () => {
    // source_kind text/self_description carries no URL, so there is nothing
    // to point a chip at — an unsourced chip would be worse than none.
    render(<FieldRow field={field({ source_url: null })} />);

    expect(
      screen.queryByText(/We move mid-market ERP estates on a fixed price\./),
    ).toBeNull();
    expect(
      screen.getByText("Fixed-price migrations for mid-market ERP"),
    ).toBeTruthy();
  });
});

describe("PeopleCard", () => {
  it("states why nobody was proposed rather than showing an empty list", async () => {
    render(<PeopleCard people={[]} />);

    expect(screen.getByText("0 lead proposals")).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: /Review/ }));
    expect(
      screen.getByText(
        "Nobody yet. I only propose a person when the page gives a name and a role.",
      ),
    ).toBeTruthy();
  });

  it("counts the people it actually lists", async () => {
    render(<PeopleCard people={[PERSON, { ...PERSON, name: "Bo Lange" }]} />);

    expect(screen.getByText("2 lead proposals")).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: /Review/ }));
    expect(screen.getByText("Ada Ritter")).toBeTruthy();
    expect(screen.getByText("Bo Lange")).toBeTruthy();
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

describe("EntityDecisionCard", () => {
  it("refuses to confirm until an entity is picked", () => {
    const onConfirm = vi.fn();
    render(
      <EntityDecisionCard
        entities={ENTITIES}
        chosen={null}
        onConfirm={onConfirm}
        onDecline={vi.fn()}
      />,
    );

    const confirm = screen.getByRole("button", { name: "Confirm" });
    expect(confirm.hasAttribute("disabled")).toBe(true);
    // A dispatched click bypasses the pointer, so the handler has to refuse
    // an empty choice on its own.
    fireEvent.click(confirm);
    expect(onConfirm).not.toHaveBeenCalled();
  });

  it("confirms the entity the human selected", async () => {
    const onConfirm = vi.fn();
    render(
      <EntityDecisionCard
        entities={ENTITIES}
        chosen={null}
        onConfirm={onConfirm}
        onDecline={vi.fn()}
      />,
    );

    await userEvent.click(
      screen.getByRole("radio", { name: /Example Services GmbH/ }),
    );
    const confirm = screen.getByRole("button", { name: "Confirm" });
    expect(confirm.hasAttribute("disabled")).toBe(false);
    await userEvent.click(confirm);

    expect(onConfirm).toHaveBeenCalledWith("Example Services GmbH");
  });

  it("groups the radios so only one entity can be chosen", () => {
    render(
      <EntityDecisionCard
        entities={ENTITIES}
        chosen={null}
        onConfirm={vi.fn()}
        onDecline={vi.fn()}
      />,
    );

    const names = screen
      .getAllByRole("radio")
      .map((radio) => radio.getAttribute("name"));
    expect(new Set(names).size).toBe(1);
    expect(names[0]).toBeTruthy();
  });

  it("declines through the existing skip action", async () => {
    const onDecline = vi.fn();
    render(
      <EntityDecisionCard
        entities={ENTITIES}
        chosen={null}
        onConfirm={vi.fn()}
        onDecline={onDecline}
      />,
    );

    await userEvent.click(
      screen.getByRole("button", { name: "Skip this - I will set it myself" }),
    );
    expect(onDecline).toHaveBeenCalledTimes(1);
  });

  it("collapses to a done card stating the answer once it is given", () => {
    render(
      <EntityDecisionCard
        entities={ENTITIES}
        chosen="Example Holding GmbH"
        onConfirm={vi.fn()}
        onDecline={vi.fn()}
      />,
    );

    expect(screen.queryByRole("radio")).toBeNull();
    expect(screen.getByRole("button", { name: /Review/ })).toBeTruthy();
  });

  it("renders nothing when the site named no legal entity", () => {
    const { container } = render(
      <EntityDecisionCard
        entities={[]}
        chosen={null}
        onConfirm={vi.fn()}
        onDecline={vi.fn()}
      />,
    );

    expect(container.innerHTML).toBe("");
  });
});

describe("OnboardingLivePanel", () => {
  it("shows no cards at all while the read is still running", () => {
    render(
      <OnboardingLivePanel
        host="example.test"
        done={false}
        read={null}
        entityChoice={null}
        onConfirmEntity={vi.fn()}
        onDeclineEntity={vi.fn()}
        voiceState="waiting"
        connectState="waiting"
      />,
    );

    expect(screen.getByText("Reading example.test")).toBeTruthy();
    expect(
      screen.getByText(
        "Nothing is saved yet. I will show you everything when I am done.",
      ),
    ).toBeTruthy();
    expect(screen.queryByRole("button", { name: /Review/ })).toBeNull();
  });

  it("counts the fields it renders rather than a fixed number", async () => {
    render(
      <OnboardingLivePanel
        host="example.test"
        done
        read={read({
          profile_fields: [
            coldField("display_name", "Example"),
            coldField("legal_name", "Example Holding GmbH"),
            coldField("industry", "IT services"),
            coldField("icp", "Mid-market ERP owners"),
          ],
        })}
        entityChoice={null}
        onConfirmEntity={vi.fn()}
        onDeclineEntity={vi.fn()}
        voiceState="waiting"
        connectState="waiting"
      />,
    );

    // Three identity fields and one positioning field went in; the labels
    // must report exactly that split, not a hardcoded pair of numbers.
    expect(screen.getByText("3 fields")).toBeTruthy();
    expect(screen.getByText("1 fields")).toBeTruthy();

    await userEvent.click(
      screen.getByRole("button", { name: /Company identity/ }),
    );
    expect(screen.getByText("Example Holding GmbH")).toBeTruthy();
  });

  it("summarises the read in the head and keeps waiting steps out", () => {
    render(
      <OnboardingLivePanel
        host="example.test"
        done
        read={read({
          profile_fields: [
            {
              field: "display_name",
              value: "Example",
              confidence: 0.9,
              evidence_snippet: "Example",
              source_kind: "url",
              source_url: "https://example.test",
            },
          ],
          facts: [
            {
              category: "company",
              field: "location",
              value: "Berlin",
              value_key: "company:location:berlin",
              evidence_snippet: "Berlin",
              evidence_url: "https://example.test",
              confidence: 0.8,
            },
          ],
        })}
        entityChoice={null}
        onConfirmEntity={vi.fn()}
        onDeclineEntity={vi.fn()}
        voiceState="waiting"
        connectState="waiting"
      />,
    );

    expect(screen.getByText("Read example.test")).toBeTruthy();
    expect(screen.getByText("You are")).toBeTruthy();
    expect(
      screen.getByText(
        "1 facts from 1 pages, already filled in. Open any section to check it.",
      ),
    ).toBeTruthy();
    expect(screen.queryByText("Your writing voice")).toBeNull();
  });

  it("leads the dossier with the decision only a human can make", () => {
    render(
      <OnboardingLivePanel
        host="example.test"
        done
        read={read({ legal_entities: [...ENTITIES] })}
        entityChoice={null}
        onConfirmEntity={vi.fn()}
        onDeclineEntity={vi.fn()}
        voiceState="now"
        connectState="waiting"
      />,
    );

    const cards = document.querySelectorAll(".ob-live-cards > .ob-live-card");
    expect(cards[0]?.getAttribute("data-decision")).toBe("true");
    expect(screen.getAllByRole("radio")).toHaveLength(2);
    // The voice step is in progress, so it names what is not built yet.
    expect(screen.getByText("not built yet")).toBeTruthy();
  });
});
