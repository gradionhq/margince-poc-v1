import { useId } from "react";
import type { components } from "../api/schema";
import { navigate } from "../app/router";
import { Button, EmptyState, Modal } from "../design-system/atoms";
import { useT } from "../i18n";
import { ApprovalRow, useApprovalTokenSink } from "./inbox";

// What is waiting on a decision FOR THIS ACCOUNT, where the account is being
// read. The count alone ("27 decisions waiting") told a reader that something
// was owed and gave them nowhere to pay it.
//
// The rows come out of the 360 payload the page has already read, so opening
// the panel costs no second request. Same-kind proposals are grouped, because
// a deep read of a company website stages one proposal per person it found —
// twenty-five rows that are one decision to the reader making it.

type Organization360 = components["schemas"]["Organization360"];
type Approval = components["schemas"]["Approval"];

/** The pending approvals the 360 carries, or none when the section was withheld. */
export function pendingApprovals(view?: Organization360): Approval[] {
  return view?.pending_approvals?.data ?? [];
}

/** Same-kind proposals, in the order their kinds first appear. */
export function groupByKind(approvals: readonly Approval[]): {
  kind: string;
  approvals: Approval[];
}[] {
  const groups: { kind: string; approvals: Approval[] }[] = [];
  const at = new Map<string, number>();
  for (const approval of approvals) {
    const index = at.get(approval.kind);
    if (index === undefined) {
      at.set(approval.kind, groups.length);
      groups.push({ kind: approval.kind, approvals: [approval] });
      continue;
    }
    groups[index].approvals.push(approval);
  }
  return groups;
}

/**
 * DecisionsChip is the way in. It is absent — not empty — when nothing is
 * waiting or the caller may not triage, so the page never claims a queue it
 * cannot show.
 */
export function DecisionsChip({
  view,
  onOpen,
}: Readonly<{ view?: Organization360; onOpen: () => void }>) {
  const t = useT();
  const count = pendingApprovals(view).length;
  if (count === 0) {
    return null;
  }
  return (
    <Button small variant="primary" onClick={onOpen}>
      {t("co.decisions.open", { count })}
    </Button>
  );
}

export function CompanyApprovalsPanel({
  orgId,
  view,
  onClose,
}: Readonly<{
  orgId: string;
  view?: Organization360;
  onClose: () => void;
}>) {
  const t = useT();
  const titleId = useId();
  const sink = useApprovalTokenSink();
  const approvals = pendingApprovals(view);
  const groups = groupByKind(approvals);
  // A decision changes what the page says is waiting, so the composite read
  // this panel was filled from has to be re-read alongside the approvals list.
  const extraInvalidateKeys = [["organization360", orgId]];
  const hasMore = view?.pending_approvals?.page?.has_more ?? false;
  return (
    <Modal open onClose={onClose} labelledBy={titleId} size="wide">
      <h2 id={titleId} className="t-h2 modal-title">
        {t("co.decisions.title")}
      </h2>
      {sink.decidedNote}
      {groups.length === 0 && (
        <EmptyState>{t("co.decisions.empty")}</EmptyState>
      )}
      {groups.map((group) => (
        <section key={group.kind} className="co-part">
          <h3 className="co-part-label">
            {t("co.decisions.group", {
              count: group.approvals.length,
              kind: group.kind,
            })}
          </h3>
          {group.approvals.map((approval) => (
            <ApprovalRow
              key={approval.id}
              approval={approval}
              onApproved={sink.onApproved}
              onAlreadyDecided={sink.onAlreadyDecided}
              extraInvalidateKeys={extraInvalidateKeys}
            />
          ))}
        </section>
      ))}
      {/* The 360 caps its own section, so the panel says plainly when it is
          not showing everything rather than letting a decided-through list
          read as an empty queue. */}
      {hasMore && (
        <p className="form-actions">
          <Button
            small
            onClick={() => {
              onClose();
              navigate({ screen: "inbox" });
            }}
          >
            {t("co.decisions.more")}
          </Button>
        </p>
      )}
      {sink.tokenModal}
    </Modal>
  );
}
