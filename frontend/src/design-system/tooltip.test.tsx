/** @vitest-environment jsdom */
// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import "@testing-library/jest-dom/vitest";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { useTruncationTooltip } from "./tooltip";

// The specs for the tip that reveals a string its row could not fit. The
// promise the three call sites depend on is narrow and worth stating twice: the
// tip appears for text that IS clipped and stays away from text that is not,
// because a tip repeating a name already fully on screen is noise on every row
// of a page.

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

const LONG = "In contact, but one person carries the whole account.";

// jsdom lays nothing out, so every element reports a width of zero and no text
// is ever clipped. These are the two measurements the hook reads, and stubbing
// them is what lets a test say "this string did not fit" at all.
function stubWidths({ scroll, client }: { scroll: number; client: number }) {
  vi.spyOn(HTMLElement.prototype, "scrollWidth", "get").mockReturnValue(scroll);
  vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(client);
}

function Row({ text }: Readonly<{ text: string }>) {
  const tip = useTruncationTooltip<HTMLSpanElement>(text);
  return (
    <span ref={tip.ref} {...tip.trigger}>
      {text}
      {tip.tip}
    </span>
  );
}

describe("a string too long for its row", () => {
  it("reveals the whole of it on hover", async () => {
    stubWidths({ scroll: 480, client: 220 });
    render(<Row text={LONG} />);
    expect(screen.queryByRole("tooltip")).toBeNull();

    await userEvent.hover(screen.getByText(LONG));

    expect(screen.getByRole("tooltip")).toHaveTextContent(LONG);
  });

  it("takes the tip away again when the pointer leaves", async () => {
    stubWidths({ scroll: 480, client: 220 });
    render(<Row text={LONG} />);
    const row = screen.getByText(LONG);

    await userEvent.hover(row);
    await userEvent.unhover(row);

    expect(screen.queryByRole("tooltip")).toBeNull();
  });

  it("describes the row it belongs to, so a screen reader reads the two as one", async () => {
    stubWidths({ scroll: 480, client: 220 });
    render(<Row text={LONG} />);
    const row = screen.getByText(LONG);

    await userEvent.hover(row);

    const tip = screen.getByRole("tooltip");
    expect(row).toHaveAttribute("aria-describedby", tip.id);
  });

  it("is reachable by keyboard and answers focus, not only a pointer", async () => {
    stubWidths({ scroll: 480, client: 220 });
    render(<Row text={LONG} />);
    // Captured before the tip opens: once it is up, the row and the tip carry
    // the same string and a text query matches both.
    const row = screen.getByText(LONG);

    await userEvent.tab();

    expect(row).toHaveFocus();
    expect(screen.getByRole("tooltip")).toHaveTextContent(LONG);
  });

  it("dismisses on Escape without waiting for focus to move", async () => {
    stubWidths({ scroll: 480, client: 220 });
    render(<Row text={LONG} />);
    const row = screen.getByText(LONG);
    await userEvent.tab();

    await userEvent.keyboard("{Escape}");

    expect(screen.queryByRole("tooltip")).toBeNull();
    // Still focused: Escape dismissed the tip, it did not leave the row.
    expect(row).toHaveFocus();
  });
});

describe("a string its row can already show in full", () => {
  it("gets no tip on hover, having nothing left to reveal", async () => {
    stubWidths({ scroll: 220, client: 220 });
    render(<Row text="Sontana" />);

    await userEvent.hover(screen.getByText("Sontana"));

    expect(screen.queryByRole("tooltip")).toBeNull();
  });

  it("takes no tab stop, so a page of short names adds none", () => {
    stubWidths({ scroll: 220, client: 220 });
    render(<Row text="Sontana" />);

    expect(screen.getByText("Sontana")).not.toHaveAttribute("tabindex");
  });
});
