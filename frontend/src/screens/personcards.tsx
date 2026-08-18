import { ExternalLink, FileText } from "lucide-react";
import type { ReactNode } from "react";
import type { components } from "../api/schema";
import { navigate } from "../app/router";
import { Avatar, Badge, Button, Checkbox } from "../design-system/atoms";
import { Panel, PanelBody, PanelRow } from "../design-system/panel";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { interactionIcon, useInteractionLabel } from "./interactionchrome";
import { money } from "./personstrip";

// The overview's four cards (concept §5.6–5.9). Each one is a read of what the
// 360 already assembled — none of them fetches, so a card can never show a
// record the page beside it is withholding.

type Person360 = components["schemas"]["Person360"];
type PersonBrief = components["schemas"]["PersonBrief"];
type ConversationClaim = components["schemas"]["ConversationClaim"];
type Activity = components["schemas"]["Activity"];
type BriefEvidence = components["schemas"]["OrganizationBriefEvidence"];

// --- Relationship brief (§5.6) ---------------------------------------------

export function PersonBriefCard({
  brief,
  loading,
  view,
}: Readonly<{
  brief: PersonBrief | undefined;
  loading: boolean;
  view: Person360;
}>) {
  const t = useT();
  const firstName = view.person.full_name.split(" ")[0];
  // The timeline the page already read, by id. A citation is resolved from it
  // rather than fetched, on the same terms as every other card here: a chip
  // can never name a record the page beside it is withholding.
  const citedActivities = new Map(
    (view.activities?.data ?? []).map((row) => [row.id, row]),
  );
  return (
    <Panel title={t("person.brief.title")}>
      <PanelBody>
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
              {/* One chip per distinct source, not per citation: several
                  sentences routinely cite the same thread, and rendering the
                  chip once per mention would repeat it and collide on its key. */}
              {[
                ...new Map(
                  brief.sentences.flatMap((sentence) =>
                    sentence.evidence.map(
                      (cited) =>
                        [
                          `${cited.entity_type}-${cited.entity_id}`,
                          cited,
                        ] as const,
                    ),
                  ),
                ).entries(),
              ].map(([key, cited]) => (
                <SourceChip
                  key={key}
                  cited={cited}
                  activity={citedActivities.get(cited.entity_id)}
                />
              ))}
            </div>
          </>
        )}
      </PanelBody>
      <PanelBody className="pe-brief-state">
        {/* The band under the prose: the three detail panels below only render
            when they have something beyond their own empty sentence, so this
            is the one place a reader always finds all three states named,
            whether or not the panel that expands on them is present. */}
        <div className="pe-brief-block">
          <h3 className="pe-brief-label t-caption">
            {t("person.commercial.title")}
          </h3>
          <p className="pe-brief-line">{commercialLine(view, t)}</p>
        </div>
        <div className="pe-brief-block">
          <h3 className="pe-brief-label t-caption">
            {t("person.loops.title")}
          </h3>
          <p className="pe-brief-line">{commitmentsLine(view, t)}</p>
        </div>
        <div className="pe-brief-block">
          <h3 className="pe-brief-label t-caption">
            {t("person.matters.title", { name: firstName })}
          </h3>
          <p className="pe-brief-line">{mattersLine(view, t)}</p>
        </div>
      </PanelBody>
    </Panel>
  );
}

// The band's commercial block: the same "does this card have anything to
// show" test PersonCommercialCard makes, so the band and the panel below it
// never disagree about whether there is a deal to speak of.
export function hasCommercial(view: Person360): boolean {
  const commercial = view.commercial;
  if (!commercial) {
    return false;
  }
  return (
    commercial.deal != null ||
    commercial.role != null ||
    commercial.committee.length > 0
  );
}

function commercialLine(view: Person360, t: ReturnType<typeof useT>): string {
  const commercial = view.commercial;
  if (!commercial) {
    return t("person.commercial.withheld");
  }
  const deal = commercial.deal;
  if (deal) {
    const amount =
      deal.amount_minor != null && deal.currency
        ? money(deal.amount_minor, deal.currency)
        : null;
    return [deal.title, amount].filter(Boolean).join(" · ");
  }
  if (commercial.role) {
    return readableRole(commercial.role);
  }
  if (commercial.committee.length > 0) {
    return t("person.commercial.committee");
  }
  return t("person.commercial.noDeal");
}

// One cited source, named for what it is.
//
// A citation carries a RECORD TYPE and an id, never a transport — so an
// activity citation says nothing at all about how the conversation was
// carried, and calling every one of them a mail thread told the reader of a
// contact with no email address that they had been mailed. The activity the
// page ALREADY read is the resolver: it carries the kind, and for a message
// the provider the directory names. A citation whose activity is not on this
// page is named for the one thing it certainly is — a conversation — rather
// than for a transport nobody checked.
function SourceChip({
  cited,
  activity,
}: Readonly<{ cited: BriefEvidence; activity: Activity | undefined }>) {
  const t = useT();
  const interactionLabel = useInteractionLabel();
  if (cited.entity_type === "activity") {
    return (
      <span className="pe-memory-channel">
        {interactionIcon(activity?.kind)}
        {activity
          ? interactionLabel(activity.kind, activity.channel_provider)
          : t("person.brief.sourceActivity")}
      </span>
    );
  }
  return (
    <span className="pe-memory-channel">
      <FileText size={13} aria-hidden="true" />
      {cited.entity_type === "deal"
        ? t("person.brief.sourceDeal")
        : cited.entity_type}
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
    <Panel title={t("person.matters.title", { name: firstName })}>
      {MATTERS.map((row) => {
        const match = claims.find(
          (claim) => claim.kind === row.kind && claim.status !== "dismissed",
        );
        return (
          <PanelRow className="pe-row" key={row.kind}>
            <span className="pe-row-label">{t(row.labelKey)}</span>
            <span className="pe-row-value">
              {match ? match.body : <Absent />}
            </span>
            {match && <FileText size={15} aria-hidden="true" />}
          </PanelRow>
        );
      })}
    </Panel>
  );
}

// The band's what-matters block: true the moment any row PersonMattersCard
// lists has a live claim behind it, so a band that says "captured" is never
// followed by a panel showing nothing but "Nothing captured yet" three times.
export function hasMatters(view: Person360): boolean {
  const claims = view.claims ?? [];
  return MATTERS.some((row) =>
    claims.some(
      (claim) => claim.kind === row.kind && claim.status !== "dismissed",
    ),
  );
}

function mattersLine(view: Person360, t: ReturnType<typeof useT>): string {
  const claims = view.claims ?? [];
  const present = MATTERS.filter((row) =>
    claims.some(
      (claim) => claim.kind === row.kind && claim.status !== "dismissed",
    ),
  );
  if (present.length === 0) {
    return t("person.matters.absent");
  }
  return present.map((row) => t(row.labelKey)).join(", ");
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
      <Panel title={t("person.commercial.title")}>
        <PanelBody>
          <p className="pe-prose">{t("person.commercial.withheld")}</p>
        </PanelBody>
      </Panel>
    );
  }
  const deal = commercial.deal;
  return (
    <Panel title={t("person.commercial.title")}>
      <PanelBody>
        {/* "No open deal" is a fact about the deal, not about the role or the
            committee — a person can carry a buying role and sit on a
            committee with nothing currently for sale, and both facts belong
            on the card whether or not there is a deal to hang them off. */}
        {!deal && <p className="pe-prose">{t("person.commercial.noDeal")}</p>}
        {!deal && commercial.role && (
          <Badge tone="success">{readableRole(commercial.role)}</Badge>
        )}
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
          </>
        )}

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

        {deal && (
          <Button
            small
            className="pe-rail-more"
            onClick={() => navigate({ screen: "deals", id: deal.deal_id })}
          >
            {t("person.commercial.openDeal")}{" "}
            <ExternalLink size={13} aria-hidden="true" />
          </Button>
        )}
      </PanelBody>
    </Panel>
  );
}

// The stored role key rendered as words. An unrecognized key is shown as it
// was stored — inventing a label for a role nobody defined would be a claim.
export function readableRole(role: string): string {
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

// The band's commitments block: the same non-dismissed, loop-kind test the
// card's row list runs, so an empty band block and an empty panel are always
// the same fact rather than two independent reads of the claims.
export function hasCommitments(view: Person360): boolean {
  const claims = view.claims ?? [];
  return LOOPS.some((loop) =>
    claims.some(
      (claim) => claim.kind === loop.kind && claim.status !== "dismissed",
    ),
  );
}

function commitmentsLine(view: Person360, t: ReturnType<typeof useT>): string {
  const claims = view.claims ?? [];
  const openCount = LOOPS.flatMap((loop) =>
    claims.filter(
      (claim) => claim.kind === loop.kind && claim.status !== "dismissed",
    ),
  ).length;
  if (openCount === 0) {
    return t("person.loops.empty");
  }
  return `${openCount} ${t("person.loops.open")}`;
}

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
    <Panel title={t("person.loops.title")}>
      {rows.length === 0 && (
        // An empty commitments card on a record whose mail contains no
        // promises is CORRECT behaviour, not a gap (ADR-0097 consequences).
        <PanelBody>
          <p className="pe-prose">{t("person.loops.empty")}</p>
        </PanelBody>
      )}
      {rows.map(({ claim, loop }) => (
        <PanelRow className="pe-loop" key={claim.id}>
          {/* A read of the claim's done state, never a write: disabled so a
              click can't nudge the tick, and the accessible name lives here
              (sr-only) because the visible body sits in its own cell so the
              row's three-column rhythm holds. */}
          <Checkbox
            label={
              <span className="sr-only">
                {loopPrefix(loop, firstName, t)}
                {claim.body}
              </span>
            }
            checked={claim.status === "done"}
            disabled
          />
          <span className="pe-loop-body">
            {loopPrefix(loop, firstName, t)}
            {claim.body}
          </span>
          <LoopStatus claim={claim} />
        </PanelRow>
      ))}
    </Panel>
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
