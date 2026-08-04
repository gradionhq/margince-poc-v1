// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

/** @vitest-environment jsdom */
import "@testing-library/jest-dom/vitest";

import {
  cleanup,
  render as rtlRender,
  screen,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { type ReactNode, useState } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { components } from "../api/schema";
import { LocaleProvider } from "../i18n";
import {
  type CompanyFieldName,
  changeDraftField,
  EMPTY_DRAFT,
  MAX_SELECTED_FACTS,
} from "./onboarding";
import { CompanyStep } from "./onboarding-company-form";
import { missingRequiredFields } from "./onboarding-conversation/company-proposal";
import { CompanyConfirmCard } from "./onboarding-conversation/confirm-card";
import {
  defaultSelectedFactKeys,
  type FactSelection,
  FactsCard,
  FactTable,
  useFactSelection,
} from "./onboarding-facts";

// The fact surface is prop-driven, so every case is a fixture in and a claim
// out — no fetch, no clock. The claims that matter: the selection model is the
// only thing any of the three views reads, the contract's 100-key ceiling is
// enforced here rather than hoped for downstream, and the table is a real dialog
// (portalled, Escape-closable, focus in and back out again).

type CompanySiteReadFact = components["schemas"]["CompanySiteReadFact"];

function fact(
  over: Partial<CompanySiteReadFact> & { value_key: string; value: string },
): CompanySiteReadFact {
  return {
    category: "company",
    field: "founded_year",
    evidence_snippet: "Founded in Hamburg in 2011.",
    evidence_url: "https://acme.test/about",
    confidence: 0.9,
    ...over,
  };
}

const FOUNDED = fact({
  value_key: "company:founded_year:2011",
  value: "Founded 2011",
  confidence: 0.95,
});
const SERVICE = fact({
  value_key: "offering:service:k8s",
  value: "Managed Kubernetes",
  category: "offering",
  field: "service",
  evidence_snippet: "We run Kubernetes for logistics operators.",
  evidence_url: "https://acme.test/services/kubernetes",
  confidence: 0.88,
});
const SUPPORT = fact({
  value_key: "offering:service:support",
  value: "24/7 support desk",
  category: "offering",
  field: "service",
  evidence_snippet: "The desk answers around the clock.",
  evidence_url: "https://acme.test/services/support",
  confidence: 0.71,
});
const INDUSTRY = fact({
  value_key: "market:served_industry:logistics",
  value: "Logistics",
  category: "market",
  field: "served_industry",
  evidence_snippet: "Trusted by freight forwarders across the EU.",
  evidence_url: "https://acme.test/industries",
  confidence: 0.62,
});
const OUTCOME = fact({
  value_key: "signal:quantified_outcome:deploys",
  value: "Cut deploy time by 40%",
  category: "signal",
  field: "quantified_outcome",
  evidence_snippet: "Deploys went from two hours to seventy minutes.",
  evidence_url: "https://acme.test/cases/freight",
  confidence: 0.34,
});

const FACTS = [FOUNDED, SERVICE, SUPPORT, INDUSTRY, OUTCOME];

// 120 facts: more than the contract's ceiling, so "select all" has to stop
// somewhere the reader can see.
const MANY = Array.from({ length: 120 }, (_, index) =>
  fact({
    value_key: `company:location:${index}`,
    value: `Office ${index}`,
    confidence: 1 - index / 1000,
  }),
);

// The ceiling as the reader is told it, spelled once: the claim is that this
// sentence reaches a screen reader exactly one time per surface stack.
const CAP_SENTENCE =
  "You can save up to 100 facts. Clear one to make room for another.";

// Confidence RISES with wire position, so "the first N on the wire" and "the N
// most certain" name different facts throughout: a seed that trusted the wire
// would tick the ten low-confidence facts at the head and drop the strongest
// ones off the tail. More facts than the ceiling, so the cap has to choose too.
const LOW_HEAD = 10;
const RISING = Array.from({ length: MAX_SELECTED_FACTS + 15 }, (_, index) =>
  fact({
    value_key: `company:location:${index}`,
    value: `Office ${index}`,
    // The head sits under the shared low-confidence boundary; the tail climbs
    // from it, most certain last.
    confidence:
      index < LOW_HEAD ? 0.1 + index / 100 : 0.5 + (index - LOW_HEAD) / 1000,
  }),
);

function render(ui: ReactNode) {
  return rtlRender(<LocaleProvider initial="en">{ui}</LocaleProvider>);
}

// The card with the wizard state it will really have: a parent owning the key
// list, plus the list itself rendered so a test can read what was persisted.
function CardHarness({
  facts,
  initial = [],
}: Readonly<{ facts: readonly CompanySiteReadFact[]; initial?: string[] }>) {
  const [keys, setKeys] = useState<readonly string[]>(initial);
  const selection = useFactSelection(facts, keys, setKeys);
  return (
    <>
      <FactsCard facts={facts} selection={selection} locale="en" />
      <output data-testid="keys">{keys.join(" ")}</output>
    </>
  );
}

// The selection model with no UI over it, so the cap and the key list can be
// exercised without going through a disabled control.
function SelectionProbe({
  facts,
  initial = [],
}: Readonly<{ facts: readonly CompanySiteReadFact[]; initial?: string[] }>) {
  const [keys, setKeys] = useState<readonly string[]>(initial);
  const selection: FactSelection = useFactSelection(facts, keys, setKeys);
  return (
    <div>
      <output data-testid="keys">{keys.join(" ")}</output>
      <output data-testid="count">{selection.selectedCount}</output>
      <output data-testid="cap">{String(selection.atCap)}</output>
      <output data-testid="all">{String(selection.allSelected)}</output>
      <button type="button" onClick={() => selection.setAll(true)}>
        probe-all
      </button>
      <button type="button" onClick={() => selection.setAll(false)}>
        probe-none
      </button>
      {facts.map((item) => (
        <button
          key={item.value_key}
          type="button"
          onClick={() => selection.toggle(item)}
        >
          {`probe-toggle-${item.value_key}`}
        </button>
      ))}
    </div>
  );
}

function keysOf(): string[] {
  const text = screen.getByTestId("keys").textContent ?? "";
  return text === "" ? [] : text.split(" ");
}

// The ceiling sentences a screen reader would actually be told about. Drawing the
// sentence is not announcing it: only the copy carrying the live role speaks.
function announcing(): HTMLElement[] {
  return screen
    .getAllByText(CAP_SENTENCE)
    .filter((notice) => notice.getAttribute("role") === "status");
}

afterEach(cleanup);

describe("useFactSelection", () => {
  it("toggles a fact by its value_key and appends to keep the list order", async () => {
    const user = userEvent.setup();
    render(<SelectionProbe facts={FACTS} initial={[INDUSTRY.value_key]} />);

    await user.click(
      screen.getByRole("button", {
        name: `probe-toggle-${FOUNDED.value_key}`,
      }),
    );

    expect(keysOf()).toEqual([INDUSTRY.value_key, FOUNDED.value_key]);
    expect(screen.getByTestId("count")).toHaveTextContent("2");
  });

  it("removes a key on a second toggle without disturbing the rest", async () => {
    const user = userEvent.setup();
    render(
      <SelectionProbe
        facts={FACTS}
        initial={[FOUNDED.value_key, SERVICE.value_key, OUTCOME.value_key]}
      />,
    );

    await user.click(
      screen.getByRole("button", {
        name: `probe-toggle-${SERVICE.value_key}`,
      }),
    );

    expect(keysOf()).toEqual([FOUNDED.value_key, OUTCOME.value_key]);
  });

  it("reports allSelected only once every fact is in the list", async () => {
    const user = userEvent.setup();
    render(<SelectionProbe facts={[FOUNDED, SERVICE]} />);

    expect(screen.getByTestId("all")).toHaveTextContent("false");
    await user.click(screen.getByRole("button", { name: "probe-all" }));

    expect(screen.getByTestId("all")).toHaveTextContent("true");
    expect(keysOf()).toEqual([FOUNDED.value_key, SERVICE.value_key]);
  });

  it("stops setAll(true) at the contract ceiling", async () => {
    const user = userEvent.setup();
    render(<SelectionProbe facts={MANY} />);

    await user.click(screen.getByRole("button", { name: "probe-all" }));

    expect(keysOf()).toHaveLength(MAX_SELECTED_FACTS);
    expect(screen.getByTestId("cap")).toHaveTextContent("true");
    // The ceiling truncates the tail, never a fact the reader already saw.
    expect(keysOf()[0]).toBe(MANY[0].value_key);
  });

  it("refuses to add past the ceiling but still allows a removal", async () => {
    const user = userEvent.setup();
    const atCap = MANY.slice(0, MAX_SELECTED_FACTS).map(
      (item) => item.value_key,
    );
    const beyond = MANY[MAX_SELECTED_FACTS];
    render(<SelectionProbe facts={MANY} initial={atCap} />);

    await user.click(
      screen.getByRole("button", { name: `probe-toggle-${beyond.value_key}` }),
    );

    expect(keysOf()).toHaveLength(MAX_SELECTED_FACTS);
    expect(keysOf()).not.toContain(beyond.value_key);

    await user.click(
      screen.getByRole("button", {
        name: `probe-toggle-${MANY[0].value_key}`,
      }),
    );
    expect(keysOf()).toHaveLength(MAX_SELECTED_FACTS - 1);
    expect(screen.getByTestId("cap")).toHaveTextContent("false");
  });

  it("clears the whole list on setAll(false)", async () => {
    const user = userEvent.setup();
    render(
      <SelectionProbe
        facts={FACTS}
        initial={[FOUNDED.value_key, SERVICE.value_key]}
      />,
    );

    await user.click(screen.getByRole("button", { name: "probe-none" }));

    expect(keysOf()).toEqual([]);
    expect(screen.getByTestId("count")).toHaveTextContent("0");
  });
});

describe("defaultSelectedFactKeys", () => {
  it("ticks every fact above the confidence boundary, most certain first", () => {
    expect(defaultSelectedFactKeys(FACTS)).toEqual([
      FOUNDED.value_key,
      SERVICE.value_key,
      SUPPORT.value_key,
      INDUSTRY.value_key,
    ]);
  });

  it("leaves a fact the scale calls low for the reader to decide", () => {
    expect(defaultSelectedFactKeys([OUTCOME])).toEqual([]);
  });

  it("seeds by confidence rather than by wire order, and caps by confidence too", () => {
    const keys = defaultSelectedFactKeys(RISING);
    const strongest = RISING[RISING.length - 1];

    expect(keys).toHaveLength(MAX_SELECTED_FACTS);
    expect(keys[0]).toBe(strongest.value_key);
    // Not one of the low-confidence facts the wire happened to send first.
    for (const weak of RISING.slice(0, LOW_HEAD)) {
      expect(keys).not.toContain(weak.value_key);
    }
    // The ceiling drops the least certain of the eligible facts, not the tail of
    // the wire: the five weakest are out, everything above them is in.
    expect(keys).not.toContain(RISING[LOW_HEAD + 4].value_key);
    expect(keys).toContain(RISING[LOW_HEAD + 5].value_key);
  });

  it("opens a fresh read below the ceiling, with room left to add a fact", () => {
    const keys = defaultSelectedFactKeys(FACTS);
    render(<SelectionProbe facts={FACTS} initial={keys} />);

    expect(screen.getByTestId("cap")).toHaveTextContent("false");
    expect(screen.getByTestId("all")).toHaveTextContent("false");
  });
});

describe("FactsCard", () => {
  it("names the fact in each row's checkbox label", () => {
    render(<CardHarness facts={FACTS} />);

    expect(
      screen.getByRole("checkbox", {
        name: "Save the fact: Managed Kubernetes",
      }),
    ).toBeInTheDocument();
  });

  it("derives each category chip's count from the facts it was given", () => {
    render(<CardHarness facts={FACTS} />);

    expect(screen.getByRole("button", { name: "All 5" })).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Company 1" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Offering 2" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Market 1" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Signal 1" }),
    ).toBeInTheDocument();
  });

  it("marks the filter that is on, and only that one", async () => {
    const user = userEvent.setup();
    render(<CardHarness facts={FACTS} />);
    const all = screen.getByRole("button", { name: "All 5" });
    const offering = screen.getByRole("button", { name: "Offering 2" });

    expect(all).toHaveAttribute("aria-pressed", "true");
    expect(offering).toHaveAttribute("aria-pressed", "false");

    await user.click(offering);

    // aria-pressed is what both the announcement and the committed inverse pill
    // hang off, so the chip a reader hears and the chip they see are one fact.
    expect(offering).toHaveAttribute("aria-pressed", "true");
    expect(all).toHaveAttribute("aria-pressed", "false");
    // The count sits inside the chip, so the pressed ground has to carry it too.
    expect(within(offering).getByText("2")).toBeInTheDocument();
  });

  it("offers no dead end: a category with nothing in it cannot be filtered to", () => {
    render(<CardHarness facts={[FOUNDED]} />);

    expect(screen.getByRole("button", { name: "Signal 0" })).toBeDisabled();
  });

  it("keeps a row's checked state across a filter and back", async () => {
    const user = userEvent.setup();
    render(<CardHarness facts={FACTS} />);
    const label = "Save the fact: Logistics";

    await user.click(screen.getByRole("checkbox", { name: label }));
    expect(screen.getByRole("checkbox", { name: label })).toBeChecked();

    await user.click(screen.getByRole("button", { name: "Company 1" }));
    expect(
      screen.queryByRole("checkbox", { name: label }),
    ).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "All 5" }));
    expect(screen.getByRole("checkbox", { name: label })).toBeChecked();
    expect(keysOf()).toEqual([INDUSTRY.value_key]);
  });

  it("says how many of the read's facts the preview is showing", () => {
    render(<CardHarness facts={MANY} />);

    expect(
      screen.getByText("Showing the 10 highest-confidence facts."),
    ).toBeInTheDocument();
    expect(screen.getAllByRole("checkbox")).toHaveLength(10);
  });

  it("states the ceiling once select-all reaches it", async () => {
    const user = userEvent.setup();
    render(<CardHarness facts={MANY} />);

    expect(
      screen.queryByText(/You can save up to 100 facts/),
    ).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Select all" }));

    expect(keysOf()).toHaveLength(MAX_SELECTED_FACTS);
    expect(
      screen.getByText(
        "You can save up to 100 facts. Clear one to make room for another.",
      ),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Select all" })).toBeDisabled();
  });

  it("explains an empty read instead of pretending to load", () => {
    render(<CardHarness facts={[]} />);

    expect(
      screen.getByText(
        "I read the site but pulled no separate facts out of it. What I did learn is in the sections above, each with its source.",
      ),
    ).toBeInTheDocument();
    expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Open the full table" }),
    ).not.toBeInTheDocument();
  });
});

describe("FactTable", () => {
  async function openTable(facts: readonly CompanySiteReadFact[] = FACTS) {
    const user = userEvent.setup();
    const { container } = render(<CardHarness facts={facts} />);
    const opener = screen.getByRole("button", { name: "Open the full table" });
    await user.click(opener);
    return { user, container, opener };
  }

  it("renders through a portal on document.body, not inside the panel", async () => {
    const { container } = await openTable();

    const dialog = screen.getByRole("dialog", { name: "All facts I read" });
    expect(container.contains(dialog)).toBe(false);
    expect(dialog.parentElement?.parentElement).toBe(document.body);
  });

  it("moves focus to the search field on open and back to the opener on close", async () => {
    const { user, opener } = await openTable();

    expect(
      screen.getByRole("searchbox", { name: "Search facts" }),
    ).toHaveFocus();

    await user.keyboard("{Escape}");

    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(opener).toHaveFocus();
  });

  it("closes on the footer button as well as Escape", async () => {
    const { user } = await openTable();

    await user.click(screen.getByRole("button", { name: "Done" }));

    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("gives every column a real header", async () => {
    await openTable();

    for (const column of ["Save", "Category", "Fact", "Source", "Confidence"]) {
      expect(
        screen.getByRole("columnheader", { name: column }),
      ).toBeInTheDocument();
    }
    expect(screen.getAllByRole("row")).toHaveLength(FACTS.length + 1);
  });

  it("filters on the fact value and counts the hits", async () => {
    const { user } = await openTable();

    await user.type(
      screen.getByRole("searchbox", { name: "Search facts" }),
      "kubernetes",
    );

    expect(screen.getByText("1 of 5")).toBeInTheDocument();
    expect(screen.getAllByRole("row")).toHaveLength(2);
    expect(
      screen.getByRole("cell", { name: "Managed Kubernetes" }),
    ).toBeInTheDocument();
  });

  it("filters on the evidence as well as the value", async () => {
    const { user } = await openTable();

    await user.type(
      screen.getByRole("searchbox", { name: "Search facts" }),
      "freight forwarders",
    );

    expect(screen.getByText("1 of 5")).toBeInTheDocument();
    expect(screen.getByRole("cell", { name: "Logistics" })).toBeInTheDocument();
  });

  it("says so when the search matches nothing", async () => {
    const { user } = await openTable();

    await user.type(
      screen.getByRole("searchbox", { name: "Search facts" }),
      "nothing here",
    );

    expect(
      screen.getByText("Nothing matches that search."),
    ).toBeInTheDocument();
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
    expect(screen.getByText("0 of 5")).toBeInTheDocument();
  });

  it("renders confidence as a percentage and links the evidence path", async () => {
    await openTable();

    expect(screen.getByRole("cell", { name: "34%" })).toBeInTheDocument();
    expect(
      screen.getByRole("link", { name: "/cases/freight" }),
    ).toHaveAttribute("href", "https://acme.test/cases/freight");
  });

  it("selects from the table into the same key list the card writes", async () => {
    const { user } = await openTable();
    // The card is still mounted behind the dialog and carries the same row, so
    // the press has to be scoped to the dialog to mean anything.
    const dialog = within(screen.getByRole("dialog"));

    await user.click(
      dialog.getByRole("checkbox", { name: "Save the fact: Logistics" }),
    );

    expect(keysOf()).toEqual([INDUSTRY.value_key]);
  });

  it("holds the page still while it is open and lets it go on close", async () => {
    const { user } = await openTable();

    expect(document.body.style.overflow).toBe("hidden");

    await user.keyboard("{Escape}");

    expect(document.body.style.overflow).toBe("");
  });

  it("stops a table select-all at the ceiling and states it", async () => {
    const { user } = await openTable(MANY);
    const dialog = within(screen.getByRole("dialog"));

    await user.click(dialog.getByRole("button", { name: "Select all" }));

    expect(keysOf()).toHaveLength(MAX_SELECTED_FACTS);
    expect(
      dialog.getByText(
        "You can save up to 100 facts. Clear one to make room for another.",
      ),
    ).toBeInTheDocument();
    // Past the ceiling an unchosen row offers no press that would be ignored.
    expect(
      dialog.getByRole("checkbox", {
        name: `Save the fact: ${MANY[MAX_SELECTED_FACTS].value}`,
      }),
    ).toBeDisabled();
  });

  it("cycles Tab inside the panel instead of letting focus walk out", async () => {
    const { user } = await openTable();
    const dialog = within(screen.getByRole("dialog"));
    const close = dialog.getByRole("button", { name: "Close the table" });
    const done = dialog.getByRole("button", { name: "Done" });

    // The panel claims modality with aria-modal, so the tab ring has to end
    // where the dialog ends: the last stop leads back to the first, not out into
    // a wizard the reader's assistive tech no longer exposes.
    done.focus();
    await user.tab();
    expect(close).toHaveFocus();

    await user.tab({ shift: true });
    expect(done).toHaveFocus();
  });

  it("closes on a press on the ground behind it, not on one inside the panel", async () => {
    const { user } = await openTable();
    const dialog = screen.getByRole("dialog");
    const scrim = dialog.parentElement;

    await user.click(dialog);
    expect(screen.getByRole("dialog")).toBeInTheDocument();

    if (scrim instanceof HTMLElement) {
      await user.click(scrim);
    }
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("closes on the head's own close control and returns focus to the opener", async () => {
    const { user, opener } = await openTable();

    await user.click(screen.getByRole("button", { name: "Close the table" }));

    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(opener).toHaveFocus();
  });

  it("announces the ceiling once, though both surfaces show it", async () => {
    const { user } = await openTable(MANY);
    const dialog = within(screen.getByRole("dialog"));

    await user.click(dialog.getByRole("button", { name: "Select all" }));

    // The card stays mounted behind the portal, so the sentence is drawn twice
    // — but only the dialog's copy is a live region, or a screen reader would
    // hear the ceiling twice on one press.
    expect(screen.getAllByText(CAP_SENTENCE)).toHaveLength(2);
    expect(announcing()).toHaveLength(1);
    expect(dialog.getByText(CAP_SENTENCE)).toHaveAttribute("role", "status");

    await user.keyboard("{Escape}");

    // Closed, the card is the only surface left and takes the announcement back.
    expect(screen.getAllByText(CAP_SENTENCE)).toHaveLength(1);
    expect(announcing()).toHaveLength(1);
  });

  it("closes through the callback it was given, not by unmounting itself", async () => {
    const onClose = vi.fn();
    const user = userEvent.setup();
    render(<TableOnly onClose={onClose} />);

    await user.keyboard("{Escape}");

    expect(onClose).toHaveBeenCalledTimes(1);
    // Still mounted: the owner of the open flag decides, not the dialog.
    expect(screen.getByRole("dialog")).toBeInTheDocument();
  });
});

// The two surfaces that pick facts outside the fact table — the thread's review
// card and the edit form — go through the same selection model, so the contract
// ceiling is one rule with one explanation rather than a number re-typed per
// call site.
describe("the other fact-picking surfaces", () => {
  const AT_CAP = MANY.slice(0, MAX_SELECTED_FACTS).map(
    (item) => item.value_key,
  );
  const BEYOND = MANY[MAX_SELECTED_FACTS];

  function ConfirmHarness({ initial }: Readonly<{ initial: string[] }>) {
    const [keys, setKeys] = useState<readonly string[]>(initial);
    return (
      <>
        <CompanyConfirmCard
          proposal={{ ready: true, fields: [], facts: MANY }}
          draft={EMPTY_DRAFT}
          answers={[]}
          selectedFactKeys={keys}
          setSelectedFactKeys={setKeys}
          missingRequired={[]}
          setField={vi.fn()}
          onAcceptAll={vi.fn()}
          pending={false}
          authorizing={false}
          error={null}
        />
        <output data-testid="keys">{keys.join(" ")}</output>
      </>
    );
  }

  function FormHarness({ initial }: Readonly<{ initial: string[] }>) {
    const [keys, setKeys] = useState<readonly string[]>(initial);
    return (
      <>
        <CompanyStep
          draft={EMPTY_DRAFT}
          setField={vi.fn()}
          onPickEntity={vi.fn()}
          read={readWith(MANY)}
          saved={false}
          saveError={null}
          missingRequired={[]}
          selectedFactKeys={keys}
          setSelectedFactKeys={setKeys}
          onFieldBlur={vi.fn()}
        />
        <output data-testid="keys">{keys.join(" ")}</output>
      </>
    );
  }

  it("refuses a fact past the ceiling on the review card, and says why", async () => {
    const user = userEvent.setup();
    render(<ConfirmHarness initial={AT_CAP} />);
    const refused = screen.getByRole("button", { name: /Office 100(?!\d)/ });

    expect(refused).toBeDisabled();
    expect(screen.getByText(CAP_SENTENCE)).toBeInTheDocument();

    await user.click(refused);

    expect(keysOf()).toHaveLength(MAX_SELECTED_FACTS);
    expect(keysOf()).not.toContain(BEYOND.value_key);
  });

  it("takes the refusal back on the review card once a fact is cleared", async () => {
    const user = userEvent.setup();
    render(<ConfirmHarness initial={AT_CAP} />);

    await user.click(screen.getByRole("button", { name: /Office 0(?!\d)/ }));

    expect(keysOf()).toHaveLength(MAX_SELECTED_FACTS - 1);
    expect(keysOf()).not.toContain(MANY[0].value_key);
    expect(screen.queryByText(CAP_SENTENCE)).not.toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /Office 100(?!\d)/ }),
    ).toBeEnabled();
  });

  it("refuses a fact past the ceiling on the edit form, and says why", async () => {
    const user = userEvent.setup();
    render(<FormHarness initial={AT_CAP} />);
    const refused = screen.getByRole("button", { name: /Office 100(?!\d)/ });

    expect(refused).toBeDisabled();
    expect(screen.getByText(CAP_SENTENCE)).toBeInTheDocument();

    await user.click(refused);

    expect(keysOf()).toHaveLength(MAX_SELECTED_FACTS);
    expect(keysOf()).not.toContain(BEYOND.value_key);
  });

  it("writes the form's own toggle into the same key list", async () => {
    const user = userEvent.setup();
    render(<FormHarness initial={[]} />);

    await user.click(screen.getByRole("button", { name: /Office 3(?!\d)/ }));

    expect(keysOf()).toEqual([MANY[3].value_key]);
  });
});

// The triage layout: a summary line the reader can skim before any row, the
// work rows ordered ahead of the settled fields inside each group, and a
// prose field's snippet-shaped preview/full-text swap.
describe("CompanyConfirmCard as a triage surface", () => {
  type Proposal = components["schemas"]["OnboardingCompanyProposal"];

  const LONG_OFFER =
    "We help logistics operators run their fleets more efficiently by combining live telemetry with route optimization, and the platform plugs directly into dispatch software to remove the manual planning steps that used to take hours every single day.";

  function triageProposal(): Proposal {
    return {
      ready: true,
      fields: [
        {
          field: "legal_name",
          value: "Gradion GmbH",
          confidence: 0.9,
          evidence_snippet: "Legal name on the imprint page.",
          source_url: "https://gradion.com/impressum",
        },
        {
          field: "offer_summary",
          value: LONG_OFFER,
          confidence: 0.9,
          evidence_snippet: "About page copy.",
          source_url: "https://gradion.com/about",
        },
      ],
      facts: [],
      open_questions: [],
      remaining_required_fields: ["icp"],
      draft_version: 1,
      proposal_hash: "hash",
    };
  }

  function triageDraft() {
    return {
      ...EMPTY_DRAFT,
      values: {
        ...EMPTY_DRAFT.values,
        legal_name: "Gradion GmbH",
        offer_summary: LONG_OFFER,
      },
    };
  }

  function renderTriage(
    missingRequired: readonly "icp"[],
    read: components["schemas"]["CompanySiteRead"] | null = null,
  ) {
    render(
      <CompanyConfirmCard
        proposal={triageProposal()}
        draft={triageDraft()}
        answers={[]}
        read={read}
        selectedFactKeys={[]}
        setSelectedFactKeys={vi.fn()}
        missingRequired={missingRequired}
        setField={vi.fn()}
        onAcceptAll={vi.fn()}
        pending={false}
        authorizing={false}
        error={null}
      />,
    );
  }

  // The pinned continue bar replaced the old tally strip: progress measures
  // real completion toward being able to continue (required fields only),
  // never a count of every row the board happens to carry.
  it("measures the continue bar's progress by required fields filled, not every row on the board", () => {
    render(
      <CompanyConfirmCard
        proposal={triageProposal()}
        draft={{
          ...EMPTY_DRAFT,
          values: {
            ...EMPTY_DRAFT.values,
            offer_summary: "We do things.",
            icp: "Mid-market logistics.",
          },
        }}
        answers={[]}
        selectedFactKeys={[]}
        setSelectedFactKeys={vi.fn()}
        missingRequired={["display_name"]}
        setField={vi.fn()}
        onAcceptAll={vi.fn()}
        pending={false}
        authorizing={false}
        error={null}
      />,
    );

    // 2 of the 3 required fields (offer_summary, icp) are filled; the other
    // 13 profile fields on the board — settled or not — play no part.
    const bar = screen.getByRole("progressbar", {
      name: "Required fields completed",
    });
    expect(bar).toHaveProperty("value", 2);
    expect(bar).toHaveProperty("max", 3);
    expect(
      screen.getByText("1 field needed before you can continue"),
    ).toBeInTheDocument();
  });

  it("says plainly that nothing more is required once every required field is filled", () => {
    renderTriage([]);

    expect(
      screen.getByText("Nothing more needed — you can continue."),
    ).toBeInTheDocument();
  });

  it("counts exactly the fields the section nav marks with its blocking icon", () => {
    render(
      <CompanyConfirmCard
        proposal={triageProposal()}
        draft={{
          ...EMPTY_DRAFT,
          values: { ...EMPTY_DRAFT.values, offer_summary: "x" },
        }}
        answers={[]}
        selectedFactKeys={[]}
        setSelectedFactKeys={vi.fn()}
        missingRequired={["display_name", "icp"]}
        setField={vi.fn()}
        onAcceptAll={vi.fn()}
        pending={false}
        authorizing={false}
        error={null}
      />,
    );

    const blocking = document.querySelectorAll(
      '.ob-triage-nav-item[data-blocking="true"]',
    );
    expect(blocking).toHaveLength(2);
    expect(
      screen.getByText("2 fields needed before you can continue"),
    ).toBeInTheDocument();
  });

  // The nav's blocking cluster and the bottom bar's status sentence are now
  // the only two places the board states what is missing; a banner reciting
  // the same gap a third time above the fold was noise, not a third source
  // of truth, so its absence must never leave the surface silent about why
  // Continue is disabled.
  it("renders no blocker banner while a required field is empty, and keeps Continue disabled with the reason visible", () => {
    render(
      <CompanyConfirmCard
        proposal={triageProposal()}
        draft={EMPTY_DRAFT}
        answers={[]}
        selectedFactKeys={[]}
        setSelectedFactKeys={vi.fn()}
        missingRequired={["display_name", "offer_summary", "icp"]}
        setField={vi.fn()}
        onAcceptAll={vi.fn()}
        pending={false}
        authorizing={false}
        error={null}
      />,
    );

    expect(document.querySelector(".ob-triage-blocker")).toBeNull();
    expect(
      screen.getByText("3 fields needed before you can continue"),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Continue" })).toBeDisabled();
  });

  // Only display_name, offer_summary and icp ever block confirm (the server
  // 422s on exactly those three); every other empty or weakly-grounded field
  // is advisory, however many of them a section carries.
  it("presents a section as blocking exactly when it holds a required-and-empty field, never for advisory ones alone", () => {
    renderTriage(["icp"]);

    const nav = screen.getByRole("navigation", { name: "Jump to a section" });
    // Identity carries display_name (required, empty) alongside four merely
    // empty-and-optional fields — it must read as blocking.
    const identityLink = within(nav).getByRole("button", {
      name: /Legal organization/,
    });
    expect(identityLink.querySelector('[data-blocking="true"]')).not.toBeNull();

    // None of buying_intents, common_objections or sales_motion is in
    // REQUIRED_FIELDS — sales can never present as blocking, however many
    // of its own fields are still empty. It still marks itself at the
    // section level (every section with outstanding work does, so the nav
    // alone tells settled, advisory and blocking apart) — but with the
    // neutral count, never the danger pill reserved for a field that
    // actually gates confirm.
    const salesLink = within(nav).getByRole("button", {
      name: /Positioning and sales/,
    });
    expect(salesLink.querySelector('[data-blocking="true"]')).toBeNull();
    expect(salesLink.querySelector(".ob-triage-nav-badge")).toBeNull();
    expect(salesLink.querySelector(".ob-triage-nav-advisory")).not.toBeNull();
    const salesItem = salesLink.parentElement as HTMLElement;
    const buyingIntents = within(salesItem).getByRole("button", {
      name: /Buying intents/,
    });
    expect(buyingIntents.querySelector("svg")).toBeNull();
    // No triangle glyph on an advisory row — only its own name — but the
    // tier still reaches a screen reader on the row itself.
    expect(
      within(buyingIntents).getByText("Worth a check", {
        selector: ".sr-only",
      }),
    ).toBeInTheDocument();
  });

  it("disables continue exactly while a required field remains empty, and enables it on the same render the last one is filled", async () => {
    const user = userEvent.setup();

    function RequiredHarness() {
      const [draft, setDraft] = useState({
        ...EMPTY_DRAFT,
        values: {
          ...EMPTY_DRAFT.values,
          offer_summary: "We do things.",
          icp: "Mid-market logistics.",
        },
      });
      return (
        <CompanyConfirmCard
          proposal={triageProposal()}
          draft={draft}
          answers={[]}
          selectedFactKeys={[]}
          setSelectedFactKeys={vi.fn()}
          missingRequired={missingRequiredFields(draft.values)}
          setField={(field, value) =>
            setDraft((current) => changeDraftField(current, field, value))
          }
          onAcceptAll={vi.fn()}
          pending={false}
          authorizing={false}
          error={null}
        />
      );
    }
    render(<RequiredHarness />);

    const continueButton = screen.getByRole("button", { name: /Continue/ });
    expect(continueButton).toBeDisabled();

    await user.type(
      screen.getByRole("textbox", { name: /Company name/ }),
      "Gradion",
    );

    expect(continueButton).toBeEnabled();
  });

  it("puts a group's work rows before its settled rows in the DOM", () => {
    renderTriage(["icp"]);

    // Within the legal-identity group: display_name is required and empty,
    // legal_name is grounded and settled — the gap outranks the done row.
    // "Gradion GmbH" also names the identity card above the board; the row
    // itself is the one that is a button.
    const missingRow = screen.getByRole("textbox", { name: /Company name/ });
    const settledValue = screen.getByRole("button", { name: /Gradion GmbH/ });

    expect(
      missingRow.compareDocumentPosition(settledValue) &
        Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
  });

  // A settled row is a one-line summary that is itself the expand control;
  // the full text stays out of the DOM until the row opens, and opening it
  // yields the value editable in place.
  it("hides a prose field's full text behind its collapsed row until the reader expands it", async () => {
    const user = userEvent.setup();
    renderTriage([]);

    expect(screen.queryByText(LONG_OFFER)).not.toBeInTheDocument();
    // The row button's name carries the preview, which is what tells it
    // apart from the map square that names the same field.
    const row = screen.getByRole("button", { name: /We help logistics/ });
    expect(row).toHaveAttribute("aria-expanded", "false");

    await user.click(row);

    expect(
      screen.getByRole("textbox", { name: /What do you sell\?/ }),
    ).toHaveValue(LONG_OFFER);
  });

  it("keeps a field's evidence snippet out of the DOM until the open row's chip is toggled", async () => {
    const user = userEvent.setup();
    renderTriage([]);

    expect(
      screen.queryByText('"Legal name on the imprint page."'),
    ).not.toBeInTheDocument();
    // Collapsed rows carry no evidence control at all; the chip appears
    // with the opened card and its snippet still waits for the toggle.
    await user.click(screen.getByRole("button", { name: /Gradion GmbH/ }));
    const toggle = screen.getByRole("button", {
      name: "Evidence from gradion.com/impressum",
    });
    expect(toggle).toHaveAttribute("aria-expanded", "false");

    await user.click(toggle);

    expect(
      screen.getByText('"Legal name on the imprint page."'),
    ).toBeInTheDocument();
  });

  // The identity card leads the surface: a name, and the fields that name
  // the business — each a jump into the row it summarizes, not another
  // form. No brand image ever renders; the contract carries none.
  it("leads with an identity card naming the company, whose values jump to their rows", async () => {
    const user = userEvent.setup();
    render(
      <CompanyConfirmCard
        proposal={triageProposal()}
        draft={{
          ...EMPTY_DRAFT,
          values: {
            ...EMPTY_DRAFT.values,
            display_name: "Gradion",
            legal_name: "Gradion GmbH",
            website: "https://gradion.com/",
            registered_address: "Musterstraße 1, Hamburg",
            industry: "Software",
          },
        }}
        answers={[]}
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

    expect(
      screen.getByRole("heading", { name: "Gradion", level: 3 }),
    ).toBeInTheDocument();
    // The domain, not the raw URL, and no logo — the contract has none.
    expect(screen.getByText("gradion.com")).toBeInTheDocument();
    expect(screen.queryByRole("img")).not.toBeInTheDocument();

    await user.click(
      screen.getByRole("button", { name: "Musterstraße 1, Hamburg" }),
    );

    expect(document.activeElement).toHaveAttribute(
      "data-finding-id",
      "registered_address",
    );
  });

  it("says nothing about a company with no name yet", () => {
    const { container } = render(
      <CompanyConfirmCard
        proposal={triageProposal()}
        draft={EMPTY_DRAFT}
        answers={[]}
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

    expect(container.querySelector(".ob-company-card")).toBeNull();
  });

  // The map is gone: the nav is a plain list of section names, each a jump
  // link, with only a single blocking count as its status — no per-field
  // squares, no legend explaining a colour vocabulary, and no second count
  // for the merely-advisory fields the named list already speaks for.
  it("navigates by section name, badging only what blocks, not a grid of squares", () => {
    renderTriage(["icp"]);

    const nav = screen.getByRole("navigation", { name: "Jump to a section" });
    const identityLink = within(nav).getByRole("button", {
      name: /Legal organization/,
    });
    // display_name is required and empty — the one field that actually
    // blocks confirm, and the only count this section's badge carries.
    // registered_address, register_vat, industry and history are merely
    // empty-and-optional (legal_name alone is settled) — worth a look,
    // never an obstacle, and never given a count of their own to add to it.
    expect(within(identityLink).getByText("1")).toBeInTheDocument();
    expect(within(identityLink).queryByText("4")).toBeNull();
    expect(screen.queryByText("What the squares mean")).not.toBeInTheDocument();
  });

  it("lists a section's outstanding fields by name, exactly the ones its count counts", () => {
    renderTriage(["icp"]);

    const nav = screen.getByRole("navigation", { name: "Jump to a section" });
    const identityLink = within(nav).getByRole("button", {
      name: /Legal organization/,
    });
    const section = identityLink.closest("li");
    if (section === null) {
      throw new Error("expected the identity section's list item to exist");
    }
    // Same 5 fields the badge counts (legal_name alone is settled and so
    // named nowhere here), same order the rows themselves use: the gap
    // outranks the merely-empty fields that follow it. Only the visible
    // label counts here — the tier rides along as sr-only text on the same
    // button and is covered by its own test.
    const named = within(section)
      .getAllByRole("button")
      .filter((button) => button !== identityLink)
      .map((button) => button.querySelector("span:not(.sr-only)")?.textContent);

    expect(named).toEqual([
      "Company name",
      "Registered address",
      "Register / VAT ID",
      "Industry",
      "Company history",
    ]);
  });

  // A field label plus a multi-word tier phrase on the same row is what
  // broke the nav's layout; the tier still has to reach a screen reader.
  it("names a field with no visible status phrase, while its accessible name still carries the tier", () => {
    renderTriage(["icp"]);

    const nav = screen.getByRole("navigation", { name: "Jump to a section" });
    // The accessible name (both matchers below succeed) already proves the
    // tier reaches assistive tech; the visible span is checked separately
    // to prove it carries the field name ALONE.
    const blockingItem = within(nav).getByRole("button", {
      name: /Company name.*Needed to continue/,
    });
    expect(blockingItem.querySelector("span:not(.sr-only)")).toHaveTextContent(
      "Company name",
    );
    expect(blockingItem.querySelector(".sr-only")).toHaveTextContent(
      "Needed to continue",
    );

    const advisoryItem = within(nav).getByRole("button", {
      name: /Industry.*Worth a check/,
    });
    expect(advisoryItem.querySelector("span:not(.sr-only)")).toHaveTextContent(
      "Industry",
    );
    expect(advisoryItem.querySelector(".sr-only")).toHaveTextContent(
      "Worth a check",
    );
  });

  // Arriving at the review is not an action the reader took, so nothing on
  // the surface may act as though it were: no field grabs focus, and
  // nothing scrolls to one, on mount. A jump only ever follows a click.
  it("leaves focus where the document put it on mount, with no field auto-focused or scrolled to", () => {
    // jsdom carries no real scrollIntoView; stubbing it on the prototype is
    // the only way to observe whether anything called it. Restored after,
    // since a global prototype patch would otherwise leak into every other
    // test in this file.
    const original = HTMLElement.prototype.scrollIntoView;
    const scrollIntoView = vi.fn();
    HTMLElement.prototype.scrollIntoView = scrollIntoView;
    try {
      renderTriage(["icp"]);

      // display_name is required and empty — the strongest possible pull
      // for an old "focus the first blocking row" habit — and its input
      // still must not be where focus landed.
      expect(document.activeElement).not.toBe(
        screen.getByRole("textbox", { name: /Company name/ }),
      );
      expect(document.activeElement === document.body).toBe(true);
      expect(scrollIntoView).not.toHaveBeenCalled();
    } finally {
      HTMLElement.prototype.scrollIntoView = original;
    }
  });

  it("lands focus inside the named field's row, ready to type", async () => {
    const user = userEvent.setup();
    renderTriage(["icp"]);

    const nav = screen.getByRole("navigation", { name: "Jump to a section" });
    await user.click(within(nav).getByRole("button", { name: /^Industry/ }));

    // The jump exists so the reader can fill the field, so it lands on the
    // control rather than the row that holds it.
    const focused = document.activeElement;
    expect(focused?.closest("[data-finding-id]")).toHaveAttribute(
      "data-finding-id",
      "industry",
    );
    expect(focused?.tagName).toMatch(/^(INPUT|TEXTAREA)$/);
  });

  // The active-section tint is a visual replacement for the old border rule;
  // the semantics it reads (aria-current) must land on exactly one section
  // regardless of which visual treatment marks it.
  it("marks exactly one section current, never zero and never more than one", () => {
    renderTriage(["icp"]);

    const nav = screen.getByRole("navigation", { name: "Jump to a section" });
    const current = within(nav)
      .getAllByRole("button")
      .filter((button) => button.getAttribute("aria-current") === "true");

    // jsdom carries no IntersectionObserver, so the board falls back to the
    // first section — the same honest default the live browser starts from
    // before anything has scrolled.
    expect(current).toEqual([
      within(nav).getByRole("button", { name: /Legal organization/ }),
    ]);
  });

  it("shows no unresolved count for a section with nothing outstanding", () => {
    render(
      <CompanyConfirmCard
        proposal={{
          ready: true,
          fields: [],
          facts: [],
          open_questions: [],
          remaining_required_fields: [],
          draft_version: 1,
          proposal_hash: "hash",
        }}
        draft={{
          ...EMPTY_DRAFT,
          values: {
            display_name: "filled",
            website: "filled",
            legal_name: "filled",
            register_vat: "filled",
            registered_address: "filled",
            industry: "filled",
            offer_summary: "filled",
            icp: "filled",
            value_proposition: "filled",
            usp: "filled",
            customer_pains: "filled",
            desired_outcomes: "filled",
            buying_center: "filled",
            buying_intents: "filled",
            common_objections: "filled",
            sales_motion: "filled",
            history: "filled",
          },
        }}
        answers={[]}
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

    const nav = screen.getByRole("navigation", { name: "Jump to a section" });
    const salesLink = within(nav).getByRole("button", {
      name: /Positioning and sales/,
    });

    expect(within(salesLink).queryByText(/^\d+$/)).not.toBeInTheDocument();
  });

  const FOUNDER: components["schemas"]["CompanySiteReadPerson"] = {
    name: "Jamie Fox",
    role: "Co-founder",
    evidence_snippet: "Jamie Fox, co-founder, leads product.",
    evidence_url: "https://gradion.com/team",
  };

  // People are a company fact (who to talk to), the same class of thing as
  // an office or a service line — they belong on the board, in the section
  // nav and the group list, not folded into the tail below it.
  it("promotes people found on the site to their own section on the board", () => {
    renderTriage([], { ...readWith([]), people: [FOUNDER] });

    const nav = screen.getByRole("navigation", { name: "Jump to a section" });
    expect(
      within(nav).getByRole("button", { name: /^People/ }),
    ).toBeInTheDocument();

    const heading = screen.getByRole("heading", { name: "People", level: 3 });
    // The section sits in the group list, the same place every field group
    // does — not in the reference tail further down.
    expect(heading.closest(".ob-triage-groups")).not.toBeNull();
    expect(heading.closest(".ob-triage-readmore")).toBeNull();
    const section = heading.closest("section");
    if (section === null) {
      throw new Error("expected the People section to exist");
    }
    expect(within(section).getByText("Jamie Fox")).toBeInTheDocument();
    expect(within(section).getByText("Co-founder")).toBeInTheDocument();
  });

  it("says plainly when the read found no people, rather than a zero count", () => {
    renderTriage([], readWith([]));

    const heading = screen.getByRole("heading", { name: "People", level: 3 });
    const section = heading.closest("section");
    if (section === null) {
      throw new Error("expected the People section to exist");
    }
    expect(
      within(section).getByText("No people found on your site."),
    ).toBeInTheDocument();
    // No bare digit stands in for the honest sentence, and the section
    // carries no outstanding count of its own in the nav either — nothing
    // here is the human's to resolve.
    expect(within(section).queryByText(/^\d+$/)).not.toBeInTheDocument();
    const nav = screen.getByRole("navigation", { name: "Jump to a section" });
    const peopleLink = within(nav).getByRole("button", { name: /^People/ });
    expect(within(peopleLink).queryByText(/^\d+$/)).not.toBeInTheDocument();
  });

  // The count beside People or Facts means "this is what I found", never
  // "this needs you" — it must equal the section's own content and must
  // never be mistaken, sighted or not, for an outstanding-work count.
  it("shows how many people the read found in the nav, as a found quantity rather than outstanding work", () => {
    const found = [FOUNDER, { ...FOUNDER, name: "Alex Chen", role: "COO" }];
    renderTriage([], { ...readWith([]), people: found });

    const nav = screen.getByRole("navigation", { name: "Jump to a section" });
    const peopleLink = within(nav).getByRole("button", {
      name: /^People.*2 found/,
    });
    expect(within(peopleLink).getByText("2")).toBeInTheDocument();
    // Never the blocking/advisory pill's own class — a find is not a gap.
    expect(peopleLink.querySelector(".ob-triage-nav-badge")).toBeNull();

    const heading = screen.getByRole("heading", { name: "People", level: 3 });
    const section = heading.closest("section");
    if (section === null) {
      throw new Error("expected the People section to exist");
    }
    expect(within(section).getAllByRole("listitem")).toHaveLength(found.length);
  });

  it("shows how many facts the read produced in the nav, as a found quantity rather than outstanding work", () => {
    const proposal: Proposal = {
      ...triageProposal(),
      facts: [FOUNDED, SERVICE, SUPPORT],
    };
    render(
      <CompanyConfirmCard
        proposal={proposal}
        draft={triageDraft()}
        answers={[]}
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

    const nav = screen.getByRole("navigation", { name: "Jump to a section" });
    const factsLink = within(nav).getByRole("button", {
      name: /^Facts.*3 found/,
    });
    expect(within(factsLink).getByText("3")).toBeInTheDocument();
    expect(factsLink.querySelector(".ob-triage-nav-badge")).toBeNull();

    // The nav's number is the same 3 the section itself renders — one
    // derivation, not a second tally kept in step by hand.
    expect(document.querySelectorAll(".ob-triage-fact").length).toBe(3);
  });

  it("keeps the read's coverage honesty in the tail, since it is provenance, not a company fact", () => {
    renderTriage([], readWith([]));

    expect(
      screen.getByText("What I read, and what I skipped"),
    ).toBeInTheDocument();
    expect(screen.getByText("Background, not work")).toBeInTheDocument();
    expect(
      screen.queryByText("Everything else I found"),
    ).not.toBeInTheDocument();
  });

  // A stateful render, so a simulated edit actually changes the draft the
  // board reads from — the point of these tests is what happens on the
  // SAME re-render a value lands, which a static fixture cannot exercise.
  function TriageHarness() {
    const [draft, setDraft] = useState(triageDraft());
    return (
      <CompanyConfirmCard
        proposal={triageProposal()}
        draft={draft}
        answers={[]}
        selectedFactKeys={[]}
        setSelectedFactKeys={vi.fn()}
        missingRequired={[]}
        setField={(field, value) =>
          setDraft((current) => changeDraftField(current, field, value))
        }
        onAcceptAll={vi.fn()}
        pending={false}
        authorizing={false}
        error={null}
      />
    );
  }

  it("marks a required gap more urgently than a merely-empty field, in words a screen reader gets", async () => {
    const user = userEvent.setup();
    render(<TriageHarness />);

    // Both are open by default (every work row is); collapse them to the
    // one-line shape — the shape a settled row also uses, and the one the
    // mark has to carry on its own since there is no label beside it there.
    const requiredRow = document.getElementById("ob-triage-row-display_name");
    const emptyRow = document.getElementById("ob-triage-row-industry");
    if (requiredRow === null || emptyRow === null) {
      throw new Error("expected both rows to exist");
    }
    await user.click(
      within(requiredRow).getByRole("button", { name: "Show less" }),
    );
    await user.click(
      within(emptyRow).getByRole("button", { name: "Show less" }),
    );

    expect(
      within(requiredRow).getByText("required, still empty"),
    ).toBeInTheDocument();
    expect(within(emptyRow).getByText("empty")).toBeInTheDocument();
    expect(
      within(emptyRow).queryByText("required, still empty"),
    ).not.toBeInTheDocument();
  });

  // An empty legal field can say WHY, from what the crawl's own pages show —
  // never a guess dressed up as a finding.
  it("says the legal field was not published when the crawl actually fetched an imprint page", async () => {
    const user = userEvent.setup();
    renderTriage([], {
      ...readWith([]),
      pages: [
        {
          url: "https://gradion.com/impressum",
          status: "fetched",
          kind: "impressum",
        },
      ],
    });

    const row = document.getElementById("ob-triage-row-registered_address");
    if (row === null) {
      throw new Error("expected the registered_address row to exist");
    }
    await user.click(within(row).getByRole("button", { name: "Show less" }));

    expect(
      within(row).getByText(
        "Not stated on your legal or imprint page. Yours to add.",
      ),
    ).toBeInTheDocument();
  });

  it("says the legal field was not checked when no imprint page was crawled", async () => {
    const user = userEvent.setup();
    renderTriage([], readWith([]));

    const row = document.getElementById("ob-triage-row-registered_address");
    if (row === null) {
      throw new Error("expected the registered_address row to exist");
    }
    await user.click(within(row).getByRole("button", { name: "Show less" }));

    expect(
      within(row).getByText(
        "I did not find a legal or imprint page on your site to check. Yours to add.",
      ),
    ).toBeInTheDocument();
  });

  it("drops a row's mark and the section's outstanding count on the same render a value lands", async () => {
    const user = userEvent.setup();
    render(<TriageHarness />);

    const nav = screen.getByRole("navigation", { name: "Jump to a section" });
    const identityLink = within(nav).getByRole("button", {
      name: /Legal organization/,
    });
    const section = identityLink.closest("li") as HTMLElement;
    const advisoryItems = () =>
      section.querySelectorAll(".ob-triage-nav-item:not([data-blocking])");
    expect(within(identityLink).getByText("1")).toBeInTheDocument();
    expect(advisoryItems()).toHaveLength(4);
    const row = document.getElementById("ob-triage-row-display_name");
    if (row === null) {
      throw new Error("expected the row to exist");
    }
    expect(within(row).getByText("required, still empty")).toBeInTheDocument();

    await user.type(
      screen.getByRole("textbox", { name: /Company name/ }),
      "Gradion",
    );

    // The blocking badge and the row's own mark are the same predicate read
    // twice: filling the one required field can only drop both together.
    // The advisory list is a different predicate entirely — untouched by
    // this edit — and must not move just because the blocking badge did.
    expect(within(identityLink).queryByText("1")).not.toBeInTheDocument();
    expect(advisoryItems()).toHaveLength(4);
    expect(
      within(row).queryByText("required, still empty"),
    ).not.toBeInTheDocument();
  });

  // The live decision surface (DecisionScene) owns answering an open
  // question; the review card only reads extracted facts back, so it must
  // never re-ask one in a second shape of its own.
  it("renders no question panel when the proposal carries open questions", () => {
    const { container } = render(
      <CompanyConfirmCard
        proposal={{
          ...triageProposal(),
          open_questions: [
            {
              id: "clarify:registered_address:1",
              question:
                "The legal notice names more than one registered address. Which one belongs to your company?",
              field: "registered_address",
              options: [
                {
                  value: "Musterstraße 1, Berlin",
                  label: "Musterstraße 1, Berlin",
                },
                {
                  value: "Hauptstraße 5, Hamburg",
                  label: "Hauptstraße 5, Hamburg",
                },
              ],
            },
          ],
        }}
        draft={triageDraft()}
        answers={[]}
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

    expect(container.querySelector(".ob-conv-confirm-questions")).toBeNull();
    expect(
      screen.queryByText(/Which one belongs to your company/),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Musterstraße 1, Berlin" }),
    ).not.toBeInTheDocument();
    // Nothing here re-asks the question — but an unanswered one still gates
    // Continue, and says so, rather than relying on the live DecisionScene
    // being the only thing standing between an open question and confirm.
    expect(screen.getByRole("button", { name: /Continue/ })).toBeDisabled();
    expect(
      screen.getByText("A decision is still open. Answer it to continue."),
    ).toBeInTheDocument();
  });

  // The guard is built from state (proposal.open_questions vs. answers),
  // never from which scene happens to be on screen — constructed directly
  // here rather than through a DecisionScene/company-act render, since
  // trusting the scene swap is the assumption that let the gap through.
  it("disables continue while an unresolved open question exists, even with every required field filled", () => {
    const openQuestion = {
      id: "clarify:registered_address:1",
      question: "Which registered address belongs to your company?",
      field: "registered_address",
      options: [
        { value: "Musterstraße 1, Berlin", label: "Musterstraße 1, Berlin" },
      ],
    };
    const proposal: Proposal = {
      ...triageProposal(),
      open_questions: [openQuestion],
    };

    render(
      <CompanyConfirmCard
        proposal={proposal}
        draft={triageDraft()}
        answers={[]}
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

    expect(screen.getByRole("button", { name: /Continue/ })).toBeDisabled();

    cleanup();

    // The same proposal, but this time the answer already landed — the
    // question is resolved and no longer blocks, on this same render.
    render(
      <CompanyConfirmCard
        proposal={proposal}
        draft={triageDraft()}
        answers={[
          {
            clarifyId: openQuestion.id,
            field: "registered_address",
            value: "Musterstraße 1, Berlin",
          },
        ]}
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

    expect(screen.getByRole("button", { name: /Continue/ })).toBeEnabled();
  });

  // One row per RowState, so a future change to any one of them cannot
  // quietly drop its collapsed signal without this test noticing. The
  // colour band is never the only thing checked here — every row is
  // collapsed first, and the assertion is on words, not on a data-state
  // attribute or a border colour.
  it("gives every row state a non-colour signal once the row is collapsed", async () => {
    const user = userEvent.setup();
    const proposal: Proposal = {
      ready: true,
      fields: [
        {
          field: "offer_summary",
          value: "We build revenue software",
          confidence: 0.9,
          evidence_snippet: "We build revenue software for manufacturers.",
          source_url: "https://gradion.com",
        },
        {
          field: "icp",
          value: "Mid-market manufacturers",
          confidence: 0.6,
          evidence_snippet: "We serve manufacturers.",
          source_url: "https://gradion.com",
        },
        {
          field: "value_proposition",
          value: "Faster invoicing",
          confidence: 0.3,
          evidence_snippet: "Cuts invoicing time.",
          source_url: "https://gradion.com",
        },
      ],
      facts: [],
      open_questions: [],
      remaining_required_fields: ["display_name"],
      draft_version: 1,
      proposal_hash: "hash",
    };
    const draft = {
      ...EMPTY_DRAFT,
      values: {
        ...EMPTY_DRAFT.values,
        offer_summary: "We build revenue software",
        icp: "Mid-market manufacturers",
        value_proposition: "Faster invoicing",
        legal_name: "Acme GmbH",
        registered_address: "Hauptstraße 1, Berlin",
      },
      edited: new Set<CompanyFieldName>(["legal_name"]),
    };

    render(
      <CompanyConfirmCard
        proposal={proposal}
        draft={draft}
        answers={[]}
        selectedFactKeys={[]}
        setSelectedFactKeys={vi.fn()}
        missingRequired={["display_name"]}
        setField={vi.fn()}
        onAcceptAll={vi.fn()}
        pending={false}
        authorizing={false}
        error={null}
      />,
    );

    // display_name: required, empty. industry: empty, optional.
    // legal_name: typed. registered_address: stored (a value with no
    // grounding at all). offer_summary/icp/value_proposition: the three
    // confidence bands.
    const signalByField: Record<string, string> = {
      display_name: "required, still empty",
      industry: "empty",
      legal_name: "typed by you",
      registered_address: "from your profile",
      offer_summary: "high",
      icp: "medium",
      value_proposition: "low",
    };

    for (const [field, signal] of Object.entries(signalByField)) {
      const row = document.getElementById(`ob-triage-row-${field}`);
      if (row === null) {
        throw new Error(`expected a row for ${field}`);
      }
      // Every work state (required, low, empty) opens by default; collapse
      // it back to the one-line shape this test is actually proving.
      const showLess = within(row).queryByRole("button", {
        name: "Show less",
      });
      if (showLess !== null) {
        await user.click(showLess);
      }
      expect(within(row).getByText(signal)).toBeInTheDocument();
    }
  });

  // The whole point: the densest thing the crawl produced must be visible
  // content, grouped by type, the moment the review renders — never behind
  // a disclosure the reader has to know to open.
  it("shows facts as visible board content grouped by type, with no disclosure to open", () => {
    const proposal: Proposal = {
      ...triageProposal(),
      facts: [
        {
          category: "company",
          field: "founded_year",
          value: "Founded 2011",
          value_key: "company:founded_year:2011",
          evidence_snippet: "Founded in Hamburg in 2011.",
          evidence_url: "https://gradion.com/about",
          confidence: 0.9,
        },
        {
          category: "offering",
          field: "service",
          value: "Managed Kubernetes",
          value_key: "offering:service:k8s",
          evidence_snippet: "We run Kubernetes for logistics operators.",
          evidence_url: "https://gradion.com/services",
          confidence: 0.8,
        },
      ],
    };
    render(
      <CompanyConfirmCard
        proposal={proposal}
        draft={triageDraft()}
        answers={[]}
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

    // No disclosure anywhere on the surface — nothing to open.
    expect(document.querySelector("details")).toBeNull();
    // Both facts read straight off the page, each under its own type.
    expect(screen.getByText("Founded 2011")).toBeInTheDocument();
    expect(screen.getByText("Managed Kubernetes")).toBeInTheDocument();
    expect(screen.getByText("founded year")).toBeInTheDocument();
    expect(screen.getByText("service")).toBeInTheDocument();
    // The section is navigable like any other.
    const nav = screen.getByRole("navigation", { name: "Jump to a section" });
    expect(
      within(nav).getByRole("button", { name: /^Facts/ }),
    ).toBeInTheDocument();
  });

  // jsdom performs no layout, so it cannot assert a rendered pixel width —
  // it CAN assert the guarantee the width constraint depends on: the meta
  // column that holds the evidence chip renders inside the containing
  // `.ob-triage-fact` (never widens it out of the DOM's own containment),
  // the chip's own wrapper is present to carry the width cap
  // (`.evidence-chip-collapsed`/`.evidence-chip-snippet` in trust.css add
  // `min-width: 0` / `max-width: 100%` down the flex chain — a real browser
  // check, not one jsdom can run), and evidence-or-omit still holds: the
  // full, untruncated snippet is the text a reader gets once expanded, not
  // a silently shortened stand-in.
  it("keeps a very long evidence snippet inside the fact's own meta column rather than truncating it", async () => {
    const user = userEvent.setup();
    const longSnippet =
      "77 High Street, #09-11 High Street Plaza, Singapore (179433) Contact: info@gradion.com Authorized: Lars Jankowfsky Business Profile: 201629357M Gradion Pte Ltd trading as Gradion Consulting Group since incorporation";
    const proposal: Proposal = {
      ...triageProposal(),
      facts: [
        {
          category: "company",
          field: "contact_email",
          value: "info@gradion.com",
          value_key: "company:contact_email:info",
          evidence_snippet: longSnippet,
          evidence_url: "https://gradion.com/legal",
          confidence: 0.9,
        },
      ],
    };
    render(
      <CompanyConfirmCard
        proposal={proposal}
        draft={triageDraft()}
        answers={[]}
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

    await user.click(
      screen.getByRole("button", { name: "Evidence from gradion.com/legal" }),
    );

    const snippet = screen.getByText(`"${longSnippet}"`);
    expect(snippet).toHaveClass("evidence-chip-snippet");
    expect(snippet.closest(".ob-triage-fact-meta")).not.toBeNull();
  });

  it("still lets a fact be ticked or unticked from the new inline shape", async () => {
    const user = userEvent.setup();
    const setSelectedFactKeys = vi.fn();
    const proposal: Proposal = {
      ...triageProposal(),
      facts: [
        {
          category: "company",
          field: "founded_year",
          value: "Founded 2011",
          value_key: "company:founded_year:2011",
          evidence_snippet: "Founded in Hamburg in 2011.",
          evidence_url: "https://gradion.com/about",
          confidence: 0.9,
        },
      ],
    };
    render(
      <CompanyConfirmCard
        proposal={proposal}
        draft={triageDraft()}
        answers={[]}
        selectedFactKeys={[]}
        setSelectedFactKeys={setSelectedFactKeys}
        missingRequired={[]}
        setField={vi.fn()}
        onAcceptAll={vi.fn()}
        pending={false}
        authorizing={false}
        error={null}
      />,
    );

    await user.click(
      screen.getByRole("button", { name: "Save the fact: Founded 2011" }),
    );

    expect(setSelectedFactKeys).toHaveBeenCalledWith([
      "company:founded_year:2011",
    ]);
  });
});

// The read the edit form needs to offer facts at all: the fact list is the only
// part of it under test, so everything else is the read's empty shape.
function readWith(
  facts: readonly CompanySiteReadFact[],
): components["schemas"]["CompanySiteRead"] {
  return {
    id: "11111111-1111-4111-8111-111111111111",
    target_kind: "onboarding",
    root_url: "https://acme.test",
    status: "ready",
    status_code: null,
    status_detail: null,
    next_attempt_at: null,
    pages: [],
    profile_fields: [],
    facts: [...facts],
    comparisons: [],
    people: [],
    warnings: [],
    draft_version: 1,
    proposal_hash: "hash",
    created_at: "2026-07-01T09:00:00Z",
    updated_at: "2026-07-01T09:05:00Z",
  };
}

// The table on its own, so Escape can be observed as a callback rather than as
// the card's state change.
function TableOnly({ onClose }: Readonly<{ onClose: () => void }>) {
  const [keys, setKeys] = useState<readonly string[]>([]);
  const selection = useFactSelection(FACTS, keys, setKeys);
  return (
    <FactTable
      facts={FACTS}
      selection={selection}
      locale="en"
      onClose={onClose}
    />
  );
}
