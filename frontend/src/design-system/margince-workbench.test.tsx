// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

/** @vitest-environment jsdom */
import "@testing-library/jest-dom/vitest";

import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";
import {
  MarginceWorkbench,
  type WorkbenchRuntimeLabels,
} from "./margince-workbench";

// The workbench chrome's own claims, as opposed to what the acts put inside it.
// The one that needs proving is the runtime chip: hover and keyboard focus BOTH
// open its popover, which is what makes closing it easy to get wrong — a press
// that only clears the pin leaves the popover open under a focus that is still
// there, and the button then looks dead.

const LABELS: WorkbenchRuntimeLabels = {
  configured: "Configured",
  used: "Answered by",
  route: "Route",
  calls: "Calls",
  tokens: "Tokens",
  latency: "Latency",
  estimatedCost: "Estimated cost",
  partial: "Partial",
  awaiting: "Shown after my first model call",
  unavailable: "Not available yet",
  chip: "What is answering, and what it costs",
  answering: "Answering right now",
  scope: "This run only",
};

afterEach(cleanup);

function renderWorkbench() {
  return render(
    <MarginceWorkbench
      state="working"
      eyebrow="Margince"
      title="Your company research AI"
      status="Reading"
      configured="ollama/gemma3"
      locale="en"
      runtimeLabels={LABELS}
      steps={[
        { label: "Read", state: "done" },
        { label: "Confirm", state: "now" },
        { label: "Voice", state: "todo" },
      ]}
    >
      <p>Thread</p>
    </MarginceWorkbench>,
  );
}

describe("the runtime chip", () => {
  it("pins the popover open on the first press, even though focus already opened it", async () => {
    renderWorkbench();
    const chip = screen.getByRole("button", { name: new RegExp(LABELS.chip) });

    // Tab moves focus onto the chip, which opens the popover on its own — the
    // keyboard's equivalent of the pointer arriving.
    await userEvent.tab();
    expect(chip).toHaveFocus();
    expect(chip).toHaveAttribute("aria-expanded", "true");

    // The press pins what focus already opened rather than reading "already
    // open" as a request to close it — a reader who presses once should not
    // have to press again just to make the popover stick.
    await userEvent.keyboard("[Space]");
    expect(chip).toHaveAttribute("aria-expanded", "true");

    // A second press is the honest close: it un-pins and dismisses.
    await userEvent.keyboard("[Space]");
    expect(chip).toHaveAttribute("aria-expanded", "false");

    // A third press opens it again.
    await userEvent.keyboard("[Space]");
    expect(chip).toHaveAttribute("aria-expanded", "true");
  });

  it("puts the spend it shows into the name a screen reader hears", () => {
    renderWorkbench();

    // The chip's whole purpose is that nobody has to ask what a run costs. A
    // bare aria-label would override the figure on screen, so the one number
    // that must always be available would be the one never announced.
    expect(
      screen.getByRole("button", {
        name: `${LABELS.chip}: ${LABELS.awaiting}`,
      }),
    ).toBeInTheDocument();
  });

  it("closes on Escape when the keyboard is what opened it", async () => {
    renderWorkbench();
    const chip = screen.getByRole("button", { name: new RegExp(LABELS.chip) });

    await userEvent.tab();
    expect(chip).toHaveAttribute("aria-expanded", "true");

    await userEvent.keyboard("{Escape}");
    expect(chip).toHaveAttribute("aria-expanded", "false");
  });

  it("opens again after focus leaves and comes back", async () => {
    renderWorkbench();
    const chip = screen.getByRole("button", { name: new RegExp(LABELS.chip) });

    await userEvent.tab();
    await userEvent.keyboard("{Escape}");
    expect(chip).toHaveAttribute("aria-expanded", "false");

    // Leaving resets the dismissal: a reader who tabs back is asking again.
    chip.blur();
    await userEvent.tab();
    expect(chip).toHaveFocus();
    expect(chip).toHaveAttribute("aria-expanded", "true");
  });
});

describe("the step rail", () => {
  it("states where the journey is without offering a control", () => {
    renderWorkbench();

    const rail = screen.getByRole("list");
    const stops = [...rail.querySelectorAll("li")];
    expect(stops.map((stop) => stop.textContent)).toEqual([
      "1Readdone",
      "2Confirmin progress",
      "3Voicewaiting",
    ]);
    // The machine decides what comes next, so no stop may look clickable.
    expect(rail.querySelector("button")).toBeNull();
    expect(rail.querySelector("a")).toBeNull();
    expect(stops.map((stop) => stop.className)).toEqual([
      "mw-step is-done",
      "mw-step is-now",
      "mw-step is-todo",
    ]);
  });

  // On screen the three states are told apart by label colour and a ring on the
  // numeral. A reader who gets none of that has to be told, so each stop names
  // its state in words that never show, and the current one is marked as such.
  it("says each stop's state in words for assistive tech", () => {
    renderWorkbench();

    const stops = screen.getAllByRole("listitem");
    expect(
      stops.map((stop) => stop.querySelector(".sr-only")?.textContent),
    ).toEqual(["done", "in progress", "waiting"]);
  });

  it("marks the stop the journey is on, and only that one", () => {
    renderWorkbench();

    const stops = screen.getAllByRole("listitem");
    expect(stops.map((stop) => stop.getAttribute("aria-current"))).toEqual([
      null,
      "step",
      null,
    ]);
  });

  // `list-style: none` is enough for Safari to drop list semantics, and with
  // them the "stop 2 of 3" a screen reader would otherwise announce. The
  // numeral is decorative precisely because the list position says it.
  it("keeps list semantics even though the bullets are styled off", () => {
    renderWorkbench();

    const rail = screen.getByRole("list");
    expect(rail.tagName).toBe("OL");
    expect(rail).toHaveAttribute("role", "list");
    expect(screen.getAllByRole("listitem")).toHaveLength(3);
    expect(rail.querySelector("b")).toHaveAttribute("aria-hidden");
  });
});

// The rail is the variant with no top bar, so its foot row is the only place a
// surface-level control can live. The row therefore has to hold that control
// even while the signed-in identity beside it is still loading — a control that
// appears one request late reads as a control that is not there.
describe("the rail's person row", () => {
  // The variant is a parameter rather than a constant because the row's absence
  // from the split layout is a claim about the LAYOUT: it only means anything
  // when the same person and control the rail draws are supplied and still
  // produce nothing.
  function renderPersonRow(
    variant: "split" | "rail",
    person?: Readonly<{ name: string; detail: string }>,
  ) {
    return render(
      <MarginceWorkbench
        state="working"
        eyebrow="Margince"
        title="Your company research AI"
        status="Reading"
        configured="ollama/gemma3"
        locale="en"
        runtimeLabels={LABELS}
        variant={variant}
        person={person}
        personAction={<button type="button">Theme</button>}
        artifact={<p>Work</p>}
      >
        <p>Thread</p>
      </MarginceWorkbench>,
    );
  }

  it("puts the caller's control after the identity it belongs beside", () => {
    const { container } = renderPersonRow("rail", {
      name: "Ada Lovelace",
      detail: "ada@example.com",
    });

    const row = container.querySelector(".mw-person");
    expect(row).not.toBeNull();
    expect(row?.textContent).toContain("Ada Lovelace");
    // Last child, so the identity keeps the reading order and the control
    // stays at the row's right-hand end.
    expect(row?.lastElementChild).toHaveClass("mw-person-action");
    expect(row?.lastElementChild?.textContent).toBe("Theme");
  });

  it("still renders the control while the identity is unresolved", () => {
    const { container } = renderPersonRow("rail", undefined);

    expect(container.querySelector(".mw-person-avatar")).toBeNull();
    expect(screen.getByRole("button", { name: "Theme" })).toBeInTheDocument();
  });

  it("renders no person row at all in the split variant", () => {
    const { container } = renderPersonRow("split", {
      name: "Ada Lovelace",
      detail: "ada@example.com",
    });

    // Given everything the rail needs to draw the row, the split layout still
    // draws none of it — the top bar carries the identity and its controls
    // there, and a second copy in the thread would be the duplicate.
    expect(container.querySelector(".mw-person")).toBeNull();
    expect(container.textContent).not.toContain("Ada Lovelace");
    expect(screen.queryByRole("button", { name: "Theme" })).toBeNull();
  });
});
