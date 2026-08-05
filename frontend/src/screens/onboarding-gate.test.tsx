// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

/** @vitest-environment jsdom */
import "@testing-library/jest-dom/vitest";

import {
  cleanup,
  render as rtlRender,
  screen,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { components } from "../api/schema";
import { LocaleProvider } from "../i18n";
import { OnboardingGate, ReadTheatre } from "./onboarding-gate";

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

describe("ReadTheatre page ticker", () => {
  it("shows only the most recently crawled page, next to the honest total", () => {
    render(
      <ReadTheatre
        read={siteRead()}
        host="gradion.com"
        locale="en"
        configuredModel={MODEL}
      />,
    );

    // The fixture's last-arrived FETCH is /about — the skip and the failure
    // after it in the array are older news, not later arrivals, so the
    // ticker never shows either one alongside it.
    expect(screen.getByText("/about")).toBeInTheDocument();
    expect(screen.queryByText("/legal")).toBeNull();
    expect(screen.queryByText("/careers")).toBeNull();
    expect(screen.getByText("2 pages read")).toBeInTheDocument();
  });

  it("swaps to the newest page as it arrives, without ever showing two", () => {
    const { rerender } = render(
      <ReadTheatre
        read={siteRead()}
        host="gradion.com"
        locale="en"
        configuredModel={MODEL}
      />,
    );
    expect(screen.getByText("/about")).toBeInTheDocument();

    rerender(
      <LocaleProvider initial="en">
        <ReadTheatre
          read={siteRead({
            pages: [
              ...siteRead().pages,
              {
                url: "https://gradion.com/team",
                status: "fetched",
                kind: "about",
              },
            ],
            pages_read: 3,
          })}
          host="gradion.com"
          locale="en"
          configuredModel={MODEL}
        />
      </LocaleProvider>,
    );

    expect(screen.getByText("/team")).toBeInTheDocument();
    expect(screen.queryByText("/about")).toBeNull();
    expect(screen.getByText("3 pages read")).toBeInTheDocument();
  });

  it("does not let an earlier skip outlive a fetch that arrives after it", () => {
    // The wire always lists every fetched page before any skipped one, so a
    // skip sits last in the array on every poll it survives — even once a
    // brand new fetch has landed. Trusting array position here would pin the
    // ticker on the old skip forever; the fix is to track which URL is
    // actually new since the last render.
    const { rerender } = render(
      <ReadTheatre
        read={siteRead({
          pages: [
            {
              url: "https://gradion.com/legal",
              status: "fetched",
              kind: "home",
            },
            {
              url: "https://gradion.com/careers",
              status: "skipped",
              kind: "other",
              reason: "not company context",
            },
          ],
          pages_read: 1,
        })}
        host="gradion.com"
        locale="en"
        configuredModel={MODEL}
      />,
    );
    // Both pages arrive together on mount, nothing yet counting as "seen" —
    // the fetch, being real news, wins the tie over the skip beside it.
    expect(screen.getByText("/legal")).toBeInTheDocument();
    expect(screen.queryByText("/careers")).toBeNull();

    rerender(
      <LocaleProvider initial="en">
        <ReadTheatre
          read={siteRead({
            pages: [
              {
                url: "https://gradion.com/legal",
                status: "fetched",
                kind: "home",
              },
              {
                url: "https://gradion.com/team",
                status: "fetched",
                kind: "about",
              },
              {
                url: "https://gradion.com/careers",
                status: "skipped",
                kind: "other",
                reason: "not company context",
              },
            ],
            pages_read: 2,
          })}
          host="gradion.com"
          locale="en"
          configuredModel={MODEL}
        />
      </LocaleProvider>,
    );

    // /team is the page that just arrived; the skip is old news even though
    // it is still the array's last entry.
    expect(screen.getByText("/team")).toBeInTheDocument();
    expect(screen.queryByText("/careers")).toBeNull();
  });

  it("prefers a fetch that arrives in the very same poll as a new skip", () => {
    // A fetch and a skip can both be genuinely new in one poll; the wire's
    // fetched-first ordering must not make the skip look like the later
    // arrival just because it sits last in the array.
    const { rerender } = render(
      <ReadTheatre
        read={siteRead({
          pages: [
            {
              url: "https://gradion.com/legal",
              status: "fetched",
              kind: "home",
            },
          ],
          pages_read: 1,
        })}
        host="gradion.com"
        locale="en"
        configuredModel={MODEL}
      />,
    );
    expect(screen.getByText("/legal")).toBeInTheDocument();

    rerender(
      <LocaleProvider initial="en">
        <ReadTheatre
          read={siteRead({
            pages: [
              {
                url: "https://gradion.com/legal",
                status: "fetched",
                kind: "home",
              },
              {
                url: "https://gradion.com/team",
                status: "fetched",
                kind: "about",
              },
              {
                url: "https://gradion.com/careers",
                status: "skipped",
                kind: "other",
                reason: "not company context",
              },
            ],
            pages_read: 2,
          })}
          host="gradion.com"
          locale="en"
          configuredModel={MODEL}
        />
      </LocaleProvider>,
    );

    expect(screen.getByText("/team")).toBeInTheDocument();
    expect(screen.queryByText("/careers")).toBeNull();
  });

  it("shows the page once — the status word beside the path, never the url again", () => {
    render(
      <ReadTheatre
        read={siteRead({
          pages: [
            {
              url: "https://gradion.com/legal",
              status: "failed",
              kind: "impressum",
              reason: null,
            },
          ],
        })}
        host="gradion.com"
        locale="en"
        configuredModel={MODEL}
      />,
    );

    // The path is the one place the url appears; the status beside it names
    // what happened without repeating it.
    expect(screen.getByText("/legal")).toBeInTheDocument();
    expect(
      screen.getByText("could not be read: no reason recorded"),
    ).toBeInTheDocument();
    expect(screen.queryByText(/https:\/\/gradion\.com\/legal/)).toBeNull();
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

  it("marks the figure as incomplete when a call came back with no rate", () => {
    // A call the provider billed with no effective rate is missing from the
    // total, not folded into it as a silent zero — the reader must not read
    // the figure as the full cost.
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
            unpriced_calls: 1,
            models: [],
          },
        })}
        host="gradion.com"
        locale="en"
        configuredModel={MODEL}
      />,
    );

    expect(screen.getByText(/unpriced usage exists/)).toBeInTheDocument();
  });

  it("says nothing about unpriced usage when every call was priced", () => {
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

    expect(screen.queryByText(/unpriced usage exists/)).toBeNull();
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

  it("keeps the spend and the model list as two rows, never one squeezed grid", () => {
    const models =
      "gemini/gemini-3.1-flash-lite · cloud, efficient + " +
      "qwen/qwen3-32b · local, fast + " +
      "anthropic/claude-opus-4 · premium reasoning";
    render(
      <ReadTheatre
        read={siteRead({
          ai_runtime: {
            currency: "USD",
            call_attempts: 40,
            tokens_in: 100_000,
            tokens_out: 4_203,
            latency_ms: 8_200,
            estimated_cost_microusd: 48_800,
            unpriced_calls: 0,
            models: [],
          },
        })}
        host="gradion.com"
        locale="en"
        configuredModel={models}
      />,
    );

    // The label and the spend/calls line share one row — the point, at a
    // glance.
    const head = document.querySelector(".ob-scan-cost-head");
    expect(head).not.toBeNull();
    expect(
      within(head as HTMLElement).getByText("Transparency"),
    ).toBeInTheDocument();
    expect(
      within(head as HTMLElement).getByText(/40 calls/),
    ).toBeInTheDocument();

    // The model list is a full-width sibling below it, not squeezed inside
    // that row — and nothing about giving it its own row drops any of it:
    // three long ids with their role suffixes all survive verbatim.
    const model = document.querySelector(".ob-scan-cost-model");
    expect(model).not.toBeNull();
    expect(head?.contains(model)).toBe(false);
    expect(model?.textContent).toBe(models);
  });
});
