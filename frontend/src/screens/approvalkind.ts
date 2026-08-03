import type { MessageKey } from "../i18n/en";

// What a staged proposal is, in words a reader recognises.
//
// `approval.kind` is a wire enum — `site_lead`, `fx_rate_proposal` — and it was
// rendered verbatim wherever a proposal was listed. A reader deciding whether
// to accept twenty-five of something needs to know what that something is, and
// snake_case in the German UI is not a translation of anything.
//
// The authoritative set is `decisionGrants` in the approvals module — a kind
// absent from THAT map cannot be staged at all — so this one is pinned against
// it in approvalkind.test.ts. A kind added upstream fails that test rather than
// reaching a reader unlabelled, which is how two of them (a deal follow-up and
// a close-date correction) reached a German list in English.
//
// A kind that still slips through falls back to its own words rather than its
// identifier: it must degrade to "site lead", never to a token that only makes
// sense to whoever wrote the server.

export const KIND_LABEL: Readonly<Record<string, MessageKey>> = {
  advance_deal: "approval.kind.advance_deal",
  progress_deal: "approval.kind.advance_deal",
  promote_lead: "approval.kind.promote_lead",
  archive_record: "approval.kind.archive_record",
  merge_records: "approval.kind.merge_records",
  share_record: "approval.kind.share_record",
  update_record: "approval.kind.update_record",
  create_record: "approval.kind.create_record",
  send_email: "approval.kind.send_email",
  book_meeting: "approval.kind.book_meeting",
  send_offer: "approval.kind.send_offer",
  coldstart: "approval.kind.coldstart",
  enrich: "approval.kind.enrich",
  deepread: "approval.kind.deepread",
  linkedin_match: "approval.kind.linkedin_match",
  site_lead: "approval.kind.site_lead",
  close_date_correction: "approval.kind.close_date_correction",
  deal_follow_up: "approval.kind.deal_follow_up",
  capture_counterparty: "approval.kind.capture_counterparty",
  org_name_promotion: "approval.kind.org_name_promotion",
  lifecycle_change: "approval.kind.lifecycle_change",
  fx_rate_proposal: "approval.kind.fx_rate_proposal",
  ai_model_rate_proposal: "approval.kind.ai_model_rate_proposal",
};

// What a reader may CHANGE before accepting, per kind.
//
// The inline editor's default is every string field of the proposed_change,
// rendered as a text box. That default is right for a rename — the value IS
// prose — and wrong for a proposal built out of identifiers and enums. Editing
// `organization_id` re-aims the proposal at another record, and the server
// refuses that (assertSameEntityRefs); editing `proposed_lifecycle` by typing
// produces an invalid stage, and the server refuses that too. Both refusals
// are correct and neither is a thing to show a reader who was only trying to
// answer the question in front of them.
//
// So a kind may declare which fields it offers and what each one accepts. A
// kind that declares nothing keeps the default, which is why adding this
// changed no existing surface.
export type EditableField =
  | { readonly field: string; readonly as: "text" }
  | {
      readonly field: string;
      readonly as: "choice";
      readonly options: readonly string[];
      /**
       * What each option is CALLED. Without it the editor offers the wire
       * enum, so a German inbox asks a reader to choose "former_customer".
       * Optional so a choice field whose values are already words needs
       * nothing.
       */
      readonly optionLabels?: Readonly<Record<string, MessageKey>>;
    };

const ORG_LIFECYCLE_STAGES = [
  "unknown",
  "target",
  "prospect",
  "opportunity",
  "customer",
  "former_customer",
  "disqualified",
] as const;

// The same catalog keys the account page's stage badge reads, so the inbox and
// the record cannot call one stage two things. Keyed off the list above:
// a stage added there with no entry here fails the type.
const ORG_LIFECYCLE_LABELS: Readonly<
  Record<(typeof ORG_LIFECYCLE_STAGES)[number], MessageKey>
> = {
  unknown: "org.lifecycle.unknown",
  target: "org.lifecycle.target",
  prospect: "org.lifecycle.prospect",
  opportunity: "org.lifecycle.opportunity",
  customer: "org.lifecycle.customer",
  former_customer: "org.lifecycle.former_customer",
  disqualified: "org.lifecycle.disqualified",
};

export const EDITABLE_FIELDS: Readonly<
  Record<string, readonly EditableField[]>
> = {
  // The stage is the whole question. Everything else in the payload — which
  // account, which signal, the stage it is in now — is what the question is
  // ABOUT, and a reader who disagrees with any of that says no rather than
  // editing it into a different question.
  lifecycle_change: [
    {
      field: "proposed_lifecycle",
      as: "choice",
      options: ORG_LIFECYCLE_STAGES,
      optionLabels: ORG_LIFECYCLE_LABELS,
    },
  ],
};

/** humanize turns an unmapped wire enum into readable words. */
export function humanizeKind(kind: string): string {
  return kind.replaceAll("_", " ");
}

export function approvalKindLabel(
  kind: string,
  t: (key: MessageKey, params?: Record<string, string | number>) => string,
): string {
  const key = KIND_LABEL[kind];
  return key ? t(key) : humanizeKind(kind);
}
