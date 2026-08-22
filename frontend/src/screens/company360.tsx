import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { type ReactNode, useEffect, useId, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { navigate, routeHash } from "../app/router";
import {
  Avatar,
  Badge,
  Button,
  Card,
  EmptyState,
  Field,
  Modal,
  Skeleton,
  StatCard,
} from "../design-system/atoms";
import { AvatarStack } from "../design-system/avatarstack";
import { type TimelineEntry, TimelineRow } from "../design-system/composed";
import { EvidenceMark } from "../design-system/evidencemark";
import { Eyebrow } from "../design-system/eyebrow";
import { Panel, PanelBody, PanelRow } from "../design-system/panel";
import {
  liveProjects,
  type PickableProject,
  ProjectPicker,
  useSoleProjectDefault,
} from "../design-system/projectpicker";
import { Select } from "../design-system/select";
import { StatStrip } from "../design-system/statstrip";
import {
  omitted,
  type SectionDetail,
  type SectionState,
  SurfaceState,
  sectionState,
} from "../design-system/surfacestate";
import {
  formatDate,
  formatDateTime,
  formatMoney,
  formatMoneyCompact,
  formatMoneyOrAbsent,
} from "../format/format";
import { RECORD_ZONE, viewerZone } from "../format/timezone";
import { type Locale, useLocale, useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import {
  problemCodeOf,
  problemMessageOf,
  throwProblem,
  useFinanceSummary,
  useViewerId,
} from "./common";
import "./company360.css";
import { activityTimeline } from "../design-system/activitytimeline";
import {
  HEALTH_RANK,
  HEALTH_RATING_LABEL,
  type HealthRating,
  usePaymentHealth,
} from "./companylookups";
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
import { TaskCompleteCheck, type useTaskUpdate } from "./taskactions";

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
  project_gone_quiet: "signal.kind.project_gone_quiet",
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
// finding something on Object's prototype and typing as a tone. Exported so
// the daily brief's risk reading (companytoday.tsx) colours its tile the same
// way the strip used to, rather than a second mapping that could drift.
export function signalTone(severity: string): "warn" | "danger" | undefined {
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
// OVERLAY_REFUSAL is the validation code the 360 answers for a workspace
// reading from an incumbent mirror. It is a refusal to assemble, not a
// failure, so the screen falls back instead of showing an error.
const OVERLAY_REFUSAL = "unsupported_in_overlay_mode";

export type Org360Result =
  | { state: "ready"; view: Organization360 }
  | { state: "overlay" };

/**
 * useOrganization360 reads the whole company page in one round trip.
 *
 * `enabled` exists for callers that are not the page: chrome mounted on every
 * screen has to hold the hook unconditionally and ask for nothing when there is
 * no record under it — an empty id is a 422, not an empty answer.
 */
export function useOrganization360(id: string, enabled = true) {
  return useQuery<Org360Result>({
    queryKey: ["organization360", id],
    enabled: enabled && id !== "",
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

// The coverage line the mockup draws above the contact rows: how many
// contacts, how many nobody has written to, how many roles are unfilled.
//
// ONLY counts this page can total. The mockup also carries "11 connected
// colleagues", which would mean summing each contact's routes — and those are
// capped at the top three by strength, so the sum would count the same
// colleague once per contact they know and report a bigger team than exists.
// A number nobody can check is worse than a shorter line.
//
// Silent when the contact list was truncated: "7 contacts" off a capped page
// is a count of the page, not of the account.
function CoverageSummary({
  contacts,
  untried,
  gaps,
  truncated,
  routesReadable,
}: Readonly<{
  contacts: readonly Contact[];
  untried: number;
  gaps: number;
  truncated: boolean;
  // Whether the routes this page read carry an answer at all. "Never written
  // to" is derived from who has exchanged messages with whom, so a reader
  // without that access must not be handed the aggregate — an activity-derived
  // count is still activity data.
  routesReadable: boolean;
}>) {
  const t = useT();
  if (contacts.length === 0) {
    return null;
  }
  // A truncated page still knows how many contacts it is showing, and saying
  // "at least 25" beats saying nothing: the reader learns both that the
  // account is large and that this is not the whole of it.
  const parts = [
    truncated
      ? t("co.coverage.contactsAtLeast", { count: contacts.length })
      : t("co.coverage.contacts", { count: contacts.length }),
  ];
  if (routesReadable && untried > 0) {
    parts.push(t("co.coverage.untried", { count: untried }));
  }
  // The gap count is only meaningful over a complete picture: a capped page
  // hides the contacts who might hold the roles it would report as missing.
  if (!truncated && gaps > 0) {
    parts.push(t("co.coverage.gaps", { count: gaps }));
  }
  return <p className="surfacestate-empty">{parts.join(" · ")}</p>;
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
    <Card className="co-card" title={title}>
      <SurfaceState state={state} emptyLabel={emptyLabel} detail={detail}>
        {children}
      </SurfaceState>
      {present && footer}
      {present && actions && <div className="co-card-actions">{actions}</div>}
    </Card>
  );
}

/**
 * RailPanel is SectionCard's four-state discipline rendered through Panel's
 * chrome — a fixed-height header and full-bleed rows — instead of the
 * negative-margin CSS breakout that shape used to need. The message states
 * (empty, withheld, unavailable, loading, failed) reuse SurfaceState verbatim,
 * padded in a PanelBody; `ready` is left to the caller, so rows passed as
 * children run edge to edge the way Panel is built to take them.
 *
 * Scoped to the rail's own cards — SectionCard itself is untouched, because
 * its other callers (the grid, the other tabs) are not this card's chrome.
 */
export function RailPanel({
  title,
  state,
  emptyLabel,
  detail,
  footer,
  children,
}: Readonly<{
  title: string;
  state: SectionState;
  emptyLabel: string;
  detail?: SectionDetail;
  // A figure belonging to the whole card rather than to one row. Shown only
  // on `ready`/`empty` — the states RailPanel's callers ever reach — because a
  // withheld or unavailable section has no figure to report either.
  footer?: ReactNode;
  children: ReactNode;
}>) {
  const present = state === "ready" || state === "empty";
  return (
    <Panel title={title} footer={present ? footer : undefined}>
      {state === "ready" ? (
        children
      ) : (
        <PanelBody>
          <SurfaceState state={state} emptyLabel={emptyLabel} detail={detail}>
            {null}
          </SurfaceState>
        </PanelBody>
      )}
    </Panel>
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
  // The composite read's own pending flag, so an undefined `view` reads as
  // "still loading" rather than "could not be loaded" for as long as the
  // read is actually running.
  loading = false,
}: Readonly<{
  view?: Organization360;
  writable?: boolean;
  orgId?: string;
  loading?: boolean;
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
    <RailPanel
      title={t("co.people.title")}
      state={sectionState(
        view,
        "people",
        Boolean(view?.people),
        contacts.length,
        loading,
      )}
      emptyLabel={t("co.people.empty")}
      footer={
        <>
          {contacts.length > 0 && (
            <AvatarStack
              people={contacts.map((contact) => ({
                name: contact.full_name,
              }))}
            />
          )}
          <CoverageSummary
            contacts={contacts}
            untried={untried.length}
            gaps={missing.length}
            truncated={truncated}
            routesReadable={contacts.some((each) => each.routes)}
          />
          {/* The per-row coverage says who to call. The explorer answers the
              other question — where are we thin — for a handful of
              colleagues the reader picks, rather than a column per person
              on a forty-strong team. */}
          {orgId && contacts.length > 0 && (
            <CoverageExplorer orgId={orgId} contacts={contacts} />
          )}
        </>
      }
    >
      {/* The per-contact chips read as all-time claims — "Not approached"
          above a timeline showing last year's outbound email is the page
          arguing with itself. They are computed over the server's 90-day
          window (PO-F-3), so the window is stated once here rather than
          repeated on every row. */}
      {contacts.length > 0 && (
        <PanelBody>
          <p className="t-caption">{t("co.reach.window")}</p>
        </PanelBody>
      )}
      {contacts.map((contact) => (
        <PanelRow key={contact.person_id} className="co-person-row">
          <ContactRow
            contact={contact}
            openDeals={openDeals}
            writable={writable}
            orgId={orgId}
          />
        </PanelRow>
      ))}
      {contacts.length === 1 && !truncated && (
        <PanelBody>
          <p className="co-callout">
            <Badge tone="warn">{t("co.people.singleThread")}</Badge>
          </p>
        </PanelBody>
      )}
      {/* Who is missing, not only who is present. On an account where every
          known contact has gone quiet, the person nobody has written to is the
          only move left that is not a fourth follow-up. */}
      {(untried.length > 0 || missing.length > 0) && (
        <PanelBody>
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
                  roles: missing
                    .map((role) => t(roleLabelKey(role)))
                    .join(" / "),
                })}
              </Badge>
            </p>
          )}
        </PanelBody>
      )}
    </RailPanel>
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
          // Not sent: the flag is an EMPLOYMENT fact and this edge is a deal
          // stakeholder, so the literal said nothing and only stood in the way
          // of a gate that can now assert no screen states it by hand.
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
            <Button onClick={() => setOpen(false)}>{t("common.close")}</Button>
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
  // Title and role read as one quiet line under the name, not as a run of
  // badges — "Sales Manager · Champion on Pilot" is one fact about who this
  // person is, and a badge per clause said the same thing louder than it
  // needed to.
  // A purchased title fills the gap where nobody typed one (PO-EXT-9). The
  // server decides that — it sends provider_title only where the canonical
  // one is empty — so this reads the field rather than re-deciding the
  // precedence, which is how the two would come to disagree.
  const subline = [
    contact.title ?? contact.provider_title,
    ...roles.map((entry) => {
      const deal = nameOfDeal(entry.deal_id);
      return deal
        ? `${dealRoleLabel(entry.role, t)} · ${deal}`
        : dealRoleLabel(entry.role, t);
    }),
  ]
    .filter(Boolean)
    .join(" · ");
  return (
    <>
      <span className="co-person-avatar">
        <Avatar name={contact.full_name} />
      </span>
      <span className="co-person-body">
        {/* A real href, and it covers the whole row: a name is a small target
            beside the face and the badges that describe the same person, and
            a reader aiming at any of them means "open this contact". The
            verbs at the row's end sit above the cover, so they still act on
            the row rather than opening it. */}
        <a
          className="co-rowlink co-person-name co-rowcover"
          href={routeHash({ screen: "contacts", id: contact.person_id })}
        >
          {contact.full_name}
        </a>
        {subline && (
          <span className="co-person-sub">
            {/* ONE mark, on the whole line, and only where the title was
                bought. A mark per clause would turn a quiet subline into a
                run of badges, which is what this line was written to avoid. */}
            {contact.title_source === "provider" ? (
              <EvidenceMark
                value={subline}
                source={{
                  provenance: { kind: "connector", connector: "provider" },
                }}
              />
            ) : (
              subline
            )}
          </span>
        )}
      </span>
      <span className="co-person-end">
        {/* Where this person stands with us. "No reply" and "never asked"
            looked identical in this list and call for opposite next moves. */}
        <Badge tone={reach === "answered" ? "success" : undefined}>
          {t(reachLabelKey(reach))}
        </Badge>
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
    </>
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
            <p className="surfacestate-withheld">
              {t("co.section.unavailable")}
            </p>
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
  extra,
  loading = false,
}: Readonly<{
  view?: Organization360;
  // The verbs that change this section, rendered under it. Absent on an
  // archived record, which takes no new deals.
  actions?: ReactNode;
  // Whatever else belongs beside this account's deals — the Deals tab hands
  // in the last offer read here rather than drawing it as a second card, so
  // the two readings that both start from "this account's open deals" stop
  // reading as two different sections.
  extra?: ReactNode;
  // The composite read's own pending flag — see sectionState's own doc. The
  // Deals tab already gates its own skeleton on `!view && !failed` before
  // this ever renders, so `view` is always defined by the time this call
  // runs; passed anyway so the card is correct on its own terms rather than
  // depending on a caller's guard it cannot see.
  loading?: boolean;
}>) {
  const t = useT();
  const { locale } = useLocale();
  const deals = view?.deals;
  const won = deals?.won_lifetime;
  const state = sectionState(
    view,
    "deals",
    Boolean(deals),
    deals?.data.length ?? 0,
    loading,
  );
  const present = state === "ready" || state === "empty";
  return (
    <Panel
      title={t("co.deals.title")}
      titleAction={present ? actions : undefined}
      footer={
        deals && (
          <p className="co-row-meta">
            <span>
              {t("co.deals.wonLifetime")}{" "}
              {formatMoneyOrAbsent(won?.amount_minor, won?.currency, locale)}
            </span>
            <span>{t("co.deals.lostCount", { count: deals.lost_count })}</span>
          </p>
        )
      }
    >
      {present ? (
        <>
          {(deals?.data ?? []).map((deal) => (
            <DealRow key={deal.deal_id} deal={deal} />
          ))}
          {state === "empty" && (
            <PanelBody>
              <p className="surfacestate-empty">{t("co.deals.empty")}</p>
            </PanelBody>
          )}
          {extra}
        </>
      ) : (
        <PanelBody>
          <SurfaceState state={state} emptyLabel={t("co.deals.empty")}>
            {null}
          </SurfaceState>
        </PanelBody>
      )}
    </Panel>
  );
}

function DealRow({ deal }: Readonly<{ deal: Deal360 }>) {
  const t = useT();
  const { locale } = useLocale();
  return (
    <PanelRow className="co-row">
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
            {formatMoneyOrAbsent(
              deal.amount.amount_minor,
              deal.amount.currency,
              locale,
            )}
          </span>
        )}
        {deal.stalled && <Badge tone="warn">{t("deal.stalled")}</Badge>}
      </span>
    </PanelRow>
  );
}

/**
 * CommercialPanel is the overview's own reading of the pipeline: the two
 * lifetime figures the deals section actually carries, then the open deals
 * themselves. It is deliberately not DealsCard reused wholesale — the Deals
 * tab keeps that card in full, and this is the shorter reading a rep gets
 * without leaving Overview.
 *
 * No open-pipeline total is drawn: nothing in Organization360 sums the open
 * deals' amounts, and inventing one here would be exactly the fabricated
 * figure the deals section's own honesty rule forbids.
 */
export function CommercialPanel({
  view,
  titleAction,
  extra,
  onAllDeals,
  loading = false,
}: Readonly<{
  view?: Organization360;
  // The "new deal" verb, gated by the caller on the record being writable.
  titleAction?: ReactNode;
  // What else belongs to this account's commercial standing but is not read
  // off its deals — the overview hands in what it is already under contract
  // for, rather than a second card repeating "the commercial picture" under
  // its own heading.
  //
  // Rendered OUTSIDE the deals branch below, unlike DealsCard's slot of the
  // same name: the two readings answer to different grants, and a reader who
  // may see contracts and not deals would otherwise lose theirs to somebody
  // else's permission.
  extra?: ReactNode;
  onAllDeals?: () => void;
  // The composite read's own pending flag — see sectionState's own doc.
  loading?: boolean;
}>) {
  const t = useT();
  const { locale } = useLocale();
  const deals = view?.deals;
  const state = sectionState(
    view,
    "deals",
    Boolean(deals),
    deals?.data.length ?? 0,
    loading,
  );
  const present = state === "ready" || state === "empty";
  // The section is a page of `deals.data` with `has_more` beside it — past
  // the cap this reads as the whole open pipeline unless it says otherwise.
  const truncated = deals?.page.has_more === true;
  return (
    <Panel
      title={t("co.commercial.title")}
      titleAction={present ? titleAction : undefined}
      footer={
        present && (onAllDeals || truncated) ? (
          <>
            {truncated && (
              <p className="co-row-meta">{t("co.commercial.truncated")}</p>
            )}
            {onAllDeals && (
              <Button small variant="ghost" onClick={onAllDeals}>
                {t("co.commercial.allDeals")}
              </Button>
            )}
          </>
        ) : undefined
      }
    >
      {/* Before the pipeline, and before the panel's own deals footer: what
          the account is already signed for frames the deals that are still
          moving, and the Deals tab reads in that order too. */}
      {extra}
      {state === "ready" && deals ? (
        <>
          <PanelBody className="co-figures">
            <CommercialFigure
              label={t("co.deals.wonLifetime")}
              value={formatMoneyOrAbsent(
                deals.won_lifetime?.amount_minor,
                deals.won_lifetime?.currency,
                locale,
              )}
            />
            <CommercialFigure
              label={t("co.commercial.lostFigure")}
              value={String(deals.lost_count)}
            />
          </PanelBody>
          {deals.data.map((deal) => (
            <PanelRow key={deal.deal_id} className="co-commercial-row">
              <button
                type="button"
                className="co-rowlink co-commercial-name"
                onClick={() => navigate({ screen: "deals", id: deal.deal_id })}
              >
                <span className="co-commercial-title">{deal.name}</span>
                {deal.expected_close_date && (
                  <span className="co-commercial-sub">
                    {t("commercial.closes", {
                      when: formatDate(
                        deal.expected_close_date,
                        locale,
                        RECORD_ZONE,
                      ),
                    })}
                  </span>
                )}
              </button>
              <span className="co-row-meta">
                {deal.stage_name && <Badge>{deal.stage_name}</Badge>}
                {deal.amount?.amount_minor != null && (
                  <span className="t-mono">
                    {formatMoneyOrAbsent(
                      deal.amount.amount_minor,
                      deal.amount.currency,
                      locale,
                    )}
                  </span>
                )}
              </span>
            </PanelRow>
          ))}
        </>
      ) : (
        <PanelBody>
          <SurfaceState state={state} emptyLabel={t("co.deals.empty")}>
            {null}
          </SurfaceState>
        </PanelBody>
      )}
    </Panel>
  );
}

// One eyebrow-labelled figure. Shared shape with the finance panel, so the
// two read as the same kind of reading rather than two different cards that
// happen to sit near each other.
function CommercialFigure({
  label,
  value,
}: Readonly<{ label: string; value: string }>) {
  return (
    <div className="co-figure">
      <Eyebrow>{label}</Eyebrow>
      {/* A figure the page does not have still occupies its slot, as the
          absence its formatter returned: the reader sees WHICH reading is
          missing rather than a shorter row that reads as complete. */}
      <span className="co-figure-value">{value}</span>
    </div>
  );
}

// How many entries the overview's chronology carries. The full history is the
// History tab; this is "what happened lately" without leaving Overview.
const RECENT_ACTIVITY_LIMIT = 5;

// One run of the timeline under the day it happened. Consecutive rather than
// grouped-by-key, because `entries` arrives newest-first from the server and a
// day is never revisited later in the same page.
type ActivityDay = { key: string; entries: TimelineEntry[] };

function groupByDay(entries: readonly TimelineEntry[], locale: Locale) {
  const days: ActivityDay[] = [];
  for (const entry of entries) {
    const key = formatDate(entry.atIso, locale, RECORD_ZONE);
    const last = days.at(-1);
    if (last?.key === key) {
      last.entries.push(entry);
    } else {
      days.push({ key, entries: [entry] });
    }
  }
  return days;
}

/**
 * RecentActivityPanel is the overview's chronology: the same activities
 * section the rail used to carry, grouped under the day they happened rather
 * than as a flat list — a day is one thing that happened, several messages
 * are how it happened.
 *
 * Reads the SAME activities section the account's Suggestions and health
 * cards read, so the story here cannot disagree with what they cite.
 */
export function RecentActivityPanel({
  view,
  onOpenHistory,
  loading = false,
}: Readonly<{
  view?: Organization360;
  // Where the header's link leads. Absent for a caller with no History tab
  // of its own (the stories file).
  onOpenHistory?: () => void;
  // The composite read's own pending flag — see sectionState's own doc.
  loading?: boolean;
}>) {
  const t = useT();
  const { locale } = useLocale();
  const viewerId = useViewerId();
  // Every logged activity, not only the ones with a subject: a call or a note
  // often has none, and filtering them out here would under-report the
  // chronology and — because the count feeds sectionState — draw "nothing
  // logged with them yet" on an account that has been called five times.
  const logged = view?.activities?.data ?? [];
  const state = sectionState(
    view,
    "activities",
    Boolean(view?.activities),
    logged.length,
    loading,
  );
  const days = groupByDay(
    activityTimeline(logged.slice(0, RECENT_ACTIVITY_LIMIT), viewerId),
    locale,
  );
  return (
    <Panel
      title={t("co.recent.title")}
      titleAction={
        onOpenHistory && (
          <Button small variant="ghost" onClick={onOpenHistory}>
            {t("co.recent.viewHistory")}
          </Button>
        )
      }
    >
      {state === "ready" ? (
        days.map((day) => (
          <div key={day.key} className="co-timeline-day">
            <h3 className="co-timeline-day-heading t-eyebrow">{day.key}</h3>
            <ul className="timeline">
              {day.entries.map((entry) => (
                <TimelineRow key={entry.id} entry={entry} zone={RECORD_ZONE} />
              ))}
            </ul>
          </div>
        ))
      ) : (
        <PanelBody>
          <SurfaceState state={state} emptyLabel={t("co.recent.empty")}>
            {null}
          </SurfaceState>
        </PanelBody>
      )}
    </Panel>
  );
}

/**
 * NextSteps is the middle column's first block: the open tasks on this
 * account, overdue first, each showing what it is linked to.
 *
 * The tick is `update`'s own verb (`TaskCompleteCheck`), not `renderAction`'s
 * — a row names its primary move as the row, not as one more item in a menu.
 * `renderAction` is left for whatever ELSE a caller wants beside the tick
 * (snooze), and stays hidden until the row is hovered or focused, since a
 * list of open tasks reads by title and due date first and only reveals its
 * verbs on approach.
 */
export function NextSteps({
  view,
  renderAction,
  onOpenTask,
  update,
}: Readonly<{
  view: Organization360;
  renderAction?: (step: NextStep) => ReactNode;
  // Given, the subject opens the task where it is listed. Absent, it stays
  // plain text rather than a button that goes nowhere.
  onOpenTask?: (step: NextStep) => void;
  // Wires the tick to the real completion write. Absent (the stories file,
  // a read-only account) draws the row with no checkbox at all — a box that
  // cannot be ticked is worse than no box.
  update?: ReturnType<typeof useTaskUpdate>;
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
    <Panel title={t("co.next.title")}>
      {state === "unavailable" && (
        <PanelBody>
          <p className="surfacestate-withheld">{t("co.section.unavailable")}</p>
        </PanelBody>
      )}
      {state === "empty" && (
        <PanelBody>
          <p className="surfacestate-empty">{t("co.next.empty")}</p>
        </PanelBody>
      )}
      {state === "ready" &&
        steps.map((step) => (
          <PanelRow key={step.activity_id} className="co-task-row">
            {update && (
              <TaskCompleteCheck
                activityId={step.activity_id}
                update={update}
              />
            )}
            <span className="co-task-body">
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
                      // The one viewer-clock reading on this record page, and
                      // it is not a preference: `dueInstant` mints a due date
                      // as the end of the picked day in the BROWSER's zone, so
                      // the stored instant already carries the picker's clock.
                      // Read in the organization's zone it names a different
                      // calendar day than the one the picker chose, for every
                      // reader outside that zone — there is no organization
                      // reading of it to prefer. The timeline below still reads
                      // in RECORD_ZONE, because an activity's occurrence IS a
                      // fact about the record.
                      when: formatDate(step.due_at, locale, viewerZone()),
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
              </span>
            </span>
            {renderAction && (
              <span className="co-task-verbs">{renderAction(step)}</span>
            )}
          </PanelRow>
        ))}
    </Panel>
  );
}

type Cited = BriefSentence["evidence"][number];
type CitedKind = Cited["entity_type"];

/**
 * A citation chip as it is rendered: either one (or a counted GROUP of) an
 * openable record, or the count of records of one kind that have nowhere to
 * open.
 */
export type CitationChip =
  | {
      openable: true;
      entityType: CitedKind;
      entityId: string;
      count: number;
      // The record's own name, when the citation carried one — a deal's
      // name, an activity's subject. Absent on a grouped chip: a count
      // already speaks for several records, and one of their names would
      // read as though it spoke for the rest.
      name?: string;
    }
  | { openable: false; entityType: CitedKind; count: number };

/**
 * citationChips turns a sentence's raw evidence into what a reader should see.
 *
 * Three reductions, all of which the raw list gets wrong on its own. The same
 * record cited twice is one source, not two. Several records of a kind the app
 * cannot open are one statement about that kind — rendered one by one they
 * became a run of identical unopenable labels ("activity activity activity"),
 * which says nothing the count does not say better. And several RECEIPT
 * citations of the same kind (`groupable`) are one counted chip too, opening
 * the first and stepping through the rest — rendered one per record they
 * became the same run under a different reason: a receipt has no name of its
 * own, so ten profile fields all read "profile field", ten times, with nothing
 * to tell them apart. `deal`/`person` stay one chip per record: each opens its
 * OWN screen rather than a shared stepper, so collapsing them would silently
 * drop every record after the first.
 *
 * Order is first-seen, so the chips follow the sentence's own reasoning.
 */
export function citationChips(
  evidence: readonly Cited[],
  openable: (entityType: CitedKind) => boolean,
  groupable: (entityType: CitedKind) => boolean = () => false,
): CitationChip[] {
  const chips: CitationChip[] = [];
  const seen = new Set<string>();
  const groupAt = new Map<CitedKind, number>();
  for (const cited of evidence) {
    const identity = `${cited.entity_type}:${cited.entity_id}`;
    if (seen.has(identity)) {
      continue;
    }
    seen.add(identity);
    const isOpenable = openable(cited.entity_type);
    if (isOpenable && !groupable(cited.entity_type)) {
      chips.push({
        openable: true,
        entityType: cited.entity_type,
        entityId: cited.entity_id,
        count: 1,
        name: cited.name,
      });
      continue;
    }
    const at = groupAt.get(cited.entity_type);
    if (at === undefined) {
      groupAt.set(cited.entity_type, chips.length);
      chips.push(
        isOpenable
          ? {
              openable: true,
              entityType: cited.entity_type,
              entityId: cited.entity_id,
              count: 1,
            }
          : { openable: false, entityType: cited.entity_type, count: 1 },
      );
      continue;
    }
    chips[at].count += 1;
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

// The sentence's receipt-bearing citations, once each, in the order it cites
// them. Mapped here at the one place that knows both shapes: the wire is
// snake_case and the drawer's CitedRecord is not.
function dedupeCited(evidence: readonly Cited[]): CitedSibling[] {
  const seen = new Set<string>();
  const out: CitedSibling[] = [];
  for (const each of evidence) {
    const key = `${each.entity_type}:${each.entity_id}`;
    if (!RECEIPT_CITATIONS.has(each.entity_type) || seen.has(key)) {
      continue;
    }
    seen.add(key);
    out.push({
      entityType: each.entity_type as "fact" | "profile_field",
      entityId: each.entity_id,
    });
  }
  return out;
}

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
    (entityType) => RECEIPT_CITATIONS.has(entityType),
  );
  // THIS sentence's citations, in the order it cites them, so the receipt's
  // prev/next walks the sentence the reader is actually looking at. The order
  // belongs to the sentence, which is why it is passed from here rather than
  // rebuilt in the drawer.
  // Mapped to the receipt's own shape here, at the one place that knows both:
  // the wire is snake_case and the drawer's CitedRecord is not.
  // Deduplicated, because the stepper finds its position by id: a sentence
  // citing the same fact twice would leave `findIndex` returning the first
  // occurrence forever, and Next would never move past it.
  const siblings = dedupeCited(evidence);
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
            {/* A grouped chip (fact/profile_field, several of the same kind
                in one prose block) opens the FIRST and names the count; the
                drawer's own stepper reaches the rest, which is the receipt
                kind's whole reason for having one. A single deal or person
                names ITSELF rather than its kind — "deal" told a reader
                nothing they could not already see; the deal's own name tells
                them which one. */}
            {chip.count === 1 && chip.name
              ? chip.name
              : chip.count === 1
                ? t(`co.brief.cite.${chip.entityType}`)
                : t(`co.brief.cite.${chip.entityType}.many`, {
                    count: chip.count,
                  })}
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
  citations = "per-sentence",
}: Readonly<{
  sentences: BriefSentence[];
  onOpenRecord?: (entityType: string, entityId: string) => void;
  // WHERE the receipts go, which is a reading decision rather than a styling
  // one.
  //
  // "per-sentence" is the brief's: each line is a separate claim a reader
  // checks on its own, so its chips belong beside it.
  //
  // "collected" is the dossier's: it is one continuous description of a
  // company, and a chip after every clause turned three sentences into a wall
  // of "fact fact fact". The sources are the same, gathered once underneath —
  // every claim stays checkable, and the prose stays readable.
  citations?: "per-sentence" | "collected";
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
          {citations === "per-sentence" && (
            <Citations
              evidence={sentence.evidence}
              onOpenRecord={onOpenRecord}
            />
          )}
        </li>
      ))}
      {citations === "collected" && (
        <li className="co-brief-sources">
          <Citations
            evidence={sentences.flatMap((sentence) => sentence.evidence)}
            onOpenRecord={onOpenRecord}
          />
        </li>
      )}
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
          <h3 className="co-brief-section-label t-eyebrow">
            {t(SECTION_LABELS[section.kind])}
          </h3>
          <SentenceList
            sentences={section.sentences}
            onOpenRecord={onOpenRecord}
            citations="collected"
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
}: Readonly<{
  orgId: string;
  // The 360 the page already holds. The brief itself is written server-side;
  // this is for the two things it cannot write — whether any of the account
  // was withheld from this reader, and which projects it can be about.
  view?: Organization360;
  enabled: boolean;
  onOpenRecord?: (entityType: string, entityId: string) => void;
}>) {
  const t = useT();
  const { locale } = useLocale();
  const queryClient = useQueryClient();
  // The project the brief is about. Part of the query key, so a scoped brief
  // and the whole account's are two cached readings rather than one
  // overwriting the other on screen.
  //
  // No sole-project default here, unlike the composers and the questions:
  // the brief is fetched on open, so a default applied after the page's
  // projects arrive would make every open of a one-project account two
  // reads — the whole account's, then the project's — and the server
  // rewrites the brief on each switch. The standing brief is the account's;
  // a reader narrows it on purpose.
  const [projectId, setProjectId] = useState("");
  const projects = liveProjects(view?.projects);
  const query = projectId ? { project_id: projectId } : undefined;
  const brief = useQuery({
    queryKey: ["org-brief", orgId, projectId],
    enabled,
    queryFn: async () => {
      const { data, error } = await api.GET("/organizations/{id}/brief", {
        params: { path: { id: orgId }, query },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });
  const rewrite = useMutation({
    // The project rides as the variable, so a rewrite pressed after a switch
    // rewrites the brief on screen rather than the one a stale closure saw.
    mutationFn: async (project: string) => {
      const { data, error } = await api.POST("/organizations/{id}/brief", {
        params: {
          path: { id: orgId },
          query: project ? { project_id: project } : undefined,
        },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    onSuccess: (data, project) =>
      queryClient.setQueryData(["org-brief", orgId, project], data),
  });

  if (!enabled) {
    return null;
  }
  const written: Brief | undefined = brief.data;
  // A payload without sentences is a brief this build cannot read, not an
  // account with nothing to say — the same distinction every card here keeps.
  const readable = Array.isArray(written?.sections) ? written : undefined;
  // Who wrote it and when, plus the verb to have it rewritten — the panel's
  // own sourcing, so it sits in the footer band rather than inside the prose
  // it is sourcing.
  const footer = readable && (
    <div className="co-brief-foot">
      <WrittenBy by={readable.generated_by} />
      <Button
        small
        onClick={() => rewrite.mutate(projectId)}
        disabled={rewrite.isPending}
      >
        {rewrite.isPending ? t("co.brief.rewriting") : t("co.brief.rewrite")}
      </Button>
    </div>
  );
  // The "as of" timestamp reads as the header's own fact — when this record
  // was last assembled — rather than a footnote under the rewrite control.
  const titleAction = readable && (
    <span className="t-small">
      {t("co.brief.generatedAt", {
        when: formatDateTime(readable.generated_at, locale, RECORD_ZONE),
      })}
    </span>
  );
  return (
    <Panel
      title={t("co.brief.title")}
      titleAction={titleAction}
      footer={footer}
    >
      <PanelBody className="co-brief-body">
        <ProjectPicker
          projects={projects}
          projectId={projectId}
          onChange={setProjectId}
          scope={readable?.scope}
        />
        {brief.isPending && <Skeleton width="100%" height={64} />}
        {/* Errored, or answered with a payload this build cannot read: both are
            "no brief to show", and rendering the panel over nothing would be a
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
        {rewrite.isError && (
          <p className="t-caption form-error">
            {problemMessageOf(rewrite.error, t)}
          </p>
        )}
        <BriefFooter view={view} />
      </PanelBody>
    </Panel>
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
  projects,
}: Readonly<{
  orgId: string;
  enabled: boolean;
  onOpenRecord?: (entityType: string, entityId: string) => void;
  // The account's projects, as the page read them. Offered as a picker
  // when any is live, so a question can be asked about one engagement
  // rather than the whole account.
  projects?: readonly PickableProject[];
}>) {
  const t = useT();
  const { locale } = useLocale();
  const [projectId, setProjectId] = useState("");
  const live = liveProjects(projects);
  useSoleProjectDefault(live, projectId, setProjectId);
  const ask = useMutation({
    // The project travels as the mutation variable beside the question, so
    // a stale closure cannot ask about a project the picker no longer shows.
    mutationFn: async ({
      question,
      project,
    }: {
      question: Question;
      project: string;
    }) => {
      const { data, error } = await api.POST("/organizations/{id}/ask", {
        params: { path: { id: orgId } },
        body: { question, ...(project ? { project_id: project } : {}) },
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
    <section className="co-part" aria-label={t("co.ask.title")}>
      <Eyebrow as="h3">{t("co.ask.title")}</Eyebrow>
      <ProjectPicker
        projects={live}
        projectId={projectId}
        onChange={(next) => {
          setProjectId(next);
          // The answer on screen was written about the previous project, and
          // its scope line would otherwise stand over the next project's key.
          ask.reset();
        }}
        scope={readable?.scope}
      />
      <p className="co-ask-questions">
        {QUESTIONS.map((question) => (
          <Button
            key={question}
            small
            onClick={() => ask.mutate({ question, project: projectId })}
            disabled={ask.isPending}
          >
            {t(`co.ask.q.${question}`)}
          </Button>
        ))}
      </p>
      {ask.isPending && <Skeleton width="100%" height={40} />}
      {ask.isError && (
        <p className="surfacestate-withheld">
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
            <p className="surfacestate-empty">{t("co.ask.nothing")}</p>
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
// The rating vocabulary, worst first. The ORDER is the worst-of rule: a
// verdict is the lowest-ranked rating among the dimensions that have one
// (PO-AC-N-11).
/**
 * The account's health as its named dimensions and one verdict over them.
 *
 * `overall` is the WORST rating present, never an average: an average lets a
 * strong relationship hide a payment problem, and payment problems are the ones
 * a rep must not miss. It is also a sentence a reader can check — "at risk,
 * because payment is at risk" — where a composite number is not.
 *
 * A dimension with no rating is not in the verdict, and the card says how many
 * it was computed from. Three-of-three and one-of-three are different claims.
 */
export function worstOf(
  dimensions: ReadonlyArray<{ rating?: string } | undefined>,
): { overall?: HealthRating; rated: number } {
  const present = dimensions
    .map((dimension) => dimension?.rating)
    .filter((rating): rating is HealthRating =>
      HEALTH_RANK.includes(rating as HealthRating),
    );
  if (present.length === 0) {
    return { rated: 0 };
  }
  const worst = HEALTH_RANK.find((rating) => present.includes(rating));
  return { overall: worst, rated: present.length };
}

export type StateStrip = NonNullable<Organization360["state_strip"]>;

// Whose move it is, in words. Exported (and no longer rendered by this file
// as a strip tile) because the daily brief's context band reads the same
// `engagement` field now — companytoday.tsx composes the label from here
// rather than re-deriving it.
export const ENGAGEMENT_LABELS: Record<
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
// these keeps the brief from reading as a dashboard where every tile is lit.
export const ENGAGEMENT_TONE: Partial<
  Record<NonNullable<StateStrip["engagement"]>["state"], "warn">
> = {
  waiting_on_them: "warn",
  dormant: "warn",
};

// A reading the caller's grants withheld. Shared with the person record's
// readings row and rail rather than spelled per surface: all three state the
// same fact about the same reader, and a second spelling is exactly the drift
// that had these rows drawn by two different components in the first place.
const WITHHELD_READING: MessageKey = "record.notShown";

// A reading nobody has judged. It is NOT the withheld word — "you may not see
// this" and "there is no verdict yet" are opposite facts about who is missing
// what, and a slot that confuses them sends the reader to ask for an access
// grant that would show them nothing. Its own key rather than the lifecycle
// label it happens to match today: a rename of one must not silently move the
// other.
const UNASSESSED_READING: MessageKey = "co.strip.notAssessed";

/**
 * StateStrip is the KPI row directly under the header: FIVE slots, always five,
 * and every lifecycle draws the account's own standing — stage, open pipeline,
 * relationship, health — because a customer needs those readings at least as
 * much as a prospect does. Being a customer is not a reason to stop reporting
 * whether the relationship is healthy or what deals are open with them. What
 * the lifecycle changes is the fifth reading: a prospect is asked when the deal
 * lands, a customer what the money says.
 *
 * The standing readings come first, the money readings after: what is true of
 * the account regardless of what it has paid is the frame the money sits in,
 * not the other way round. A row that led with money would ask a reader to
 * read the account's health off a number they have not been given the
 * context for yet.
 *
 * EVERY SLOT ALWAYS DRAWS, and says honestly that it has no reading when it has
 * none. A slot that vanishes leaves the reader unable to tell WHICH reading is
 * missing — the row simply looks shorter — and only an empty state is allowed to
 * claim there is none (SurfaceState's rule). So the three absences are three
 * different words and never one: withheld is a fact about the READER, unassessed
 * is a fact about how much has been judged, and "no date" is a fact about the
 * ACCOUNT. Inventing "never contacted" out of a withheld engagement would state
 * the business conclusion a rep acts on, from a permission boundary.
 */
//
// What it must never render is the harder half of the rule, and every omission
// below is one of its bullets: no €0 when the figure is unavailable, no
// cross-currency sum without its conversion source, and nothing called
// "revenue" that is only a count of open deals.
export function StateStrip({
  orgId,
  view,
  lifecycleLabel,
  relationshipLabels,
}: Readonly<{
  orgId: string;
  view?: Organization360;
  lifecycleLabel: (value: string) => string;
  relationshipLabels: (values: readonly string[]) => string;
}>) {
  const t = useT();
  const { locale } = useLocale();
  const strip = view?.state_strip;
  if (!strip) {
    // Absent for two different reasons, and only `sections_omitted` tells
    // them apart. The caller already withholds this component entirely while
    // the composite read is still in flight or has failed (`view` itself is
    // undefined then), so reaching this branch WITH a view means the read
    // succeeded and this one section did not — either it was withheld, or a
    // future account state has nothing here yet. Only the first is worth
    // saying: silently dropping the whole KPI row on a page that otherwise
    // rendered would read as "no readings for this account", the empty state
    // a permission boundary must never impersonate.
    if (view && omitted(view, "state_strip")) {
      return (
        <section className="co-strip-withheld" aria-label={t("co.strip.title")}>
          <p className="surfacestate-withheld">{t("co.section.restricted")}</p>
        </section>
      );
    }
    return null;
  }
  const types = strip.account.relationship_types ?? [];
  // A CURRENT customer only. A former one has invoices in its past, but "do
  // they pay us, and on time?" is not the question their page is opened with,
  // and leading with a money reading on an account that has stopped buying
  // reads as though the relationship were still running.
  const customer = strip.account.lifecycle === "customer";
  // The contract pairs an absent optional section with its name in
  // `sections_omitted` (Organization360), so the reason `health` did not arrive
  // is readable rather than guessable — and guessing is how a grant boundary
  // gets reported as an account nobody has assessed.
  const healthWithheld = view != null && omitted(view, "health");
  const lifecycle = lifecycleLabel(strip.account.lifecycle);
  const relationships =
    types.length > 0 ? relationshipLabels(types) : undefined;
  return (
    // The region's name, and the page rhythm under it, are the SCREEN's to set;
    // the plate inside is the shared primitive's. Keeping them in two elements
    // is what lets the strip be StatStrip unchanged — a screen that reached into
    // the primitive for a margin or a label is how this row became a second copy
    // of it in the first place.
    <section className="co-readings" aria-label={t("co.strip.title")}>
      <StatStrip testId="company-strip">
        {/* Stage, pipeline, relationship and health are the account's own
            standing, and every lifecycle gets them — a customer is not asked
            to give up knowing whether the relationship is healthy or what is
            open with them just because it also has money to report. */}
        <StatCard
          label={t("co.strip.account")}
          value={lifecycle}
          // The relationship types qualify the lifecycle; they do not restate
          // it. An account whose lifecycle is "customer" and whose types also
          // say "customer" was drawing the same word twice in one slot, which
          // reads as a second reading that happens to agree rather than as one
          // reading with nothing to add.
          detail={relationships === lifecycle ? undefined : relationships}
        />
        <PipelineCard commercial={strip.commercial} locale={locale} t={t} />
        {/* Expected close is a prospect's question — how soon a deal not yet
            won might land — and stays out of a customer's row, where a money
            reading already answers what is coming from them. The two are
            exclusive, which is what keeps the row at five slots on every
            account: StatStrip counts the children it is handed, so a row that
            sometimes drew a sixth would fold at a different width from one
            click away. */}
        {!customer && (
          <CloseDateStat commercial={strip.commercial} locale={locale} t={t} />
        )}
        <HealthStat health={view?.health} withheld={healthWithheld} t={t} />
        {/* Whose move it is and the worst open signal both moved to the daily
            brief's context band (companytoday.tsx) — the brief reads the same
            `engagement` and `signal` fields this strip used to, so the strip
            carries the account's STANDING state and the brief carries what is
            DATED. A reading in both places is one the reader has to reconcile,
            which is what made this page a mishmash in the first place. */}
        <HealthSummaryStat
          health={view?.health}
          orgId={orgId}
          withheld={healthWithheld}
          t={t}
        />
        {/* Money closes the row, and only for a customer: everyone else has no
            invoices to ask about. */}
        {customer && <MoneyStat orgId={orgId} locale={locale} t={t} />}
      </StatStrip>
    </section>
  );
}

/**
 * A median days-after-due as a sentence (FIN-FORM-3).
 *
 * Negative days mean they pay BEFORE the due date. "-4 days after due" is a
 * puzzle; "typically 4 days early" is the reading. Shared by the KPI slot and
 * the finance card so the two cannot come to describe earliness differently —
 * spelled twice, only one of the copies would be changed.
 */
export function medianDaysLabel(
  median: number,
  t: ReturnType<typeof useT>,
): string {
  return median < 0
    ? t("finance.medianEarly", { days: Math.abs(median) })
    : t("finance.medianAfterDue", { days: median });
}

// The caveat on a figure that IS shown but is not current. Undefined when the
// figure is current and needs none.
function staleDetailKey(
  state?: components["schemas"]["FinanceSummaryState"],
): MessageKey | undefined {
  switch (state) {
    case "stale":
      return "co.strip.fin.staleFigure";
    case "error":
      return "co.strip.fin.errorFigure";
    case "syncing":
      // The first pass has not finished, so what is shown may be partial.
      return "co.strip.fin.syncing";
    default:
      return undefined;
  }
}

// Why there is no figure, in the reader's terms. Each state has its own fix,
// and naming the wrong one costs the reader a trip to a settings page they did
// not need.
function financeDetailKey({
  pending,
  withheld,
  failed,
  state,
}: Readonly<{
  pending: boolean;
  withheld: boolean;
  failed: boolean;
  state?: components["schemas"]["FinanceSummaryState"];
}>): MessageKey {
  if (pending) {
    return "co.strip.fin.loading";
  }
  // Both before the state switch: with no answer there is no state to read,
  // and guessing one from its absence is how a denial became setup advice.
  if (withheld) {
    return "co.strip.fin.withheld";
  }
  if (failed) {
    return "co.strip.fin.error";
  }
  switch (state) {
    case "unmapped":
      return "co.strip.fin.unmapped";
    case "syncing":
      return "co.strip.fin.syncing";
    case "stale":
      return "co.strip.fin.staleFigure";
    case "error":
      return "co.strip.fin.error";
    case "connected":
      // A live, mapped source that produced no figure. Nothing is broken and
      // there is nothing to set up — we have simply never billed them, or no
      // invoice could be converted. Setup advice here sends the reader to fix
      // a connection that is already working.
      return "co.strip.fin.nothingBilled";
    default:
      // no_connection, and the read that never answered. Both mean there is
      // no source to read, which is the one case the setup advice fits.
      return "co.strip.fin.noConnection";
  }
}

/**
 * The customer row's ONE money slot: what this account has been invoiced over
 * the trailing year.
 *
 * One slot, not three. The strip is a GLANCE, and the Finance tab
 * (companyfinance.tsx) is where the detail lives — which is already why open
 * balance and the payment-habit median are not here. Spending three of five
 * slots on windows of the same figure buried the account's own standing behind
 * the money, and the standing is the frame the money is read in.
 *
 * The label follows the STATE, because the two states answer different
 * questions. With a figure it names the window the figure covers; with none it
 * says "Finance", because the reason there is none is usually a fact about the
 * connection rather than about that window — labelling a "connect your
 * accounting" slot "net invoiced, 12 months" claims we looked at those twelve
 * months and found nothing there.
 */
function MoneyStat({
  orgId,
  locale,
  t,
}: Readonly<{
  orgId: string;
  locale: Locale;
  t: ReturnType<typeof useT>;
}>) {
  // The SAME query the finance card and the payment health dimension run, so
  // every money reading on one page agrees and all but the first cost no
  // request.
  const { data, isPending, isError, error } = useFinanceSummary(orgId);
  // A refusal is not a failure and neither is a setup gap. A reader whose role
  // cannot see finance told to "connect your accounting" is sent to a settings
  // page to fix a permission — the one thing they cannot fix from there.
  const withheld = isError && problemCodeOf(error) === "permission_denied";
  const amount = data?.net_invoiced;
  const caveat = staleDetailKey(data?.state);
  // No figure is not €0, and the six reasons there is none are not one reason.
  // "Connect your accounting" is wrong advice for a connection that exists and
  // is syncing, stale, errored or unmatched — it sends the reader to set up
  // something they already have.
  if (!amount || amount.amount_minor == null || !amount.currency) {
    return (
      <StatCard
        label={t("co.strip.finance")}
        value={t("co.strip.financeUnknown")}
        detail={t(
          financeDetailKey({
            pending: isPending,
            withheld,
            failed: isError && !withheld,
            state: data?.state,
          }),
        )}
      />
    );
  }
  return (
    <StatCard
      label={t("co.strip.netInvoiced")}
      value={formatMoneyCompact(amount.amount_minor, amount.currency, locale)}
      // The provider name goes in the detail line rather than beside the label:
      // a strip slot is narrower than a free-standing stat card, and a badge
      // beside the label wraps onto its own row underneath it, standing the one
      // slot that names its source taller than every sibling in the row.
      //
      // A figure that is not current is shown WITH its caveat rather than
      // withheld: the last known number is usually the right one, and hiding it
      // tells the reader less than showing it qualified would. The caveat takes
      // the line ahead of the provider name, because which accounting system a
      // figure came from matters less than whether the figure is current — and
      // the Finance tab names the connection anyway.
      //
      // Stale and error say DIFFERENT things, which is why `staleDetailKey` does
      // not fold them: `stale` is a sync that SUCCEEDED, just long enough ago
      // that the date matters; `error` is the last good answer after an attempt
      // that failed. Calling either one the other is a wrong claim about whether
      // anything is broken.
      // Lifetime rides in the detail line rather than taking a slot of its own:
      // beside the trailing year it is the one comparison nothing else on this
      // page carries — what the account has ever been worth against what it has
      // been worth lately — and two money slots on a five-slot row would make
      // this row a finance report rather than a glance.
      //
      // Overdue and the open balance stay OUT, and not for want of room. The
      // Finance tab renders both as headline figures one tab away, so a copy
      // here is a second answer to the same question, read at a glance and
      // drifting from the first the moment either changes.
      detail={
        caveat
          ? t(caveat)
          : moneyPhrase(
              "co.strip.lifetimeOf",
              data?.net_invoiced_lifetime,
              locale,
              t,
            ) ||
            data?.provider ||
            undefined
      }
    />
  );
}

// One money figure as a phrase for the detail line, or nothing. Both halves are
// required and neither absence has a substitute: a figure with no currency
// cannot be rendered at all, and a zero the server did not send would say this
// account owes us nothing when the truth is that nobody has told us.
function moneyPhrase(
  key: "co.strip.lifetimeOf",
  amount:
    | { amount_minor?: number | null; currency?: string | null }
    | undefined,
  locale: Locale,
  t: ReturnType<typeof useT>,
): string | undefined {
  return amount?.amount_minor != null && amount.currency
    ? t(key, {
        amount: formatMoneyCompact(
          amount.amount_minor,
          amount.currency,
          locale,
        ),
      })
    : undefined;
}

type StripCommercial = NonNullable<
  NonNullable<Organization360["state_strip"]>["commercial"]
>;

// Open pipeline, labelled as exactly what it is: the sum of open deals, never
// "potential" and never "revenue" (§4.2). Unpriced when nothing on the account
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
    // A null `commercial` is the contract's way of saying the caller has no
    // deal grant, so this is the READER's boundary and not an account with
    // nothing running. "No open deals" here would be the business conclusion a
    // rep acts on, invented out of a permission.
    return (
      <StatCard label={t("co.strip.pipeline")} value={t(WITHHELD_READING)} />
    );
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
  // Three readings, not one blank. Withheld deals are the reader's boundary;
  // an open deal that names no expected close is a gap in the RECORD, which is
  // something a rep can go and fill in; and a date is a date.
  if (!commercial) {
    return (
      <StatCard
        label={t("co.strip.expectedClose")}
        value={t(WITHHELD_READING)}
      />
    );
  }
  if (!commercial.next_close_on) {
    return (
      <StatCard
        label={t("co.strip.expectedClose")}
        value={t("co.next.undated")}
      />
    );
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
// daily brief already answers "whose move is it" — two readings saying "in
// conversation" in different words is one reading's worth of information taking
// two slots of five. A relationship where they write and we do not answer, and
// one where we write into silence, are both "in conversation" by recency and are
// opposite problems.
function HealthStat({
  health,
  withheld,
  t,
}: Readonly<{
  health?: Health;
  withheld: boolean;
  t: ReturnType<typeof useT>;
}>) {
  if (!health) {
    // No health section at all. Withheld says so; anything else has simply not
    // been assessed. Neither is "they have never written" — that is a claim
    // about the account this read has no basis for.
    return (
      <StatCard
        label={t("co.strip.health")}
        value={t(withheld ? WITHHELD_READING : UNASSESSED_READING)}
      />
    );
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

// HealthSummaryStat is the row's fourth slot: the rated dimensions' worst
// verdict, and how many of the possible three were rated at all — the same
// `worstOf` verdict HealthCard's rail breakdown reaches, read here as one
// figure rather than the rail's per-dimension list.
//
// With nothing rated it says so and keeps its denominator. It must not borrow a
// verdict `worstOf` itself declined to give, and it must not carry a tone: a
// grey slot with no words reads as a reading that failed to load, where "not
// assessed, 0 of 3 rated" says which reading is missing and how far off a
// verdict the account is.
function HealthSummaryStat({
  health,
  orgId,
  withheld,
  t,
}: Readonly<{
  health?: Health;
  orgId: string;
  withheld: boolean;
  t: ReturnType<typeof useT>;
}>) {
  const payment = usePaymentHealth(orgId);
  const { overall, rated } = worstOf([
    health?.relationship,
    health?.commercial,
    payment,
  ]);
  if (!overall) {
    return (
      <StatCard
        label={t("co.strip.healthSummary")}
        value={t(withheld ? WITHHELD_READING : UNASSESSED_READING)}
        detail={
          withheld ? undefined : t("co.strip.healthSummary.of", { rated })
        }
      />
    );
  }
  // The denominator is always said, not only when something is failing:
  // "3 of 3 rated" and "1 of 3 rated" are different claims about how much is
  // known, and a figure that only speaks up when things are bad would let a
  // thin reading pass for a complete one.
  const failing = [health?.relationship, health?.commercial, payment].filter(
    (dimension) => dimension?.rating === "at_risk",
  ).length;
  return (
    <StatCard
      label={t("co.strip.healthSummary")}
      value={t(HEALTH_RATING_LABEL[overall])}
      tone={overall === "at_risk" ? "warn" : undefined}
      dot={overall === "at_risk"}
      detail={
        failing > 0
          ? t("co.strip.healthSummary.failingOf", { failing, rated })
          : t("co.strip.healthSummary.of", { rated })
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

// nextCommitmentLine is the daily brief's own footer reading: what is owed
// and how soon. It is not a suggestion — nobody proposed it, the open tasks
// section simply has one — so it sits in the footer rather than as a row.
// Exported so the brief (companytoday.tsx) reads the same truncation-honesty
// logic rather than a second copy of it.
export function nextCommitmentLine(
  view: Organization360 | undefined,
  t: (key: MessageKey, vars?: Record<string, string | number>) => string,
): { headline: string; overdue: boolean } | undefined {
  const steps = view?.next_steps?.data ?? [];
  const step = steps[0];
  if (!step) {
    return undefined;
  }
  // The section is a page of 25 with `has_more` beside it, so past the cap
  // the count is a claim about the PAGE. "12 overdue" on an account with 40
  // is the kind of small wrong figure a rep plans against.
  const truncated = view?.next_steps?.page?.has_more === true;
  const overdueCount = steps.filter((each) => each.overdue).length;
  const count = overdueCount > 0 ? overdueCount : steps.length;
  const key = overdueCount > 0 ? "overdue" : "open";
  return {
    headline: truncated
      ? t(`co.suggest.commitment.${key}AtLeast`, { count })
      : t(`co.suggest.commitment.${key}Count`, { count }),
    overdue: overdueCount > 0,
  };
}

// useSuggestionsBody is the advice section's data and rows, split out of the
// Panel that used to own it: the daily brief now carries this chrome, so the
// dismiss mutation and the "move" rows live here where both that panel and
// the standalone `SuggestionsSection` (still used on its own in tests) can
// reach them without a second, drifting copy. Exported so companytoday.tsx
// composes the same rows rather than reimplementing them.
export function useSuggestionsBody({
  orgId,
  view,
  onOpenRecord,
  onPerform,
}: Readonly<{
  orgId: string;
  view?: Organization360;
  onOpenRecord?: (entityType: string, entityId: string) => void;
  // Performing the advice is the page's job, not this section's: the
  // composer, the deal and the task form all live above it.
  onPerform?: (action: SuggestionAction) => void;
}>): {
  // Whether the section has rows worth showing. A withheld, empty or
  // unavailable suggestion block carries none — advice is additive, and
  // "no advice" or "we cannot advise you" are not things a rep acts on.
  ready: boolean;
  rows: ReactNode;
  // How many rows `rows` draws. A caller that wants to count them beside its
  // own title cannot count a ReactNode, and a caller that recomputed the
  // number from the same view would be a second answer free to disagree with
  // the one on screen.
  count: number;
  // The truncation count and a failed dismissal, additive on top of whatever
  // else the caller's own footer carries.
  footer?: ReactNode;
} {
  const { locale } = useLocale();
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
  if (state !== "ready") {
    return { ready: false, rows: null, count: 0 };
  }
  const footer =
    (dropped !== undefined && dropped > 0) || dismiss.isError ? (
      <>
        {/* A truncated list with no count reads as "that is everything".
            Absent means the section was never computed, which this card
            does not render at all. */}
        {dropped !== undefined && dropped > 0 && (
          <p className="co-row-meta">
            {t("co.suggest.more", { count: dropped })}
          </p>
        )}
        {/* The row staying put with no word reads as a click that missed,
            and the rep clicks again. */}
        {dismiss.isError && (
          <p className="surfacestate-withheld">
            {t("co.suggest.dismissFailed")}
            {` ${problemMessageOf(dismiss.error, t)}`}
          </p>
        )}
      </>
    ) : undefined;
  const rows = suggestions.map((suggestion) => (
    <PanelRow key={suggestion.fingerprint} className="co-move">
      <span className="co-move-body">
        {/* The ASK, at the row's loudest weight: what the rule wants done.
            Falls back to the kind only when the rule named no title of
            its own. */}
        <span className="co-move-ask">
          {suggestion.title ?? t(`co.suggest.kind.${suggestion.kind}`)}
        </span>
        {/* The WHY, under the ask and quieter: the reason is the
            suggestion, and the rest of the row is chrome around it. */}
        <span className="co-move-why">{suggestion.reason}</span>
        <span className="co-move-do">
          {/* WHAT the advice rests on, then WHEN — in that order, because
              the record it read is what a reader checks first and the date
              only means anything once they know which record it belongs
              to. */}
          <span className="co-move-cites">
            <Citations
              evidence={suggestion.evidence}
              onOpenRecord={onOpenRecord}
            />
            {/* The date the EVIDENCE carries — when the thread went
                quiet, when the deal last moved. Never a deadline the
                system chose, which is why a rule firing on an absence
                shows none. */}
            {suggestion.due_at && (
              <span className="co-row-meta">
                {formatDate(suggestion.due_at, locale, RECORD_ZONE)}
              </span>
            )}
          </span>
          <span className="co-move-actions">
            {/* What performing the advice means, named by the server. A
                rule that could not name one carries null and this
                renders nothing rather than a control that does nothing. */}
            {suggestion.action && onPerform && (
              <SuggestionActionButton
                action={suggestion.action}
                onPerform={onPerform}
              />
            )}
            {/* Putting this off is not the row's verb and must not look like
                one: bordered beside the action it would offer a reader two
                equal choices, when only one of them advances the account. */}
            <Button
              small
              className="co-move-defer"
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
        </span>
      </span>
    </PanelRow>
  ));
  return { ready: true, rows, count: suggestions.length, footer };
}

/**
 * SuggestionsSection is the advice rows on their own, in their own Panel —
 * used standalone where nothing else on the page carries this chrome (the
 * stories file, and the suites that exercise the rows without the rest of
 * the daily brief). The live record page mounts the merged brief instead
 * (`TodayOnThisAccount`, companytoday.tsx), which composes the same rows
 * body via `useSuggestionsBody` alongside its own context band.
 */
export function SuggestionsSection({
  orgId,
  view,
  onOpenRecord,
  onPerform,
  onOpenTasks,
}: Readonly<{
  orgId: string;
  view?: Organization360;
  onOpenRecord?: (entityType: string, entityId: string) => void;
  onPerform?: (action: SuggestionAction) => void;
  // Where the footer's commitment reading leads. Absent for a caller with no
  // Tasks tab of its own (the stories file).
  onOpenTasks?: () => void;
}>) {
  const t = useT();
  const body = useSuggestionsBody({ orgId, view, onOpenRecord, onPerform });
  if (!body.ready) {
    return null;
  }
  const commitment = nextCommitmentLine(view, t);
  const footer =
    commitment || onOpenTasks || body.footer ? (
      <>
        {commitment && (
          <Badge tone={commitment.overdue ? "warn" : undefined}>
            {commitment.headline}
          </Badge>
        )}
        {onOpenTasks && (
          <Button small variant="ghost" onClick={onOpenTasks}>
            {t("co.suggest.viewTasks")}
          </Button>
        )}
        {body.footer}
      </>
    ) : undefined;
  return (
    <Panel
      title={t("co.suggest.title")}
      footer={footer}
      tone="accent"
      className="co-lead"
    >
      {body.rows}
    </Panel>
  );
}
