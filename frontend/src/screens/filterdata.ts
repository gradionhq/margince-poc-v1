// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The two reads a filter builder needs (LVS-EXT-8/9), and nothing else.
//
// Kept apart from the screen for one reason: the screen decides what a human
// SEES, and these decide what the server was ASKED. The interesting rules here
// are about when not to ask — a tree mid-edit is not worth a round trip, and a
// vocabulary is worth exactly one per record type.

import { useQuery } from "@tanstack/react-query";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { throwProblem } from "./common";
import {
  type EncodedPredicate,
  encode,
  isComplete,
  type Node,
} from "./segmentpredicate";

export type FilterVocabulary = components["schemas"]["FilterVocabulary"];
export type VocabularyField = components["schemas"]["FilterVocabularyField"];
export type FilterPreview = components["schemas"]["FilterPreview"];

/** The record types a filter may name — the contract's own closed set. */
export type FilterResource = FilterVocabulary["resource"];

/**
 * The page this screen asks for, sent on every request rather than left absent.
 *
 * The server defaults an absent limit to the same number, but the generated body
 * type makes `limit` required — openapi-typescript treats a schema `default` as
 * "always present", which is right for a response and wrong for a request. That
 * is established here (`max_messages` on the connector body generates the same
 * way), so this follows the house rather than reshaping the contract to dodge it.
 */
const PREVIEW_PAGE = 25;

export function vocabularyQueryKey(resource: FilterResource) {
  return ["filter-vocabulary", resource] as const;
}

/**
 * What this record type may be filtered on.
 *
 * Cached per resource and never invalidated by editing: the vocabulary changes
 * when an admin adds or retires a custom field, not when somebody builds a
 * clause, so one fetch serves a whole editing session. That is also why the
 * field picker reads THIS rather than a list of its own — the server owns which
 * fields exist, and a second copy would offer one the engine refuses.
 */
export function useFilterVocabulary(resource: FilterResource) {
  return useQuery<FilterVocabulary>({
    queryKey: vocabularyQueryKey(resource),
    queryFn: async () => {
      const { data, error } = await api.GET("/filters/vocabulary", {
        params: { query: { resource } },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });
}

/**
 * The match count and first page for a tree nobody has saved.
 *
 * Two things this deliberately does not do.
 *
 * It does not ask about an INCOMPLETE tree. A clause whose value is still empty
 * would be refused with a 422 naming the field, which is correct of the server
 * and useless to a human who has simply not finished typing — so `isComplete`
 * gates the request and the caller renders the previous count until the clause
 * is whole. The server's refusal stays the backstop for a tree that is complete
 * and still wrong.
 *
 * And it does not key on the tree's identity — it keys on the ENCODED tree, so
 * two edits that produce the same filter (retyping a value back, reordering
 * nothing) reuse one cached answer instead of spending a request to learn the
 * count did not move.
 */
export function useFilterPreview(
  resource: FilterResource,
  tree: Node,
  limit: number = PREVIEW_PAGE,
) {
  const askable = isComplete(tree);
  const filter = encode(tree);
  return useQuery<FilterPreview>({
    queryKey: ["filter-preview", resource, filter, limit],
    enabled: askable,
    // A count that flickers to a spinner on every keystroke is harder to read
    // than one that lags: the previous answer stays on screen while the next is
    // in flight, and the caller shows staleness rather than absence.
    placeholderData: (previous) => previous,
    queryFn: async () => {
      const { data, error } = await api.POST("/filters/preview", {
        body: { resource, filter, limit },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });
}

/**
 * The offerable fields grouped for a picker: core first, then custom.
 *
 * Core-before-custom because a reader looking for "Owner" should not scroll past
 * a workspace's twenty bespoke columns to find it, and within each group by the
 * name the server gave — which is already sorted, so this only splits.
 */
export function groupFields(fields: readonly VocabularyField[]) {
  const core: VocabularyField[] = [];
  const custom: VocabularyField[] = [];
  for (const field of fields) {
    (field.custom ? custom : core).push(field);
  }
  return { core, custom };
}

/**
 * The label a picker shows for a field.
 *
 * A custom column arrives as its physical `cf_`-prefixed name because the
 * vocabulary deliberately carries no labels — the custom-field catalog owns
 * those. Until this screen joins that catalog, stripping the prefix and
 * un-snaking the rest is an honest approximation of the admin's label rather
 * than an invented one: `cf_loyalty_tier` reads as "loyalty tier", which is what
 * the admin typed, modulo capitalization.
 */
export function fieldLabel(field: VocabularyField): string {
  const bare = field.custom ? field.name.replace(/^cf_/, "") : field.name;
  return bare.replaceAll("_", " ");
}

/** The wire shape a caller hands the preview, for tests that assert the body. */
export type { EncodedPredicate };
