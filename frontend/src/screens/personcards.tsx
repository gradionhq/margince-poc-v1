import { ExternalLink, FileText, Mail } from "lucide-react";
import type { ReactNode } from "react";
import type { components } from "../api/schema";
import { navigate } from "../app/router";
import { Avatar, Badge } from "../design-system/atoms";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { money } from "./personstrip";

// The overview's four cards (concept §5.6–5.9). Each one is a read of what the
// 360 already assembled — none of them fetches, so a card can never show a
// record the page beside it is withholding.

type Person360 = components["schemas"]["Person360"];
type PersonBrief = components["schemas"]["PersonBrief"];
type ConversationClaim = components["schemas"]["ConversationClaim"];

// --- Relationship brief (§5.6) ---------------------------------------------

export function PersonBriefCard({
  brief,
  loading,
}: Readonly<{ brief: PersonBrief | undefined; loading: boolean }>) {
  const t = useT();
  return (
    <section className="pe-card" data-testid="person-brief">
      <h3 className="pe-card-title">{t("person.brief.title")}</h3>
      {loading && <p className="pe-prose">{t("person.brief.reading")}</p>}
      {!loading && (!brief || brief.sentences.length === 0) && (
        // Honest rather than blank: a brief with nothing to say has nothing to
        // say, and inventing prose to fill the card is the one thing the
        // grounding rule forbids.
        <p className="pe-prose">{t("person.brief.empty")}</p>
      )}
      {brief && brief.sentences.length > 0 && (
        <>
          <p className="pe-prose">
            {brief.sentences.map((sentence) => sentence.text).join(" ")}
          </p>
          <div className="pe-chiprow">
            {brief.sentences.flatMap((sentence) =>
              sentence.evidence.map((cited) => (
                <SourceChip
                  key={`${cited.entity_type}-${cited.entity_id}`}
                  kind={cited.entity_type}
                />
              )),
            )}
          </div>
        </>
      )}
    </section>
  );
}

function SourceChip({ kind }: Readonly<{ kind: string }>) {
  const t = useT();
  const label =
    kind === "activity"
      ? t("person.brief.sourceEmail")
      : kind === "deal"
        ? t("person.brief.sourceDeal")
        : kind;
  return (
    <span className="pe-memory-channel">
      {kind === "activity" ? (
        <Mail size={13} aria-hidden="true" />
      ) : (
        <FileText size={13} aria-hidden="true" />
      )}
      {label}
    </span>
  );
}

// --- What matters (§5.7) ---------------------------------------------------

// The three what-matters kinds, in the order a reader asks them. The
// communication-preference row the concept once proposed is deliberately
// absent: observed-style inference was dropped from the product (ADR-0097 D1).
const MATTERS: ReadonlyArray<{ kind: string; labelKey: MessageKey }> = [
  { kind: "priority", labelKey: "person.matters.priorities" },
  { kind: "objection", labelKey: "person.matters.objections" },
  { kind: "success_criterion", labelKey: "person.matters.successCriteria" },
];

export function PersonMattersCard({
  view,
  firstName,
}: Readonly<{ view: Person360; firstName: string }>) {
  const t = useT();
  const claims = view.claims ?? [];
  return (
    <section className="pe-card" data-testid="person-matters">
      <h3 className="pe-card-title">
        {t("person.matters.title", { name: firstName })}
      </h3>
      {MATTERS.map((row) => {
        const match = claims.find(
          (claim) => claim.kind === row.kind && claim.status !== "dismissed",
        );
        return (
          <div className="pe-row" key={row.kind}>
            <span className="pe-row-label">{t(row.labelKey)}</span>
            <span className="pe-row-value">
              {match ? match.body : <Absent />}
            </span>
            {match && <FileText size={15} aria-hidden="true" />}
          </div>
        );
      })}
    </section>
  );
}

// Absence has meaning (concept §4.7): a row nobody has said anything about
// says so, rather than disappearing and leaving the card looking complete.
function Absent(): ReactNode {
  const t = useT();
  return (
    <span className="pe-rail-value-muted">{t("person.matters.absent")}</span>
  );
}

// --- Open deal and buying role (§5.8) --------------------------------------

export function PersonCommercialCard({ view }: Readonly<{ view: Person360 }>) {
  const t = useT();
  const commercial = view.commercial;
  if (!commercial) {
    // The section was withheld. "You may not see deals" and "there is no deal"
    // are different facts, and only the first belongs here.
    return (
      <section className="pe-card" data-testid="person-commercial">
        <h3 className="pe-card-title">{t("person.commercial.title")}</h3>
        <p className="pe-prose">{t("person.commercial.withheld")}</p>
      </section>
    );
  }
  const deal = commercial.deal;
  return (
    <section className="pe-card" data-testid="person-commercial">
      <h3 className="pe-card-title">{t("person.commercial.title")}</h3>
      {!deal && <p className="pe-prose">{t("person.commercial.noDeal")}</p>}
      {deal && (
        <>
          <div className="pe-deal-head">
            <span className="pe-deal-title">{deal.title}</span>
            {commercial.role && (
              <Badge tone="success">{readableRole(commercial.role)}</Badge>
            )}
          </div>
          <div className="pe-deal-figures">
            {[
              deal.amount_minor != null && deal.currency
                ? money(deal.amount_minor, deal.currency)
                : null,
              deal.stage,
              deal.close_date
                ? t("person.commercial.closes", {
                    date: shortDate(deal.close_date),
                  })
                : null,
            ]
              .filter(Boolean)
              .join(" · ")}
          </div>

          {commercial.committee.length > 0 && (
            <>
              <div className="pe-committee-label">
                {t("person.commercial.committee")}
              </div>
              {commercial.committee.map((member) => (
                <div className="pe-committee-row" key={member.person_id}>
                  <span className="pe-committee-person">
                    <Avatar name={member.full_name} src={member.photo_url} />
                    <span>{member.full_name}</span>
                  </span>
                  <span className="pe-committee-role">
                    {readableRole(member.role)}
                  </span>
                </div>
              ))}
            </>
          )}

          <button
            type="button"
            className="pe-rail-more"
            onClick={() => navigate({ screen: "deals", id: deal.deal_id })}
          >
            {t("person.commercial.openDeal")}{" "}
            <ExternalLink size={13} aria-hidden="true" />
          </button>
        </>
      )}
    </section>
  );
}

// The stored role key rendered as words. An unrecognized key is shown as it
// was stored — inventing a label for a role nobody defined would be a claim.
function readableRole(role: string): string {
  const words = role.replace(/_/g, " ");
  return words.charAt(0).toUpperCase() + words.slice(1);
}

function shortDate(date: string): string {
  return new Date(date).toLocaleDateString(undefined, {
    day: "numeric",
    month: "short",
  });
}

// --- Commitments and open loops (§5.9) -------------------------------------

// ours / theirs / questions, in that order: what WE owe leads, because it is
// the only one entirely within the reader's control.
// A loop whose prefix key is null is prefixed with the contact's own name
// instead, which no catalog can carry.
const LOOPS: ReadonlyArray<{ kind: string; prefixKey: MessageKey | null }> = [
  { kind: "commitment_ours", prefixKey: "person.loops.ours" },
  { kind: "commitment_theirs", prefixKey: null },
  { kind: "open_question", prefixKey: "person.loops.question" },
];

export function PersonCommitmentsCard({
  view,
  firstName,
}: Readonly<{ view: Person360; firstName: string }>) {
  const t = useT();
  const claims = view.claims ?? [];
  const rows = LOOPS.flatMap((loop) =>
    claims
      .filter(
        (claim) => claim.kind === loop.kind && claim.status !== "dismissed",
      )
      .map((claim) => ({ claim, loop })),
  );
  return (
    <section className="pe-card" data-testid="person-commitments">
      <h3 className="pe-card-title">{t("person.loops.title")}</h3>
      {rows.length === 0 && (
        // An empty commitments card on a record whose mail contains no
        // promises is CORRECT behaviour, not a gap (ADR-0097 consequences).
        <p className="pe-prose">{t("person.loops.empty")}</p>
      )}
      {rows.map(({ claim, loop }) => (
        <div className="pe-loop" key={claim.id}>
          <input type="checkbox" checked={claim.status === "done"} readOnly />
          <span className="pe-loop-body">
            {loopPrefix(loop, firstName, t)}
            {claim.body}
          </span>
          <LoopStatus claim={claim} />
        </div>
      ))}
    </section>
  );
}

function loopPrefix(
  loop: { kind: string; prefixKey: MessageKey | null },
  firstName: string,
  t: ReturnType<typeof useT>,
): string {
  if (loop.kind === "commitment_theirs") {
    return `${firstName}: `;
  }
  return loop.prefixKey ? `${t(loop.prefixKey)}: ` : "";
}

function LoopStatus({ claim }: Readonly<{ claim: ConversationClaim }>) {
  const t = useT();
  if (claim.due_at) {
    const days = Math.floor(
      (Date.now() - new Date(claim.due_at).getTime()) / 86_400_000,
    );
    if (days > 0) {
      return (
        <span className="pe-loop-due pe-loop-overdue">
          {t("person.loops.overdue", { count: days })}
        </span>
      );
    }
    return (
      <span className="pe-loop-due">
        {t("person.loops.due", { when: dueWord(days, t) })}
      </span>
    );
  }
  if (claim.kind === "commitment_theirs") {
    return <Badge tone="accent">{t("person.loops.waiting")}</Badge>;
  }
  return <Badge>{t("person.loops.open")}</Badge>;
}

function dueWord(days: number, t: ReturnType<typeof useT>): string {
  if (days === 0) {
    return t("person.loops.dueToday");
  }
  if (days === -1) {
    return t("person.loops.dueTomorrow");
  }
  return t("person.loops.dueInDays", { count: Math.abs(days) });
}
