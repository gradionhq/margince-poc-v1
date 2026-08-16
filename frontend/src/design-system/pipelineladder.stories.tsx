// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import type { components } from "../api/schema";
import { PipelineLadder } from "./pipelineladder";

// The states worth seeing side by side are the ones that differ in what the
// ladder may HONESTLY claim — a step that declined, one that cannot be shown,
// one whose record has been swept, and one this build has never heard of.

type Rung = components["schemas"]["PipelineStageRung"];

function rung(
  over: Partial<Rung> & Pick<Rung, "stage" | "order" | "status">,
): Rung {
  return { subject_kind: "message", ...over };
}

// The motivating case, end to end: a chat message that landed and then met a
// step which reads email only.
const CHAT_MESSAGE: Rung[] = [
  rung({
    stage: "connector_filter",
    order: 10,
    status: "not_reported",
    reason: "not_comparable_between_connectors",
  }),
  rung({
    stage: "ingress_gate",
    order: 20,
    status: "not_reported",
    reason: "connector_side_defect",
  }),
  rung({
    stage: "erasure_check",
    order: 30,
    status: "not_reported",
    reason: "would_restore_erased",
  }),
  rung({ stage: "internal_drop", order: 40, status: "not_applicable" }),
  rung({ stage: "activity_write", order: 50, status: "done" }),
  rung({
    stage: "tier_ladder",
    order: 60,
    status: "done",
    subject_kind: "sender",
  }),
  rung({
    stage: "person_create",
    order: 70,
    status: "done",
    subject_kind: "sender",
  }),
  rung({
    stage: "verdict",
    order: 80,
    status: "not_applicable",
    subject_kind: "sender",
    reason: "no_open_question",
  }),
  rung({
    stage: "company_triage",
    order: 90,
    status: "not_reported",
    subject_kind: "domain",
    reason: "not_reported_yet",
  }),
  rung({
    stage: "attention_label",
    order: 100,
    status: "skipped",
    reason: "transport_not_read",
  }),
  rung({
    stage: "material_events",
    order: 110,
    status: "not_reported",
    subject_kind: "thread",
    reason: "not_reported_yet",
  }),
  rung({
    stage: "claim_extraction",
    order: 120,
    status: "not_reported",
    subject_kind: "sender",
    reason: "no_writer_yet",
  }),
];

const meta: Meta<typeof PipelineLadder> = {
  title: "Design System/PipelineLadder",
  component: PipelineLadder,
};
export default meta;
type Story = StoryObj<typeof PipelineLadder>;

// The case the whole surface was built for: captured, and then a step that
// declined to read it, saying why.
export const ChatMessageNotClassified: Story = {
  args: { stages: CHAT_MESSAGE, payloadsEnabled: false },
};

// A colleague reading a shared record. The stored rungs keep their place and
// say they are withheld — never omitted, which would state that the steps did
// not happen, and never conditional, which would turn their presence into a
// row-existence oracle.
export const WithheldFromANonOwner: Story = {
  args: {
    stages: [
      rung({ stage: "internal_drop", order: 40, status: "withheld" }),
      rung({ stage: "activity_write", order: 50, status: "done" }),
      rung({
        stage: "tier_ladder",
        order: 60,
        status: "withheld",
        subject_kind: "sender",
      }),
      rung({
        stage: "person_create",
        order: 70,
        status: "unknown",
        subject_kind: "sender",
      }),
      rung({
        stage: "attention_label",
        order: 100,
        status: "done",
        reason: "labelled",
      }),
    ],
    payloadsEnabled: false,
  },
};

// Past the 24-hour window. `unknown` rather than `not_applicable`: absence and
// never-happened are indistinguishable once the rows are swept, and only one of
// those is a claim the data supports.
export const PastTheRetentionWindow: Story = {
  args: {
    stages: [
      rung({ stage: "internal_drop", order: 40, status: "unknown" }),
      rung({ stage: "activity_write", order: 50, status: "done" }),
      rung({
        stage: "tier_ladder",
        order: 60,
        status: "unknown",
        subject_kind: "sender",
      }),
      rung({
        stage: "attention_label",
        order: 100,
        status: "pending",
        reason: "awaiting_batch",
      }),
    ],
    payloadsEnabled: false,
  },
};

// An operator diagnosing, with capture.trace_payloads on.
export const WithPayloadCapture: Story = {
  args: {
    stages: [
      rung({
        stage: "internal_drop",
        order: 40,
        status: "skipped",
        reason: "internal_only",
        counterparty: "colleague@acme.com",
        subject: "Meeting recap",
      }),
      rung({ stage: "activity_write", order: 50, status: "not_applicable" }),
    ],
    payloadsEnabled: true,
  },
};

// A step added by a NEWER server than this client. It renders from the
// server's own words rather than vanishing — the seam that lets the pipeline
// grow without a frontend release.
export const AStageThisBuildDoesNotKnow: Story = {
  args: {
    stages: [
      rung({ stage: "activity_write", order: 50, status: "done" }),
      rung({
        stage: "sentiment_scoring",
        order: 130,
        status: "failed",
        label: "Sentiment scoring",
        reason: "model_unavailable",
        reason_text: "no model was configured for this pass",
      }),
    ],
    payloadsEnabled: false,
  },
};

// Both themes, because every derived value is a color-mix of a canonical token
// and follows the dark accent lift: a surface can be right in light and wrong
// in dark. This story carries every tone the ladder can show at once.
export const EveryToneDark: Story = {
  args: {
    stages: [
      rung({ stage: "activity_write", order: 50, status: "done" }),
      rung({
        stage: "tier_ladder",
        order: 60,
        status: "failed",
        subject_kind: "sender",
        reason: "derivation_failed",
      }),
      rung({
        stage: "person_create",
        order: 70,
        status: "pending",
        subject_kind: "sender",
        reason: "not_linked_yet",
      }),
      rung({
        stage: "verdict",
        order: 80,
        status: "withheld",
        subject_kind: "sender",
      }),
      rung({
        stage: "attention_label",
        order: 100,
        status: "skipped",
        reason: "archived",
      }),
      rung({
        stage: "claim_extraction",
        order: 120,
        status: "not_reported",
        subject_kind: "sender",
        reason: "no_writer_yet",
      }),
    ],
    payloadsEnabled: false,
  },
  globals: { theme: "dark" },
};
