/** @vitest-environment jsdom */
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  ConfidenceMeter,
  type Proposal,
  ProvenanceTag,
  StagedProposal,
} from "./trust";

// These tests are the B-EP09.3a acceptance: the universal Accept/Edit/Dismiss
// triad, "Edit flips a value to human-typed while retaining the original
// snippet" (§4.4), staged vs real as visibly distinct styles (§5c), and
// low confidence always shown (§4.2).

afterEach(cleanup);

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
    expect(screen.getByText("agent: capture")).toBeTruthy();
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
    expect(screen.getByText("agent: capture")).toBeTruthy();
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
    expect(screen.queryByText("agent: capture")).toBeNull();
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
    render(<ProvenanceTag provenance={{ kind: "human" }} />);
    expect(screen.getByText("agent: runner").className).toContain(
      "provenance-agent",
    );
    expect(screen.getByText("typed by you").className).toContain(
      "provenance-human",
    );
  });
});
