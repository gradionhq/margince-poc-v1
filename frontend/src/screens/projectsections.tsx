// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { ReactNode } from "react";
import type { components } from "../api/schema";
import { navigate } from "../app/router";
import { Badge } from "../design-system/atoms";
import { Panel, PanelBody, PanelRow } from "../design-system/panel";
import {
  type SectionState,
  SurfaceState,
  sectionState,
} from "../design-system/surfacestate";
import {
  formatBytes,
  formatDateAbbrev,
  formatDateTime,
  formatDuration,
  formatMoneyOrAbsent,
} from "../format/format";
import { RECORD_ZONE, viewerZone } from "../format/timezone";
import { useLocale, useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { dealRoleLabel } from "./company360";
import { EntityRef } from "./entityref";
import { isProjectPhase, PHASE_LABEL } from "./projects.form";

// The project page's sections, each drawn from ONE composite read
// (GET /projects/{id}/360). Every card below answers the same three questions
// in the same order — withheld, unavailable, empty — through `sectionState`,
// so a section a grant refused says so rather than drawing an empty list that
// reads as "there is none".

export type Project360 = components["schemas"]["Project360"];
type Project360Section = components["schemas"]["Project360Section"];
type Deal = components["schemas"]["Deal"];
type Contract = components["schemas"]["Contract"];
type Attachment = components["schemas"]["Attachment"];
type Stakeholder = components["schemas"]["Project360Stakeholder"];
type Commitment = components["schemas"]["Project360Commitment"];
type PhaseTransition = components["schemas"]["Project360PhaseTransition"];

// Every section of the page the reader may lack a grant for. `documents`
// rides the project grant itself and is never in `sections_omitted`, so it is
// not in this union either.
type SectionKey = Project360Section | "documents";

/**
 * SectionPanel is the shape every card on this page takes: a titled Panel
 * whose rows run edge to edge when the section is `ready`, and whose
 * message states — withheld, unavailable, empty, loading — are SurfaceState's
 * own sentences in a padded body. The empty sentence is the caller's, because
 * what belongs in a section is the one thing only the section knows.
 */
function SectionPanel({
  title,
  state,
  emptyLabel,
  titleAction,
  children,
}: Readonly<{
  title: string;
  state: SectionState;
  emptyLabel: string;
  titleAction?: ReactNode;
  children: ReactNode;
}>) {
  return (
    <Panel title={title} titleAction={titleAction}>
      {state === "ready" ? (
        children
      ) : (
        <PanelBody>
          <SurfaceState state={state} emptyLabel={emptyLabel}>
            {null}
          </SurfaceState>
        </PanelBody>
      )}
    </Panel>
  );
}

export function stateOf(
  view: Project360 | undefined,
  section: SectionKey,
  present: boolean,
  count: number,
): SectionState {
  if (section === "documents") {
    // Documents ride the project grant: the page carries them whenever it
    // carries the project, so absent means the read failed, never withheld.
    if (!view) {
      return "unavailable";
    }
    return present ? (count === 0 ? "empty" : "ready") : "unavailable";
  }
  return sectionState(view, section, present, count);
}

// The delivery roles a project adds to the deal-stakeholder vocabulary. The
// five deal roles fall through to the company page's own labels, so one word
// names a champion on a deal and on the project it became.
const PROJECT_ROLE_LABELS: Record<string, MessageKey> = {
  sponsor: "project.role.sponsor",
  project_lead: "project.role.project_lead",
  delivery_lead: "project.role.delivery_lead",
  subject_matter_expert: "project.role.subject_matter_expert",
};

export function projectRoleLabel(
  role: string,
  t: (key: MessageKey) => string,
): string {
  const key = Object.hasOwn(PROJECT_ROLE_LABELS, role)
    ? PROJECT_ROLE_LABELS[role]
    : undefined;
  return key ? t(key) : dealRoleLabel(role, t);
}

function phaseWord(phase: string, t: (key: MessageKey) => string): string {
  return isProjectPhase(phase) ? t(PHASE_LABEL[phase]) : phase;
}

/**
 * Every phase transition, oldest first, with how long the project has spent
 * in each phase so far. A reopened project visits a phase twice and the
 * duration sums the visits, which is what the server's fold already says.
 */
export function PhaseHistoryCard({
  view,
}: Readonly<{ view: Project360 | undefined }>) {
  const t = useT();
  const { locale } = useLocale();
  const history = view?.phase_history;
  const rows = history?.data ?? [];
  const state = stateOf(view, "phase_history", Boolean(history), rows.length);
  return (
    <SectionPanel
      title={t("project.history.title")}
      state={state}
      emptyLabel={t("project.history.empty")}
    >
      {history && history.phase_durations.length > 0 && (
        <PanelBody>
          <ul className="project-durations">
            {history.phase_durations.map((duration) => (
              <li key={duration.phase}>
                <span>{phaseWord(duration.phase, t)}</span>
                <span className="t-mono">
                  {formatDuration(duration.seconds * 1000, locale)}
                  {duration.current && ` · ${t("project.history.current")}`}
                </span>
              </li>
            ))}
          </ul>
        </PanelBody>
      )}
      {rows.map((transition) => (
        <TransitionRow key={transition.id} transition={transition} />
      ))}
    </SectionPanel>
  );
}

function TransitionRow({
  transition,
}: Readonly<{ transition: PhaseTransition }>) {
  const t = useT();
  const { locale } = useLocale();
  return (
    <PanelRow className="project-row">
      <span>
        {transition.from_phase
          ? t("project.history.moved", {
              from: phaseWord(transition.from_phase, t),
              to: phaseWord(transition.to_phase, t),
            })
          : t("project.history.born", {
              phase: phaseWord(transition.to_phase, t),
            })}
      </span>
      <span className="project-row-meta">
        <span>
          {formatDateTime(transition.changed_at, locale, RECORD_ZONE)}
        </span>
        <span>
          {transition.changed_by.display_name ?? t("project.history.bySystem")}
        </span>
        {transition.reason && <span>“{transition.reason}”</span>}
      </span>
    </PanelRow>
  );
}

/** The deals rolled up to the project, newest first, every status. */
export function ProjectDealsCard({
  view,
  actions,
}: Readonly<{ view: Project360 | undefined; actions?: ReactNode }>) {
  const t = useT();
  const { locale } = useLocale();
  const deals = view?.deals?.data ?? [];
  const state = stateOf(view, "deals", Boolean(view?.deals), deals.length);
  return (
    <SectionPanel
      title={t("project.deals.title")}
      state={state}
      emptyLabel={t("project.deals.empty")}
      titleAction={state === "ready" || state === "empty" ? actions : undefined}
    >
      {deals.map((deal) => (
        <ProjectDealRow key={deal.id} deal={deal} locale={locale} />
      ))}
      {view?.deals?.page.has_more && (
        <PanelBody>
          <p className="t-caption">{t("project.deals.more")}</p>
        </PanelBody>
      )}
    </SectionPanel>
  );
}

function ProjectDealRow({
  deal,
  locale,
}: Readonly<{ deal: Deal; locale: ReturnType<typeof useLocale>["locale"] }>) {
  return (
    <PanelRow className="project-row">
      <button
        type="button"
        className="project-rowlink"
        onClick={() => navigate({ screen: "deals", id: deal.id })}
      >
        {deal.name}
      </button>
      <span className="project-row-meta">
        <Badge tone={deal.status === "won" ? "success" : undefined} quiet>
          {deal.status}
        </Badge>
        <span className="t-mono">
          {formatMoneyOrAbsent(deal.amount_minor, deal.currency, locale)}
        </span>
      </span>
    </PanelRow>
  );
}

/** The people seated on the project, each with the seat they hold. */
export function StakeholdersCard({
  view,
}: Readonly<{ view: Project360 | undefined }>) {
  const t = useT();
  const seats = view?.stakeholders?.data ?? [];
  const state = stateOf(
    view,
    "stakeholders",
    Boolean(view?.stakeholders),
    seats.length,
  );
  return (
    <SectionPanel
      title={t("project.stakeholders.title")}
      state={state}
      emptyLabel={t("project.stakeholders.empty")}
    >
      {seats.map((seat) => (
        <StakeholderRow key={seat.relationship_id} seat={seat} />
      ))}
    </SectionPanel>
  );
}

function StakeholderRow({ seat }: Readonly<{ seat: Stakeholder }>) {
  const t = useT();
  return (
    <PanelRow className="project-row">
      <EntityRef kind="person" id={seat.person_id} name={seat.person_name} />
      {seat.role && (
        <span className="project-row-meta">
          <Badge quiet>{projectRoleLabel(seat.role, t)}</Badge>
        </span>
      )}
    </PanelRow>
  );
}

/** The agreements filed under the project. */
export function ProjectContractsCard({
  view,
}: Readonly<{ view: Project360 | undefined }>) {
  const t = useT();
  const { locale } = useLocale();
  const contracts = view?.contracts?.data ?? [];
  const state = stateOf(
    view,
    "contracts",
    Boolean(view?.contracts),
    contracts.length,
  );
  return (
    <SectionPanel
      title={t("project.contracts.title")}
      state={state}
      emptyLabel={t("project.contracts.empty")}
    >
      {contracts.map((contract) => (
        <ContractRow key={contract.id} contract={contract} locale={locale} />
      ))}
    </SectionPanel>
  );
}

function ContractRow({
  contract,
  locale,
}: Readonly<{
  contract: Contract;
  locale: ReturnType<typeof useLocale>["locale"];
}>) {
  return (
    <PanelRow className="project-row">
      <span>{contract.title}</span>
      <span className="project-row-meta">
        <Badge quiet>{contract.status}</Badge>
        <span className="t-mono">
          {formatMoneyOrAbsent(contract.value_minor, contract.currency, locale)}
        </span>
        {contract.ends_on && (
          <span>{formatDateAbbrev(contract.ends_on, locale, RECORD_ZONE)}</span>
        )}
      </span>
    </PanelRow>
  );
}

/** The files attached to the project itself, newest first. */
export function ProjectDocumentsCard({
  view,
}: Readonly<{ view: Project360 | undefined }>) {
  const t = useT();
  const { locale } = useLocale();
  const documents = view?.documents?.data ?? [];
  const state = stateOf(
    view,
    "documents",
    Boolean(view?.documents),
    documents.length,
  );
  return (
    <SectionPanel
      title={t("project.documents.title")}
      state={state}
      emptyLabel={t("project.documents.empty")}
    >
      {documents.map((doc) => (
        <DocumentRow key={doc.id} doc={doc} locale={locale} />
      ))}
    </SectionPanel>
  );
}

function DocumentRow({
  doc,
  locale,
}: Readonly<{
  doc: Attachment;
  locale: ReturnType<typeof useLocale>["locale"];
}>) {
  return (
    <PanelRow className="project-row">
      {/* The name is the download, as on the company page: one thing to find
          for the one thing the row does. */}
      <a
        className="project-rowlink"
        href={`/v1/attachments/${doc.id}`}
        download={doc.filename}
      >
        {doc.title || doc.filename}
      </a>
      <span className="project-row-meta">
        {doc.byte_size != null && (
          <span className="t-mono">{formatBytes(doc.byte_size, locale)}</span>
        )}
        <span>{formatDateAbbrev(doc.created_at, locale, RECORD_ZONE)}</span>
      </span>
    </PanelRow>
  );
}

/** The open tasks filed under the project, soonest due first. */
export function CommitmentsCard({
  view,
}: Readonly<{ view: Project360 | undefined }>) {
  const t = useT();
  const { locale } = useLocale();
  const commitments = view?.commitments?.data ?? [];
  const state = stateOf(
    view,
    "commitments",
    Boolean(view?.commitments),
    commitments.length,
  );
  return (
    <SectionPanel
      title={t("project.commitments.title")}
      state={state}
      emptyLabel={t("project.commitments.empty")}
    >
      {commitments.map((commitment) => (
        <CommitmentRow
          key={commitment.activity_id}
          commitment={commitment}
          locale={locale}
        />
      ))}
    </SectionPanel>
  );
}

function CommitmentRow({
  commitment,
  locale,
}: Readonly<{
  commitment: Commitment;
  locale: ReturnType<typeof useLocale>["locale"];
}>) {
  const t = useT();
  return (
    <PanelRow className="project-row">
      <span>{commitment.subject}</span>
      <span className="project-row-meta">
        {/* A due date is a personal deadline, read in the reader's own zone
            exactly as the tasks screen reads it. */}
        {commitment.due_at && (
          <span>
            {formatDateAbbrev(commitment.due_at, locale, viewerZone())}
          </span>
        )}
        {commitment.overdue && (
          <Badge tone="danger">{t("project.commitments.overdue")}</Badge>
        )}
        {commitment.assignee_name && <span>{commitment.assignee_name}</span>}
      </span>
    </PanelRow>
  );
}
