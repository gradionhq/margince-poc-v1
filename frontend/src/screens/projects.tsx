// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useQuery } from "@tanstack/react-query";
import { Hash } from "lucide-react";
import { api } from "../api/client";
import { Badge, EmptyState } from "../design-system/atoms";
import { Chip } from "../design-system/readings";
import { useLocale, useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { throwProblem, useMe, useSorMode } from "./common";
import { CreateAction } from "./create";
import { EntityRef } from "./entityref";
import {
  type ListPage,
  type ListQuery,
  ListTable,
  listFetchLimit,
  useListQuery,
} from "./listquery";
import {
  mapProjectCreate,
  PHASE_LABEL,
  PROJECT_PHASES,
  type Project,
  type ProjectCompanyOption,
  type ProjectPhase,
  projectFields,
} from "./projects.form";
import { lastActivityColumn, ownerColumn } from "./recordlist";
import { SaveViewAction, useSavedViews, useSavedViewTabs } from "./savedviews";
import "./projects.css";

// The projects list: every body of work the reader may see, newest activity
// first, narrowed by phase. A project starts during the deal and outlives
// close-won, which is why this list is not a column of the pipeline board —
// a project in delivery has no stage to stand in.

async function fetchProjectsPage(
  query: ListQuery,
  cursor: string | null,
): Promise<ListPage<Project>> {
  const { data, error } = await api.GET("/projects", {
    params: {
      query: {
        q: query.q || undefined,
        sort: query.sort || undefined,
        include_archived: query.includeArchived || undefined,
        cursor: cursor || undefined,
        limit: listFetchLimit(query.perPage),
        // Passed through whole rather than narrowed to a typed subset: the
        // list endpoint answers a field outside its allow-list with a 422 the
        // table renders, and a saved view the server refuses is better shown
        // refused than silently shown as an active tab over an unfiltered list.
        ...query.filters,
      },
    },
  });
  if (error) {
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

/** The phase as a status pill: closed is the one terminal reading. */
export function PhaseBadge({ phase }: Readonly<{ phase: ProjectPhase }>) {
  const t = useT();
  return (
    <Badge tone={phase === "closed" ? undefined : "success"} quiet>
      {t(PHASE_LABEL[phase])}
    </Badge>
  );
}

/**
 * The key as a fact chip. A key is the handle a human writes in a subject
 * line, so it draws in the mono face beside the name rather than as a status.
 */
export function ProjectKeyChip({
  projectKey,
}: Readonly<{ projectKey: string }>) {
  return (
    <Chip icon={Hash}>
      <span className="t-mono">{projectKey}</span>
    </Chip>
  );
}

/**
 * The companies a project may be created on. The first page of the company
 * list, which is the same read the deal form makes for the same picker.
 */
export function useCompanyOptions(): ProjectCompanyOption[] {
  const companies = useQuery({
    queryKey: ["organizations"],
    queryFn: async () => {
      const { data, error } = await api.GET("/organizations", {
        params: { query: { limit: 50 } },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });
  return companies.data?.data ?? [];
}

async function createProject(values: Record<string, string>): Promise<Project> {
  const { data, error } = await api.POST("/projects", {
    body: mapProjectCreate(values),
  });
  if (error) {
    throwProblem(error);
  }
  return data;
}

/**
 * The create affordance, shared by the list's header and its first-run plate.
 * One element rather than two renderings, so the dialog behind both is the
 * same dialog with the same fields.
 */
function NewProjectAction({
  companies,
  me,
}: Readonly<{ companies: ProjectCompanyOption[]; me: string }>) {
  const t = useT();
  return (
    <CreateAction
      label={t("project.new")}
      invalidate="projects"
      screen="projects"
      create={createProject}
      resolveExisting={(_code, id) => ({ screen: "projects", id })}
      fields={projectFields(t, {
        companies,
        me,
        currentOwner: null,
        mode: "create",
      })}
    />
  );
}

const PHASE_CHIP_OPTIONS: { value: ProjectPhase; label: MessageKey }[] =
  PROJECT_PHASES.map((phase) => ({ value: phase, label: PHASE_LABEL[phase] }));

export function ProjectsScreen() {
  const t = useT();
  const { locale } = useLocale();
  const me = useMe();
  const overlay = useSorMode() === "overlay";
  const companies = useCompanyOptions();
  const views = useSavedViews("projects");
  const savedViews = useSavedViewTabs("projects");
  const state = useListQuery<Project>({
    key: "projects",
    initialSort: "-last_activity_at",
    fetchPage: fetchProjectsPage,
  });
  // Held back until the first page has answered: the first-run plate below
  // mounts its own copy of this verb, and a button pressed in the table's
  // header a moment before the plate replaces it would open a dialog the
  // swap throws away.
  const createAction = !overlay && !state.isPending && (
    <NewProjectAction companies={companies} me={me.data?.user.id ?? ""} />
  );
  // The first-run plate: nothing exists yet, and nothing is narrowing the
  // list. A filtered-empty list is the table's own case — it already knows to
  // offer the way back — and a list that is empty because a reader typed a
  // search must not be told the product has no projects. A saved view is
  // reachable only from the table's rail, so the plate waits for the views
  // read to confirm there is none; an unanswered or failed read keeps the
  // table, where SaveViewAction says what went wrong.
  const firstRun =
    !state.isPending &&
    !state.isError &&
    views.isSuccess &&
    savedViews.length === 0 &&
    state.rows.length === 0 &&
    state.query.q === "" &&
    !state.query.includeArchived &&
    Object.keys(state.query.filters).length === 0;

  if (firstRun) {
    return (
      <div className="wrap">
        <EmptyState title={t("project.emptyTitle")} action={createAction}>
          <p>{t("project.emptyBody")}</p>
          <p>{t("project.emptyKey")}</p>
        </EmptyState>
      </div>
    );
  }

  return (
    <div className="wrap">
      <ListTable
        state={state}
        unit="unit.projects"
        action={createAction}
        columns={[
          {
            key: "name",
            header: t("project.name"),
            cell: (project: Project) => (
              <span className="project-name-cell">
                <strong>{project.name}</strong>
                {project.key && <ProjectKeyChip projectKey={project.key} />}
                {project.archived_at && (
                  <Badge tone="warn">{t("record.archived")}</Badge>
                )}
              </span>
            ),
            sort: "name",
            fixed: true,
          },
          {
            key: "company",
            header: t("project.company"),
            cell: (project: Project) => (
              <EntityRef
                kind="organization"
                id={project.organization_id}
                asText
              />
            ),
          },
          {
            key: "phase",
            header: t("project.phaseLabel"),
            // Not sortable: phase is not in the list's sort vocabulary, and
            // the chip beside the table is the way to read one phase at a time.
            cell: (project: Project) => <PhaseBadge phase={project.phase} />,
          },
          ownerColumn<Project>(t),
          lastActivityColumn<Project>(t, locale),
        ]}
        tools={<SaveViewAction resource="projects" query={state.query} />}
        rowKey={(project) => project.id}
        rowRoute={(project) => ({ screen: "projects", id: project.id })}
        dataViews={savedViews}
        chips={[
          {
            key: "phase",
            label: "project.phaseLabel",
            allLabel: "project.filterPhaseAll",
            options: PHASE_CHIP_OPTIONS,
          },
        ]}
        views={[
          { label: "list.viewAll", sort: "-last_activity_at" },
          {
            label: "project.viewDelivering",
            sort: "-last_activity_at",
            filters: { phase: "delivering" },
          },
          { label: "list.viewAZ", sort: "name" },
        ]}
      />
    </div>
  );
}
