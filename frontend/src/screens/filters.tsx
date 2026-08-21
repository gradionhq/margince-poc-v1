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
import { QueryStates } from "./common";
import { FilterBuilder } from "./filterbuilder";
import {
  type FilterResource,
  useFilterPreview,
  useFilterVocabulary,
  type VocabularyField,
} from "./filterdata";
import { ExportFilterMenu } from "./filterexport";
import { SaveFilterListAction } from "./filterlist";
import { FilterResults } from "./filterresults";
import "./filters.css";
import {
  LoadFilterViewMenu,
  SaveFilterViewAction,
  type ViewResource,
} from "./savedviews";
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

/**
 * The same three objects again, as `/views` spells them.
 *
 * A third spelling, and it is not a mistake to fix here: `/filters/*` takes
 * `person` and `/views` takes `people`, both enumerated in the contract. So this
 * screen is where the two vocabularies meet, and the correspondence is written
 * down once — beside `RESOURCE_OF`, so a reader sees both mappings together —
 * rather than derived at each call site by adding an "s".
 */
const VIEW_OF: Record<ObjectTab, ViewResource> = {
  contacts: "people",
  companies: "organizations",
  deals: "deals",
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
    <div className="filters-screen arrive-stack">
      {/* The object control alone. The page's name and its subtitle belong to
          the shell's page head, which names every rail destination — a screen
          that printed them again would put two page titles in one document, and
          a reader navigating by heading could not tell which was the page. */}
      <div className="filters-object-row">
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
      </div>

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
                failed={preview.isError}
              />
              {/* AC-1's live badge: what this filter IS, not what it is doing.
                  A dynamic list recomputes on every event, and that is the
                  property a reader needs before trusting a count at all. */}
              <Badge tone="accent">{t("filters.dynamic")}</Badge>
              <LoadFilterViewMenu resource={VIEW_OF[tab]} onLoad={setTree} />
              <SaveFilterViewAction resource={VIEW_OF[tab]} tree={tree} />
              {/* Beside save-as-view, because they are the two things a reader
                  can do with a finished filter and hiding one of them behind a
                  menu makes the pair look like one choice. The list takes the
                  FILTER vocabulary's word for the object — `/lists` enumerates
                  `person`, like the preview it must agree with. */}
              <SaveFilterListAction resource={resource} tree={tree} />
            </span>
          }
        />
        <SurfaceState
          state={vocabularyState(vocabulary.isPending, vocabulary.isError)}
          emptyLabel={t("filters.noFields")}
          loadingLabel={t("filters.loadingVocabulary")}
          // The builder that lands here is a condition row plus its verbs.
          loadingLines={4}
        >
          <FilterBuilder
            tree={tree}
            onChange={setTree}
            fields={vocabulary.data?.fields ?? []}
          />
        </SurfaceState>
        {/* Below the builder, not in the header beside the count: the export
            takes the filter as its argument, so it belongs after the thing it
            reads — and a refusal is a sentence, which the header row has no
            width for. It also takes the FILTER vocabulary's word for the object
            rather than the view rail's, because `/exports` enumerates `person`,
            the same as the preview it has to agree with. */}
        <div className="filters-export-row">
          <ExportFilterMenu resource={resource} tree={tree} />
        </div>
      </Card>

      <PreviewSection
        preview={preview}
        tab={tab}
        fields={vocabulary.data?.fields ?? []}
        named={fieldsNamed(tree)}
      />
    </div>
  );
}

/**
 * The rows behind the count, the reason there are none, or nothing at all.
 *
 * Three outcomes and they are three different statements, which is why the
 * absent one cannot stand in for the other two. Nothing at all, while no
 * complete clause has been written: an empty table there would say "no records
 * match this filter" about a filter nobody wrote. The rows, once the server has
 * answered. And a failure with its reason and a retry, when the preview was
 * refused — a read seat is refused every `POST /filters/preview` before RBAC is
 * consulted, so a reader who may read the vocabulary and build a clause can
 * still never get a count, and the reason is the only thing that tells them so.
 * The header row beside the count has no width for a sentence, so it lands here.
 */
function PreviewSection({
  preview,
  tab,
  fields,
  named,
}: Readonly<{
  preview: ReturnType<typeof useFilterPreview>;
  tab: ObjectTab;
  fields: readonly VocabularyField[];
  named: readonly string[];
}>) {
  const t = useT();
  if (preview.isError) {
    return (
      <Card>
        <SectionHeader level={2} title={t("filters.resultsTitle")} />
        {/* The house spelling of a failed read: the headline and the server's
            own cause in one live region, with the retry beside it. */}
        <QueryStates query={preview}>{null}</QueryStates>
      </Card>
    );
  }
  if (preview.data === undefined) {
    return null;
  }
  return (
    <Card>
      <SectionHeader level={2} title={t("filters.resultsTitle")} />
      <FilterResults
        preview={preview.data}
        fields={fields}
        named={named}
        unit={t(UNIT_LABEL[tab])}
        // Per object, so switching tabs does not hand a deal's table the
        // widths a reader dragged for a contact's columns.
        widthsKey={`filter-preview-${tab}`}
        pending={preview.isFetching}
      />
    </Card>
  );
}

/**
 * The count, and whether it is behind.
 *
 * Four readings, and keeping them apart is the point. A count the server has
 * answered reads plainly. A count being recomputed reads as the LAST answer,
 * marked stale — not as a spinner, because a number that vanishes on every
 * keystroke is harder to read than one that lags a moment. A tree with no
 * complete clause has no count at all, which is different from a count of zero:
 * zero means "nothing matches", and this means "you have not asked yet". And a
 * count the server was asked for and refused says exactly that.
 *
 * The refusal outranks the other three. It is read first because the previous
 * answer survives a failed refetch, so a stale number would otherwise be
 * presented as current, and because "you have not asked yet" over a finished
 * clause blames the reader for the server's refusal.
 */
function MatchCount({
  tab,
  count,
  stale,
  failed,
}: Readonly<{
  tab: ObjectTab;
  count: number | undefined;
  stale: boolean;
  failed: boolean;
}>) {
  const t = useT();
  if (failed) {
    // Silent: the results card below carries the reason in an assertive live
    // region, and announcing the same failure twice fragments it.
    return (
      <span className="filters-count filters-count-failed">
        {t("filters.countUnavailable")}
      </span>
    );
  }
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
