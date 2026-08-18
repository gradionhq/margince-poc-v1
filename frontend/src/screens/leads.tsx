import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useId, useRef, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { ifMatch } from "../api/version";
import { isOption } from "../app/options";
import { navigate } from "../app/router";
import { activityTimeline } from "../design-system/activitytimeline";
import {
  Badge,
  Button,
  Card,
  Modal,
  SegmentedControl,
  Textarea,
  TextInput,
} from "../design-system/atoms";
import {
  type BoardColumn,
  type BoardDeal,
  PipelineBoard,
  RecordView,
} from "../design-system/composed";
import { InlineText } from "../design-system/inlinechoice";
import type { ListChip } from "../design-system/listtable";
import { Panel, PanelBody } from "../design-system/panel";
import { useRecordTimeline } from "../design-system/recordtimeline";
import { Select } from "../design-system/select";
import { ProvenanceTag } from "../design-system/trust";
import { formatDateAbbrev } from "../format/format";
import { useLocale, useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { ArchiveAction } from "./archive";
import {
  OverlayUnavailable,
  ProblemError,
  problemMessageOf,
  provenanceOf,
  QueryGate,
  throwProblem,
  useMe,
  useSorMode,
  useViewerId,
} from "./common";
import { RECORD_ZONE } from "./company360";
import { CreateAction, type CreateField } from "./create";
import { CustomFieldsCard } from "./customfields.card";
import { useObjectCustomFields } from "./customfields.form";
import { EditAction } from "./edit";
import { EntityRef, OwnerName, useRoster } from "./entityref";
import { RecordHistoryTab, useRecordHistory } from "./history";
import {
  type ListPage,
  type ListQuery,
  ListTable,
  listFetchLimit,
  useListQuery,
} from "./listquery";
import { LogActivity } from "./logactivity";
import { ShareAction } from "./share";

// Leads (B-EP09.10a/b): visually SEGREGATED from the contact graph — the
// lead surface is accent-tinted, lead detail is its own screen (never
// person.html — gap §3.5), and promote is eligibility-gated. Lead score is
// lead-local; the ≥60 / 40–59 / <40 colour thresholds are pinned by test.
// Search/filter/sort/pagination (P-14), the rich create modal (P-15), the
// If-Match edit form (P-1), and the dedupe view-existing link (P-16) are
// wired in here the same way as contacts (people.tsx) — the Promote button
// and score/status/company badges on the lead 360 stay exactly as they
// were. Status-change and score-override are Phase 4, not surfaced here.

type Lead = components["schemas"]["Lead"];
type CreateLeadRequest = components["schemas"]["CreateLeadRequest"];
type UpdateLeadRequest = components["schemas"]["UpdateLeadRequest"];
type PromoteLeadRequest = components["schemas"]["PromoteLeadRequest"];
type PromoteTrigger = PromoteLeadRequest["trigger"];

export function scoreTone(score: number): "success" | "warn" | "danger" {
  if (score >= 60) {
    return "success";
  }
  if (score >= 40) {
    return "warn";
  }
  return "danger";
}

export function promoteEligible(lead: Lead): boolean {
  return (
    (lead.status === "new" || lead.status === "working") && Boolean(lead.email)
  );
}

// The terminal badge a lead status earns (null = live/open, no badge). A lead
// is archived iff it is promoted or disqualified; keying the label off the
// status — not a bare archived_at — is what stops a promoted lead reading
// "Disqualified". Exhaustive over the four statuses: a new value is a compile
// error here, not a silently-unlabelled row.
export function terminalBadge(
  status: Lead["status"],
): { label: MessageKey; tone: "warn" } | null {
  switch (status) {
    case "disqualified":
      return { label: "lead.disqualified", tone: "warn" };
    case "promoted":
      return { label: "record.archived", tone: "warn" };
    case "new":
    case "working":
      return null;
  }
}

// The 4 genuine-engagement triggers the server accepts (PromoteLeadRequest).
// cold_outbound_no_reply is deliberately absent — promotion is engagement,
// not an outbound touch, and the server rejects it with 422.
const PROMOTE_TRIGGERS: readonly {
  value: PromoteTrigger;
  label: MessageKey;
}[] = [
  { value: "human_qualify", label: "lead.trigger.humanQualify" },
  { value: "inbound_reply", label: "lead.trigger.inboundReply" },
  { value: "meeting_booked", label: "lead.trigger.meetingBooked" },
  { value: "meeting_held", label: "lead.trigger.meetingHeld" },
];

// Pulls the collided person's id out of a 409 already_promoted problem body —
// the promote dialog navigates there instead of showing an error, mirroring
// the create/update dedupe "view existing" pattern (problemExistingId).
function alreadyPromotedPersonId(error: unknown): string | null {
  if (!(error instanceof ProblemError)) {
    return null;
  }
  const problem = error.problem;
  if (!problem || typeof problem !== "object") {
    return null;
  }
  const details = (problem as Record<string, unknown>).details;
  if (!details || typeof details !== "object") {
    return null;
  }
  const promotedId = (details as Record<string, unknown>).promoted_person_id;
  return typeof promotedId === "string" ? promotedId : null;
}

async function fetchLeadsPage(
  query: ListQuery,
  cursor: string | null,
): Promise<ListPage<Lead>> {
  const { data, error } = await api.GET("/leads", {
    params: {
      query: {
        q: query.q || undefined,
        sort: query.sort || undefined,
        include_archived: query.includeArchived || undefined,
        cursor: cursor || undefined,
        limit: listFetchLimit(query.perPage),
        ...query.filters,
      },
    },
  });
  if (error) {
    // A LIST read's honest-error path only needs a message to render — the
    // dedupe "view existing" link is a create/update-only concern.
    throwProblem(error);
  }
  return {
    data: data.data,
    page: {
      next_cursor: data.page.next_cursor ?? null,
      has_more: data.page.has_more,
    },
  };
}

// Builds the create-lead request body: scalar fields trim to undefined when
// blank (never sent rather than sent empty). Lead email is a single string —
// not a repeatable list — so no rows channel is used here.
export function mapLeadBody(values: Record<string, string>): CreateLeadRequest {
  return {
    full_name: values.full_name?.trim() || undefined,
    email: values.email?.trim() || undefined,
    linkedin_url: values.linkedin_url?.trim() || undefined,
    title: values.title?.trim() || undefined,
    company_name: values.company_name?.trim() || undefined,
    candidate_org_key: values.candidate_org_key?.trim() || undefined,
    status: "new",
    source: "manual",
  };
}

function stringField(value: unknown): string {
  return typeof value === "string" ? value : "";
}

// Builds the PATCH body: only the five scalar fields this task surfaces —
// status and score are Phase 4 and never sent from this form.
export function mapLeadUpdate(
  values: Record<string, unknown>,
): UpdateLeadRequest {
  return {
    full_name: stringField(values.full_name).trim() || undefined,
    email: stringField(values.email).trim() || undefined,
    title: stringField(values.title).trim() || undefined,
    company_name: stringField(values.company_name).trim() || undefined,
    candidate_org_key:
      stringField(values.candidate_org_key).trim() || undefined,
  };
}

const leadCreateFields: CreateField[] = [
  { key: "full_name", label: "create.fullName", required: true },
  { key: "email", label: "create.email", type: "email" },
  { key: "linkedin_url", label: "create.linkedinUrl" },
  { key: "title", label: "create.personTitle" },
  { key: "company_name", label: "create.companyName" },
  { key: "candidate_org_key", label: "create.candidateOrgKey" },
];

const leadEditFields: CreateField[] = [
  { key: "full_name", label: "create.fullName", required: true },
  { key: "email", label: "create.email", type: "email" },
  { key: "title", label: "create.personTitle" },
  { key: "company_name", label: "create.companyName" },
  { key: "candidate_org_key", label: "create.candidateOrgKey" },
];

const leadStatusFilterOptions = [
  { value: "new", label: "lead.statusNew" },
  { value: "working", label: "lead.statusWorking" },
  { value: "promoted", label: "lead.statusPromoted" },
  { value: "disqualified", label: "lead.statusDisqualified" },
] as const;

// The score bands a reader triages by. `min_score` is a floor, so each band
// names the bottom of a range rather than a bucket — "60+" and "80+" overlap
// on purpose, because that is what the parameter means.
const LEAD_SCORE_BANDS = [
  { value: "80", label: "lead.filterScoreHot" },
  { value: "60", label: "lead.filterScoreWarm" },
  { value: "40", label: "lead.filterScoreCool" },
] as const;

/**
 * useLeadOwnerChips is the owner dial listLeads can actually answer.
 *
 * The shared `useOwnerChips` offers team and unassigned entries under
 * `owner_team_id` and `unassigned`; listLeads takes neither, so those options
 * would 422 rather than narrow. Until the endpoint learns that vocabulary the
 * honest dial is the one question it does answer: mine, or anyone's.
 */
function useLeadOwnerChips(): readonly ListChip[] {
  const t = useT();
  const me = useMe();
  const viewerId = me.data?.user.id;
  if (!viewerId) {
    return [];
  }
  return [
    {
      key: "owner_id",
      label: t("list.owner"),
      allLabel: t("list.filterOwnerAll"),
      options: [{ value: viewerId, label: t("list.filterOwnerMe") }],
    },
  ];
}

/**
 * statusLabel is the ONE spelling of a lead status for a reader.
 *
 * Read from the filter options rather than a second table: the chips and the
 * cells naming the same status differently is exactly how a German UI came to
 * print the chip "In Bearbeitung" beside a cell reading the raw enum
 * "working".
 */
function statusLabel(status: Lead["status"]): MessageKey | null {
  const option = leadStatusFilterOptions.find((o) => o.value === status);
  // Null, not a stand-in key: a status the contract adds and this list has not
  // learned yet has no honest translation, and every candidate for one lies.
  // The callers render the raw value instead, which is wrong-LOOKING rather
  // than wrong — a reader seeing an untranslated word can report it, where a
  // badge reading "Status" looks deliberate and hides the gap.
  return option?.label ?? null;
}

/** The status as a reader should see it: translated, or the raw value. */
function StatusBadge({ status }: Readonly<{ status: Lead["status"] }>) {
  const t = useT();
  const label = statusLabel(status);
  return <Badge>{label ? t(label) : status}</Badge>;
}

async function createLead(
  values: Record<string, string>,
  customFields: Record<string, unknown>,
  t: (key: MessageKey) => string,
): Promise<Lead> {
  const { data, error } = await api.POST("/leads", {
    body: { ...mapLeadBody(values), ...customFields },
  });
  if (error) {
    throwProblem(error, t);
  }
  return data;
}

/**
 * The two columns a lead can be dragged between.
 *
 * Only the LIVE statuses. `promoted` and `disqualified` are terminal and
 * reachable only through their own audited verbs — a board column for either
 * would offer a drag that ends in a 422, and worse, would imply a lead can be
 * promoted by moving a card, which is the one thing ADR-0008's trigger set
 * exists to prevent.
 */
const LEAD_BOARD_STAGES = [
  { stage: "new", label: "lead.statusNew" },
  { stage: "working", label: "lead.statusWorking" },
] as const;

/** A lead as the board's card reads it. */
function LeadCard({
  lead,
  onOpen,
  dragHandlers,
}: Readonly<{
  lead: Lead;
  onOpen: (lead: Lead) => void;
  dragHandlers?: {
    draggable: true;
    onDragStart: (event: React.DragEvent) => void;
    onDragEnd: () => void;
  };
}>) {
  const t = useT();
  return (
    <button
      type="button"
      className="deal-card"
      data-lead={lead.id}
      onClick={() => onOpen(lead)}
      {...dragHandlers}
    >
      <span className="deal-name">
        {lead.full_name ?? lead.email ?? t("nav.leads")}
      </span>
      {lead.company_name && (
        <span className="deal-org">
          <span className="deal-org-name">{lead.company_name}</span>
        </span>
      )}
      <span className="deal-meta">
        <Badge tone={scoreTone(lead.score)}>
          {t("lead.score")}: {lead.score}
        </Badge>
        {lead.title && <span>{lead.title}</span>}
      </span>
    </button>
  );
}

/**
 * LeadBoard is the triage surface: the live leads, in the two columns they can
 * actually move between, dragged from one to the other.
 *
 * It renders the rows the LIST already fetched rather than asking again, so a
 * reader's filters and search narrow the board exactly as they narrow the
 * table — a board that ignored the filter bar above it would be a second,
 * silently different answer to the same question.
 */
function LeadBoard({
  rows,
  onMoved,
  hasMore,
  loadMore,
}: Readonly<{
  rows: Lead[];
  onMoved: () => void;
  hasMore: boolean;
  loadMore: () => void;
}>) {
  const t = useT();
  const queryClient = useQueryClient();
  const dragging = useRef<string | null>(null);
  const lastDragEnd = useRef(0);

  const move = useMutation({
    // The lead's version rides the variables, not a closure: the card that
    // carried it belongs to the committed render, so it cannot be stale.
    mutationFn: async (moved: {
      id: string;
      // Optional exactly as the contract has it. ifMatch omits the header when
      // it is absent, which is the honest behaviour: a row the server did not
      // version is one this client cannot make a concurrency claim about.
      version?: number;
      // The two the contract accepts. `promoted` and `disqualified` are
      // reachable only through their own verbs, and typing the board's write
      // this way is what stops a third column being added without noticing.
      status: "new" | "working";
    }) => {
      const { data, error } = await api.PATCH("/leads/{id}", {
        params: { path: { id: moved.id }, ...ifMatch(moved.version) },
        body: { status: moved.status },
      });
      if (error) {
        throwProblem(error, t);
      }
      return data;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["leads"] });
      onMoved();
    },
    onError: () => {
      // A 409 means the card's version is stale. Without a refetch the reader
      // retries with the same doomed If-Match and the drag never takes.
      queryClient.invalidateQueries({ queryKey: ["leads"] });
    },
  });

  const live = rows.filter(
    (lead) => lead.status === "new" || lead.status === "working",
  );
  const columns: BoardColumn[] = LEAD_BOARD_STAGES.map((stage) => {
    const held = live.filter((lead) => lead.status === stage.stage);
    return {
      stage: stage.stage,
      label: t(stage.label),
      count: held.length,
      // The board's card type is the deal's, so each lead rides as one and the
      // renderCard hook reads it back out of `leadsById` below. Only `id` is
      // ever read off this object.
      deals: held.map((lead) => ({ id: lead.id, name: "" }) as BoardDeal),
    };
  });
  const leadsById = new Map(live.map((lead) => [lead.id, lead]));

  return (
    <>
      {move.isError && (
        <p className="t-caption" style={{ color: "var(--danger)" }}>
          {problemMessageOf(move.error, t)}
        </p>
      )}
      {/* The board holds the live statuses only, so a filter narrowed to a
          terminal one leaves it empty — and an empty board with no reason
          reads as a broken render rather than a filter doing its job. */}
      {rows.length > 0 && live.length === 0 && (
        <p className="t-caption">{t("lead.boardTerminalOnly")}</p>
      )}
      <PipelineBoard
        variant="plain"
        columns={columns}
        renderCard={(card) => {
          const lead = leadsById.get(card.id);
          if (!lead) {
            return null;
          }
          return (
            <LeadCard
              lead={lead}
              onOpen={(opened) => {
                // A drag ends in a click the browser also reports; opening the
                // record on it would navigate away from the board every time a
                // card was moved.
                if (Date.now() - lastDragEnd.current > 250) {
                  navigate({ screen: "leads", id: opened.id });
                }
              }}
              dragHandlers={{
                draggable: true,
                onDragStart: (event) => {
                  dragging.current = lead.id;
                  event.dataTransfer.setData("text/plain", lead.id);
                },
                // Recorded on END, not on drop: a drag cancelled off the board
                // never reaches a drop handler, and the click the browser then
                // reports would navigate away from the board.
                onDragEnd: () => {
                  dragging.current = null;
                  lastDragEnd.current = Date.now();
                },
              }}
            />
          );
        }}
        columnDropHandlers={(column) => ({
          onDragOver: (event) => {
            event.preventDefault();
            (event.currentTarget as HTMLElement).classList.add("droptarget");
          },
          onDragLeave: (event) => {
            (event.currentTarget as HTMLElement).classList.remove("droptarget");
          },
          onDrop: (event) => {
            event.preventDefault();
            (event.currentTarget as HTMLElement).classList.remove("droptarget");
            const id =
              event.dataTransfer.getData("text/plain") || dragging.current;
            dragging.current = null;
            lastDragEnd.current = Date.now();
            const lead = id ? leadsById.get(id) : undefined;
            // A card dropped on the column it already sits in is not a move.
            const target = LEAD_BOARD_STAGES.find(
              (stage) => stage.stage === column.stage,
            );
            if (lead && target && lead.status !== target.stage) {
              move.mutate({
                id: lead.id,
                version: lead.version,
                status: target.stage,
              });
            }
          },
        })}
      />
      {/* A board that showed page one while looking like the whole pipeline
          would be a confident wrong answer about how much work is waiting. */}
      {hasMore && (
        <Button small onClick={loadMore}>
          {t("list.loadMore")}
        </Button>
      )}
    </>
  );
}

export function LeadsScreen() {
  const ownerChips = useLeadOwnerChips();
  const t = useT();
  const { locale } = useLocale();
  const cf = useObjectCustomFields("lead");
  const state = useListQuery<Lead>({
    key: "leads",
    initialSort: "-created_at",
    fetchPage: fetchLeadsPage,
  });
  // The board writes status, which the mirror refuses (a lead's lifecycle is
  // not a field write-back), so overlay gets the table and no toggle.
  const overlay = useSorMode() === "overlay";
  const [view, setView] = useState<"table" | "board">("table");

  return (
    <div className="wrap lead-surface">
      {!overlay && (
        <div style={{ marginBottom: "var(--space-3)" }}>
          <SegmentedControl
            options={["table", "board"] as const}
            value={view}
            onChange={setView}
            labels={{
              table: t("deals.viewTable"),
              board: t("deals.viewBoard"),
            }}
          />
        </div>
      )}
      <ListTable
        // The board renders INSIDE the surface, so the search, the chips and
        // the saved views stay above it. A board that replaced the surface
        // took the filter bar with it, leaving the reader looking at a
        // narrowed answer with no way to see or change what narrowed it.
        body={
          view === "board" && !overlay ? (
            <LeadBoard
              rows={state.rows}
              onMoved={() => state.refetch()}
              hasMore={state.hasMore}
              loadMore={state.loadMore}
            />
          ) : undefined
        }
        state={state}
        unit="unit.leads"
        caption="lead.segregated"
        action={
          <CreateAction
            label={t("create.lead")}
            invalidate="leads"
            screen="leads"
            create={(values) => createLead(values, cf.toBody(values), t)}
            resolveExisting={(_code, id) => ({ screen: "leads", id })}
            fields={[...leadCreateFields, ...cf.formFields]}
          />
        }
        columns={[
          {
            key: "name",
            header: t("people.name"),
            cell: (lead: Lead) => {
              const terminal = terminalBadge(lead.status);
              return (
                <span>
                  <strong>{lead.full_name ?? lead.email ?? ""}</strong>
                  {lead.company_name && (
                    <span className="t-caption"> · {lead.company_name}</span>
                  )}
                  {terminal && (
                    <Badge tone={terminal.tone}>{t(terminal.label)}</Badge>
                  )}
                </span>
              );
            },
            fixed: true,
          },
          {
            key: "score",
            header: t("lead.score"),
            cell: (lead: Lead) => (
              <Badge tone={scoreTone(lead.score)}>{lead.score}</Badge>
            ),
            sort: "score",
            numeric: true,
          },
          {
            key: "status",
            header: t("lead.status"),
            cell: (lead: Lead) => <StatusBadge status={lead.status} />,
          },
          {
            key: "owner",
            header: t("list.owner"),
            cell: (lead: Lead) => (
              <OwnerName ownerId={lead.owner_id} unowned={t("list.unowned")} />
            ),
            sort: "owner_id",
          },
          {
            key: "created",
            header: t("list.created"),
            cell: (lead: Lead) => (
              <span className="t-caption">
                {formatDateAbbrev(lead.created_at, locale, RECORD_ZONE)}
              </span>
            ),
            sort: "created_at",
          },
        ]}
        rowKey={(lead) => lead.id}
        rowRoute={(lead) => ({ screen: "leads", id: lead.id })}
        chips={[
          {
            key: "status",
            label: "lead.filterStatus",
            allLabel: "lead.filterStatusAll",
            options: leadStatusFilterOptions.map((option) => ({ ...option })),
          },
          {
            key: "min_score",
            label: "lead.filterScore",
            allLabel: "lead.filterScoreAll",
            options: LEAD_SCORE_BANDS.map((band) => ({ ...band })),
          },
        ]}
        // Owner is a DATA chip: its options are people, read at runtime.
        // Deliberately not the shared `useOwnerChips` dial, which also offers
        // team and unassigned options spelled `owner_team_id`/`unassigned` —
        // parameters listLeads does not take, so those choices would 422
        // rather than narrow the list.
        dataChips={ownerChips}
        views={[
          { label: "list.viewAll", sort: "-created_at" },
          { label: "list.viewHighestScore", sort: "-score" },
          {
            label: "list.viewHot",
            sort: "-score",
            filters: { min_score: "80" },
          },
        ]}
      />
    </div>
  );
}

const LEAD_OPEN_STATUSES = ["new", "working"] as const;
type LeadOpenStatus = (typeof LEAD_OPEN_STATUSES)[number];

function isOpenStatus(status: Lead["status"]): status is LeadOpenStatus {
  return status === "new" || status === "working";
}

// "Explain This Score" (AC-S7): the weighted factors behind the number,
// with the decay shown as arithmetic rather than asserted.
//
// The read serves what was STORED with the score, so a lead scored before
// The decision-maker title pattern the score uses (formulas §3.1). Mirrored
// here ONLY to say why a title earned nothing; the score itself is computed
// server-side and this never adds to it.
const DECISION_MAKER_TITLE =
  /(chief|vp|head|director|founder|owner|c[a-z]o)\b/i;
const HIGH_INTENT_SOURCES = new Set(["inbound", "webform", "referral"]);
// The server PENALISES these five points rather than merely granting nothing
// (leadscore.go). Calling that "no buying intent on its own" would soften a
// subtraction into a neutral, which is a different and kinder claim than the
// model made.
const LOW_INTENT_SOURCES = new Set(["import", "crawl"]);

// What a lead is missing, in the model's own terms — shown when no retained
// decomposition exists yet.
//
// A zero score and an unscored lead look identical as a number and mean
// opposite things: "we assessed this and it earns nothing" versus "nothing
// has been assessed". A rep reads both as a bad prospect, and only one of
// them is (ADR-0108 §4). These reasons are always derivable, so the page
// states them rather than explaining our own storage history.
function ScoreShortfall({ lead }: Readonly<{ lead: Lead }>) {
  const t = useT();
  const missing: string[] = [];
  if (!lead.title) {
    missing.push(t("lead.shortfall.noTitle"));
  } else if (!DECISION_MAKER_TITLE.test(lead.title)) {
    missing.push(t("lead.shortfall.titleNotSenior", { title: lead.title }));
  }
  if (!lead.source) {
    // Split exactly as the title pair above is: interpolating an absent value
    // would print "Came in as undefined" at a rep.
    missing.push(t("lead.shortfall.noSource"));
  } else if (LOW_INTENT_SOURCES.has(lead.source)) {
    missing.push(t("lead.shortfall.sourcePenalised", { source: lead.source }));
  } else if (!HIGH_INTENT_SOURCES.has(lead.source)) {
    missing.push(t("lead.shortfall.sourceNoIntent", { source: lead.source }));
  }
  // Deliberately NOT a claim that no reply or meeting exists. Engagement lives
  // in linked activities the client never reads, and a decayed reply can round
  // to nothing while still being a reply — "no reply yet" would be a statement
  // about the prospect this page cannot support. What it CAN say is what
  // would move the score, which is the actionable half anyway.
  missing.push(t("lead.shortfall.engagementMoves"));

  return (
    <div
      style={{
        display: "flex",
        flexDirection: "column",
        gap: "var(--space-1)",
      }}
    >
      <span className="t-caption">{t("lead.shortfall.lead")}</span>
      <ul style={{ listStyle: "none", margin: 0, padding: 0 }}>
        {missing.map((reason) => (
          <li key={reason} className="t-caption">
            {reason}
          </li>
        ))}
      </ul>
    </div>
  );
}

function ScoreBreakdown({ id, lead }: Readonly<{ id: string; lead: Lead }>) {
  const t = useT();
  const explain = useQuery({
    queryKey: ["lead", id, "score"],
    queryFn: async () => {
      const { data, error } = await api.GET("/leads/{id}/score", {
        params: { path: { id } },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });

  if (explain.isPending) {
    return <span className="t-caption">{t("lead.scoreLoading")}</span>;
  }
  if (explain.isError) {
    return (
      <span className="t-caption">{problemMessageOf(explain.error, t)}</span>
    );
  }
  const current = explain.data?.current;
  if (!explain.data?.explained || !current) {
    // No retained decomposition. For a score of ZERO the reasons are still
    // derivable from the lead in hand, and they are what the reader came for
    // — "this score predates the breakdown" answers a question nobody asked
    // and leaves a 0 looking like a bad prospect rather than an unassessed
    // one (ADR-0108 §4).
    //
    // A NON-zero score is a different case: something did count, this client
    // cannot say what, and listing what is missing would state the opposite
    // of the truth. It says only that the breakdown is not stored yet.
    return lead.score === 0 ? (
      <ScoreShortfall lead={lead} />
    ) : (
      <span className="t-caption">{t("lead.scoreNotStoredYet")}</span>
    );
  }
  const factors = current.factors ?? [];
  // Under a Commercial Judgement override the displayed score is the
  // human's and these factors sum to the machine's, so the reader is told
  // which number they are looking at rather than left to assume.
  const overridden = current.override_reason != null;

  return (
    <div
      style={{
        display: "flex",
        flexDirection: "column",
        gap: "var(--space-1)",
      }}
    >
      {overridden && (
        <span className="t-caption">
          {t("lead.scoreFactorsExplainMachine", {
            score: current.score_computed,
          })}
        </span>
      )}
      {factors.length === 0 ? (
        <span className="t-caption">{t("lead.scoreNoFactors")}</span>
      ) : (
        <ul style={{ listStyle: "none", margin: 0, padding: 0 }}>
          {factors.map((factor) => (
            <li
              key={factor.factor}
              style={{
                display: "flex",
                gap: "var(--space-2)",
                alignItems: "baseline",
              }}
            >
              <span>{scoreFactorLabel(factor.factor, t)}</span>
              <span className="t-mono">{factor.points.toFixed(1)}</span>
              {factor.base_points != null && (
                // The decay as arithmetic a reader can check: 25 halving
                // every 14 days is why this row reads 12.5 today.
                <span className="t-caption t-mono">
                  {t("lead.scoreDecayed", { base: factor.base_points })}
                </span>
              )}
              {factor.source_activity_ids != null &&
                factor.source_activity_ids.length > 0 && (
                  // How many records fed the factor. The ids themselves are
                  // already filtered to what this reader may open, so the
                  // count never claims more than they can see.
                  <span className="t-caption">
                    {t("lead.scoreSources", {
                      count: factor.source_activity_ids.length,
                    })}
                  </span>
                )}
            </li>
          ))}
        </ul>
      )}
      <span className="t-caption t-mono">
        {t("lead.scoreReconciles", {
          raw: current.raw_sum.toFixed(2),
          rounded: current.rounded_sum,
          score: current.score_computed,
        })}
      </span>
    </div>
  );
}

// A factor's name in the reader's language, falling back to the raw key so
// a factor the UI has no wording for yet still appears with its points —
// an unnamed contribution is better than a silently missing one.
function scoreFactorLabel(factor: string, t: ReturnType<typeof useT>): string {
  const key = `lead.factor.${factor}` as MessageKey;
  const label = t(key);
  return label === key ? factor : label;
}

// Ownership: who holds the lead, and reassignment to any workspace user.
// The owner reads as a NAME — EntityRef resolves it off the shared `/users`
// roster and falls back to the id only while that load is in flight or when
// the viewer cannot see the roster, so a reader is never handed a bare uuid.
// Reassignment is a plain owner change (UC-E13-04): the server audits it and
// keeps whatever routing decision it overrides, so the only thing this
// control owes the reader is an honest list of who they can hand it to.
function LeadOwner({
  lead,
  meId,
  pending,
  onAssign,
  terminalReasonId,
}: Readonly<{
  lead: Lead;
  meId: string | undefined;
  pending: boolean;
  onAssign: (ownerId: string) => void;
  terminalReasonId: string;
}>) {
  const t = useT();
  const pickerId = useId();
  const [picking, setPicking] = useState(false);
  const roster = useRoster("user", picking);
  // Everyone but the current owner, with the VIEWER first: assigning to
  // yourself is the common case on a small team, and it is now an option in
  // this one control rather than a button of its own (ADR-0108 §5).
  const candidates = (roster.data ?? [])
    .filter((entry) => entry.id !== lead.owner_id)
    .sort((a, b) => {
      if (a.id === meId) return -1;
      if (b.id === meId) return 1;
      return 0;
    });

  return (
    <div
      style={{
        display: "flex",
        flexDirection: "column",
        gap: "var(--space-2)",
      }}
    >
      <div
        style={{
          display: "flex",
          gap: "var(--space-2)",
          alignItems: "center",
        }}
      >
        <span className="t-caption">{t("lead.ownerLabel")}</span>
        {lead.owner_id ? (
          lead.owner_id === meId ? (
            <span className="t-caption">{t("lead.ownerYou")}</span>
          ) : (
            <EntityRef kind="user" id={lead.owner_id} />
          )
        ) : (
          <span className="t-caption">{t("lead.unassigned")}</span>
        )}
        {/* ONE control, not a button that assigns to you beside a button
            that reveals a picker nobody can see until they press it
            (ADR-0108 §5). The viewer is the first option because
            self-assignment is the common case on a small team. */}
        <Button
          small
          disabled={pending}
          reasonId={lead.archived_at ? terminalReasonId : undefined}
          aria-expanded={picking}
          aria-controls={pickerId}
          onClick={() => setPicking(!picking)}
        >
          {t("lead.assign")}
        </Button>
      </div>

      <div id={pickerId}>
        {picking &&
          (roster.isPending ? (
            <span className="t-caption">{t("share.rosterLoading")}</span>
          ) : roster.isError ? (
            <div
              style={{
                display: "flex",
                gap: "var(--space-2)",
                alignItems: "center",
              }}
            >
              <span className="t-caption share-error">
                {t("share.rosterErrorUsers")}
              </span>
              <Button small onClick={() => roster.refetch()}>
                {t("common.retry")}
              </Button>
            </div>
          ) : candidates.length === 0 ? (
            <span className="t-caption">{t("lead.assignNobodyElse")}</span>
          ) : (
            <Select
              aria-label={t("lead.assignTo")}
              placeholder={t("lead.assignChoose")}
              value=""
              disabled={pending}
              options={candidates.map((entry) => ({
                value: entry.id,
                // The viewer reads as "Me" — a rep scanning this list looks
                // for themselves, not for their own name among colleagues'.
                // A user with no display name still has to be pickable, so
                // the id stands in rather than rendering a blank row.
                label:
                  entry.id === meId
                    ? t("lead.assignToMe")
                    : (("display_name" in entry ? entry.display_name : null) ??
                      entry.id),
              }))}
              onChange={(value) => {
                onAssign(value);
                setPicking(false);
              }}
            />
          ))}
      </div>
    </div>
  );
}

// Phase 4 lifecycle controls (P-10/11/12): status (new↔working only —
// promoted/disqualified are terminal and stay badge-only), the score
// explain/override panel (the read carries no per-factor breakdown, so
// "explain" here is honestly just the override-vs-machine story), and
// ownership — the owner's name plus reassignment to any workspace user.
// All three share one PATCH /leads/{id} + If-Match(lead.version) mutation.
// The score block: its explanation, and the Commercial Judgement override.
// Extracted from LeadLifecycle because guarding every terminal write pushed
// that render past the complexity budget — and because the score's own
// controls are a thing in themselves.
function LeadScorePanel({
  lead,
  id,
  readOnly,
  terminalReasonId,
  overriding,
  setOverriding,
  scoreValue,
  setScoreValue,
  reasonValue,
  setReasonValue,
  scoreFieldId,
  reasonFieldId,
  patch,
}: Readonly<{
  lead: Lead;
  id: string;
  readOnly: boolean;
  terminalReasonId: string;
  overriding: boolean;
  setOverriding: (next: boolean) => void;
  scoreValue: string;
  setScoreValue: (next: string) => void;
  reasonValue: string;
  setReasonValue: (next: string) => void;
  scoreFieldId: string;
  reasonFieldId: string;
  patch: { isPending: boolean; mutate: (body: UpdateLeadRequest) => void };
}>) {
  const t = useT();
  const reasonBlank = reasonValue.trim() === "";
  const scoreBlank = scoreValue.trim() === "";
  const parsedScore = Number(scoreValue);
  const scoreInvalid =
    scoreBlank ||
    !Number.isInteger(parsedScore) ||
    parsedScore < 0 ||
    parsedScore > 100;

  return (
    <div
      style={{
        display: "flex",
        flexDirection: "column",
        gap: "var(--space-2)",
      }}
    >
      <span className="t-caption">{t("lead.explainScore")}</span>
      <ScoreBreakdown id={id} lead={lead} />
      {lead.score_override_reason ? (
        <div
          style={{
            display: "flex",
            flexDirection: "column",
            gap: "var(--space-2)",
          }}
        >
          <p>
            {t("lead.scoreOverridden", {
              reason: lead.score_override_reason,
            })}
          </p>
          {lead.score_computed != null && (
            <p className="t-caption">
              {t("lead.machineScore", { score: lead.score_computed })}
            </p>
          )}
          <Button
            small
            disabled={patch.isPending || readOnly}
            reasonId={readOnly ? terminalReasonId : undefined}
            onClick={() => patch.mutate({ score: null })}
          >
            {t("lead.clearOverride")}
          </Button>
        </div>
      ) : overriding ? (
        <div
          style={{
            display: "flex",
            flexDirection: "column",
            gap: "var(--space-2)",
            maxWidth: 320,
          }}
        >
          <div
            className="t-caption"
            style={{
              display: "flex",
              flexDirection: "column",
              gap: "var(--space-1)",
            }}
          >
            <label htmlFor={scoreFieldId}>{t("lead.overrideScoreValue")}</label>
            <TextInput
              id={scoreFieldId}
              type="number"
              min={0}
              max={100}
              value={scoreValue}
              onChange={(event) => setScoreValue(event.target.value)}
            />
          </div>
          <div
            className="t-caption"
            style={{
              display: "flex",
              flexDirection: "column",
              gap: "var(--space-1)",
            }}
          >
            <label htmlFor={reasonFieldId}>{t("lead.overrideReason")}</label>
            <TextInput
              id={reasonFieldId}
              value={reasonValue}
              onChange={(event) => setReasonValue(event.target.value)}
            />
          </div>
          <div style={{ display: "flex", gap: "var(--space-2)" }}>
            <Button
              variant="primary"
              small
              disabled={reasonBlank || scoreInvalid || patch.isPending}
              reasonId={readOnly ? terminalReasonId : undefined}
              onClick={() =>
                patch.mutate({
                  score: parsedScore,
                  score_override_reason: reasonValue.trim(),
                })
              }
            >
              {t("lead.saveOverride")}
            </Button>
            <Button small onClick={() => setOverriding(false)}>
              {t("create.cancel")}
            </Button>
          </div>
        </div>
      ) : (
        // "Machine-computed score" was a label with no value beside it,
        // naming what the badge above already says. The override is a rare
        // action and stands alone.
        <Button
          small
          reasonId={lead.archived_at ? terminalReasonId : undefined}
          onClick={() => setOverriding(true)}
        >
          {t("lead.overrideScore")}
        </Button>
      )}
    </div>
  );
}

/**
 * The lead's own words, editable where they stand.
 *
 * Everything here was previously reachable only through the Edit modal, which
 * is four clicks and a context switch to fix a misspelled company name. The
 * modal stays — it is how a lead is edited wholesale, and how the fields this
 * grid does NOT carry are reached — but the four a rep corrects while reading
 * are corrected while reading.
 *
 * Every row saves through the SAME patch the lifecycle card uses, so one
 * inline edit and another cannot invalidate different caches or send a
 * different If-Match.
 */
function LeadIdentityFields({
  lead,
  save,
  saving,
  readOnlyReason,
}: Readonly<{
  lead: Lead;
  save: (body: UpdateLeadRequest) => Promise<void>;
  saving: boolean;
  readOnlyReason?: string;
}>) {
  const t = useT();
  // One write at a time: a second row opened while a save is in flight would
  // carry the If-Match the first write is about to make stale.
  const canEdit = !readOnlyReason && !saving;
  return (
    <Panel title={t("lead.details")}>
      <PanelBody>
        <InlineText
          label={t("create.fullName")}
          value={lead.full_name ?? ""}
          placeholder={t("lead.detailsUnset")}
          canEdit={canEdit}
          readOnlyReason={readOnlyReason}
          onSave={(next) => save({ full_name: next.trim() || null })}
        />
        <InlineText
          label={t("create.personTitle")}
          value={lead.title ?? ""}
          placeholder={t("lead.detailsUnset")}
          canEdit={canEdit}
          readOnlyReason={readOnlyReason}
          onSave={(next) => save({ title: next.trim() || null })}
        />
        <InlineText
          label={t("create.companyName")}
          value={lead.company_name ?? ""}
          placeholder={t("lead.detailsUnset")}
          canEdit={canEdit}
          readOnlyReason={readOnlyReason}
          onSave={(next) => save({ company_name: next.trim() || null })}
        />
        {/* Email is NOT here. It is the lead's dedupe key: changing it can
            collide with a live lead and answer 409 with an incumbent id, which
            is a conversation (view the existing record) rather than a field
            edit. The Edit modal owns it, where that answer has somewhere to
            render. */}
      </PanelBody>
    </Panel>
  );
}

function LeadLifecycle({
  lead,
  id,
  onChanged,
  terminalReasonId,
}: Readonly<{
  lead: Lead;
  id: string;
  onChanged: () => void;
  terminalReasonId: string;
}>) {
  const t = useT();
  const me = useMe();
  const scoreFieldId = useId();
  const reasonFieldId = useId();
  const [overriding, setOverriding] = useState(false);
  const [scoreValue, setScoreValue] = useState("");
  const [reasonValue, setReasonValue] = useState("");
  // A terminal lead takes no writes: the server refuses score, status and
  // owner on it, so every control here is refused by ONE fact. Derived once
  // rather than re-tested per control, because the control that gets missed
  // is the one that had to remember on its own.
  const readOnly = Boolean(lead.archived_at);

  const patch = useMutation({
    mutationFn: async (body: UpdateLeadRequest) => {
      // The last word on a terminal lead, and deliberately not a per-control
      // check: the server refuses every one of these writes, and a control
      // added later would otherwise have to remember on its own. `readOnly`
      // is read from the record the mutation is about, not from render state,
      // so a lead that went terminal while this page was open is refused too.
      if (lead.archived_at) {
        throw new Error("a terminal lead takes no writes");
      }
      const { data, error } = await api.PATCH("/leads/{id}", {
        params: { path: { id }, ...ifMatch(lead.version) },
        body,
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    onSuccess: () => {
      onChanged();
      setOverriding(false);
      setScoreValue("");
      setReasonValue("");
    },
  });

  const meId = me.data?.user?.id;

  // The inline rows await their save and render what it throws, so they need a
  // promise rather than the mutation's fire-and-forget. mutateAsync is that
  // same mutation — one PATCH shape, one If-Match, one invalidation.
  const saveField = async (body: UpdateLeadRequest) => {
    await patch.mutateAsync(body);
  };

  return (
    <Card
      as="div"
      inset
      style={{
        marginTop: "var(--space-4)",
        display: "flex",
        flexDirection: "column",
        gap: "var(--space-3)",
      }}
    >
      <LeadIdentityFields
        lead={lead}
        save={saveField}
        saving={patch.isPending}
        readOnlyReason={readOnly ? t("lead.terminalReadOnly") : undefined}
      />

      {isOpenStatus(lead.status) && (
        <div style={{ display: "flex", flexDirection: "column", gap: 6 }}>
          <span className="t-caption">{t("lead.setStatus")}</span>
          <SegmentedControl
            options={LEAD_OPEN_STATUSES}
            value={lead.status}
            labels={{
              new: t("lead.status.new"),
              working: t("lead.status.working"),
            }}
            onChange={(status) => {
              // Same one-write-at-a-time rule as the inline rows: a status
              // sent while another save is in flight races it for If-Match.
              if (!patch.isPending) {
                patch.mutate({ status });
              }
            }}
          />
        </div>
      )}

      <LeadScorePanel
        lead={lead}
        id={id}
        readOnly={readOnly}
        terminalReasonId={terminalReasonId}
        overriding={overriding}
        setOverriding={setOverriding}
        scoreValue={scoreValue}
        setScoreValue={setScoreValue}
        reasonValue={reasonValue}
        setReasonValue={setReasonValue}
        scoreFieldId={scoreFieldId}
        reasonFieldId={reasonFieldId}
        patch={patch}
      />

      <LeadOwner
        lead={lead}
        meId={meId}
        terminalReasonId={terminalReasonId}
        pending={patch.isPending || readOnly}
        onAssign={(ownerId) => patch.mutate({ owner_id: ownerId })}
      />

      {patch.isError && (
        <span className="t-caption" style={{ color: "var(--danger)" }}>
          {problemMessageOf(patch.error, t)}
        </span>
      )}
    </Card>
  );
}

// The lead-360 badge row. Extracted so LeadScreen's render stays legible and
// the terminal-state labelling lives in one place (terminalBadge).
function LeadBadges({ lead }: Readonly<{ lead: Lead }>) {
  const t = useT();
  const viewerId = useViewerId();
  const terminal = terminalBadge(lead.status);
  return (
    <div
      style={{ display: "flex", gap: 8, flexWrap: "wrap", marginBottom: 12 }}
    >
      <Badge tone={scoreTone(lead.score)}>
        {t("lead.score")}: {lead.score}
      </Badge>
      {lead.score_override_reason && <Badge>{t("lead.overriddenBadge")}</Badge>}
      <StatusBadge status={lead.status} />
      {lead.company_name && <Badge>{lead.company_name}</Badge>}
      {terminal && <Badge tone={terminal.tone}>{t(terminal.label)}</Badge>}
      <ProvenanceTag provenance={provenanceOf(lead.captured_by, viewerId)} />
    </div>
  );
}

/**
 * What the promotion did, read from the promote audit row it wrote.
 *
 * `outcome` is a closed union with an explicit unknown, not a bare string: the
 * page states "merged into a contact we already knew" or "became a new
 * contact", and treating every non-"merged" value as "created" would make
 * schema drift, a bad row, or a future third outcome read as a confident
 * claim about a merge that never happened.
 */
type PromotionOutcome = "merged" | "created" | "unknown";

type PromotionRecord = {
  outcome: PromotionOutcome;
  trigger?: string;
  evidenceNote?: string;
  // The read's own state. Loading and failing are not "created" — a panel that
  // reported an outcome while its source was still in flight would show the
  // wrong one for as long as the request took, and forever on a 403.
  pending: boolean;
  failed: boolean;
};

/**
 * usePromotionRecord reads the promotion off the lead's audit trail.
 *
 * The outcome, trigger and evidence are not columns on `lead` — the write
 * shape puts them in the `promote` audit row, which is the honest source:
 * re-deriving "did this merge?" from today's data would answer about the
 * records as they are now, not about what actually happened.
 *
 * Only a promoted lead has a promotion to describe, so the read is disabled on
 * every other one rather than fetching a history nothing renders.
 */
function usePromotionRecord(id: string, promoted: boolean): PromotionRecord {
  const history = useRecordHistory("lead", id, promoted);
  // `page?.data` for the same reason getNextPageParam needs it: a 200 with no
  // body is a shape the contract permits, and this read runs on every promoted
  // lead page.
  const entries = history.data?.pages.flatMap((page) => page?.data ?? []) ?? [];
  const row = entries.find((entry) => entry.action === "promote");

  // The history is served OLDEST FIRST, 20 to a page, and `promote` is the
  // LAST thing that ever happens to a lead — it retires the record. So a lead
  // worked long enough to collect 20 earlier audit rows carries its promotion
  // on a later page, and reading only the first one found nothing and reported
  // the outcome as unknowable on exactly the leads someone worked hardest.
  //
  // Paging on until it turns up is the client half of the fix. The server half
  // — an `action` filter on the history endpoint, so this is one row rather
  // than a walk — needs a contract change, filed as issue 1611.
  const { fetchNextPage, hasNextPage, isFetchingNextPage } = history;
  const pagesRead = history.data?.pages.length ?? 0;
  // Two things end the walk besides finding the row, and each is a way it
  // would otherwise never end:
  //
  //   - a later page FAILING. The pages already read stay cached, so
  //     `hasNextPage` stays true and `isFetchingNextPage` falls back to false
  //     the moment the failure settles — which re-arms the effect and retries
  //     forever, while `pending` masks the error the panel should be showing.
  //   - a history long enough that the walk is itself the problem, or a server
  //     bug handing back a cursor that never advances. The cap is generous
  //     against a real lead and finite against a pathological one; stopping
  //     early reports the outcome as unavailable, which is true.
  const WALK_PAGE_CAP = 25;
  const seeking =
    promoted &&
    !row &&
    hasNextPage &&
    !isFetchingNextPage &&
    !history.isError &&
    pagesRead < WALK_PAGE_CAP;
  useEffect(() => {
    if (seeking) {
      fetchNextPage();
    }
  }, [seeking, fetchNextPage]);

  const after = (row?.after ?? {}) as Record<string, unknown>;
  const str = (key: string) =>
    typeof after[key] === "string" ? (after[key] as string) : undefined;
  const recorded = str("dedupe_outcome");
  return {
    outcome:
      recorded === "merged" || recorded === "created" ? recorded : "unknown",
    trigger: str("trigger"),
    evidenceNote: str("evidence_note"),
    // Still walking is still pending: reporting "we cannot tell" while pages
    // are in flight is the same false certainty as reporting "created".
    // A FAILED read is never pending — the panel checks pending first, so
    // leaving both true renders a waiting line over an error nobody ever sees.
    pending:
      promoted &&
      !history.isError &&
      (history.isPending || Boolean(seeking) || isFetchingNextPage),
    failed: promoted && history.isError,
  };
}

/**
 * PromotePreviewLine says what promoting will DO before the rep commits
 * (ADR-0119/A170): merge into a contact we already hold, or create one. It
 * reads GET /leads/{id}/promote-preview, which runs the promotion's own dedupe
 * ladder without writing.
 *
 * An absent person on a `merge` never means "no match" — it means the matched
 * contact is outside the reader's row scope, and the line says so rather than
 * promising a new contact the server will not create.
 */
function PromotePreviewLine({
  id,
  open,
}: Readonly<{ id: string; open: boolean }>) {
  const t = useT();
  const preview = useQuery({
    queryKey: ["lead-promote-preview", id],
    enabled: open,
    // Always fresh: the answer is about the workspace as it stands the moment
    // the dialog opens, and a 30s-old "create" can be a merge by now.
    staleTime: 0,
    queryFn: async () => {
      const { data, error } = await api.GET("/leads/{id}/promote-preview", {
        params: { path: { id } },
      });
      if (error) {
        throwProblem(error, t);
      }
      return data;
    },
  });
  if (preview.isPending) {
    return <p className="t-caption">{t("lead.previewPending")}</p>;
  }
  // A failed preview does not block the promotion; the confirm still runs
  // the same ladder. It just cannot be described in advance.
  if (preview.isError || !preview.data) {
    return null;
  }
  if (preview.data.outcome === "create") {
    return <p className="t-caption">{t("lead.previewCreate")}</p>;
  }
  if (!preview.data.person) {
    return <p className="t-caption">{t("lead.previewMergeWithheld")}</p>;
  }
  return (
    <p className="t-caption">
      {t("lead.previewMerge")}{" "}
      <EntityRef kind="person" id={preview.data.person.id} />
    </p>
  );
}

/**
 * DemoteAction is the reversal ADR-0008 §4 promises, from the one page that
 * can honestly host it. A reason is required and recorded: an undo nobody
 * explained is later indistinguishable from a mistake.
 */
function DemoteAction({ id }: Readonly<{ id: string }>) {
  const t = useT();
  const queryClient = useQueryClient();
  const headingId = useId();
  const [open, setOpen] = useState(false);
  const [reason, setReason] = useState("");
  const demote = useMutation({
    mutationFn: async () => {
      const { data, error } = await api.POST("/leads/{id}/demote", {
        params: { path: { id } },
        body: { reason: reason.trim() },
      });
      if (error) {
        throwProblem(error, t);
      }
      return data;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["leads"] });
      queryClient.invalidateQueries({ queryKey: ["lead", id] });
      queryClient.invalidateQueries({
        queryKey: ["record-history", "lead", id],
      });
      setOpen(false);
      setReason("");
    },
  });
  const close = () => {
    setOpen(false);
    demote.reset();
  };
  return (
    <>
      <Button small onClick={() => setOpen(true)}>
        {t("lead.demote")}
      </Button>
      <Modal open={open} onClose={close} labelledBy={headingId}>
        <h2
          id={headingId}
          className="t-h2"
          style={{ marginBottom: "var(--space-3)" }}
        >
          {t("lead.demoteDialog")}
        </h2>
        <p className="t-body" style={{ marginBottom: "var(--space-3)" }}>
          {t("lead.demoteExplain")}
        </p>
        <label
          className="t-caption field"
          style={{ marginBottom: "var(--space-4)" }}
        >
          {t("lead.demoteReason")}
          <Textarea
            aria-label={t("lead.demoteReason")}
            value={reason}
            onChange={(event) => setReason(event.target.value)}
          />
        </label>
        {demote.isError && (
          <p
            className="t-caption"
            style={{ color: "var(--danger)", marginBottom: "var(--space-3)" }}
          >
            {problemMessageOf(demote.error, t)}
          </p>
        )}
        <div
          style={{
            display: "flex",
            gap: "var(--space-2)",
            justifyContent: "flex-end",
          }}
        >
          <Button small onClick={close} disabled={demote.isPending}>
            {t("create.cancel")}
          </Button>
          <Button
            small
            variant="primary"
            disabled={demote.isPending || reason.trim() === ""}
            onClick={() => demote.mutate()}
          >
            {t("lead.demoteConfirm")}
          </Button>
        </div>
      </Modal>
    </>
  );
}

/**
 * PromotedLeadPanel is what a promoted lead's page is FOR (ADR-0119/A170).
 *
 * The page used to redirect to the person, which told the reader the lead had
 * ceased to exist — untrue of a record this product keeps, audits and can
 * reverse (ADR-0008 §4). It also left the reversal that ADR promises with no
 * surface to be started from, and hid whether promotion merged into a contact
 * we already knew or created a new one. That distinction is the difference
 * between "my prospect is now a contact" and "my prospect was already someone
 * we knew".
 */
function PromotedLeadPanel({
  lead,
  promotion,
}: Readonly<{ lead: Lead; promotion: PromotionRecord }>) {
  const overlay = useSorMode() === "overlay";
  const t = useT();
  const { locale } = useLocale();
  const triggerLabel = PROMOTE_TRIGGERS.find(
    (option) => option.value === promotion.trigger,
  )?.label;
  // Four states, not two. The person link below is a fact the LEAD row carries,
  // so it renders either way; only the outcome waits on the audit read.
  const outcomeLine = () => {
    if (promotion.pending) {
      return t("lead.promotedOutcomePending");
    }
    if (promotion.failed) {
      return t("lead.promotedOutcomeUnavailable");
    }
    switch (promotion.outcome) {
      case "merged":
        return t("lead.promotedMerged");
      case "created":
        return t("lead.promotedCreated");
      // The audit row is missing, unreadable, or names an outcome this build
      // does not know. Saying so is the honest answer; picking one would be a
      // claim about a merge nobody recorded.
      case "unknown":
        return t("lead.promotedOutcomeUnavailable");
    }
  };
  return (
    <Panel title={t("lead.promotedTitle")}>
      <PanelBody>
        <p className="t-body">{outcomeLine()}</p>
        <p className="t-body" style={{ marginTop: "var(--space-2)" }}>
          <EntityRef kind="person" id={lead.promoted_person_id} />
        </p>
        {lead.promoted_at && (
          <p className="t-caption" style={{ marginTop: "var(--space-2)" }}>
            {t("lead.promotedAt")}{" "}
            {formatDateAbbrev(
              lead.promoted_at,
              locale,
              // The reader's own zone, the same one the shell stamps this
              // page's timeline rows in — a lead carries no location of its
              // own to prefer over where the reader is.
              Intl.DateTimeFormat().resolvedOptions().timeZone,
            )}
          </p>
        )}
        {triggerLabel && (
          <p className="t-caption">
            {t("lead.promotedTrigger")} {t(triggerLabel)}
          </p>
        )}
        {promotion.evidenceNote && (
          <p className="t-caption">
            {t("lead.promotedEvidence")} {promotion.evidenceNote}
          </p>
        )}
        {/* The reversal lives here and nowhere else: this is the record the
            promotion is a fact about. Not in overlay, where the mirror owns
            the person. */}
        {!overlay && (
          <div style={{ marginTop: "var(--space-3)" }}>
            <DemoteAction id={lead.id} />
          </div>
        )}
      </PanelBody>
    </Panel>
  );
}

const LEAD_TABS = ["overview", "history"] as const;
type LeadTab = (typeof LEAD_TABS)[number];

// The lead-360's "overview" pane, split out of LeadScreen so the tab switch
// doesn't push the render-prop closure over the cognitive-complexity budget.
// Every prop here is a value already resolved (or owned as local state) by
// LeadScreen — no new fetches, no behavior change from the pre-tab layout;
// the promote modal's open/trigger/note state stays lifted in the parent so
// it survives a tab switch away and back.
function LeadOverviewPane({
  lead,
  id,
  promotion,
  headingId,
  terminalReasonId,
  promoteOpen,
  closePromote,
  trigger,
  setTrigger,
  note,
  setNote,
  promotePending,
  promoteErrorMessage,
  onSubmitPromote,
  onLifecycleChanged,
}: Readonly<{
  lead: Lead;
  id: string;
  promotion: PromotionRecord;
  headingId: string;
  terminalReasonId: string;
  promoteOpen: boolean;
  closePromote: () => void;
  trigger: PromoteTrigger;
  setTrigger: (trigger: PromoteTrigger) => void;
  note: string;
  setNote: (note: string) => void;
  promotePending: boolean;
  promoteErrorMessage: string | null;
  onSubmitPromote: () => void;
  onLifecycleChanged: () => void;
}>) {
  const t = useT();
  // Promote turns a mirrored lead into a person — a write the incumbent mirror
  // refuses (unsupported_by_sor), so the button is hidden in overlay.
  const overlay = useSorMode() === "overlay";
  return (
    <>
      {!lead.archived_at && !overlay && (
        <>
          {/* The button moved to the header (ADR-0108 §6); the dialog it
              opens stays here, where the promote state lives. */}
          <div>
            <Modal
              open={promoteOpen}
              onClose={closePromote}
              labelledBy={headingId}
            >
              <h2 id={headingId} className="t-h2" style={{ marginBottom: 12 }}>
                {t("lead.promoteDialog")}
              </h2>
              <div
                style={{
                  display: "flex",
                  flexDirection: "column",
                  gap: 12,
                  marginBottom: 16,
                }}
              >
                <label className="t-caption field">
                  {t("lead.trigger")}
                  <Select
                    aria-label={t("lead.trigger")}
                    value={trigger}
                    onChange={(value) => {
                      const triggers = PROMOTE_TRIGGERS.map((o) => o.value);
                      if (isOption(value, triggers)) setTrigger(value);
                    }}
                    options={PROMOTE_TRIGGERS.map((option) => ({
                      value: option.value,
                      label: t(option.label),
                    }))}
                  />
                </label>
                <label className="t-caption field">
                  {t("lead.evidenceNote")}
                  <Textarea
                    aria-label={t("lead.evidenceNote")}
                    value={note}
                    onChange={(event) => setNote(event.target.value)}
                  />
                </label>
              </div>
              <div style={{ marginBottom: "var(--space-3)" }}>
                <PromotePreviewLine id={id} open={promoteOpen} />
              </div>
              {promoteErrorMessage && (
                <p
                  className="t-caption"
                  style={{ color: "var(--danger)", marginBottom: 12 }}
                >
                  {promoteErrorMessage}
                </p>
              )}
              <div
                style={{ display: "flex", gap: 8, justifyContent: "flex-end" }}
              >
                <Button small onClick={closePromote} disabled={promotePending}>
                  {t("create.cancel")}
                </Button>
                <Button
                  small
                  variant="primary"
                  disabled={promotePending}
                  onClick={onSubmitPromote}
                >
                  {t("lead.promoteConfirm")}
                </Button>
              </div>
            </Modal>
          </div>
        </>
      )}
      {/* Outside the terminal gate: a disqualified lead still has a score,
          a status and an owner, and a reader who opens it came to see them.
          Hiding the whole body left the page blank below the tab bar, which
          reads as a broken render rather than a closed lead. The controls
          inside it are individually disabled by their own state. */}
      {/* A promoted lead's page leads with what the promotion did — the
          reader arrived asking whether this became a contact, and which one. */}
      {lead.promoted_person_id && (
        <PromotedLeadPanel lead={lead} promotion={promotion} />
      )}
      {/* Working the lead (ADR-0118/A169): a note or a task about the
          prospect, in the one composer the person and deal pages use. Absent
          on a terminal lead, whose record is closed, and in overlay, where
          the mirror owns the activity. */}
      {!lead.archived_at && !overlay && (
        <LogActivity entityType="lead" entityId={id} />
      )}
      <LeadLifecycle
        lead={lead}
        id={id}
        onChanged={onLifecycleChanged}
        terminalReasonId={terminalReasonId}
      />
      <CustomFieldsCard object="lead" record={lead} />
    </>
  );
}

// The lead's identity row and its verbs. Extracted from LeadScreen because
// the terminal-state branch pushed that render past the complexity budget,
// and because a header is a thing in its own right: the name, why the verbs
// are gone when they are, and the verbs themselves.
function LeadActions({
  lead,
  id,
  cf,
  overlay,
  onPromote,
  terminalReasonId,
}: Readonly<{
  lead: Lead;
  id: string;
  cf: ReturnType<typeof useObjectCustomFields>;
  overlay: boolean;
  onPromote: () => void;
  // The id of the ONE sentence this page prints about being closed. Every
  // refused control points at it rather than repeating it, which is what
  // stops a terminal lead printing the same line five times.
  terminalReasonId: string;
}>) {
  const t = useT();
  return (
    <>
      {/* Promote is the page's ONE primary action and it leads, in the header
          where a reader looks for the verb (ADR-0108 §6). Ineligibility is
          stated on the control itself rather than as a sentence beside it —
          a disabled button whose reason is elsewhere is a dead button. */}
      {!lead.archived_at && (
        <Button
          variant="primary"
          reason={
            promoteEligible(lead) ? undefined : t("lead.promoteIneligible")
          }
          onClick={onPromote}
        >
          {t("lead.promote")}
        </Button>
      )}
      {/* A terminal lead keeps its controls, DISABLED with the reason
          (STATE-4a): the reason is the information, and hiding the control
          hides a fact the reader needs. Both closures reach this page — a
          disqualified lead and, since ADR-0119/A170, a promoted one — and the
          band above names which, so these controls point at that one
          sentence rather than guessing at it. */}
      <EditAction
        disabledReasonId={lead.archived_at ? terminalReasonId : undefined}
        label={t("record.edit")}
        notice={overlay ? t("overlay.partialWriteBack") : undefined}
        fields={[...leadEditFields, ...cf.formFields]}
        record={{
          id: lead.id,
          version: lead.version,
          full_name: lead.full_name ?? "",
          email: lead.email ?? "",
          title: lead.title ?? "",
          company_name: lead.company_name ?? "",
          candidate_org_key: lead.candidate_org_key ?? "",
          ...cf.recordSlice(lead),
        }}
        update={async (values) => {
          const { data, error } = await api.PATCH("/leads/{id}", {
            params: {
              path: { id },
              ...ifMatch(lead.version),
            },
            body: {
              ...mapLeadUpdate(values),
              ...cf.toBody(values),
            },
          });
          if (error) {
            throwProblem(error);
          }
          return data;
        }}
        invalidate="leads"
        recordKey="lead"
      />
      {/* The overlay seam refuses disqualify (a cross-type lifecycle
          transition) and share (a grant probes a native row a mirror lead
          does not have), so in overlay these are genuinely UNSUPPORTED
          rather than state-blocked — a different STATE-4a cause, and the
          answer for that one is absence. */}
      {!overlay && (
        <>
          <ArchiveAction
            disabledReasonId={lead.archived_at ? terminalReasonId : undefined}
            label={t("record.disqualify")}
            confirmText={t("record.disqualifyConfirm")}
            archive={async () => {
              const { data, error } = await api.DELETE("/leads/{id}", {
                params: { path: { id } },
              });
              if (error) {
                throwProblem(error, t);
              }
              return data;
            }}
            invalidate="leads"
            recordKey="lead"
            onArchived={() => navigate({ screen: "leads" })}
          />
          <ShareAction
            recordType="lead"
            recordId={lead.id}
            disabledReasonId={lead.archived_at ? terminalReasonId : undefined}
          />
        </>
      )}
    </>
  );
}

export function LeadScreen({ id }: Readonly<{ id: string }>) {
  const t = useT();
  const cf = useObjectCustomFields("lead");
  const queryClient = useQueryClient();
  const headingId = useId();
  // ONE sentence about this lead being closed, minted here and pointed at by
  // every control the closure refuses (ADR-0108 §6).
  const terminalReasonId = useId();
  const [tab, setTab] = useState<LeadTab>("overview");
  // The seam serves update for a mirrored lead (write-back projects onto the
  // incumbent, overlay/provider_writes.go), so Edit renders in overlay too.
  // DELETE /leads/{id} is disqualify_lead, not an archive — a cross-type
  // lifecycle transition the seam refuses outright, so it and share stay
  // hidden (share: a record grant probes the native lead row, which a
  // mirror lead has no row in — see deals.tsx's DealBadges).
  const overlay = useSorMode() === "overlay";
  const leadQuery = useQuery({
    queryKey: ["lead", id],
    queryFn: async () => {
      const { data, error } = await api.GET("/leads/{id}", {
        params: { path: { id } },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });
  // A lead's own activities: what we already did about this prospect
  // (ADR-0118/A169). `activity_link` has carried the lead arm since migration
  // 0038; only the screen was missing.
  const timelineQuery = useRecordTimeline("lead", id);
  const viewerId = useViewerId();
  const [promoteOpen, setPromoteOpen] = useState(false);
  const [trigger, setTrigger] = useState<PromoteTrigger>("human_qualify");
  const [note, setNote] = useState("");

  const closePromote = () => {
    setPromoteOpen(false);
    setTrigger("human_qualify");
    setNote("");
    promote.reset();
  };

  const promote = useMutation({
    mutationFn: async (body: PromoteLeadRequest) => {
      const { data, error } = await api.POST("/leads/{id}/promote", {
        params: { path: { id } },
        body,
      });
      if (error) {
        throwProblem(error, t);
      }
      return data;
    },
    onSuccess: (result) => {
      queryClient.invalidateQueries({ queryKey: ["leads"] });
      queryClient.invalidateQueries({ queryKey: ["lead", id] });
      // The promotion WROTE the audit row this page reads its outcome from. A
      // reader who opened the History tab before promoting holds a cached last
      // page saying there is nothing more to fetch, so without this the panel
      // walks no further and reports the outcome unavailable while the row
      // sits one page away.
      queryClient.invalidateQueries({
        queryKey: ["record-history", "lead", id],
      });
      setPromoteOpen(false);
      navigate({ screen: "contacts", id: result.person.id });
    },
    onError: (error) => {
      const existingPersonId = alreadyPromotedPersonId(error);
      if (existingPersonId) {
        setPromoteOpen(false);
        navigate({ screen: "contacts", id: existingPersonId });
      }
    },
  });

  // already_promoted is handled by navigating away (onError above), so it
  // never renders as an error — anything else surfaces verbatim.
  const promoteErrorMessage =
    promote.isError && !alreadyPromotedPersonId(promote.error)
      ? problemMessageOf(promote.error, t)
      : null;

  // A promoted lead keeps its page (ADR-0119/A170). It no longer redirects to
  // the person: the redirect said the lead had ceased to exist, which is untrue
  // of a record this product keeps, audits and can reverse — and it left the
  // reversal with nowhere to start from. The page reads the promotion off its
  // own audit row and says what happened.
  const promotion = usePromotionRecord(
    id,
    Boolean(leadQuery.data?.promoted_person_id),
  );

  return (
    <div className="wrap lead-surface">
      <QueryGate query={leadQuery}>
        {(lead) => (
          <RecordView
            name={lead.full_name ?? lead.email ?? t("nav.leads")}
            avatarSrc={null}
            // The "Lead" marker rides the identity, not a badge among badges:
            // a reader has to know this is a prospect and not a contact
            // BEFORE they read anything else about them (ADR-0108 §1).
            subtitle={<Badge tone="accent">{t("lead.marker")}</Badge>}
            pulse={
              lead.email ? <span className="t-mono">{lead.email}</span> : null
            }
            actions={
              <LeadActions
                lead={lead}
                id={id}
                cf={cf}
                overlay={overlay}
                terminalReasonId={terminalReasonId}
                onPromote={() => setPromoteOpen(true)}
              />
            }
            actionsInline
            // The shell stamps timeline rows in this zone. The viewer's own is
            // the honest default for a prospect: a lead carries no workspace
            // location of its own to prefer over where the reader is.
            zone={Intl.DateTimeFormat().resolvedOptions().timeZone}
            timeline={
              timelineQuery.isSuccess
                ? activityTimeline(timelineQuery.data.data, viewerId)
                : []
            }
            timelineNotice={overlay ? <OverlayUnavailable /> : undefined}
            // The readings ride the band, above the columns: they describe the
            // PROSPECT, and a strip that vanished on the History tab would
            // move the tab bar and re-flow the page under the reader.
            band={
              <>
                <LeadBadges lead={lead} />
                {/* Stated ONCE for the page. Every control the closure
                    refuses points at this element by id, so a screen reader
                    reaches it from each of them without the sentence being
                    printed beside all six. */}
                {lead.archived_at && (
                  <p id={terminalReasonId} className="t-caption">
                    {/* Which closure, not merely THAT it is closed. Both
                        terminal states archive the row, so keying this off
                        archived_at alone told every promoted lead it had been
                        disqualified — invisible until ADR-0119 stopped the
                        page redirecting away before anyone could read it. */}
                    {lead.status === "promoted"
                      ? t("lead.terminalPromoted")
                      : t("lead.terminalDisqualified")}
                  </p>
                )}
              </>
            }
          >
            {/* The bar leads the column it governs. */}
            <div style={{ marginBottom: "var(--space-4)" }}>
              <SegmentedControl
                options={LEAD_TABS}
                value={tab}
                onChange={setTab}
                labels={{
                  overview: t("tab.overview"),
                  history: t("tab.history"),
                }}
              />
            </div>
            {tab === "overview" && (
              <LeadOverviewPane
                lead={lead}
                id={id}
                promotion={promotion}
                headingId={headingId}
                terminalReasonId={terminalReasonId}
                promoteOpen={promoteOpen}
                closePromote={closePromote}
                trigger={trigger}
                setTrigger={setTrigger}
                note={note}
                setNote={setNote}
                promotePending={promote.isPending}
                promoteErrorMessage={promoteErrorMessage}
                onSubmitPromote={() => {
                  const trimmedNote = note.trim();
                  promote.mutate({
                    trigger,
                    evidence: trimmedNote ? { note: trimmedNote } : undefined,
                  });
                }}
                onLifecycleChanged={() => {
                  queryClient.invalidateQueries({ queryKey: ["leads"] });
                  queryClient.invalidateQueries({ queryKey: ["lead", id] });
                }}
              />
            )}
            {tab === "history" && !overlay && (
              <RecordHistoryTab kind="lead" id={lead.id} />
            )}
            {tab === "history" && overlay && <OverlayUnavailable />}
          </RecordView>
        )}
      </QueryGate>
    </div>
  );
}
