/** @vitest-environment jsdom */
import { cleanup, render as rtlRender, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it } from "vitest";
import { LocaleProvider } from "../i18n";
import {
  type BoardColumn,
  DealCard,
  MorningBriefItem,
  PipelineBoard,
  RecordView,
} from "./composed";

// B-EP09.3b acceptance: the composed surfaces consume the 3a primitives and
// the staged / real / human-typed three-way distinction carries through.

afterEach(cleanup);

const render = (ui: ReactNode) =>
  rtlRender(<LocaleProvider initial="en">{ui}</LocaleProvider>);

describe("MorningBriefItem", () => {
  const item = {
    id: "b1",
    rank: 1,
    title: "Brandt Automotive went quiet",
    confidence: "med" as const,
    evidence: { snippet: "…last reply 14 days ago…", source: "email 20 Jun" },
    proposal: {
      description: "Draft a follow-up to Anna Weber",
      value: "Follow-up draft",
      agent: "runner",
      confidence: "med" as const,
      evidence: {
        snippet: "…shall we sync next week?…",
        source: "email 20 Jun",
      },
    },
  };

  it("renders the staged action visibly not-real with the nothing-sent marker", () => {
    render(<MorningBriefItem item={item} />);
    expect(screen.getByText("Nothing sent yet")).toBeTruthy();
    expect(
      screen.getByRole("region", { name: "staged proposal" }),
    ).toBeTruthy();
    expect(screen.getByText("agent: runner")).toBeTruthy();
  });

  it("carries the triad through composition: edit lands human-typed with evidence kept", async () => {
    render(<MorningBriefItem item={item} />);
    await userEvent.click(screen.getByRole("button", { name: "Edit" }));
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(screen.getByText("typed by you")).toBeTruthy();
    expect(screen.getByText(/shall we sync next week/)).toBeTruthy();
  });
});

describe("DealCard + PipelineBoard", () => {
  const deal = {
    id: "d1",
    name: "Fleet retrofit",
    org: "Brandt Automotive",
    valueMinor: 4_800_000,
    currency: "EUR",
    ageMs: 62 * 86_400_000,
    stalled: true,
  };

  it("renders value/age and the stalled aging flag (AC-pipeline-5)", () => {
    render(<DealCard deal={deal} />);
    expect(screen.getByText("€48,000.00")).toBeTruthy();
    expect(screen.getByText("stalled")).toBeTruthy();
    expect(screen.getByRole("button").className).toContain("stalled");
  });

  it("a staged deal renders visibly distinct from a real one", () => {
    const { container } = render(
      <>
        <DealCard deal={{ ...deal, id: "real", stalled: false }} />
        <DealCard
          deal={{ ...deal, id: "staged", stalled: false, staged: true }}
        />
      </>,
    );
    const [real, staged] = Array.from(container.querySelectorAll(".deal-card"));
    expect(real.className).not.toContain("staged");
    expect(staged.className).toContain("staged");
  });

  it("board columns render probability, count, raw and weighted sub-lines", () => {
    const column: BoardColumn = {
      stage: "proposal",
      label: "Proposal",
      probabilityPct: 40,
      rawMinor: 6_050_000,
      weightedMinor: 2_420_000,
      currency: "EUR",
      deals: [deal],
    };
    render(<PipelineBoard columns={[column]} />);
    expect(screen.getByText("40%")).toBeTruthy();
    expect(screen.getByText("1 deals")).toBeTruthy();
    expect(screen.getByText("€60,500.00")).toBeTruthy();
    expect(screen.getByText("weighted €24,200.00")).toBeTruthy();
  });
});

describe("RecordView + timeline", () => {
  it("renders the header and provenance-tagged timeline in the workspace zone", () => {
    render(
      <RecordView
        name="Anna Weber"
        subtitle="Head of Procurement · Brandt Automotive"
        zone="Europe/Berlin"
        timeline={[
          {
            id: "t1",
            kind: "email",
            title: "Re: fleet retrofit offer",
            atIso: "2026-06-12T09:00:00Z",
            provenance: { kind: "agent", agent: "capture" },
          },
          {
            id: "t2",
            kind: "note",
            title: "Call notes",
            atIso: "2026-06-14T10:00:00Z",
            provenance: { kind: "human" },
          },
        ]}
      />,
    );
    expect(
      screen.getByRole("heading", { level: 1, name: "Anna Weber" }),
    ).toBeTruthy();
    expect(screen.getByText("12/06/2026")).toBeTruthy();
    expect(screen.getByText("agent: capture")).toBeTruthy();
    expect(screen.getByText("typed by you")).toBeTruthy();
  });

  it("renders a timeline entry's action slot when present", () => {
    render(
      <RecordView
        name="Acme"
        zone="UTC"
        timeline={[
          {
            id: "a1",
            kind: "email",
            title: "Re: Q3",
            atIso: "2026-07-01T00:00:00Z",
            provenance: { kind: "human" },
            actions: (
              <button type="button" key="reply">
                Reply
              </button>
            ),
          },
        ]}
      />,
    );
    expect(screen.getByRole("button", { name: "Reply" })).toBeTruthy();
  });
});
