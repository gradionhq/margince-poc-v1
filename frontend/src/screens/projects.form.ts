// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { components } from "../api/schema";
import type { MessageKey } from "../i18n/en";
import type { CreateField } from "./create";

// The project form's vocabulary and transport, with no React in it: what the
// create and edit dialogs ask, how a typed key is refused before the server
// sees it, and how the answers become the two request bodies.

export type Project = components["schemas"]["Project"];
export type ProjectPhase = Project["phase"];
type CreateProjectRequest = components["schemas"]["CreateProjectRequest"];
type UpdateProjectRequest = components["schemas"]["UpdateProjectRequest"];

// The four phases in the order a project walks them. Movement goes both ways
// — a closed project can be reopened into delivery — so this is the ORDER of
// the stepper, not a one-way ladder.
export const PROJECT_PHASES = [
  "initiative",
  "pursuing",
  "delivering",
  "closed",
] as const satisfies readonly ProjectPhase[];

export const PHASE_LABEL: Record<ProjectPhase, MessageKey> = {
  initiative: "project.phase.initiative",
  pursuing: "project.phase.pursuing",
  delivering: "project.phase.delivering",
  closed: "project.phase.closed",
};

export function isProjectPhase(value: string): value is ProjectPhase {
  return (PROJECT_PHASES as readonly string[]).includes(value);
}

/**
 * The contract's own shape for a key (createProject): letter-led, then up to
 * 23 of letters, digits, `_` or `-`. Letter-led so a key can never be a bare
 * number, which would match dates, amounts and order numbers in a subject
 * line — the one place a key is meant to be recognised.
 */
export const PROJECT_KEY_PATTERN = /^[A-Za-z][A-Za-z0-9_-]{1,23}$/;

/**
 * Why a typed key is refused, as a message key, or undefined for a key the
 * contract would accept. Empty is accepted: the key is optional.
 */
export function projectKeyRefusal(value: string): MessageKey | undefined {
  const key = value.trim();
  if (key === "") {
    return undefined;
  }
  return PROJECT_KEY_PATTERN.test(key) ? undefined : "project.keyInvalid";
}

export type ProjectCompanyOption = { id: string; display_name: string };

/**
 * The fields both dialogs share. The company is the one difference: a project
 * names its company at birth and the update body carries no
 * `organization_id`, so the edit form shows the company as a fact rather
 * than as a picker.
 */
export function projectFields(
  t: (key: MessageKey) => string,
  opts: Readonly<{
    companies: readonly ProjectCompanyOption[];
    // The company this project already names, so an edit form whose
    // pickable page does not reach it still shows the one it has.
    currentCompany?: { id: string; label: string };
    me: string;
    currentOwner: string | null;
    mode: "create" | "edit";
  }>,
): CreateField[] {
  const companyOptions = opts.companies.map((company) => ({
    value: company.id,
    label: company.display_name,
  }));
  const current = opts.currentCompany;
  const ownerOptions = [
    ...(opts.currentOwner && opts.currentOwner !== opts.me
      ? [{ value: opts.currentOwner, label: t("project.ownerKeep") }]
      : []),
    { value: opts.me, label: t("project.ownerMe") },
    { value: "", label: t("project.ownerUnassign") },
  ];
  return [
    { key: "name", label: "project.name", required: true },
    {
      key: "key",
      label: "project.key",
      placeholder: "ACME-CRM",
      hint: t("project.keyHint"),
      validate: (value) => {
        const refusal = projectKeyRefusal(value);
        return refusal ? t(refusal) : undefined;
      },
    },
    ...(opts.mode === "create"
      ? [
          {
            key: "organization_id",
            label: "project.company" as const,
            type: "select" as const,
            required: true,
            options:
              current &&
              !companyOptions.some((option) => option.value === current.id)
                ? [
                    { value: current.id, label: current.label },
                    ...companyOptions,
                  ]
                : companyOptions,
          },
        ]
      : []),
    {
      key: "owner_id",
      label: "project.owner",
      type: "select",
      options: ownerOptions,
    },
    { key: "description", label: "project.description", type: "textarea" },
    { key: "target_end_date", label: "project.targetEnd", type: "date" },
  ];
}

function str(value: unknown): string {
  return typeof value === "string" ? value.trim() : "";
}

export function mapProjectCreate(
  values: Record<string, unknown>,
): CreateProjectRequest {
  return {
    name: str(values.name),
    key: str(values.key) || null,
    organization_id: str(values.organization_id),
    owner_id: str(values.owner_id) || null,
    description: str(values.description) || null,
    target_end_date: str(values.target_end_date) || null,
    source: "manual",
  };
}

/**
 * The edit form's values as a project patch. A blank scalar clears the field
 * (explicit null), exactly as the deal patch does; the name is the one field
 * that cannot be cleared, so a blank one is left out rather than sent.
 */
export function mapProjectUpdate(
  values: Record<string, unknown>,
): UpdateProjectRequest {
  return {
    name: str(values.name) || undefined,
    key: str(values.key) || null,
    owner_id: str(values.owner_id) || null,
    description: str(values.description) || null,
    target_end_date: str(values.target_end_date) || null,
  };
}

/** One project as the edit form's initial values. */
export function projectEditRecord(project: Project): Record<
  string,
  string | number | undefined
> & {
  id: string;
  version?: number;
} {
  return {
    id: project.id,
    version: project.version,
    name: project.name,
    key: project.key ?? "",
    owner_id: project.owner_id ?? "",
    description: project.description ?? "",
    target_end_date: project.target_end_date ?? "",
  };
}
