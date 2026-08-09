import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { type ReactNode, useEffect, useId, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { navigate } from "../app/router";
import {
  Badge,
  Button,
  EmptyState,
  Field,
  Modal,
  SectionHeader,
  Skeleton,
  StatCard,
} from "../design-system/atoms";
import { Select } from "../design-system/select";
import { formatDate, formatDateTime, formatMoney } from "../format/format";

import { type Locale, useLocale, useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { problemMessageOf, throwProblem } from "./common";
import "./company360.css";
import {
  routesTo,
  type StrengthBucket,
  useOrganizationGraph,
} from "./connections";
import {
  byReach,
  missingRoles,
  reachLabelKey,
  reachOf,
  roleLabelKey,
} from "./coverage";
import { CoverageExplorer } from "./coverageexplorer";
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

// What each signal kind is, in words. The badge rendered the stored enum, so
// a German reader met `buying_intent` and an English one met an identifier.
// Typed against the schema union: a kind added upstream fails the build here.
// Keyed by plain string, matching how the value arrives: the strip's signal
// kind is an open wire string, so a producer added upstream must be able to
// reach signalKindLabel's fallback rather than failing the index.
const SIGNAL_KIND_LABELS: Record<string, MessageKey> = {
  stalled_deal: "signal.kind.stalled_deal",
  champion_left: "signal.kind.champion_left",
  reengagement: "signal.kind.reengagement",
  buying_intent: "signal.kind.buying_intent",
  risk: "signal.kind.risk",
  other: "signal.kind.other",
  contract_ended: "signal.kind.contract_ended",
  new_opportunity: "signal.kind.new_opportunity",
  commitment_made: "signal.kind.commitment_made",
  ghosted_thread: "signal.kind.ghosted_thread",
};

// The strip's signal kind is an open string on the wire, on purpose: the strip
// states whatever a producer raised, and a producer added upstream must not
// make the tile disappear. An unmapped kind renders as its own words rather
// than as an identifier — the same degradation an unmapped approval kind gets.
export function signalKindLabel(
  kind: string,
  t: (key: MessageKey) => string,
): string {
  // Own-property only, as dealRoleLabel does below: a wire value named
  // `toString` would otherwise find something on Object's prototype and pass
  // the truthy check instead of degrading to its own words.
  const key = Object.hasOwn(SIGNAL_KIND_LABELS, kind)
    ? SIGNAL_KIND_LABELS[kind]
    : undefined;
  return key ? t(key) : kind.replaceAll("_", " ");
}

// How serious a signal is, in the strip's own vocabulary of tones. `info` is
// deliberately untoned: the strip leads with the WORST open signal, and an
// account whose worst news is a commitment somebody made is an account with no
// bad news — colouring that would cry wolf on every healthy record.
const SIGNAL_TONE: Record<string, "warn" | "danger" | undefined> = {
  info: undefined,
  warn: "warn",
  urgent: "danger",
};

// Severity is a closed enum on the wire, but it arrives as a string like every
// other wire value: an own-property check keeps a value named `toString` from
// finding something on Object's prototype and typing as a tone.
function signalTone(severity: string): "warn" | "danger" | undefined {
  return Object.hasOwn(SIGNAL_TONE, severity)
    ? SIGNAL_TONE[severity]
    : undefined;
}

// The deal-stakeholder roles worth a word. `role` is free text on the wire
// (the enum is an unminted contract extension, DEAL-EXT-5), so an unknown
// value renders as itself rather than being hidden — a role somebody typed is
// still a fact about this contact.
const DEAL_ROLE_LABELS: Record<string, MessageKey> = {
  champion: "co.role.champion",
  economic_buyer: "co.role.economic_buyer",
  blocker: "co.role.blocker",
  influencer: "co.role.influencer",
  user: "co.role.user",
};

export function dealRoleLabel(role: string, t: (key: MessageKey) => string) {
  // Own-property only: `role` is free text off the wire, and a value named
  // `toString` or `constructor` would otherwise find something on Object's
  // prototype, pass the truthy check, and render as an empty badge.
  const key = Object.hasOwn(DEAL_ROLE_LABELS, role)
    ? DEAL_ROLE_LABELS[role]
    : undefined;
  return key ? t(key) : role.replace(/_/g, " ");
}
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
        throwProblem(error);
      }
      return { state: "ready", view: data };
    },
  });
}

// VIEW_ACK_DWELL_MS is how long the account must stay open before the visit
// counts. Opening a record and bouncing straight back out is not reading it,
// and an ack from that would mark unread activity as seen.
const VIEW_ACK_DWELL_MS = 5_000;

/**
 * useAcknowledgeOrganizationView advances THIS reader's "last seen" baseline
 * for the account — the thing that makes "N new since your last visit" mean
 * anything on the next visit. Without it the server keeps answering with no
 * baseline at all, so every visit reads as the first one.
 *
 * The 360 deliberately does not advance the baseline itself (a prefetch must
 * not be indistinguishable from a visit), so this is the only caller. Leaving
 * before the dwell elapses cancels the timer: the baseline moves only for a
 * visit that actually happened, and when in doubt it stays where it is —
 * showing an item twice is a smaller wrong than hiding one.
 *
 * Success does NOT invalidate the 360. The "new since your last visit" line
 * describes the visit in progress; refetching it out from under the reader
 * would erase the very thing they opened the page to see.
 */
export function useAcknowledgeOrganizationView(id: string, visited: boolean) {
  const ack = useMutation({
    mutationFn: async (organizationId: string) => {
      const { error } = await api.POST("/organizations/{id}/view-ack", {
        params: { path: { id: organizationId } },
      });
      if (error) {
        throwProblem(error);
      }
    },
  });
  // The mutation's own error state holds a failure; nothing renders it. A
  // baseline that did not move costs the reader one repeated line next time,
  // which is not worth an error banner over the account they came to read.
  const fire = ack.mutate;
  useEffect(() => {
    if (!visited) {
      return;
    }
    const timer = window.setTimeout(() => fire(id), VIEW_ACK_DWELL_MS);
    return () => window.clearTimeout(timer);
  }, [id, visited, fire]);
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
 *
 * The §7 matrix adds four more, each of which would otherwise be drawn as one
 * of the above and lose what makes it different:
 *
 *   unsupported — this MODE cannot serve the section (an overlay-only
 *                 installation and a native composite section). Distinct from
 *                 unavailable: nothing is broken and retrying changes nothing.
 *   failed      — the read failed and can be retried. `onRetry` is what makes
 *                 it a different state from unavailable rather than a
 *                 differently-worded one.
 *   stale       — the last known value, with when it was last true. It is
 *                 shown rather than withheld, because a figure from this
 *                 morning beats a blank, but never without its `as of`.
 *   partial     — some of the rows, with the count that is missing and a way
 *                 to the full list. Never a silent truncation.
 */
export type SectionState =
  | "ready"
  | "empty"
  | "withheld"
  | "unavailable"
  | "loading"
  | "unsupported"
  | "failed"
  | "stale"
  | "partial";

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
 * SectionDetail is what the four §7 states need in order to say something a
 * reader can act on rather than a differently-worded "not here".
 */
export type SectionDetail = {
  // `failed` without a retry is `unavailable` with extra words.
  onRetry?: () => void;
  // Already formatted by the caller: this tier holds no locale or zone.
  staleAsOf?: string;
  // How many rows the caller is NOT seeing. A truncation nobody states reads
  // as the whole list.
  remaining?: number;
  // Which mode limitation this is, in the caller's words. The generic
  // sentence is the floor, not the target.
  unsupportedReason?: string;
};

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
  detail,
  children,
}: Readonly<{
  label?: string;
  state: SectionState;
  emptyLabel: string;
  // What the four §7 states need in order to be honest. Each is read by
  // exactly one state and ignored by the rest; a state whose detail is absent
  // still renders, one sentence shorter.
  detail?: SectionDetail;
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
      {state === "unsupported" && (
        <p className="co-restricted">
          {detail?.unsupportedReason ?? t("co.section.unsupported")}
        </p>
      )}
      {state === "failed" && (
        <div className="co-failed">
          <p className="co-restricted">{t("co.section.failed")}</p>
          {detail?.onRetry && (
            <Button small onClick={detail.onRetry}>
              {t("co.section.retry")}
            </Button>
          )}
        </div>
      )}
      {/* The value first, then when it was last true. Reversing them buries
          the caveat under the figure a reader has already taken as current. */}
      {state === "stale" && (
        <>
          <p className="co-stale">
            {detail?.staleAsOf
              ? t("co.section.staleAsOf", { when: detail.staleAsOf })
              : t("co.section.stale")}
          </p>
          {children}
        </>
      )}
      {state === "partial" && (
        <>
          {children}
          <p className="co-empty">
            {detail?.remaining
              ? t("co.section.partialCount", { count: detail.remaining })
              : t("co.section.partial")}
          </p>
        </>
      )}
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
  detail,
  footer,
  actions,
  children,
}: Readonly<{
  title: string;
  state: SectionState;
  emptyLabel: string;
  detail?: SectionDetail;
  footer?: ReactNode;
  // Verbs that CHANGE this section, under everything that describes it.
  //
  // They render whenever the section is present — including when it is empty,
  // which is the state a create verb most belongs to. They do NOT render on a
  // withheld or unavailable section: a caller who may not read the deals has
  // no business being offered a button to add one, and a section that failed
  // to load cannot say whether the write would even make sense.
  actions?: ReactNode;
  children: ReactNode;
}>) {
  // `stale` and `partial` both carry real rows, so the footer figures and the
  // verbs that change the section belong with them — a truncated deal list is
  // still a deal list you can add to.
  const present =
    state === "ready" ||
    state === "empty" ||
    state === "stale" ||
    state === "partial";
  return (
    <section className="card co-card">
      <SectionHeader title={title} />
      <SectionPart state={state} emptyLabel={emptyLabel} detail={detail}>
        {children}
      </SectionPart>
      {present && footer}
      {present && actions && <div className="co-card-actions">{actions}</div>}
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
// incompleteGraph says the connection graph this page read is not the whole
// one: it capped its contact ring, or it withheld groups the caller may not
// read. Either way the routes below it are a subset, and both the empty answer
// and the found-someone answer have to say so.
export function incompleteGraph(graph: {
  groups_omitted?: unknown[];
  dropped_count?: number;
}): boolean {
  return (
    (graph.groups_omitted?.length ?? 0) > 0 || (graph.dropped_count ?? 0) > 0
  );
}

export function PeopleCard({
  view,
  // Whether this account takes writes at all. An archived record is read-only
  // — the page hides every other verb on one — so the role control goes too.
  writable = false,
  // The account whose connection graph a route-in asks about. Absent on the
  // loading skeleton, where there is no account to ask about yet.
  orgId,
}: Readonly<{
  view?: Organization360;
  writable?: boolean;
  orgId?: string;
}>) {
  const t = useT();
  const contacts = [...(view?.people?.data ?? [])].sort(byReach);
  const truncated = Boolean(view?.people?.page.has_more);
  const dealsReadable =
    Boolean(view?.deals) && view != null && !omitted(view, "deals");
  const openDeals: OpenDeal[] = dealsReadable
    ? (view?.deals?.data ?? []).map((deal) => ({
        id: deal.deal_id,
        name: deal.name,
      }))
    : [];
  const openDealIds = new Set(openDeals.map((deal) => deal.id));
  // Every way the committee picture can be partial, in one flag. An empty
  // `contacts` means "nobody" only when the section was actually READ: a
  // people section the grants withheld, or one this response never carried,
  // leaves the same empty array and would otherwise report both roles missing
  // from data the page never had. Deals past their first page hide the roles
  // held on them the same way.
  const committeeIncomplete =
    truncated ||
    !view?.people ||
    omitted(view, "people") ||
    Boolean(view?.deals?.page.has_more);
  const missing = missingRoles(contacts, openDealIds, committeeIncomplete);
  const untried = contacts.filter((c) => reachOf(c) === "untried");
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
      // The per-row coverage says who to call. The explorer answers the other
      // question — where are we thin — for a handful of colleagues the reader
      // picks, rather than a column per person on a forty-strong team.
      actions={
        orgId && contacts.length > 0 ? (
          <CoverageExplorer orgId={orgId} contacts={contacts} />
        ) : undefined
      }
    >
      {/* The per-contact chips read as all-time claims — "Not approached"
          above a timeline showing last year's outbound email is the page
          arguing with itself. They are computed over the server's 90-day
          window (PO-F-3), so the window is stated once here rather than
          repeated on every row. */}
      {contacts.length > 0 && (
        <p className="t-caption">{t("co.reach.window")}</p>
      )}
      <ul className="co-list">
        {contacts.map((contact) => (
          <ContactRow
            key={contact.person_id}
            contact={contact}
            openDeals={openDeals}
            writable={writable}
            orgId={orgId}
          />
        ))}
      </ul>
      {contacts.length === 1 && !truncated && (
        <p className="co-callout">
          <Badge tone="warn">{t("co.people.singleThread")}</Badge>
        </p>
      )}
      {/* Who is missing, not only who is present. On an account where every
          known contact has gone quiet, the person nobody has written to is the
          only move left that is not a fourth follow-up. */}
      {untried.length > 0 && (
        <p className="co-callout">
          <Badge tone="accent">
            {untried.length === 1
              ? t("co.people.untriedHintOne")
              : t("co.people.untriedHint", { count: untried.length })}
          </Badge>
        </p>
      )}
      {missing.length > 0 && (
        <p className="co-callout">
          <Badge tone="warn">
            {t("co.people.missing", {
              roles: missing.map((role) => t(roleLabelKey(role))).join(" / "),
            })}
          </Badge>
        </p>
      )}
    </SectionCard>
  );
}

// OpenDeal is the slice of an open deal a role can be attached to.
type OpenDeal = { id: string; name: string };

// everyRoleHeld reports whether this contact already holds every assignable
// role on every open deal, which is when the verb has nothing left to write.
function everyRoleHeld(
  contact: Contact,
  openDeals: readonly OpenDeal[],
): boolean {
  return openDeals.every((deal) => {
    const held = new Set(
      contact.deal_roles
        .filter((entry) => entry.deal_id === deal.id)
        .map((entry) => entry.role),
    );
    return ASSIGNABLE_ROLES.every((role) => held.has(role));
  });
}

// The stakeholder roles offered here. `role` is free text on the wire until
// DEAL-EXT-5 mints the enum upstream, so this list is the UI's own vocabulary
// — the five the spec names, in the order a rep thinks of them.
const ASSIGNABLE_ROLES = [
  "champion",
  "economic_buyer",
  "influencer",
  "blocker",
  "user",
] as const;

/**
 * SetRoleAction records who this person is on a deal.
 *
 * The page told a reader "nobody here is your champion" and gave them nowhere
 * to say who is: the roles live on `relationship` rows written from the deal
 * screen, which is a different page and a different task. So the warning was
 * true, unactionable, and permanent.
 *
 * The role is recorded HUMAN-set, never inferred. Every CRM surveyed keeps
 * buyer roles human-tagged — AI may suggest one, but a champion nobody named
 * is a guess about a relationship, and the whole committee reading is built
 * on top of it.
 */
function SetRoleAction({
  contact,
  openDeals,
}: Readonly<{ contact: Contact; openDeals: readonly OpenDeal[] }>) {
  const t = useT();
  const queryClient = useQueryClient();
  const [open, setOpen] = useState(false);
  const [dealId, setDealId] = useState("");
  const [role, setRole] = useState<string>(ASSIGNABLE_ROLES[0]);
  const titleId = useId();

  // A role this contact already holds on the selected deal is not on offer:
  // the write creates an edge, so picking it again asks the server for a
  // second copy of a fact that is already recorded.
  const held = new Set(
    contact.deal_roles
      .filter((entry) => entry.deal_id === dealId)
      .map((entry) => entry.role),
  );
  const offered: readonly string[] = ASSIGNABLE_ROLES.filter(
    (candidate) => !held.has(candidate),
  );
  // Changing the deal changes what is left to pick, so the selection follows
  // the list rather than the list following a stale selection.
  const picked = offered.includes(role) ? role : offered[0];

  const save = useMutation({
    mutationFn: async () => {
      const { data, error } = await api.POST("/relationships", {
        body: {
          kind: "deal_stakeholder",
          person_id: contact.person_id,
          deal_id: dealId,
          role: picked,
          is_current_primary: false,
          source: "manual",
        },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    onSuccess: async () => {
      setOpen(false);
      // The committee reading, the missing-role warning and the row's own
      // chips all come off the 360, so the account is re-read rather than
      // patched in place.
      await queryClient.invalidateQueries({ queryKey: ["organization360"] });
    },
  });

  // A role belongs to a deal. With no open deal there is nothing to be a
  // champion OF, and the card already says so in its own words.
  //
  // Nothing left to offer is the same answer: a contact already holding every
  // role on every open deal would otherwise open a dialog with an empty list
  // and a dead Save button.
  if (openDeals.length === 0 || everyRoleHeld(contact, openDeals)) {
    return null;
  }
  return (
    <>
      <Button
        small
        onClick={() => {
          setDealId(openDeals[0].id);
          setOpen(true);
        }}
      >
        {t("co.role.set")}
      </Button>
      <Modal open={open} onClose={() => setOpen(false)} labelledBy={titleId}>
        <h2 id={titleId} className="t-h2 modal-title">
          {t("co.role.setOn", { name: contact.full_name })}
        </h2>
        {/* What the two words mean, once, where they are being chosen. The
            page used them as though everyone shares one definition. */}
        <p className="t-caption">{t("co.role.explain")}</p>
        <div className="form-stack">
          <Field label={t("co.role.onDeal")}>
            {(control) => (
              <Select
                {...control}
                value={dealId}
                onChange={setDealId}
                options={openDeals.map((deal) => ({
                  value: deal.id,
                  label: deal.name,
                }))}
              />
            )}
          </Field>
          <Field label={t("co.role.role")}>
            {(control) => (
              <Select
                {...control}
                value={picked ?? ""}
                onChange={setRole}
                options={offered.map((candidate) => ({
                  value: candidate,
                  label: dealRoleLabel(candidate, t),
                }))}
              />
            )}
          </Field>
          {save.isError && (
            <p className="t-caption form-error">
              {problemMessageOf(save.error, t)}
            </p>
          )}
          <div className="form-actions">
            <Button
              variant="primary"
              onClick={() => save.mutate()}
              disabled={save.isPending || dealId === "" || !picked}
            >
              {t("record.save")}
            </Button>
            <Button onClick={() => setOpen(false)}>{t("fab.close")}</Button>
          </div>
        </div>
      </Modal>
    </>
  );
}

function ContactRow({
  contact,
  openDeals,
  writable,
  orgId,
}: Readonly<{
  contact: Contact;
  // The account this contact belongs to, for the route-in read.
  orgId?: string;
  // The open deals a role can be recorded against. A role belongs to a DEAL,
  // not to a person: this contact may be the champion on the renewal and
  // nobody on the new business.
  openDeals: readonly OpenDeal[];
  // Read-only accounts still NAME the roles held on them; they just offer no
  // way to change one.
  writable: boolean;
}>) {
  const t = useT();
  // Only roles on the deals this card is showing. `deal_roles` carries a
  // contact's role on CLOSED deals too, and a champion badge read off a deal
  // that was lost last year describes a pipeline that no longer exists.
  const openDealIds = new Set(openDeals.map((deal) => deal.id));
  const roles = contact.deal_roles.filter(
    (entry) => entry.role && openDealIds.has(entry.deal_id),
  );
  // Which deal a role is on only matters when there is more than one to
  // confuse: a person can be champion on the renewal and nobody on the new
  // business, and two identical badges would say neither.
  const nameOfDeal = (dealId: string) =>
    openDeals.length > 1
      ? openDeals.find((deal) => deal.id === dealId)?.name
      : undefined;
  const reach = reachOf(contact);
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
        {/* Where this person stands with us. "No reply" and "never asked"
            looked identical in this list and call for opposite next moves. */}
        <Badge tone={reach === "answered" ? "success" : undefined}>
          {t(reachLabelKey(reach))}
        </Badge>
        {roles.map((entry) => {
          const deal = nameOfDeal(entry.deal_id);
          return (
            <Badge key={`${entry.deal_id}:${entry.role}`}>
              {deal
                ? `${dealRoleLabel(entry.role, t)} · ${deal}`
                : dealRoleLabel(entry.role, t)}
            </Badge>
          );
        })}
        {/* Who here can actually reach them, inline rather than a click away.
            A forty-person team makes a contact x colleague matrix unreadable,
            so the row names the few worth naming and counts the rest. */}
        <ContactRoutes routes={contact.routes} />
        <ConsentChip consent={contact.consent} />
        {/* The page said "nobody here is your champion" and gave no way to
            say who is: the roles are set on the deal screen, which is a
            different page and a different task. */}
        {writable && <SetRoleAction contact={contact} openDeals={openDeals} />}
        {orgId && <RouteInAction orgId={orgId} contact={contact} />}
      </span>
    </li>
  );
}

// The strongest few colleagues who have actually exchanged messages with this
// contact, and how many more there are.
//
// UNTRIED IS NOT COLD, and the distinction is the whole reason this renders a
// sentence rather than an empty space: "nobody has tried" tells a rep to pick up
// the phone, where a cold band tells them somebody already did and got nowhere.
// A page that shows the same thing for both gives the opposite instruction half
// the time.
function ContactRoutes({ routes }: Readonly<{ routes?: Contact["routes"] }>) {
  const t = useT();
  // Absent, not empty: the caller has no roster grant, so naming a colleague is
  // a read they may not make. Silence is the honest answer — an "untried" badge
  // here would be a claim about the account rather than about the reader.
  if (!routes) {
    return null;
  }
  if (routes.untried) {
    return <Badge>{t("co.routes.untried")}</Badge>;
  }
  return (
    <span className="co-routes">
      {routes.top.map((route) => (
        <Badge
          key={route.user_id}
          tone={route.strength_bucket === "strong" ? "success" : undefined}
        >
          {route.display_name}
        </Badge>
      ))}
      {routes.remainder > 0 && (
        // The count, not a silent truncation: three names with nothing after
        // them reads as "these are the only three".
        <span className="t-caption">
          {t("co.routes.more", { count: routes.remainder })}
        </span>
      )}
    </span>
  );
}

/**
 * RouteInAction answers one question about one person: who here already talks
 * to them.
 *
 * It replaces the standing connections card, which asked nobody and answered
 * everybody — a staff directory in the rail of every account, costing a graph
 * read on every page load. This reads the same endpoint and only when someone
 * asks, and it asks the question from the end a rep actually has: not "who is
 * Lars in contact with" but "how do I reach Dana".
 *
 * "Nobody yet" is an answer worth giving, so it is given. The alternative — a
 * button that opens onto nothing — makes the reader wonder whether the read
 * failed.
 */
// The server's own bands, in this surface's words. Derived nowhere: the score
// behind them is the black box AC-company-3 took off this page.
const ROUTE_BAND_LABELS: Record<StrengthBucket, MessageKey> = {
  strong: "co.routeIn.band.strong",
  moderate: "co.routeIn.band.some",
  weak: "co.routeIn.band.faint",
  none: "co.routeIn.band.unknown",
};

function RouteInAction({
  orgId,
  contact,
}: Readonly<{ orgId: string; contact: Contact }>) {
  const t = useT();
  const [open, setOpen] = useState(false);
  const titleId = useId();
  // The read is armed by opening, so a page nobody asks costs no graph query.
  const query = useOrganizationGraph(orgId, open);
  const graph = query.data;
  const readable = Array.isArray(graph?.nodes) ? graph : undefined;
  const routes = readable ? routesTo(readable, contact.person_id) : [];

  return (
    <>
      <button
        type="button"
        className="link-button"
        onClick={() => setOpen(true)}
      >
        {t("co.routeIn.open")}
      </button>
      {open && (
        <Modal open onClose={() => setOpen(false)} labelledBy={titleId}>
          <h2 id={titleId} className="t-h2 modal-title">
            {t("co.routeIn.title", { name: contact.full_name })}
          </h2>
          {query.isPending && <Skeleton width="100%" height={64} />}
          {/* A failed read is unavailable, never "nobody knows them": the two
              call for opposite next moves, and only a read that succeeded can
              say the second. */}
          {(query.isError || (!query.isPending && !readable)) && (
            <p className="co-restricted">{t("co.section.unavailable")}</p>
          )}
          {/* "Nobody" is a claim about the account, and only a COMPLETE read
              can make it. The graph caps its contact ring and withholds whole
              groups the caller may not read, so a page showing 25 contacts can
              hold a graph that saw 15 — and a rep told "nobody here has
              written to them" would stop looking. Partial says so instead. */}
          {readable && routes.length === 0 && (
            <EmptyState>
              {t(
                incompleteGraph(readable)
                  ? "co.routeIn.partial"
                  : "co.routeIn.none",
              )}
            </EmptyState>
          )}
          {/* The same incompleteness, said over a list that DID find someone.
              Stated only when the list is empty, a page that read 15 of 25
              contacts presented the routes it happened to see as all of them,
              and a rep who found one stopped looking for a better one. */}
          {readable && routes.length > 0 && incompleteGraph(readable) && (
            <p className="t-caption">{t("co.routeIn.mayBeMore")}</p>
          )}
          {readable && routes.length > 0 && (
            <ul className="co-list">
              {routes.map((route) => (
                <li key={route.id} className="co-row">
                  <span>{route.label}</span>
                  {/* The strength band, not the number: the score itself is
                      the black box AC-company-3 took off this page. */}
                  <span className="co-row-meta">
                    {t(ROUTE_BAND_LABELS[route.bucket])}
                  </span>
                </li>
              ))}
            </ul>
          )}
        </Modal>
      )}
    </>
  );
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
export function DealsCard({
  view,
  actions,
}: Readonly<{
  view?: Organization360;
  // The verbs that change this section, rendered under it. Absent on an
  // archived record, which takes no new deals.
  actions?: ReactNode;
}>) {
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
      // The verb sits under the section it changes, and renders whatever the
      // section's own state is: "no open deal on this account" is exactly the
      // reading that should be one click from opening one.
      actions={actions}
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
export function TagsCard({
  view,
  listAction,
  tagAction,
}: Readonly<{
  view?: Organization360;
  // One verb per SECTION, not one per card. The two halves are governed by
  // different grants, so a caller who may read tags but not lists must be
  // offered the tag verb and not the list one — the same rule the card
  // already applies to what it displays.
  listAction?: ReactNode;
  tagAction?: ReactNode;
}>) {
  const t = useT();
  const tags = view?.tags ?? [];
  const lists = view?.list_memberships ?? [];
  const listState = sectionState(
    view,
    "list_memberships",
    Boolean(view?.list_memberships),
    lists.length,
  );
  const tagState = sectionState(view, "tags", Boolean(view?.tags), tags.length);
  // Present means read and answered — ready or empty. A withheld section says
  // the caller may not see it, and an unavailable one says nobody knows; a
  // verb on either offers a write whose refusal would be the first the reader
  // hears of the limit.
  const shows = (state: SectionState) => state === "ready" || state === "empty";
  return (
    <section className="card co-card">
      <SectionHeader title={t("co.tags.title")} />
      <SectionPart
        label={t("co.tags.lists")}
        state={listState}
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
        state={tagState}
        emptyLabel={t("co.tags.noTags")}
      >
        <p className="co-row-meta">
          {tags.map((tag) => (
            <Badge key={tag.id}>{tag.name}</Badge>
          ))}
        </p>
      </SectionPart>
      {/* The strip exists only when something is IN it. An archived company
          passes no verbs, and a wrapper rendered anyway leaves an empty box
          and its margin under the card — SectionCard already gates on the
          same condition. */}
      {((shows(tagState) && tagAction) || (shows(listState) && listAction)) && (
        <div className="co-card-actions">
          {shows(tagState) && tagAction}
          {shows(listState) && listAction}
        </div>
      )}
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
        throwProblem(error);
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
              <Badge>{t(SIGNAL_KIND_LABELS[signal.kind])}</Badge>
              <span>{formatDate(signal.detected_at, locale, RECORD_ZONE)}</span>
            </span>
          </li>
        ))}
      </ul>
    </SectionCard>
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

type Cited = BriefSentence["evidence"][number];
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
// The citation kinds that open a RECEIPT rather than a record page. Only these
// can be stepped through, because only these render in the drawer.
const RECEIPT_CITATIONS = new Set(["fact", "profile_field"]);

// One steppable citation, in the receipt's own shape.
export type CitedSibling = {
  entityType: "fact" | "profile_field";
  entityId: string;
};

function Citations({
  evidence,
  onOpenRecord,
}: Readonly<{
  evidence: readonly Cited[];
  onOpenRecord?: (
    entityType: string,
    entityId: string,
    siblings?: readonly CitedSibling[],
  ) => void;
}>) {
  const t = useT();
  const chips = citationChips(
    evidence,
    (entityType) => Boolean(onOpenRecord) && ROUTABLE_CITATIONS.has(entityType),
  );
  // THIS sentence's citations, in the order it cites them, so the receipt's
  // prev/next walks the sentence the reader is actually looking at. The order
  // belongs to the sentence, which is why it is passed from here rather than
  // rebuilt in the drawer.
  // Mapped to the receipt's own shape here, at the one place that knows both:
  // the wire is snake_case and the drawer's CitedRecord is not.
  const siblings = evidence
    .filter((each) => RECEIPT_CITATIONS.has(each.entity_type))
    .map((each) => ({
      entityType: each.entity_type as "fact" | "profile_field",
      entityId: each.entity_id,
    }));
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
            onClick={() =>
              onOpenRecord?.(chip.entityType, chip.entityId, siblings)
            }
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

// The citation kinds a reader can open something for. `deal` and `person` route
// to their own screens; `fact` and `profile_field` open their receipt instead —
// where the value came from, when it was read, and what could not be recorded.
//
// An activity has no detail route of its own (it lives in a timeline) and no
// receipt either, and the organization citation is the page the reader is
// already on. Both stay flat: a clickable element that does nothing teaches the
// reader that citations do not work, which costs more than the click it saves.
const ROUTABLE_CITATIONS = new Set(["deal", "person", "fact", "profile_field"]);

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
export function SentenceList({
  sentences,
  onOpenRecord,
}: Readonly<{
  sentences: BriefSentence[];
  onOpenRecord?: (entityType: string, entityId: string) => void;
}>) {
  const t = useT();
  return (
    <ul className="co-brief-lines">
      {sentences.map((sentence, index) => (
        // Indexed because two sentences may legitimately read the same;
        // keying on the text collapses them into one row.
        // biome-ignore lint/suspicious/noArrayIndexKey: the list is replaced wholesale on every read, never reordered in place
        <li key={index}>
          {/* What KIND of claim this is, marked where it is made. A judgment
              that looked like a stored fact would be the one thing a reader
              could not check — and the brief is allowed to judge now. */}
          {sentence.nature && sentence.nature !== "fact" && (
            <Badge
              tone={sentence.nature === "recommendation" ? "accent" : undefined}
            >
              {t(NATURE_LABELS[sentence.nature])}
            </Badge>
          )}{" "}
          {sentence.text}
          <Citations evidence={sentence.evidence} onOpenRecord={onOpenRecord} />
        </li>
      ))}
    </ul>
  );
}

export type BriefSentence = NonNullable<
  Brief["sections"]
>[number]["sentences"][number];
type BriefSectionKind = NonNullable<Brief["sections"]>[number]["kind"];

const NATURE_LABELS: Record<
  NonNullable<BriefSentence["nature"]>,
  MessageKey
> = {
  fact: "co.brief.nature.fact",
  assessment: "co.brief.nature.assessment",
  recommendation: "co.brief.nature.recommendation",
};

const SECTION_LABELS: Record<BriefSectionKind, MessageKey> = {
  snapshot: "co.brief.section.snapshot",
  fit: "co.brief.section.fit",
  health: "co.brief.section.health",
  activity: "co.brief.section.activity",
  next_step: "co.brief.section.next_step",
};

/**
 * BriefSections renders the brief under the questions it answers. A section
 * with nothing to say never arrives, so there is no empty heading to draw —
 * a heading over silence reads as a finding of nothing.
 */
function BriefSections({
  sections,
  onOpenRecord,
}: Readonly<{
  sections: NonNullable<Brief["sections"]>;
  onOpenRecord?: (entityType: string, entityId: string) => void;
}>) {
  const t = useT();
  return (
    <>
      {sections.map((section) => (
        <div key={section.kind} className="co-brief-section">
          <h3 className="co-brief-section-label t-caption">
            {t(SECTION_LABELS[section.kind])}
          </h3>
          <SentenceList
            sentences={section.sentences}
            onOpenRecord={onOpenRecord}
          />
        </div>
      ))}
    </>
  );
}

/**
 * WrittenBy names which writer produced a piece of prose. Always shown: a
 * reader weighing a sentence needs to know whether a model or the
 * deterministic fallback wrote it, and the two are not interchangeable.
 */
export function WrittenBy({ by }: Readonly<{ by: Brief["generated_by"] }>) {
  const t = useT();
  return (
    <Badge tone={by === "model" ? "ai" : undefined}>
      {t(`co.brief.by.${by}`)}
    </Badge>
  );
}

/**
 * AccountBrief is what a rep reads before they do anything else on this page:
 * where this account stands with us, then what the company itself is.
 *
 * It replaces reading the record. The page used to answer "what is this
 * company" with sixteen scraped statements in a rail card, every value a
 * paragraph — a wall nobody reads before a call. The same statements now feed
 * two sentences here and stay underneath for whoever wants them.
 *
 * Fetched on open, not on request. The server rewrites a brief whose inputs
 * have moved before it answers, so what renders is always current and an
 * account nobody has touched costs no model call at all. "Refresh" is for a
 * reader who wants it rewritten anyway.
 */
export function AccountBrief({
  orgId,
  view,
  enabled,
  onOpenRecord,
  onPerform,
}: Readonly<{
  orgId: string;
  // The 360 the page already holds. The brief itself is written server-side;
  // this is for the two things it cannot write — what to DO next, and whether
  // any of the account was withheld from this reader.
  view?: Organization360;
  enabled: boolean;
  onOpenRecord?: (entityType: string, entityId: string) => void;
  onPerform?: (action: SuggestionAction) => void;
}>) {
  const t = useT();
  const { locale } = useLocale();
  const queryClient = useQueryClient();
  const brief = useQuery({
    queryKey: ["org-brief", orgId],
    enabled,
    queryFn: async () => {
      const { data, error } = await api.GET("/organizations/{id}/brief", {
        params: { path: { id: orgId } },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });
  const rewrite = useMutation({
    mutationFn: async () => {
      const { data, error } = await api.POST("/organizations/{id}/brief", {
        params: { path: { id: orgId } },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    onSuccess: (data) => queryClient.setQueryData(["org-brief", orgId], data),
  });

  if (!enabled) {
    return null;
  }
  const written: Brief | undefined = brief.data;
  // A payload without sentences is a brief this build cannot read, not an
  // account with nothing to say — the same distinction every card here keeps.
  const readable = Array.isArray(written?.sections) ? written : undefined;
  return (
    <section className="co-part co-brief" aria-label={t("co.brief.title")}>
      <h2 className="co-part-label">{t("co.brief.title")}</h2>
      {brief.isPending && <Skeleton width="100%" height={64} />}
      {/* Errored, or answered with a payload this build cannot read: both are
          "no brief to show", and rendering the heading over nothing would be a
          card that looks broken rather than one that says so. */}
      {(brief.isError || (!brief.isPending && !readable)) && (
        <EmptyState>{t("co.brief.unavailable")}</EmptyState>
      )}
      {readable && readable.sections.length === 0 && (
        <EmptyState>{t("co.brief.empty")}</EmptyState>
      )}
      {readable && readable.sections.length > 0 && (
        <BriefSections
          sections={readable.sections}
          onOpenRecord={onOpenRecord}
        />
      )}
      {readable && (
        <p className="co-brief-meta">
          {/* Who wrote it and when, always — a reader weighing a sentence
              needs both, and an undated summary is one nobody can trust. */}
          <WrittenBy by={readable.generated_by} />
          <span className="t-small">
            {t("co.brief.generatedAt", {
              when: formatDateTime(readable.generated_at, locale, RECORD_ZONE),
            })}
          </span>
          <Button
            small
            onClick={() => rewrite.mutate()}
            disabled={rewrite.isPending}
          >
            {rewrite.isPending
              ? t("co.brief.rewriting")
              : t("co.brief.rewrite")}
          </Button>
        </p>
      )}
      {rewrite.isError && (
        <p className="t-caption form-error">
          {problemMessageOf(rewrite.error, t)}
        </p>
      )}
      {/* What to do about it, in the same block that said what it is. These
          were two cards — one describing the account, one advising on it —
          so the reader carried the reading from the first into the second
          themselves. */}
      {view && (
        <SuggestionsSection
          orgId={orgId}
          view={view}
          onOpenRecord={onOpenRecord}
          onPerform={onPerform}
        />
      )}
      <BriefFooter view={view} />
    </section>
  );
}

// BriefFooter is the reading's own caveats: what moved while the reader was
// away, and whether any of the account was withheld from them. Split out
// because it answers questions ABOUT the brief rather than being part of it.
function BriefFooter({ view }: Readonly<{ view?: Organization360 }>) {
  const t = useT();
  const since = sinceLastVisit(view);
  return (
    <p className="co-prep-foot">
      {/* Never both: on a first open the server counts every activity as new,
          and "14 new items" beside "you are opening this account for the first
          time" is the page contradicting itself. */}
      {firstVisit(view) && (
        <span className="t-caption">{t("co.since.first")}</span>
      )}
      {!firstVisit(view) && since > 0 && (
        <span className="t-caption">
          {t(
            since === 1 ? "co.read.newActivityOne" : "co.read.newActivityMany",
            { count: since },
          )}
        </span>
      )}
      {/* Withheld sections are named once, about the whole reading, rather
          than as a refusal beside each line the reader did not get. */}
      {(view?.sections_omitted?.length ?? 0) > 0 && (
        <span className="t-caption">{t("co.prep.withheld")}</span>
      )}
    </p>
  );
}

// sinceLastVisit is how many activities landed since the reader's baseline.
//
// Zero and "not counted" are different answers and neither earns a line: a
// withheld section means nobody counted, and a counted zero means nothing
// happened — reporting either as news would be a claim the page cannot make.
function sinceLastVisit(view?: Organization360): number {
  if (!view || (view.sections_omitted ?? []).includes("since_last_visit")) {
    return 0;
  }
  return view.since_last_visit?.new_activities ?? 0;
}

// firstVisit is true only when the account HAS a baseline section and it is
// empty. Read off an absent section it would turn data a reader's grants
// withheld into a claim about their own history.
function firstVisit(view?: Organization360): boolean {
  if (!view || (view.sections_omitted ?? []).includes("since_last_visit")) {
    return false;
  }
  return Boolean(view.since_last_visit) && !view.since_last_visit?.baseline_at;
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
        throwProblem(error);
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
          {` ${problemMessageOf(ask.error, t)}`}
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
type Health = NonNullable<Organization360["health"]>;

/**
 * HealthCard is how the relationship stands, in the parts a reader can act on
 * (AC-company-3).
 *
 * It replaced a single 0–100 score. That number was the MAX over the account's
 * contacts of a decayed message count, so one talkative contact spoke for the
 * whole account and a long, low-volume relationship read as near-dead. Each
 * line here names a fact instead: "no inbound for 90 days" says what to do,
 * where "2/100" said only a mood.
 *
 * A part the server could not compute is ABSENT, never zero. Zero is a claim
 * about the account; absence is a fact about the reading.
 */
export function HealthCard({ health }: Readonly<{ health?: Health }>) {
  const t = useT();
  if (!health) {
    return null;
  }
  const lines: string[] = [];
  if (health.days_since_last_inbound != null) {
    lines.push(
      t("co.health.sinceInbound", { days: health.days_since_last_inbound }),
    );
  }
  if (health.reply_balance != null) {
    lines.push(
      t("co.health.replyBalance", {
        percent: Math.round(health.reply_balance * 100),
      }),
    );
  }
  if (health.active_contacts != null) {
    lines.push(
      t("co.health.activeContacts", { count: health.active_contacts }),
    );
  }
  if (health.open_commitments != null && health.open_commitments > 0) {
    lines.push(
      t("co.health.openCommitments", { count: health.open_commitments }),
    );
  }
  if (lines.length === 0) {
    return null;
  }
  return (
    <SectionCard
      title={t("co.health.title")}
      state="ready"
      // Never reached: the card returns null when it has no line to draw,
      // because "how it stands: nothing" is not a reading of an account.
      emptyLabel={t("co.health.title")}
    >
      <ul className="co-list">
        {lines.map((line) => (
          <li key={line} className="co-row">
            {line}
          </li>
        ))}
      </ul>
      {/* The one shape a rep can fix before it costs them the account, so it
          is said rather than scored. */}
      {health.single_threaded && (
        <p className="co-row-meta">
          <Badge tone="warn">{t("co.health.singleThreaded")}</Badge>
        </p>
      )}
    </SectionCard>
  );
}

type StateStrip = NonNullable<Organization360["state_strip"]>;

const ENGAGEMENT_LABELS: Record<
  NonNullable<StateStrip["engagement"]>["state"],
  MessageKey
> = {
  never_contacted: "co.strip.engagement.never_contacted",
  active: "co.strip.engagement.active",
  waiting_on_them: "co.strip.engagement.waiting_on_them",
  waiting_on_us: "co.strip.engagement.waiting_on_us",
  dormant: "co.strip.engagement.dormant",
};

// The two states that name a problem rather than a condition. Colouring only
// these keeps the strip from reading as a dashboard where every tile is lit.
const ENGAGEMENT_TONE: Partial<
  Record<NonNullable<StateStrip["engagement"]>["state"], "warn">
> = {
  waiting_on_them: "warn",
  dormant: "warn",
};

/**
 * StateStrip is the three readings the overview leads with (AC-company-13):
 * where the account stands, whose move it is, and what commercial work is open.
 *
 * Each half is drawn only when the server answered it. A null engagement means
 * the caller may not read the account's mail, and inventing "never contacted"
 * from that would state a business conclusion the page has no basis for — the
 * one a rep would act on.
 */
// The compact KPI row (plan §4.2): at most four cards, and WHICH four depends
// on where the account stands. A customer is asked about money and health; a
// prospect is asked about pipeline, timing and fit. Showing one set to both
// makes half of them noise.
//
// What it must never render is the harder half of the rule, and every omission
// below is one of its bullets: no €0 when the figure is unavailable, no
// cross-currency sum without its conversion source, and nothing called
// "revenue" that is only a count of open deals.
export function StateStrip({
  view,
  lifecycleLabel,
  relationshipLabels,
}: Readonly<{
  view?: Organization360;
  lifecycleLabel: (value: string) => string;
  relationshipLabels: (values: readonly string[]) => string;
}>) {
  const t = useT();
  const { locale } = useLocale();
  const strip = view?.state_strip;
  if (!strip) {
    return null;
  }
  const types = strip.account.relationship_types ?? [];
  // A CURRENT customer only. A former one has invoices in its past, but "do
  // they pay us, and on time?" is not the question their page is opened with,
  // and leading with a money reading on an account that has stopped buying
  // reads as though the relationship were still running.
  const customer = strip.account.lifecycle === "customer";
  return (
    <section className="co-strip" aria-label={t("co.strip.title")}>
      {/* Four cards is the cap (§4.2). On a CUSTOMER the two money readings
          lead, because "do they pay us, and on time?" is the question a
          customer page is opened with — on everyone else there is no such
          question and the account's own state leads instead. */}
      {/* Where the account stands holds the first slot on every page. It is
          the one reading that is true of every company, and a page that leads
          with money on some accounts and with state on others gives the reader
          no fixed place to look. */}
      <StatCard
        label={t("co.strip.account")}
        value={lifecycleLabel(strip.account.lifecycle)}
        detail={types.length > 0 ? relationshipLabels(types) : undefined}
      />
      {/* On a customer the second slot is money — "do they pay us?" is the
          question their page is opened with, and it is the slot the mockup's
          State D gives to net invoiced. On everyone else there are no invoices
          to ask about and the question is when the next deal lands. */}
      {customer ? (
        <FinanceStat reading="netInvoiced" t={t} />
      ) : (
        <PipelineCard commercial={strip.commercial} locale={locale} t={t} />
      )}
      {customer ? (
        <HealthStat health={view?.health} t={t} />
      ) : (
        <CloseDateStat commercial={strip.commercial} locale={locale} t={t} />
      )}
      {/* The signal, when one is open, takes the last slot rather than adding
          a fifth — the worst thing standing open is the more urgent reading. */}
      {strip.signal ? (
        <StatCard
          label={t("co.strip.signal")}
          value={signalKindLabel(strip.signal.kind, t)}
          tone={signalTone(strip.signal.severity)}
          detail={strip.signal.summary}
        />
      ) : (
        <EngagementStat engagement={strip.engagement} locale={locale} t={t} />
      )}
    </section>
  );
}

/**
 * The two finance slots a customer's KPI row carries, in the honest-unknown
 * state (mockup State B).
 *
 * No finance source is connected to this installation yet — the ingestion
 * module lands separately — so the value is a dash and the detail line says
 * what would make it a number. It is NOT €0 and NOT "pays on time": both are
 * conclusions about the customer drawn from the absence of a connector, which
 * is the one thing §6 State B forbids outright.
 *
 * The slot is rendered rather than omitted on purpose. A customer page that
 * simply has no money reading tells a reader nothing is missing; this one
 * tells them what to connect.
 */
function FinanceStat({
  reading,
  t,
}: Readonly<{
  reading: "netInvoiced" | "openInvoices";
  t: ReturnType<typeof useT>;
}>) {
  return (
    <StatCard
      label={t(`co.strip.${reading}`)}
      value={t("co.strip.financeUnknown")}
      detail={t("co.strip.connectFinance")}
    />
  );
}

type StripCommercial = NonNullable<
  NonNullable<Organization360["state_strip"]>["commercial"]
>;

// Open pipeline, labelled as exactly what it is: the sum of open deals, never
// "potential" and never "revenue" (§4.2). Absent when nothing on the account
// carries a convertible figure — a €0 there would claim a priced pipeline
// worth nothing, where the truth is the page cannot price it.
function PipelineCard({
  commercial,
  locale,
  t,
}: Readonly<{
  commercial?: StripCommercial | null;
  locale: Locale;
  t: ReturnType<typeof useT>;
}>) {
  if (!commercial) {
    return null;
  }
  // No open deals is not an unpriced pipeline. Saying "no convertible amount"
  // about an account that has nothing open reports a data problem where the
  // truth is simply that nothing is running.
  if (commercial.open_count === 0) {
    return (
      <StatCard
        label={t("co.strip.pipeline")}
        value={t("co.strip.noOpenDeals")}
      />
    );
  }
  const { open_pipeline_minor_base: value, base_currency: currency } =
    commercial;
  const stalled =
    commercial.stalled_count > 0
      ? t("co.strip.stalled", { count: commercial.stalled_count })
      : undefined;
  if (value == null || !currency) {
    // Open deals with no priceable figure still say how many there are: the
    // count is a fact, the money is not. The unpriced note is never dropped
    // for the stalled one — a reader who is told only "1 stalled" has no way
    // to know the pipeline was never priced at all.
    return (
      <StatCard
        label={t("co.strip.pipeline")}
        value={t("co.strip.openDeals", { count: commercial.open_count })}
        detail={join(t("co.strip.unpriced"), stalled)}
        tone={stalled ? "warn" : undefined}
      />
    );
  }
  // Everything qualifying this figure travels WITH it. §4.2 forbids a
  // cross-currency sum without an explicit conversion source and as-of date,
  // and forbids a total that silently covers only part of the pipeline — so a
  // partial total names its share, and a converted one names the oldest rate
  // date standing behind it.
  const partial = commercial.priced_count < commercial.open_count;
  const converted =
    commercial.converted_count > 0 && commercial.fx_as_of
      ? t("co.strip.convertedAsOf", {
          count: commercial.converted_count,
          date: formatDate(commercial.fx_as_of, locale, RECORD_ZONE),
        })
      : undefined;
  return (
    <StatCard
      label={t("co.strip.pipeline")}
      value={formatMoney(value, currency, locale)}
      tone={stalled ? "warn" : undefined}
      detail={join(
        partial
          ? t("co.strip.pricedPartly", {
              priced: commercial.priced_count,
              total: commercial.open_count,
            })
          : t("co.strip.openDeals", { count: commercial.open_count }),
        converted,
        stalled,
      )}
    />
  );
}

// One detail line from the parts that apply. A card has room for one, and
// dropping a part because another is present is how a qualification goes
// missing exactly when it matters.
function join(...parts: (string | undefined)[]): string {
  return parts.filter(Boolean).join(" · ");
}

// When the next open deal is expected to close. A prospect's page is asked
// "when?", and the answer is a date on a record rather than an assessment.
function CloseDateStat({
  commercial,
  locale,
  t,
}: Readonly<{
  commercial?: StripCommercial | null;
  locale: Locale;
  t: ReturnType<typeof useT>;
}>) {
  if (!commercial?.next_close_on) {
    return null;
  }
  return (
    <StatCard
      label={t("co.strip.expectedClose")}
      value={formatDate(commercial.next_close_on, locale, RECORD_ZONE)}
    />
  );
}

// Health as a STATUS with its reason, never a 0-100 verdict (§4.2). The card
// below the fold decomposes it; this says which way it points and why.
//
// It reports the BALANCE of the exchange rather than its recency, because the
// engagement card beside it already answers "whose move is it" — two cards
// saying "in conversation" in different words is one card's worth of
// information taking two of the four slots. A relationship where they write
// and we do not answer, and one where we write into silence, are both
// "in conversation" by recency and are opposite problems.
function HealthStat({
  health,
  t,
}: Readonly<{ health?: Health; t: ReturnType<typeof useT> }>) {
  if (!health) {
    return null;
  }
  const days = health.days_since_last_inbound;
  if (days == null) {
    return (
      <StatCard
        label={t("co.strip.health")}
        value={t("co.strip.noInboundEver")}
        tone="warn"
      />
    );
  }
  if (days > HEALTH_QUIET_DAYS) {
    return (
      <StatCard
        label={t("co.strip.health")}
        value={t("co.strip.healthQuiet")}
        tone="warn"
        detail={t("co.health.sinceInbound", { days })}
      />
    );
  }
  // A live relationship: say who is carrying it. Below a third of the
  // exchange coming from them is us talking to ourselves, whatever the dates
  // say; above two thirds they are asking more than we are answering.
  const share = health.reply_balance;
  if (share == null) {
    return (
      <StatCard
        label={t("co.strip.health")}
        value={t("co.strip.healthActive")}
      />
    );
  }
  const percent = Math.round(share * 100);
  const oneSided = share < 0.34 || share > 0.66;
  return (
    <StatCard
      label={t("co.strip.health")}
      value={
        oneSided ? t("co.strip.healthOneSided") : t("co.strip.healthBalanced")
      }
      tone={oneSided ? "warn" : undefined}
      detail={t("co.strip.replyShare", { percent })}
    />
  );
}

// The threshold that separates a live conversation from a quiet one. It names
// a number the strip states rather than one the reader must infer from a date,
// and it is deliberately the same span the dormant engagement state uses.
const HEALTH_QUIET_DAYS = 30;

function EngagementStat({
  engagement,
  locale,
  t,
}: Readonly<{
  engagement?: NonNullable<Organization360["state_strip"]>["engagement"];
  locale: Locale;
  t: ReturnType<typeof useT>;
}>) {
  if (!engagement) {
    return null;
  }
  const when = (at?: string | null) =>
    at ? formatDate(at, locale, RECORD_ZONE) : undefined;
  return (
    <StatCard
      label={t("co.strip.engagement")}
      value={t(ENGAGEMENT_LABELS[engagement.state])}
      tone={ENGAGEMENT_TONE[engagement.state]}
      detail={
        engagement.last_inbound_at || engagement.last_outbound_at
          ? t("co.strip.lastBoth", {
              inbound: when(engagement.last_inbound_at) ?? t("co.strip.never"),
              outbound:
                when(engagement.last_outbound_at) ?? t("co.strip.never"),
            })
          : undefined
      }
    />
  );
}

// What performing a suggestion means. The server names it; this maps the name
// to the words on the button.
export type SuggestionAction = NonNullable<Suggestion["action"]>;

const SUGGESTION_ACTION_LABELS: Record<SuggestionAction["kind"], MessageKey> = {
  draft_reply: "co.suggest.act.draftReply",
  open_deal: "co.suggest.act.openDeal",
  add_task: "co.suggest.act.addTask",
};

// SuggestionActionButton exists so the action is narrowed ONCE, at the call
// site, rather than re-narrowed inside a callback where TypeScript has already
// lost it.
function SuggestionActionButton({
  action,
  onPerform,
}: Readonly<{
  action: SuggestionAction;
  onPerform: (action: SuggestionAction) => void;
}>) {
  const t = useT();
  return (
    <Button small onClick={() => onPerform(action)}>
      {t(SUGGESTION_ACTION_LABELS[action.kind])}
    </Button>
  );
}

export function SuggestionsSection({
  orgId,
  view,
  onOpenRecord,
  onPerform,
}: Readonly<{
  orgId: string;
  view?: Organization360;
  onOpenRecord?: (entityType: string, entityId: string) => void;
  // Performing the advice is the page's job, not this card's: the composer,
  // the deal and the task form all live above it.
  onPerform?: (action: SuggestionAction) => void;
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
        throwProblem(error);
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
              {/* What performing the advice means, named by the server. A rule
                  that could not name one carries null and this renders nothing
                  rather than a control that does nothing. */}
              {suggestion.action && onPerform && (
                <SuggestionActionButton
                  action={suggestion.action}
                  onPerform={onPerform}
                />
              )}
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
          {` ${problemMessageOf(dismiss.error, t)}`}
        </p>
      )}
    </section>
  );
}
