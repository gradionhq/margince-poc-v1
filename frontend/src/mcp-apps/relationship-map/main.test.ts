// @vitest-environment jsdom

import { describe, expect, it } from "vitest";
import { relationshipMapFixture } from "./fixture";
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

describe("the relationship map renders what it was given", () => {
  it("renders one row per colleague, in the warmest-first order the seam answered", () => {
    const el = root();
    render(el, relationshipMapFixture.data, []);
    expect(texts(el, ".name")).toEqual([
      "Dana Okafor",
      "Ravi Bhatt",
      "Mira Lindqvist",
    ]);
    expect(texts(el, ".rank")).toEqual(["#1", "#2", "#3"]);
  });

  it("renders the empty state when nobody here has spoken to the contact", () => {
    const el = root();
    render(el, { person_id: "p-1", colleagues: [] }, []);
    expect(el.querySelector(".empty")).not.toBeNull();
    expect(el.querySelectorAll(".row")).toHaveLength(0);
  });

  it("shows a never-spoken colleague as absent rather than as a strength of zero", () => {
    // Never having spoken is not a score of zero. Rendering a missing strength
    // as 0 would tell a rep a relationship decayed when none ever existed.
    const el = root();
    render(el, relationshipMapFixture.data, []);
    const cold = el.querySelector(".band-none");
    expect(cold).not.toBeNull();
    expect(cold?.textContent).toBe("none");
    expect(cold?.textContent).not.toContain("0");
  });

  it("says the list is not the whole network when the sweep stopped at its bound", () => {
    const el = root();
    render(el, relationshipMapFixture.data, [{ code: "sweep_truncated" }]);
    expect(el.querySelector(".meta")?.textContent).toMatch(
      /not the whole network/i,
    );
  });

  it("claims warmest-first only when the ranking is complete", () => {
    const el = root();
    render(el, relationshipMapFixture.data, []);
    expect(el.querySelector(".meta")?.textContent).toMatch(/warmest first/i);
  });

  it("renders a band the seam has not published yet without colouring it wrongly", () => {
    // The vocabulary belongs to the seam. A view that refused an unknown value
    // would go blank the first time one was added.
    const el = root();
    render(
      el,
      {
        person_id: "p-1",
        colleagues: [{ display_name: "Sam", strength_bucket: "scorching" }],
      },
      [],
    );
    expect(el.querySelector(".band-scorching")).toBeNull();
    expect(el.textContent).toContain("scorching");
  });

  it("does not read a band name off the prototype chain", () => {
    // A bucket of "constructor" finds a truthy value on any plain object, and a
    // lookup that trusted it would emit a class this stylesheet does not have.
    const el = root();
    render(
      el,
      {
        person_id: "p-1",
        colleagues: [{ display_name: "Sam", strength_bucket: "constructor" }],
      },
      [],
    );
    expect(el.querySelector(".band-constructor")).toBeNull();
  });

  it("names a colleague by user id when the seam answered no display name", () => {
    const el = root();
    render(
      el,
      {
        person_id: "p-1",
        colleagues: [{ user_id: "u-77", strength_bucket: "low" }],
      },
      [],
    );
    expect(el.querySelector(".name")?.textContent).toBe("u-77");
  });

  it("renders a missing interaction count as an em dash, never as NaN", () => {
    const el = root();
    render(
      el,
      {
        person_id: "p-1",
        colleagues: [{ display_name: "Sam", strength_bucket: "low" }],
      },
      [],
    );
    expect(el.textContent).toContain("—");
    expect(el.textContent).not.toContain("NaN");
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
