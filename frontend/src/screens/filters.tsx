// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The Filters & views screen (AC-filters-and-views-1/3/4): the surface a
// human authors a dynamic filter on, which until now existed only through the API.
//
// It hosts three things and owns none of them. The object control picks which
// record type's vocabulary to read; the builder draws the tree; the count comes
// back from the preview. What this file adds is the wiring and one judgement — how
// to report a count that is one edit behind, which is the honest state of any live
// recount over a moving table.

import { useState } from "react";
import {
  Badge,
  Card,
  SectionHeader,
  SegmentedControl,
} from "../design-system/atoms";
import { type SectionState, SurfaceState } from "../design-system/surfacestate";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { FilterBuilder } from "./filterbuilder";
import {
  type FilterResource,
  useFilterPreview,
  useFilterVocabulary,
} from "./filterdata";
import { FilterResults } from "./filterresults";
import "./filters.css";
import { fieldsNamed, type Node, newGroup } from "./segmentpredicate";

/**
 * The three object tabs AC-1 names, and the record type each reads.
 *
 * The tab says "Contacts" and the vocabulary says "person": the wire's word and
 * the product's word differ, and this is the one place that correspondence is
 * written down rather than assumed at each call site.
 */
const OBJECT_TABS = ["contacts", "companies", "deals"] as const;
type ObjectTab = (typeof OBJECT_TABS)[number];

const RESOURCE_OF: Record<ObjectTab, FilterResource> = {
  contacts: "person",
  companies: "organization",
  deals: "deal",
};

const TAB_LABEL: Record<ObjectTab, MessageKey> = {
  contacts: "filters.tab.contacts",
  companies: "filters.tab.companies",
  deals: "filters.tab.deals",
};

const MATCH_LABEL: Record<ObjectTab, MessageKey> = {
  contacts: "filters.matchContacts",
  companies: "filters.matchCompanies",
  deals: "filters.matchDeals",
};

/** The plural noun the results table counts and names its empty state by. */
const UNIT_LABEL: Record<ObjectTab, MessageKey> = {
  contacts: "unit.contacts",
  companies: "unit.companies",
  deals: "unit.deals",
};

/** A resource this screen can address, or the default when the route names none. */
function tabFromRoute(id: string | undefined): ObjectTab {
  return OBJECT_TABS.find((tab) => tab === id) ?? "contacts";
}

export function FiltersScreen({ id }: Readonly<{ id?: string }>) {
  const t = useT();
  const [tab, setTab] = useState<ObjectTab>(tabFromRoute(id));
  // A fresh tree per object, because a clause naming a person's field means
  // nothing on a deal — carrying the tree across would offer the human a filter
  // the new vocabulary refuses.
  const [tree, setTree] = useState<Node>(() => newGroup("and"));

  const resource = RESOURCE_OF[tab];
  const vocabulary = useFilterVocabulary(resource);
  const preview = useFilterPreview(resource, tree);

  const switchTab = (next: ObjectTab) => {
    setTab(next);
    setTree(newGroup("and"));
  };

  return (
    <div className="filters-screen">
      <SectionHeader
        level={1}
        title={t("filters.title")}
        sub={t("filters.subtitle")}
        actions={
          <SegmentedControl
            options={OBJECT_TABS}
            value={tab}
            onChange={switchTab}
            labels={{
              contacts: t(TAB_LABEL.contacts),
              companies: t(TAB_LABEL.companies),
              deals: t(TAB_LABEL.deals),
            }}
            label={t("filters.objectLabel")}
          />
        }
      />

      <Card>
        <SectionHeader
          level={2}
          title={t("filters.builderTitle")}
          actions={
            <span className="filters-count-row">
              <MatchCount
                tab={tab}
                count={preview.data?.match_count}
                stale={preview.isFetching}
              />
              {/* AC-1's live badge: what this filter IS, not what it is doing.
                  A dynamic list recomputes on every event, and that is the
                  property a reader needs before trusting a count at all. */}
              <Badge tone="accent">{t("filters.dynamic")}</Badge>
            </span>
          }
        />
        {vocabulary.isPending && (
          // SurfaceState's loading state is a shimmer with no text, so a reader
          // who cannot see it hears nothing between mount and the first field.
          <span className="sr-only" role="status">
            {t("filters.loadingVocabulary")}
          </span>
        )}
        <SurfaceState
          state={vocabularyState(vocabulary.isPending, vocabulary.isError)}
          emptyLabel={t("filters.noFields")}
        >
          <FilterBuilder
            tree={tree}
            onChange={setTree}
            fields={vocabulary.data?.fields ?? []}
          />
        </SurfaceState>
      </Card>

      {/* Only once something has been asked. An empty table under an unasked
          filter would say "no records match this filter" about a filter nobody
          wrote — the same falsehood the count avoids by showing no number, and
          the reason both read from the same `data === undefined`. */}
      {preview.data !== undefined && (
        <Card>
          <SectionHeader level={2} title={t("filters.resultsTitle")} />
          <FilterResults
            preview={preview.data}
            fields={vocabulary.data?.fields ?? []}
            named={fieldsNamed(tree)}
            unit={t(UNIT_LABEL[tab])}
            // Per object, so switching tabs does not hand a deal's table the
            // widths a reader dragged for a contact's columns.
            widthsKey={`filter-preview-${tab}`}
            pending={preview.isFetching}
          />
        </Card>
      )}
    </div>
  );
}

/**
 * The count, and whether it is behind.
 *
 * Three readings, and keeping them apart is the point. A count the server has
 * answered reads plainly. A count being recomputed reads as the LAST answer,
 * marked stale — not as a spinner, because a number that vanishes on every
 * keystroke is harder to read than one that lags a moment. And a tree with no
 * complete clause has no count at all, which is different from a count of zero:
 * zero means "nothing matches", and this means "you have not asked yet".
 */
function MatchCount({
  tab,
  count,
  stale,
}: Readonly<{ tab: ObjectTab; count: number | undefined; stale: boolean }>) {
  const t = useT();
  if (count === undefined) {
    return (
      <span className="filters-count filters-count-unasked">
        {t("filters.noFilterYet")}
      </span>
    );
  }
  return (
    <span
      className="filters-count"
      // Spoken, because the count changing is the feedback for every edit — a
      // sighted reader sees the number move and a screen-reader user would
      // otherwise get nothing back from adding a clause.
      role="status"
      aria-busy={stale}
      data-stale={stale ? "true" : undefined}
    >
      {t(MATCH_LABEL[tab], { count })}
    </span>
  );
}

/**
 * The vocabulary read's three outcomes as a surface state.
 *
 * An error is `unavailable` rather than `empty`: an empty field list would tell a
 * reader this record type has nothing to filter on, which is a different and false
 * statement — and the endpoint 404s rather than answering an empty set, so a
 * genuine empty cannot arrive.
 */
function vocabularyState(pending: boolean, failed: boolean): SectionState {
  if (pending) {
    return "loading";
  }
  return failed ? "unavailable" : "ready";
}
