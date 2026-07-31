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
import { EMPTY_DRAFT } from "./onboarding";
import { CompanyActArtifact } from "./onboarding-conversation/artifact";
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

  it("opens itself when the narration points at what it holds, and stays open", () => {
    const { rerender } = render(
      <DossierCard title="Company identity" count="3 fields">
        <p>Example Holding GmbH</p>
      </DossierCard>,
    );
    expect(screen.queryByText("Example Holding GmbH")).toBeNull();

    rerender(
      <LocaleProvider initial="en">
        <DossierCard title="Company identity" count="3 fields" revealed>
          <p>Example Holding GmbH</p>
        </DossierCard>
      </LocaleProvider>,
    );
    expect(screen.getByText("Example Holding GmbH")).toBeTruthy();

    // The pulse is over, but the reader's attention was sent here — snapping
    // the card shut behind them would undo the pointing.
    rerender(
      <LocaleProvider initial="en">
        <DossierCard title="Company identity" count="3 fields">
          <p>Example Holding GmbH</p>
        </DossierCard>
      </LocaleProvider>,
    );
    expect(screen.getByText("Example Holding GmbH")).toBeTruthy();
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
    // The state is on the block as data, not only in the sentence: the
    // stylesheet reads it to tell an in-progress step from a finished one.
    expect(
      document.querySelector(".ob-live-step")?.getAttribute("data-state"),
    ).toBe("now");
  });

  it("distinguishes a finished step from one in progress in the markup", () => {
    render(
      <>
        <StepBlock n={1} title="Your website" state="done">
          <p>Website body</p>
        </StepBlock>
        <StepBlock n={2} title="Your writing voice" state="now">
          <p>Voice body</p>
        </StepBlock>
      </>,
    );

    const states = Array.from(document.querySelectorAll(".ob-live-step")).map(
      (step) => step.getAttribute("data-state"),
    );
    expect(states).toEqual(["done", "now"]);
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

  it("keeps the quote a child of the row, where the value column can hold it", () => {
    // The row is a key/value grid and the quote is placed in the value column
    // by being a child of the row itself. Wrapped in anything, it lands back
    // under the label and the pairing the grid draws comes apart.
    render(<FieldRow field={field()} />);

    const chip = document.querySelector(".evidence-chip");
    expect(chip?.parentElement?.classList.contains("ob-live-field")).toBe(true);
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

  it("mounts the named field's row, so the narration has something to point at", () => {
    // The conversation says which field it just learned and the artifact pulses
    // that row by its data-finding-id. A collapsed card mounts no rows, so
    // without this the pulse has nothing to find and the pointing is silent.
    render(
      <OnboardingLivePanel
        host="example.test"
        done
        read={read({
          profile_fields: [
            coldField("display_name", "Example"),
            coldField("icp", "Mid-market ERP owners"),
          ],
        })}
        entityChoice={null}
        onConfirmEntity={vi.fn()}
        onDeclineEntity={vi.fn()}
        voiceState="waiting"
        connectState="waiting"
        highlightFields={["icp"]}
      />,
    );

    expect(document.querySelector('[data-finding-id="icp"]')).not.toBeNull();
    // Only the card that holds it opens; the rest stay shut.
    expect(
      document.querySelector('[data-finding-id="display_name"]'),
    ).toBeNull();
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
    // And it is marked as in progress, not left in the same neutral state as a
    // step nobody has reached.
    const states = Array.from(document.querySelectorAll(".ob-live-step")).map(
      (step) => step.getAttribute("data-state"),
    );
    expect(states).toEqual(["done", "now"]);
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

  const NO_FACTS_FOUND =
    "I read the site but pulled no separate facts out of it. What I did learn is in the sections above, each with its source.";

  it("still shows the facts card when a finished read extracted nothing", () => {
    // Honest degradation only reaches the reader if the card is there to say
    // it: omitting the card leaves a settled read looking identical to a
    // dossier that lost a section.
    render(
      artifact(
        read({
          profile_fields: [coldField("display_name", "Example")],
          facts: [],
        }),
      ),
    );

    expect(screen.getByRole("heading", { name: "Facts" })).toBeTruthy();
    expect(screen.getByText(NO_FACTS_FOUND)).toBeTruthy();
  });

  it("shows the facts themselves when the read did extract some", () => {
    render(
      artifact(
        read({
          facts: [
            {
              category: "company",
              field: "location",
              value: "Berlin",
              value_key: "company:location:berlin",
              evidence_snippet: "Our workshop sits in Berlin",
              evidence_url: "https://example.test",
              confidence: 0.8,
            },
          ],
        }),
      ),
    );

    expect(screen.getByRole("heading", { name: "Facts" })).toBeTruthy();
    expect(screen.getByText("Berlin")).toBeTruthy();
    expect(screen.queryByText(NO_FACTS_FOUND)).toBeNull();
  });
});
