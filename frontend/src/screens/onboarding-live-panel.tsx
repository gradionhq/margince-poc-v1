// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { AlertTriangle, Check, ChevronDown, Circle } from "lucide-react";
import { type ReactNode, useEffect, useId, useState } from "react";
import type { components } from "../api/schema";
import { Button } from "../design-system/atoms";
import {
  ConfidenceMeter,
  type Evidence,
  EvidenceChip,
} from "../design-system/trust";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { coldFieldLabel, namedSiteReadKind } from "./common";
import { confidenceLevel } from "./inbox";
import "./onboarding-live-panel.css";

// The live panel: the right-hand pane of the onboarding working view, where
// the record the read produced assembles itself. Three things make it a
// dossier rather than a dump of findings:
//
//  - Nothing shows until the read is finished. A half-filled dossier invites
//    the reader to correct data that is still arriving.
//  - Every card states its own count while collapsed, so leaving a card shut
//    is an informed choice rather than a missed one.
//  - The one card a human must answer (which legal entity this installation
//    is) cannot be folded away.
//
// Purely presentational: every value arrives as a prop, so the panel renders
// identically from a live read, a story fixture, and a test.

type CompanySiteRead = components["schemas"]["CompanySiteRead"];
type SitePage = components["schemas"]["CompanySiteReadPage"];
type SitePerson = components["schemas"]["CompanySiteReadPerson"];
type LegalEntity = components["schemas"]["CompanySiteReadLegalEntity"];

/**
 * The evidenced-field shape BOTH wire types satisfy: the read's
 * `ColdStartField` (which adds `source_kind` and makes `source_url`
 * nullable) and the proposal's `OnboardingCompanyProposalField` (which has a
 * required `source_url` and no `source_kind`). A row can only render what
 * both carry, so this is exactly that intersection — no cast at either call
 * site.
 */
export type PanelField = Readonly<{
  field: string;
  value: string;
  confidence: number;
  evidence_snippet: string;
  source_url?: string | null;
}>;

export type StepState = "done" | "now" | "waiting";

/**
 * Voice and connect have no wire in this panel yet, so their blocks can only
 * state honestly that nothing is built. Narrowing the prop keeps a caller
 * from marking them done under a placeholder that would then be a lie.
 */
export type PendingStepState = Extract<StepState, "now" | "waiting">;

/**
 * The identity half of the closed 16-value profile-field enum: who the
 * company legally is. Everything else is positioning — the complement, not a
 * second list, so a field added to the enum upstream lands in a card instead
 * of disappearing from the panel.
 */
const IDENTITY_FIELDS: ReadonlySet<string> = new Set([
  "display_name",
  "legal_name",
  "registered_address",
  "register_vat",
  "industry",
]);

// EvidenceChip renders a quote and its source, so it needs both. A field
// whose evidence lives in pasted text or the user's own words has no URL to
// cite (source_kind text/self_description) and gets no chip — the absence is
// the honest signal, not a chip pointing nowhere.
function evidenceOf(
  snippet: string | undefined,
  source: string | null | undefined,
): Evidence | null {
  if (!snippet || !source) {
    return null;
  }
  return { snippet, source };
}

function partitionFields(fields: readonly PanelField[]): {
  identity: PanelField[];
  positioning: PanelField[];
} {
  const identity: PanelField[] = [];
  const positioning: PanelField[] = [];
  for (const field of fields) {
    (IDENTITY_FIELDS.has(field.field) ? identity : positioning).push(field);
  }
  return { identity, positioning };
}

function fieldValue(
  fields: readonly PanelField[],
  name: string,
): string | undefined {
  const value = fields.find((field) => field.field === name)?.value.trim();
  return value ? value : undefined;
}

// ---------------------------------------------------------------------------
// The sticky head
// ---------------------------------------------------------------------------

function summaryRows(
  identity: Readonly<{ name?: string; offer?: string; icp?: string }>,
): ReadonlyArray<{ labelKey: MessageKey; value: string }> {
  const candidates: ReadonlyArray<{
    labelKey: MessageKey;
    value: string | undefined;
  }> = [
    { labelKey: "ob.live.summaryYouAre", value: identity.name },
    { labelKey: "ob.live.summaryYouSell", value: identity.offer },
    { labelKey: "ob.live.summaryYouSellTo", value: identity.icp },
  ];
  return candidates.flatMap((row) =>
    row.value ? [{ labelKey: row.labelKey, value: row.value }] : [],
  );
}

/**
 * The panel's sticky header. It bleeds to the panel edges and casts a shadow
 * under itself, so content scrolling beneath it reads as passing under a
 * surface rather than vanishing at a line.
 */
export function LivePanelHead({
  host,
  done,
  identity,
  factCount,
  pageCount,
}: Readonly<{
  host: string;
  done: boolean;
  identity: Readonly<{ name?: string; offer?: string; icp?: string }>;
  factCount: number;
  pageCount: number;
}>) {
  const t = useT();
  const rows = done ? summaryRows(identity) : [];
  return (
    <header className="ob-live-head" data-done={done}>
      <div className="ob-live-head-line">
        <h2 className="ob-live-head-title">
          {t(done ? "ob.live.headDone" : "ob.live.headReading", { host })}
        </h2>
        {!done && (
          <span className="ob-live-working" aria-hidden="true">
            <span />
            <span />
            <span />
          </span>
        )}
      </div>
      {done && (
        <div className="ob-live-summary">
          <p className="ob-live-summary-heading">
            {t("ob.live.summaryHeading")}
          </p>
          {rows.map((row) => (
            <p className="ob-live-summary-row" key={row.labelKey}>
              <span className="ob-live-summary-label">{t(row.labelKey)}</span>
              <span className="ob-live-summary-value">{row.value}</span>
            </p>
          ))}
          <p className="ob-live-summary-volume">
            {t("ob.live.summaryVolume", {
              facts: factCount,
              pages: pageCount,
            })}
          </p>
        </div>
      )}
    </header>
  );
}

/**
 * What the panel shows while the read runs: one line saying nothing is saved
 * yet. Announced politely, because it replaces the whole dossier.
 */
export function ReadingNotice() {
  const t = useT();
  return (
    <p className="ob-live-notice" aria-live="polite">
      {t("ob.live.nothingSaved")}
    </p>
  );
}

// ---------------------------------------------------------------------------
// Step blocks and cards
// ---------------------------------------------------------------------------

/**
 * One numbered stage of the onboarding, with its state said in words. A
 * `waiting` block renders nothing at all: a step the conversation has not
 * reached is not shown as an empty promise.
 */
export function StepBlock({
  n,
  title,
  state,
  children,
}: Readonly<{
  n: number;
  title: string;
  state: StepState;
  children: ReactNode;
}>) {
  const t = useT();
  if (state === "waiting") {
    return null;
  }
  return (
    <section className="ob-live-step" data-state={state}>
      <div className="ob-live-step-head">
        <span className="ob-live-step-n">{n}</span>
        <h3 className="ob-live-step-title">{title}</h3>
        <span className="ob-live-step-state">
          {t(state === "done" ? "ob.live.stateDone" : "ob.live.stateNow")}
        </span>
      </div>
      {children}
    </section>
  );
}

// Shape, not only colour, separates the three card states: a filled check, an
// open ring, an alert triangle.
function CardGlyph({
  done,
  needsDecision,
}: Readonly<{ done: boolean; needsDecision: boolean }>) {
  if (needsDecision) {
    return (
      <AlertTriangle className="ob-live-card-glyph" aria-hidden size={14} />
    );
  }
  if (done) {
    return <Check className="ob-live-card-glyph" aria-hidden size={14} />;
  }
  return <Circle className="ob-live-card-glyph" aria-hidden size={14} />;
}

/**
 * A folding card in the dossier stack. Collapsed by default — the count in
 * its header is what makes that safe.
 *
 * The `needsDecision` variant is forced open and has no toggle: its header is
 * plain text, not a button, because this is the one card that may not be
 * dismissed unread.
 *
 * `revealed` is the narration pointing here. The conversation says which field
 * it just learned and the artifact pulses that row — which cannot happen while
 * the row is unmounted inside a collapsed card, so a card the narration names
 * opens itself. It then STAYS open: snapping shut behind a reader whose
 * attention was just directed into it would undo the pointing.
 */
export function DossierCard({
  title,
  count,
  done,
  needsDecision,
  revealed,
  children,
}: Readonly<{
  title: string;
  count?: string;
  done?: boolean;
  needsDecision?: boolean;
  revealed?: boolean;
  children: ReactNode;
}>) {
  const t = useT();
  const [open, setOpen] = useState(false);
  useEffect(() => {
    if (revealed === true) {
      setOpen(true);
    }
  }, [revealed]);
  const decision = needsDecision === true;
  const head = (
    <>
      <CardGlyph done={done === true} needsDecision={decision} />
      <span className="ob-live-card-title">{title}</span>
      {count !== undefined && (
        <span className="ob-live-card-count">{count}</span>
      )}
    </>
  );
  return (
    <section
      className="ob-live-card"
      data-decision={decision}
      data-done={done === true}
    >
      {decision ? (
        <div className="ob-live-card-head">{head}</div>
      ) : (
        <button
          type="button"
          className="ob-live-card-head"
          aria-expanded={open}
          onClick={() => setOpen(!open)}
        >
          {head}
          <span className="ob-live-card-toggle">
            {t(open ? "ob.live.hide" : "ob.live.review")}
          </span>
          <ChevronDown className="ob-live-card-chev" aria-hidden size={14} />
        </button>
      )}
      {(decision || open) && (
        <div className="ob-live-card-body">{children}</div>
      )}
    </section>
  );
}

/**
 * One extracted profile field: how sure the read is, what it says, and the
 * quote it read it from.
 */
export function FieldRow({ field }: Readonly<{ field: PanelField }>) {
  const t = useT();
  const level = confidenceLevel(field.confidence);
  const evidence = evidenceOf(field.evidence_snippet, field.source_url);
  const value = field.value.trim();
  return (
    // data-finding-id is a behavioural contract, not decoration: narration on
    // the left carries the field name it just learned, and the artifact pulses
    // and scrolls to the matching row. Drop the attribute and speech stops
    // pointing at evidence.
    <div className="ob-live-field" data-finding-id={field.field}>
      <div className="ob-live-field-head">
        {level && <ConfidenceMeter level={level} />}
        <span className="ob-live-field-label">
          {coldFieldLabel(field.field, t)}
        </span>
        <span className="ob-live-field-pct">
          {Math.round(field.confidence * 100)}%
        </span>
      </div>
      <p className="ob-live-field-value" data-empty={value === ""}>
        {value === "" ? t("ob.live.noValue") : value}
      </p>
      {evidence && <EvidenceChip evidence={evidence} />}
    </div>
  );
}

/**
 * The people the read proposed as leads. The empty state names the reason
 * nobody was proposed — the product's restraint is a feature, and stating it
 * is the difference between restraint and looking broken.
 */
export function PeopleCard({
  people,
}: Readonly<{ people: readonly SitePerson[] }>) {
  const t = useT();
  return (
    <DossierCard
      title={t("ob.live.cardPeople")}
      count={t("ob.live.countPeople", { count: people.length })}
      done={people.length > 0}
    >
      {people.length === 0 ? (
        <p className="ob-live-empty">{t("ob.live.peopleEmpty")}</p>
      ) : (
        <ul className="ob-live-rows">
          {people.map((person) => {
            const evidence = evidenceOf(
              person.evidence_snippet,
              person.evidence_url,
            );
            return (
              <li
                className="ob-live-person"
                key={`${person.name}:${person.evidence_url}`}
              >
                <span className="ob-live-person-name">{person.name}</span>
                <span className="ob-live-person-role">{person.role}</span>
                {person.published_email && (
                  <span className="ob-live-person-meta">
                    {person.published_email}
                  </span>
                )}
                {person.linkedin_url && (
                  <span className="ob-live-person-meta">
                    {person.linkedin_url}
                  </span>
                )}
                {evidence && <EvidenceChip evidence={evidence} />}
              </li>
            );
          })}
        </ul>
      )}
    </DossierCard>
  );
}

/**
 * What kind of gap a coverage row is. A page the crawler chose not to fetch is
 * routine housekeeping, a page it could not fetch is a hole in the read, and a
 * warning is a caveat about the read as a whole — three different things a
 * reader must be able to tell apart at a glance.
 */
type CoverageKind = "warn" | "skip" | "fail";

type CoverageRow = Readonly<{
  id: string;
  kind: CoverageKind;
  label: string;
  /**
   * Which page this was, when the read named one worth naming. Absent for a
   * warning (no page) and for a kind that says nothing the label does not.
   */
  name?: string;
  url?: string;
  reason: string;
}>;

// Every gap the read admits to, derived from the read itself: the warnings it
// raised, the pages robots.txt or a fetch error kept it out of. There is no
// hand-kept list here, so a new skip reason surfaces the moment the wire
// carries it.
function coverageRows(
  pages: readonly SitePage[],
  warnings: readonly string[],
  t: ReturnType<typeof useT>,
): CoverageRow[] {
  const rows: CoverageRow[] = [];
  let seq = 0;
  for (const warning of warnings) {
    seq += 1;
    rows.push({
      id: `warning:${seq}`,
      kind: "warn",
      label: t("ob.live.coverageWarning"),
      reason: warning,
    });
  }
  const groups: ReadonlyArray<{
    status: SitePage["status"];
    kind: CoverageKind;
    key: MessageKey;
  }> = [
    { status: "skipped", kind: "skip", key: "ob.live.coverageSkipped" },
    { status: "failed", kind: "fail", key: "ob.live.coverageFailed" },
  ];
  for (const { status, kind, key } of groups) {
    for (const page of pages.filter((page) => page.status === status)) {
      seq += 1;
      // The page's own kind is the reader's answer to "what did you miss?"; the
      // URL alone makes them decode a path. When the wire names no kind, or
      // names "other", the status label is already everything there is to say.
      const named = namedSiteReadKind(page.kind);
      rows.push({
        id: `${status}:${seq}`,
        kind,
        label: t(key),
        name: named === undefined ? undefined : t(named),
        url: page.url,
        reason: page.reason ?? t("ob.scan.pageNoReason"),
      });
    }
  }
  return rows;
}

/**
 * What the read covered and what it could not. Its count is the read/skipped
 * split, derived by filtering the pages the wire returned — there is no
 * page-count denominator to show a ratio against.
 */
export function CoverageCard({
  pages,
  warnings,
}: Readonly<{ pages: readonly SitePage[]; warnings: readonly string[] }>) {
  const t = useT();
  const rows = coverageRows(pages, warnings, t);
  const read = pages.filter((page) => page.status === "fetched").length;
  const skipped = pages.filter((page) => page.status === "skipped").length;
  return (
    <DossierCard
      title={t("ob.live.cardCoverage")}
      count={t("ob.live.countPages", { read, skipped })}
      done={rows.length === 0}
    >
      {rows.length === 0 ? (
        <p className="ob-live-empty">{t("ob.live.coverageClean")}</p>
      ) : (
        <ul className="ob-live-rows">
          {rows.map((row) => (
            // data-kind carries the row's role to the stylesheet. The label
            // beside it says the same thing in words, so the colour it selects
            // is never the only signal.
            <li className="ob-live-coverage" data-kind={row.kind} key={row.id}>
              <span className="ob-live-coverage-label">{row.label}</span>
              {row.name !== undefined && (
                <span className="ob-live-coverage-name">{row.name}</span>
              )}
              {row.url !== undefined && (
                <span className="ob-live-coverage-url">{row.url}</span>
              )}
              <span className="ob-live-coverage-reason">{row.reason}</span>
            </li>
          ))}
        </ul>
      )}
    </DossierCard>
  );
}

/**
 * The one card a human must answer: a site's legal notice can name several
 * entities and the read refuses to guess which one this installation is. It
 * cannot be folded away, and the confirm button is disabled until a choice
 * exists — in the attribute and in the handler.
 *
 * Once answered it collapses into a done card stating the chosen entity.
 */
export function EntityDecisionCard({
  entities,
  chosen,
  onConfirm,
  onDecline,
}: Readonly<{
  entities: readonly LegalEntity[];
  chosen: string | null;
  onConfirm: (value: string) => void;
  onDecline: () => void;
}>) {
  const t = useT();
  const group = useId();
  const [picked, setPicked] = useState("");
  if (entities.length === 0) {
    return null;
  }
  if (chosen !== null) {
    return (
      <DossierCard title={t("ob.legalTitle")} count={t("ob.legalEntity")} done>
        <p className="ob-live-field-value">{chosen}</p>
      </DossierCard>
    );
  }
  return (
    <DossierCard title={t("ob.legalTitle")} needsDecision>
      <p className="ob-live-decide-sub">{t("ob.legalSub")}</p>
      <fieldset className="ob-live-entities">
        <legend className="sr-only">{t("ob.conv.clarify.entity")}</legend>
        {entities.map((entity) => {
          const evidence = evidenceOf(
            entity.evidence_snippet,
            entity.source_url,
          );
          return (
            <label
              className="ob-live-entity"
              key={`${entity.name}:${entity.source_url}`}
            >
              <input
                type="radio"
                name={group}
                value={entity.name}
                checked={picked === entity.name}
                onChange={() => setPicked(entity.name)}
              />
              <span className="ob-live-entity-body">
                <span className="ob-live-entity-name">{entity.name}</span>
                {entity.registered_address && (
                  <span className="ob-live-entity-meta">
                    {entity.registered_address}
                  </span>
                )}
                {entity.register_number && (
                  <span className="ob-live-entity-meta">
                    {entity.register_number}
                  </span>
                )}
                {/* Collapsed: five candidates each dragging their whole
                    imprint quote onto the card is a wall, not a choice. The
                    verbatim snippet stays one toggle away. */}
                {evidence && <EvidenceChip evidence={evidence} collapsed />}
              </span>
            </label>
          );
        })}
      </fieldset>
      <div className="ob-live-decide-acts">
        <Button
          variant="primary"
          small
          disabled={picked === ""}
          onClick={() => {
            // The attribute keeps the pointer out; this keeps a programmatic
            // click from confirming an entity nobody chose.
            if (picked !== "") {
              onConfirm(picked);
            }
          }}
        >
          {t("ob.confirm")}
        </Button>
        <Button small onClick={onDecline}>
          {t("ob.conv.clarify.dismiss")}
        </Button>
      </div>
    </DossierCard>
  );
}

// ---------------------------------------------------------------------------
// The panel
// ---------------------------------------------------------------------------

type WebsiteStepProps = Readonly<{
  read: CompanySiteRead;
  entityChoice: string | null;
  onConfirmEntity: (value: string) => void;
  onDeclineEntity: () => void;
  factsSlot?: ReactNode;
  highlightFields?: readonly string[];
}>;

function WebsiteStep({
  read,
  entityChoice,
  onConfirmEntity,
  onDeclineEntity,
  factsSlot,
  highlightFields,
}: WebsiteStepProps) {
  const t = useT();
  const { identity, positioning } = partitionFields(read.profile_fields);
  // Counted off the arrays that render, so a card can never advertise a
  // number its body does not contain.
  const countFields = (fields: readonly PanelField[]) =>
    t("ob.live.countFields", { count: fields.length });
  // Does the narration's finding live in THIS card? Asked per card rather than
  // resolved centrally, because only the card knows which rows it renders.
  const holdsHighlight = (fields: readonly PanelField[]) =>
    highlightFields !== undefined &&
    fields.some((field) => highlightFields.includes(field.field));
  return (
    <StepBlock n={1} title={t("ob.live.stepWebsite")} state="done">
      <div className="ob-live-cards">
        <EntityDecisionCard
          entities={read.legal_entities ?? []}
          chosen={entityChoice}
          onConfirm={onConfirmEntity}
          onDecline={onDeclineEntity}
        />
        <DossierCard
          title={t("ob.live.cardIdentity")}
          count={countFields(identity)}
          done={identity.length > 0}
          revealed={holdsHighlight(identity)}
        >
          {identity.map((field) => (
            <FieldRow field={field} key={field.field} />
          ))}
        </DossierCard>
        <DossierCard
          title={t("ob.live.cardPositioning")}
          count={countFields(positioning)}
          done={positioning.length > 0}
          revealed={holdsHighlight(positioning)}
        >
          {positioning.map((field) => (
            <FieldRow field={field} key={field.field} />
          ))}
        </DossierCard>
        {factsSlot}
        <PeopleCard people={read.people} />
        <CoverageCard pages={read.pages} warnings={read.warnings} />
      </div>
    </StepBlock>
  );
}

// A step whose subject exists in the product but has nothing to show yet:
// named, so its absence is legible, and never dressed as a card with a body.
function PendingStep({
  n,
  step,
  card,
  pending,
  state,
}: Readonly<{
  n: number;
  step: MessageKey;
  card: MessageKey;
  pending: MessageKey;
  state: PendingStepState;
}>) {
  const t = useT();
  return (
    <StepBlock n={n} title={t(step)} state={state}>
      <p className="ob-live-pending">
        <span className="ob-live-pending-title">{t(card)}</span>
        <span className="ob-live-pending-state">{t(pending)}</span>
      </p>
    </StepBlock>
  );
}

export type OnboardingLivePanelProps = Readonly<{
  /** The host being read, for the header line. */
  host: string;
  /** The read has finished. Until then the panel shows no cards at all. */
  done: boolean;
  read: CompanySiteRead | null;
  /** The legal entity already chosen, or null while the decision is open. */
  entityChoice: string | null;
  onConfirmEntity: (value: string) => void;
  onDeclineEntity: () => void;
  voiceState: PendingStepState;
  connectState: PendingStepState;
  /** The facts card, owned by the facts slice, in its place in step one. */
  factsSlot?: ReactNode;
  /**
   * The profile fields the conversation is pointing at right now. A card that
   * holds one of them opens, so the row exists for the pulse to land on.
   */
  highlightFields?: readonly string[];
}>;

export function OnboardingLivePanel({
  host,
  done,
  read,
  entityChoice,
  onConfirmEntity,
  onDeclineEntity,
  voiceState,
  connectState,
  factsSlot,
  highlightFields,
}: OnboardingLivePanelProps) {
  // A finished read with no dossier behind it has nothing to disclose, so it
  // keeps the reading notice rather than opening empty cards.
  const complete = done && read !== null;
  const fields = read?.profile_fields ?? [];
  const pageCount =
    read?.pages.filter((page) => page.status === "fetched").length ?? 0;
  return (
    <div className="ob-live">
      <LivePanelHead
        host={host}
        done={complete}
        identity={{
          name: fieldValue(fields, "display_name"),
          offer: fieldValue(fields, "offer_summary"),
          icp: fieldValue(fields, "icp"),
        }}
        factCount={read?.facts.length ?? 0}
        pageCount={pageCount}
      />
      {complete && read !== null ? (
        <>
          <WebsiteStep
            read={read}
            entityChoice={entityChoice}
            onConfirmEntity={onConfirmEntity}
            onDeclineEntity={onDeclineEntity}
            factsSlot={factsSlot}
            highlightFields={highlightFields}
          />
          <PendingStep
            n={2}
            step="ob.live.stepVoice"
            card="ob.live.cardVoice"
            pending="ob.live.voiceNotBuilt"
            state={voiceState}
          />
          <PendingStep
            n={3}
            step="ob.live.stepConnect"
            card="ob.live.cardConnect"
            pending="ob.live.connectNone"
            state={connectState}
          />
        </>
      ) : (
        <ReadingNotice />
      )}
    </div>
  );
}
