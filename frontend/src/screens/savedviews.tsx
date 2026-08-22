// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { Button, OverflowMenu } from "../design-system/atoms";
import { NamePrompt } from "../design-system/nameprompt";
import { SurfaceState } from "../design-system/surfacestate";
import { useT } from "../i18n";
import { problemMessageOf, throwProblem, useSorMode } from "./common";
import type { ListQuery, SavedViewTab } from "./listquery";
import { decode, encode, isComplete, type Node } from "./segmentpredicate";

// A saved view is the reader's own list state, by name: the search, the sort,
// the filters, the archived toggle and the page size they were looking at.
//
// Pressing its tab restores four of those five. The SEARCH is stored and read
// back but not written to the box, so a view naming one is a view whose tab
// lights only while that search is actually typed: the tab is a claim about
// what the list is showing, and it makes no claim it cannot keep.
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
export type ViewResource = "people" | "organizations" | "deals" | "projects";

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
  "q" | "sort" | "includeArchived" | "filters"
> & {
  /**
   * The page size the view claims, or absent when the stored blob makes no
   * claim about one.
   *
   * Optional rather than defaulted, because those are different facts and the
   * rail acts on the difference: a view that stored 25 asks for 25, and a view
   * that stored nothing leaves the reader on whatever they had. Substituting a
   * number here made the two indistinguishable, and a view written through
   * `POST /views` with a `list` blob carrying no page size then dropped a
   * reader from 100 rows to the default with no dial moving.
   */
  perPage?: number;
};

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
  // shape at all. The four dials that describe WHICH rows the list holds take a
  // neutral value when the blob omits them, because the list has to send the
  // server something; the page size does not, and stays absent so the rail can
  // tell "asked for the default" from "asked for nothing".
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
    perPage: typeof state.perPage === "number" ? state.perPage : undefined,
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
 *
 * Everything the view claims travels with the tab, not just the sort and the
 * filters: the search, the archived toggle and the page size were saved too, and
 * a tab that drops one of them claims a list it is not describing. The id
 * travels for a different reason — it is what still identifies the view after
 * `/views` has re-ordered the rail around a rename.
 *
 * A failed read answers with no tabs, which is indistinguishable from a reader
 * who has saved none. `SaveViewAction` says which it was, because a hook can
 * only answer with tabs.
 */
export function useSavedViewTabs(resource: ViewResource): SavedViewTab[] {
  const views = useSavedViews(resource);
  return (views.data ?? []).flatMap((view) => {
    const state = listStateOf(view);
    return state
      ? [
          {
            id: view.id,
            label: view.name,
            q: state.q,
            sort: state.sort,
            filters: state.filters,
            includeArchived: state.includeArchived,
            perPage: state.perPage,
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
 * "Save this view", named through the catalog's one name-and-save dialog.
 *
 * What differs between a list's dials and the segment builder's tree is WHAT
 * gets saved, so that arrives as the blob to store. The dialog itself is not
 * this module's to own: it used to be a hand-rolled `Modal` here, which is how a
 * second surface asking the same question ends up asking it differently.
 *
 * `blob` is read at save time rather than at render time: it is the committed
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
  const { create } = useSaveView(resource);

  return (
    <NamePrompt
      trigger={t("views.save")}
      title={t("views.saveTitle")}
      label={t("views.name")}
      confirmLabel={t("views.saveConfirm")}
      pending={create.isPending}
      // `problemMessageOf`, not `problemMessage`: what a failed mutation carries
      // is a ProblemError, and handing that to the body reader yields the
      // generic "request failed" instead of the reason the server gave.
      problem={create.isError ? problemMessageOf(create.error, t) : undefined}
      onSave={(name, done) =>
        create.mutate({ name, query: blob() }, { onSuccess: done })
      }
    />
  );
}

/**
 * "Save this view" beside a list's own tools, and the one place that says the
 * saved-view rail failed to load.
 *
 * Saving is offered only when the list is actually narrowed. Saving the
 * unfiltered default would create a tab that does what the All tab already
 * does, and a rail of those is how a useful feature becomes clutter.
 *
 * The failure is reported here rather than on the rail because the rail is a
 * row of tabs: with no tabs there is nothing to hang the state on, and an empty
 * rail says "you have saved none" — a claim the screen cannot make when the
 * read failed. This is the control that manages saved views, so it is where a
 * reader looks, and it is `failed` rather than `unavailable` because a retry is
 * offered with it. It is NAMED, because it lands in the list's tools slot beside
 * Columns and Compact: "this section did not load" under a toolbar covering
 * three controls says which one only if the part is named.
 *
 * And it is reported only where the rail is drawn at all. Overlay mode has no
 * saved-view rail — a tab whose preset the incumbent mirror refuses is a tab
 * that lights up and does nothing — so a failed read there has no surface to be
 * about, and reporting it would put a failure on screen for something the
 * screen deliberately does not show.
 */
export function SaveViewAction({
  resource,
  query,
}: Readonly<{ resource: ViewResource; query: ListQuery }>) {
  const t = useT();
  const railDrawn = useSorMode() !== "overlay";
  // The same query the rail reads, so this costs no second request: one read of
  // /views answers both, and the rail cannot be shown as loaded here and failed
  // there.
  const views = useSavedViews(resource);
  const narrowed =
    Boolean(query.q) ||
    Boolean(query.sort) ||
    query.includeArchived ||
    Object.values(query.filters).some(Boolean);
  return (
    <>
      {railDrawn && views.isError && (
        <SurfaceState
          state="failed"
          label={t("views.rail")}
          // Read by the `empty` arm alone, which this call never reaches: what
          // there is none of is not a question a failed read has an answer to.
          emptyLabel={t("common.empty")}
          detail={{ onRetry: () => void views.refetch() }}
        >
          {null}
        </SurfaceState>
      )}
      {narrowed && (
        <SaveViewButton resource={resource} blob={() => listStateFrom(query)} />
      )}
    </>
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
