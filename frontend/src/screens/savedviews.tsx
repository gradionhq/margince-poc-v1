// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useId, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import {
  Button,
  Field,
  Modal,
  OverflowMenu,
  TextInput,
} from "../design-system/atoms";
import { useT } from "../i18n";
import { problemMessageOf, throwProblem } from "./common";
import type { ListQuery } from "./listquery";
import { decode, encode, isComplete, type Node } from "./segmentpredicate";

// A saved view is the reader's own list state, by name: the sort, the filters,
// the search and the page size they were looking at, restored exactly.
//
// It is per-user and private (the server stamps owner_id from the caller and
// writes shared_scope 'private'), so nothing here asks who may see it — the
// answer is always "only you", and a picker that implied otherwise would be
// promising a sharing model V1 does not have.

type SavedView = components["schemas"]["SavedView"];

/**
 * The resources whose lists offer saved views, as the contract spells them.
 *
 * Plural here and singular on `/filters/*` — `people` against `person` — because
 * the two endpoint families spell their enums differently. That correspondence
 * is written down once, where the two meet (the filters screen's `VIEW_OF`), and
 * nowhere else.
 */
export type ViewResource = "people" | "organizations" | "deals";

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

/**
 * The key the segment builder's tree lives under — the one `LIST_STATE_KEY`
 * above deliberately leaves free.
 *
 * The server validates this one as a filter TREE and compiles it against the
 * resource's engine, which is exactly what it holds: the same predicate the
 * builder sends to `/filters/preview` and the same one `/exports` takes. One
 * filter dialect, whichever surface reads it.
 */
const FILTER_KEY = "filter";

/**
 * The filter tree a view restores, or null when it holds none this editor can
 * read.
 *
 * Same contract as `listStateOf` and for the same reason: `query` is an open
 * JSON object, so a row can carry a shape this build has never seen, and a
 * builder that threw while reading its own view menu would take the screen with
 * it. `decode` does the checking and answers null rather than guessing.
 */
export function filterTreeOf(view: SavedView): Node | null {
  const stored = view.query as Record<string, unknown> | undefined;
  return decode(stored?.[FILTER_KEY]);
}

/** What a view saves, given the filter the reader has just built. */
function filterStateFrom(tree: Node): Record<string, unknown> {
  return { [FILTER_KEY]: encode(tree) };
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

  // The blob is the caller's, not this hook's: a list saves its dials under one
  // key and the segment builder saves a tree under another, and both go through
  // ONE write so there is one place that stamps the resource and invalidates the
  // rail. A second mutation per shape is how the two would drift.
  const create = useMutation({
    mutationFn: async (
      input: Readonly<{ name: string; query: Record<string, unknown> }>,
    ) => {
      const { data, error } = await api.POST("/views", {
        body: {
          resource,
          name: input.name,
          query: input.query,
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
 * "Save this view", and the dialog that names it.
 *
 * One spelling for both surfaces. What differs between a list and the segment
 * builder is WHAT gets saved, so that arrives as the blob to store — the dialog,
 * the naming rule, and the error placement are the same question either way, and
 * a second copy of this is how the two would end up asking it differently.
 *
 * `blob` is read at click time rather than at render time: it is the committed
 * render's state that the mutation is given, never a closure the observer might
 * still hold from an earlier one.
 */
function SaveViewButton({
  resource,
  blob,
}: Readonly<{
  resource: ViewResource;
  blob: () => Record<string, unknown>;
}>) {
  const t = useT();
  const [open, setOpen] = useState(false);
  const [name, setName] = useState("");
  const { create } = useSaveView(resource);
  // Minted rather than fixed: two save actions can share a page, and a repeated
  // id points the dialog's label at whichever heading rendered first.
  const headingId = useId();

  const submit = () => {
    const named = name.trim();
    if (!named) {
      return;
    }
    create.mutate(
      { name: named, query: blob() },
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
          // `problemMessageOf`, not `problemMessage`: what a failed mutation
          // carries is a ProblemError, and handing that to the body reader
          // yields the generic "request failed" instead of the reason the
          // server actually gave.
          error={create.isError ? problemMessageOf(create.error, t) : undefined}
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

/**
 * "Save this view" beside a list's own tools.
 *
 * Offered only when the list is actually narrowed. Saving the unfiltered
 * default would create a tab that does what the All tab already does, and a
 * rail of those is how a useful feature becomes clutter.
 */
export function SaveViewAction({
  resource,
  query,
}: Readonly<{ resource: ViewResource; query: ListQuery }>) {
  const narrowed =
    Boolean(query.q) ||
    Boolean(query.sort) ||
    query.includeArchived ||
    Object.values(query.filters).some(Boolean);
  if (!narrowed) {
    return null;
  }
  return (
    <SaveViewButton resource={resource} blob={() => listStateFrom(query)} />
  );
}

/**
 * "Save this view" beside the segment builder.
 *
 * The same completeness rule the count and the preview use, for the same reason:
 * an incomplete tree is one the engine refuses, so saving it would store a view
 * that fails the moment anybody opens it. `isComplete` is the one place that
 * judgement lives.
 */
export function SaveFilterViewAction({
  resource,
  tree,
}: Readonly<{ resource: ViewResource; tree: Node }>) {
  if (!isComplete(tree)) {
    return null;
  }
  return (
    <SaveViewButton resource={resource} blob={() => filterStateFrom(tree)} />
  );
}

/**
 * The reader's saved filters for this object, each one click from being loaded.
 *
 * A menu rather than a picker: loading a view is an ACTION, and a select would
 * keep claiming the loaded view is what the builder holds long after the reader
 * has edited it into something else.
 *
 * A view whose stored tree cannot be read is left out, matching the list rail's
 * rule — an entry that lights up and restores nothing is worse than no entry.
 */
export function LoadFilterViewMenu({
  resource,
  onLoad,
}: Readonly<{ resource: ViewResource; onLoad: (tree: Node) => void }>) {
  const t = useT();
  const views = useSavedViews(resource);
  const readable = (views.data ?? []).flatMap((view) => {
    const tree = filterTreeOf(view);
    return tree ? [{ id: view.id, name: view.name, tree }] : [];
  });
  if (readable.length === 0) {
    return null;
  }
  return (
    <OverflowMenu label={t("filters.loadView")}>
      {readable.map((view) => (
        <Button key={view.id} small onClick={() => onLoad(view.tree)}>
          {view.name}
        </Button>
      ))}
    </OverflowMenu>
  );
}
