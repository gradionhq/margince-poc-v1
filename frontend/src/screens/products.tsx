import { api } from "../api/client";
import type { components } from "../api/schema";
import { ifMatch } from "../api/version";
import { Badge, DataTable, SectionHeader } from "../design-system/atoms";
import { formatMoney } from "../format/format";
import { useLocale, useT } from "../i18n";
import { ArchiveAction } from "./archive";
import { problemMessage, throwProblem } from "./common";
import { CreateAction, type CreateField } from "./create";
import { EditAction } from "./edit";
import { ListGate, type ListPage, type ListQuery, ListToolbar, useListQuery } from "./listquery";

type Product = components["schemas"]["Product"];

async function fetchProductsPage(query: ListQuery, cursor: string | null): Promise<ListPage<Product>> {
  const { data, error } = await api.GET("/products", {
    params: { query: {
      q: query.q || undefined,
      sort: query.sort || undefined,
      include_archived: query.includeArchived || undefined,
      cursor: cursor || undefined,
      limit: 50,
      ...query.filters,
    } },
  });
  if (error) { throw new Error(problemMessage(error)); }
  return { data: data.data, page: { next_cursor: data.page.next_cursor ?? null, has_more: data.page.has_more } };
}

// Phase-3 line-item product picker reuses this list read, mapped to {id,name}.
export async function searchProductCandidates(q: string): Promise<{ id: string; name: string }[]> {
  const { data, error } = await api.GET("/products", { params: { query: { q, limit: 10 } } });
  if (error) { throwProblem(error); }
  return data.data.map((p) => ({ id: p.id, name: p.name }));
}

const PRODUCT_FIELDS: CreateField[] = [
  { key: "name", label: "product.name", required: true },
  { key: "sku", label: "product.sku" },
  { key: "description", label: "product.description" },
  { key: "unit", label: "product.unit", placeholder: "day" },
  { key: "unit_price", label: "product.unitPrice", type: "number", required: true },
  { key: "currency", label: "product.currency", type: "select", required: true,
    options: ["EUR", "USD", "GBP", "CHF"].map((c) => ({ value: c, label: c })) },
  { key: "default_tax_rate", label: "product.taxRate", type: "number" },
];

// Major-unit price string -> integer minor units (P11: no float money on the wire).
function toMinor(major: string | undefined): number {
  return Math.round(Number(major ?? "0") * 100);
}

export function ProductsScreen() {
  const t = useT();
  const { locale } = useLocale();
  const list = useListQuery<Product>({ key: "products", fetchPage: fetchProductsPage, initialSort: "name" });

  const createProduct = async (values: Record<string, string>) => {
    const { data, error } = await api.POST("/products", {
      body: {
        name: values.name.trim(),
        sku: values.sku?.trim() || null,
        description: values.description?.trim() || null,
        unit: values.unit?.trim() || null,
        unit_price_minor: toMinor(values.unit_price),
        currency: values.currency || "EUR",
        default_tax_rate: values.default_tax_rate ? Number(values.default_tax_rate) : null,
        source: "manual",
      },
    });
    if (error) { throwProblem(error); }
    return data;
  };

  const updateProduct = (product: Product) => async (values: Record<string, unknown>) => {
    const { data, error } = await api.PATCH("/products/{id}", {
      params: { path: { id: product.id }, ...ifMatch(product.version) },
      body: {
        name: String(values.name).trim(),
        sku: (values.sku as string)?.trim() || null,
        description: (values.description as string)?.trim() || null,
        unit: (values.unit as string)?.trim() || undefined,
        unit_price_minor: toMinor(values.unit_price as string),
        currency: (values.currency as string) || undefined,
        default_tax_rate: values.default_tax_rate ? Number(values.default_tax_rate) : undefined,
      },
    });
    if (error) { throwProblem(error); }
    return data;
  };

  return (
    <div className="wrap narrow">
      <SectionHeader title={t("product.title")} sub={t("product.settingsSub")} />
      <div className="list-head">
        <CreateAction
          label={t("product.new")}
          invalidate="products"
          screen="products"
          create={createProduct}
          fields={PRODUCT_FIELDS}
        />
      </div>
      <ListToolbar
        query={list.query}
        setQuery={list.setQuery}
        sortOptions={[
          { value: "name", label: "product.sortName" },
          { value: "-created_at", label: "product.sortCreated" },
        ]}
        filters={[{ kind: "select", key: "active", label: "product.activeFilter",
          options: [{ value: "true", label: "product.active" }] }]}
      />
      <ListGate state={list} empty={t("product.empty")}>
        {(rows) => (
          <DataTable
            columns={[
              { key: "name", header: t("product.name"), render: (p: Product) => p.name },
              { key: "sku", header: t("product.sku"), render: (p: Product) => p.sku ?? "" },
              { key: "price", header: t("product.unitPrice"),
                render: (p: Product) => (<span className="t-mono">{formatMoney(p.unit_price_minor, p.currency, locale)}</span>) },
              { key: "active", header: t("product.active"),
                render: (p: Product) => (p.archived_at ? <Badge tone="danger">–</Badge> : p.active ? <Badge tone="success">✓</Badge> : <Badge>–</Badge>) },
              { key: "actions", header: "",
                render: (p: Product) => (
                  <div style={{ display: "flex", gap: 6 }}>
                    <EditAction
                      label={t("product.edit")}
                      invalidate="products"
                      recordKey="product"
                      record={{ ...p, unit_price: String((p.unit_price_minor / 100).toFixed(2)) }}
                      update={updateProduct(p)}
                      fields={PRODUCT_FIELDS}
                    />
                    <ArchiveAction
                      label={t("product.archive")}
                      confirmText={t("product.archiveConfirm")}
                      invalidate="products"
                      recordKey="product"
                      onArchived={() => list.refetch()}
                      archive={async () => {
                        const { data, error } = await api.DELETE("/products/{id}", { params: { path: { id: p.id } } });
                        if (error) { throwProblem(error); }
                        return data ?? p;
                      }}
                    />
                  </div>
                ) },
            ]}
            rows={rows}
            rowKey={(p) => p.id}
          />
        )}
      </ListGate>
    </div>
  );
}
