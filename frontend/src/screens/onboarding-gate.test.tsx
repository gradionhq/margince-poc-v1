// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

/** @vitest-environment jsdom */
import "@testing-library/jest-dom/vitest";

import {
  act,
  cleanup,
  render as rtlRender,
  screen,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { components } from "../api/schema";
import { LocaleProvider } from "../i18n";
import { FactSnippets, OnboardingGate, ReadTheatre } from "./onboarding-gate";

// The label useConfiguredModel() hands the gate in the real screen.
const MODEL = "gemini/gemini-3.5-flash · cloud, efficient";

// The notice arrives as a finished sentence; composing it is gate-notice.ts's
// job and is tested there.
const FAILED_MESSAGE =
  "I could not read that site. The host did not answer. Try another address, or enter the details yourself.";
const PAUSED_MESSAGE =
  "That read is paused for now. The AI budget is spent. It resumes on its own.";

// The gate and the read theatre: the surface is prop-driven, so every case here
// is a fixture in and a claim out — no fetch, no clock, no router. The claims
// that matter are that the gate refuses a non-address without calling out, that
// the theatre says only what the wire carries (an open page count, never a
// fraction), and that reduced motion lands on the END state rather than on an
// empty column.

type CompanySiteRead = components["schemas"]["CompanySiteRead"];
type CompanySiteReadFact = components["schemas"]["CompanySiteReadFact"];

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

const render = (ui: ReactNode) =>
  rtlRender(<LocaleProvider initial="en">{ui}</LocaleProvider>);

// jsdom implements no AnimationEvent, so a chip's own animation ending has to be
// dispatched as a plain bubbling event. The component listens natively, which is
// why this reaches it at all — a JSX onAnimationEnd would not fire here.
function endAnimation(chip: Element | null): void {
  expect(chip).not.toBeNull();
  if (chip !== null) {
    act(() => {
      chip.dispatchEvent(new Event("animationend", { bubbles: true }));
    });
  }
}

// The default jsdom stub answers "no preference"; a case that wants the reduced
// path installs this one.
function stubReducedMotion(reduced: boolean): void {
  vi.stubGlobal("matchMedia", (query: string) => ({
    matches: reduced && query.includes("prefers-reduced-motion"),
    media: query,
    onchange: null,
    addEventListener: () => undefined,
    removeEventListener: () => undefined,
    addListener: () => undefined,
    removeListener: () => undefined,
    dispatchEvent: () => false,
  }));
}

function fact(
  over: Partial<CompanySiteReadFact> & { value_key: string },
): CompanySiteReadFact {
  return {
    category: "offering",
    field: "service",
    value: "Managed Kubernetes for regulated industries",
    evidence_snippet: "We run managed Kubernetes…",
    evidence_url: "https://gradion.com/services/platform",
    confidence: 0.8,
    ...over,
  };
}

function siteRead(over: Partial<CompanySiteRead> = {}): CompanySiteRead {
  return {
    id: "018f3a1b-0000-7000-8000-0000000000b2",
    target_kind: "onboarding",
    organization_id: null,
    root_url: "https://gradion.com",
    status: "reading",
    status_code: null,
    status_detail: null,
    next_attempt_at: null,
    phase: "crawling",
    pages_read: 2,
    pages: [
      { url: "https://gradion.com", status: "fetched", kind: "home" },
      {
        url: "https://gradion.com/about",
        status: "fetched",
        kind: "about",
      },
      {
        url: "https://gradion.com/careers",
        status: "skipped",
        kind: "other",
        reason: "not company context",
      },
      {
        url: "https://gradion.com/legal",
        status: "failed",
        kind: "impressum",
        reason: null,
      },
    ],
    profile_fields: [],
    facts: [fact({ value_key: "service:platform" })],
    comparisons: [],
    people: [],
    warnings: [],
    draft_version: 1,
    proposal_hash: "h1",
    created_at: "2026-07-31T10:00:00Z",
    updated_at: "2026-07-31T10:00:04Z",
    ...over,
  };
}

describe("OnboardingGate", () => {
  it("normalizes an address with a scheme and a path down to its host", async () => {
    const onSubmit = vi.fn();
    render(
      <OnboardingGate
        running={false}
        onSubmit={onSubmit}
        onManual={vi.fn()}
        configuredModel={MODEL}
      />,
    );

    await userEvent.type(
      screen.getByLabelText("Your website address"),
      "https://x.co/path",
    );
    await userEvent.click(screen.getByRole("button", { name: "Read my site" }));

    expect(onSubmit).toHaveBeenCalledWith("x.co");
  });

  it("refuses something that is not an address, says so, and does not start a read", async () => {
    const onSubmit = vi.fn();
    render(
      <OnboardingGate
        running={false}
        onSubmit={onSubmit}
        onManual={vi.fn()}
        configuredModel={MODEL}
      />,
    );

    await userEvent.type(
      screen.getByLabelText("Your website address"),
      "notadomain",
    );
    await userEvent.click(screen.getByRole("button", { name: "Read my site" }));

    expect(screen.getByRole("alert")).toHaveTextContent(
      "That does not look like a web address. Try it as yourcompany.com.",
    );
    expect(onSubmit).not.toHaveBeenCalled();
    // The field is marked invalid and points at the message.
    expect(screen.getByLabelText("Your website address")).toHaveAttribute(
      "aria-invalid",
      "true",
    );
  });

  it("submits on Enter, so the field alone is a working form", async () => {
    const onSubmit = vi.fn();
    render(
      <OnboardingGate
        running={false}
        onSubmit={onSubmit}
        onManual={vi.fn()}
        configuredModel={MODEL}
      />,
    );

    await userEvent.type(
      screen.getByLabelText("Your website address"),
      "gradion.com{Enter}",
    );

    expect(onSubmit).toHaveBeenCalledWith("gradion.com");
  });

  it("greets by name when there is one and states the identity when there is not", () => {
    const { unmount } = render(
      <OnboardingGate
        name="Lars"
        running={false}
        onSubmit={vi.fn()}
        onManual={vi.fn()}
        configuredModel={MODEL}
      />,
    );
    expect(
      screen.getByRole("heading", { level: 1, name: /Hi Lars/ }),
    ).toBeInTheDocument();
    unmount();

    render(
      <OnboardingGate
        running={false}
        onSubmit={vi.fn()}
        onManual={vi.fn()}
        configuredModel={MODEL}
      />,
    );
    expect(
      screen.getByRole("heading", { level: 1, name: "I am the Margince AI." }),
    ).toBeInTheDocument();
  });

  it("offers the manual path as its own control", async () => {
    const onManual = vi.fn();
    render(
      <OnboardingGate
        running={false}
        onSubmit={vi.fn()}
        onManual={onManual}
        configuredModel={MODEL}
      />,
    );

    await userEvent.click(
      screen.getByRole("button", { name: "Enter the details yourself" }),
    );

    expect(onManual).toHaveBeenCalledTimes(1);
  });

  it("reports a failed read with the server's own detail and stays usable", () => {
    render(
      <OnboardingGate
        running={false}
        configuredModel={MODEL}
        notice={{ tone: "error", message: FAILED_MESSAGE }}
        onSubmit={vi.fn()}
        onManual={vi.fn()}
      />,
    );

    expect(screen.getByRole("alert")).toHaveTextContent(
      "I could not read that site. The host did not answer. Try another address, or enter the details yourself.",
    );
    expect(screen.getByRole("button", { name: "Read my site" })).toBeEnabled();
  });

  // A deferral is the server shelving the work, not the reader getting it
  // wrong, so it must not arrive as an alert telling them to fix something.
  it("reports a paused read as status rather than as a failure", () => {
    render(
      <OnboardingGate
        running={false}
        configuredModel={MODEL}
        notice={{ tone: "paused", message: PAUSED_MESSAGE }}
        onSubmit={vi.fn()}
        onManual={vi.fn()}
      />,
    );

    expect(screen.queryByRole("alert")).toBeNull();
    expect(screen.getByRole("status")).toHaveTextContent(
      "That read is paused for now. The AI budget is spent. It resumes on its own",
    );
  });

  it("names the model that is about to read, before the address is handed over", () => {
    render(
      <OnboardingGate
        running={false}
        configuredModel={MODEL}
        onSubmit={vi.fn()}
        onManual={vi.fn()}
      />,
    );

    expect(screen.getByText(MODEL)).toBeTruthy();
  });

  it("refuses a second start while one is in flight, in the attribute and in the handler", async () => {
    const onSubmit = vi.fn();
    render(
      <OnboardingGate
        running={true}
        onSubmit={onSubmit}
        onManual={vi.fn()}
        configuredModel={MODEL}
      />,
    );

    await userEvent.type(
      screen.getByLabelText("Your website address"),
      "gradion.com{Enter}",
    );

    expect(screen.getByRole("button", { name: "Read my site" })).toBeDisabled();
    expect(onSubmit).not.toHaveBeenCalled();
  });
});

describe("the gate while a start is in flight", () => {
  it("withholds the manual escape, so it cannot race the read beginning", () => {
    const { rerender } = render(
      <OnboardingGate
        running={false}
        configuredModel={MODEL}
        onSubmit={vi.fn()}
        onManual={vi.fn()}
      />,
    );
    expect(
      screen.getByRole("button", { name: /Enter the details yourself/ }),
    ).toBeInTheDocument();

    rerender(
      <LocaleProvider initial="en">
        <OnboardingGate
          running
          configuredModel={MODEL}
          onSubmit={vi.fn()}
          onManual={vi.fn()}
        />
      </LocaleProvider>,
    );
    expect(
      screen.queryByRole("button", { name: /Enter the details yourself/ }),
    ).toBeNull();
  });
});

describe("the gate-to-read handoff", () => {
  it("replaces the tail of one column instead of mounting a second screen", () => {
    const { rerender } = render(
      <OnboardingGate
        running={false}
        configuredModel={MODEL}
        onSubmit={vi.fn()}
        onManual={vi.fn()}
      />,
    );
    const core = document.querySelector(".core");
    const title = screen.getByRole("heading", { level: 1 });
    expect(core).not.toBeNull();
    expect(screen.getByLabelText(/Your website address/)).toBeInTheDocument();

    rerender(
      <LocaleProvider initial="en">
        <OnboardingGate
          running={false}
          configuredModel={MODEL}
          scan={{ read: siteRead(), host: "gradion.com", locale: "en" }}
          onSubmit={vi.fn()}
          onManual={vi.fn()}
        />
      </LocaleProvider>,
    );

    // The SAME nodes, not equivalent ones. Identity is the whole assertion: a
    // remounted Core rebuilds its WebGL context and restarts every loop it is
    // mid-way through, so the flow's most important moment would flash and
    // re-enter rather than carry on.
    expect(document.querySelector(".core")).toBe(core);
    expect(screen.getByRole("heading", { level: 1 })).toBe(title);
    // Only the tail changed: the question is gone, the read's regions are there.
    expect(screen.queryByLabelText(/Your website address/)).toBeNull();
    expect(
      screen.getByRole("list", { name: "Pages read so far" }),
    ).toBeInTheDocument();
    expect(title).toHaveTextContent("Reading gradion.com");
  });
});

describe("ReadTheatre phase line", () => {
  const cases: ReadonlyArray<[string, Partial<CompanySiteRead>, string]> = [
    ["queued", { status: "queued", phase: null }, "Queued, starting shortly"],
    ["deferred", { status: "deferred", phase: null }, "Paused for now"],
    ["crawling", { status: "reading", phase: "crawling" }, "Fetching pages"],
    [
      "extracting",
      { status: "reading", phase: "extracting" },
      "Working out what you sell",
    ],
  ];

  for (const [label, over, expected] of cases) {
    it(`names the ${label} phase from the wire fields`, () => {
      render(
        <ReadTheatre
          read={siteRead(over)}
          host="gradion.com"
          locale="en"
          configuredModel={MODEL}
        />,
      );
      expect(screen.getByText(expected)).toBeInTheDocument();
    });
  }

  it("says nothing about the phase when a settled read carries none", () => {
    render(
      <ReadTheatre
        read={siteRead({ status: "ready", phase: null })}
        host="gradion.com"
        locale="en"
        configuredModel={MODEL}
      />,
    );

    for (const [, , copy] of cases) {
      expect(screen.queryByText(copy)).toBeNull();
    }
    expect(
      screen.getByRole("heading", { level: 1, name: "Read gradion.com" }),
    ).toBeInTheDocument();
  });
});

describe("ReadTheatre page strip", () => {
  it("shows one named tile per page, with the reason and its honest fallback", () => {
    render(
      <ReadTheatre
        read={siteRead()}
        host="gradion.com"
        locale="en"
        configuredModel={MODEL}
      />,
    );

    const strip = screen.getByRole("list", { name: "Pages read so far" });
    expect(strip.querySelectorAll("li")).toHaveLength(4);

    expect(
      screen.getByRole("img", { name: "https://gradion.com — read" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("img", {
        name: "https://gradion.com/careers — skipped: not company context",
      }),
    ).toBeInTheDocument();
    // reason: null must not read as an empty reason.
    expect(
      screen.getByRole("img", {
        name: "https://gradion.com/legal — could not be read: no reason recorded",
      }),
    ).toBeInTheDocument();
  });

  it("keeps every count open — no fraction, no percentage, no denominator", () => {
    render(
      <ReadTheatre
        read={siteRead()}
        host="gradion.com"
        locale="en"
        configuredModel={MODEL}
      />,
    );

    expect(screen.getByText("2 pages read")).toBeInTheDocument();
    expect(screen.getByText("1 skipped")).toBeInTheDocument();
    expect(screen.getByText("1 facts so far")).toBeInTheDocument();
    expect(screen.getByText("still reading")).toBeInTheDocument();

    const surface = screen.getByRole("heading", { level: 1 }).parentElement;
    expect(surface).not.toBeNull();
    const text = surface?.textContent ?? "";
    expect(text).not.toMatch(/\d\s*\/\s*\d/);
    expect(text).not.toMatch(/%/);
  });

  it("counts the fetched tiles when the server sends no tally of its own", () => {
    render(
      <ReadTheatre
        read={siteRead({ pages_read: undefined })}
        host="gradion.com"
        locale="en"
        configuredModel={MODEL}
      />,
    );

    expect(screen.getByText("2 pages read")).toBeInTheDocument();
  });

  it("drops the still-reading marker once the read has settled", () => {
    render(
      <ReadTheatre
        read={siteRead({ status: "ready", phase: null })}
        host="gradion.com"
        locale="en"
        configuredModel={MODEL}
      />,
    );

    expect(screen.queryByText("still reading")).toBeNull();
  });
});

describe("ReadTheatre cost strip", () => {
  it("discloses calls, tokens and cost from the run summary", () => {
    render(
      <ReadTheatre
        read={siteRead({
          ai_runtime: {
            currency: "USD",
            call_attempts: 6,
            tokens_in: 12_400,
            tokens_out: 3_100,
            latency_ms: 8_200,
            estimated_cost_microusd: 4_200,
            unpriced_calls: 0,
            models: [],
          },
        })}
        host="gradion.com"
        locale="en"
        configuredModel={MODEL}
      />,
    );

    expect(screen.getByText("Transparency")).toBeInTheDocument();
    // Fractions of a cent stay visible rather than rounding to $0.00.
    expect(
      screen.getByText("6 calls · 15,500 tokens · $0.0042"),
    ).toBeInTheDocument();
  });

  it("says nothing has been billed yet rather than printing a zero", () => {
    render(
      <ReadTheatre
        read={siteRead({ ai_runtime: undefined })}
        host="gradion.com"
        locale="en"
        configuredModel={MODEL}
      />,
    );

    expect(screen.getByText("no model calls billed yet")).toBeInTheDocument();
    expect(screen.queryByText(/\$/)).toBeNull();
  });

  it("formats the numbers for the locale it is given", () => {
    render(
      <ReadTheatre
        read={siteRead({
          ai_runtime: {
            currency: "USD",
            call_attempts: 6,
            tokens_in: 12_400,
            tokens_out: 3_100,
            latency_ms: 8_200,
            estimated_cost_microusd: 4_200,
            unpriced_calls: 0,
            models: [],
          },
        })}
        host="gradion.com"
        locale="de"
        configuredModel={MODEL}
      />,
    );

    expect(screen.getByText(/15\.500 tokens/)).toBeInTheDocument();
  });
});

describe("FactSnippets", () => {
  const facts = [
    fact({ value_key: "a", value: "Managed Kubernetes" }),
    fact({ value_key: "b", value: "Frankfurt, Germany", category: "company" }),
    fact({
      value_key: "c",
      value: "A very long offering line that runs past the chip's limit",
      category: "market",
      evidence_url: "https://gradion.com/",
    }),
  ];

  it("surfaces the newest facts in fixed slots, tinted by category", () => {
    render(<FactSnippets facts={facts} />);

    const chips = document.querySelectorAll(".ob-snip");
    expect(chips).toHaveLength(3);
    // Slots come from a coprime step, so no two on screen share one.
    const slots = [...chips].map((chip) => chip.getAttribute("data-slot"));
    expect(new Set(slots).size).toBe(slots.length);
    expect(
      [...chips].map((chip) => chip.getAttribute("data-fact-category")),
    ).toEqual(["offering", "company", "market"]);
  });

  it("truncates a long value and shows the page it came from", () => {
    render(<FactSnippets facts={facts} />);

    expect(
      screen.getByText("A very long offering line that…"),
    ).toBeInTheDocument();
    expect(screen.getAllByText("/services/platform")).toHaveLength(2);
    // The root page has no path worth printing.
    expect(screen.queryByText("/")).toBeNull();
    expect(document.querySelectorAll(".ob-snip-src")).toHaveLength(2);
  });

  it("is decoration: the whole layer is hidden from assistive technology", () => {
    render(<FactSnippets facts={facts} />);

    expect(document.querySelector(".ob-snips")).toHaveAttribute(
      "aria-hidden",
      "true",
    );
  });

  it("renders nothing before the first fact arrives", () => {
    render(<FactSnippets facts={[]} />);

    expect(document.querySelector(".ob-snips")).toBeNull();
  });

  it("gives every single-value fact its own chip, empty value_key and all", () => {
    // A single-value fact carries no value_key by contract, so the key that
    // separates chips has to be the server's composite identity. Keyed on
    // value_key alone these three collide and only one chip mounts.
    render(
      <FactSnippets
        facts={[
          fact({ value_key: "", field: "phone", value: "+49 40 123456" }),
          fact({ value_key: "", field: "location", value: "Hamburg" }),
          fact({ value_key: "", field: "founded_year", value: "2011" }),
        ]}
      />,
    );

    expect(document.querySelectorAll(".ob-snip")).toHaveLength(3);
    for (const value of ["+49 40 123456", "Hamburg", "2011"]) {
      expect(screen.getByText(value)).toBeInTheDocument();
    }
  });

  it("keeps a chip until its own animation ends, not until the next poll", () => {
    const { rerender } = render(<FactSnippets facts={[facts[0]]} />);
    const first = document.querySelector(".ob-snip");
    expect(first).not.toBeNull();

    // Extraction arrives in per-page batches. The chip already on screen is
    // mid-fade and must survive the batch that lands beside it.
    rerender(
      <LocaleProvider initial="en">
        <FactSnippets facts={facts} />
      </LocaleProvider>,
    );
    expect(document.querySelectorAll(".ob-snip")).toHaveLength(3);
    expect(document.querySelector(".ob-snip")).toBe(first);

    // Its animation finishing is the only thing that retires it.
    endAnimation(first);
    expect(document.querySelectorAll(".ob-snip")).toHaveLength(2);
  });

  it("does not bring a faded chip back when the next poll repeats its fact", () => {
    const { rerender } = render(<FactSnippets facts={facts} />);
    for (const chip of [...document.querySelectorAll(".ob-snip")]) {
      endAnimation(chip);
    }
    expect(document.querySelector(".ob-snips")).toBeNull();

    // The wire returns the whole fact list on every poll; a chip that has had
    // its turn must not flash again on the next one.
    rerender(
      <LocaleProvider initial="en">
        <FactSnippets facts={[...facts]} />
      </LocaleProvider>,
    );
    expect(document.querySelector(".ob-snips")).toBeNull();
  });

  it("stands down under reduced motion while the read's own numbers stay", () => {
    stubReducedMotion(true);
    render(
      <ReadTheatre
        read={siteRead()}
        host="gradion.com"
        locale="en"
        configuredModel={MODEL}
      />,
    );

    expect(document.querySelector(".ob-snips")).toBeNull();
    // The END state, not nothing: the tiles and the counts are still there.
    expect(
      screen
        .getByRole("list", { name: "Pages read so far" })
        .querySelectorAll("li"),
    ).toHaveLength(4);
    expect(screen.getByText("2 pages read")).toBeInTheDocument();
    expect(screen.getByText("Fetching pages")).toBeInTheDocument();
  });
});
