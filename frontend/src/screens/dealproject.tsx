// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Briefcase } from "lucide-react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { ifMatch, requireVersion } from "../api/version";
import { navigate, routeHash } from "../app/router";
import { Button } from "../design-system/atoms";
import { Callout } from "../design-system/callout";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { problemMessageOf, throwProblem } from "./common";
import type { CreateField } from "./create";
import { useEntityName } from "./entityref";
import { type Project, projectKeyRefusal } from "./projects.form";

// Where a deal meets its project: the picker on the deal form (with the
// inline "new project" it can grow), the chip on the deal page, and the one
// prompt a won deal without a project gets.

type Deal = components["schemas"]["Deal"];

/**
 * The option value that means "make a new project for this deal". Not a
 * uuid, so it can never collide with a real project id, and not empty, which
 * is the "no project" answer.
 */
export const NEW_PROJECT = "__new_project__";

/**
 * The open projects a deal may be filed under, with the company each belongs
 * to, because the deal form chooses its company in the same dialog and the
 * server refuses a project on another company (422). A closed project is not
 * offered: a deal born into a closed project would reopen nothing.
 */
export function useOpenProjects(): Project[] {
  const projects = useQuery({
    queryKey: ["projects", "open"],
    queryFn: async () => {
      const { data, error } = await api.GET("/projects", {
        params: { query: { limit: 200 } },
      });
      if (error) {
        throwProblem(error);
      }
      return data.data;
    },
    staleTime: 60_000,
  });
  return (projects.data ?? []).filter((project) => project.phase !== "closed");
}

/**
 * The project fields of the deal form: the picker, and the two fields that
 * appear when the reader chooses to start a project here. The picker offers
 * only the projects of the company the SAME form has chosen — the server
 * refuses a project on another company (422) — and stays disabled until a
 * company is named; a project picked under one company is cleared when the
 * company changes.
 */
export function dealProjectFields(
  t: (key: MessageKey) => string,
  projects: readonly Project[],
  // The project this deal already names, kept on the list even when the
  // open-projects page does not reach it (a closed one, say), so the edit
  // form shows the value it has rather than a blank picker whose save would
  // clear it.
  current?: { id: string; label: string },
): CreateField[] {
  return [
    {
      key: "project_id",
      label: "deal.project",
      type: "select",
      optionsFor: (values) => {
        const company = values.organization_id ?? "";
        if (!company) {
          return [];
        }
        const options = projects
          .filter((project) => project.organization_id === company)
          .map((project) => ({ value: project.id, label: project.name }));
        return [
          ...(current && !options.some((option) => option.value === current.id)
            ? [{ value: current.id, label: current.label }]
            : []),
          ...options,
          { value: NEW_PROJECT, label: t("deal.projectNew") },
        ];
      },
    },
    {
      key: "new_project_name",
      label: "project.name",
      required: true,
      showWhen: (values) => values.project_id === NEW_PROJECT,
    },
    {
      key: "new_project_key",
      label: "project.key",
      hint: t("project.keyHint"),
      validate: (value) => {
        const refusal = projectKeyRefusal(value);
        return refusal ? t(refusal) : undefined;
      },
      showWhen: (values) => values.project_id === NEW_PROJECT,
    },
  ];
}

/**
 * The project id the deal body should carry, creating the project first when
 * the form asked for a new one. The new project is born on the deal's
 * company — the one company the two are required to share.
 */
export async function resolveDealProject(
  values: Record<string, string>,
  organizationId: string | null,
  t: (key: MessageKey) => string,
): Promise<string | null> {
  const picked = values.project_id?.trim() ?? "";
  if (picked !== NEW_PROJECT) {
    return picked || null;
  }
  if (!organizationId) {
    throw new Error(t("deal.projectNeedsCompany"));
  }
  const { data, error } = await api.POST("/projects", {
    body: {
      name: values.new_project_name?.trim() ?? "",
      key: values.new_project_key?.trim() || null,
      organization_id: organizationId,
      source: "manual",
    },
  });
  if (error) {
    throwProblem(error);
  }
  return data.id;
}

/** The deal's project as a linked chip, or nothing when it has none. */
export function DealProjectChip({ deal }: Readonly<{ deal: Deal }>) {
  const t = useT();
  const { name } = useEntityName("project", deal.project_id);
  if (!deal.project_id) {
    return null;
  }
  const href = routeHash({ screen: "projects", id: deal.project_id });
  return (
    <a className="chip chip-link" href={href} data-testid="deal-project">
      <Briefcase size={14} aria-hidden="true" />
      <span>{name ?? t("deal.projectUnnamed")}</span>
    </a>
  );
}

/**
 * StartDeliveryPrompt is the one offer a won deal with no project gets, and
 * only when its company has exactly ONE open project: attach the deal to it
 * and move the project into delivery. Two open projects is a choice the
 * reader makes on the edit form; none is a project to create first.
 */
export function StartDeliveryPrompt({ deal }: Readonly<{ deal: Deal }>) {
  const t = useT();
  const queryClient = useQueryClient();
  const candidates = useOpenProjects().filter(
    (project) =>
      deal.organization_id != null &&
      project.organization_id === deal.organization_id,
  );
  const attach = useMutation({
    mutationFn: async (input: {
      dealId: string;
      version: number | undefined;
      project: Project;
      // True once the deal already names the project: the PATCH landed and
      // only the advance is still owed. Two writes, so a failure between
      // them is a state the reader can be left in, and the retry must not
      // attach a second time.
      attached: boolean;
    }) => {
      if (!input.attached) {
        const patched = await api.PATCH("/deals/{id}", {
          params: {
            path: { id: input.dealId },
            ...ifMatch(requireVersion(input.version)),
          },
          body: { project_id: input.project.id },
        });
        if (patched.error) {
          throwProblem(patched.error);
        }
      }
      // A project already in delivery is not moved again: the advance would
      // be a no-op the server may refuse, and the deal is attached either way.
      if (input.project.phase !== "delivering") {
        const advanced = await api.POST("/projects/{id}/advance", {
          params: {
            path: { id: input.project.id },
            ...ifMatch(requireVersion(input.project.version)),
          },
          body: { to_phase: "delivering", reason: null },
        });
        if (advanced.error) {
          throwProblem(advanced.error);
        }
      }
      return input.project;
    },
    onSuccess: (project) => {
      queryClient.invalidateQueries({ queryKey: ["deal", deal.id] });
      queryClient.invalidateQueries({ queryKey: ["deals"] });
      queryClient.invalidateQueries({ queryKey: ["project", project.id] });
      queryClient.invalidateQueries({ queryKey: ["projects"] });
      navigate({ screen: "projects", id: project.id });
    },
    // Whatever landed before the failure is real: re-read both records so
    // the offer below says what is still owed rather than what was owed.
    onError: (_error, input) => {
      queryClient.invalidateQueries({ queryKey: ["deal", input.dealId] });
      queryClient.invalidateQueries({
        queryKey: ["project", input.project.id],
      });
      queryClient.invalidateQueries({ queryKey: ["projects"] });
    },
  });
  // A withheld project arrives as null with `masked_fields` naming it, and
  // is not the same as none; an archived deal takes no writes at all.
  const masked = (deal.masked_fields ?? []).includes("project_id");
  if (
    deal.status !== "won" ||
    masked ||
    deal.archived_at ||
    candidates.length !== 1
  ) {
    return null;
  }
  const project = candidates[0];
  // The retry case: the deal already names this project but the advance
  // failed, so the project is still short of delivery.
  const attached = deal.project_id === project.id;
  if (deal.project_id && !attached) {
    return null;
  }
  if (attached && project.phase === "delivering") {
    return null;
  }
  return (
    <Callout
      tone="info"
      icon={Briefcase}
      title={t("deal.startDeliveryTitle")}
      actions={
        <Button
          small
          variant="primary"
          disabled={attach.isPending}
          data-testid="deal-start-delivery"
          onClick={() =>
            attach.mutate({
              dealId: deal.id,
              version: deal.version,
              project,
              attached,
            })
          }
        >
          {t("deal.startDelivery")}
        </Button>
      }
    >
      {t(attached ? "deal.startDeliveryAttached" : "deal.startDeliveryBody", {
        project: project.name,
      })}
      {attach.isError && (
        <p className="t-caption" role="alert">
          {problemMessageOf(attach.error, t)}
        </p>
      )}
    </Callout>
  );
}
