// @vitest-environment jsdom

import { describe, expect, it } from "vitest";
import { commitmentsFixture } from "./fixture";
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

describe("the commitments view renders what it was given", () => {
  it("renders one row per promise, in the order the seam answered", () => {
    const el = root();
    render(el, commitmentsFixture.data, []);
    expect(texts(el, ".name")).toEqual([
      "Send the signed SOW to Acme",
      "Confirm the security review date",
      "Draft the kickoff agenda",
      "Chase the reference call",
    ]);
  });

  it("says how late an overdue promise is, not merely that it is late", () => {
    const el = root();
    render(el, commitmentsFixture.data, []);
    expect(texts(el, ".state-overdue")).toEqual([
      "7d overdue",
      "overdue today",
    ]);
  });

  it("reads zero whole days as overdue TODAY rather than as zero days", () => {
    // Hours past its date is late by no whole days, which is not the same as
    // not being late — and "0d overdue" reads as neither.
    const el = root();
    render(el, commitmentsFixture.data, []);
    expect(el.textContent).not.toContain("0d overdue");
    expect(el.textContent).toContain("overdue today");
  });

  it("says a promise nobody owns is unowned rather than leaving a blank", () => {
    const el = root();
    render(el, commitmentsFixture.data, []);
    expect(el.textContent).toContain("unowned");
  });

  it("shows an undated promise as having no due date, never as overdue", () => {
    const el = root();
    render(
      el,
      {
        as_of: "2026-06-10T12:00:00Z",
        commitments: [{ subject: "Chase it", state: "undated" }],
      },
      [],
    );
    expect(el.querySelector(".state-undated")?.textContent).toBe("undated");
    expect(el.querySelector(".state-overdue")).toBeNull();
    expect(el.textContent).toContain("no due date");
  });

  it("names the instant the states were judged against", () => {
    const el = root();
    render(el, commitmentsFixture.data, []);
    expect(el.querySelector(".meta")?.textContent).toContain(
      "judged as of 2026-06-10",
    );
  });

  it("renders the empty state when nothing is outstanding", () => {
    const el = root();
    render(el, { as_of: "2026-06-10T12:00:00Z", commitments: [] }, []);
    expect(el.querySelector(".empty")?.textContent).toMatch(
      /nothing is outstanding/i,
    );
    expect(el.querySelectorAll(".row")).toHaveLength(0);
  });

  it("stops claiming the queue is everything when the sweep hit its bound", () => {
    const el = root();
    render(el, commitmentsFixture.data, [{ code: "sweep_truncated" }]);
    const meta = el.querySelector(".meta")?.textContent ?? "";
    expect(meta).toMatch(/more are outstanding/i);
    expect(meta).not.toMatch(/oldest promise first/i);
  });

  it("renders a state the seam has not published yet without colouring it wrongly", () => {
    const el = root();
    render(
      el,
      { commitments: [{ subject: "Something", state: "escalated" }] },
      [],
    );
    expect(el.querySelector(".state-escalated")).toBeNull();
    expect(el.textContent).toContain("escalated");
  });

  it("does not read a state name off the prototype chain", () => {
    const el = root();
    render(
      el,
      { commitments: [{ subject: "Something", state: "constructor" }] },
      [],
    );
    expect(el.querySelector(".state-constructor")).toBeNull();
  });

  it("names the record a promise is about, and falls to the id where there is no name", () => {
    const el = root();
    render(
      el,
      {
        commitments: [
          {
            subject: "Something",
            state: "upcoming",
            about: [{ entity_type: "lead", entity_id: "l-77" }],
          },
        ],
      },
      [],
    );
    expect(el.textContent).toContain("lead: l-77");
  });

  it("falls to the empty state on a payload of the wrong shape rather than throwing", () => {
    const el = root();
    expect(() => render(el, 42, [])).not.toThrow();
    expect(el.querySelector(".empty")).not.toBeNull();
  });

  it("says so when the host sent no structured result at all", () => {
    const el = root();
    render(el, null, []);
    expect(el.querySelector(".empty")?.textContent).toMatch(
      /no structured result/i,
    );
  });
});
