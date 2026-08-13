// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

/** @vitest-environment jsdom */
import "@testing-library/jest-dom/vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { Callout } from "./callout";
import { FactList } from "./factlist";

afterEach(cleanup);

describe("Callout", () => {
  it("stays silent unless the caller says it appeared for a reason", () => {
    const { container } = render(<Callout>Nothing urgent.</Callout>);
    // A notice rendered with the page has nothing to interrupt for. Announcing
    // every one of them is how a reader learns to ignore the ones that matter.
    expect(container.querySelector("[role]")).toBeNull();
  });

  it("interrupts only where the caller asked for it", () => {
    render(
      <Callout tone="danger" live="alert">
        That did not save.
      </Callout>,
    );
    expect(screen.getByRole("alert")).toHaveTextContent("That did not save.");
  });

  it("carries the tone as a class rather than a colour", () => {
    const { container } = render(<Callout tone="warn">Running low.</Callout>);
    // Tone is never the only signal — the words carry the meaning — but it has
    // to reach CSS as something the theme can restyle in both palettes.
    expect(container.querySelector(".callout-warn")).toBeInTheDocument();
  });

  it("renders a title and actions when given them, and neither when not", () => {
    const { container, rerender } = render(
      <Callout
        title="Reindex needed"
        actions={<button type="button">Open</button>}
      >
        The index is behind.
      </Callout>,
    );
    expect(screen.getByText("Reindex needed")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Open" })).toBeInTheDocument();

    rerender(<Callout>The index is behind.</Callout>);
    expect(container.querySelector(".callout-title")).toBeNull();
    expect(container.querySelector(".callout-actions")).toBeNull();
  });
});

describe("FactList", () => {
  it("pairs every term with its value", () => {
    render(
      <FactList
        facts={[
          { key: "in", term: "Last inbound", value: "3 Feb 2026" },
          { key: "out", term: "Last outbound", value: "Never" },
        ]}
      />,
    );
    const terms = screen.getAllByRole("term").map((node) => node.textContent);
    const values = screen
      .getAllByRole("definition")
      .map((node) => node.textContent);
    expect(terms).toEqual(["Last inbound", "Last outbound"]);
    expect(values).toEqual(["3 Feb 2026", "Never"]);
  });

  it("keeps rows apart when the term repeats, which the real data does", () => {
    render(
      <FactList
        facts={[
          { key: "email-1", term: "Email", value: "a@acme.test" },
          { key: "email-2", term: "Email", value: "b@acme.test" },
        ]}
      />,
    );
    expect(screen.getAllByRole("term")).toHaveLength(2);
    expect(screen.getByText("b@acme.test")).toBeInTheDocument();
  });

  it("puts a note under its value rather than beside it", () => {
    const { container } = render(
      <FactList
        facts={[
          {
            key: "spend",
            term: "Spend",
            value: "€1,200",
            note: "Partial month",
          },
        ]}
      />,
    );
    const note = container.querySelector(".factlist-note");
    expect(note).toHaveTextContent("Partial month");
    // Inside the value, so a reader hears the qualifier with the figure it
    // qualifies rather than as a row of its own.
    expect(note?.closest(".factlist-value")).not.toBeNull();
  });
});
