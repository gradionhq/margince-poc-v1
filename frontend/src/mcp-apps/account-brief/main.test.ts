// @vitest-environment jsdom

import { describe, expect, it } from "vitest";
import { accountBriefFixture } from "./fixture";
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

describe("the account brief renders what it was given", () => {
  it("renders one row per queue item, in the order given", () => {
    const el = root();
    render(el, accountBriefFixture.data, []);
    expect(texts(el, ".name")).toEqual([
      "8f14e45f-ceea-467a-9a1a-2e9b0e4c3d21",
      "c9f0f895-fb98-4b1b-9a5b-1d3f2e6a7c04",
    ]);
    expect(texts(el, ".rank")).toEqual(["#1", "#2"]);
  });

  it("reports how many candidates the ranking left out, which is the brief's own honesty rule", () => {
    // candidate_count may exceed the queue, and the difference is what ranked
    // and was not shown. A view that printed only the rows would be presenting
    // a shortlist as the whole field.
    const el = root();
    render(el, accountBriefFixture.data, []);
    expect(el.querySelector(".meta")?.textContent).toContain(
      "2 of 7 candidates",
    );
  });

  it("renders the empty state when the queue is empty, because an empty queue is a real answer", () => {
    const el = root();
    render(el, { items: [], candidate_count: 0 }, []);
    expect(el.querySelector(".empty")).not.toBeNull();
    expect(el.querySelectorAll(".row")).toHaveLength(0);
  });

  it("renders a missing factor as an em dash, never as NaN", () => {
    const el = root();
    render(
      el,
      {
        candidate_count: 1,
        items: [
          {
            deal_id: "d-1",
            rank: 1,
            composite: 0.5,
            factors: { winnability: 0.9 },
          },
        ],
      },
      [],
    );
    expect(el.textContent).toContain("—");
    expect(el.textContent).not.toContain("NaN");
    expect(el.textContent).toContain("win 90%");
  });

  it("keeps a row whose deal id is arbitrary text as text, never as markup", () => {
    // structuredContent is customer data by this system's own reckoning. The
    // one privilege the sandbox cannot take back is execution inside the view's
    // own origin, so a name that looks like a tag has to arrive as characters.
    const el = root();
    render(
      el,
      {
        candidate_count: 1,
        items: [{ deal_id: "<img onerror=x>", rank: 1, composite: 0.5 }],
      },
      [],
    );
    expect(el.querySelector(".name")?.textContent).toBe("<img onerror=x>");
    expect(el.querySelector("img")).toBeNull();
  });

  it("skips a queue entry that is not an object rather than counting it", () => {
    // Counting the raw list and rendering the filtered one is how a view says
    // "3 items" above two rows.
    const el = root();
    render(
      el,
      { candidate_count: 9, items: [null, "x", { deal_id: "d-1", rank: 1 }] },
      [],
    );
    expect(el.querySelectorAll(".row")).toHaveLength(1);
    expect(el.querySelector(".meta")?.textContent).toContain(
      "1 of 9 candidates",
    );
  });

  it("falls to the empty state on a payload of the wrong shape rather than throwing", () => {
    const el = root();
    expect(() => render(el, "not an object", [])).not.toThrow();
    expect(el.querySelector(".empty")).not.toBeNull();
  });

  it("says so when the host sent no structured result at all", () => {
    const el = root();
    render(el, null, []);
    expect(el.querySelector(".empty")?.textContent).toMatch(
      /no structured result/i,
    );
  });

  it("replaces the previous answer when a second result arrives", () => {
    const el = root();
    render(el, accountBriefFixture.data, []);
    render(el, { items: [], candidate_count: 0 }, []);
    expect(el.querySelectorAll(".row")).toHaveLength(0);
  });
});
