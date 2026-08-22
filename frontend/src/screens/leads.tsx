import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { type ReactNode, useEffect, useId, useRef, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { ifMatch, requireVersion } from "../api/version";
import { activityTimeline } from "../design-system/activitytimeline";
import {
  Badge,
  Button,
  Card,
  Disclosure,
  Modal,
  SegmentedControl,
  Textarea,
  TextInput,
} from "../design-system/atoms";
import { RecordView } from "../design-system/composed";
import { FieldGrid, FieldRow } from "../design-system/fieldgrid";
import { InlineChoice, InlineText } from "../design-system/inlinechoice";
import { Panel, PanelBody } from "../design-system/panel";
import {
  useRecordTimeline,
  useTimelineFilters,
} from "../design-system/recordtimeline";
import { Select } from "../design-system/select";
import { TimelineFilterBar } from "../design-system/timelinefilterbar";
import { formatDateAbbrev } from "../format/format";
import { viewerZone } from "../format/timezone";
import { useLocale, useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import {
  LoadMoreButton,
  OverlayUnavailable,
  problemMessageOf,
  QueryGate,
  throwProblem,
  timelineZoneNotice,
  useMe,
  useSorMode,
  useViewerId,
} from "./common";
import type { CreateField } from "./create";
import { CustomFieldsCard } from "./customfields.card";
import { useObjectCustomFields } from "./customfields.form";
import { EditAction } from "./edit";
import {
  EntityRef,
  RosterPartialNote,
  useRoster,
  useRosterPartial,
} from "./entityref";
import { RecordHistoryTab, useRecordHistory } from "./history";
import {
  FirstResponseLine,
  promoteEligible,
  StatusBadge,
  scoreFactorLabel,
  scoreTone,
  terminalBadge,
} from "./leadpresentation";
import { DisqualifyDialog } from "./leads.disqualify";
import { QualifyDialog } from "./leads.qualify";
import { LeadStepper } from "./leads.stepper";
import { LeadManualSignals } from "./leadsignals";
import { LogActivity } from "./logactivity";
import { ShareAction } from "./share";
import { groupChronology } from "./timelinegroups";
import "./leads.css";

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
type UpdateLeadRequest = components["schemas"]["UpdateLeadRequest"];

import {
  sourceLabelFor,
  sourcePickOptions,
  useLeadSources,
} from "./leadsources";

export {
  promoteEligible,
  scoreTone,
  terminalBadge,
} from "./leadpresentation";
export { LeadsScreen } from "./leads.list";

// The recorded trigger, as the outcome card names it — the wire token never
// reaches the reader.
function promotionTriggerLabel(
  trigger: string | null | undefined,
): MessageKey | null {
  switch (trigger) {
    case "inbound_reply":
      return "lead.trigger.inboundReply";
    case "meeting_booked":
      return "lead.trigger.meetingBooked";
    case "meeting_held":
      return "lead.trigger.meetingHeld";
    case "human_qualify":
      return "lead.trigger.humanQualify";
    default:
      return null;
  }
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
  };
}

const leadEditFields: CreateField[] = [
  { key: "full_name", label: "create.fullName", required: true },
  { key: "email", label: "create.email", type: "email" },
  { key: "title", label: "create.personTitle" },
  { key: "company_name", label: "create.companyName" },
];

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
// Ownership: who holds the lead, and reassignment to any workspace user.
// The owner reads as a NAME — EntityRef resolves it off the shared `/users`
// roster and falls back to the id only while that load is in flight or when
// the viewer cannot see the roster, so a reader is never handed a bare uuid.
// Reassignment is a plain owner change (UC-E13-04): the server audits it and
// keeps whatever routing decision it overrides, so the only thing this
// control owes the reader is an honest list of who they can hand it to.
// How one candidate reads in the list. The viewer reads as "Me": a rep scanning
// this list looks for themselves, not for their own name among colleagues'. A
// user with no display name still has to be pickable, so the id stands in
// rather than rendering a blank row.
function candidateLabel(
  entry: Readonly<{ id: string; display_name?: string }>,
  meId: string | undefined,
  t: ReturnType<typeof useT>,
): string {
  if (entry.id === meId) {
    return t("lead.assignToMe");
  }
  return entry.display_name ?? entry.id;
}

// The assignee list's four readings, as early returns rather than one chained
// conditional: a roster still arriving, a roster that failed, a workspace with
// nobody else in it, and the picker itself.
function AssigneePicker({
  roster,
  rosterPartial,
  candidates,
  meId,
  pending,
  onPick,
}: Readonly<{
  roster: ReturnType<typeof useRoster>;
  rosterPartial: boolean;
  candidates: readonly Readonly<{ id: string; display_name?: string }>[];
  meId: string | undefined;
  pending: boolean;
  onPick: (ownerId: string) => void;
}>) {
  const t = useT();
  if (roster.isPending) {
    return <span className="t-caption">{t("share.rosterLoading")}</span>;
  }
  if (roster.isError) {
    return (
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
    );
  }
  // "Nobody else" is a claim about the WHOLE workspace, so only a roster read
  // to its end may make it. Over a walk that stopped early it would report a
  // lead as unassignable when the colleague to hand it to sits on a page
  // nothing here read.
  if (candidates.length === 0 && !rosterPartial) {
    return <span className="t-caption">{t("lead.assignNobodyElse")}</span>;
  }
  return (
    <>
      <Select
        aria-label={t("lead.assignTo")}
        placeholder={t("lead.assignChoose")}
        value=""
        disabled={pending}
        options={candidates.map((entry) => ({
          value: entry.id,
          label: candidateLabel(entry, meId, t),
        }))}
        onChange={onPick}
      />
      <RosterPartialNote partial={rosterPartial} />
    </>
  );
}

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
  const rosterPartial = useRosterPartial("user", picking);
  // Everyone but the current owner, with the VIEWER first: assigning to
  // yourself is the common case on a small team, and it is now an option in
  // this one control rather than a button of its own (ADR-0108 §5).
  const candidates = (roster.data ?? [])
    .filter((entry) => !("is_agent" in entry) || !entry.is_agent)
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
        {picking && (
          <AssigneePicker
            roster={roster}
            rosterPartial={rosterPartial}
            candidates={candidates}
            meId={meId}
            pending={pending}
            onPick={(value) => {
              onAssign(value);
              setPicking(false);
            }}
          />
        )}
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
  const sources = useLeadSources();
  const fromConnector = (lead.source ?? "").startsWith("connector:");
  // One write at a time: a second row opened while a save is in flight would
  // carry the If-Match the first write is about to make stale.
  const canEdit = !readOnlyReason && !saving;
  return (
    <Panel title={t("lead.details")}>
      <PanelBody>
        <FieldGrid>
          <FieldRow label={t("create.fullName")}>
            <InlineText
              label={t("create.fullName")}
              value={lead.full_name ?? ""}
              placeholder={t("lead.detailsUnset")}
              canEdit={canEdit}
              readOnlyReason={readOnlyReason}
              onSave={(next) => save({ full_name: next.trim() || null })}
            />
          </FieldRow>
          <FieldRow label={t("create.personTitle")}>
            <InlineText
              label={t("create.personTitle")}
              value={lead.title ?? ""}
              placeholder={t("lead.detailsUnset")}
              canEdit={canEdit}
              readOnlyReason={readOnlyReason}
              onSave={(next) => save({ title: next.trim() || null })}
            />
          </FieldRow>
          <FieldRow label={t("create.companyName")}>
            <InlineText
              label={t("create.companyName")}
              value={lead.company_name ?? ""}
              placeholder={t("lead.detailsUnset")}
              canEdit={canEdit}
              readOnlyReason={readOnlyReason}
              onSave={(next) => save({ company_name: next.trim() || null })}
            />
          </FieldRow>
          <FieldRow label={t("create.email")}>
            {lead.email ?? t("lead.detailsUnset")}
          </FieldRow>
          <FieldRow label={t("create.linkedinUrl")}>
            {lead.linkedin_url ? (
              <a href={lead.linkedin_url} target="_blank" rel="noreferrer">
                {t("lead.openLinkedIn")}
              </a>
            ) : (
              t("lead.detailsUnset")
            )}
          </FieldRow>
          <FieldRow label={t("lead.source")}>
            <InlineChoice
              label={t("lead.source")}
              hideLabel
              value={lead.source ?? ""}
              options={sourcePickOptions(sources.data?.data, lead.source, t)}
              // A connector's value stays where the connector put it; the
              // administered list is what a human may pick from.
              canEdit={canEdit && !fromConnector}
              readOnlyReason={
                fromConnector ? t("lead.sourceFromConnector") : readOnlyReason
              }
              render={() => sourceLabelFor(lead, sources.data?.data, t)}
              onSave={(next) => save({ source: next })}
            />
          </FieldRow>
          {lead.project_id && (
            <FieldRow label={t("lead.project")}>
              <EntityRef kind="project" id={lead.project_id} />
            </FieldRow>
          )}
        </FieldGrid>
        {/* Email is read-only here because it is the lead's dedupe key. The Edit
            modal owns the write so a 409 collision with a live lead has a place
            to link the incumbent record. */}
      </PanelBody>
    </Panel>
  );
}

function LeadLifecycle({
  lead,
  id,
  onChanged,
  terminalReasonId,
  onQualify,
  onDisqualify,
  overlay,
}: Readonly<{
  lead: Lead;
  id: string;
  onChanged: () => void;
  terminalReasonId: string;
  onQualify: () => void;
  onDisqualify: () => void;
  overlay: boolean;
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
    mutationKey: ["lead-edit", id],
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
        params: { path: { id }, ...ifMatch(requireVersion(lead.version)) },
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
      {/* The ladder leads: where this lead stands and how it got there is
          the first thing a rep needs, and the step they take next is one
          click on it (the terminal steps open their own dialogs). The mirror
          refuses a lifecycle write, so in overlay the ladder only reads. */}
      <LeadStepper
        lead={lead}
        pending={patch.isPending}
        readOnlyReason={
          readOnly
            ? t("lead.terminalReadOnly")
            : overlay
              ? t("lead.ladder.overlay")
              : undefined
        }
        onStep={(status) => {
          // Same one-write-at-a-time rule as the inline rows: a status
          // sent while another save is in flight races it for If-Match.
          if (!patch.isPending && !readOnly && !overlay) {
            patch.mutate({ status });
          }
        }}
        onQualify={onQualify}
        onDisqualify={onDisqualify}
      />
      {/* The first-response line only exists while the target is on: the
          server derives sla_state from the setting, so an installation that
          never opted in sees nothing here. */}
      <FirstResponseLine lead={lead} />
      <LeadIdentityFields
        lead={lead}
        save={saveField}
        saving={patch.isPending}
        readOnlyReason={readOnly ? t("lead.terminalReadOnly") : undefined}
      />

      <LeadOwner
        lead={lead}
        meId={meId}
        terminalReasonId={terminalReasonId}
        pending={patch.isPending || readOnly}
        onAssign={(ownerId) => patch.mutate({ owner_id: ownerId })}
      />

      {/* The score is a reading, not the work: it folds to one line with
          its top factor, and opens for the breakdown, the override and the
          rep's own inputs. */}
      <Disclosure
        summary={
          <span className="lead-score-summary">
            <Badge tone={scoreTone(lead.score)}>
              {t("lead.score")}: {lead.score}
            </Badge>{" "}
            <span className="t-caption">
              {lead.score_reason
                ? scoreFactorLabel(lead.score_reason, t)
                : t("lead.scoreNoSignals")}
            </span>
          </span>
        }
      >
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
        <LeadManualSignals
          // Keyed by lead: a half-typed input for one lead must not be
          // submitted against the next one the reader navigates to.
          key={id}
          id={id}
          readOnlyReason={readOnly ? t("lead.terminalReadOnly") : undefined}
        />
      </Disclosure>

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
      {lead.source && <Badge>{sourceLabelFor(lead, undefined, t)}</Badge>}
      {terminal && <Badge tone={terminal.tone}>{t(terminal.label)}</Badge>}
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
  const triggerLabel = promotionTriggerLabel(promotion.trigger);
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
              viewerZone(),
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
// The status ladder climbs a few seconds AFTER a logged touch: the workflow
// runs off the outbox, in the worker, not inside the POST. Without a delayed
// re-read the stepper keeps the old status until a manual reload — which
// reads as "logging did nothing". Three staggered re-reads cover the
// relay+workflow latency without polling forever; unmounting cancels them.
function useLadderRefresh(id: string): () => void {
  const queryClient = useQueryClient();
  const timers = useRef<number[]>([]);
  useEffect(
    () => () => {
      for (const timer of timers.current) {
        window.clearTimeout(timer);
      }
    },
    [],
  );
  return () => {
    for (const delay of [1500, 4000, 8000]) {
      timers.current.push(
        window.setTimeout(() => {
          queryClient.invalidateQueries({ queryKey: ["lead", id] });
          queryClient.invalidateQueries({ queryKey: ["leads"] });
          queryClient.invalidateQueries({
            queryKey: ["record-history", "lead", id],
          });
        }, delay),
      );
    }
  };
}

function LeadOverviewPane({
  lead,
  id,
  promotion,
  terminalReasonId,
  onQualify,
  onDisqualify,
  onLifecycleChanged,
  onTouchLogged,
}: Readonly<{
  lead: Lead;
  id: string;
  promotion: PromotionRecord;
  terminalReasonId: string;
  onQualify: () => void;
  onDisqualify: () => void;
  onLifecycleChanged: () => void;
  // Owned by LeadScreen, ABOVE the tab switch: the refresh timers this
  // schedules must survive this pane unmounting when the reader flips to
  // History mid-climb.
  onTouchLogged: () => void;
}>) {
  // Qualify turns a mirrored lead into a person — a write the incumbent mirror
  // refuses (unsupported_by_sor), so the verbs are hidden in overlay.
  const overlay = useSorMode() === "overlay";
  return (
    <>
      {/* A promoted lead's page leads with what the promotion did — the
          reader arrived asking whether this became a contact, and which one. */}
      {lead.promoted_person_id && (
        <PromotedLeadPanel lead={lead} promotion={promotion} />
      )}
      <LeadLifecycle
        lead={lead}
        id={id}
        onChanged={onLifecycleChanged}
        terminalReasonId={terminalReasonId}
        onQualify={onQualify}
        onDisqualify={onDisqualify}
        overlay={overlay}
      />
      {/* The composer follows the facts so opening a lead answers "what
          should I do" before asking the rep to type. */}
      {!lead.archived_at && !overlay && (
        <LogActivity entityType="lead" entityId={id} onLogged={onTouchLogged} />
      )}
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
  onQualify,
  onDisqualify,
  terminalReasonId,
}: Readonly<{
  lead: Lead;
  id: string;
  cf: ReturnType<typeof useObjectCustomFields>;
  overlay: boolean;
  onQualify: () => void;
  onDisqualify: () => void;
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
      {!lead.archived_at && !overlay && (
        <Button
          variant="primary"
          data-testid="lead-qualify"
          reason={
            promoteEligible(lead) ? undefined : t("lead.promoteIneligible")
          }
          onClick={onQualify}
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
          ...cf.recordSlice(lead),
        }}
        update={async (values) => {
          const { data, error } = await api.PATCH("/leads/{id}", {
            params: {
              path: { id },
              ...ifMatch(requireVersion(lead.version)),
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
          {/* Disqualify asks why, in its own dialog — and is a secondary
              verb, not a red one: closing a lead is routine work. A terminal
              lead keeps the control, disabled with the page's one reason. */}
          <Button
            data-testid="lead-disqualify"
            reasonId={lead.archived_at ? terminalReasonId : undefined}
            reason={lead.archived_at ? t("lead.terminalReadOnly") : undefined}
            onClick={onDisqualify}
          >
            {t("record.disqualify")}
          </Button>
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

// The two governed-transition dialogs, mounted only while open and keyed by
// the lead so a half-filled deal block for one lead never carries to the
// next. The qualify outcome comes back as the sentence the page shows.
function LeadDialogs({
  lead,
  dialog,
  onClose,
  onQualified,
}: Readonly<{
  lead: Lead;
  dialog: "qualify" | "disqualify" | null;
  onClose: () => void;
  onQualified: (done: ReactNode) => void;
}>) {
  const t = useT();
  if (dialog === "qualify") {
    return (
      <QualifyDialog
        key={`qualify-${lead.id}`}
        lead={lead}
        open
        onClose={onClose}
        onQualified={(result) =>
          onQualified(
            <span>
              {t("lead.qualify.done", {
                name: lead.full_name ?? lead.email ?? "",
              })}{" "}
              <EntityRef kind="person" id={result.person.id} />
              {result.deal_id && (
                <>
                  {" · "}
                  <EntityRef kind="deal" id={result.deal_id} />
                </>
              )}
            </span>,
          )
        }
      />
    );
  }
  if (dialog === "disqualify") {
    return (
      <DisqualifyDialog
        key={`disqualify-${lead.id}`}
        lead={lead}
        open
        onClose={onClose}
        onDisqualified={onClose}
      />
    );
  }
  return null;
}

export function LeadScreen({ id }: Readonly<{ id: string }>) {
  const refreshAfterTouch = useLadderRefresh(id);
  const t = useT();
  const cf = useObjectCustomFields("lead");
  const queryClient = useQueryClient();
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
  const [timelineFilters, setTimelineFilters] = useTimelineFilters(id);
  const timelineQuery = useRecordTimeline("lead", id, {
    filters: timelineFilters,
  });
  const viewerId = useViewerId();
  const timelineEntries = activityTimeline(timelineQuery.activities, viewerId);
  const [dialog, setDialog] = useState<"qualify" | "disqualify" | null>(null);
  // What the last qualify did, said once under the header: the contact and,
  // when one was opened, the deal. The page stays (ADR-0119): a promoted
  // lead keeps its page, and the outcome card below carries the links on.
  const [toast, setToast] = useState<ReactNode>(null);
  // This screen stays mounted from one lead to the next: an open dialog or a
  // sentence about the previous lead must not carry over to this one.
  // biome-ignore lint/correctness/useExhaustiveDependencies: the reset runs when the lead changes, and the lead is the only input
  useEffect(() => {
    setDialog(null);
    setToast(null);
  }, [id]);

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
              lead.email ? (
                <span className="t-mono lead-email">{lead.email}</span>
              ) : null
            }
            actions={
              <LeadActions
                lead={lead}
                id={id}
                cf={cf}
                overlay={overlay}
                terminalReasonId={terminalReasonId}
                onQualify={() => setDialog("qualify")}
                onDisqualify={() => setDialog("disqualify")}
              />
            }
            actionsInline
            // The shell stamps timeline rows in this zone. The viewer's own is
            // the honest default for a prospect: a lead carries no workspace
            // location of its own to prefer over where the reader is.
            zone={viewerZone()}
            timeline={timelineEntries}
            timelineGroups={groupChronology(
              timelineEntries,
              timelineQuery.hasNextPage,
            )}
            timelineHeader={
              overlay ? undefined : (
                <TimelineFilterBar
                  value={timelineFilters}
                  onChange={setTimelineFilters}
                />
              )
            }
            timelineFooter={<LoadMoreButton query={timelineQuery} />}
            timelineNotice={timelineZoneNotice(
              { overlay, pending: timelineQuery.isPending },
              t,
            )}
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
            {toast && (
              <div className="toast-region">
                <output className="toast">
                  <span className="dot dot-auto" />
                  {toast}
                </output>
              </div>
            )}
            {tab === "overview" && (
              <LeadOverviewPane
                lead={lead}
                id={id}
                promotion={promotion}
                terminalReasonId={terminalReasonId}
                onQualify={() => setDialog("qualify")}
                onDisqualify={() => setDialog("disqualify")}
                onLifecycleChanged={() => {
                  queryClient.invalidateQueries({ queryKey: ["leads"] });
                  queryClient.invalidateQueries({ queryKey: ["lead", id] });
                }}
                onTouchLogged={refreshAfterTouch}
              />
            )}
            <LeadDialogs
              lead={lead}
              dialog={dialog}
              onClose={() => setDialog(null)}
              onQualified={(done) => {
                setDialog(null);
                setToast(done);
              }}
            />
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
