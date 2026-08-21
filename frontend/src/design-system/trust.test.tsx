/** @vitest-environment jsdom */
import { cleanup, render as rtlRender, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider } from "../i18n";
import {
  ConfidenceMeter,
  EvidenceChip,
  formatSourceLines,
  type Proposal,
  ProvenanceTag,
  StagedProposal,
  toEvidence,
} from "./trust";

// These tests are the B-EP09.3a acceptance: the universal Accept/Edit/Dismiss
// triad, "Edit flips a value to human-typed while retaining the original
// snippet" (§4.4), staged vs real as visibly distinct styles (§5c), and
// low confidence always shown (§4.2).

afterEach(cleanup);

// The components read copy from the i18n catalogs; assertions here use the
// English catalog so the strings under test are the spec's own wording.
const render = (ui: ReactNode) =>
  rtlRender(<LocaleProvider initial="en">{ui}</LocaleProvider>);

const proposal: Proposal = {
  description: "Set Brandt Automotive's deal value",
  value: "€48.000",
  agent: "capture",
  confidence: "med",
  evidence: { snippet: "…offer of 48k as discussed…", source: "email 12 Jun" },
};

describe("StagedProposal (B-EP09.3a)", () => {
  it("renders staged as visibly not-yet-real: staging style, agent provenance, confidence, evidence", () => {
    render(<StagedProposal proposal={proposal} />);
    const card = screen.getByRole("region", { name: "staged proposal" });
    expect(card.className).toContain("staging-card");
    expect(screen.getByText("Automated by capture")).toBeTruthy();
    expect(screen.getByText("medium")).toBeTruthy();
    expect(screen.getByText(/offer of 48k/)).toBeTruthy();
  });

  it("Accept persists the value with AGENT provenance kept", async () => {
    const onResolve = vi.fn();
    render(<StagedProposal proposal={proposal} onResolve={onResolve} />);
    await userEvent.click(screen.getByRole("button", { name: "Accept" }));

    expect(onResolve).toHaveBeenCalledWith({
      outcome: "accepted",
      value: "€48.000",
    });
    const card = screen.getByRole("region", { name: "resolved value" });
    expect(card.className).toContain("real-card");
    expect(card.className).not.toContain("staging-card");
    expect(screen.getByText("Automated by capture")).toBeTruthy();
  });

  it("Edit flips the value to human-typed while RETAINING the original snippet (§4.4)", async () => {
    const onResolve = vi.fn();
    render(<StagedProposal proposal={proposal} onResolve={onResolve} />);
    await userEvent.click(screen.getByRole("button", { name: "Edit" }));

    const input = screen.getByRole("textbox", { name: /Edit Set Brandt/ });
    await userEvent.clear(input);
    await userEvent.type(input, "€45.000");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    expect(onResolve).toHaveBeenCalledWith({
      outcome: "edited",
      value: "€45.000",
    });
    expect(screen.getByText("typed by you")).toBeTruthy();
    expect(screen.queryByText("Automated by capture")).toBeNull();
    // the original evidence snippet is still attached to the edited value
    expect(screen.getByText(/offer of 48k/)).toBeTruthy();
  });

  it("Dismiss resolves without leaving a value behind", async () => {
    const onResolve = vi.fn();
    render(<StagedProposal proposal={proposal} onResolve={onResolve} />);
    await userEvent.click(screen.getByRole("button", { name: "Dismiss" }));

    expect(onResolve).toHaveBeenCalledWith({ outcome: "dismissed" });
    expect(screen.queryByText(/€48.000/)).toBeNull();
    expect(screen.getByText("Suggestion dismissed.")).toBeTruthy();
  });
});

describe("ConfidenceMeter", () => {
  it("shows low as low — there is no way to hide it (§4.2)", () => {
    render(<ConfidenceMeter level="low" />);
    const meter = screen.getByText("low");
    expect(meter.className).toContain("confidence-low");
  });
});

describe("ProvenanceTag", () => {
  it("distinguishes agent-written from human-typed", () => {
    render(<ProvenanceTag provenance={{ kind: "agent", agent: "runner" }} />);
    render(<ProvenanceTag provenance={{ kind: "human", self: true }} />);
    expect(screen.getByText("Automated by runner").className).toContain(
      "provenance-agent",
    );
    expect(screen.getByText("typed by you").className).toContain(
      "provenance-human",
    );
  });

  // "typed by you" over a colleague's entry is a false statement about who to
  // ask, and it used to be what an UNATTRIBUTED row said too — the two cases a
  // reader most needs kept apart both read as their own handiwork.
  it("names another person rather than claiming the reader typed it", () => {
    render(
      <ProvenanceTag
        provenance={{ kind: "human", self: false, userId: "u-2" }}
        renderUser={(id) => <span>Christian ({id})</span>}
      />,
    );
    expect(screen.getByText(/Christian \(u-2\)/)).toBeTruthy();
    expect(screen.queryByText("typed by you")).toBeNull();
  });

  it("says a person entered it when it cannot say which person", () => {
    render(<ProvenanceTag provenance={{ kind: "human", self: false }} />);
    expect(screen.getByText("typed by a person")).toBeTruthy();
  });

  it("reads an unrecorded source as unknown, not as the reader", () => {
    render(<ProvenanceTag provenance={{ kind: "unknown" }} />);
    expect(screen.getByText("source not recorded").className).toContain(
      "provenance-unknown",
    );
  });

  it("names the connector a record was imported through", () => {
    render(
      <ProvenanceTag provenance={{ kind: "connector", connector: "gmail" }} />,
    );
    expect(screen.getByText("via gmail").className).toContain(
      "provenance-agent",
    );
  });

  // A background job and an AI agent are different answers to "who do I ask",
  // so they take different wording and different chrome. Drawn in the agent
  // tint, a scheduled sweep would tell a reader a model decided something.
  it("reads a job the installation ran as the system, not as an agent", () => {
    render(
      <ProvenanceTag provenance={{ kind: "system", job: "close-date" }} />,
    );
    const tag = screen.getByText("System task close-date");
    expect(tag.className).toContain("provenance-system");
    expect(tag.className).not.toContain("provenance-agent");
  });

  // The two unnamed cases: an agent behind a passport uuid, and a job that
  // stamped no id. Both say the KIND — which is what the wire recorded —
  // instead of printing an identifier a reader can do nothing with.
  it("says the kind and stops when the actor has no name to print", () => {
    render(<ProvenanceTag provenance={{ kind: "agent" }} />);
    render(<ProvenanceTag provenance={{ kind: "system" }} />);
    expect(screen.getByText("Automated by an agent").className).toContain(
      "provenance-agent",
    );
    expect(screen.getByText("System task").className).toContain(
      "provenance-system",
    );
  });
});

// WHERE in the source a claim came from. A quoted sentence with no address is
// a claim the reader has to take on trust; a line reference is what lets them
// go back to the exchange and check it.
describe("the lines an evidence chip was read from", () => {
  it("closes a run into one range and keeps a gap apart", () => {
    expect(formatSourceLines([12, 13, 14])).toBe("12–14");
    expect(formatSourceLines([3, 9])).toBe("3, 9");
    expect(formatSourceLines([7])).toBe("7");
  });

  it("orders and de-duplicates what the server sent, rather than trusting it", () => {
    expect(formatSourceLines([14, 12, 13, 12])).toBe("12–14");
  });

  it("shows the reference beside the snippet, in the reader's language", () => {
    render(
      <EvidenceChip
        evidence={{
          snippet: "I'll send the revised quote on Monday.",
          source: "transcript",
          lines: [12, 13, 14],
        }}
      />,
    );
    expect(screen.getByText("lines 12–14")).toBeTruthy();
  });

  it("says line, singular, for a claim read from one line", () => {
    render(
      <EvidenceChip
        evidence={{
          snippet: "Monday it is.",
          source: "transcript",
          lines: [7],
        }}
      />,
    );
    expect(screen.getByText("line 7")).toBeTruthy();
  });

  it("adds nothing to a source that has no lines to point at", () => {
    render(
      <EvidenceChip
        evidence={{ snippet: "…offer of 48k…", source: "email 12 Jun" }}
      />,
    );
    expect(screen.queryByText(/^lines? /)).toBeNull();
  });
});

// A chip exists to make a claim checkable by a reader. A record reference —
// the shape the contract documents an evidence source to be — is checkable by
// the system and by nobody else, so the reader gets the record KIND and the row
// id stays in the title.
describe("the source an evidence chip names", () => {
  const LEAD_REF = "lead:019fff1e-8439-75fe-adfe-78ab4b497f12";

  it("names the record kind, never the row id, when the source is a record reference", () => {
    const { container } = render(
      <EvidenceChip
        evidence={{ snippet: "Ruebenase Gert", source: LEAD_REF }}
      />,
    );
    const chip = container.querySelector(".evidence-chip");
    expect(chip?.textContent).toBe('"Ruebenase Gert" · lead');
    expect(chip?.getAttribute("title")).toBe(LEAD_REF);
  });

  it("keeps the row id out of the compact form too, where the source is all there is", () => {
    const { container } = render(
      <EvidenceChip
        evidence={{ snippet: "Ruebenase Gert", source: LEAD_REF }}
        collapsed
      />,
    );
    const toggle = container.querySelector(".evidence-chip-toggle");
    expect(toggle?.textContent).toBe("lead");
    expect(toggle?.getAttribute("title")).toBe(LEAD_REF);
  });

  it("shows a source somebody wrote exactly as written, tooltip and all", () => {
    // A colon does not make a source a record reference: these two are words,
    // and shortening either would hide what the claim rests on.
    for (const source of ["email 12 Jun", "deal_coverage_risk:margin_thin"]) {
      cleanup();
      const { container } = render(
        <EvidenceChip evidence={{ snippet: "…offer of 48k…", source }} />,
      );
      const chip = container.querySelector(".evidence-chip");
      expect(chip?.textContent).toBe(`"…offer of 48k…" · ${source}`);
      expect(chip?.hasAttribute("title")).toBe(false);
    }
  });
});

// toEvidence is the boundary the untyped contract value crosses, so it owes the
// narrowing to every caller rather than trusting one: a screen that has to
// assert a shape before handing it over has done the checking itself, unchecked.
describe("narrowing a contract value to evidence", () => {
  it("accepts an object carrying both fields as strings", () => {
    expect(
      toEvidence({ snippet: "…48k as discussed…", source: "email" }),
    ).toEqual({ snippet: "…48k as discussed…", source: "email" });
  });

  it("keeps only the trust vocabulary's two fields", () => {
    expect(
      toEvidence({ snippet: "s", source: "email", confidence: 0.9 }),
    ).toEqual({ snippet: "s", source: "email" });
  });

  it("reads anything else as no evidence rather than guessing one", () => {
    // A missing field, a field of the wrong type, and the values a free-form
    // contract field can carry that are no object with those two fields at all.
    for (const raw of [
      { snippet: "s" },
      { source: "email" },
      { snippet: 12, source: "email" },
      "email 12 Jun",
      42,
      null,
      undefined,
      ["snippet", "source"],
    ]) {
      expect(toEvidence(raw)).toBeNull();
    }
  });
});
