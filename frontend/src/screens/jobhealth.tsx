// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useQuery } from "@tanstack/react-query";
import { TriangleAlert } from "lucide-react";
import type { CSSProperties, ReactNode } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { useHoldsAdminRole } from "../app/capability";
import { Badge, Card, EmptyState } from "../design-system/atoms";
import { Callout } from "../design-system/callout";
import { type Fact, FactList } from "../design-system/factlist";
import { formatDateTime, formatNumber } from "../format/format";
import type { Translator } from "../format/now";
import { type Locale, useLocale, useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { QueryGate, throwProblem } from "./common";

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

// A row of pills that wraps instead of running the card wide.
const PILL_ROW: CSSProperties = {
  display: "flex",
  gap: "var(--space-2)",
  flexWrap: "wrap",
};

// The gap between this card's stacked sections.
const SECTION: CSSProperties = { marginTop: "var(--space-4)" };

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
function formatWaitedFor(seconds: number, t: Translator): string {
  if (seconds >= 86_400) {
    return t("jobs.waitedDays", { count: Math.floor(seconds / 86_400) });
  }
  if (seconds >= 3_600) {
    return t("jobs.waitedHours", { count: Math.floor(seconds / 3_600) });
  }
  if (seconds >= 60) {
    return t("jobs.waitedMinutes", { count: Math.floor(seconds / 60) });
  }
  return t("jobs.waitedSeconds", { count: seconds });
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
    <span style={PILL_ROW}>
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

function KindSection({
  title,
  sub,
  kinds,
  emptyText,
}: Readonly<{
  title: string;
  sub?: string;
  kinds: readonly JobKindHealth[];
  emptyText: string;
}>) {
  const t = useT();
  return (
    <section style={SECTION}>
      <h3>{title}</h3>
      {sub !== undefined && <p className="t-caption">{sub}</p>}
      {kinds.length === 0 ? (
        <EmptyState>{emptyText}</EmptyState>
      ) : (
        <FactList numeric facts={kindFacts(kinds, t)} />
      )}
    </section>
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
      value: failure.reason,
      note: t("jobs.attempt", {
        attempt: failure.attempt,
        max: failure.max_attempts,
        when: formatDateTime(failure.failed_at, locale, zone),
      }),
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
    <section style={SECTION}>
      <h3>{t("jobs.failures")}</h3>
      <p className="t-caption">{t("jobs.failuresSub")}</p>
      {failures.length === 0 ? (
        <EmptyState>{t("jobs.failuresEmpty")}</EmptyState>
      ) : (
        <>
          <FactList facts={failureFacts(failures, t, locale, zone)} />
          <p className="t-caption" style={SECTION}>
            {t("jobs.reasonVetted")}
          </p>
        </>
      )}
    </section>
  );
}

function JobHealthBody({ health }: Readonly<{ health: JobHealth }>) {
  const t = useT();
  const { locale } = useLocale();
  // The reader's own resolved zone: a stalled-queue timestamp is only useful
  // against the clock on their wall, and no other zone here is ours to assume.
  const zone = Intl.DateTimeFormat().resolvedOptions().timeZone;

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
      <KindSection
        title={t("jobs.workspaceKinds")}
        kinds={health.kinds.filter((kind) => !kind.fleet_wide)}
        emptyText={t("jobs.workspaceEmpty")}
      />
      <KindSection
        title={t("jobs.dispatcherKinds")}
        sub={t("jobs.dispatcherSub")}
        kinds={health.kinds.filter((kind) => kind.fleet_wide)}
        emptyText={t("jobs.dispatcherEmpty")}
      />
      <FailureSection
        failures={health.recent_failures}
        locale={locale}
        zone={zone}
      />
      <p className="t-caption" style={SECTION}>
        {t("jobs.generatedAt", {
          time: formatDateTime(health.generated_at, locale, zone),
        })}
      </p>
    </>
  );
}

export function JobHealthCard() {
  const t = useT();
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
      return data;
    },
  });

  let body: ReactNode;
  if (!isAdmin) {
    // Withheld, not absent. The card keeps its place on a maintenance page a
    // non-admin reaches for its other sections, and an absent card there would
    // read as "nothing is queued" — a different claim entirely.
    body = (
      <EmptyState>
        <p className="t-small">{t("jobs.adminOnly")}</p>
      </EmptyState>
    );
  } else {
    // No `empty` predicate on the gate: its generic copy would understate the
    // one thing this card exists to report, so the body owns that rung.
    body = (
      <QueryGate query={query}>
        {(health) => <JobHealthBody health={health} />}
      </QueryGate>
    );
  }

  return (
    <Card
      title={t("settings.jobs")}
      sub={t("settings.jobsSub")}
      style={{ marginBottom: "var(--space-4)" }}
    >
      {body}
    </Card>
  );
}
