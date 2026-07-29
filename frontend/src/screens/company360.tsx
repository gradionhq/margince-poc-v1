import { useQuery } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { navigate } from "../app/router";
import {
  Badge,
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
function isOverlayRefusal(problem: unknown): boolean {
  const details = (problem as { details?: { errors?: { code?: string }[] } })
    .details;
  return (details?.errors ?? []).some((e) => e.code === OVERLAY_REFUSAL);
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

// STATE_RANK orders the states by how much the card can actually say. A
// card built from two sections shows the better-informed of the two, so
// losing one grant narrows the card instead of blanking it.
const STATE_RANK: Record<SectionState, number> = {
  ready: 4,
  empty: 3,
  loading: 2,
  unavailable: 1,
  withheld: 0,
};

function mostInformative(a: SectionState, b: SectionState): SectionState {
  return STATE_RANK[a] >= STATE_RANK[b] ? a : b;
}

/**
 * sectionState classifies one 360 section. `present` is whether the payload
 * carried it at all, which is a different question from whether it had rows.
 */
export function sectionState(
  view: Organization360,
  section: Section,
  present: boolean,
  count: number,
): SectionState {
  if (omitted(view, section)) {
    return "withheld";
  }
  if (!present) {
    return "unavailable";
  }
  return count === 0 ? "empty" : "ready";
}

/**
 * SectionCard is the one shape every rail card takes, so the four answers
 * above stay visibly different rather than three of them looking alike.
 */
export function SectionCard({
  title,
  state,
  emptyLabel,
  action,
  children,
}: Readonly<{
  title: string;
  state: SectionState;
  emptyLabel: string;
  action?: ReactNode;
  children: ReactNode;
}>) {
  const t = useT();
  return (
    <section className="card co-card">
      <SectionHeader title={title} />
      {state === "ready" && children}
      {state === "empty" && <p className="co-empty">{emptyLabel}</p>}
      {state === "withheld" && (
        <p className="co-restricted">{t("co.section.restricted")}</p>
      )}
      {state === "unavailable" && (
        <p className="co-restricted">{t("co.section.unavailable")}</p>
      )}
      {state === "loading" && <Skeleton width="100%" height={32} />}
      {state === "ready" && action}
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
export function PeopleCard({ view }: Readonly<{ view: Organization360 }>) {
  const t = useT();
  const contacts = view.people?.data ?? [];
  const openDeals = view.deals?.data ?? [];
  const hasChampion = contacts.some((c) =>
    c.deal_roles.some((role) => role.role === "champion"),
  );
  return (
    <SectionCard
      title={t("co.people.title")}
      state={sectionState(
        view,
        "people",
        Boolean(view.people),
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
      {openDeals.length > 0 && !hasChampion && !omitted(view, "deals") && (
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
export function DealsCard({ view }: Readonly<{ view: Organization360 }>) {
  const t = useT();
  const { locale } = useLocale();
  const deals = view.deals;
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
      action={
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

/** TagsCard shows the lists the account is on and the tags applied to it. */
export function TagsCard({ view }: Readonly<{ view: Organization360 }>) {
  const t = useT();
  const tags = view.tags ?? [];
  const lists = view.list_memberships ?? [];
  // Two halves in one card: whichever half the caller CAN read decides what
  // the card shows, so a caller who reads tags but not lists still sees
  // their tags rather than one blanket refusal.
  const state = mostInformative(
    sectionState(view, "tags", Boolean(view.tags), tags.length),
    sectionState(
      view,
      "list_memberships",
      Boolean(view.list_memberships),
      lists.length,
    ),
  );
  return (
    <SectionCard
      title={t("co.tags.title")}
      state={state}
      emptyLabel={t("co.tags.empty")}
    >
      <p className="co-row-meta">
        {lists.map((list) => (
          <Badge key={list.id} tone="accent">
            {list.name}
          </Badge>
        ))}
        {tags.map((tag) => (
          <Badge key={tag.id}>{tag.name}</Badge>
        ))}
      </p>
    </SectionCard>
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
export function SinceLastVisit({ view }: Readonly<{ view: Organization360 }>) {
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
  if (delta.pending_proposals) {
    lines.push(t("co.since.proposals", { count: delta.pending_proposals }));
  }
  const first = !delta.baseline_at;
  return (
    <section className="card co-since">
      <SectionHeader title={t("co.since.title")} />
      {first && <p className="co-empty">{t("co.since.first")}</p>}
      {!first && lines.length === 0 && (
        <p className="co-empty">{t("co.since.nothing")}</p>
      )}
      {lines.length > 0 && (
        <p className="co-row-meta">
          {lines.map((line) => (
            <Badge key={line} tone="accent">
              {line}
            </Badge>
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
}: Readonly<{
  view: Organization360;
  renderAction?: (step: NextStep) => ReactNode;
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
              <span>{step.subject}</span>
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

/** OverlayFallback replaces the page when the workspace reads elsewhere. */
export function OverlayFallback() {
  const t = useT();
  return <EmptyState>{t("co.overlayFallback")}</EmptyState>;
}
