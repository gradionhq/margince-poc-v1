// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { Button, Field, Modal, TextInput } from "../design-system/atoms";
import { useT } from "../i18n";
import { problemMessage, throwProblem } from "./common";
import type { ListQuery } from "./listquery";

// A saved view is the reader's own list state, by name: the sort, the filters,
// the search and the page size they were looking at, restored exactly.
//
// It is per-user and private (the server stamps owner_id from the caller and
// writes shared_scope 'private'), so nothing here asks who may see it — the
// answer is always "only you", and a picker that implied otherwise would be
// promising a sharing model V1 does not have.

type SavedView = components["schemas"]["SavedView"];

/** The resources whose lists offer saved views, as the contract spells them. */
export type ViewResource = "people" | "organizations";

/**
 * The list state a view restores.
 *
 * Deliberately NOT written under the `query.filter` key: the server validates
 * that one as a segment filter TREE (field/op/value, compiled against the
 * resource's engine), and a list's filters are flat `param=value` pairs the
 * list endpoints take directly. Storing them there would be refused as "not a
 * valid filter tree" — and storing a tree here would be a second filter dialect
 * the lists cannot read. So list state lives under its own key and the tree key
 * stays free for the segment builder that owns it.
 */
const LIST_STATE_KEY = "list";

type SavedListState = Pick<
  ListQuery,
  "q" | "sort" | "includeArchived" | "filters" | "perPage"
>;

/** The saved state of one view, or null when the row predates this shape. */
export function listStateOf(view: SavedView): SavedListState | null {
  // `query` is required by the contract, but a stub, a hand-written row, or a
  // future shape can still hand this an object without it — and a list screen
  // that throws while reading its own tab rail takes the whole screen with it.
  const stored = view.query as Record<string, unknown> | undefined;
  const raw = stored?.[LIST_STATE_KEY];
  if (!raw || typeof raw !== "object") {
    return null;
  }
  const state = raw as Partial<SavedListState>;
  // Every field is checked rather than trusted: a view is stored as an open
  // JSON object, so a row written by an older build (or by hand) can carry any
  // shape at all, and a missing sort silently becoming `undefined` would
  // restore a different list than the one that was saved.
  return {
    q: typeof state.q === "string" ? state.q : "",
    sort: typeof state.sort === "string" ? state.sort : "",
    includeArchived: state.includeArchived === true,
    filters:
      state.filters && typeof state.filters === "object"
        ? Object.fromEntries(
            Object.entries(state.filters).filter(
              ([, value]) => typeof value === "string",
            ),
          )
        : {},
    perPage: typeof state.perPage === "number" ? state.perPage : 25,
  };
}

/** What a view saves, given the list the reader is looking at now. */
function listStateFrom(query: ListQuery): Record<string, unknown> {
  return {
    [LIST_STATE_KEY]: {
      q: query.q,
      sort: query.sort,
      includeArchived: query.includeArchived,
      filters: query.filters,
      perPage: query.perPage,
    },
  };
}

/** The caller's saved views for one resource, newest last. */
export function useSavedViews(resource: ViewResource) {
  return useQuery({
    queryKey: ["views", resource],
    queryFn: async (): Promise<SavedView[]> => {
      const { data, error } = await api.GET("/views", {
        params: { query: { resource, limit: 50 } },
      });
      if (error) {
        throwProblem(error);
      }
      return data.data;
    },
    staleTime: 60_000,
  });
}

/**
 * The saved views of one resource, as view tabs the list can render beside its
 * built-in presets.
 *
 * A view whose stored state cannot be read is dropped rather than shown: a tab
 * that lights up and restores nothing is worse than a tab that is not there.
 */
export function useSavedViewTabs(resource: ViewResource) {
  const views = useSavedViews(resource);
  return (views.data ?? []).flatMap((view) => {
    const state = listStateOf(view);
    return state
      ? [
          {
            id: view.id,
            label: view.name,
            sort: state.sort,
            filters: state.filters,
          },
        ]
      : [];
  });
}

/**
 * Save the current list as a named view, and remove one that has served its
 * purpose.
 *
 * Both invalidate the resource's view list, so the tab rail is whatever the
 * server holds rather than a local copy that drifts from it.
 */
export function useSaveView(resource: ViewResource) {
  const client = useQueryClient();
  const invalidate = () =>
    client.invalidateQueries({ queryKey: ["views", resource] });

  const create = useMutation({
    mutationFn: async (input: Readonly<{ name: string; query: ListQuery }>) => {
      const { data, error } = await api.POST("/views", {
        body: {
          resource,
          name: input.name,
          query: listStateFrom(input.query),
        },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    onSuccess: invalidate,
  });

  const remove = useMutation({
    mutationFn: async (id: string) => {
      const { error } = await api.DELETE("/views/{id}", {
        params: { path: { id } },
      });
      if (error) {
        throwProblem(error);
      }
    },
    onSuccess: invalidate,
  });

  return { create, remove };
}

/**
 * "Save this view" beside a list's own tools, plus the dialog that names it.
 *
 * Offered only when the list is actually narrowed. Saving the unfiltered
 * default would create a tab that does what the All tab already does, and a
 * rail of those is how a useful feature becomes clutter.
 */
export function SaveViewAction({
  resource,
  query,
}: Readonly<{ resource: ViewResource; query: ListQuery }>) {
  const t = useT();
  const [open, setOpen] = useState(false);
  const [name, setName] = useState("");
  const { create } = useSaveView(resource);
  const headingId = "save-view-heading";

  const narrowed =
    Boolean(query.q) ||
    Boolean(query.sort) ||
    query.includeArchived ||
    Object.values(query.filters).some(Boolean);
  if (!narrowed) {
    return null;
  }

  const submit = () => {
    const named = name.trim();
    if (!named) {
      return;
    }
    create.mutate(
      { name: named, query },
      {
        onSuccess: () => {
          setName("");
          setOpen(false);
        },
      },
    );
  };

  return (
    <>
      <Button small onClick={() => setOpen(true)}>
        {t("views.save")}
      </Button>
      <Modal open={open} onClose={() => setOpen(false)} labelledBy={headingId}>
        <h2
          id={headingId}
          className="t-h2"
          style={{ marginBottom: "var(--space-3)" }}
        >
          {t("views.saveTitle")}
        </h2>
        <Field
          label={t("views.name")}
          error={create.isError ? problemMessage(create.error, t) : undefined}
        >
          {(control) => (
            <TextInput
              {...control}
              value={name}
              onChange={(event) => setName(event.target.value)}
            />
          )}
        </Field>
        <div className="row-end" style={{ marginTop: "var(--space-4)" }}>
          <Button onClick={() => setOpen(false)}>{t("create.cancel")}</Button>
          <Button
            variant="primary"
            onClick={submit}
            disabled={!name.trim() || create.isPending}
          >
            {t("views.saveConfirm")}
          </Button>
        </div>
      </Modal>
    </>
  );
}
