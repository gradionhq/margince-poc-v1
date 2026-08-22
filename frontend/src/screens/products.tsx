import { api } from "../api/client";
import type { components } from "../api/schema";
import { ifMatch, requireVersion } from "../api/version";
import { useCanWrite } from "../app/capability";
import { Badge } from "../design-system/atoms";
import type { ListColumn } from "../design-system/listtable";
import { Panel, PanelBody } from "../design-system/panel";
import { formatMoney } from "../format/format";
import { toMajorUnits, toMinorUnits } from "../format/minorunits";
import { useLocale, useT } from "../i18n";
import { ArchiveAction } from "./archive";
import { throwProblem, useMe } from "./common";
import { CreateAction, type CreateField } from "./create";
import { EditAction } from "./edit";
import {
  type ListPage,
  type ListQuery,
  ListTable,
  listFetchLimit,
  useListQuery,
} from "./listquery";
import "./listsection.css";

type Product = components["schemas"]["Product"];

async function fetchProductsPage(
  query: ListQuery,
  cursor: string | null,
): Promise<ListPage<Product>> {
  const { data, error } = await api.GET("/products", {
    params: {
      query: {
        q: query.q || undefined,
        sort: query.sort || undefined,
        include_archived: query.includeArchived || undefined,
        cursor: cursor || undefined,
        limit: listFetchLimit(query.perPage),
        ...query.filters,
      },
    },
  });
  if (error) {
    throwProblem(error);
  }
  return {
    data: data.data,
    page: {
      next_cursor: data.page.next_cursor ?? null,
      has_more: data.page.has_more,
    },
  };
}

// Phase-3 line-item product picker reuses this list read, mapped to {id,name}.
export async function searchProductCandidates(
  q: string,
): Promise<{ id: string; name: string }[]> {
  const { data, error } = await api.GET("/products", {
    params: { query: { q, limit: 10 } },
  });
  if (error) {
    throwProblem(error);
  }
  return data.data.map((p) => ({ id: p.id, name: p.name }));
}

const PRODUCT_FIELDS: CreateField[] = [
  { key: "name", label: "product.name", required: true },
  { key: "sku", label: "product.sku" },
  { key: "description", label: "product.description" },
  { key: "unit", label: "product.unit", placeholder: "day" },
  {
    key: "unit_price",
    label: "product.unitPrice",
    type: "number",
    required: true,
  },
  {
    key: "currency",
    label: "product.currency",
    type: "select",
    required: true,
    options: ["EUR", "USD", "GBP", "CHF"].map((c) => ({ value: c, label: c })),
  },
  { key: "default_tax_rate", label: "product.taxRate", type: "number" },
];

// text narrows one value off the edit form's Record<string, unknown> without
// asserting: a field the reader left alone is absent, not an empty string, and
// an assertion would hand `undefined` to a scale that then silently defaults.
function text(value: unknown): string {
  return typeof value === "string" ? value : "";
}

// Major-unit price string -> integer minor units (P11: no float money on the
// wire), at the scale the PRODUCT's own currency carries. A hard-coded hundred
// priced a yen product at a hundredth and a dinar product at ten times.
function toMinor(major: string | undefined, currency: string): number {
  return toMinorUnits(Number(major ?? "0"), currency);
}

/**
 * The priced things an offer line can point at, as a section of Settings → Data
 * model.
 *
 * Not a screen of its own: it was one, reached by a card that existed only to
 * send you here, and a door is not a section. So it renders no `.wrap` — the
 * settings page owns the reading column — and names itself, since the shell's
 * page title now belongs to the whole data-model page.
 *
 * A Panel around the list surface rather than a bare heading above it: the two
 * surfaces beside it on this tab are cards, and a section whose name floats on
 * the page ground while its neighbours' names sit inside their cards reads as
 * two different kinds of thing on one page.
 */
export function ProductsAdmin() {
  const t = useT();
  const { locale } = useLocale();
  // One grant per affordance, each named for the request it actually issues:
  // New POSTs, Edit PATCHes, Archive DELETEs. `useCanWrite` rather than `useCan`
  // because all three mutate, and the licensing seat is clamped on the HTTP
  // method before RBAC is consulted — a read seat holding product:update would
  // otherwise be handed an editor every save bounces off.
  const me = useMe();
  const canCreate = useCanWrite("product", "create");
  const canUpdate = useCanWrite("product", "update");
  const canArchive = useCanWrite("product", "delete");
  const list = useListQuery<Product>({
    key: "products",
    fetchPage: fetchProductsPage,
    initialSort: "name",
  });

  const createProduct = async (values: Record<string, string>) => {
    const { data, error } = await api.POST("/products", {
      body: {
        name: values.name.trim(),
        sku: values.sku?.trim() || null,
        description: values.description?.trim() || null,
        unit: values.unit?.trim() || null,
        unit_price_minor: toMinor(values.unit_price, values.currency || "EUR"),
        currency: values.currency || "EUR",
        default_tax_rate: values.default_tax_rate
          ? Number(values.default_tax_rate)
          : null,
        source: "manual",
      },
    });
    if (error) {
      throwProblem(error);
    }
    return data;
  };

  // `sku`/`description` are nullable contract fields, so an emptied form
  // input sends an explicit `null` to clear the stored value. `unit`/
  // `currency` are non-nullable, so an emptied input instead sends
  // `undefined` (omitted from the PATCH body) to leave the existing value
  // unchanged rather than overwrite it with an invalid empty string.
  const updateProduct =
    (product: Product) => async (values: Record<string, unknown>) => {
      const { data, error } = await api.PATCH("/products/{id}", {
        params: {
          path: { id: product.id },
          ...ifMatch(requireVersion(product.version)),
        },
        body: {
          name: String(values.name).trim(),
          sku: (values.sku as string)?.trim() || null,
          description: (values.description as string)?.trim() || null,
          unit: (values.unit as string)?.trim() || undefined,
          // The currency the form carries, falling back to the product's
          // stored one when the field was left alone — the same value the
          // PATCH below sends, so the amount and its scale cannot disagree.
          //
          // Narrowed rather than asserted: this callback is handed
          // Record<string, unknown>, and `as string` on a value that turns out
          // to be undefined would scale the price at the two-digit default
          // without anything failing.
          unit_price_minor: toMinor(
            text(values.unit_price),
            text(values.currency) || product.currency,
          ),
          currency: text(values.currency) || undefined,
          default_tax_rate: values.default_tax_rate
            ? Number(values.default_tax_rate)
            : undefined,
        },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    };

  // The per-row affordances, in a column that exists only while at least one of
  // them does. Keeping the column and emptying its cells would leave a headed,
  // width-taking, column-picker-listed strip of nothing, which reads as a table
  // that lost its buttons rather than as a permission — so the column goes,
  // exactly as the custom-fields table drops its own.
  const rowActions: ListColumn<Product> = {
    key: "actions",
    header: t("table.actions"),
    // Two labelled buttons, not a value: the column is sized by the verbs in
    // it rather than by a share of a 720px settings column, which is what had
    // this cell handing the reader half an "Edit product" and a red sliver of
    // "Archive product" (listtable.tsx, COLUMN_SIZES.verbs).
    verbs: true,
    cell: (p: Product) => (
      <div className="listsection-rowverbs">
        {canUpdate && (
          <EditAction
            label={t("product.edit")}
            invalidate="products"
            recordKey="product"
            record={{
              ...p,
              unit_price: String(toMajorUnits(p.unit_price_minor, p.currency)),
            }}
            update={updateProduct(p)}
            fields={PRODUCT_FIELDS}
          />
        )}
        {canArchive && (
          <ArchiveAction
            label={t("product.archive")}
            confirmText={t("product.archiveConfirm")}
            invalidate="products"
            recordKey="product"
            onArchived={() => list.refetch()}
            archive={async () => {
              const { data, error } = await api.DELETE("/products/{id}", {
                params: { path: { id: p.id } },
              });
              if (error) {
                throwProblem(error);
              }
              return data ?? p;
            }}
          />
        )}
      </div>
    ),
  };

  return (
    <Panel className="listsection" title={t("product.title")}>
      <PanelBody className="listsection-intro">
        <p className="settings-panel-sub">{t("product.settingsSub")}</p>
        {/* A reader who holds no write verb here sees a list with no editor, and
            silence about why is a claim that the list has none. The posture is
            stated ONCE for the whole section (design-system README, "Absent,
            disabled, or withheld"); a reader who can edit but not archive needs no
            page-level notice, because the affordances they do hold say what they
            may do. */}
        {/* me.isSuccess first: every capability hook fails CLOSED while the
            probe is in flight, so branching on the grants alone flashes a
            read-only notice at the admin who holds all three. Gate on the
            probe, not on its absence. */}
        {me.isSuccess && !canCreate && !canUpdate && !canArchive && (
          <p className="settings-panel-sub">{t("product.readOnly")}</p>
        )}
      </PanelBody>
      <ListTable
        state={list}
        unit="unit.products"
        action={
          canCreate ? (
            <CreateAction
              label={t("product.new")}
              invalidate="products"
              // `stay` because a fresh product has no page of its own to
              // open: the list the row joins is already on screen, and this
              // panel is one of the settings surfaces the screen names.
              screen="settings"
              stay
              create={createProduct}
              fields={PRODUCT_FIELDS}
            />
          ) : undefined
        }
        columns={[
          {
            key: "name",
            header: t("product.name"),
            sort: "name",
            cell: (p: Product) => p.name,
            fixed: true,
          },
          {
            key: "sku",
            header: t("product.sku"),
            sort: "sku",
            cell: (p: Product) => p.sku ?? "",
          },
          {
            key: "price",
            header: t("product.unitPrice"),
            sort: "unit_price_minor",
            cell: (p: Product) => (
              <span className="t-mono">
                {formatMoney(p.unit_price_minor, p.currency, locale)}
              </span>
            ),
            numeric: true,
          },
          {
            key: "active",
            header: t("product.active"),
            sort: "active",
            cell: (p: Product) =>
              p.archived_at ? (
                <Badge tone="danger">{t("product.archived")}</Badge>
              ) : p.active ? (
                <Badge tone="success">{t("product.active")}</Badge>
              ) : (
                <Badge>{t("product.inactive")}</Badge>
              ),
          },
          ...(canUpdate || canArchive ? [rowActions] : []),
        ]}
        rowKey={(p) => p.id}
        chips={[
          {
            key: "active",
            label: "product.activeFilter",
            allLabel: "product.activeFilterAll",
            options: [{ value: "true", label: "product.active" }],
          },
        ]}
      />
    </Panel>
  );
}
