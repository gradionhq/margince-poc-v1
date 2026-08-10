// @vitest-environment jsdom

import { describe, expect, it } from "vitest";
import { pipelineReviewFixture } from "./fixture";
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

describe("the pipeline review renders what it was given", () => {
  it("renders one row per deal, in the worst-first order the seam answered", () => {
    const el = root();
    render(el, pipelineReviewFixture.data, []);
    expect(texts(el, ".name")).toEqual([
      "Acme ERP licence",
      "Globex platform expansion",
      "Initech pilot",
    ]);
    expect(texts(el, ".rank")).toEqual(["#1", "#2", "#3"]);
  });

  it("shows every evidence line, with the field it was read off", () => {
    const el = root();
    render(el, pipelineReviewFixture.data, []);
    // The second deal is both quiet and late, so it carries two.
    expect(texts(el, ".source")).toEqual([
      "deal.last_activity_at",
      "deal.last_activity_at",
      "deal.expected_close_date",
      "deal.created_at",
    ]);
    expect(el.textContent).toContain("expected close 2026-05-31 is past due");
  });

  it("scales a money amount by the currency's own minor units", () => {
    // 24_000_000 minor EUR is 240,000 — a view dividing by a hard-coded 100
    // would be right here and wrong for a zero-decimal currency, so the rule
    // is asserted against one of each.
    const el = root();
    render(
      el,
      {
        deals: [
          {
            name: "Euro deal",
            amount_minor: 24_000_000,
            currency: "EUR",
            evidence: [{ source: "deal.created_at", snippet: "quiet" }],
          },
          {
            name: "Yen deal",
            amount_minor: 1234,
            currency: "JPY",
            evidence: [{ source: "deal.created_at", snippet: "quiet" }],
          },
        ],
      },
      [],
    );
    const [euro, yen] = texts(el, ".score");
    expect(euro).toContain("240,000");
    // JPY has no minor digits: 1234 stored units means 1,234, not 12.34.
    expect(yen).toContain("1,234");
    expect(yen).not.toContain("12.34");
  });

  it("shows an unpriced deal as absent rather than as a currency zero", () => {
    const el = root();
    render(el, pipelineReviewFixture.data, []);
    const amounts = texts(el, ".score");
    expect(amounts[2]).toBe("—");
    expect(amounts[2]).not.toContain("0");
  });

  it("renders a currency Intl does not know as absent rather than throwing", () => {
    const el = root();
    expect(() =>
      render(
        el,
        {
          deals: [
            {
              name: "Odd deal",
              amount_minor: 100,
              currency: "not-a-currency",
              evidence: [{ source: "deal.created_at", snippet: "quiet" }],
            },
          ],
        },
        [],
      ),
    ).not.toThrow();
    expect(el.querySelector(".score")?.textContent).toBe("—");
  });

  it("says a deal arrived with no evidence rather than showing a rank with no reason", () => {
    // The tool never answers one — a deal whose risk cannot be evidenced is
    // absent. So this payload did not come from it, and saying so beats
    // presenting the ranking as an oracle.
    const el = root();
    render(el, { deals: [{ name: "Unexplained", evidence: [] }] }, []);
    expect(el.querySelector(".state")?.textContent).toMatch(/no evidence/i);
  });

  it("counts what is SHOWN rather than claiming to be the whole risk set", () => {
    const el = root();
    render(el, pipelineReviewFixture.data, []);
    expect(el.querySelector(".meta")?.textContent).toMatch(/3 deal\(s\) shown/);
  });

  it("renders the empty state when nothing can be evidenced", () => {
    const el = root();
    render(el, { deals: [] }, []);
    expect(el.querySelector(".empty")).not.toBeNull();
    expect(el.querySelectorAll(".row")).toHaveLength(0);
  });

  it("falls to the empty state on a payload of the wrong shape rather than throwing", () => {
    const el = root();
    expect(() => render(el, 42, [])).not.toThrow();
    expect(el.querySelector(".empty")).not.toBeNull();
  });

  // "No deal's risk can be evidenced" is a definite answer about the pipeline.
  // Another tool's result must not produce it.
  it("refuses a payload with no deals member rather than reporting a clean pipeline", () => {
    const el = root();
    render(el, { commitments: [] }, []);
    const empty = el.querySelector(".empty")?.textContent ?? "";
    expect(empty).toMatch(/no readable pipeline review/i);
    expect(empty).not.toMatch(/cannot be evidenced/i);
  });

  // The tool answers its own rank. Re-deriving it from the array index would
  // renumber the moment this view drops a row, putting a rank on screen the
  // tool never gave.
  it("shows the rank the tool answered, not this array's index", () => {
    const el = root();
    render(
      el,
      {
        deals: [
          {
            rank: 4,
            name: "Fourth worst",
            evidence: [{ source: "deal.created_at", snippet: "quiet" }],
          },
        ],
      },
      [],
    );
    expect(el.querySelector(".rank")?.textContent).toBe("#4");
  });

  it("says so when the host sent no structured result at all", () => {
    const el = root();
    render(el, null, []);
    expect(el.querySelector(".empty")?.textContent).toMatch(
      /no structured result/i,
    );
  });
});
