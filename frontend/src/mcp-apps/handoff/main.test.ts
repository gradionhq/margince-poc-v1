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

  // "Who to call" answered as a UUID restates the question. The name is what
  // the tool promises on this list, so it is what the panel shows.
  it("names the people to call, and falls to the id for one it cannot name", () => {
    const el = root();
    render(el, handoffFixture.data, []);
    expect(el.textContent).toContain("Alice Müller");

    render(
      el,
      {
        project_id: "5c4d3e2f-1a0b-4c9d-8e7f-6a5b4c3d2e1f",
        gaps: [],
        stakeholders: [{ person_id: "7c9e6679-7425-40de-944b-e07fc1f90ae7" }],
      },
      [],
    );
    expect(el.textContent).toContain("7c9e6679-7425-40de-944b-e07fc1f90ae7");
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

  // The tool always serializes `gaps`, an empty array at worst. An absent
  // member is proof of skew — and it is the member whose emptiness this panel
  // reads as "ready to hand over", which is the strongest claim it makes.
  it("refuses a payload whose gaps member is absent rather than clearing it", () => {
    const el = root();
    render(
      el,
      { project_id: "5c4d3e2f-1a0b-4c9d-8e7f-6a5b4c3d2e1f", name: "P" },
      [],
    );
    const empty = el.querySelector(".empty")?.textContent ?? "";
    expect(empty).toMatch(/no readable handoff/i);
    expect(empty).not.toMatch(/ready to hand over/i);
  });

  // Dropping ONE unreadable gap is right. Dropping the LAST one and then
  // announcing the work is ready is the failure — a gap lost in transit must
  // not read as a gap that does not exist.
  it("does not clear the work when every reported gap was unreadable", () => {
    const el = root();
    render(
      el,
      {
        project_id: "5c4d3e2f-1a0b-4c9d-8e7f-6a5b4c3d2e1f",
        name: "P",
        gaps: [{ code: "no_delivery_owner", source: "project.owner_id" }],
      },
      [],
    );
    const empty = el.querySelector(".empty")?.textContent ?? "";
    // The positive claim, in the words it is actually made in — the refusal
    // below contains "ready to hand over" too, inside "cannot say".
    expect(empty).not.toMatch(
      /nothing the records were checked for is missing/i,
    );
    expect(empty).toMatch(/not every check could be made/i);
  });

  // The tool WITHHOLDS the absence gaps when a list stopped at its bound and
  // says so with sweep_truncated. A view that dropped the warning would show a
  // briefing with checks missing as a briefing with nothing missing.
  it("never says the work is ready when a list stopped at its bound", () => {
    const el = root();
    render(el, ready(), [{ code: "sweep_truncated" }]);
    const empty = el.querySelector(".empty")?.textContent ?? "";
    expect(empty).not.toMatch(
      /nothing the records were checked for is missing/i,
    );
    expect(empty).toMatch(/withheld rather than guessed/i);
  });

  it("says the lists are partial even when there are gaps to show", () => {
    const el = root();
    render(el, handoffFixture.data, [{ code: "sweep_truncated" }]);
    expect(el.textContent).toMatch(/stopped at their bound/i);
  });

  it("says so when the host sent no structured result at all", () => {
    const el = root();
    render(el, null, []);
    expect(el.querySelector(".empty")?.textContent).toMatch(
      /no structured result/i,
    );
  });
});
