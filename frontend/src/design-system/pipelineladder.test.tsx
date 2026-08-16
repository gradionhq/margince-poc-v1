// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

/** @vitest-environment jsdom */
import "@testing-library/jest-dom/vitest";

import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import type { components } from "../api/schema";
import { LocaleProvider } from "../i18n";
import { PipelineLadder } from "./pipelineladder";

// What the ladder may say, and what it must never say.
//
// The component holds no list of stages on purpose, so the tests that matter
// are about the seam that makes that safe: a stage this build has never heard
// of has to render from the server's words rather than vanish or leak a key.

type Rung = components["schemas"]["PipelineStageRung"];

function rung(
  over: Partial<Rung> & Pick<Rung, "stage" | "order" | "status">,
): Rung {
  return { subject_kind: "message", ...over };
}

function show(stages: Rung[], payloadsEnabled = false) {
  return render(
    <LocaleProvider initial="en">
      <PipelineLadder stages={stages} payloadsEnabled={payloadsEnabled} />
    </LocaleProvider>,
  );
}

afterEach(cleanup);

describe("the pipeline ladder", () => {
  it("renders a stage it has never heard of from the server's own words", () => {
    // The growth seam. A stage added by a newer server must appear, or the
    // surface reintroduces exactly the silence it exists to remove.
    show([
      rung({
        stage: "sentiment_scoring",
        order: 130,
        status: "failed",
        label: "Sentiment scoring",
        reason: "model_unavailable",
        reason_text: "no model was configured for this pass",
      }),
    ]);
    expect(screen.getByText("Sentiment scoring")).toBeInTheDocument();
    expect(
      screen.getByText(/no model was configured for this pass/i),
    ).toBeInTheDocument();
  });

  it("never renders a raw catalog key at a member", () => {
    // The catalog falls back to the KEY when an entry is missing, which is how
    // `captureActivity.reason.transactional_infra` once shipped to a member.
    show([
      rung({ stage: "a_stage_nobody_registered", order: 900, status: "done" }),
      rung({
        stage: "internal_drop",
        order: 40,
        status: "skipped",
        reason: "internal_only",
      }),
    ]);
    expect(screen.queryByText(/pipeline\./)).not.toBeInTheDocument();
  });

  it("falls back to the stage id when the server sent no label either", () => {
    // Worse than a name, better than an empty row: it still tells a reader that
    // a step exists and that nobody has named it.
    show([rung({ stage: "unnamed_step", order: 950, status: "done" })]);
    expect(screen.getByText("unnamed_step")).toBeInTheDocument();
  });

  it("says whose answer a rung is when it is not this message's", () => {
    // The verdict is asked once per SENDER. Rendering it without saying whose
    // reads as a claim about this one message, which is a different fact.
    show([
      rung({
        stage: "verdict",
        order: 80,
        status: "done",
        subject_kind: "sender",
        reason: "verdict_reached",
      }),
    ]);
    expect(
      screen.getByText(/about the sender, not this message alone/i),
    ).toBeInTheDocument();
  });

  it("does not qualify a rung that IS about this message", () => {
    show([
      rung({ stage: "internal_drop", order: 40, status: "not_applicable" }),
    ]);
    expect(screen.queryByText(/about the sender/i)).not.toBeInTheDocument();
  });

  it("states the payload posture once, not per rung", () => {
    show([
      rung({ stage: "internal_drop", order: 40, status: "done" }),
      rung({ stage: "tier_ladder", order: 60, status: "done" }),
    ]);
    expect(
      screen.getAllByText(/did not turn payload capture on/i),
    ).toHaveLength(1);
  });

  it("says nothing about the posture when the operator turned it on", () => {
    show([rung({ stage: "tier_ladder", order: 60, status: "done" })], true);
    expect(
      screen.queryByText(/did not turn payload capture on/i),
    ).not.toBeInTheDocument();
  });

  it("shows a rung's payload only when the row carried one", () => {
    show(
      [
        rung({
          stage: "tier_ladder",
          order: 60,
          status: "done",
          counterparty: "dana@client.io",
          subject: "Q3 pricing",
        }),
        rung({
          stage: "verdict",
          order: 80,
          status: "done",
          subject_kind: "sender",
        }),
      ],
      true,
    );
    expect(screen.getByText("dana@client.io")).toBeInTheDocument();
    expect(screen.getByText("Q3 pricing")).toBeInTheDocument();
  });

  it("distinguishes cannot-tell from did-not-apply", () => {
    // The two claims the whole surface turns on. Past the sweep only one of
    // them is honest, and they must not read as the same rung.
    show([
      rung({
        stage: "internal_drop",
        order: 40,
        status: "unknown",
        reason: "record_not_available",
      }),
      rung({ stage: "tier_ladder", order: 60, status: "not_applicable" }),
    ]);
    expect(screen.getByText(/cannot tell/i)).toBeInTheDocument();
    expect(screen.getByText(/did not apply/i)).toBeInTheDocument();
  });
});
