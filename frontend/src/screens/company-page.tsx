import { useMutation, useQuery } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { useCan } from "../app/capability";
import { navigate } from "../app/router";
import {
  Badge,
  Button,
  Disclosure,
  EmptyState,
  Modal,
  SectionHeader,
  SegmentedControl,
  Skeleton,
} from "../design-system/atoms";
import {
  RecordView,
  type TimelineEntry,
  type TimelineGroup,
} from "../design-system/composed";
import {
  EvidenceMark,
  type EvidenceMarkSource,
} from "../design-system/evidencemark";
import {
  AutonomyDot,
  ConfidenceMeter,
  EvidenceChip,
} from "../design-system/trust";
import { formatDateTime, formatMoney } from "../format/format";
import { type Locale, useLocale, useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { taskWriteKeys } from "./activitykeys";
import { AssistantPanel } from "./assistant";
import {
  coldFieldLabel,
  LoadMoreButton,
  problemMessageOf,
  provenanceOf,
  QueryGate,
  QueryStates,
  siteReadKindLabel,
  throwProblem,
  useSorMode,
  useViewerId,
} from "./common";
import {
  AccountBrief,
  DealsCard,
  HealthCard,
  NextSteps,
  type Org360Result,
  OverlayFallback,
  PeopleCard,
  RECORD_ZONE,
  SignalsCard,
  StateStrip,
  StateStripSkeleton,
  type SuggestionAction,
  TagsCard,
  useAcknowledgeOrganizationView,
  useOrganization360,
} from "./company360";
import { ListAction, NewDealAction, TagAction } from "./companyactions";
import { CompanyApprovalsPanel } from "./companyapprovals";
import { CompanyCommercialCard } from "./companycommercial";
import { CompanyDocumentsCard } from "./companydocuments";
import { DossierPanel } from "./companydossier";
import { type CitedRecord, EvidenceModal } from "./companyevidence";
import { CompanyFinanceCard } from "./companyfinance";
import { GrowthFitPanel } from "./companygrowthfit";
import {
  CompanyActionBadges,
  CompanyChips,
  CompanyDescription,
  CompanyPrimaryActions,
  CompanyPulse,
} from "./companyheader";
import { TodayOnThisAccount } from "./companytoday";
import { ComposeModal, TimelineActions } from "./compose";
import { CustomFieldsCard } from "./customfields.card";
import {
  EvidenceVerdict,
  factClaim,
  profileFieldClaim,
} from "./evidenceverdict";
import { type FactGroup, factFieldLabelKey, groupFacts } from "./factview";
import { changeTimeline, RecordHistoryTab, useFieldHistory } from "./history";
import { mergeChronology } from "./history.logic";
import { confidenceLevel } from "./inbox";
import { LogActivityAction } from "./logactivity";
import { LIFECYCLE_LABELS, RELATIONSHIP_TYPE_LABELS } from "./organizations";
import { PartnerTab } from "./partners";
import { activityTimeline } from "./people";
import { RelationshipsTab } from "./relationships";
import {
  TaskDetailModal,
  TaskQuickActions,
  useTaskUpdate,
} from "./taskactions";
import { groupChronology } from "./timelinegroups";

// The company RECORD page: the account as one reading — its header, the tabs
// that switch what is read about it, the card grid of the overview, and the
// chronology beneath. The companies LIST stays in organizations.tsx; the two
// share only the form mappers a record edit and a record create both need.

type Organization = components["schemas"]["Organization"];
type Organization360View = components["schemas"]["Organization360"];
type CompanyProfileField = components["schemas"]["CompanyProfileField"];
type OrganizationFact = components["schemas"]["OrganizationFact"];

function EnrichCard({ orgId }: Readonly<{ orgId: string }>) {
  const t = useT();
  const enrich = useMutation({
    mutationFn: async () => {
      const { data, error } = await api.POST("/organizations/{id}/enrich", {
        params: { path: { id: orgId } },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });

  return (
    <section className="card" style={{ marginBottom: "var(--space-4)" }}>
      <div className="list-head">
        <SectionHeader title={t("enrich.title")} sub={t("enrich.sub")} />
        <Button
          small
          disabled={enrich.isPending}
          onClick={() => enrich.mutate()}
        >
          {enrich.isPending ? t("enrich.reading") : t("enrich.cta")}
        </Button>
      </div>
      {enrich.isError && (
        <p className="t-caption" style={{ color: "var(--danger)" }}>
          {problemMessageOf(enrich.error, t)}
        </p>
      )}
      {enrich.data && (
        <div>
          <p
            style={{
              display: "flex",
              alignItems: "center",
              gap: "var(--space-2)",
              flexWrap: "wrap",
              margin: "6px 0 12px",
            }}
          >
            <AutonomyDot tier="confirm" />
            <span className="t-small">{t("enrich.staged")}</span>
            <Button small onClick={() => navigate({ screen: "inbox" })}>
              {t("enrich.toInbox")}
            </Button>
          </p>
          <p className="t-caption" style={{ marginBottom: "var(--space-3)" }}>
            {t("enrich.from", { url: enrich.data.source_url })}
          </p>
          {enrich.data.fields.map((field) => {
            const level = confidenceLevel(field.confidence);
            return (
              <div key={field.field} style={{ marginBottom: "var(--space-3)" }}>
                <div
                  style={{
                    display: "flex",
                    alignItems: "center",
                    gap: "var(--space-2)",
                    marginBottom: "var(--space-1)",
                  }}
                >
                  <span className="t-label">
                    {coldFieldLabel(field.field, t)}
                  </span>
                  {level && <ConfidenceMeter level={level} />}
                </div>
                <div>{field.value}</div>
                {field.evidence_snippet && (
                  <EvidenceChip
                    evidence={{
                      snippet: field.evidence_snippet,
                      source: field.source_url ?? "",
                    }}
                  />
                )}
              </div>
            );
          })}
        </div>
      )}
    </section>
  );
}

type SiteReadReport = components["schemas"]["SiteReadReport"];

const SITE_READ_STATUS_LABELS: Record<SiteReadReport["status"], MessageKey> = {
  queued: "deepread.statusQueued",
  deferred: "deepread.statusDeferred",
  running: "deepread.statusRunning",
  done: "deepread.statusDone",
  partial: "deepread.statusPartial",
  cancelled: "deepread.statusCancelled",
  failed: "deepread.statusFailed",
};

const SITE_READ_STOP_LABELS: Record<
  NonNullable<SiteReadReport["stopped_reason"]>,
  MessageKey
> = {
  budget: "deepread.stopBudget",
  page_cap: "deepread.stopPageCap",
  byte_cap: "deepread.stopByteCap",
  deadline: "deepread.stopDeadline",
};

const SITE_READ_SKIP_LABELS: Record<
  components["schemas"]["SiteReadSkip"]["reason"],
  MessageKey
> = {
  robots: "deepread.skipRobots",
  off_domain: "deepread.skipOffDomain",
  page_cap: "deepread.skipPageCap",
  byte_cap: "deepread.skipByteCap",
  unreadable: "deepread.skipUnreadable",
};

// Trims the scheme and clamps long paths so the pages/skips lists stay
// scannable; the full URL survives on the title attribute.
function shortUrl(url: string): string {
  const bare = url.replace(/^https?:\/\//, "");
  return bare.length > 60 ? `${bare.slice(0, 59)}…` : bare;
}

function SiteReadDeferral({ report }: Readonly<{ report: SiteReadReport }>) {
  const t = useT();
  const { locale } = useLocale();
  if (report.status !== "deferred") {
    return null;
  }
  return (
    <p className="t-small" style={{ margin: "var(--space-2) 0 0" }}>
      {report.status_detail}
      {report.next_attempt_at && (
        <>
          {" "}
          {t("deepread.resumesAt", {
            when: formatDateTime(
              report.next_attempt_at,
              locale,
              "Europe/Berlin",
            ),
          })}
        </>
      )}
    </p>
  );
}

// The polled half of the deep read: renders progress while the crawl is in
// flight (3s poll, stops on a terminal status) and the full account when it
// ends — pages read, pages SKIPPED and why, and the stop reason when the
// crawl ended early. The skip/stop rendering is the transparency surface: a
// truncated crawl must never read as complete.
function SiteReadPanel({
  orgId,
  readId,
}: Readonly<{ orgId: string; readId: string }>) {
  const t = useT();
  const reportQuery = useQuery({
    queryKey: ["site-read", orgId, readId],
    queryFn: async () => {
      const { data, error } = await api.GET(
        "/organizations/{id}/site-reads/{readId}",
        { params: { path: { id: orgId, readId } } },
      );
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    refetchInterval: (query) => {
      const status = query.state.data?.status;
      if (status === "queued" || status === "running") {
        return 3000;
      }
      return status === "deferred" ? 60_000 : false;
    },
  });

  if (reportQuery.isPending) {
    return <Skeleton width="60%" />;
  }
  if (reportQuery.isError) {
    return (
      <p className="t-caption" style={{ color: "var(--danger)" }}>
        {problemMessageOf(reportQuery.error, t)}
      </p>
    );
  }

  const report = reportQuery.data;
  const terminal =
    report.status === "done" ||
    report.status === "partial" ||
    report.status === "failed";

  return (
    <div style={{ marginTop: "var(--space-3)" }}>
      <p
        style={{
          display: "flex",
          alignItems: "center",
          gap: "var(--space-2)",
          flexWrap: "wrap",
          margin: 0,
        }}
      >
        <Badge tone={report.status === "failed" ? "danger" : undefined}>
          {t(SITE_READ_STATUS_LABELS[report.status])}
        </Badge>
        <span className="t-small">
          {t(
            report.pages.length === 1
              ? "deepread.pagesSoFar.one"
              : "deepread.pagesSoFar.other",
            { count: report.pages.length },
          )}
        </span>
        {terminal && (
          <span className="t-small">
            {t(
              (report.fact_count ?? 0) === 1
                ? "deepread.factCount.one"
                : "deepread.factCount.other",
              { count: report.fact_count ?? 0 },
            )}
          </span>
        )}
      </p>
      <SiteReadDeferral report={report} />
      {report.stopped_reason && (
        <p style={{ margin: "var(--space-2) 0 0" }}>
          <Badge tone="warn">
            {t("deepread.stoppedEarly", {
              reason: t(SITE_READ_STOP_LABELS[report.stopped_reason]),
            })}
          </Badge>
        </p>
      )}
      {terminal && report.proposal_ids.length > 0 && (
        <p
          style={{
            display: "flex",
            alignItems: "center",
            gap: "var(--space-2)",
            flexWrap: "wrap",
            margin: "var(--space-3) 0 0",
          }}
        >
          <AutonomyDot tier="confirm" />
          <span className="t-small">
            {report.proposal_ids.length === 1
              ? t("deepread.proposalsOne")
              : t("deepread.proposals", { count: report.proposal_ids.length })}
          </span>
          <Button small onClick={() => navigate({ screen: "inbox" })}>
            {t("enrich.toInbox")}
          </Button>
        </p>
      )}
      {terminal && report.pages.length > 0 && (
        <div style={{ marginTop: "var(--space-3)" }}>
          <span className="t-label">{t("deepread.pagesRead")}</span>
          <ul
            className="t-small"
            style={{
              listStyle: "none",
              margin: "var(--space-2) 0 0",
              padding: 0,
              display: "flex",
              flexDirection: "column",
              gap: "var(--space-1)",
            }}
          >
            {report.pages.map((page) => (
              <li key={page.url}>
                <Badge>{siteReadKindLabel(page.kind, t)}</Badge>{" "}
                <span className="t-mono" title={page.url}>
                  {shortUrl(page.url)}
                </span>
              </li>
            ))}
          </ul>
        </div>
      )}
      {terminal && report.skipped.length > 0 && (
        <div style={{ marginTop: "var(--space-3)" }}>
          <span className="t-label">{t("deepread.skippedPages")}</span>
          <ul
            className="t-small"
            style={{
              listStyle: "none",
              margin: "var(--space-2) 0 0",
              padding: 0,
              display: "flex",
              flexDirection: "column",
              gap: "var(--space-1)",
            }}
          >
            {report.skipped.map((skip) => (
              <li key={skip.url}>
                <span className="t-mono" title={skip.url}>
                  {shortUrl(skip.url)}
                </span>{" "}
                <Badge tone="warn">
                  {t(SITE_READ_SKIP_LABELS[skip.reason])}
                </Badge>
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  );
}

// The whole-site deep read (A102/R2), the enrich verb's big sibling: one
// click starts (or joins — idempotent per org+url) a background crawl of the
// company's own site; findings stage as 🟡 proposals for the inbox, nothing
// writes to the record here. 422 (no website on file) and 501 (crawl seam
// unwired) surface their honest cause instead of a generic failure.
function DeepReadCard({ orgId }: Readonly<{ orgId: string }>) {
  const t = useT();
  const [readId, setReadId] = useState<string | null>(null);
  const start = useMutation({
    mutationFn: async () => {
      const { data, error, response } = await api.POST(
        "/organizations/{id}/deep-read",
        { params: { path: { id: orgId } } },
      );
      if (error) {
        // 501 means the crawl seam is unwired, which the server states in its
        // own terms and the card states in the reader's. Either way this stays
        // a problem, so the render below can tell it from a bug in here.
        throwProblem(
          response.status === 501
            ? { title: t("deepread.unavailable") }
            : error,
        );
      }
      return data;
    },
    onSuccess: (started) => setReadId(started.read_id),
  });

  return (
    <section className="card" style={{ marginBottom: "var(--space-4)" }}>
      <div className="list-head">
        <SectionHeader title={t("deepread.title")} sub={t("deepread.sub")} />
        <Button small disabled={start.isPending} onClick={() => start.mutate()}>
          {start.isPending ? t("deepread.starting") : t("deepread.cta")}
        </Button>
      </div>
      {start.isError && (
        <p className="t-caption" style={{ color: "var(--danger)" }}>
          {problemMessageOf(start.error, t)}
        </p>
      )}
      {readId && <SiteReadPanel orgId={orgId} readId={readId} />}
    </section>
  );
}

type OrganizationHierarchyRollup =
  components["schemas"]["OrganizationHierarchyRollup"];

// A missing stored FX rate fails the whole rollup read with 422
// fx_rate_unavailable (never a rate-of-1 substitute, never zeros) — this
// marker lets the render branch on that ONE cause without re-parsing the
// problem body a second time.
class FxUnavailableError extends Error {}

async function fetchHierarchyRollup(
  orgId: string,
): Promise<OrganizationHierarchyRollup> {
  const { data, error } = await api.GET(
    "/organizations/{id}/hierarchy-rollup",
    {
      params: { path: { id: orgId }, query: { scope: "tree" } },
    },
  );
  if (error) {
    if (error.code === "fx_rate_unavailable") {
      throw new FxUnavailableError();
    }
    throwProblem(error);
  }
  return data;
}

// P-7: the org hierarchy roll-up (weighted pipeline, current-quarter
// closed-won, 30-day activity, aggregated account count), read-only. Money
// renders only when both amount_minor and currency are present (Money's
// fields are individually optional on the wire) — never a hand-formatted or
// zero-filled figure.
function HierarchyRollupCard({ orgId }: Readonly<{ orgId: string }>) {
  const t = useT();
  const { locale } = useLocale();
  const rollupQuery = useQuery({
    queryKey: ["rollup", orgId],
    queryFn: () => fetchHierarchyRollup(orgId),
  });

  if (rollupQuery.isPending) {
    return (
      <div
        style={{
          display: "flex",
          flexDirection: "column",
          gap: "var(--space-3)",
        }}
      >
        <Skeleton width="60%" />
        <Skeleton width="90%" />
        <Skeleton width="75%" />
      </div>
    );
  }
  if (rollupQuery.isError) {
    if (rollupQuery.error instanceof FxUnavailableError) {
      return <EmptyState>{t("rollup.fxUnavailable")}</EmptyState>;
    }
    return <EmptyState>{problemMessageOf(rollupQuery.error, t)}</EmptyState>;
  }

  const rollup = rollupQuery.data;
  const money = (value: OrganizationHierarchyRollup["weighted_pipeline"]) =>
    value.amount_minor != null && value.currency
      ? formatMoney(value.amount_minor, value.currency, locale)
      : "—";

  return (
    <section className="card" style={{ marginBottom: "var(--space-4)" }}>
      <SectionHeader title={t("tab.rollup")} />
      <dl className="firmo">
        <div>
          <dt>{t("rollup.weightedPipeline")}</dt>
          <dd className="t-mono">{money(rollup.weighted_pipeline)}</dd>
        </div>
        <div>
          <dt>{t("rollup.closedWon")}</dt>
          <dd className="t-mono">{money(rollup.closed_won)}</dd>
        </div>
        <div>
          <dt>{t("rollup.activity30d")}</dt>
          <dd>{rollup.activity_count_30d}</dd>
        </div>
        <div>
          <dt>{t("rollup.accounts")}</dt>
          <dd>{rollup.aggregated_account_count}</dd>
        </div>
      </dl>
      {rollup.restricted_excluded.length > 0 && (
        <p className="t-caption" style={{ marginTop: "var(--space-3)" }}>
          {t("rollup.excluded", { count: rollup.restricted_excluded.length })}
        </p>
      )}
      <p className="t-caption" style={{ marginTop: "var(--space-3)" }}>
        {t("rollup.computedAt", {
          when: formatDateTime(rollup.computed_at, locale, "Europe/Berlin"),
        })}
      </p>
    </section>
  );
}

// One confirmed profile field (S-E02): the human field label, the value, and
// a footer that names where it came from — provenance, confidence when the
// read carried one, and the grounding evidence snippet. Mirrors EnrichCard's
// field row, but these are ACCEPTED values on the record, not staged proposals.
// The shared trust-signal footer for an evidence-backed row: provenance
// always, confidence whenever graded, and the evidence snippet when present.
// One spelling for profile fields and facts so the "confidence is never
// hidden" convention can't drift between them.
// derivedSource builds the evidence mark's payload for a value the system
// read rather than a person typed. A value a HUMAN entered gets no mark: the
// record is full of human-entered values, and marking them all would make
// the underline mean nothing.
function derivedSource(
  row: Readonly<{
    captured_by?: string;
    confidence?: number | null;
    evidence_snippet?: string | null;
    source_url?: string | null;
    updated_at?: string;
  }>,
  locale: Locale,
): EvidenceMarkSource | undefined {
  const provenance = provenanceOf(row.captured_by);
  if (provenance.kind === "human") {
    return undefined;
  }
  return {
    provenance,
    confidence: confidenceLevel(row.confidence) ?? undefined,
    snippet: row.evidence_snippet,
    sourceUrl: row.source_url,
    at: row.updated_at
      ? formatDateTime(row.updated_at, locale, RECORD_ZONE)
      : undefined,
  };
}

// PROFILE_FIELD_LABELS names the profile fields as statements ABOUT a company.
//
// The same fields are asked of the reader during onboarding, where the second
// person is right — "What do you sell?" is a question to us. On a prospect's
// record that framing put the reader in the wrong chair: the page appeared to
// be interviewing us about a company we are trying to sell to.
const PROFILE_FIELD_LABELS: Record<string, MessageKey> = {
  display_name: "co.profileField.display_name",
  offer_summary: "co.profileField.offer_summary",
  icp: "co.profileField.icp",
  buying_center: "co.profileField.buying_center",
  value_proposition: "co.profileField.value_proposition",
  usp: "co.profileField.usp",
  customer_pains: "co.profileField.customer_pains",
  desired_outcomes: "co.profileField.desired_outcomes",
  buying_intents: "co.profileField.buying_intents",
  common_objections: "co.profileField.common_objections",
  sales_motion: "co.profileField.sales_motion",
  legal_name: "co.profileField.legal_name",
  registered_address: "co.profileField.registered_address",
  register_vat: "co.profileField.register_vat",
  industry: "co.profileField.industry",
  history: "co.profileField.history",
};

// The onboarding wording is the fallback, so a field added there still reads
// as words rather than as a column name here.
function profileFieldLabel(field: string, t: ReturnType<typeof useT>): string {
  const key = PROFILE_FIELD_LABELS[field];
  return key ? t(key) : coldFieldLabel(field, t);
}

function ProfileFieldRow({
  orgId,
  field,
  onOpenHistory,
}: Readonly<{
  orgId: string;
  field: CompanyProfileField;
  onOpenHistory?: () => void;
}>) {
  const t = useT();
  const { locale } = useLocale();
  const canEdit = useCan("organization", "update");
  return (
    <div className="co-field">
      <span className="t-label">{profileFieldLabel(field.field, t)}</span>
      <div>
        <EvidenceMark
          value={field.value}
          source={derivedSource(field, locale)}
          onOpenHistory={onOpenHistory}
        />
        {/* The verdict beside the value, not inside the mark: the mark says
            where the value came from, and this says what the reader makes of
            it. Folding the second into the first would hide the only action on
            the page that stops a wrong extraction being rewritten tomorrow. */}
        <EvidenceVerdict
          orgId={orgId}
          claim={profileFieldClaim(orgId, field)}
          canEdit={canEdit}
        />
      </div>
    </div>
  );
}

// The Firmographics & legal card: the org's confirmed profile fields, rendered
// evidence-or-omit — a field with no stored value is simply absent, never
// guessed. An empty read is stated honestly ("nothing read yet"), never
// fabricated into blank rows. This card carries the region's loading/error
// surface; the sibling facts card stays silent when it has nothing to add.
function ProfileFieldsCard({
  orgId,
  onOpenHistory,
}: Readonly<{ orgId: string; onOpenHistory?: () => void }>) {
  const t = useT();
  const fieldsQuery = useQuery({
    queryKey: ["org-profile-fields", orgId],
    queryFn: async () => {
      const { data, error } = await api.GET(
        "/organizations/{id}/profile-fields",
        { params: { path: { id: orgId } } },
      );
      if (error) {
        throwProblem(error);
      }
      return data.data ?? [];
    },
  });

  return (
    <section className="card" style={{ marginBottom: "var(--space-4)" }}>
      <SectionHeader title={t("co.profile.title")} />
      <QueryStates query={fieldsQuery}>
        {fieldsQuery.data && fieldsQuery.data.length === 0 ? (
          <p className="t-caption">{t("org.firmographicsEmpty")}</p>
        ) : (
          (fieldsQuery.data ?? []).map((field) => (
            <ProfileFieldRow
              key={field.field}
              orgId={orgId}
              field={field}
              onOpenHistory={onOpenHistory}
            />
          ))
        )}
      </QueryStates>
    </section>
  );
}

// Facts read from the site, grouped into the four fixed categories. Empty
// categories are omitted and an empty read renders nothing at all — the
// profile card above already carries the region's honest empty state, so a
// second "nothing here" would only be noise.
//
// Ordering and duplicate collapsing live in factview.ts; the category order
// comes from there too, so the card has one source for what it draws.
//
// FACT_PREVIEW is how many rows of a category are shown before the reader asks
// for the rest. A real account returns ninety-odd facts, and rendering them all
// made this card taller than the page it sits beside — at which point nobody
// reads any of it.
const FACT_PREVIEW = 5;

const FACT_CATEGORY_LABELS: Record<OrganizationFact["category"], MessageKey> = {
  company: "org.factCategory.company",
  offering: "org.factCategory.offering",
  market: "org.factCategory.market",
  signal: "org.factCategory.signal",
};

// One fact row: the value carries its own evidence mark, the same
// affordance every derived value on this page uses.
// Why a fact looks wrong, in the words a reader can act on. Typed against the
// schema union, so a rule added upstream fails the build here rather than
// rendering a raw reason code.
type FactSuspectReason = NonNullable<OrganizationFact["suspect_reason"]>;

const FACT_SUSPECT_LABELS: Record<FactSuspectReason, MessageKey> = {
  phone_shaped_location: "co.factSuspect.phoneShapedLocation",
  not_a_phone: "co.factSuspect.notAPhone",
  not_a_year: "co.factSuspect.notAYear",
  not_an_email: "co.factSuspect.notAnEmail",
  not_a_size: "co.factSuspect.notASize",
};

function FactRow({
  orgId,
  fact,
  onOpenHistory,
}: Readonly<{
  orgId: string;
  fact: OrganizationFact;
  onOpenHistory?: () => void;
}>) {
  const t = useT();
  const { locale } = useLocale();
  const canEdit = useCan("organization", "update");
  return (
    <div className="co-field">
      <span className="t-label">{t(factFieldLabelKey(fact.field))}</span>
      <div>
        <EvidenceMark
          value={fact.value}
          source={derivedSource(fact, locale)}
          onOpenHistory={onOpenHistory}
        />
        {/* The value contradicts its own field — a phone number filed as a
            location, a register number filed as a headcount. The fact is still
            shown with its evidence: hiding it would be a worse answer than
            flagging it, and the reader is the one who can tell. */}
        {fact.suspect_reason && (
          <span className="co-fact-suspect">
            <Badge tone="warn">
              {t(FACT_SUSPECT_LABELS[fact.suspect_reason])}
            </Badge>
          </span>
        )}
        {/* A flagged fact is exactly the one a reader should be able to fix
            without leaving the page — the flag says the machine doubts itself,
            and only a person can settle it. */}
        <EvidenceVerdict
          orgId={orgId}
          claim={factClaim(orgId, fact)}
          canEdit={canEdit}
        />
      </div>
    </div>
  );
}

// One category of facts. Only the first few rows are drawn until the reader
// asks for the rest, and the count of what is hidden is on the button — a
// truncated list with no number reads as "that is everything".
function FactCategory({
  orgId,
  group,
  onOpenHistory,
}: Readonly<{
  orgId: string;
  group: FactGroup;
  onOpenHistory?: () => void;
}>) {
  const t = useT();
  const [expanded, setExpanded] = useState(false);
  const hidden = group.facts.length - FACT_PREVIEW;
  const shown = expanded ? group.facts : group.facts.slice(0, FACT_PREVIEW);
  return (
    <div className="co-facts-group">
      <div className="t-label co-facts-heading">
        {t(FACT_CATEGORY_LABELS[group.category])}
      </div>
      {shown.map((fact) => (
        <FactRow
          key={`${fact.field}:${fact.value_key}`}
          orgId={orgId}
          fact={fact}
          onOpenHistory={onOpenHistory}
        />
      ))}
      {hidden > 0 && (
        <Button small onClick={() => setExpanded(!expanded)}>
          {expanded
            ? t("co.facts.showLess")
            : t("co.facts.showAll", { count: group.facts.length })}
        </Button>
      )}
    </div>
  );
}

function FactsCard({
  orgId,
  onOpenHistory,
}: Readonly<{ orgId: string; onOpenHistory?: () => void }>) {
  const t = useT();
  const factsQuery = useQuery({
    queryKey: ["org-facts", orgId],
    queryFn: async () => {
      const { data, error } = await api.GET("/organizations/{id}/facts", {
        params: { path: { id: orgId } },
      });
      if (error) {
        throwProblem(error);
      }
      return data.data ?? [];
    },
  });

  // A read that failed is surfaced, never swallowed as "no facts" — an empty
  // read and a 404/network error must stay distinguishable and retryable.
  if (factsQuery.isError) {
    return (
      <section className="card" style={{ marginBottom: "var(--space-4)" }}>
        <SectionHeader title={t("org.facts")} />
        <QueryStates query={factsQuery}>{null}</QueryStates>
      </section>
    );
  }

  // Otherwise facts are supplementary: while the read is in flight, or if it
  // has nothing to show, the card stays absent rather than flashing a skeleton
  // or an empty shell next to the profile card that owns the region's states.
  const facts = factsQuery.data;
  if (!facts || facts.length === 0) {
    return null;
  }

  return (
    <section className="card" style={{ marginBottom: "var(--space-4)" }}>
      <SectionHeader title={t("org.facts")} />
      {groupFacts(facts).map((group) => (
        <FactCategory
          key={group.category}
          orgId={orgId}
          group={group}
          onOpenHistory={onOpenHistory}
        />
      ))}
    </section>
  );
}

// Two tabs, not three. The company view is ONE scrolling page: the
// relationship edges and the hierarchy roll-up moved into its rails, where a
// rep reads them alongside everything else instead of hunting for them.
//
// History is no longer one of them. Field changes belong in the account's own
// chronology (the timeline's Changes filter), and the audit spine is an
// inspection of the record rather than part of its story — it opens from the
// header's overflow menu. A tab of equal weight beside the account's story
// said the two were the same kind of question. Partner stays a tab: it is a
// form, not a reading of this account.
const COMPANY_TABS = [
  "overview",
  "context",
  "people",
  "timeline",
  "partner",
] as const;
type CompanyTab = (typeof COMPANY_TABS)[number];

// Partner is not a permanent tab. It renders the partner programme —
// certification, role, margin tier — which is a form about a commercial
// arrangement the overwhelming majority of accounts do not have. A tab that
// is empty on nearly every record teaches the reader to skip the tab strip.
//
// It shows for an account that HAS a partner programme, and for the reader
// who just asked to set one up (the overflow menu switches the tab, which is
// what `tab` already carries) — so the only path to a first partner row is
// still open, and the form stops greeting everyone else.
function companyTabsFor(
  org: Organization,
  tab: CompanyTab,
): readonly CompanyTab[] {
  // Gated on the relationship type, not on `org.partner`: the Organization
  // read does not select the extension row, so that field is always absent
  // and every partner would lose the tab. The type is equivalent and IS
  // returned — an org carries it exactly when it has a programme, which the
  // store enforces in both directions (ADR-0079/A124).
  const isPartner = (org.relationship_types ?? []).includes("partner");
  return isPartner || tab === "partner"
    ? COMPANY_TABS
    : (["overview", "context", "people", "timeline"] as const);
}

// Which slice of the account's chronology is on screen. Activities is what
// happened WITH them, changes is what happened TO the record; a reader who
// wants them in one order picks "all".
const TIMELINE_FILTERS = ["activities", "changes", "all"] as const;
type TimelineFilter = (typeof TIMELINE_FILTERS)[number];

type Activity = components["schemas"]["Activity"];
type ChangesQuery = ReturnType<typeof useFieldHistory>;

// The company 360 badge/action bar. Archived records are read-only: the
// backend rejects edit/merge/archive on a non-live row (there is no unarchive
// path), so those buttons would only 404 — the Archived badge is the whole
// affordance. Extracted from CompanyScreen so its render stays legible.
// The company's edit form. Its own component because it owns three reads the
// rest of the action bar has no use for — the custom-field catalogue, the user
// roster behind the owner picker, and the record slice they prefill.
export function CompanyScreen({ id }: Readonly<{ id: string }>) {
  const t = useT();
  const [tab, setTab] = useState<CompanyTab>("overview");
  const view = useOrganization360(id);
  // Only an assembled 360 counts as a visit: in overlay mode there is no
  // baseline to advance, and a page that never rendered the account is not
  // one the reader saw.
  useAcknowledgeOrganizationView(id, view.data?.state === "ready");
  // The account itself still comes from its own read: the 360 refuses
  // entirely in overlay mode, and the header must render either way.
  const orgQuery = useQuery({
    queryKey: ["organization", id],
    queryFn: async () => {
      const { data, error } = await api.GET("/organizations/{id}", {
        params: { path: { id } },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });

  return (
    <div className="wrap">
      <QueryGate query={orgQuery}>
        {(org) => (
          <CompanyRecord org={org} view={view} tab={tab} onTab={setTab} t={t} />
        )}
      </QueryGate>
    </div>
  );
}

// CompanyRecord renders the page once the account itself has loaded. Split
// out so the 360's three states — assembling, assembled, refused because the
// workspace reads elsewhere — are handled in one place rather than nested
// inside the account gate.
function CompanyRecord({
  org,
  view,
  tab,
  onTab,
  t,
}: Readonly<{
  org: Organization;
  view: { data?: Org360Result; isPending: boolean; isError: boolean };
  tab: CompanyTab;
  onTab: (next: CompanyTab) => void;
  t: ReturnType<typeof useT>;
}>) {
  const sorMode = useSorMode();
  const assembled = view.data?.state === "ready" ? view.data.view : undefined;
  // Two sources say "this workspace reads elsewhere", and either is enough.
  // The 360 refuses with a 422, and /me reports the mode directly — a
  // workspace that flipped mode after this page cached its read would
  // otherwise keep serving a native-looking company view.
  const overlay = view.data?.state === "overlay" || sorMode === "overlay";
  const visibleTabs = companyTabsFor(org, tab);
  // One tab is not a choice. A segmented control with a single option is a
  // button that does nothing, so the strip disappears entirely rather than
  // asking the reader to pick the page they are already on.
  const tabs =
    visibleTabs.length > 1 ? (
      <SegmentedControl
        options={visibleTabs}
        value={tab}
        onChange={onTab}
        label={t("record.tabs")}
        labels={{
          overview: t("tab.overview"),
          context: t("tab.context"),
          people: t("tab.people"),
          timeline: t("tab.timeline"),
          partner: t("tab.partner"),
        }}
      />
    ) : null;

  // Both tabs render inside ONE page. Partner used to be a different
  // component tree with no rails, so switching tab unmounted both side
  // columns and every query behind them: the grid re-columned under the
  // reader and the page refetched itself on the way back. Only the middle
  // column's body changes now.
  return (
    <CompanyPage
      org={org}
      view={assembled}
      overlay={overlay}
      loading={view.isPending}
      failed={view.isError}
      tab={tab}
      tabs={tabs}
      onTab={onTab}
    />
  );
}

function useAccountChronology({
  orgId,
  filter,
  activities,
  activitiesHaveMore,
  renderActions,
}: Readonly<{
  orgId: string;
  filter: TimelineFilter;
  activities: Activity[];
  activitiesHaveMore: boolean;
  renderActions: (activity: Activity) => ReactNode;
}>): {
  entries: TimelineEntry[];
  truncated: boolean;
  changes: ChangesQuery;
  // What the CURRENT filter is waiting on or failed at. A query that is
  // switched off never resolves — it reports pending forever — so the caller
  // must not read the query's own flags. Reading them turned the default
  // Activities view into a skeleton that never became a timeline.
  loading: boolean;
  failed: boolean;
  // Whether fetching more CHANGES would lengthen the merged view. When the
  // activity feed is the shorter of the two, it is not: the merge cuts at its
  // oldest row and every extra change page falls below that line.
  changesAreTheLimit: boolean;
} {
  const t = useT();
  const viewerId = useViewerId();
  const wantsChanges = filter !== "activities";
  const changes = useFieldHistory("organization", orgId, {
    enabled: wantsChanges,
  });
  const changeRows = changes.data?.pages.flatMap((page) => page.data) ?? [];
  const activityEntries = activityTimeline(activities, viewerId, renderActions);
  const changeEntries = changeTimeline(
    changeRows,
    (field) => coldFieldLabel(field, t),
    viewerId,
  );
  const loading = wantsChanges && changes.isPending;
  const failed = wantsChanges && changes.isError;

  if (filter === "activities") {
    return {
      entries: activityEntries,
      // The 360 caps this section, and a capped list that says nothing reads
      // as the whole history: a rep looking at the oldest of 25 rows would
      // take it for the day the relationship began.
      truncated: activitiesHaveMore,
      changes,
      loading: false,
      failed: false,
      changesAreTheLimit: false,
    };
  }
  if (filter === "changes") {
    return {
      entries: changeEntries,
      truncated: false,
      changes,
      loading,
      failed,
      changesAreTheLimit: changes.hasNextPage,
    };
  }
  const merged = mergeChronology<TimelineEntry>(
    [
      { rows: activityEntries, hasMore: activitiesHaveMore },
      { rows: changeEntries, hasMore: changes.hasNextPage },
    ],
    (entry) => entry.atIso,
  );
  // The merged view is cut at the newest "oldest loaded" among the feeds that
  // still have more. Another page of changes only reaches the reader when the
  // change feed owns that cut — i.e. its oldest loaded row is not older than
  // the activity feed's.
  const oldest = (rows: TimelineEntry[]) =>
    rows.length > 0
      ? rows.reduce((a, b) => (a.atIso < b.atIso ? a : b)).atIso
      : undefined;
  const oldestChange = oldest(changeEntries);
  const oldestActivity = oldest(activityEntries);
  const changesAreTheLimit =
    changes.hasNextPage &&
    (!activitiesHaveMore ||
      oldestChange === undefined ||
      oldestActivity === undefined ||
      oldestChange >= oldestActivity);

  return {
    entries: merged.rows,
    truncated: merged.truncated,
    changes,
    loading,
    failed,
    changesAreTheLimit,
  };
}

// The four RecordView slots the chronology section fills: the list, the
// filter above it, the load-more and disclosure below it, and the notice that
// replaces the list when there is nothing honest to draw. Assembled here so
// the page's render reads as a layout rather than as four nested ternaries.
// useResetOnRecord is the chronology filter, owned by the ACCOUNT being read
// rather than by the session. When both records are already cached the route
// swaps one company for another without ever unmounting this component, so a
// reader who checked Changes once met Changes on every account afterwards.
function useResetOnRecord(
  recordId: string,
): [TimelineFilter, (next: TimelineFilter) => void] {
  const [filter, setFilter] = useState<TimelineFilter>("activities");
  const [filterFor, setFilterFor] = useState(recordId);
  if (filterFor !== recordId) {
    setFilterFor(recordId);
    setFilter("activities");
  }
  return [filter, setFilter];
}

type ChronologySlots = {
  timeline?: TimelineEntry[];
  timelineGroups?: readonly TimelineGroup[];
  timelineHeader?: ReactNode;
  timelineFooter?: ReactNode;
  timelineNotice?: ReactNode;
  onOpenThread?: (threadKey: string) => void;
};

// ChronologyFilter narrows the account's own history. It sits ABOVE the list
// rather than in the page's tab strip: it scopes this section, and a control
// that looks like a tab reads as a different page.
function ChronologyFilter({
  filter,
  onFilter,
}: Readonly<{
  filter: TimelineFilter;
  onFilter: (next: TimelineFilter) => void;
}>) {
  const t = useT();
  return (
    <div className="co-tabs">
      <SegmentedControl
        options={TIMELINE_FILTERS}
        value={filter}
        onChange={onFilter}
        label={t("co.chronology.label")}
        labels={{
          activities: t("co.chronology.activities"),
          changes: t("co.chronology.changes"),
          all: t("co.chronology.all"),
        }}
      />
    </div>
  );
}

function useChronologySlots({
  org,
  view,
  overlay,
  loading,
  failed,
  active,
}: Readonly<{
  org: Organization;
  view?: Organization360View;
  overlay: boolean;
  loading: boolean;
  failed: boolean;
  // Whether the chronology is on screen at all. The Partner tab is a form,
  // so it renders no timeline rather than an empty one.
  active: boolean;
}>): {
  slots: ChronologySlots;
  showChanges: () => void;
} {
  const t = useT();
  const [filter, setFilter] = useResetOnRecord(org.id);

  const history = useAccountChronology({
    orgId: org.id,
    filter,
    activities: view?.activities?.data ?? [],
    activitiesHaveMore: view?.activities?.page.has_more ?? false,
    renderActions: (activity) => (
      <TimelineActions
        activity={activity}
        entityType="organization"
        entityId={org.id}
      />
    ),
  });
  // An evidence mark asks "where did this value come from" — the answer is
  // the record's change history, so the mark turns the timeline to Changes
  // rather than opening a screen of its own.
  const showChanges = () => setFilter("changes");

  if (!active) {
    return { slots: { timelineNotice: <span /> }, showChanges };
  }
  // In overlay mode the refusal is stated once, in the body: repeating it over
  // the timeline would read as two separate things being unavailable rather
  // than one page not being assembled.
  if (overlay) {
    return {
      slots: { timeline: history.entries, timelineNotice: <span /> },
      showChanges,
    };
  }
  return {
    showChanges,
    slots: {
      timeline: history.entries,
      // Conversations, not messages. The account's timeline is where the same
      // exchange showed up three times — a product update to three contacts
      // was three rows, and a five-message thread was five.
      timelineGroups: groupChronology(
        history.entries,
        view?.activities?.page.has_more ?? false,
      ),
      timelineHeader: <ChronologyFilter filter={filter} onFilter={setFilter} />,
      timelineFooter: (
        <>
          {/* Where the merged view stops being complete, said out loud.
              Silence here would read as the end of the account's history. */}
          {history.truncated && (
            <p className="t-small">
              {t(
                filter === "activities"
                  ? "co.chronology.truncatedActivities"
                  : "co.chronology.truncated",
              )}
            </p>
          )}
          {/* Only where fetching more changes actually lengthens the list.
              Under "all" the merge is cut at whichever feed is shorter, so if
              the ACTIVITY feed is the constraint, another page of changes is
              filtered straight back out and the button does nothing. */}
          {(filter === "changes" ||
            (filter === "all" && history.changesAreTheLimit)) && (
            <LoadMoreButton query={history.changes} />
          )}
        </>
      ),
      timelineNotice: chronologyNotice(
        {
          // Per filter, because the two feeds fail independently. A 360 that
          // omitted its activities section says nothing about the change
          // feed, and reporting the Changes view as unavailable on that
          // basis hid rows that had loaded perfectly well.
          loading:
            filter === "changes" ? history.loading : loading || history.loading,
          failed:
            filter === "changes" ? history.failed : failed || history.failed,
          assembled: filter === "changes" ? true : Boolean(view?.activities),
          filter,
        },
        history.entries.length,
        t,
      ),
    },
  };
}

// CompanyPage is the page itself: identity and verbs at the top, then three
// zones — what this company IS on the left, what is HAPPENING in the middle,
// the BUSINESS around it on the right.
//
// All three tabs render here. The rails belong to the ACCOUNT, not to the
// overview, so they stay mounted whichever tab is open and the reader keeps
// the firmographics and the business context while reading the partner form
// or the change history.
// The receipt drawer's state: which claim is open, and the ordered list it
// steps through.
//
// The ORDER belongs to the card that offered the chip, not to the drawer. A
// reader who clicked the third citation in a sentence expects "next" to mean
// the fourth citation in THAT sentence — a drawer that built its own order
// would step somewhere they cannot predict. A card with no ordering to give
// passes none, and the drawer draws no arrows rather than guessing one.
function useCitedReceipt() {
  const [cited, setCited] = useState<CitedRecord | null>(null);
  const [list, setList] = useState<readonly CitedRecord[]>([]);
  const open = (
    entityType: string,
    entityId: string,
    siblings?: readonly CitedRecord[],
  ) => {
    if (citationOpensRecord(entityType)) {
      openCitation(entityType, entityId);
      return;
    }
    if (citationHasReceipt(entityType)) {
      setCited({ entityType, entityId });
      setList(siblings ?? []);
    }
  };
  // Wrapping at each end: a reader walking a sentence's citations should not
  // hit a dead stop and have to close the drawer to reach the first one again.
  const step = (direction: -1 | 1) => {
    if (!cited) {
      return;
    }
    const at = list.findIndex(
      (each) =>
        each.entityType === cited.entityType &&
        each.entityId === cited.entityId,
    );
    if (at < 0) {
      return;
    }
    setCited(list[(at + direction + list.length) % list.length]);
  };
  return {
    cited,
    open,
    close: () => setCited(null),
    step: list.length > 1 ? step : undefined,
  };
}

// Where the account stands, as the masthead's own band: the strip while the
// 360 is still assembling, the readings once it has answered, and nothing at
// all when it answered with nothing. Its own component because the labels it
// needs — the lifecycle and relationship vocabularies — are the list screen's,
// and threading two label maps through the page's render only obscured it.
function CompanyStanding({
  view,
  loading,
}: Readonly<{ view?: Organization360View; loading: boolean }>) {
  const t = useT();
  if (loading) {
    return <StateStripSkeleton />;
  }
  if (!view) {
    return null;
  }
  return (
    <StateStrip
      view={view}
      lifecycleLabel={(value) =>
        t(LIFECYCLE_LABELS[value as keyof typeof LIFECYCLE_LABELS])
      }
      relationshipLabels={(values) =>
        values
          .map((value) =>
            t(
              RELATIONSHIP_TYPE_LABELS[
                value as keyof typeof RELATIONSHIP_TYPE_LABELS
              ],
            ),
          )
          .join(" · ")
      }
    />
  );
}

function CompanyPage({
  org,
  view,
  overlay,
  loading,
  failed,
  tab,
  tabs,
  onTab,
}: Readonly<{
  org: Organization;
  view?: Organization360View;
  overlay: boolean;
  loading: boolean;
  // The composite read failed. Distinct from "still loading" and from "the
  // account is empty", because all three would otherwise draw the same
  // blank page and only one of them is a fact about the account.
  failed: boolean;
  tab: CompanyTab;
  tabs: ReactNode;
  // An evidence mark can be clicked from the Partner tab, where the timeline
  // it wants to filter is not on screen; the page has to come back to the
  // Overview before the filter means anything.
  onTab: (next: CompanyTab) => void;
}>) {
  const t = useT();
  const [openTaskId, setOpenTaskId] = useState<string | null>(null);
  // The two surfaces a suggestion's action opens. Held here because both are
  // page-level: the composer anchors on a timeline message, and the task form
  // is the account's own log-activity surface.
  const [replyToActivityId, setReplyToActivityId] = useState<string | null>(
    null,
  );
  const [taskFormOpen, setTaskFormOpen] = useState(false);
  const [decisionsOpen, setDecisionsOpen] = useState(false);
  const [auditOpen, setAuditOpen] = useState(false);
  const receipt = useCitedReceipt();
  // A task written or completed from this page changes the account's timeline,
  // its next steps (both read from the 360) and the standing work queue.
  const taskUpdate = useTaskUpdate(taskWriteKeys("organization", org.id));
  const { slots, showChanges: filterToChanges } = useChronologySlots({
    org,
    view,
    overlay,
    loading,
    failed,
    active: tab === "timeline",
  });
  // An evidence mark asks where a value came from, and the answer is the
  // record's change history — which now lives on its own tab.
  const openTask = (step: { activity_id: string }) =>
    setOpenTaskId(step.activity_id);
  const showChanges = () => {
    onTab("timeline");
    filterToChanges();
  };
  return (
    <RecordView
      name={org.display_name}
      avatarSrc={org.logo_url}
      // The description a person wrote, then the row of attribute chips
      // (mockup State D). It replaces a dot-joined string of the same five
      // values, in which the two that were links did not read as links.
      subtitle={
        <>
          <CompanyDescription org={org} />
          <CompanyChips org={org} />
        </>
      }
      zone={RECORD_ZONE}
      badges={
        <CompanyActionBadges
          org={org}
          onOpenHistory={() => setAuditOpen(true)}
          onSetUpPartner={() => onTab("partner")}
        />
      }
      pulse={
        <CompanyPulse
          org={org}
          view={view}
          // The chip opens the queue, so it appears only where the queue can:
          // a count you cannot act on from here is a dead end.
          onOpenDecisions={
            tab === "overview" ? () => setDecisionsOpen(true) : undefined
          }
        />
      }
      // The composer opens from a button rather than standing open above the
      // page: a whole form in the header's action strip pushed the account's
      // own story below the fold before a word of it was read.
      actions={<CompanyPrimaryActions org={org} />}
      // Two columns, the business beside the story: the readings a rep scans
      // sit where the eye starts, and the account's own story keeps the wide
      // side. They belong to the RECORD rather than to the overview, so the
      // column stands on every tab — a reader moving between tabs keeps their
      // anchor. Overlay is the one exception: cards that each read as an empty
      // account are the half-page the refusal exists to prevent.
      //
      // DOM order still meets the story first (asideFirst is placement only),
      // so a screen reader and the tab order are unaffected by the seating.
      aside={
        overlay ? undefined : (
          <CompanyBusinessColumn
            org={org}
            view={view}
            failed={failed}
            readOnly={Boolean(org.archived_at)}
          />
        )
      }
      asideFirst
      timelineTitle={t("co.story.title")}
      // The Partner tab is a form, so it does not repeat the chronology.
      // Where the account stands and the tabs that switch what is read about
      // it belong to the record's masthead, not to the overview: they were
      // the same on every tab and still redrew themselves inside each one.
      // The skeleton is drawn while the 360 is still in flight, not merely
      // while there is no view: an overlay refusal or a failed read also
      // leaves `view` undefined, and both are settled answers, not a strip
      // still loading.
      strip={<CompanyStanding view={view} loading={loading} />}
      tabs={tabs}
      {...slots}
    >
      {/* Overlay refuses the whole company page, not one tab of it: the
          partner extension and the field history are native records the
          mirror does not hold, so switching tabs must not walk around the
          refusal into reads that can only fail. */}
      {overlay && <OverlayFallback />}
      {!overlay && tab === "partner" && <PartnerTab organizationId={org.id} />}
      {tab === "overview" && failed && (
        <EmptyState>{t("co.partial")}</EmptyState>
      )}
      {/* What needs a person, before anything that merely reports state. It is
          assembled from sections the page already read — open tasks, the
          calendar, what changed since the last visit, the suggestions — put in
          the order a rep works them, with facts, assessments and
          recommendations labelled apart. */}
      {tab === "overview" && (
        <CompanyOverviewStack
          org={org}
          view={view}
          overlay={overlay}
          loading={loading}
          failed={failed}
          taskUpdate={taskUpdate}
          onOpenTask={openTask}
          onCompose={setReplyToActivityId}
          onLogTask={() => setTaskFormOpen(true)}
          onOpenRecord={receipt.open}
        />
      )}
      {/* The composer, anchored on the message a draft_reply suggestion named.
          It is the same modal the timeline's own Reply opens — the advice
          shortcuts to it rather than inventing a second way to answer. */}
      {replyToActivityId && (
        <ComposeModal
          activityId={replyToActivityId}
          entityType="organization"
          entityId={org.id}
          kind="email"
          open
          onClose={() => setReplyToActivityId(null)}
        />
      )}
      {taskFormOpen && (
        <LogActivityAction
          entityType="organization"
          entityId={org.id}
          initialKind="task"
          openOnMount
          onClose={() => setTaskFormOpen(false)}
        />
      )}
      {/* The People tab gives the account team the whole middle column. The
          rail's card is a summary; this is the roster, with room for the title
          and the last exchange beside each name. */}
      {/* Asking sits UNDER the account's own story: it is the tool for when the
          page did not already answer the question. It belongs to the account
          rather than to its history, so it stays on the overview instead of
          following the chronology onto its own tab. */}
      {tab === "people" && (
        <PeopleCard view={view} writable={!org.archived_at} orgId={org.id} />
      )}
      {openTaskId && (
        <TaskDetailModal
          activityId={openTaskId}
          onClose={() => setOpenTaskId(null)}
          update={taskUpdate}
        />
      )}
      {/* The decision queue belongs to the OVERVIEW. Leaving it standing over
          Partner put a panel from one tab on top of another, and a reader who
          switched tabs to get rid of it could not. */}
      {decisionsOpen && tab === "overview" && (
        <CompanyApprovalsPanel
          orgId={org.id}
          view={view}
          onClose={() => setDecisionsOpen(false)}
        />
      )}
      {receipt.cited && (
        <EvidenceModal
          orgId={org.id}
          cited={receipt.cited}
          onClose={receipt.close}
          onStep={receipt.step}
        />
      )}
      {/* Context is what Margince HOLDS about the account: the profile the
          site read produced, its facts, its relationships, the dossier and the
          fit assessment, and the one-off tools. Folded into the overview it
          was more content than the rest of the page put together, and none of
          it answers "what do I do about this account".
          None of it comes from the 360 either — each card runs its own read,
          so a failed composite must not take the company's profile and files
          with it. Overlay is the one exception: the page has already refused
          once there. */}
      {!overlay && tab === "context" && (
        <>
          <DossierPanel orgId={org.id} enabled onOpenRecord={openCitation} />
          <GrowthFitPanel orgId={org.id} enabled onOpenRecord={openCitation} />
          <ReferenceDisclosures org={org} onOpenHistory={showChanges} t={t} />
        </>
      )}
      {/* The audit spine, opened from the header's overflow menu. It belongs
          to the RECORD, not to a tab, so it opens over whichever tab is up. */}
      <Modal
        open={auditOpen}
        onClose={() => setAuditOpen(false)}
        labelledBy="co-audit-title"
        size="wide"
      >
        <h2 id="co-audit-title" className="t-h2 modal-title">
          {t("record.fullHistory")}
        </h2>
        {/* Mounted only while open: the two history reads behind it are the
            page's most expensive, and nobody who never opens the panel should
            pay for them. */}
        {auditOpen && <RecordHistoryTab kind="organization" id={org.id} />}
        <div className="form-actions">
          <Button onClick={() => setAuditOpen(false)}>{t("fab.close")}</Button>
        </div>
      </Modal>
    </RecordView>
  );
}

// The overview's own stack: what needs a person, then what the account looks
// like, then the open commitments in full. Extracted from CompanyPage because
// each section was its own `tab === "overview" && view &&` branch there, and the
// page had become a list of conditions rather than a layout.
function CompanyOverviewStack({
  org,
  view,
  overlay,
  loading,
  failed,
  taskUpdate,
  onOpenTask,
  onCompose,
  onLogTask,
  onOpenRecord,
}: Readonly<{
  org: Organization;
  view?: Organization360View;
  overlay: boolean;
  loading: boolean;
  failed: boolean;
  taskUpdate: ReturnType<typeof useTaskUpdate>;
  onOpenTask: (step: { activity_id: string }) => void;
  onCompose: (activityId: string) => void;
  onLogTask: () => void;
  // Where a cited chip leads. Owned by the page, because the grid below this
  // stack cites the same records and two owners would mean two receipts open
  // over each other.
  onOpenRecord: (entityType: string, entityId: string) => void;
}>) {
  return (
    <>
      {/* Full width, and first: what needs a person today, before anything
          that merely reports state (mockup State D). */}
      <TodayOnThisAccount
        view={view}
        loading={loading}
        failed={failed}
        onPrepareMeeting={onCompose}
      />
      {/* Then what the account looks like right now, in its own words and
          ours. These three read the same evidence and cite it the same way, so
          they lead the overview together — the grid of business cards below
          them belongs to the record and renders on every tab. */}
      {view && (
        <AccountBrief
          orgId={org.id}
          view={view}
          enabled={!overlay}
          onOpenRecord={onOpenRecord}
          onPerform={(action) =>
            performSuggestion(action, {
              compose: onCompose,
              logTask: onLogTask,
            })
          }
        />
      )}
      {/* The open commitments in full, then the tool for when the page did not
          answer the question. Neither is a card about the account — one is a
          work list and one is a prompt — so neither belongs in the grid. */}
      {view && (
        <NextSteps
          view={view}
          onOpenTask={onOpenTask}
          renderAction={(step) => (
            <TaskQuickActions
              activityId={step.activity_id}
              dueAt={step.due_at}
              update={taskUpdate}
            />
          )}
        />
      )}
      <AssistantPanel orgId={org.id} enabled onOpenRecord={onOpenRecord} />
    </>
  );
}

// The loading grid's cell heights, so the skeletons occupy roughly what the
// cards will and the page does not jump when the read lands.
const GRID_SKELETON_HEIGHTS = [96, 96, 64, 96, 64, 32];

// The business cards, as the grid that replaced the right column.
//
// It belongs to the RECORD rather than to the overview: a reader who switches
// to Partner and back must not pay for every query behind these cards a second
// time, and the page must not re-flow under them on the way back.
function CompanyBusinessColumn({
  org,
  view,
  failed,
  readOnly,
}: Readonly<{
  org: Organization;
  view?: Organization360View;
  // The composite read failed, as distinct from still being in flight. With no
  // view the cards cannot tell the two apart on their own, and they say
  // opposite things: one is "we could not load this", the other is "not yet".
  failed: boolean;
  // An archived company takes no new deals, tags or list rows, so it shows no
  // verb that would only be refused.
  readOnly: boolean;
}>) {
  const t = useT();
  // Still in flight. The cards are handed no view either way, and a card with
  // no view reports "could not be loaded" — which, before the answer has
  // arrived, is a claim about a read that has not finished. Skeletons hold the
  // cells until it does.
  if (!view && !failed) {
    return (
      <div className="co-grid">
        {GRID_SKELETON_HEIGHTS.map((height, index) => (
          <section
            // The placeholders are positional and interchangeable; there is no
            // record behind one to key it by.
            // biome-ignore lint/suspicious/noArrayIndexKey: placeholder cells have no identity of their own
            key={index}
            className="card co-card"
          >
            <Skeleton width="100%" height={height} />
          </section>
        ))}
      </div>
    );
  }
  return (
    // A plain div: RecordView seats this in its own <aside> and names it, so
    // a second landmark here would announce the business twice.
    <div className="co-grid">
      {/* The commercial picture. */}
      <DealsCard
        view={view}
        actions={
          readOnly ? undefined : (
            <NewDealAction orgId={org.id} orgName={org.display_name} />
          )
        }
      />
      {/* What is running with them, and what we last put in front of them.
          Beside the deals card because the two answer the same question at
          different depths. */}
      <CompanyCommercialCard view={view} />
      <HealthCard health={view?.health} />
      {/* The money, next to the pipeline it belongs beside. Absent entirely on
          an account we have never billed — an empty finance card on a target
          is a question nobody asked. */}
      <CompanyFinanceCard orgId={org.id} lifecycle={org.lifecycle} />
      <SignalsCard orgId={org.id} />
      {/* Who carries the account, and the paperwork behind it. */}
      <PeopleCard view={view} writable={!readOnly} orgId={org.id} />
      <CompanyDocumentsCard orgId={org.id} />
      {/* How the account is FILED. It stays folded — this is our own
          bookkeeping rather than anything about the company — but it stays in
          the business grid, because tags and lists are governed sections of
          the 360 like the cards above them, and a withheld half has to say so
          where the reader is looking for it. */}
      <Disclosure summary={t("co.tags.title")}>
        <TagsCard
          view={view}
          tagAction={readOnly ? undefined : <TagAction orgId={org.id} />}
          listAction={readOnly ? undefined : <ListAction orgId={org.id} />}
        />
      </Disclosure>
    </div>
  );
}

// performSuggestion carries out the advice the server named.
//
// It routes rather than acts: each kind opens the governed surface a rep would
// have reached for anyway, prefilled from the evidence the rule fired on.
// Nothing here writes, and nothing is staged — the card advises, the surface
// it opens is where the human decides.
//
// draft_reply and add_task both land on the account's own timeline surfaces,
// which is why the page owns this handler rather than the card: the composer
// and the log-activity form live here.
function performSuggestion(
  action: SuggestionAction,
  open: { compose: (activityId: string) => void; logTask: () => void },
) {
  if (action.kind === "open_deal" && action.deal_id) {
    openCitation("deal", action.deal_id);
    return;
  }
  if (action.kind === "draft_reply" && action.activity_id) {
    open.compose(action.activity_id);
    return;
  }
  if (action.kind === "add_task") {
    open.logTask();
  }
}

// openCitation routes a cited record to its own screen. The brief, the
// prepared answers and the suggestions all cite the same records, so they
// share one route — a second copy would drift and send one card's reader to
// the wrong screen.
// A citation goes to one of two places. A deal or a person has a screen of its
// own; a fact or a profile field has no screen, but it does have a receipt —
// where the value came from and what could not be recorded about it — which is
// what the reader wanted when they clicked the chip.
function citationOpensRecord(entityType: string): boolean {
  return entityType === "deal" || entityType === "person";
}

// The kinds a receipt can be written for. Narrowing HERE rather than asserting
// at the fetch is what keeps the modal's contract honest: a kind that grows a
// receipt upstream fails to compile until this decision learns about it.
function citationHasReceipt(
  entityType: string,
): entityType is CitedRecord["entityType"] {
  return entityType === "fact" || entityType === "profile_field";
}

function openCitation(entityType: string, entityId: string) {
  if (entityType === "deal") {
    navigate({ screen: "deals", id: entityId });
  }
  if (entityType === "person") {
    navigate({ screen: "contacts", id: entityId });
  }
}

// The reference material a reader opens when the summary above is not enough.
//
// It renders whatever state the 360 is in, because none of it comes from the
// 360: each card runs its own read. That is the rule to keep as the layout
// moves — a failed composite read hides what the 360 answered, not the
// company's profile, its facts or its relationships.
function ReferenceDisclosures({
  org,
  onOpenHistory,
  t,
}: Readonly<{
  org: Organization;
  onOpenHistory: () => void;
  t: ReturnType<typeof useT>;
}>): ReactNode {
  return (
    <>
      <Disclosure summary={t("co.profile.title")}>
        <ProfileFieldsCard orgId={org.id} onOpenHistory={onOpenHistory} />
      </Disclosure>
      {/* Documents are deliberately NOT here: they are a card of their own in
          the grid now, and a reader given the same list in two places has two
          lists to reconcile. */}
      <Disclosure summary={t("co.evidence.title")}>
        <FactsCard orgId={org.id} onOpenHistory={onOpenHistory} />
      </Disclosure>
      <Disclosure summary={t("co.relationships.title")}>
        <RelationshipsTab scope={{ organization_id: org.id }} />
      </Disclosure>
      {/* One-off tools and configuration, last: used a fraction as often as
          anything above them. */}
      <Disclosure summary={t("co.tools.title")}>
        <CustomFieldsCard object="organization" record={org} />
        <HierarchyRollupCard orgId={org.id} />
        <EnrichCard orgId={org.id} />
        <DeepReadCard orgId={org.id} />
      </Disclosure>
    </>
  );
}

// chronologyNotice keeps four things apart that all render as an empty
// list if you let them: still loading, the read failed, the section was
// never in the payload, and the account genuinely has nothing to show. Only
// the last one may say so — the other three would have a rep conclude
// nobody has ever touched this account.
//
// The empty sentence names what the filter was looking for. "Nothing logged
// on this account" under the Changes filter would be a claim about the
// activity feed the reader is not looking at.
function chronologyNotice(
  timeline: {
    loading: boolean;
    failed: boolean;
    assembled: boolean;
    filter: TimelineFilter;
  },
  count: number,
  t: ReturnType<typeof useT>,
): ReactNode {
  if (timeline.loading) {
    return <Skeleton width="100%" height={48} />;
  }
  if (timeline.failed || !timeline.assembled) {
    return <EmptyState>{t("co.section.unavailable")}</EmptyState>;
  }
  if (count > 0) {
    return undefined;
  }
  const empty: Record<TimelineFilter, MessageKey> = {
    activities: "co.timeline.empty",
    changes: "co.chronology.changesEmpty",
    all: "co.chronology.allEmpty",
  };
  return <EmptyState>{t(empty[timeline.filter])}</EmptyState>;
}
