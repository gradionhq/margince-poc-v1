// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useQuery } from "@tanstack/react-query";
import { type ReactNode, useId, useState } from "react";
import { api } from "../api/client";
import { ifMatch, requireVersion } from "../api/version";
import { navigate } from "../app/router";
import { RecordView } from "../design-system/composed";
import {
  hasTimelineFilters,
  useRecordTimeline,
  useTimelineFilters,
} from "../design-system/recordtimeline";
import { SurfaceState, sectionState } from "../design-system/surfacestate";
import { TimelineFilterBar } from "../design-system/timelinefilterbar";
import { formatDate } from "../format/format";
import { RECORD_ZONE } from "../format/timezone";
import { useLocale, useT } from "../i18n";
import { ArchiveAction } from "./archive";
import { QueryGate, throwProblem, useMe, useSorMode } from "./common";
import { NewDealAction } from "./companyactions";
import { TimelineActions } from "./compose";
import { EditAction } from "./edit";
import { EntityRef, OwnerName } from "./entityref";
import { AdvanceProjectModal, PhaseStepper } from "./projectphase";
import { CoverageLine, RollupsStrip } from "./projectreadings";
import { PhaseBadge, ProjectKeyChip, useCompanyOptions } from "./projects";
import {
  mapProjectUpdate,
  type Project,
  type ProjectPhase,
  projectEditRecord,
  projectFields,
} from "./projects.form";
import {
  CommitmentsCard,
  PhaseHistoryCard,
  type Project360,
  ProjectContractsCard,
  ProjectDealsCard,
  ProjectDocumentsCard,
  StakeholdersCard,
} from "./projectsections";
import {
  ChronologyFilter,
  ChronologyFooter,
  chronologyNotice,
  useChronologyFilter,
  useRecordChronology,
} from "./recordchronology";
import { ShareAction } from "./share";
import { groupChronology } from "./timelinegroups";
import "./projects.css";

// The project page: one composite read (GET /projects/{id}/360) drawn in the
// company page's three zones — what the project IS on the left (its phase
// history, its people, its paper), what is HAPPENING in the middle (deals,
// open commitments), and the chronology underneath. `sections_omitted` is
// what keeps every card honest: a section the reader's role cannot read says
// so instead of drawing an empty list.

export function useProject360(id: string) {
  return useQuery({
    // Under the ["project", id] prefix the edit and archive actions
    // invalidate, so a saved name reaches the page without a second key.
    queryKey: ["project", id, "360"],
    queryFn: async () => {
      const { data, error } = await api.GET("/projects/{id}/360", {
        params: { path: { id } },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });
}

export function ProjectScreen({ id }: Readonly<{ id: string }>) {
  const view = useProject360(id);
  return (
    <div className="wrap">
      <QueryGate query={view}>
        {(data) => <ProjectPage view={data} />}
      </QueryGate>
    </div>
  );
}

function ProjectPage({ view }: Readonly<{ view: Project360 }>) {
  const t = useT();
  const project = view.project;
  const archivedReasonId = useId();
  const [moveTo, setMoveTo] = useState<ProjectPhase | null>(null);
  const overlay = useSorMode() === "overlay";
  const chronology = useProjectChronology(view, overlay);
  const refusedByArchive = project.archived_at ? archivedReasonId : undefined;
  return (
    <RecordView
      name={project.name}
      subtitle={<ProjectSubtitle view={view} />}
      zone={RECORD_ZONE}
      badges={
        <>
          <PhaseBadge phase={project.phase} />
          {project.key && <ProjectKeyChip projectKey={project.key} />}
        </>
      }
      actions={
        <ProjectActions project={project} refusedReasonId={refusedByArchive} />
      }
      band={
        <div className="project-band">
          {project.archived_at && (
            <p id={archivedReasonId} className="t-caption">
              {t("project.archivedReadOnly")}
            </p>
          )}
          <PhaseStepper
            phase={project.phase}
            refusedReasonId={refusedByArchive}
            pending={false}
            onMove={setMoveTo}
          />
          <RollupsStrip view={view} />
          <CoverageLine view={view} />
        </div>
      }
      rail={
        <div className="project-rail">
          <PhaseHistoryCard view={view} />
          <StakeholdersCard view={view} />
          <ProjectContractsCard view={view} />
          <ProjectDocumentsCard view={view} />
        </div>
      }
      railLabel={t("project.railLabel")}
      {...chronology}
    >
      <div className="project-main">
        <ProjectDealsCard
          view={view}
          actions={
            !overlay &&
            !project.archived_at &&
            project.organization_id && (
              <NewDealAction
                orgId={project.organization_id}
                orgName={project.name}
                projectId={project.id}
              />
            )
          }
        />
        <CommitmentsCard view={view} />
      </div>
      <AdvanceProjectModal
        projectId={project.id}
        version={project.version}
        to={moveTo}
        onClose={() => setMoveTo(null)}
      />
    </RecordView>
  );
}

/** The company and the owner, under the name. */
function ProjectSubtitle({ view }: Readonly<{ view: Project360 }>) {
  const t = useT();
  const { locale } = useLocale();
  const project = view.project;
  const company = view.organization;
  const companyState = sectionState(
    view,
    "organization",
    Boolean(company),
    company ? 1 : 0,
  );
  return (
    <span className="project-subtitle">
      {companyState === "withheld" ? (
        // The grant refused the company: say so rather than leave the name
        // out, which would read as a project with no company.
        <span data-testid="project-company-withheld">
          {t("state.withheld")}
        </span>
      ) : (
        <EntityRef
          kind="organization"
          id={project.organization_id}
          name={company?.name}
        />
      )}
      <span aria-hidden="true">·</span>
      <OwnerName ownerId={project.owner_id} unowned={t("list.unowned")} />
      {project.target_end_date && (
        <>
          <span aria-hidden="true">·</span>
          <span>
            {t("project.targetEndShort", {
              date: formatDate(project.target_end_date, locale, RECORD_ZONE),
            })}
          </span>
        </>
      )}
    </span>
  );
}

/** Edit, archive and share — the record's rare verbs, beside its identity. */
function ProjectActions({
  project,
  refusedReasonId,
}: Readonly<{ project: Project; refusedReasonId?: string }>) {
  const t = useT();
  const me = useMe();
  const companies = useCompanyOptions();
  const overlay = useSorMode() === "overlay";
  return (
    <>
      <EditAction
        disabledReasonId={refusedReasonId}
        label={t("project.edit")}
        fields={projectFields(t, {
          companies,
          me: me.data?.user.id ?? "",
          currentOwner: project.owner_id ?? null,
          mode: "edit",
        })}
        record={projectEditRecord(project)}
        update={async (values) => {
          const { data, error } = await api.PATCH("/projects/{id}", {
            params: {
              path: { id: project.id },
              ...ifMatch(requireVersion(project.version)),
            },
            body: mapProjectUpdate(values),
          });
          if (error) {
            throwProblem(error);
          }
          return data;
        }}
        invalidate="projects"
        recordKey="project"
      />
      <ArchiveAction
        disabledReasonId={refusedReasonId}
        label={t("project.archive")}
        confirmText={t("project.archiveConfirm")}
        archive={async () => {
          // Archive answers 204: the archived record is the one the page
          // already holds, so its id is what the shared choreography gets.
          const { error } = await api.DELETE("/projects/{id}", {
            params: {
              path: { id: project.id },
              ...ifMatch(requireVersion(project.version)),
            },
          });
          if (error) {
            throwProblem(error);
          }
          return { id: project.id };
        }}
        invalidate="projects"
        recordKey="project"
        onArchived={() => navigate({ screen: "projects" })}
      />
      {!overlay && (
        <ShareAction
          recordType="project"
          recordId={project.id}
          disabledReasonId={refusedReasonId}
        />
      )}
    </>
  );
}

type ChronologySlots = Readonly<{
  timeline?: ReturnType<typeof useRecordChronology>["entries"];
  timelineGroups?: ReturnType<typeof groupChronology>;
  timelineHeader?: ReactNode;
  timelineFooter?: ReactNode;
  timelineNotice?: ReactNode;
}>;

/**
 * The chronology zone: the activities the 360 already carries (capped, with
 * `has_more` beside them), folded with the field-history feed under the same
 * Activities / Changes / All filter the company page offers. The 360's own
 * page of activities is what is drawn, so the list cannot disagree with the
 * rollup figures read in the same transaction.
 */
function useProjectChronology(
  view: Project360,
  overlay: boolean,
): ChronologySlots {
  const t = useT();
  const [filter, setFilter] = useChronologyFilter(view.project.id);
  const activities = view.activities;
  const activitiesState = sectionState(
    view,
    "activities",
    Boolean(activities),
    activities?.data.length ?? 0,
  );
  const [filters, setFilters] = useTimelineFilters(view.project.id);
  // The 360's own page seeds the list; older pages and every narrowed read
  // come from the activity list itself.
  const timeline = useRecordTimeline("project", view.project.id, {
    filters,
    firstPage: activities,
  });
  const history = useRecordChronology({
    kind: "project",
    recordId: view.project.id,
    filter,
    activities: timeline.activities,
    activitiesHaveMore: timeline.hasNextPage,
    loadMore: timeline,
    renderActions: (activity) => (
      <TimelineActions
        activity={activity}
        entityType="project"
        entityId={view.project.id}
      />
    ),
  });
  if (overlay) {
    return { timeline: history.entries, timelineNotice: <span /> };
  }
  // A withheld activities section is not an empty timeline, and the change
  // feed is a separate grant: Activities and All say withheld, Changes still
  // reads.
  if (activitiesState === "withheld" && filter !== "changes") {
    return {
      timeline: [],
      timelineHeader: <ChronologyFilter filter={filter} onFilter={setFilter} />,
      timelineNotice: (
        <SurfaceState state="withheld" emptyLabel="">
          {null}
        </SurfaceState>
      ),
    };
  }
  return {
    timeline: history.entries,
    timelineGroups: groupChronology(history.entries, timeline.hasNextPage),
    timelineHeader: (
      <>
        <ChronologyFilter filter={filter} onFilter={setFilter} />
        {filter !== "changes" && (
          <TimelineFilterBar value={filters} onChange={setFilters} />
        )}
      </>
    ),
    timelineFooter: <ChronologyFooter filter={filter} chronology={history} />,
    timelineNotice: chronologyNotice(
      "project.timeline.empty",
      {
        // The 360 is already on screen here, so for the unfiltered read only
        // the change feed can still be loading or failed; a narrowed read is
        // the list's own and has its own wait.
        loading: history.loading || timeline.isPending,
        failed: history.failed || timeline.isError,
        assembled:
          filter === "changes" ||
          (hasTimelineFilters(filters)
            ? timeline.isSuccess
            : Boolean(activities)),
        filter,
      },
      history.entries.length,
      t,
    ),
  };
}
