import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { navigate } from "../app/router";
import {
  Badge,
  Button,
  EmptyState,
  SectionHeader,
  Skeleton,
} from "../design-system/atoms";
import { formatDate, formatMoney } from "../format/format";

import { useLocale, useT } from "../i18n";
import { problemMessage } from "./common";
import "./company360.css";
import { EntityRef } from "./entityref";

// The company view's data layer and its right-rail cards.
//
// One read (GET /organizations/{id}/360) serves the whole page, and its
// `sections_omitted` is the thing that makes the page honest: a section the
// caller's role cannot read is ABSENT from the payload and named there, so
// every card below can say "hidden from you" instead of drawing an empty
// list that reads as "there is none".

type Organization360 = components["schemas"]["Organization360"];
type Contact = components["schemas"]["Organization360Contact"];
type Deal360 = components["schemas"]["Organization360Deal"];
type NextStep = components["schemas"]["Organization360NextStep"];
type Signal = components["schemas"]["Signal"];
type Section = Organization360["sections_omitted"][number];

// OVERLAY_REFUSAL is the validation code the 360 answers for a workspace
// reading from an incumbent mirror. It is a refusal to assemble, not a
// failure, so the screen falls back instead of showing an error.
const OVERLAY_REFUSAL = "unsupported_in_overlay_mode";

// RECORD_ZONE is the zone every record page renders its dates in, matching
// what RecordView passes its timeline. One spelling, so a due date and the
// activity beside it can never be read in two different zones.
export const RECORD_ZONE = "Europe/Berlin";

export type Org360Result =
  | { state: "ready"; view: Organization360 }
  | { state: "overlay" };

/** useOrganization360 reads the whole company page in one round trip. */
export function useOrganization360(id: string) {
  return useQuery<Org360Result>({
    queryKey: ["organization360", id],
    queryFn: async () => {
      const { data, error, response } = await api.GET(
        "/organizations/{id}/360",
        { params: { path: { id } } },
      );
      if (error) {
        if (response.status === 422 && isOverlayRefusal(error)) {
          return { state: "overlay" };
        }
        throw new Error(problemMessage(error));
      }
      return { state: "ready", view: data };
    },
  });
}

// isOverlayRefusal distinguishes "this workspace reads elsewhere" from every
// other 422 (a malformed id, say), which stays an error the caller sees.
//
// It narrows by checking rather than asserting: a problem body that is not
// the shape we expect — null, a string, an older server's payload — is not
// an overlay refusal, and must read as one failure rather than throwing a
// second one on the way to saying so.
function isOverlayRefusal(problem: unknown): boolean {
  const errors = asRecord(asRecord(problem)?.details)?.errors;
  if (!Array.isArray(errors)) {
    return false;
  }
  return errors.some((entry) => asRecord(entry)?.code === OVERLAY_REFUSAL);
}

// asRecord narrows an unknown to a readable object, or gives up. Truthiness
// first, because typeof null is "object" — the one case that would otherwise
// pass the guard and throw on the next property read.
function asRecord(value: unknown): Record<string, unknown> | undefined {
  return value && typeof value === "object"
    ? (value as Record<string, unknown>)
    : undefined;
}

/**
 * omitted reports whether the caller's role withheld one section.
 *
 * A payload with no list at all names nothing, so nothing reads as
 * withheld — the section then falls through to its empty state, which is
 * the safe display: "there is none" understates rather than inventing
 * content the caller cannot see.
 */
export function omitted(view: Organization360, section: Section): boolean {
  return (view.sections_omitted ?? []).includes(section);
}

/**
 * SectionState is what a card actually knows, and the four cases are
 * deliberately not collapsed:
 *
 *   ready       — the section came back with rows.
 *   empty       — the section came back, and there are none. A FACT.
 *   withheld    — the caller's role cannot read it; sections_omitted says so.
 *   unavailable — the section is missing and nobody said why: the read
 *                 failed, or the server sent a payload this client does not
 *                 fully understand.
 *
 * empty is the only one that may say "there is none", because it is the only
 * one that knows. Rendering the other three as empty states a fact the page
 * does not have — the rep reads "no open deals" and stops looking.
 */
export type SectionState =
  | "ready"
  | "empty"
  | "withheld"
  | "unavailable"
  | "loading";

/**
 * sectionState classifies one 360 section. `present` is whether the payload
 * carried it at all, which is a different question from whether it had rows.
 *
 * No payload at all — the composite read failed — makes every section
 * unavailable. The cards take an optional view for exactly this reason: a
 * fabricated empty payload would have to claim an as_of it does not have,
 * and would be indistinguishable from a real answer one refactor later.
 */
export function sectionState(
  view: Organization360 | undefined,
  section: Section,
  present: boolean,
  count: number,
): SectionState {
  if (!view) {
    return "unavailable";
  }
  if (omitted(view, section)) {
    return "withheld";
  }
  if (!present) {
    return "unavailable";
  }
  return count === 0 ? "empty" : "ready";
}

/**
 * SectionPart renders ONE section's body in whichever of the four states it
 * is in. A card with two independently-governed sections renders two of
 * these, so neither half's state can speak for the other.
 *
 * `label` names the part, and a card with more than one part MUST pass it:
 * "hidden from you" under a heading covering two sections says which of the
 * two it is only if the part is named. A single-section card leaves it out —
 * the card's own title is already the name.
 */
export function SectionPart({
  label,
  state,
  emptyLabel,
  children,
}: Readonly<{
  label?: string;
  state: SectionState;
  emptyLabel: string;
  children: ReactNode;
}>) {
  const t = useT();
  const body = (
    <>
      {state === "ready" && children}
      {state === "empty" && <p className="co-empty">{emptyLabel}</p>}
      {state === "withheld" && (
        <p className="co-restricted">{t("co.section.restricted")}</p>
      )}
      {state === "unavailable" && (
        <p className="co-restricted">{t("co.section.unavailable")}</p>
      )}
      {state === "loading" && <Skeleton width="100%" height={32} />}
    </>
  );
  if (!label) {
    return body;
  }
  return (
    <section className="co-part" aria-label={label}>
      <h3 className="co-part-label">{label}</h3>
      {body}
    </section>
  );
}

/**
 * SectionCard is the one shape a single-section rail card takes.
 *
 * `footer` carries figures that belong to the SECTION rather than to its
 * rows — an account's lifetime won total is true whether or not it has an
 * open deal today — so it renders whenever the section came back at all,
 * not only when the list has rows.
 */
export function SectionCard({
  title,
  state,
  emptyLabel,
  footer,
  children,
}: Readonly<{
  title: string;
  state: SectionState;
  emptyLabel: string;
  footer?: ReactNode;
  children: ReactNode;
}>) {
  const present = state === "ready" || state === "empty";
  return (
    <section className="card co-card">
      <SectionHeader title={title} />
      <SectionPart state={state} emptyLabel={emptyLabel}>
        {children}
      </SectionPart>
      {present && footer}
    </section>
  );
}

/**
 * PeopleCard lists the account's contacts with their relationship strength,
 * their role on the open deals, and whether they may be contacted.
 *
 * The two callouts are the ones a rep acts on: an account carried by a
 * single contact, and open deals with nobody named as champion.
 */
export function PeopleCard({ view }: Readonly<{ view?: Organization360 }>) {
  const t = useT();
  const contacts = view?.people?.data ?? [];
  const openDeals = view?.deals?.data ?? [];
  const hasChampion = contacts.some((c) =>
    c.deal_roles.some((role) => role.role === "champion"),
  );
  return (
    <SectionCard
      title={t("co.people.title")}
      state={sectionState(
        view,
        "people",
        Boolean(view?.people),
        contacts.length,
      )}
      emptyLabel={t("co.people.empty")}
    >
      <ul className="co-list">
        {contacts.map((contact) => (
          <ContactRow key={contact.person_id} contact={contact} />
        ))}
      </ul>
      {contacts.length === 1 && (
        <p className="co-callout">
          <Badge tone="warn">{t("co.people.singleThread")}</Badge>
        </p>
      )}
      {openDeals.length > 0 &&
        !hasChampion &&
        view &&
        !omitted(view, "deals") && (
          <p className="co-callout">
            <Badge tone="warn">{t("co.people.championGap")}</Badge>
          </p>
        )}
    </SectionCard>
  );
}

function ContactRow({ contact }: Readonly<{ contact: Contact }>) {
  const roles = contact.deal_roles.map((role) => role.role).filter(Boolean);
  return (
    <li className="co-row">
      <button
        type="button"
        className="co-rowlink"
        onClick={() => navigate({ screen: "contacts", id: contact.person_id })}
      >
        {contact.full_name}
      </button>
      <span className="co-row-meta">
        {contact.title && <span>{contact.title}</span>}
        <Badge tone={strengthTone(contact.strength.bucket)}>
          {contact.strength.score}
        </Badge>
        {roles.map((role) => (
          <Badge key={role}>{role}</Badge>
        ))}
        <ConsentChip consent={contact.consent} />
      </span>
    </li>
  );
}

// strengthTone maps the server's bucket onto a badge tone. The bucket is
// the server's word; nothing here re-derives a band from the score.
function strengthTone(
  bucket: Contact["strength"]["bucket"],
): "success" | "accent" | undefined {
  if (bucket === "strong") {
    return "success";
  }
  if (bucket === "warm") {
    return "accent";
  }
  return undefined;
}

/**
 * ConsentChip reports the contact's outbound consent. Consent is per
 * purpose and default-deny, so the chip reads GRANTED only when at least one
 * purpose is granted, and an empty map reads "none on file" rather than
 * silently looking permissive.
 */
function ConsentChip({ consent }: Readonly<{ consent: Contact["consent"] }>) {
  const t = useT();
  const states = Object.values(consent);
  if (states.some((state) => state === "granted")) {
    return <Badge tone="success">{t("co.people.consentGranted")}</Badge>;
  }
  if (states.some((state) => state === "withdrawn")) {
    return <Badge tone="danger">{t("co.people.consentWithdrawn")}</Badge>;
  }
  return <Badge>{t("co.people.consentUnknown")}</Badge>;
}

/** DealsCard lists the open pipeline plus the two lifetime figures. */
export function DealsCard({ view }: Readonly<{ view?: Organization360 }>) {
  const t = useT();
  const { locale } = useLocale();
  const deals = view?.deals;
  const won = deals?.won_lifetime;
  return (
    <SectionCard
      title={t("co.deals.title")}
      state={sectionState(
        view,
        "deals",
        Boolean(deals),
        deals?.data.length ?? 0,
      )}
      emptyLabel={t("co.deals.empty")}
      footer={
        deals && (
          <p className="co-row-meta">
            <span>
              {t("co.deals.wonLifetime")}{" "}
              {formatMoney(won?.amount_minor ?? 0, won?.currency ?? "", locale)}
            </span>
            <span>{t("co.deals.lostCount", { count: deals.lost_count })}</span>
          </p>
        )
      }
    >
      <ul className="co-list">
        {(deals?.data ?? []).map((deal) => (
          <DealRow key={deal.deal_id} deal={deal} />
        ))}
      </ul>
    </SectionCard>
  );
}

function DealRow({ deal }: Readonly<{ deal: Deal360 }>) {
  const t = useT();
  const { locale } = useLocale();
  return (
    <li className="co-row">
      <button
        type="button"
        className="co-rowlink"
        onClick={() => navigate({ screen: "deals", id: deal.deal_id })}
      >
        {deal.name}
      </button>
      <span className="co-row-meta">
        <span>{deal.stage_name ?? t("co.deals.noStage")}</span>
        {deal.amount?.amount_minor != null && (
          <span className="t-mono">
            {formatMoney(
              deal.amount.amount_minor,
              deal.amount.currency ?? "",
              locale,
            )}
          </span>
        )}
        {deal.stalled && <Badge tone="warn">{t("deal.stalled")}</Badge>}
      </span>
    </li>
  );
}

/**
 * TagsCard shows two INDEPENDENT sections in one card: the lists the account
 * belongs to, and the tags applied to it. They are governed by different
 * grants, so each reports its own state.
 *
 * Collapsing them into one verdict let either half speak for the other: a
 * caller who could read tags but not lists was told "not on any list, and no
 * tags applied", which was false about the half nobody had answered for.
 */
export function TagsCard({ view }: Readonly<{ view?: Organization360 }>) {
  const t = useT();
  const tags = view?.tags ?? [];
  const lists = view?.list_memberships ?? [];
  return (
    <section className="card co-card">
      <SectionHeader title={t("co.tags.title")} />
      <SectionPart
        label={t("co.tags.lists")}
        state={sectionState(
          view,
          "list_memberships",
          Boolean(view?.list_memberships),
          lists.length,
        )}
        emptyLabel={t("co.tags.noLists")}
      >
        <p className="co-row-meta">
          {lists.map((list) => (
            <Badge key={list.id} tone="accent">
              {list.name}
            </Badge>
          ))}
        </p>
      </SectionPart>
      <SectionPart
        label={t("co.tags.tags")}
        state={sectionState(view, "tags", Boolean(view?.tags), tags.length)}
        emptyLabel={t("co.tags.noTags")}
      >
        <p className="co-row-meta">
          {tags.map((tag) => (
            <Badge key={tag.id}>{tag.name}</Badge>
          ))}
        </p>
      </SectionPart>
    </section>
  );
}

/**
 * SignalsCard reads the account-filtered signals. It is its own query
 * rather than a 360 section: signals are a separate governed surface, and
 * the account filter is a dial on the list endpoint.
 */
export function SignalsCard({ orgId }: Readonly<{ orgId: string }>) {
  const t = useT();
  const { locale } = useLocale();
  const query = useQuery({
    queryKey: ["signals", "organization", orgId],
    queryFn: async () => {
      const { data, error } = await api.GET("/signals", {
        params: {
          query: { organization_id: orgId, status: "open", limit: 10 },
        },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data.data;
    },
  });
  const signals: Signal[] = query.data ?? [];
  // This card reads its own endpoint, so it owns the two states the 360's
  // sections get from the payload: a failed read is unavailable, and only a
  // successful one may say there are no signals.
  let state: SectionState = "ready";
  if (query.isError) {
    state = "unavailable";
  } else if (query.isPending) {
    state = "loading";
  } else if (signals.length === 0) {
    state = "empty";
  }
  return (
    <SectionCard
      title={t("co.signals.title")}
      state={state}
      emptyLabel={t("co.signals.empty")}
    >
      <ul className="co-list">
        {signals.map((signal) => (
          <li key={signal.id} className="co-row">
            <span>{signal.summary}</span>
            <span className="co-row-meta">
              <Badge>{signal.kind}</Badge>
              <span>{formatDate(signal.detected_at, locale, RECORD_ZONE)}</span>
            </span>
          </li>
        ))}
      </ul>
    </SectionCard>
  );
}

/**
 * SinceLastVisit is the "what changed" line. It reports only the dimensions
 * it was allowed to count: a null count means the caller lacks that grant,
 * which is not the same as zero, so those lines are absent rather than "0".
 */
export function SinceLastVisit({
  view,
  onOpenDecisions,
}: Readonly<{
  view: Organization360;
  // Given, the proposals count opens the queue it counts. Absent, it stays a
  // badge — the count is still true, it just has nowhere to send the reader.
  onOpenDecisions?: () => void;
}>) {
  const t = useT();
  const delta = view.since_last_visit;
  // Withheld or missing: the line is dropped rather than claiming nothing
  // changed. "Nothing new" is a fact this page would not have.
  if (!delta || omitted(view, "since_last_visit")) {
    return null;
  }
  const lines: string[] = [];
  if (delta.new_activities > 0) {
    lines.push(t("co.since.activities", { count: delta.new_activities }));
  }
  if (delta.deal_stage_moves) {
    lines.push(t("co.since.moves", { count: delta.deal_stage_moves }));
  }
  const proposals = delta.pending_proposals
    ? t("co.since.proposals", { count: delta.pending_proposals })
    : null;
  const first = !delta.baseline_at;
  const empty = lines.length === 0 && !proposals;
  return (
    <section className="card co-since">
      <SectionHeader title={t("co.since.title")} />
      {first && <p className="co-empty">{t("co.since.first")}</p>}
      {!first && empty && <p className="co-empty">{t("co.since.nothing")}</p>}
      {!empty && (
        <p className="co-row-meta">
          {lines.map((line) => (
            <Badge key={line} tone="accent">
              {line}
            </Badge>
          ))}
          {proposals &&
            (onOpenDecisions ? (
              <button
                type="button"
                className="co-since-open"
                onClick={onOpenDecisions}
              >
                <Badge tone="accent">{proposals}</Badge>
              </button>
            ) : (
              <Badge tone="accent">{proposals}</Badge>
            ))}
        </p>
      )}
    </section>
  );
}

/**
 * NextSteps is the middle column's first block: the open tasks on this
 * account, overdue first, each showing what it is linked to.
 */
export function NextSteps({
  view,
  renderAction,
  onOpenTask,
}: Readonly<{
  view: Organization360;
  renderAction?: (step: NextStep) => ReactNode;
  // Given, the subject opens the task where it is listed. Absent, it stays
  // plain text rather than a button that goes nowhere.
  onOpenTask?: (step: NextStep) => void;
}>) {
  const t = useT();
  const { locale } = useLocale();
  const steps = view.next_steps?.data ?? [];
  const state = sectionState(
    view,
    "next_steps",
    Boolean(view.next_steps),
    steps.length,
  );
  // A withheld block is dropped entirely — the middle column is the story,
  // and a refusal in the middle of it says nothing a rep can act on. Every
  // other state is shown, because "no open task" and "we could not tell"
  // lead to different next moves.
  if (state === "withheld") {
    return null;
  }
  return (
    <section className="card co-card">
      <SectionHeader title={t("co.next.title")} />
      {state === "unavailable" && (
        <p className="co-restricted">{t("co.section.unavailable")}</p>
      )}
      {state === "empty" && <p className="co-empty">{t("co.next.empty")}</p>}
      {state === "ready" && (
        <ul className="co-list">
          {steps.map((step) => (
            <li key={step.activity_id} className="co-row">
              {onOpenTask ? (
                <button
                  type="button"
                  className="co-rowlink"
                  onClick={() => onOpenTask(step)}
                >
                  {step.subject}
                </button>
              ) : (
                <span>{step.subject}</span>
              )}
              <span className="co-row-meta">
                {step.overdue && (
                  <Badge tone="danger">{t("co.next.overdue")}</Badge>
                )}
                {!step.overdue && step.due_at && (
                  <span>
                    {t("co.next.due", {
                      when: formatDate(step.due_at, locale, RECORD_ZONE),
                    })}
                  </span>
                )}
                {!step.due_at && <span>{t("co.next.undated")}</span>}
                {step.linked_deal_id && (
                  <EntityRef kind="deal" id={step.linked_deal_id} />
                )}
                {step.linked_person_id && (
                  <EntityRef kind="person" id={step.linked_person_id} />
                )}
                {step.assignee_id && (
                  <EntityRef kind="user" id={step.assignee_id} />
                )}
                {renderAction?.(step)}
              </span>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

type Cited = Brief["sentences"][number]["evidence"][number];
type CitedKind = Cited["entity_type"];

/**
 * A citation chip as it is rendered: either one openable record, or the count
 * of records of one kind that have nowhere to open.
 */
export type CitationChip =
  | { openable: true; entityType: CitedKind; entityId: string }
  | { openable: false; entityType: CitedKind; count: number };

/**
 * citationChips turns a sentence's raw evidence into what a reader should see.
 *
 * Two reductions, both of which the raw list gets wrong. The same record cited
 * twice is one source, not two. And several records of a kind the app cannot
 * open are one statement about that kind — rendered one by one they became a
 * run of identical unopenable labels ("activity activity activity"), which
 * says nothing the count does not say better.
 *
 * Order is first-seen, so the chips follow the sentence's own reasoning.
 */
export function citationChips(
  evidence: readonly Cited[],
  openable: (entityType: CitedKind) => boolean,
): CitationChip[] {
  const chips: CitationChip[] = [];
  const seen = new Set<string>();
  const flatAt = new Map<CitedKind, number>();
  for (const cited of evidence) {
    const identity = `${cited.entity_type}:${cited.entity_id}`;
    if (seen.has(identity)) {
      continue;
    }
    seen.add(identity);
    if (openable(cited.entity_type)) {
      chips.push({
        openable: true,
        entityType: cited.entity_type,
        entityId: cited.entity_id,
      });
      continue;
    }
    const at = flatAt.get(cited.entity_type);
    if (at === undefined) {
      flatAt.set(cited.entity_type, chips.length);
      chips.push({ openable: false, entityType: cited.entity_type, count: 1 });
      continue;
    }
    const chip = chips[at];
    if (!chip.openable) {
      chip.count += 1;
    }
  }
  return chips;
}

/**
 * Citations renders the chips for one sentence.
 *
 * A citation the app cannot open is rendered as a label, not as a button: a
 * clickable element that does nothing teaches the reader that citations do not
 * work, which costs more than the click it saves.
 */
function Citations({
  evidence,
  onOpenRecord,
}: Readonly<{
  evidence: readonly Cited[];
  onOpenRecord?: (entityType: string, entityId: string) => void;
}>) {
  const t = useT();
  const chips = citationChips(
    evidence,
    (entityType) => Boolean(onOpenRecord) && ROUTABLE_CITATIONS.has(entityType),
  );
  if (chips.length === 0) {
    return null;
  }
  return (
    <span className="co-brief-cites">
      {chips.map((chip) =>
        chip.openable ? (
          <button
            key={`${chip.entityType}:${chip.entityId}`}
            type="button"
            className="co-brief-cite"
            onClick={() => onOpenRecord?.(chip.entityType, chip.entityId)}
          >
            {t(`co.brief.cite.${chip.entityType}`)}
          </button>
        ) : (
          <span key={chip.entityType} className="co-brief-cite-flat">
            {chip.count === 1
              ? t(`co.brief.cite.${chip.entityType}`)
              : t(`co.brief.cite.${chip.entityType}.many`, {
                  count: chip.count,
                })}
          </span>
        ),
      )}
    </span>
  );
}

// The citation kinds that have a screen to open. An activity has no detail
// route of its own (it lives in a timeline) and the organization citation is
// the page the reader is already on.
const ROUTABLE_CITATIONS = new Set(["deal", "person"]);

/** OverlayFallback replaces the page when the workspace reads elsewhere. */
export function OverlayFallback() {
  const t = useT();
  return <EmptyState>{t("co.overlayFallback")}</EmptyState>;
}

type Brief = components["schemas"]["OrganizationBrief"];
type Answer = components["schemas"]["OrganizationAnswer"];
type Question = components["schemas"]["OrganizationQuestion"];
type Suggestion = components["schemas"]["Organization360Suggestion"];

/**
 * SentenceList renders grounded prose — the standing brief and the answers to
 * the prepared questions read identically, because they are the same thing
 * written from the same records with the same citations. One component, so a
 * citation can never be clickable in one place and flat in the other.
 */
function SentenceList({
  sentences,
  onOpenRecord,
}: Readonly<{
  sentences: Brief["sentences"];
  onOpenRecord?: (entityType: string, entityId: string) => void;
}>) {
  return (
    <ul className="co-brief-lines">
      {sentences.map((sentence, index) => (
        // Indexed because two sentences may legitimately read the same;
        // keying on the text collapses them into one row.
        // biome-ignore lint/suspicious/noArrayIndexKey: the list is replaced wholesale on every read, never reordered in place
        <li key={index}>
          {sentence.text}
          <Citations evidence={sentence.evidence} onOpenRecord={onOpenRecord} />
        </li>
      ))}
    </ul>
  );
}

/**
 * WrittenBy names which writer produced a piece of prose. Always shown: a
 * reader weighing a sentence needs to know whether a model or the
 * deterministic fallback wrote it, and the two are not interchangeable.
 */
function WrittenBy({ by }: Readonly<{ by: Brief["generated_by"] }>) {
  const t = useT();
  return (
    <Badge tone={by === "model" ? "ai" : undefined}>
      {t(`co.brief.by.${by}`)}
    </Badge>
  );
}

// The prepared questions, in the order the card offers them: what is open now,
// then what to walk in with, then what has moved.
//
// Keyed by question rather than listed, so the type is EXHAUSTIVE: a question
// declared upstream and not given a position here fails to compile, instead of
// shipping a server that answers it and a card that never asks.
const QUESTIONS: readonly Question[] = Object.keys({
  whats_open: 0,
  meeting_prep: 0,
  whats_changed: 0,
} satisfies Record<Question, 0>) as Question[];

/**
 * AskCard is "Ask Margince": three prepared questions, answered from this
 * account's own records.
 *
 * The questions are BUTTONS, not a text box. Each one names the records its
 * answer is written from, which is what lets every sentence carry a citation
 * the reader can open — and a text box that quietly answered from a subset
 * would look exactly like one that had searched everything.
 */
export function AskSection({
  orgId,
  enabled,
  onOpenRecord,
}: Readonly<{
  orgId: string;
  enabled: boolean;
  onOpenRecord?: (entityType: string, entityId: string) => void;
}>) {
  const t = useT();
  const { locale } = useLocale();
  const ask = useMutation({
    mutationFn: async (question: Question) => {
      const { data, error } = await api.POST("/organizations/{id}/ask", {
        params: { path: { id: orgId } },
        body: { question },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });

  if (!enabled) {
    return null;
  }
  const answer: Answer | undefined = ask.data;
  // A payload without sentences is an answer this build cannot read, not an
  // account with nothing to say — the same distinction every card here keeps.
  const readable = Array.isArray(answer?.sentences) ? answer : undefined;
  return (
    <section className="co-part co-ask" aria-label={t("co.ask.title")}>
      <h3 className="co-part-label">{t("co.ask.title")}</h3>
      <p className="co-ask-questions">
        {QUESTIONS.map((question) => (
          <Button
            key={question}
            small
            onClick={() => ask.mutate(question)}
            disabled={ask.isPending}
          >
            {t(`co.ask.q.${question}`)}
          </Button>
        ))}
      </p>
      {ask.isPending && <Skeleton width="100%" height={40} />}
      {ask.isError && (
        <p className="co-restricted">
          {t("co.ask.failed")}
          {/* The server's own detail says WHICH failure — budget exhausted reads
              differently from a malformed request, and a rep can act on one. */}
          {ask.error instanceof Error && ask.error.message
            ? ` ${ask.error.message}`
            : null}
        </p>
      )}
      {/* The previous answer is hidden while the next question is in flight.
          Leaving it under the spinner puts a finished answer next to a loading
          one, and the reader has no way to tell which question they are
          looking at the answer to. */}
      {readable && !ask.isPending && (
        <>
          {/* The question is repeated above its answer: three buttons and one
              answer block leaves the reader guessing which they pressed once
              they have scrolled, and the wrong pairing is worse than none. */}
          <p className="co-ask-asked">{t(`co.ask.q.${readable.question}`)}</p>
          {readable.sentences.length === 0 ? (
            // An empty answer is a real outcome, not a failure: the question's
            // records are not ones this reader can see, so there is nothing to
            // say. Saying that is honest; a sentence written around the gap
            // would not be.
            <p className="co-empty">{t("co.ask.nothing")}</p>
          ) : (
            <SentenceList
              sentences={readable.sentences}
              onOpenRecord={onOpenRecord}
            />
          )}
          <p className="co-row-meta">
            <WrittenBy by={readable.generated_by} />
            <span>
              {t("co.brief.generatedAt", {
                when: formatDate(readable.generated_at, locale, RECORD_ZONE),
              })}
            </span>
          </p>
        </>
      )}
    </section>
  );
}

/**
 * SuggestionsCard is what this account looks like it needs next.
 *
 * Each row leads with the REASON the rule fired, because a rep must be able to
 * disagree with the reason rather than with a verdict they cannot inspect. A
 * dismissal is theirs alone and is keyed on the evidence, so the same advice
 * stays gone while the situation holds and comes back when it changes.
 */
export function SuggestionsSection({
  orgId,
  view,
  onOpenRecord,
}: Readonly<{
  orgId: string;
  view?: Organization360;
  onOpenRecord?: (entityType: string, entityId: string) => void;
}>) {
  const t = useT();
  const client = useQueryClient();
  const dismiss = useMutation({
    mutationFn: async (fingerprint: string) => {
      const { error } = await api.POST(
        "/organizations/{id}/suggestions/dismiss",
        { params: { path: { id: orgId } }, body: { fingerprint } },
      );
      if (error) {
        throw new Error(problemMessage(error));
      }
    },
    // The 360 is the only thing that knows which suggestions survive, so the
    // row goes when the re-read says it does. Hiding it locally on click would
    // hide it even when the dismissal never reached the server.
    onSuccess: () =>
      client.invalidateQueries({ queryKey: ["organization360", orgId] }),
  });

  const suggestions: Suggestion[] = view?.suggestions ?? [];
  const dropped = view?.suggestions_dropped;
  const state = sectionState(
    view,
    "suggestions",
    Boolean(view?.suggestions),
    suggestions.length,
  );
  // A withheld, empty or unavailable suggestion block is dropped entirely.
  // Advice is additive: "no advice" and "we cannot advise you" are not things
  // a rep acts on, and either would claim space above the timeline that the
  // account's story needs.
  if (state !== "ready") {
    return null;
  }
  return (
    <section className="co-part co-suggest" aria-label={t("co.suggest.title")}>
      <h3 className="co-part-label">{t("co.suggest.title")}</h3>
      <ul className="co-list">
        {suggestions.map((suggestion) => (
          <li key={suggestion.fingerprint} className="co-row">
            <span>
              <span className="co-suggest-kind">
                {t(`co.suggest.kind.${suggestion.kind}`)}
              </span>
              {/* The reason is the suggestion. Everything else is chrome. */}
              <span className="co-suggest-reason">{suggestion.reason}</span>
            </span>
            <span className="co-row-meta">
              <Citations
                evidence={suggestion.evidence}
                onOpenRecord={onOpenRecord}
              />

              <Button
                small
                onClick={() => dismiss.mutate(suggestion.fingerprint)}
                // Only the row in flight is disabled: one dismissal must not
                // freeze the rep's other choices.
                disabled={
                  dismiss.isPending &&
                  dismiss.variables === suggestion.fingerprint
                }
              >
                {t("co.suggest.dismiss")}
              </Button>
            </span>
          </li>
        ))}
      </ul>
      {/* The card offers a handful, so what it left out is named. A truncated
          list with no count reads as "that is everything". Absent means the
          section was never computed, which this card does not render at all. */}
      {dropped !== undefined && dropped > 0 && (
        <p className="co-row-meta">
          {t("co.suggest.more", { count: dropped })}
        </p>
      )}
      {/* A dismissal that failed must say so: the row staying put with no word
          reads as a click that missed, and the rep clicks again. */}
      {dismiss.isError && (
        <p className="co-restricted">
          {t("co.suggest.dismissFailed")}
          {dismiss.error instanceof Error && dismiss.error.message
            ? ` ${dismiss.error.message}`
            : null}
        </p>
      )}
    </section>
  );
}

/** useOrganizationBrief reads the standing brief for this account. */
export function useOrganizationBrief(id: string, enabled: boolean) {
  return useQuery({
    queryKey: ["organization-brief", id],
    enabled,
    queryFn: async () => {
      const { data, error } = await api.GET("/organizations/{id}/brief", {
        params: { path: { id } },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });
}

/**
 * BriefCard leads the middle column with the account in a few sentences.
 *
 * Two things are always visible, because a reader deciding how much to
 * trust a sentence needs both: WHO wrote it — a model, or the deterministic
 * fallback — and whether it is still current. Every sentence carries the
 * records it was written from, and those are always records this reader can
 * open, because the brief was assembled under their own row scope.
 */
export function BriefSection({
  orgId,
  enabled,
  onOpenRecord,
}: Readonly<{
  orgId: string;
  enabled: boolean;
  onOpenRecord?: (entityType: string, entityId: string) => void;
}>) {
  const t = useT();
  const { locale } = useLocale();
  const query = useOrganizationBrief(orgId, enabled);
  const client = useQueryClient();
  const refresh = useMutation({
    mutationFn: async () => {
      const { data, error } = await api.POST("/organizations/{id}/brief", {
        params: { path: { id: orgId } },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
    onSuccess: (data) =>
      client.setQueryData(["organization-brief", orgId], data),
  });

  if (!enabled) {
    return null;
  }
  const brief: Brief | undefined = query.data;
  // A payload without sentences is a brief this build cannot read, not an
  // account with nothing to say about it — the same distinction every other
  // card on this page keeps.
  const readable = Array.isArray(brief?.sentences) ? brief : undefined;
  const unreadable = !query.isPending && !query.isError && !readable;
  return (
    <section className="co-part co-brief" aria-label={t("co.brief.title")}>
      <h3 className="co-part-label">{t("co.brief.title")}</h3>
      {query.isPending && <Skeleton width="100%" height={40} />}
      {(query.isError || unreadable) && (
        <p className="co-restricted">{t("co.section.unavailable")}</p>
      )}
      {readable && (
        <>
          <SentenceList
            sentences={readable.sentences}
            onOpenRecord={onOpenRecord}
          />
          <p className="co-row-meta">
            <WrittenBy by={readable.generated_by} />
            <span>
              {t("co.brief.generatedAt", {
                when: formatDate(readable.generated_at, locale, RECORD_ZONE),
              })}
            </span>
            <Button
              small
              onClick={() => refresh.mutate()}
              disabled={refresh.isPending}
            >
              {t("co.brief.refresh")}
            </Button>
            {/* A refresh that failed must say so: the button re-enabling on
                its own reads as "done", and the reader would take the brief
                in front of them for the refreshed one. */}
            {refresh.isError && (
              <span className="co-restricted">
                {t("co.brief.refreshFailed")}
              </span>
            )}
          </p>
        </>
      )}
    </section>
  );
}
