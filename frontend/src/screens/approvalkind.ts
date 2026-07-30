import type { MessageKey } from "../i18n/en";

// What a staged proposal is, in words a reader recognises.
//
// `approval.kind` is a wire enum — `site_lead`, `fx_rate_proposal` — and it was
// rendered verbatim wherever a proposal was listed. A reader deciding whether
// to accept twenty-five of something needs to know what that something is, and
// snake_case in the German UI is not a translation of anything.
//
// A kind with no entry falls back to its own words rather than its identifier:
// a proposal kind added upstream must degrade to "site lead", never to a token
// that only makes sense to whoever wrote the server.

const KIND_LABEL: Readonly<Record<string, MessageKey>> = {
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
  site_lead: "approval.kind.site_lead",
  capture_counterparty: "approval.kind.capture_counterparty",
  org_name_promotion: "approval.kind.org_name_promotion",
  fx_rate_proposal: "approval.kind.fx_rate_proposal",
  ai_model_rate_proposal: "approval.kind.ai_model_rate_proposal",
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
