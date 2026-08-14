/** @vitest-environment jsdom */
import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { components } from "../api/schema";
import { LocaleProvider } from "../i18n";
import { PersonStrip } from "./personstrip";

type Person360 = components["schemas"]["Person360"];

// The strip and the consent gate answer the same question — has this person
// ever written to us — from the same field, `activity.direction`. They are
// computed in different places (this file counts the 360's rows; the server's
// consent verdict runs its own query), so a reader who sees "1 in" beside "they
// have never written to you" cannot tell which half to believe and learns to
// trust neither.
//
// These pin the counting rule this side owns. What makes the two agree is that
// both read `direction`, and a change here that started counting something else
// — a participant row, a thread the person appears on — is what would split
// them.

function viewWith(directions: readonly string[]): Person360 {
  return {
    person: { id: "p1", full_name: "Marine Raucoules" },
    activities: {
      data: directions.map((direction, i) => ({
        id: `a${i}`,
        kind: "email",
        direction,
        occurred_at: "2026-08-11T12:00:00Z",
      })),
    },
  } as unknown as Person360;
}

function renderStrip(view: Person360) {
  render(
    <LocaleProvider initial="en">
      <PersonStrip view={view} consentVerdict={undefined} />
    </LocaleProvider>,
  );
}

describe("the reciprocity reading", () => {
  it("counts nothing inbound for a person who has only been written TO", () => {
    renderStrip(viewWith(["outbound", "outbound"]));

    // The state behind the reported contradiction: two messages on the
    // timeline, both ours. The consent gate calls this "they have never
    // written to you", and the strip must not claim otherwise.
    expect(screen.getByText("0 in · 2 out")).toBeTruthy();
  });

  it("counts an inbound message once it exists", () => {
    renderStrip(viewWith(["inbound", "outbound"]));

    expect(screen.getByText("1 in · 1 out")).toBeTruthy();
  });

  it("counts nothing for a person with no captured activity", () => {
    renderStrip(viewWith([]));

    expect(screen.getByText("0 in · 0 out")).toBeTruthy();
  });

  // A row whose direction is neither is not evidence that they wrote: a note,
  // a task, an internal record. Counting "not outbound" as inbound is the
  // shape that would make the strip claim a message the consent gate cannot
  // see — which is exactly the contradiction this pins.
  it("counts a directionless row as neither", () => {
    renderStrip(viewWith(["outbound", "internal", ""]));

    expect(screen.getByText("0 in · 1 out")).toBeTruthy();
  });
});
