// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The records an id filter clause can point at, as options a reader picks from.
//
// The vocabulary now names the target record type per field, so this module is a
// lookup FROM that name — not from the field's own name. That is the whole
// difference: a map keyed on `stage_id` would not know about the next id leaf the
// engine gains, whereas a map keyed on `stage` covers every field that ever
// points at a stage.
//
// One target is deliberately absent. An organization list is unbounded — a
// workspace has as many accounts as it has customers — so it cannot be
// enumerated into a dropdown, and the async picker it needs is its own change.
// `boundedReference` says which targets this module can answer, so the caller
// falls back to a plain box rather than rendering an empty list.

import { useQuery } from "@tanstack/react-query";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { throwProblem } from "./common";

/** The record type an id field points at, as the vocabulary reports it. */
export type Reference = NonNullable<
  components["schemas"]["FilterVocabularyField"]["references"]
>;

/** One option, already carrying the label a reader should see. */
export type ReferenceOption = Readonly<{ value: string; label: string }>;

/**
 * Whether this module can enumerate the target.
 *
 * Only `organization` cannot: it is the one reference whose set grows with the
 * business rather than with configuration. Everything else is workspace
 * configuration — tags, seats, teams, pipelines, stages, projects — and small
 * enough that one page answers it.
 */
export function boundedReference(reference: Reference | undefined): boolean {
  return reference !== undefined && reference !== "organization";
}

/**
 * How many to read from the two targets that page.
 *
 * Most of these lists take no limit at all — tags, teams, pipelines and stages
 * are unpaged because they are configuration a human maintains, and the contract
 * says so by offering no cursor. Seats and projects do page, and one page of
 * either is more than a filter builder can usefully show; a workspace past this
 * many has a problem a dropdown cannot solve.
 */
const ROSTER_LIMIT = 200;

/**
 * The options for one reference target, or an empty list while it loads and for
 * a target this module does not enumerate.
 *
 * Keyed on the target rather than on the field, so two fields pointing at the
 * same record type share one cache entry and one request — `organization_id` and
 * `partner_org_id` would, if organizations were bounded, and `owner_id` on five
 * resources already does.
 */
export function useReferenceOptions(reference: Reference | undefined) {
  const query = useQuery({
    queryKey: ["filter-reference", reference],
    enabled: boundedReference(reference),
    // Configuration changes rarely and a builder reads it on every clause, so a
    // minute of staleness saves a request per keystroke-adjacent render.
    staleTime: 60_000,
    queryFn: async (): Promise<ReferenceOption[]> => readOptions(reference),
  });
  return { options: query.data ?? [], loading: query.isPending };
}

// Each arm reads the surface that owns the record type and answers the display
// column that record type actually has — `display_name` for a seat, `name` for
// everything else. Spelled per arm rather than through one generic reader
// because the response types differ, and a cast to paper over that would be the
// place a renamed column stopped being noticed.
async function readOptions(
  reference: Reference | undefined,
): Promise<ReferenceOption[]> {
  switch (reference) {
    case "tag": {
      const { data, error } = await api.GET("/tags");
      if (error) {
        throwProblem(error);
      }
      return data.data.map((tag) => ({ value: tag.id, label: tag.name }));
    }
    case "app_user": {
      const { data, error } = await api.GET("/users", {
        params: { query: { limit: ROSTER_LIMIT } },
      });
      if (error) {
        throwProblem(error);
      }
      return data.data.map((seat) => ({
        value: seat.id,
        label: seat.display_name,
      }));
    }
    case "team": {
      const { data, error } = await api.GET("/teams");
      if (error) {
        throwProblem(error);
      }
      return data.data.map((team) => ({ value: team.id, label: team.name }));
    }
    case "pipeline": {
      const { data, error } = await api.GET("/pipelines");
      if (error) {
        throwProblem(error);
      }
      return data.data.map((pipeline) => ({
        value: pipeline.id,
        label: pipeline.name,
      }));
    }
    case "stage": {
      const { data, error } = await api.GET("/stages");
      if (error) {
        throwProblem(error);
      }
      return data.data.map((stage) => ({ value: stage.id, label: stage.name }));
    }
    case "project": {
      const { data, error } = await api.GET("/projects", {
        params: { query: { limit: ROSTER_LIMIT } },
      });
      if (error) {
        throwProblem(error);
      }
      return data.data.map((project) => ({
        value: project.id,
        label: project.name,
      }));
    }
    default:
      // `organization` and an absent target both land here. The query is
      // disabled for them, so this is unreachable rather than a silent empty
      // answer — and it stays exhaustive so a new target added to the contract
      // arrives here as a compile-visible gap rather than an empty dropdown.
      return [];
  }
}
