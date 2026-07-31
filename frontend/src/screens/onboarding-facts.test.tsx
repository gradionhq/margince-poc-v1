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
import { MAX_SELECTED_FACTS } from "./onboarding";
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
        "No facts yet. Facts appear here as I read, each with the page it came from.",
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
