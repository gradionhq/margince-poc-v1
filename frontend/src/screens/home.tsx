import {
  type UseQueryResult,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { ArrowRight, Check, RefreshCw, X } from "lucide-react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { navigate } from "../app/router";
import {
  Badge,
  Button,
  Card,
  EmptyState,
  SectionHeader,
} from "../design-system/atoms";
import { DealCard } from "../design-system/composed";
import {
  formatDate,
  formatDateTime,
  formatMoneyOrAbsent,
} from "../format/format";
import { RECORD_ZONE, viewerZone } from "../format/timezone";
import { useLocale, useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { problemMessageOf, QueryGate, throwProblem } from "./common";
import { errorClassKey, isUnhealthy } from "./connector-status";
import { EntityRef } from "./entityref";
import {
  ApprovalRow,
  useApprovalTokenSink,
  usePendingApprovals,
} from "./inbox";
import { isProjectPhase, PHASE_LABEL } from "./projects.form";
import "./home.css";

// Home / Morning Brief (B-EP09.12b on the E05 spine): the persisted /brief
// run IS the queue — the §10.1 composite with its factor decomposition (no
// mystery number), evidence-or-omit, per-rep act/dismiss (B-E05.13). Pending
// 🟡 approvals stay on top (nothing sent yet); stalled deals close the page.
// No run yet → an honest generate card; an empty run → honest quiet, never
// invented urgency.

type MorningBrief = components["schemas"]["MorningBrief"];
type MorningBriefItem = components["schemas"]["MorningBriefItem"];

export function useMorningBrief(): UseQueryResult<MorningBrief | null> {
  return useQuery({
    queryKey: ["brief"],
    queryFn: async (): Promise<MorningBrief | null> => {
      const { data, error, response } = await api.GET("/brief");
      if (response.status === 404) {
        return null;
      }
      if (error) {
        throwProblem(error);
      }
      return data ?? null;
    },
  });
}

// The overnight digest (CAP-WIRE-6): the nightly build's stored counts —
// what capture landed and what awaits review. `404 no_digest_yet` renders
// nothing at all: before the first nightly run there is no card, never a
// fabricated row of zeros. The digest also carries `connectors[]` — the
// per-source health Settings shows in full; here it surfaces only when a
// source is unhealthy (a healthy connector is not news, and a permanent
// green row would be noise), sharing Settings' own vocabulary
// (isUnhealthy/errorClassKey, Task 5) so the two surfaces never describe
// the same state differently.
type MorningDigest = components["schemas"]["MorningDigest"];

function useMorningDigest(): UseQueryResult<MorningDigest | null> {
  return useQuery({
    queryKey: ["digest"],
    queryFn: async (): Promise<MorningDigest | null> => {
      const { data, error, response } = await api.GET("/digest");
      // 404 — this installation has no digest YET. 501 — it does not serve one
      // at all: the operation is specified and unimplemented, so the answer is
      // a refusal rather than a delay, and it will not become a digest by being
      // asked again. Both are the same fact to a reader (there is no brief to
      // show), and returning null says so.
      //
      // Reading 501 as an error is what put a permanent loading block on the
      // home page. A 501 is a 5xx, so the query client retried it (queryclient
      // .ts), and React Query PAUSES between retry attempts while the document
      // is hidden — so the query sat at fetchStatus "paused" and never settled,
      // and the pending skeleton stood in for the refusal indefinitely. The
      // reader saw three grey bars that would still be there tomorrow.
      if (response.status === 404 || response.status === 501) {
        return null;
      }
      if (error) {
        throwProblem(error);
      }
      return data ?? null;
    },
  });
}

function DigestStat({
  value,
  label,
}: Readonly<{ value: number; label: MessageKey }>) {
  const t = useT();
  return (
    <div className="digest-stat">
      <span className="digest-stat-value t-h2 t-mono">{value}</span>
      <span className="digest-stat-label t-caption">{t(label)}</span>
    </div>
  );
}

type DigestProjects = NonNullable<
  components["schemas"]["MorningDigest"]["projects"]
>;

// A rung of the project ladder in the reader's words. The digest carries the
// phase as open wire text, so a rung added upstream renders as its own word
// rather than failing the index.
function phaseWord(phase: string, t: (key: MessageKey) => string): string {
  return isProjectPhase(phase) ? t(PHASE_LABEL[phase]) : phase;
}

// What moved on the projects overnight: every project named is a link to its
// page, because the section exists to send the reader there. A list that is
// empty renders nothing — the heading alone would claim news it has none of.
function DigestProjectsSection({
  projects,
}: Readonly<{ projects: DigestProjects }>) {
  const t = useT();
  const { phase_changes, new_commitments, gone_quiet } = projects;
  if (
    phase_changes.length === 0 &&
    new_commitments.length === 0 &&
    gone_quiet.length === 0
  ) {
    return null;
  }
  // The birth row of a project created overnight carries no from_phase; a
  // move between rungs is the news, so only those are listed.
  const moves = phase_changes.filter((change) => change.from_phase != null);
  return (
    <div className="digest-projects" data-testid="digest-projects">
      <span className="digest-projects-title t-caption">
        {t("home.digestProjects")}
      </span>
      {moves.length > 0 && (
        <ul
          className="digest-project-list"
          aria-label={t("home.digestPhaseChanges")}
        >
          {moves.map((change) => (
            <li key={`${change.project_id}-${change.occurred_at}`}>
              <EntityRef kind="project" id={change.project_id} />{" "}
              <span className="t-caption">
                {t("home.digestPhaseChange", {
                  from: phaseWord(change.from_phase ?? "", t),
                  to: phaseWord(change.to_phase, t),
                })}
              </span>
            </li>
          ))}
        </ul>
      )}
      {new_commitments.length > 0 && (
        <ul
          className="digest-project-list"
          aria-label={t("home.digestNewCommitments")}
        >
          {new_commitments.map((item) => (
            <li key={item.project_id}>
              <EntityRef kind="project" id={item.project_id} />{" "}
              <span className="t-caption">
                {t("home.digestCommitmentCount", {
                  count: item.new_open_commitments,
                })}
              </span>
            </li>
          ))}
        </ul>
      )}
      {gone_quiet.length > 0 && (
        <ul
          className="digest-project-list"
          aria-label={t("home.digestGoneQuiet")}
        >
          {gone_quiet.map((item) => (
            <li key={item.project_id}>
              <EntityRef kind="project" id={item.project_id} />{" "}
              <span className="t-caption">
                {t("home.digestQuietDays", { days: item.days_quiet })}
              </span>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

function DigestSection() {
  const t = useT();
  const { locale } = useLocale();
  const digestQuery = useMorningDigest();
  return (
    <QueryGate query={digestQuery}>
      {(digest) => {
        if (digest === null) {
          return null;
        }
        const { capture, review, connectors, projects } = digest;
        const unhealthyConnectors = connectors.filter(
          (c) => c.status != null && isUnhealthy(c.status),
        );
        return (
          <section aria-label={t("home.digest")}>
            <Card className="digest-card" data-testid="digest-card">
              <div className="digest-head">
                <span className="digest-title">{t("home.digest")}</span>
                <span className="t-caption">
                  {t("home.digestFor", {
                    date: formatDate(digest.date, locale, RECORD_ZONE),
                  })}
                </span>
              </div>
              <div className="digest-stats">
                <DigestStat
                  value={capture.messages_synced ?? 0}
                  label="home.digestSynced"
                />
                <DigestStat
                  value={capture.people_created ?? 0}
                  label="home.digestPeople"
                />
                <DigestStat
                  value={capture.organizations_created ?? 0}
                  label="home.digestOrgs"
                />
                <DigestStat
                  value={review.approvals_pending ?? 0}
                  label="home.digestApprovals"
                />
                <button
                  type="button"
                  className="digest-stat digest-dedupe"
                  onClick={() => navigate({ screen: "dedupe" })}
                >
                  <span className="digest-stat-value t-h2 t-mono">
                    {review.dedupe_open ?? 0}
                  </span>
                  <span className="digest-stat-label t-caption">
                    {t("home.digestDedupe")} <ArrowRight aria-hidden />
                  </span>
                </button>
              </div>
              <p className="digest-classify t-caption">
                {t("home.digestClassify", {
                  commitments: review.classify?.commitments ?? 0,
                  meetings: review.classify?.meetings ?? 0,
                  noise: review.classify?.noise ?? 0,
                })}
              </p>
              {projects && <DigestProjectsSection projects={projects} />}
              {unhealthyConnectors.length > 0 && (
                <button
                  type="button"
                  className="digest-connector-health"
                  data-testid="digest-connector-health"
                  onClick={() =>
                    navigate({ screen: "settings", id: "connections" })
                  }
                >
                  {t(
                    errorClassKey(unhealthyConnectors[0].last_sync_error_class),
                  )}{" "}
                  <ArrowRight aria-hidden />
                </button>
              )}
            </Card>
          </section>
        );
      }}
    </QueryGate>
  );
}

// The §10.1 factor order is normative (winnability · revenue · timing ·
// momentum · warmth) — displaying it in that order keeps the decomposition
// recognizable across surfaces.
const FACTORS: {
  key: keyof components["schemas"]["MorningBriefFeatureVector"];
  label: MessageKey;
}[] = [
  { key: "winnability", label: "home.factorWinnability" },
  { key: "revenue", label: "home.factorRevenue" },
  { key: "timing", label: "home.factorTiming" },
  { key: "momentum", label: "home.factorMomentum" },
  { key: "warmth", label: "home.factorWarmth" },
];

function FactorBars({
  vector,
  itemId,
}: Readonly<{
  vector: components["schemas"]["MorningBriefFeatureVector"];
  itemId: string;
}>) {
  const t = useT();
  return (
    <div className="brief-factors" title={t("home.why")}>
      {FACTORS.map((factor) => {
        const value = Math.max(0, Math.min(1, vector[factor.key]));
        return (
          <div className="brief-factor" key={`${itemId}-${factor.key}`}>
            <span className="brief-factor-label t-caption">
              {t(factor.label)}
            </span>
            <span className="brief-factor-track">
              <span
                className="brief-factor-fill"
                style={{ width: `${Math.round(value * 100)}%` }}
              />
            </span>
          </div>
        );
      })}
    </div>
  );
}

function useBriefItemMark(itemId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (mark: "act" | "dismiss") => {
      const path =
        mark === "act"
          ? "/brief/items/{itemId}/act"
          : "/brief/items/{itemId}/dismiss";
      const { data, error } = await api.POST(path, {
        params: { path: { itemId } },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    onSuccess: (updated) => {
      queryClient.setQueryData<MorningBrief | null>(["brief"], (current) =>
        current
          ? {
              ...current,
              items: current.items.map((item) =>
                item.id === updated.id ? updated : item,
              ),
            }
          : current,
      );
    },
  });
}

function BriefItemCard({ item }: Readonly<{ item: MorningBriefItem }>) {
  const t = useT();
  const { locale } = useLocale();
  const mark = useBriefItemMark(item.id);
  const dealQuery = useQuery({
    queryKey: ["deal", item.deal_id],
    queryFn: async () => {
      const { data, error } = await api.GET("/deals/{id}", {
        params: { path: { id: item.deal_id } },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });

  const deal = dealQuery.data;
  const settled = item.state !== "new";
  const stateLabel: MessageKey =
    item.state === "acted" ? "home.actedState" : "home.dismissedState";

  return (
    // `data-brief-item` was on this row and nothing read it — no test, no story,
    // no spec, no stylesheet. It survives as the card's testId, which is the one
    // per-row hook this primitive forwards.
    <Card
      as="article"
      className={`brief-deal ${settled ? "settled" : ""}`}
      testId={`brief-item-${item.id}`}
    >
      <div className="brief-deal-head">
        <span className="brief-rank">#{item.rank}</span>
        <button
          type="button"
          className="brief-deal-name"
          onClick={() => navigate({ screen: "deals", id: item.deal_id })}
        >
          {deal?.name ?? t("home.openDeal")} <ArrowRight aria-hidden />
        </button>
        {/* Gated on the deal, not on its amount: a deal still on its way has
            no figure to report yet, while a deal we HAVE and nobody priced is
            a fact this line states as absent. Neither half is invented — a
            euro sign over an unknown currency reads as a real EUR figure. */}
        {deal && (
          <span className="brief-deal-amount">
            {formatMoneyOrAbsent(deal.amount_minor, deal.currency, locale)}
          </span>
        )}
        <span className="brief-score t-mono">
          {t("home.score", { pct: Math.round(item.composite * 100) })}
        </span>
      </div>
      <FactorBars vector={item.feature_vector} itemId={item.id} />
      <div className="brief-deal-foot">
        <Badge>
          {item.evidence_ids.length === 1
            ? t("home.evidenceOne")
            : t("home.evidence", { count: item.evidence_ids.length })}
        </Badge>
        {settled ? (
          <Badge tone={item.state === "acted" ? "success" : "warn"}>
            {t(stateLabel)}
          </Badge>
        ) : (
          <span className="brief-deal-actions">
            <Button
              small
              variant="primary"
              disabled={mark.isPending}
              onClick={() => mark.mutate("act")}
            >
              <Check aria-hidden /> {t("home.act")}
            </Button>
            <Button
              small
              disabled={mark.isPending}
              onClick={() => mark.mutate("dismiss")}
            >
              <X aria-hidden /> {t("home.dismiss")}
            </Button>
          </span>
        )}
      </div>
      {mark.isError && (
        <p className="t-caption" style={{ color: "var(--danger)" }}>
          {problemMessageOf(mark.error, t)}
        </p>
      )}
    </Card>
  );
}

function honestCountLine(
  t: ReturnType<typeof useT>,
  brief: MorningBrief,
): string {
  if (brief.candidate_count > brief.items.length) {
    return t("home.overflow", {
      shown: brief.items.length,
      count: brief.candidate_count,
    });
  }
  return t("home.honestShort", { count: brief.candidate_count });
}

function BriefSection() {
  const t = useT();
  const { locale } = useLocale();
  const queryClient = useQueryClient();
  const briefQuery = useMorningBrief();

  const refresh = useMutation({
    mutationFn: async () => {
      const { data, error } = await api.POST("/brief");
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    onSuccess: (data) => {
      queryClient.setQueryData(["brief"], data ?? null);
    },
  });

  const refreshButton = (label: MessageKey) => (
    <Button
      small
      disabled={refresh.isPending}
      onClick={() => refresh.mutate()}
      data-testid="brief-refresh"
    >
      <RefreshCw aria-hidden />{" "}
      {refresh.isPending ? t("home.refreshing") : t(label)}
    </Button>
  );

  return (
    <section aria-label={t("home.queue")}>
      <QueryGate query={briefQuery}>
        {(brief) => {
          if (brief === null) {
            return (
              <Card className="brief-none">
                <SectionHeader
                  title={t("home.noneTitle")}
                  sub={t("home.noneBody")}
                />
                {refreshButton("home.generate")}
                {refresh.isError && (
                  <p className="t-caption" style={{ color: "var(--danger)" }}>
                    {problemMessageOf(refresh.error, t)}
                  </p>
                )}
              </Card>
            );
          }
          return (
            <div>
              <div className="brief-runbar">
                <SectionHeader
                  title={t("home.queue")}
                  sub={t("home.asOf", {
                    at: formatDateTime(brief.as_of, locale, viewerZone()),
                  })}
                />
                {refreshButton("home.refresh")}
              </div>
              {brief.items.length === 0 ? (
                <EmptyState>{t("home.quietRun")}</EmptyState>
              ) : (
                <div className="brief-list">
                  {brief.items.map((item) => (
                    <BriefItemCard key={item.id} item={item} />
                  ))}
                  <p className="t-caption brief-honesty">
                    {honestCountLine(t, brief)}
                  </p>
                </div>
              )}
              {refresh.isError && (
                <p className="t-caption" style={{ color: "var(--danger)" }}>
                  {problemMessageOf(refresh.error, t)}
                </p>
              )}
            </div>
          );
        }}
      </QueryGate>
    </section>
  );
}

/** One currency's open pipeline: what it is worth, and what it is worth once
 *  each deal is weighted by its stage's probability. */
type PipelineValue = {
  currency: string;
  rawMinor: number;
  weightedMinor: number;
  deals: number;
};

/** What the report answered: the per-currency lines, and whether a field mask
 *  kept rows out of them. */
type PipelineReading = {
  rows: PipelineValue[];
  /** Rows a mask withheld from every total here. Non-zero means the figures
   *  understate the pipeline, and saying so is the difference between a
   *  partial answer and a wrong one. */
  excluded: number;
};

/**
 * The open pipeline, per currency.
 *
 * Grouped by currency and rendered one line each rather than summed: adding
 * native minor units across currencies produces a number that is not money,
 * which is the rule the board's mixed-currency columns already follow.
 *
 * The report never includes archived deals, and this asks only for open ones —
 * a won deal is revenue, not pipeline, and counting it here would make the
 * headline grow every time somebody closed something.
 */
function usePipelineValue() {
  return useQuery({
    // Under ["deals"] so the invalidation every deal mutation already fires
    // reaches this too. Keyed apart, the headline went on naming yesterday's
    // pipeline after a rep won something and came back to Home.
    queryKey: ["deals", "home-pipeline-value"],
    queryFn: async (): Promise<PipelineReading> => {
      const { data, error } = await api.POST("/reports/{report}", {
        params: { path: { report: "deals-by-stage" } },
        body: {
          group_by: ["currency"],
          aggregates: [
            { fn: "count", as: "deals" },
            { fn: "sum", field: "amount_minor", as: "raw_minor" },
            { fn: "sum", field: "weighted_amount_minor", as: "weighted_minor" },
          ],
          filters: { status: "open" },
        },
      });
      if (error) {
        throwProblem(error);
      }
      return {
        rows: data.rows.flatMap((row) => {
          const currency = row.currency;
          // A SUM over deals nobody priced is absent, not zero, and a row with
          // no currency cannot be rendered as money at all.
          if (
            typeof currency !== "string" ||
            typeof row.raw_minor !== "number"
          ) {
            return [];
          }
          return [
            {
              currency,
              rawMinor: row.raw_minor,
              weightedMinor:
                typeof row.weighted_minor === "number" ? row.weighted_minor : 0,
              deals: typeof row.deals === "number" ? row.deals : 0,
            },
          ];
        }),
        excluded: data.excluded_by_permission ?? 0,
      };
    },
  });
}

/** The open pipeline at the top of Home: what is in play, and what it is
 *  worth weighted. One line per currency. */
function PipelineValueSection() {
  const t = useT();
  const { locale } = useLocale();
  const query = usePipelineValue();

  // Still loading, or genuinely nothing open: say nothing. A headline has no
  // honest skeleton — a shape where a number will be is a claim that a number
  // is coming, and Home's real work sits directly below it.
  if (query.isPending) {
    return null;
  }
  // A refusal is NOT an absence. An empty tile reads as "there is no pipeline",
  // which is a claim about the data made in place of a claim about authority —
  // the failure the design system calls out by name. So a settled error keeps
  // the tile's place and says the figure is unavailable.
  if (query.isError) {
    return (
      <section aria-label={t("home.pipeline")}>
        <SectionHeader title={t("home.pipeline")} />
        <Card>
          <p className="t-caption">{t("home.pipelineUnavailable")}</p>
        </Card>
      </section>
    );
  }
  const rows = query.data?.rows ?? [];
  const excluded = query.data?.excluded ?? 0;
  if (rows.length === 0) {
    return null;
  }
  return (
    <section aria-label={t("home.pipeline")}>
      <SectionHeader title={t("home.pipeline")} />
      <Card>
        {rows.map((row) => (
          <div key={row.currency} className="home-pipeline-row">
            <span className="t-sub">
              {formatMoneyOrAbsent(row.rawMinor, row.currency, locale)}
            </span>
            <span className="t-caption">
              {t("home.pipelineWeighted", {
                amount: String(
                  formatMoneyOrAbsent(row.weightedMinor, row.currency, locale),
                ),
              })}
            </span>
            <span className="t-caption">
              {t(
                row.deals === 1
                  ? "home.pipelineCount.one"
                  : "home.pipelineCount.other",
                { count: row.deals },
              )}
            </span>
          </div>
        ))}
        {/* A mask kept rows out of these sums, so the figures understate the
            pipeline. Saying so is the difference between a partial answer and
            a wrong one. */}
        {excluded > 0 && (
          <p className="t-caption">
            {t("home.pipelinePartial", { count: excluded })}
          </p>
        )}
      </Card>
    </section>
  );
}

export function HomeScreen() {
  const t = useT();
  // Approving from the morning brief mints an approval_token and can 409
  // already_decided too; both must survive the row's unmount (pending
  // invalidation), so Home uses the same shared sink InboxScreen does — the
  // "shown once" token AND the honest already-decided note show on Home too.
  const { onApproved, onAlreadyDecided, tokenModal, decidedNote } =
    useApprovalTokenSink();
  const approvalsQuery = usePendingApprovals();
  const dealsQuery = useQuery({
    queryKey: ["deals"],
    queryFn: async () => {
      const { data, error } = await api.GET("/deals", {
        params: { query: { limit: 100 } },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });

  const stalled = (dealsQuery.data?.data ?? []).filter(
    (deal) => deal.stalled && deal.status === "open",
  );

  // The three sections are independent surfaces: a transient approvals or
  // deals failure must never hide a healthy /brief queue (and vice versa),
  // so each renders under its own gate.
  return (
    <div className="wrap">
      <SectionHeader title={t("home.brief")} sub={t("home.sub")} />
      {/* Screen-level so it survives the approved/decided row (and its whole
          section) unmounting on the pending invalidation. */}
      {decidedNote}
      <QueryGate query={approvalsQuery} empty={() => false}>
        {(approvals) =>
          approvals.data.length > 0 ? (
            <section aria-label={t("home.staged")}>
              <SectionHeader
                title={t("home.staged")}
                sub={t("brief.nothingSent")}
              />
              {approvals.data.map((approval) => (
                <ApprovalRow
                  key={approval.id}
                  approval={approval}
                  onApproved={onApproved}
                  onAlreadyDecided={onAlreadyDecided}
                />
              ))}
            </section>
          ) : null
        }
      </QueryGate>
      <PipelineValueSection />
      <DigestSection />
      <BriefSection />
      {stalled.length > 0 && (
        <section aria-label={t("home.stalled")}>
          <SectionHeader title={t("home.stalled")} />
          <div style={{ display: "flex", flexDirection: "column", gap: 8 }}>
            {stalled.map((deal) => (
              <DealCard
                key={deal.id}
                deal={{
                  id: deal.id,
                  name: deal.name,
                  org: "",
                  // Both halves as the wire sent them: a stalled deal nobody
                  // priced has no figure, and the card draws that rather than
                  // the zero and the currency it would have to invent.
                  valueMinor: deal.amount_minor ?? null,
                  currency: deal.currency ?? null,
                  ageMs: Math.max(
                    0,
                    Date.now() -
                      new Date(
                        deal.last_activity_at ?? deal.created_at,
                      ).getTime(),
                  ),
                  stalled: true,
                }}
                onOpen={() => navigate({ screen: "deals", id: deal.id })}
              />
            ))}
          </div>
        </section>
      )}
      {tokenModal}
    </div>
  );
}
