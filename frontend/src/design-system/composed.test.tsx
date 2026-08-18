/** @vitest-environment jsdom */
import { cleanup, render as rtlRender, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it } from "vitest";
import { LocaleProvider } from "../i18n";
import {
  type BoardColumn,
  type BoardMoneyColumn,
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
    expect(screen.getByText("Automated by runner")).toBeTruthy();
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
    const column: BoardMoneyColumn = {
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

  // A caller with a capped card fetch (the Kanban board) supplies
  // the stage's TRUE count from a server aggregate — it must render that,
  // not deals.length, whenever the two disagree.
  it("renders the column's own count over deals.length when the two disagree", () => {
    const column: BoardMoneyColumn = {
      stage: "proposal",
      label: "Proposal",
      probabilityPct: 40,
      rawMinor: 6_050_000,
      weightedMinor: 2_420_000,
      currency: "EUR",
      deals: [deal],
      count: 137,
    };
    render(<PipelineBoard columns={[column]} />);
    expect(screen.getByText("137 deals")).toBeTruthy();
    expect(screen.queryByText("1 deals")).toBeNull();
  });

  it("lets a non-deal board provide its own record noun", () => {
    const column: BoardColumn<{ id: string; name: string }> = {
      stage: "new",
      label: "New",
      deals: [{ id: "lead-1", name: "Ada" }],
    };
    render(
      <PipelineBoard
        variant="plain"
        columns={[column]}
        countLabel={(count) => `${count} leads`}
        renderCard={(card) => <span>{card.name}</span>}
      />,
    );
    expect(screen.getByText("1 leads")).toBeTruthy();
    expect(screen.queryByText("1 deals")).toBeNull();
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
            provenance: { kind: "human", self: true },
          },
        ]}
      />,
    );
    expect(
      screen.getByRole("heading", { level: 1, name: "Anna Weber" }),
    ).toBeTruthy();
    expect(screen.getByText("12/06/2026")).toBeTruthy();
    expect(screen.getByText("Automated by capture")).toBeTruthy();
    expect(screen.getByText("typed by you")).toBeTruthy();
  });

  it("keeps the whole message in the document, clamped but never cut", () => {
    // The clamp is CSS. Truncating the string instead would put the rest of
    // the message out of reach of find-in-page, selection and a screen reader,
    // and no toggle can give back text that was never rendered.
    const body = `Moin Christian, ${"eine sehr lange Zeile ".repeat(20)}Ende.`;
    render(
      <RecordView
        name="ScaleCommerce"
        zone="Europe/Berlin"
        timeline={[
          {
            id: "t1",
            kind: "email",
            title: "Update zu Margince",
            atIso: "2026-07-17T09:00:00Z",
            provenance: { kind: "agent", agent: "capture" },
            body,
          },
        ]}
      />,
    );
    expect(screen.getByText(body.trim())).toBeTruthy();
  });

  it("says nothing where a message was lawfully erased", () => {
    // Retention and Art. 17 both null the body. A row whose message is gone
    // must render as a row with no message, not as an empty quote.
    render(
      <RecordView
        name="ScaleCommerce"
        zone="Europe/Berlin"
        timeline={[
          {
            id: "t1",
            kind: "email",
            title: "Update zu Margince",
            atIso: "2026-07-17T09:00:00Z",
            provenance: { kind: "agent", agent: "capture" },
            body: "   ",
          },
        ]}
      />,
    );
    expect(document.querySelector(".tl-text")).toBeNull();
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
            provenance: { kind: "human", self: true },
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

describe("TimelineText on a mail row", () => {
  const SIGNED =
    "From: anna@kunde.de\nTo: lars@gradion.com\n\nKönnen wir Dienstag über das Angebot sprechen?\n\nMit freundlichen Grüßen\nAnna Berger\nKunde GmbH";

  const mailRow = (body: string, kind: "email" | "note" = "email") => [
    {
      id: "a1",
      kind,
      title: "Re: Angebot",
      atIso: "2026-07-01T00:00:00Z",
      provenance: { kind: "human" as const, self: true },
      body,
    },
  ];

  it("shows the message and hides the signature until asked", async () => {
    const user = userEvent.setup();
    render(<RecordView name="Acme" zone="UTC" timeline={mailRow(SIGNED)} />);

    expect(screen.getByText(/Können wir Dienstag/)).toBeTruthy();
    expect(screen.queryByText(/Mit freundlichen Grüßen/)).toBeNull();

    await user.click(
      screen.getByRole("button", { name: "Show signature and quoted text" }),
    );
    expect(screen.getByText(/Mit freundlichen Grüßen/)).toBeTruthy();
    expect(screen.getByText(/Kunde GmbH/)).toBeTruthy();
  });

  it("folds the signature away again", async () => {
    const user = userEvent.setup();
    render(<RecordView name="Acme" zone="UTC" timeline={mailRow(SIGNED)} />);

    await user.click(
      screen.getByRole("button", { name: "Show signature and quoted text" }),
    );
    await user.click(
      screen.getByRole("button", { name: "Hide signature and quoted text" }),
    );
    expect(screen.queryByText(/Mit freundlichen Grüßen/)).toBeNull();
  });

  it("keeps the correspondents' addresses above the message", () => {
    // The preamble says who wrote to whom, which is part of reading a mail on
    // a record. It is the row TITLE that must not lead with it — see the
    // timelineTitle rule in people.tsx — not the message body.
    render(<RecordView name="Acme" zone="UTC" timeline={mailRow(SIGNED)} />);
    const body = document.querySelector(".tl-text-clamp")?.textContent ?? "";
    expect(body).toContain("anna@kunde.de");
    expect(body).toContain("Können wir Dienstag");
  });

  it("leaves a note whose text reads like a sign-off intact", () => {
    render(
      <RecordView
        name="Acme"
        zone="UTC"
        timeline={mailRow("Viele Grüße an das Team ausgerichtet.", "note")}
      />,
    );
    expect(screen.getByText(/Viele Grüße an das Team/)).toBeTruthy();
    expect(
      screen.queryByRole("button", {
        name: "Show signature and quoted text",
      }),
    ).toBeNull();
  });

  it("offers no reveal when a mail carries no signature or quote", () => {
    render(
      <RecordView
        name="Acme"
        zone="UTC"
        timeline={mailRow("Kurz: ja, Dienstag passt uns gut.")}
      />,
    );
    expect(
      screen.queryByRole("button", {
        name: "Show signature and quoted text",
      }),
    ).toBeNull();
  });

  it("renders a link with its address as the label", () => {
    render(
      <RecordView
        name="Acme"
        zone="UTC"
        timeline={mailRow(
          "Die Unterlagen liegen unter https://kunde.de/angebot bereit.",
        )}
      />,
    );
    const link = screen.getByRole("link", {
      name: "https://kunde.de/angebot",
    });
    expect(link.getAttribute("href")).toBe("https://kunde.de/angebot");
    expect(link.getAttribute("rel")).toContain("noopener");
  });

  it("folds the signature again when the row is given a different mail", async () => {
    // The row is keyed by activity id, so the component stays mounted when the
    // entry it renders is replaced. A reveal must not carry over to a mail the
    // reader never opened.
    const user = userEvent.setup();
    const { rerender } = rtlRender(
      <LocaleProvider initial="en">
        <RecordView name="Acme" zone="UTC" timeline={mailRow(SIGNED)} />
      </LocaleProvider>,
    );
    await user.click(
      screen.getByRole("button", { name: "Show signature and quoted text" }),
    );
    expect(screen.getByText(/Kunde GmbH/)).toBeTruthy();

    rerender(
      <LocaleProvider initial="en">
        <RecordView
          name="Acme"
          zone="UTC"
          timeline={mailRow("Neue Nachricht.\n\n-- \nMax Muster\nAndere GmbH")}
        />
      </LocaleProvider>,
    );
    expect(screen.queryByText(/Andere GmbH/)).toBeNull();
  });

  it("keeps a link inside the folded signature reachable once revealed", async () => {
    const user = userEvent.setup();
    render(
      <RecordView
        name="Acme"
        zone="UTC"
        timeline={mailRow(
          "Kurz: ja.\n\n-- \nAnna Berger\nhttps://kunde.de/impressum",
        )}
      />,
    );
    expect(screen.queryByRole("link")).toBeNull();
    await user.click(
      screen.getByRole("button", { name: "Show signature and quoted text" }),
    );
    expect(
      screen.getByRole("link", { name: "https://kunde.de/impressum" }),
    ).toBeTruthy();
  });
});
