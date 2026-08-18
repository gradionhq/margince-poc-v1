/** @vitest-environment jsdom */
// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ChoiceList } from "./choicelist";

// This lane carries no auto-cleanup and no jest-dom: the DOM is torn down by
// hand, and state is read off the elements (`:checked`, the disabled attribute)
// rather than through matchers that are not installed here.
afterEach(cleanup);

// The question is a GROUP, every answer is readable at rest, and the words are
// part of the click target. Those three are the reasons this exists rather than
// ten hand-rolled wrappers around the Radio atom.

const CHOICES = [
  {
    value: "everyone_except" as const,
    label: "Everyone I talk to — except the people I leave out",
    description: "Every conversation goes in until you name somebody.",
  },
  {
    value: "only_chosen" as const,
    label: "Only the people I choose",
    description: "Nothing goes in until you name somebody.",
  },
];

describe("ChoiceList", () => {
  it("names the group, so the answers are one question rather than loose radios", () => {
    render(
      <ChoiceList
        legend="Which conversations go into the CRM?"
        value="only_chosen"
        choices={CHOICES}
        onChange={() => {}}
      />,
    );

    expect(
      screen.getByRole("group", {
        name: "Which conversations go into the CRM?",
      }),
    ).toBeTruthy();
    // Both answers are on screen at rest — the whole difference from a Select.
    expect(screen.getAllByRole("radio")).toHaveLength(2);
    expect(
      screen.getByRole("radio", { name: /Only the people I choose/ }),
    ).toBeTruthy();
    expect(
      screen.getAllByRole("radio").filter((radio) => radio.matches(":checked")),
    ).toHaveLength(1);
  });

  it("hides the legend from the page without taking it from the group's name", () => {
    render(
      <ChoiceList
        legend="Which conversations go into the CRM?"
        hideLegend
        value=""
        choices={CHOICES}
        onChange={() => {}}
      />,
    );

    const group = screen.getByRole("group", {
      name: "Which conversations go into the CRM?",
    });
    expect(group).toBeTruthy();
    expect(group.querySelector("legend")?.classList.contains("sr-only")).toBe(
      true,
    );
  });

  it("leaves every answer unchosen when nothing has been answered", () => {
    render(
      <ChoiceList
        legend="Which conversations go into the CRM?"
        value=""
        choices={CHOICES}
        onChange={() => {}}
      />,
    );

    // A pre-selected answer is one a reader can save without having read it.
    expect(
      screen.getAllByRole("radio").filter((radio) => radio.matches(":checked")),
    ).toHaveLength(0);
  });

  it("reads the description as part of the answer, so the help line is clickable", async () => {
    const onChange = vi.fn();
    render(
      <ChoiceList
        legend="Which conversations go into the CRM?"
        value="only_chosen"
        choices={CHOICES}
        onChange={onChange}
      />,
    );

    // The accessible name carries both halves: a reader hearing the option hears
    // what choosing it does.
    const option = screen.getByRole("radio", {
      name: /Everyone I talk to.*Every conversation goes in until you name somebody\./,
    });
    // And the words are the other half of the click target: pressing the help
    // line chooses the option it belongs to.
    await userEvent.click(
      screen.getByText("Every conversation goes in until you name somebody."),
    );
    expect(onChange).toHaveBeenCalledWith("everyone_except");
    expect(option).toBeTruthy();
  });

  it("refuses every answer when the group is disabled", async () => {
    const onChange = vi.fn();
    render(
      <ChoiceList
        legend="Which conversations go into the CRM?"
        value="only_chosen"
        choices={CHOICES}
        disabled
        onChange={onChange}
      />,
    );

    for (const radio of screen.getAllByRole("radio")) {
      expect(radio.hasAttribute("disabled")).toBe(true);
    }
    await userEvent.click(screen.getByText(/Everyone I talk to/));
    expect(onChange).not.toHaveBeenCalled();
  });

  it("groups two lists apart, so one page's two questions are not one", () => {
    render(
      <>
        <ChoiceList
          legend="First question"
          value="only_chosen"
          choices={CHOICES}
          onChange={() => {}}
        />
        <ChoiceList
          legend="Second question"
          value="everyone_except"
          choices={CHOICES}
          onChange={() => {}}
        />
      </>,
    );

    // Two names, and each group holds its own answer: with one shared radio
    // name the second would have unchecked the first.
    const names = new Set(
      screen
        .getAllByRole("radio")
        .map((radio) => radio.getAttribute("name") ?? ""),
    );
    expect(names.size).toBe(2);
    expect(
      screen.getAllByRole("radio").filter((r) => r.matches(":checked")),
    ).toHaveLength(2);
  });
});
