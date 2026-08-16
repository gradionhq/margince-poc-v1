// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { components } from "../api/schema";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { Badge } from "./atoms";
import "./pipelineladder.css";

// PipelineLadder: one message's path through the ingress pipeline, top to
// bottom, with what each step did and why.
//
// IT HOLDS NO LIST OF STAGES. The server sends the ladder in order and this
// walks it. That is the whole reason the component exists in the design system
// rather than inside the settings screen: the pipeline grows, and a client that
// enumerated its steps would have to ship a release before a new one could be
// seen — which is the same silence the surface was built to end.
//
// So an unrecognised stage renders from the server's own `label` and
// `reason_text` rather than vanishing or printing a raw key at a member. That
// is a deliberate departure from this app's usual closed-set discipline: a
// closed set is right for a five-value enum and wrong for a vocabulary designed
// to grow, where an omission is indistinguishable from "nothing happened".

type Rung = components["schemas"]["PipelineStageRung"];

// The tone each status carries. `unknown` and `not_reported` share the quiet
// tone on purpose: neither is a claim about the MESSAGE, they are statements
// about what this surface can tell the reader, and a verdict colour would read
// as one.
const STATUS_TONE: Record<
  Rung["status"],
  "success" | "warn" | "danger" | undefined
> = {
  done: "success",
  skipped: undefined,
  pending: "warn",
  failed: "danger",
  not_applicable: undefined,
  unknown: undefined,
  not_reported: undefined,
};

export function PipelineLadder({
  stages,
  payloadsEnabled,
}: Readonly<{
  stages: readonly Rung[];
  // The deployment's payload posture. False means NO rung carries a sender or
  // a subject because the operator did not turn payload capture on — which is
  // a different statement from a rung that simply has none, and the ladder says
  // which once rather than repeating it per rung.
  payloadsEnabled: boolean;
}>) {
  const t = useT();
  return (
    <ol className="pipeline-ladder">
      {stages.map((rung) => (
        <PipelineRung key={rung.stage} rung={rung} />
      ))}
      {!payloadsEnabled && (
        <li className="pipeline-ladder__posture">
          {t("pipeline.payloadsOff")}
        </li>
      )}
    </ol>
  );
}

function PipelineRung({ rung }: Readonly<{ rung: Rung }>) {
  const t = useT();
  return (
    <li className="pipeline-ladder__rung" data-status={rung.status}>
      <span className="pipeline-ladder__mark" aria-hidden="true" />
      <div className="pipeline-ladder__body">
        <p className="pipeline-ladder__head">
          <span className="pipeline-ladder__stage">{stageName(rung, t)}</span>
          <Badge tone={STATUS_TONE[rung.status]}>
            {t(`pipeline.status.${rung.status}`)}
          </Badge>
          <SubjectNote rung={rung} />
        </p>
        <RungReason rung={rung} />
        <RungPayload rung={rung} />
      </div>
    </li>
  );
}

// WHAT this rung's answer is about.
//
// Only shown when it is NOT the message. The verdict is asked once per sender
// and the company check once per domain, so a rung saying "judged a real
// contact" without saying whose reads as a claim about this one message — which
// is a different and wrong fact.
function SubjectNote({ rung }: Readonly<{ rung: Rung }>) {
  const t = useT();
  if (rung.subject_kind === "message") {
    return null;
  }
  return (
    <span className="pipeline-ladder__subject">
      {t(`pipeline.subject.${rung.subject_kind}`)}
    </span>
  );
}

function RungReason({ rung }: Readonly<{ rung: Rung }>) {
  const t = useT();
  const text = reasonText(rung, t);
  if (!text) {
    return null;
  }
  return <p className="pipeline-ladder__reason">{text}</p>;
}

// The sender and subject this step saw, under the payload posture only.
function RungPayload({ rung }: Readonly<{ rung: Rung }>) {
  if (!rung.counterparty && !rung.subject) {
    return null;
  }
  return (
    <p className="pipeline-ladder__payload">
      {rung.counterparty && (
        <span className="pipeline-ladder__from">{rung.counterparty}</span>
      )}
      {rung.subject && (
        <span className="pipeline-ladder__subjectline">{rung.subject}</span>
      )}
    </p>
  );
}

// STAGE_KEYS and REASON_KEYS bind the stages THIS build knows to its catalog.
//
// A map rather than a template key, because `useT` takes a narrow MessageKey and
// a computed string is not one — which is the type system holding the line the
// catalog otherwise cannot: the catalog falls back to the KEY when an entry is
// missing, so a typo ships `captureActivity.reason.transactional_infra` to a
// member and nobody notices until somebody sees a row. That happened here once.
//
// A stage or reason absent from these maps is NOT an error. It is a step a newer
// server knows and this build does not, and it renders from the server's own
// sentence. That is the seam that lets the pipeline grow without a frontend
// release, and it is why these are `Partial` rather than exhaustive Records.
const STAGE_KEYS: Partial<Record<string, MessageKey>> = {
  connector_filter: "pipeline.stage.connector_filter",
  ingress_gate: "pipeline.stage.ingress_gate",
  erasure_check: "pipeline.stage.erasure_check",
  internal_drop: "pipeline.stage.internal_drop",
  activity_write: "pipeline.stage.activity_write",
  tier_ladder: "pipeline.stage.tier_ladder",
  person_create: "pipeline.stage.person_create",
  verdict: "pipeline.stage.verdict",
  company_triage: "pipeline.stage.company_triage",
  attention_label: "pipeline.stage.attention_label",
  material_events: "pipeline.stage.material_events",
  claim_extraction: "pipeline.stage.claim_extraction",
};

const REASON_KEYS: Partial<Record<string, MessageKey>> = {
  "internal_drop.internal_only": "pipeline.reason.internal_only",
  "activity_write.invisible_incumbent": "pipeline.reason.invisible_incumbent",
  "tier_ladder.transactional_infra": "pipeline.reason.transactional_infra",
  "tier_ladder.transactional_prefix": "pipeline.reason.transactional_prefix",
  "tier_ladder.deferral_capped": "pipeline.reason.deferral_capped",
  "tier_ladder.noise_prior": "pipeline.reason.noise_prior",
  "tier_ladder.decided_prior": "pipeline.reason.decided_prior",
  "tier_ladder.no_counterparty": "pipeline.reason.no_counterparty",
  "tier_ladder.no_granting_human": "pipeline.reason.no_granting_human",
  "tier_ladder.derivation_failed": "pipeline.reason.derivation_failed",
  "person_create.not_linked_yet": "pipeline.reason.not_linked_yet",
  "person_create.no_contact_intended": "pipeline.reason.no_contact_intended",
  "verdict.awaiting_verdict": "pipeline.reason.awaiting_verdict",
  "verdict.verdict_reached": "pipeline.reason.verdict_reached",
  "verdict.no_open_question": "pipeline.reason.no_open_question",
  "internal_drop.record_not_available": "pipeline.reason.record_not_available",
  "activity_write.record_not_available": "pipeline.reason.record_not_available",
  "tier_ladder.record_not_available": "pipeline.reason.record_not_available",
  "person_create.record_not_available": "pipeline.reason.record_not_available",
  "verdict.record_not_available": "pipeline.reason.record_not_available",
  "attention_label.record_not_available":
    "pipeline.reason.record_not_available",
  "attention_label.transport_not_read": "pipeline.reason.transport_not_read",
  "attention_label.sender_undecided": "pipeline.reason.sender_undecided",
  "attention_label.archived": "pipeline.reason.archived",
  "attention_label.not_connector_captured":
    "pipeline.reason.not_connector_captured",
  "attention_label.awaiting_batch": "pipeline.reason.awaiting_batch",
  "attention_label.labelled": "pipeline.reason.labelled",
  "connector_filter.not_comparable_between_connectors":
    "pipeline.reason.not_comparable",
  "ingress_gate.connector_side_defect": "pipeline.reason.connector_side_defect",
  "erasure_check.would_restore_erased": "pipeline.reason.would_restore_erased",
  "claim_extraction.no_writer_yet": "pipeline.reason.no_writer_yet",
  "company_triage.not_reported_yet": "pipeline.reason.not_reported_yet",
  "material_events.not_reported_yet": "pipeline.reason.not_reported_yet",
};

// stageName prefers this build's own catalog and falls back to the server's.
function stageName(rung: Rung, t: ReturnType<typeof useT>): string {
  const key = STAGE_KEYS[rung.stage];
  return key ? t(key) : (rung.label ?? rung.stage);
}

function reasonText(rung: Rung, t: ReturnType<typeof useT>): string {
  if (!rung.reason) {
    return "";
  }
  const key = REASON_KEYS[`${rung.stage}.${rung.reason}`];
  return key ? t(key) : (rung.reason_text ?? "");
}
