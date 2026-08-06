/** @vitest-environment jsdom */
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";
import { LocaleProvider } from "../i18n";
import { EvidenceMark } from "./evidencemark";

// The one provenance affordance. What it has to get right:
//
//   - a value a PERSON typed carries no mark, or the underline stops meaning
//     "this was derived" and starts meaning nothing;
//   - the receipts are reachable by keyboard, and Escape gives focus back;
//   - the quoted source text is presented as a quote, never as our own words.

afterEach(cleanup);

function show(ui: React.ReactNode) {
  return render(<LocaleProvider initial="en">{ui}</LocaleProvider>);
}

describe("evidence mark", () => {
  it("marks a derived value and shows where it came from", async () => {
    show(
      <EvidenceMark
        value="Fleet retrofits without downtime"
        source={{
          provenance: { kind: "agent", agent: "capture" },
          confidence: "high",
          snippet: "We retrofit fleets without downtime",
          sourceUrl: "https://brandt.example",
          at: "1 Jul 2026",
        }}
      />,
    );

    const trigger = screen.getByRole("button", {
      name: /Fleet retrofits without downtime/,
    });
    expect(trigger.getAttribute("aria-expanded")).toBe("false");
    await userEvent.click(trigger);

    expect(trigger.getAttribute("aria-expanded")).toBe("true");
    const panel = screen.getByRole("region", {
      name: /Where "Fleet retrofits without downtime" came from/,
    });
    expect(panel.textContent).toContain("We retrofit fleets without downtime");
    expect(panel.textContent).toContain("https://brandt.example");
    expect(panel.textContent).toContain("1 Jul 2026");
    // The snippet is quoted markup, so it never reads as the product's own
    // words about the account.
    expect(panel.querySelector("blockquote")).toBeTruthy();
  });

  it("carries the glyph on a derived value and nothing on a typed one", () => {
    const derived = show(
      <EvidenceMark
        value="Fleet retrofits without downtime"
        source={{ provenance: { kind: "agent", agent: "capture" } }}
      />,
    );
    // Scannable, not merely discoverable: the underline answers "was this
    // derived" only for a reader who already hovers, and a column of values
    // gives nobody a reason to.
    expect(derived.container.querySelector(".evmark-glyph")).toBeTruthy();

    cleanup();
    const typed = show(
      <EvidenceMark value="Brandt Automotive GmbH" source={undefined} />,
    );
    expect(typed.container.querySelector(".evmark-glyph")).toBeNull();
  });

  it("leaves a human-typed value unmarked", () => {
    show(<EvidenceMark value="Brandt Automotive GmbH" source={undefined} />);
    expect(screen.getByText("Brandt Automotive GmbH")).toBeTruthy();
    // No trigger at all: an underline on every value would teach the reader
    // that the underline means nothing.
    expect(screen.queryByRole("button")).toBeNull();
  });

  it("closes on Escape and hands focus back to the value", async () => {
    show(
      <EvidenceMark
        value="1998"
        source={{
          provenance: { kind: "agent", agent: "capture" },
          snippet: "Founded in 1998",
        }}
      />,
    );

    const trigger = screen.getByRole("button", { name: /1998/ });
    await userEvent.click(trigger);
    expect(screen.getByRole("region")).toBeTruthy();

    await userEvent.keyboard("{Escape}");
    expect(screen.queryByRole("region")).toBeNull();
    // Escape must not drop the reader at the top of the document.
    expect(document.activeElement).toBe(trigger);
  });

  it("offers no full-history link when there is nowhere to send the reader", async () => {
    show(
      <EvidenceMark
        value="1998"
        source={{ provenance: { kind: "agent", agent: "capture" } }}
      />,
    );
    await userEvent.click(screen.getByRole("button", { name: /1998/ }));
    expect(screen.queryByText("Full history")).toBeNull();
  });

  it("hands the reader through to the full history when one exists", async () => {
    let opened = false;
    show(
      <EvidenceMark
        value="1998"
        source={{ provenance: { kind: "agent", agent: "capture" } }}
        onOpenHistory={() => {
          opened = true;
        }}
      />,
    );
    await userEvent.click(screen.getByRole("button", { name: /1998/ }));
    await userEvent.click(screen.getByText("Full history"));

    expect(opened).toBe(true);
    // The panel closes on the way out, so the reader does not return to a
    // stale popover hanging over the surface they navigated to.
    expect(screen.queryByRole("region")).toBeNull();
  });

  it("keeps one panel open at a time, whatever opened it", async () => {
    show(
      <>
        <EvidenceMark
          value="1998"
          source={{ provenance: { kind: "agent", agent: "capture" } }}
        />
        <EvidenceMark
          value="Stuttgart"
          source={{ provenance: { kind: "agent", agent: "capture" } }}
        />
      </>,
    );

    // Keyboard only: pointer dismissal is not what should be enforcing this.
    await userEvent.tab();
    await userEvent.keyboard("{Enter}");
    expect(screen.getAllByRole("region")).toHaveLength(1);

    await userEvent.tab();
    await userEvent.keyboard("{Enter}");
    const open = screen.getAllByRole("region");
    expect(open).toHaveLength(1);
    expect(open[0].getAttribute("aria-label")).toContain("Stuttgart");
  });
});
