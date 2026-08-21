// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useQuery } from "@tanstack/react-query";
import { TriangleAlert } from "lucide-react";
import type { ReactNode } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { useHoldsAdminRole } from "../app/capability";
import { Badge, EmptyState } from "../design-system/atoms";
import { Callout } from "../design-system/callout";
import { CardBoundary } from "../design-system/cardboundary";
import { type Fact, FactList } from "../design-system/factlist";
import { Panel, PanelBody } from "../design-system/panel";
import { SettingList, SettingRow } from "../design-system/settingrow";
import { formatDateTime, formatNumber } from "../format/format";
import type { Translator } from "../format/now";
import { viewerZone } from "../format/timezone";
import { type Locale, useLocale, useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { QueryGate, throwProblem, useMe } from "./common";
import "./jobhealth.css";

// GET /admin/job-health — the operator's only window onto the background
// system, for Settings → Maintenance. Until this card existed a stalled queue
// had no screen at all: the endpoint shipped and nothing read it.
//
// The read is the admin's, gated server-side on the ROLE and on a human
// session (x-agent-access: human-only) rather than on any RBAC object — none
// describes background work, and the report spans the whole installation, not
// just the reader's own records.

type JobHealth = components["schemas"]["JobHealth"];
type JobKindHealth = components["schemas"]["JobKindHealth"];
type JobFailure = components["schemas"]["JobFailure"];

// The closed failure-state enum with the tone each state earns. `cancelled`
// earns none: it was stopped deliberately, so it records a decision somebody
// made rather than a fault the reader has to chase. Keying the map on the union
// makes a state added upstream a compile error here instead of an untoned badge.
const FAILURE_STATE: Record<
  JobFailure["state"],
  Readonly<{ label: MessageKey; tone: "warn" | "danger" | undefined }>
> = {
  retryable: { label: "jobs.state.retryable", tone: "warn" },
  discarded: { label: "jobs.state.discarded", tone: "danger" },
  cancelled: { label: "jobs.state.cancelled", tone: undefined },
};

// How long the oldest runnable job of a kind has waited, in the largest whole
// unit. `format.ts`'s formatDuration cannot answer this: it floors anything
// under an hour to "0 hr", and a queue that jammed five minutes ago is exactly
// the reading an operator opens this card for.
// A count of one takes the singular key, the house `.one`/`.other` pattern — a
// queue that jammed sixty-one minutes ago read "waited 1 hours".
function formatWaitedFor(seconds: number, t: Translator): string {
  const [unit, count] =
    seconds >= 86_400
      ? (["Days", Math.floor(seconds / 86_400)] as const)
      : seconds >= 3_600
        ? (["Hours", Math.floor(seconds / 3_600)] as const)
        : seconds >= 60
          ? (["Minutes", Math.floor(seconds / 60)] as const)
          : (["Seconds", seconds] as const);
  const plural = count === 1 ? "one" : "other";
  // Annotated so an unknown unit or form is a compile error rather than a key
  // the catalog silently echoes back.
  const key: MessageKey = `jobs.waited${unit}.${plural}`;
  return t(key, { count });
}

// All four states of one kind, always all four. A zero is a fact an operator
// came here to read — "0 dead" is the reassurance — and dropping the zeros
// would also let the pills shift position from row to row, which is the one
// thing that makes a column of counts unscannable.
function KindCounts({ kind }: Readonly<{ kind: JobKindHealth }>) {
  const t = useT();
  const { locale } = useLocale();
  const shown = (value: number) => formatNumber(value, locale);
  return (
    <span className="jobhealth-counts">
      <Badge>{t("jobs.count.waiting", { count: shown(kind.waiting) })}</Badge>
      <Badge>{t("jobs.count.running", { count: shown(kind.running) })}</Badge>
      <Badge tone={kind.retrying > 0 ? "warn" : undefined}>
        {t("jobs.count.retrying", { count: shown(kind.retrying) })}
      </Badge>
      <Badge tone={kind.dead > 0 ? "danger" : undefined}>
        {t("jobs.count.dead", { count: shown(kind.dead) })}
      </Badge>
    </span>
  );
}

function kindFacts(kinds: readonly JobKindHealth[], t: Translator): Fact[] {
  return kinds.map((kind) => ({
    // `kind` is unique per queue, not globally — the same worker kind can be
    // registered on two queues, and the pair is what names one row. Serialized
    // rather than joined on a separator, because neither identifier's grammar
    // rules one out and a delimiter that can appear in a part is a key collision.
    key: JSON.stringify([kind.queue, kind.kind]),
    // The stable identifier River persists in river_job.kind, verbatim and
    // mono: it is what the operator greps the worker log for, so humanizing
    // the underscores away would cost them the string they actually need.
    term: <span className="t-mono">{kind.kind}</span>,
    value: <KindCounts kind={kind} />,
    note: (
      <>
        {t("jobs.queue", { queue: kind.queue })}
        {/* Stated only when something of this kind IS runnable. A null age
            means nothing is queued now — a job scheduled for later is not
            late — and a row claiming a wait of zero would read as a queue
            that just started rather than one with nothing in it. */}
        {kind.oldest_waiting_age_seconds !== null && (
          <> · {formatWaitedFor(kind.oldest_waiting_age_seconds, t)}</>
        )}
      </>
    ),
  }));
}

// One reading of the queue, as a stacked row: the counts ARE the subject rather
// than an answer to a question that would fit beside them, so they take the
// card's full width under their naming (design-system README, SettingRow).
//
// `.settingrow-control` is a flex ROW, and a `FactList` is a grid that sizes to
// its content inside one — hence the `settingrow-measure` wrapper, which is what
// gives a stacked control the whole width back.
function KindSection({
  label,
  description,
  kinds,
  emptyText,
}: Readonly<{
  label: string;
  description?: string;
  kinds: readonly JobKindHealth[];
  emptyText: string;
}>) {
  const t = useT();
  return (
    <SettingRow
      label={label}
      description={description}
      layout="stack"
      control={
        <div className="settingrow-measure">
          {kinds.length === 0 ? (
            <EmptyState>{emptyText}</EmptyState>
          ) : (
            <FactList numeric facts={kindFacts(kinds, t)} />
          )}
        </div>
      }
    />
  );
}

// The meta line under one failure: which row it is, how far up the retry ladder
// it got, and how long it has been there.
//
// Every part but the attempt counter is optional on the wire, and an absent one
// is DROPPED rather than drawn — a "failing since" with nothing after it, or a
// bare job label, states a value this report never sent and sends the reader
// looking for a row that does not exist.
function failureNote(
  failure: JobFailure,
  t: Translator,
  locale: Locale,
  zone: string,
): ReactNode {
  const { failure_class: failureClass, job_id: jobID } = failure;
  const since = failure.first_failed_at;
  return (
    <>
      {/* The class reads as an identifier, not a `Badge`. A Badge is a status
          and this row already carries the one status it has, so a second pill
          beside it reads as a second state; and the class vocabulary grows with
          every unit that declares one, so no closed tone could be honest about
          a token this build has never seen. Mono, like the kind above it,
          because both are strings the operator greps the worker log with and
          keys an alert on, and dressing either up costs them the exact text. */}
      {failureClass !== null && failureClass !== undefined && (
        <>
          <span className="t-mono">{failureClass}</span>
          {" · "}
        </>
      )}
      {t("jobs.attempt", {
        attempt: failure.attempt,
        max: failure.max_attempts,
        when: formatDateTime(failure.failed_at, locale, zone),
      })}
      {/* Interpolated as-is, never through formatNumber: this is the id River
          prints in its own log lines, and a grouped "4,711" is not a string
          that finds them. */}
      {jobID !== undefined && (
        <>
          {" · "}
          {t("jobs.jobId", { id: jobID })}
        </>
      )}
      {/* Only when an attempt error was actually recorded. A job cancelled
          before it ever ran has no first failure, and falling back to failed_at
          would report a single failure as a span of them. */}
      {since !== null && since !== undefined && (
        <>
          {" · "}
          {t("jobs.failingSince", {
            when: formatDateTime(since, locale, zone),
          })}
        </>
      )}
    </>
  );
}

function failureFacts(
  failures: readonly JobFailure[],
  t: Translator,
  locale: Locale,
  zone: string,
): Fact[] {
  return failures.map((failure, index) => {
    const state = FAILURE_STATE[failure.state];
    return {
      // Position joins the key deliberately: this is a server-ordered snapshot
      // with nothing to reorder or edit, and two failures of one kind can
      // genuinely share a timestamp, so nothing else tells them apart.
      key: JSON.stringify([index, failure.kind]),
      term: (
        <>
          <span className="t-mono">{failure.kind}</span>{" "}
          <Badge tone={state.tone}>{t(state.label)}</Badge>
        </>
      ),
      // The job layer's own vetted sentence, shown as it arrived. The worker's
      // raw cause never travels on this endpoint, so there is nothing here to
      // second-guess or trim.
      value: (
        <>
          {failure.reason}
          {/* Checked on its own rather than off the class: the endpoint nulls
              the two together, but a screen that inferred one from the other
              would draw an empty action line the day that coupling changes. */}
          {failure.remedy !== null && failure.remedy !== undefined && (
            <span className="t-small jobhealth-remedy">
              {t("jobs.remedy", { remedy: failure.remedy })}
            </span>
          )}
        </>
      ),
      note: failureNote(failure, t, locale, zone),
    };
  });
}

function FailureSection({
  failures,
  locale,
  zone,
}: Readonly<{
  failures: readonly JobFailure[];
  locale: Locale;
  zone: string;
}>) {
  const t = useT();
  return (
    <SettingRow
      label={t("jobs.failures")}
      description={t("jobs.failuresSub")}
      layout="stack"
      control={
        failures.length === 0 ? (
          <EmptyState>{t("jobs.failuresEmpty")}</EmptyState>
        ) : (
          // The list and the caveat under it are two children of the ROW's
          // control column, not two children of one wrapper: a stacked row's
          // control is already a flex column with the interval on it
          // (settingrow.css), so a wrapper here would have to state a margin
          // the row has spelled once for every card.
          <>
            <div className="settingrow-measure">
              <FactList facts={failureFacts(failures, t, locale, zone)} />
            </div>
            <p className="t-caption">{t("jobs.reasonVetted")}</p>
          </>
        )
      }
    />
  );
}

function JobHealthBody({
  health,
  zone,
}: Readonly<{ health: JobHealth; zone: string }>) {
  const t = useT();
  const { locale } = useLocale();

  if (health.kinds.length === 0 && health.recent_failures.length === 0) {
    // Its own words rather than the generic "nothing here": that the background
    // system is idle is a finding, and the reader must be able to tell it from
    // a card that had nothing to show them.
    return <EmptyState>{t("jobs.empty")}</EmptyState>;
  }

  const dead = health.kinds.reduce((total, kind) => total + kind.dead, 0);
  return (
    <>
      {dead > 0 && (
        // The one thing on this card an operator must not scroll past: dead
        // work does not resume on its own. `alert` because it appears when the
        // report lands, and it names a failure the reader has to act on.
        <Callout
          tone="danger"
          live="alert"
          icon={TriangleAlert}
          title={t("jobs.deadTitle")}
        >
          <p>{t("jobs.deadBody", { count: formatNumber(dead, locale) })}</p>
        </Callout>
      )}
      <SettingList>
        <KindSection
          label={t("jobs.workspaceKinds")}
          kinds={health.kinds.filter((kind) => !kind.fleet_wide)}
          emptyText={t("jobs.workspaceEmpty")}
        />
        <KindSection
          label={t("jobs.dispatcherKinds")}
          description={t("jobs.dispatcherSub")}
          kinds={health.kinds.filter((kind) => kind.fleet_wide)}
          emptyText={t("jobs.dispatcherEmpty")}
        />
        <FailureSection
          failures={health.recent_failures}
          locale={locale}
          zone={zone}
        />
      </SettingList>
    </>
  );
}

export function JobHealthCard() {
  const t = useT();
  const { locale } = useLocale();
  // The reader's own resolved zone: a stalled-queue timestamp is only useful
  // against the clock on their wall, and no other zone here is ours to assume.
  // Resolved once for the card and handed down, so the stamp in the footer and
  // the failure timestamps in the body cannot read two different clocks.
  const zone = viewerZone();
  // The probe itself, not only its answer: useHoldsAdminRole reads false while
  // /me is in flight, so branching on `!isAdmin` alone told every administrator
  // that job health was admin-only, on every load of the Maintenance tab,
  // until the session landed.
  const me = useMe();
  // The role, not a grant: the endpoint gates on `admin` server-side and no
  // RBAC object describes background work. `enabled` is what keeps a non-admin
  // from issuing a call that could only 403 — a refusal the reader cannot act
  // on has no business becoming this card's error state.
  const isAdmin = useHoldsAdminRole();
  const query = useQuery({
    queryKey: ["job-health"],
    enabled: isAdmin,
    queryFn: async () => {
      const { data, error } = await api.GET("/admin/job-health");
      if (error) {
        throwProblem(error);
      }
      // The contract makes all three required (JobHealth), so a body missing
      // one is not a thin report — it is a report this card cannot read, and
      // the check belongs at the boundary where the wire stops being trusted
      // rather than at each of the six places the body dereferences it.
      //
      // Rejecting it is the point. `?? []` would draw the card's idle state,
      // which says the background system has nothing queued and nothing failed
      // — a claim about the installation that this response never made, and
      // exactly the reading an operator opens this card to trust. A malformed
      // payload is a condition to report, so it becomes the card's error state:
      // it says the report could not be loaded and offers a retry.
      if (
        !data ||
        typeof data.generated_at !== "string" ||
        !Array.isArray(data.kinds) ||
        !Array.isArray(data.recent_failures)
      ) {
        throw new Error("malformed job-health response");
      }
      return data;
    },
  });

  let body: ReactNode;
  if (!isAdmin) {
    // Withheld, not absent. The card keeps its place on a maintenance page a
    // non-admin reaches for its other sections, and an absent card there would
    // read as "nothing is queued" — a different claim entirely.
    //
    // Behind the probe, so the notice states a settled denial rather than the
    // absence of an answer: while /me is in flight nobody holds any role yet.
    body = (
      <QueryGate query={me}>
        {() => (
          <EmptyState>
            <p className="t-small">{t("jobs.adminOnly")}</p>
          </EmptyState>
        )}
      </QueryGate>
    );
  } else {
    // No `empty` predicate on the gate: its generic copy would understate the
    // one thing this card exists to report, so the body owns that rung.
    body = (
      <QueryGate query={query}>
        {(health) => <JobHealthBody health={health} zone={zone} />}
      </QueryGate>
    );
  }

  // When the report was read belongs to the whole card rather than to any one
  // reading in it, which is what `Panel`'s footer band is: the card's own
  // trailing fact, ruled off edge to edge like the header. Read off the query
  // rather than passed up out of the body, so the stamp is absent for exactly
  // the states that have no report — withheld, pending, failed — and present
  // for every state that does, the idle one included: an operator trusting
  // "nothing is queued" needs to know how old that answer is.
  // Behind `isAdmin` as well as behind the data, because a cache outlives a
  // grant: a role edited mid-session leaves the last report sitting in the
  // query cache, and a stamp under a withheld body would date a reading the
  // card is no longer showing.
  const report = isAdmin ? query.data : undefined;

  // No bottom margin: `.settings-stack` owns the gap between cards, and a card
  // that adds its own gets two.
  return (
    <Panel
      title={t("settings.jobs")}
      footer={
        report &&
        t("jobs.generatedAt", {
          time: formatDateTime(report.generated_at, locale, zone),
        })
      }
    >
      <PanelBody>
        <p className="settings-panel-sub">{t("settings.jobsSub")}</p>
        {/* One card's throw stays inside one card. This body derives every
            line from a payload the background system writes, so it has more
            ways to give out than the panels beside it — and without a boundary
            the whole Maintenance tab, navigation rail included, goes with it. */}
        <CardBoundary>{body}</CardBoundary>
      </PanelBody>
    </Panel>
  );
}
