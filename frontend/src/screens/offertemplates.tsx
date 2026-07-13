import { api } from "../api/client";
import type { components } from "../api/schema";
import { ifMatch } from "../api/version";
import { Badge, DataTable, SectionHeader } from "../design-system/atoms";
import { useT } from "../i18n";
import { ArchiveAction } from "./archive";
import { problemMessage, throwProblem } from "./common";
import { CreateAction, type CreateField } from "./create";
import { EditAction } from "./edit";
import {
  ListGate,
  type ListPage,
  type ListQuery,
  ListToolbar,
  useListQuery,
} from "./listquery";

type OfferTemplate = components["schemas"]["OfferTemplate"];

async function fetchTemplatesPage(
  query: ListQuery,
  cursor: string | null,
): Promise<ListPage<OfferTemplate>> {
  const { data, error } = await api.GET("/offer-templates", {
    params: {
      query: {
        sort: query.sort || undefined,
        include_archived: query.includeArchived || undefined,
        cursor: cursor || undefined,
        limit: 50,
        ...query.filters,
      },
    },
  });
  if (error) {
    throw new Error(problemMessage(error));
  }
  return {
    data: data.data,
    page: {
      next_cursor: data.page.next_cursor ?? null,
      has_more: data.page.has_more,
    },
  };
}

const LOCALE_OPTIONS = [
  { value: "de-DE", label: "de-DE" },
  { value: "en-US", label: "en-US" },
];

const TEMPLATE_FIELDS: CreateField[] = [
  { key: "name", label: "template.name", required: true },
  {
    key: "locale",
    label: "template.locale",
    type: "select",
    required: true,
    options: LOCALE_OPTIONS,
  },
  {
    key: "is_default",
    label: "template.isDefault",
    type: "select",
    required: true,
    options: [
      { value: "false", label: "false" },
      { value: "true", label: "true" },
    ],
  },
  { key: "header", label: "template.header" },
  { key: "footer", label: "template.footer" },
];

export function OfferTemplatesScreen() {
  const t = useT();
  const list = useListQuery<OfferTemplate>({
    key: "offer-templates",
    fetchPage: fetchTemplatesPage,
    initialSort: "name",
  });

  const createTemplate = async (values: Record<string, string>) => {
    const { data, error } = await api.POST("/offer-templates", {
      body: {
        name: values.name.trim(),
        locale: values.locale || "de-DE",
        is_default: values.is_default === "true",
        layout: {
          header: values.header || undefined,
          footer: values.footer || undefined,
        },
      },
    });
    if (error) {
      throwProblem(error);
    }
    return data;
  };

  const updateTemplate =
    (tpl: OfferTemplate) => async (values: Record<string, unknown>) => {
      // PUT full-replace (unlike product's merge-PATCH): every writable
      // field is supplied on every call — an omitted one would reset it.
      const { data, error } = await api.PUT("/offer-templates/{id}", {
        params: { path: { id: tpl.id }, ...ifMatch(tpl.version) },
        body: {
          name: String(values.name).trim(),
          locale: String(values.locale),
          is_default: values.is_default === "true",
          layout: {
            header: (values.header as string) || undefined,
            footer: (values.footer as string) || undefined,
          },
        },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    };

  return (
    <div className="wrap narrow">
      <SectionHeader
        title={t("template.title")}
        sub={t("template.settingsSub")}
      />
      <div className="list-head">
        <CreateAction
          label={t("template.new")}
          invalidate="offer-templates"
          screen="offer-templates"
          create={createTemplate}
          fields={TEMPLATE_FIELDS}
        />
      </div>
      <ListToolbar
        query={list.query}
        setQuery={list.setQuery}
        searchable={false}
        sortOptions={[{ value: "name", label: "template.sortName" }]}
        filters={[
          {
            kind: "select",
            key: "locale",
            label: "template.localeFilter",
            // @ts-expect-error - locale options have raw string labels, not i18n keys
            options: LOCALE_OPTIONS,
          },
        ]}
      />
      <ListGate state={list} empty={t("template.empty")}>
        {(rows) => (
          <DataTable
            columns={[
              {
                key: "name",
                header: t("template.name"),
                render: (tpl: OfferTemplate) => tpl.name,
              },
              {
                key: "locale",
                header: t("template.locale"),
                render: (tpl: OfferTemplate) => tpl.locale,
              },
              {
                key: "is_default",
                header: t("template.isDefault"),
                render: (tpl: OfferTemplate) =>
                  tpl.is_default ? (
                    <Badge tone="success">{t("template.isDefault")}</Badge>
                  ) : null,
              },
              {
                key: "actions",
                header: "",
                render: (tpl: OfferTemplate) => (
                  <div style={{ display: "flex", gap: 6 }}>
                    <EditAction
                      label={t("template.edit")}
                      invalidate="offer-templates"
                      recordKey="offer-template"
                      record={{
                        ...tpl,
                        is_default: String(tpl.is_default),
                        header:
                          (tpl.layout as Record<string, unknown>).header ?? "",
                        footer:
                          (tpl.layout as Record<string, unknown>).footer ?? "",
                      }}
                      update={updateTemplate(tpl)}
                      fields={TEMPLATE_FIELDS}
                    />
                    <ArchiveAction
                      label={t("template.archive")}
                      confirmText={t("template.archiveConfirm")}
                      invalidate="offer-templates"
                      recordKey="offer-template"
                      onArchived={() => list.refetch()}
                      archive={async () => {
                        const { data, error } = await api.DELETE(
                          "/offer-templates/{id}",
                          { params: { path: { id: tpl.id } } },
                        );
                        if (error) {
                          throwProblem(error);
                        }
                        return data ?? tpl;
                      }}
                    />
                  </div>
                ),
              },
            ]}
            rows={rows}
            rowKey={(tpl) => tpl.id}
          />
        )}
      </ListGate>
    </div>
  );
}
