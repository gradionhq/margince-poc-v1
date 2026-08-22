import { useQuery } from "@tanstack/react-query";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { routeHash } from "../app/router";
import { Avatar, Badge, Disclosure } from "../design-system/atoms";
import { AvatarStack } from "../design-system/avatarstack";
import { EvidenceMark } from "../design-system/evidencemark";
import { Panel, PanelBody, PanelRow } from "../design-system/panel";
import { Meter } from "../design-system/readings";
import {
  type SectionState,
  SurfaceState,
  sectionState,
} from "../design-system/surfacestate";
import { useTruncationTooltip } from "../design-system/tooltip";
import { formatDate } from "../format/format";
import { RECORD_ZONE } from "../format/timezone";
import { useLocale, useT } from "../i18n";
import { problemCodeOf, throwProblem } from "./common";
import { signalKindLabel, worstOf } from "./company360";
import {
  HEALTH_DIMENSION_LABEL,
  HEALTH_RANK,
  HEALTH_RATING_LABEL,
  type HealthRating,
  usePaymentHealth,
} from "./companylookups";
import { DetailsGrid } from "./companyraildetails";
import { SectionSummary, sectionAnswered } from "./companyrailshared";
import { TagsSection } from "./companyrailtags";
import { byReach } from "./coverage";
import { roleOf } from "./provider-status";

// The record page's LEFT rail (mockup State A): the account's context,
// beside the work rather than under it. Passed to RecordView's `rail` slot,
// so it takes the wider of the two rail shares (record-zones-rail: 3fr/7fr)
// rather than the narrower `aside` share a right-hand column would get.
//
// Drawn as ONE panel, a details grid at the top then a collapsible section
// per governed 360 slice, rather than the stack of separate cards the page
// used to draw: a hairline between two facts about the same account reads
// as one story with headings, where a gap between two cards reads as two
// stories that happen to sit beside each other.
//
// Every section here is ALSO a slice of the one composite read the page
// already made, except the signals, which run their own query exactly as
// they did before: signals are a separately governed surface, not a 360
// section.
//
// The rail is not rendered while the composer is open. That is the page's
// decision rather than this component's: the drawer opens over the page as
// its own overlay, and the rail standing behind it would only be two things
// competing for the same glance.

type Organization = components["schemas"]["Organization"];
type Organization360 = components["schemas"]["Organization360"];
type Contact = components["schemas"]["Organization360Contact"];
type Signal = components["schemas"]["Signal"];

export function CompanyRail({
  orgId,
  org,
  view,
  loading,
  withPeople,
  composerOpen,
}: Readonly<{
  orgId: string;
  // The page's own resolved record, read regardless of how the composite
  // read below is doing — Details draws from this whenever the composite
  // has no organization slice yet (still loading, or failed), rather than
  // going blank on a read the page already has the answer to.
  org?: Organization;
  view?: Organization360;
  // The composite read `view` comes off is still in flight. Threaded to the
  // sections that read `view` straight (Health, People, Tags) so their
  // `sectionState` calls can tell "still loading" apart from "the read
  // failed" — both hand a section an undefined `view`, and without this flag
  // every one of them reads the failed state for as long as the read runs,
  // flashing "could not be loaded" on every ordinary page open.
  loading: boolean;
  // False where the page's own body is already the roster in full.
  withPeople: boolean;
  // A composer drawer is open in this column. The rail stands down entirely
  // rather than narrowing: squeezed to a third of its width it is a column of
  // broken cards, and no mockup draws the two side by side.
  composerOpen: boolean;
}>) {
  const t = useT();
  if (composerOpen) {
    return null;
  }
  return (
    // A plain div: RecordView's own <aside> is the landmark around this, and a
    // second labelled region inside it would give a reader two names for one
    // column.
    <div className="co-rail">
      <Panel title={t("co.details.title")}>
        <PanelBody>
          <DetailsGrid organization={view?.organization ?? org} />
        </PanelBody>
        <HealthSection view={view} orgId={orgId} loading={loading} />
        {withPeople && <PeopleSection view={view} loading={loading} />}
        <SignalsSection orgId={orgId} />
        <TagsSection view={view} orgId={orgId} loading={loading} />
      </Panel>
    </div>
  );
}

// The meter's tone follows the same low-is-bad reading the badges elsewhere
// on this page use: "strong" is left untoned (flat) so the gradient does not
// paint a good reading as a warning creeping in.
const HEALTH_METER_TONE: Partial<Record<HealthRating, "warn" | "danger">> = {
  at_risk: "danger",
  good: "warn",
};

const HEALTH_BADGE_TONE: Record<HealthRating, "danger" | "warn" | "success"> = {
  at_risk: "danger",
  good: "warn",
  strong: "success",
};

// One named dimension: the label, the sentence the rating was read from, and
// the rating as a bar under both. The reason is server-written prose of no
// bounded length in a rail this narrow, so it truncates and its tooltip carries
// the rest — which is also why this is a component and not markup inlined in
// the map below: the tooltip is a hook, and there is one per dimension.
function HealthMeter({
  label,
  rating,
  reason,
}: Readonly<{ label: string; rating: HealthRating; reason?: string }>) {
  const tip = useTruncationTooltip<HTMLSpanElement>(reason ?? "");
  return (
    <div className="co-health-meter">
      <span className="co-health-meter-head">
        <span className="t-caption">{label}</span>
        <span className="co-health-meter-reason" ref={tip.ref} {...tip.trigger}>
          {reason}
          {tip.tip}
        </span>
      </span>
      {/* dense: the bar reads as this label's own bar, so the dimension is two
          lines tall rather than three. */}
      <Meter
        value={HEALTH_RANK.indexOf(rating) + 1}
        max={HEALTH_RANK.length}
        tone={HEALTH_METER_TONE[rating]}
        flat={!(rating in HEALTH_METER_TONE)}
        dense
        label={label}
      />
    </div>
  );
}

/**
 * HealthSection is the account's health as one verdict over three named
 * dimensions, each drawn as a meter rather than the badge-and-sentence row
 * HealthCard used: the shape this rail's other sections do not have, since
 * a rating is not a count.
 */
function HealthSection({
  view,
  orgId,
  loading,
}: Readonly<{ view?: Organization360; orgId?: string; loading: boolean }>) {
  const t = useT();
  const health = view?.health;
  const payment = usePaymentHealth(orgId);
  const { overall, rated } = worstOf([
    health?.relationship,
    health?.commercial,
    payment,
  ]);
  const lines: string[] = [];
  if (health?.days_since_last_inbound != null) {
    lines.push(
      t("co.health.sinceInbound", { days: health.days_since_last_inbound }),
    );
  }
  if (health?.reply_balance != null) {
    lines.push(
      t("co.health.replyBalance", {
        percent: Math.round(health.reply_balance * 100),
      }),
    );
  }
  if (health?.active_contacts != null) {
    lines.push(
      t(
        health.active_contacts === 1
          ? "co.health.activeContacts.one"
          : "co.health.activeContacts.other",
        { count: health.active_contacts },
      ),
    );
  }
  if (health?.open_commitments != null && health.open_commitments > 0) {
    lines.push(
      t(
        health.open_commitments === 1
          ? "co.health.openCommitments.one"
          : "co.health.openCommitments.other",
        { count: health.open_commitments },
      ),
    );
  }
  const state = sectionState(
    view,
    "health",
    Boolean(health),
    lines.length + rated,
    loading,
  );
  const dimensions = [
    ["relationship", health?.relationship],
    ["commercial", health?.commercial],
    ["payment", payment],
  ] as const;
  return (
    <Disclosure
      className="co-sect"
      open
      summary={
        <span className="co-sect-summary">
          {t("co.health.title")}
          {overall && (
            <Badge tone={HEALTH_BADGE_TONE[overall]}>
              {t(HEALTH_RATING_LABEL[overall])}
            </Badge>
          )}
        </span>
      }
    >
      {state === "ready" ? (
        <PanelBody>
          {dimensions.map(([name, dimension]) =>
            dimension?.rating ? (
              <HealthMeter
                key={name}
                label={t(HEALTH_DIMENSION_LABEL[name])}
                rating={dimension.rating}
                reason={dimension.reason}
              />
            ) : null,
          )}
          {lines.length > 0 && (
            <div className="co-health-lines">
              {lines.map((line) => (
                <p className="co-row-meta" key={line}>
                  {line}
                </p>
              ))}
            </div>
          )}
          {health?.single_threaded && (
            <p className="co-row-meta">
              <Badge tone="warn">{t("co.health.singleThreaded")}</Badge>
            </p>
          )}
        </PanelBody>
      ) : (
        <PanelBody>
          <SurfaceState state={state} emptyLabel={t("co.health.empty")}>
            {null}
          </SurfaceState>
        </PanelBody>
      )}
    </Disclosure>
  );
}

/**
 * PeopleSection is a glance at the roster: who is here, how they have
 * answered, and, where the graph read supports it, the colleagues already in
 * contact with them. The set-role and route-in verbs stay on the People tab's
 * own roster rather than being rebuilt here a second time.
 */
function PeopleSection({
  view,
  loading,
}: Readonly<{ view?: Organization360; loading: boolean }>) {
  const t = useT();
  const contacts = [...(view?.people?.data ?? [])].sort(byReach);
  const state = sectionState(
    view,
    "people",
    Boolean(view?.people),
    contacts.length,
    loading,
  );
  return (
    <Disclosure
      className="co-sect"
      open
      summary={
        <SectionSummary
          title={t("co.people.title")}
          count={sectionAnswered(state) ? contacts.length : undefined}
        />
      }
    >
      {state === "ready" ? (
        contacts.map((contact) => (
          <PanelRow key={contact.person_id} className="co-person-row">
            <PersonRow contact={contact} />
          </PanelRow>
        ))
      ) : (
        <PanelBody>
          <SurfaceState state={state} emptyLabel={t("co.people.empty")}>
            {null}
          </SurfaceState>
        </PanelBody>
      )}
    </Disclosure>
  );
}

function PersonRow({ contact }: Readonly<{ contact: Contact }>) {
  const colleagues = contact.routes?.top ?? [];
  return (
    <>
      <Avatar name={contact.full_name} />
      <span className="co-person-id">
        {/* Same target the People tab offers, and the same reach: the row is
            the link, not the eight characters of a short name. */}
        <a
          className="co-rowlink co-person-name co-rowcover"
          href={routeHash({ screen: "contacts", id: contact.person_id })}
        >
          {contact.full_name}
        </a>
        {/* The same fallback the tab makes, because a rail that disagreed
            with the tab about somebody's role is worse than neither showing
            one. The server decides the precedence; both surfaces read it.
            Branched on the VALUE, not on title_source — that field is
            optional, and a server sending a purchased title without it would
            otherwise render an empty, padded span. */}
        {roleOf(contact) && (
          <span className="co-person-role">
            {contact.title_source === "provider" ? (
              <EvidenceMark
                value={roleOf(contact)}
                source={{
                  provenance: { kind: "connector", connector: "provider" },
                }}
              />
            ) : (
              roleOf(contact)
            )}
          </span>
        )}
      </span>
      {colleagues.length > 0 && (
        <span className="co-person-routes">
          {/* The stack has no text of its own, so the names it draws as
              faces are read again here for anyone not reading them as
              monograms. A plain <span> carries no accessible name for an
              aria-label to attach to. */}
          <span className="sr-only">
            {colleagues.map((route) => route.display_name).join(", ")}
          </span>
          <AvatarStack
            people={colleagues.map((route) => ({ name: route.display_name }))}
          />
        </span>
      )}
    </>
  );
}

// The severity vocabulary a signal's dot is drawn in, mirroring SignalsCard's
// own SIGNAL_TONE (company360.tsx), copied rather than imported for the same
// reason the health tables above are: this module is on the other side of
// that one's import.
const SIGNAL_DOT_TONE: Record<string, "warn" | "danger" | undefined> = {
  info: undefined,
  warn: "warn",
  urgent: "danger",
};

// Own-property only: `severity` arrives as a wire string, and a value named
// `toString` would otherwise find something on Object's prototype and type as
// a tone.
function signalDotTone(severity: string): "warn" | "danger" | undefined {
  return Object.hasOwn(SIGNAL_DOT_TONE, severity)
    ? SIGNAL_DOT_TONE[severity]
    : undefined;
}

/**
 * SignalsSection reads the account-filtered signals, same endpoint and same
 * withheld/failed handling SignalsCard used. Signals are a separately
 * governed surface, not a 360 section, so this runs its own query rather
 * than reading a slice of `view`.
 */
function SignalsSection({ orgId }: Readonly<{ orgId: string }>) {
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
  const withheld =
    query.isError && problemCodeOf(query.error) === "permission_denied";
  let state: SectionState = "ready";
  if (withheld) {
    state = "withheld";
  } else if (query.isError) {
    state = "failed";
  } else if (query.isPending) {
    state = "loading";
  } else if (signals.length === 0) {
    state = "empty";
  }
  return (
    <Disclosure
      className="co-sect"
      open
      summary={
        <SectionSummary
          title={t("co.signals.title")}
          count={sectionAnswered(state) ? signals.length : undefined}
        />
      }
    >
      {state === "ready" ? (
        signals.map((signal) => (
          <PanelRow key={signal.id} className="co-signal-row">
            <span
              className={`co-dot${signalDotTone(signal.severity) ? ` co-dot-${signalDotTone(signal.severity)}` : ""}`}
              aria-hidden="true"
            />
            <span className="co-signal-body">
              <span className="co-signal-title">
                {signalKindLabel(signal.kind, t)}
              </span>
              <span className="co-signal-summary">{signal.summary}</span>
              {/* A signal ABOUT one of the account's projects sends the
                  reader to that project: the summary names it, the link
                  opens it. An account- or person-subject signal already
                  sits on the page it is about. */}
              {signal.entity_type === "project" && signal.entity_id && (
                <a
                  className="co-rowlink co-signal-link"
                  href={routeHash({ screen: "projects", id: signal.entity_id })}
                >
                  {t("co.signals.openProject")}
                </a>
              )}
            </span>
            <span className="co-row-meta">
              {formatDate(signal.detected_at, locale, RECORD_ZONE)}
            </span>
          </PanelRow>
        ))
      ) : (
        <PanelBody>
          <SurfaceState
            state={state}
            emptyLabel={t("co.signals.empty")}
            detail={
              state === "failed" ? { onRetry: () => void query.refetch() } : {}
            }
          >
            {null}
          </SurfaceState>
        </PanelBody>
      )}
    </Disclosure>
  );
}
