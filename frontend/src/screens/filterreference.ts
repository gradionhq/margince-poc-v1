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
import { type RosterKind, useRoster, useRosterPartial } from "./entityref";

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
 * business rather than with configuration. Everything else is either workspace
 * configuration a human maintains — tags, pipelines, stages, projects — or the
 * workspace roster, which the shared walk enumerates for every picker here.
 */
export function boundedReference(reference: Reference | undefined): boolean {
  return reference !== undefined && reference !== "organization";
}

/**
 * How many to read from the one target this module still pages itself.
 *
 * Tags, pipelines and stages take no limit at all — those three are the ones the
 * contract genuinely offers no cursor for, and they are configuration a human
 * maintains. Seats and teams DO page, and they are not this module's lists to
 * page: they are the workspace roster, which the shared walk already reads to
 * the end for every picker in the product. Projects are the remainder, and one
 * page of them is more than a filter builder can usefully show; a workspace past
 * this many has a problem a dropdown cannot solve.
 */
const PROJECT_LIMIT = 200;

/**
 * The roster kind behind a reference target, for the two targets that ARE the
 * roster.
 *
 * Reading them here with a page size of this module's own is what let the filter
 * builder offer 200 seats and 50 teams while the bulk bar beside it offered
 * 2 000 of the same people from the same endpoint — one list, one purpose, two
 * answers. Off the shared walk they are the same list everywhere, at no extra
 * request: every consumer observes one cache entry per kind.
 */
function rosterKindOf(reference: Reference | undefined): RosterKind | null {
  if (reference === "app_user") {
    return "user";
  }
  return reference === "team" ? "team" : null;
}

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
  const kind = rosterKindOf(reference);
  // `enabled` is what decides whether either read runs, so the kind handed over
  // when the target is not a roster one is inert — a disabled query fetches
  // nothing, and the branch below never looks at its result.
  const roster = useRoster(kind ?? "user", kind !== null);
  const rosterPartial = useRosterPartial(kind ?? "user", kind !== null);
  const query = useQuery({
    queryKey: ["filter-reference", reference],
    enabled: boundedReference(reference) && kind === null,
    // Configuration changes rarely and a builder reads it on every clause, so a
    // minute of staleness saves a request per keystroke-adjacent render.
    staleTime: 60_000,
    queryFn: async (): Promise<ReferenceOption[]> => readOptions(reference),
  });
  if (kind !== null) {
    return {
      options: (roster.data ?? []).map((entry) => ({
        value: entry.id,
        // A seat's display column is `display_name` and a team's is `name`;
        // narrowing on the field rather than asserting the row's type is what
        // keeps a renamed column a compile error instead of a blank label.
        label: "display_name" in entry ? entry.display_name : entry.name,
      })),
      loading: roster.isPending,
      failed: roster.isError,
      // The walk is bounded, so this list can be short in a way the reader
      // cannot see. A dropdown that quietly omits colleagues reads as a smaller
      // workspace, and the clause a reader then writes is about the wrong set.
      partial: rosterPartial,
    };
  }
  return {
    options: query.data ?? [],
    loading: query.isPending,
    // A read that FAILED must not be reported as an empty set. Without this the
    // caller renders an enabled dropdown with nothing in it, which says "this
    // workspace has no tags" — a confident answer to a question that was never
    // answered. The caller falls back to a plain box instead, so a reader can
    // still write the clause.
    failed: query.isError,
    // Every list left here answers in one page by contract, so there is no
    // truncation to disclose.
    partial: false,
  };
}

// Each arm reads the surface that owns the record type and answers the display
// column that record type actually has. Spelled per arm rather than through one
// generic reader because the response types differ, and a cast to paper over
// that would be the place a renamed column stopped being noticed. The two roster
// targets are not here: `useReferenceOptions` answers them off the shared walk,
// and its query is disabled for them.
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
        params: { query: { limit: PROJECT_LIMIT } },
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
      // `organization`, an absent target, and the two roster targets all land
      // here. The query is disabled for every one of them, so this is
      // unreachable rather than a silent empty answer — and the arms above stay
      // one per target so a new one added to the contract arrives here as an
      // empty dropdown a reader reports rather than a wrong list nobody sees.
      return [];
  }
}
