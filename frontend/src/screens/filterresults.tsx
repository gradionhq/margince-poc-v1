// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The preview's first page as a table (AC-filters-and-views-5, the reading half).
//
// It renders through the catalog's ListTable rather than a grid of its own. That
// was worth checking rather than assuming: ListTable reads as a record-list
// component, but its column type is generic over the row — key, header, and a cell
// function — so a schema-derived projection fits it without pretending to be a
// record. A second table beside it would have been the easy mistake, and
// frontend/CLAUDE.md records that this tree has twice grown a duplicate surface
// exactly that way.
//
// What this file decides is WHICH columns to show, because the preview carries
// every non-generated column of the table and thirty columns is not a table
// anybody reads.

import { type ListColumn, ListTable } from "../design-system/listtable";
import { useT } from "../i18n";
import {
  type FilterPreview,
  fieldLabel,
  type VocabularyField,
} from "./filterdata";

/** A preview row: schema-derived, so its values span every type the table holds. */
type PreviewRow = Record<string, unknown>;

/**
 * How many columns a preview opens with, before the column picker is used.
 *
 * A glance, not a report — the same reasoning that bounds the page. ListTable
 * starts with every column it is given visible, so this is the number that
 * decides whether a reader sees a table or a horizontal scrollbar.
 */
const MAX_COLUMNS = 6;

/**
 * Which column names a row, in preference order.
 *
 * `id` is last rather than first: a UUID identifies a row to the database and to
 * nobody else, so a human-readable name wins wherever the record type has one.
 */
const IDENTITY_PREFERENCE = [
  "full_name",
  "display_name",
  "name",
  "title",
  "id",
] as const;

/**
 * The columns most likely to answer "is this filter right?": what identifies the
 * row, then the fields the filter actually asked about.
 *
 * Deriving them from the filter is the point. AC-5 names five product columns for
 * contacts, and inventing my own five for each of the other record types would be
 * a list nobody agreed to; the fields a human just filtered on are the ones they
 * are checking, and that rule needs no product decision to be defensible. The
 * identity column leads because a row you cannot name is not a result you can act
 * on, and the projection's own order fills whatever is left.
 */
export function previewColumnNames(
  available: readonly string[],
  named: readonly string[],
): string[] {
  const identity = IDENTITY_PREFERENCE.find((candidate) =>
    available.includes(candidate),
  );
  const chosen: string[] = identity ? [identity] : [];
  const take = (field: string) => {
    // A filter may name a field the projection does not carry — a tag leaf reads
    // a join, not a column — so membership is checked rather than assumed.
    if (available.includes(field) && !chosen.includes(field)) {
      chosen.push(field);
    }
  };
  for (const field of named) {
    if (chosen.length >= MAX_COLUMNS) {
      return chosen;
    }
    take(field);
  }
  // A filter naming nothing the projection carries would otherwise leave a table
  // of one column, so the remaining slots fill in the projection's order —
  // except for the primary key, which is worth a column only when it is the ONLY
  // thing that names the row. A UUID beside a name it already read is a column
  // spent on nothing.
  for (const field of available) {
    if (chosen.length >= MAX_COLUMNS) {
      break;
    }
    if (field !== "id") {
      take(field);
    }
  }
  return chosen;
}

export type FilterResultsProps = Readonly<{
  preview: FilterPreview | undefined;
  fields: readonly VocabularyField[];
  /** The fields the filter names, in the order they were written. */
  named: readonly string[];
  /** The plural noun for these rows — "contacts". Translated by the caller. */
  unit: string;
  /** Names this table for the column widths it remembers between visits. */
  widthsKey: string;
  pending: boolean;
}>;

export function FilterResults({
  preview,
  fields,
  named,
  unit,
  widthsKey,
  pending,
}: FilterResultsProps) {
  const t = useT();
  const rows: readonly PreviewRow[] = preview?.rows ?? [];
  const names = previewColumnNames(preview?.columns ?? [], named);
  const columns: ListColumn<PreviewRow>[] = names.map((name, index) => ({
    key: name,
    header: headerFor(name, fields),
    // The identity column is fixed: it is what makes a row recognisable, so the
    // picker may not hide it and the phone layout promotes it to the card's
    // heading.
    fixed: index === 0,
    cell: (row) => cellText(row[name]),
  }));

  return (
    <ListTable<PreviewRow>
      rows={rows}
      columns={columns}
      rowKey={rowKey}
      unit={unit}
      widthsKey={widthsKey}
      emptyNote={t("filters.noMatches")}
      pending={pending}
      caption={t("filters.resultsCaption")}
      // The preview is deliberately a first page, and the reader's next step is
      // to narrow the filter rather than to walk pages of it — so there is no
      // pager to offer. Claiming `hasMore` without an `onLoadMore` would render
      // a control that does nothing.
      hasMore={false}
    />
  );
}

/**
 * A row's React key.
 *
 * Every table this endpoint previews has an `id`, so the fallback is here for the
 * type rather than for a case that arrives — and it falls back to the row's own
 * contents rather than to an index, because an index key makes React reuse the
 * wrong row when the result set shifts under a recount.
 */
function rowKey(row: PreviewRow): string {
  return typeof row.id === "string" ? row.id : JSON.stringify(row);
}

/**
 * A column's header: the admin's word for a custom field, the column's own name
 * for a core one — the same reading the field picker gives, so a clause and the
 * column it selects call the field by one name.
 */
function headerFor(name: string, fields: readonly VocabularyField[]): string {
  const known = fields.find((field) => field.name === name);
  return known ? fieldLabel(known) : name.replaceAll("_", " ");
}

/**
 * One cell, as text.
 *
 * A null reads as a dash rather than as blank, because an empty cell cannot be
 * told apart from a column that failed to render — and "nothing here" is exactly
 * what a reader checking an `exists` clause needs to see. An object is
 * stringified rather than dropped: a jsonb column is rare in a preview, and
 * showing its shape beats showing nothing.
 */
function cellText(value: unknown): string {
  if (value === null || value === undefined) {
    return "—";
  }
  if (typeof value === "object") {
    return JSON.stringify(value);
  }
  return String(value);
}
