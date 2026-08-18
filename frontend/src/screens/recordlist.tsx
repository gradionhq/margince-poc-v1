import type { ListColumn } from "../design-system/listtable";
import { formatDateAbbrev } from "../format/format";
import type { useLocale, useT } from "../i18n";
import { RECORD_ZONE } from "./company360";
import { OwnerName } from "./entityref";
import type { ViewSpec } from "./listquery";

/**
 * The columns and views every owner-scoped record list shares (people,
 * companies, leads), defined ONCE.
 *
 * Three lists carried three copies of the Owner column, and when the column
 * was fixed on People and Companies (it had rendered "typed by a person" for
 * every human-captured row) the Leads copy kept the bug. A column that exists
 * on two lists is defined here; a screen adds only what is specific to its
 * record.
 */

/** A row that can be owned and was created at some point. */
export type OwnedRecord = {
  owner_id?: string | null;
  created_at?: string;
};

type Translate = ReturnType<typeof useT>;
type Locale = ReturnType<typeof useLocale>["locale"];

/** The Owner column: whose record this is, sortable by owner_id. */
export function ownerColumn<Row extends OwnedRecord>(
  t: Translate,
): ListColumn<Row> {
  return {
    key: "owner",
    header: t("list.owner"),
    cell: (row) => (
      <OwnerName ownerId={row.owner_id} unowned={t("list.unowned")} />
    ),
    sort: "owner_id",
  };
}

/** The Created column, in the record zone, sortable by created_at. */
export function createdColumn<Row extends OwnedRecord>(
  t: Translate,
  locale: Locale,
): ListColumn<Row> {
  return {
    key: "created",
    header: t("list.created"),
    cell: (row) => (
      <span className="t-caption">
        {row.created_at
          ? formatDateAbbrev(row.created_at, locale, RECORD_ZONE)
          : ""}
      </span>
    ),
    sort: "created_at",
  };
}

/**
 * The views every record list opens with: everything newest-first, and —
 * for a signed-in reader — theirs. A screen appends its own (A–Z on its
 * name column, lifecycle cuts, score cuts).
 */
export function standardViews(
  viewerId: string | undefined,
): readonly ViewSpec[] {
  const views: ViewSpec[] = [{ label: "list.viewAll", sort: "-created_at" }];
  if (viewerId) {
    views.push({
      label: "list.viewMine",
      sort: "-created_at",
      filters: { owner_id: viewerId },
    });
  }
  return views;
}
