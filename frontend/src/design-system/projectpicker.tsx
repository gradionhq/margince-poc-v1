// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useEffect, useRef } from "react";
import type { components } from "../api/schema";
import { useT } from "../i18n";
import { Select } from "./select";
import "./projectpicker.css";

// The ONE way a surface is told which project it is about, and the ONE line
// that says which project its output was narrowed to.
//
// Every AI surface that reads an account or a person — the composers, the
// prepared questions, the account brief, the meeting brief — renders this
// picker over the same `projects` section the two 360s carry, and prints the
// same scope line under its output. One control, so "Scoped to ERP-27" reads
// identically wherever a rep meets it.

// One project as the picker shows it: the fields the Organization360 and
// Person360 `projects` sections share.
export type PickableProject = Pick<
  components["schemas"]["Organization360Project"],
  "project_id" | "name" | "key" | "phase"
>;

// What the server reports about a read it narrowed to one project.
export type ProjectScope = components["schemas"]["ProjectScope"];

// The projects a surface can be scoped to: the unarchived ones the page
// carries, minus the closed — a closed project is history, and a new message
// or a fresh reading is not about history.
export function liveProjects(
  projects: readonly PickableProject[] | undefined,
): PickableProject[] {
  return (projects ?? []).filter((project) => project.phase !== "closed");
}

// When the record carries exactly ONE live project, it is the default — a rep
// working from a company with one engagement should not have to say so. The
// default is applied ONCE per sole project, when it first arrives, so a rep
// who then picks "no project" is not overruled on the next render; and it is
// applied through the same setter a pick uses, so the selected value RENDERS
// rather than being sent silently. A DIFFERENT sole project arriving later —
// the list refetched after the chosen one closed — is defaulted again, since
// the earlier choice was cleared with the project it named.
export function useSoleProjectDefault(
  projects: readonly PickableProject[],
  projectId: string,
  onChange: (next: string) => void,
) {
  const defaultedFor = useRef("");
  const sole = projects.length === 1 ? projects[0].project_id : "";
  // A choice the list still offers stands, and counts as this sole project's
  // default having been settled. A choice it no longer offers is about to be
  // cleared (useClearVanishedChoice), and the default applies to the empty
  // value that follows — so it is not settled here.
  const standing = projects.some((project) => project.project_id === projectId);
  useEffect(() => {
    if (!sole || defaultedFor.current === sole) {
      return;
    }
    if (standing) {
      defaultedFor.current = sole;
      return;
    }
    if (!projectId) {
      defaultedFor.current = sole;
      onChange(sole);
    }
  }, [sole, projectId, standing, onChange]);
}

// A chosen project that the list no longer offers — closed since, withheld
// after a refetch, gone with a changed record — is cleared rather than kept.
// The picker would show "No project" over a hidden id that still travels on
// every request, so the rep reads an unscoped surface while the server
// answers for a project they can no longer see. Cleared through the same
// setter a pick uses, so the surface re-reads unscoped like any other change.
function useClearVanishedChoice(
  projects: readonly PickableProject[],
  projectId: string,
  onChange: (next: string) => void,
) {
  const offered =
    projectId === "" ||
    projects.some((project) => project.project_id === projectId);
  useEffect(() => {
    if (!offered) {
      onChange("");
    }
  }, [offered, onChange]);
}

// The picker, and the scope line beneath it.
//
// `scope` is the server's own report from the last response. It is shown
// only while it names the project the picker shows — a rep who switches
// projects after a draft arrived must not read the previous project's counts
// under the new project's key. Until a counted report arrives the line names
// the chosen project alone.
export function ProjectPicker({
  projects,
  projectId,
  onChange,
  scope,
}: Readonly<{
  projects: readonly PickableProject[];
  projectId: string;
  onChange: (next: string) => void;
  scope?: ProjectScope;
}>) {
  const t = useT();
  useClearVanishedChoice(projects, projectId, onChange);
  if (projects.length === 0) {
    return null;
  }
  const chosen = projects.find((project) => project.project_id === projectId);
  const counted = scope?.project_id === projectId ? scope : undefined;
  return (
    <>
      <label className="t-body projectpicker">
        {t("compose.project")}
        <Select
          aria-label={t("compose.project")}
          options={[
            { value: "", label: t("compose.projectNone") },
            ...projects.map((project) => ({
              value: project.project_id,
              label: project.key
                ? `${project.key} · ${project.name}`
                : project.name,
            })),
          ]}
          value={projectId}
          onChange={onChange}
        />
      </label>
      {counted && <ScopeLine scope={counted} />}
      {!counted && chosen && (
        <p className="t-caption">
          {t("compose.scopedTo", { key: chosen.key ?? chosen.name })}
        </p>
      )}
    </>
  );
}

// The scope line on its own, for a surface whose scope was not chosen in a
// picker — a meeting brief that scopes itself by the meeting's own filing.
//
// The counts are the server's: how many of the anchor's activities the
// narrowing kept, out of how many there are. They are absent when the caller
// may not count activities, and the line then names the project alone
// rather than printing a number nobody computed.
export function ScopeLine({ scope }: Readonly<{ scope: ProjectScope }>) {
  const t = useT();
  const key = scope.key ?? scope.name;
  if (scope.in_scope == null || scope.total == null) {
    return <p className="t-caption">{t("compose.scopedTo", { key })}</p>;
  }
  return (
    <p className="t-caption">
      {t("compose.scopedToCounted", {
        key,
        inScope: String(scope.in_scope),
        total: String(scope.total),
      })}
    </p>
  );
}
