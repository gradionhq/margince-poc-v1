// @vitest-environment jsdom

import { describe, expect, it } from "vitest";
import { handoffFixture } from "./fixture";
import { render } from "./main";

function root(): HTMLElement {
  const el = document.createElement("main");
  el.id = "root";
  document.body.replaceChildren(el);
  return el;
}

function texts(el: HTMLElement, selector: string): (string | null)[] {
  return [...el.querySelectorAll(selector)].map((n) => n.textContent);
}

/** ready is the same handover with nothing missing: no gaps, and an owner. */
function ready(): Record<string, unknown> {
  const data = { ...(handoffFixture.data as Record<string, unknown>) };
  data.gaps = [];
  data.owner_id = "0f8fad5b-d9cb-469f-a165-70867728950e";
  data.target_end_date = "2026-09-30T00:00:00Z";
  return data;
}

describe("the handoff view renders what it was given", () => {
  it("leads with what is missing, before what the project has", () => {
    // A brief that led with its facts reads as complete, which is the failure
    // the gaps exist to prevent.
    const el = root();
    render(el, handoffFixture.data, []);
    const titles = texts(el, ".section-title");
    expect(titles[0]).toMatch(/still missing/i);
    expect(titles.slice(1)).toEqual([
      "What was sold",
      "Who to call",
      "Already promised",
    ]);
  });

  it("shows every gap with the field it was read off", () => {
    const el = root();
    render(el, handoffFixture.data, []);
    expect(texts(el, ".gap .source")).toEqual([
      "project.owner_id",
      "project.target_end_date",
      "relationship.role",
      "deal.amount_minor",
      "activity.due_at",
    ]);
  });

  it("drops a gap that carries no message rather than rendering an empty warning", () => {
    const el = root();
    render(
      el,
      {
        project_id: "5c4d3e2f-1a0b-4c9d-8e7f-6a5b4c3d2e1f",
        name: "P",
        gaps: [{ code: "x", source: "a.b" }, { message: "Real" }],
      },
      [],
    );
    expect(texts(el, ".gap")).toHaveLength(1);
    expect(el.querySelector(".gap")?.textContent).toContain("Real");
  });

  it("says the work is ready when nothing checked for is missing", () => {
    const el = root();
    render(el, ready(), []);
    expect(el.querySelector(".empty")?.textContent).toMatch(
      /ready to hand over/i,
    );
  });

  it("says no owner in the headline when nobody is receiving the work", () => {
    const el = root();
    render(el, handoffFixture.data, []);
    expect(el.querySelector(".meta")?.textContent).toContain("no owner");
  });

  it("says no target end date rather than leaving the headline short", () => {
    const el = root();
    render(el, handoffFixture.data, []);
    expect(el.querySelector(".meta")?.textContent).toContain(
      "no target end date",
    );
  });

  it("renders the target date when there is one", () => {
    const el = root();
    render(el, ready(), []);
    expect(el.querySelector(".meta")?.textContent).toContain(
      "target 2026-09-30",
    );
  });

  it("shows an untitled seat as having no recorded part, not as a blank", () => {
    const el = root();
    render(el, handoffFixture.data, []);
    expect(el.textContent).toContain("no recorded part");
  });

  it("scales a won deal's amount by the currency's minor units, and shows an unpriced one as absent", () => {
    const el = root();
    render(el, handoffFixture.data, []);
    const amounts = texts(el, ".score");
    expect(amounts[0]).toContain("240,000");
    expect(amounts[1]).toBe("—");
  });

  it("colours only the promise that is already past due at handover", () => {
    const el = root();
    render(el, handoffFixture.data, []);
    expect(texts(el, ".state-overdue")).toEqual(["overdue · 2026-06-05"]);
  });

  it("omits a section the project has nothing in rather than heading a void", () => {
    const el = root();
    render(
      el,
      {
        project_id: "5c4d3e2f-1a0b-4c9d-8e7f-6a5b4c3d2e1f",
        name: "Bare",
        gaps: [],
        deals: [],
        stakeholders: [],
      },
      [],
    );
    expect(texts(el, ".section-title")).toEqual([]);
  });

  // A payload of the wrong shape narrows to an empty record, which has no
  // gaps — and an answer with no gaps is the one that reads "ready to hand
  // over". So it has to be refused as unreadable rather than narrowed, or the
  // view clears a project for handover on the strength of a number.
  it("refuses a payload that is not a handoff rather than clearing it for handover", () => {
    const el = root();
    expect(() => render(el, 42, [])).not.toThrow();
    const empty = el.querySelector(".empty")?.textContent ?? "";
    expect(empty).toMatch(/no readable handoff/i);
    expect(empty).not.toMatch(/ready to hand over/i);
  });

  it("refuses another tool's answer, which carries no project_id", () => {
    const el = root();
    render(el, { deals: [], gaps: [] }, []);
    expect(el.querySelector(".empty")?.textContent).toMatch(
      /no readable handoff/i,
    );
  });

  it("says so when the host sent no structured result at all", () => {
    const el = root();
    render(el, null, []);
    expect(el.querySelector(".empty")?.textContent).toMatch(
      /no structured result/i,
    );
  });
});
