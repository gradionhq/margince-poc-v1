// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Saving the authored filter as a DYNAMIC LIST.
//
// A dynamic list and a saved view are different objects and this screen offers
// both, so it is worth saying which is which rather than leaving a reader to
// guess from two adjacent buttons:
//
//   a saved view is the reader's OWN state, private, per-user, restoring the
//   filter they were looking at;
//   a dynamic list is a first-class record the workspace shares, whose
//   membership the server evaluates live on every read — the thing an
//   automation, an export or another person can point at.
//
// The tree goes to `definition` unchanged. `encode` already answers exactly the
// shape `POST /lists` validates against the live vocabulary, which is the whole
// reason the editor's tree and storekit.Predicate are the same shape: what a
// filter may say, what it selects, and what a list of it contains cannot differ.

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api/client";
import { NamePrompt } from "../design-system/nameprompt";
import { useT } from "../i18n";
import { problemMessageOf, throwProblem } from "./common";
import type { FilterResource } from "./filterdata";
import { encode, isComplete, type Node } from "./segmentpredicate";

/**
 * Create a dynamic list for one record type.
 *
 * `entity_type` takes the FILTER vocabulary's word for the object — `/lists`
 * enumerates `person`, the same as `/filters/*` and `/exports` — so no third
 * spelling is needed here. The saved-view rail is the one surface that needs the
 * plural, and that mapping lives on the screen where the two meet.
 */
function useSaveDynamicList(resource: FilterResource) {
  const client = useQueryClient();
  return useMutation({
    mutationFn: async (
      input: Readonly<{ name: string; definition: unknown }>,
    ) => {
      const { data, error } = await api.POST("/lists", {
        body: {
          name: input.name,
          entity_type: resource,
          list_type: "dynamic",
          // The tree, not a wrapper. The server reads `definition` AS the
          // predicate and 422s naming the offending field if it does not
          // typecheck against this resource's vocabulary.
          definition: input.definition as Record<string, unknown>,
        },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    // A list is a shared record, so the lists any other surface is showing are
    // now one row out of date.
    onSuccess: () => client.invalidateQueries({ queryKey: ["lists"] }),
  });
}

/**
 * "Save as list" beside the builder.
 *
 * Gated on `isComplete` for the same reason Save view and Export are: the server
 * validates the definition on create, so an incomplete tree would earn a 422 the
 * reader could have been spared — and a dynamic list that fails validation is not
 * created at all, which is a worse outcome here than for a preview.
 */
export function SaveFilterListAction({
  resource,
  tree,
}: Readonly<{ resource: FilterResource; tree: Node }>) {
  const t = useT();
  const save = useSaveDynamicList(resource);

  if (!isComplete(tree)) {
    return null;
  }

  return (
    <NamePrompt
      trigger={t("filters.saveList")}
      title={t("filters.saveListTitle")}
      label={t("filters.listName")}
      confirmLabel={t("filters.saveListConfirm")}
      pending={save.isPending}
      problem={save.isError ? problemMessageOf(save.error, t) : undefined}
      // The tree is encoded at save time, so what goes to the server is the
      // filter the reader is looking at rather than one a stale closure held.
      onSave={(name, done) =>
        save.mutate({ name, definition: encode(tree) }, { onSuccess: done })
      }
    />
  );
}
