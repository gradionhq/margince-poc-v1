import { api } from "../api/client";
import type { components } from "../api/schema";
import { ifMatch } from "../api/version";
import { useCanWrite } from "../app/capability";
import { Badge } from "../design-system/atoms";
import type { ListColumn } from "../design-system/listtable";
import { Panel, PanelBody } from "../design-system/panel";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { ArchiveAction } from "./archive";
import { throwProblem } from "./common";
import { CreateAction, type CreateField } from "./create";
import { EditAction } from "./edit";
import {
  type ListPage,
  type ListQuery,
  ListTable,
  useListQuery,
} from "./listquery";
import "./listsection.css";

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

const LOCALE_OPTIONS = [
  { value: "de-DE", label: "de-DE" },
  { value: "en-US", label: "en-US" },
];

const LOCALE_FILTER_OPTIONS: { value: string; label: MessageKey }[] = [
  { value: "de-DE", label: "template.localeDE" },
  { value: "en-US", label: "template.localeEN" },
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

/**
 * The offer shells an offer is built from, as a section of Settings → Data model.
 *
 * Not a screen of its own, for the same reason as {@link ProductsAdmin}: it was
 * reached by a card that existed only to send you here. It renders no `.wrap` —
 * the settings page owns the reading column — and names itself in a Panel, the
 * same shape products takes, since the shell's page title now belongs to the
 * whole data-model page and the surfaces on it have to read as one kind of thing.
 */
export function OfferTemplatesAdmin() {
  const t = useT();
  // One grant per affordance, named for the request it issues: New POSTs, Edit
  // PUTs, Archive DELETEs. `useCanWrite` rather than `useCan` because all three
  // mutate, and the licensing seat is clamped on the HTTP method before RBAC is
  // reached — a read seat holding offer_template:update would otherwise open an
  // editor whose every save is refused.
  const canCreate = useCanWrite("offer_template", "create");
  const canUpdate = useCanWrite("offer_template", "update");
  const canArchive = useCanWrite("offer_template", "delete");
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

  // The per-row affordances, in a column that exists only while at least one of
  // them does: an emptied actions column would still take width, carry a header
  // and appear in the column picker, which reads as a table that lost its
  // buttons rather than as a permission.
  const rowActions: ListColumn<OfferTemplate> = {
    key: "actions",
    header: t("table.actions"),
    cell: (tpl: OfferTemplate) => (
      <div style={{ display: "flex", gap: "var(--space-2)" }}>
        {canUpdate && (
          <EditAction
            label={t("template.edit")}
            invalidate="offer-templates"
            recordKey="offer-template"
            record={{
              ...tpl,
              is_default: String(tpl.is_default),
              // layout is already an index signature on the contract type, so
              // its members read straight off it — a cast here would only be
              // re-asserting what the schema states.
              header: tpl.layout.header ?? "",
              footer: tpl.layout.footer ?? "",
            }}
            update={updateTemplate(tpl)}
            fields={TEMPLATE_FIELDS}
          />
        )}
        {canArchive && (
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
        )}
      </div>
    ),
  };

  return (
    <Panel className="listsection" title={t("template.title")}>
      <PanelBody className="listsection-intro">
        <p className="t-caption">{t("template.settingsSub")}</p>
        {/* Stated once for the whole section (design-system README, "Absent,
            disabled, or withheld"): a readable list whose editors are all withheld
            has to say so, or their absence reads as a claim about the list rather
            than about authority. A reader holding one write verb needs no notice —
            the affordance they keep says what they may do. */}
        {!canCreate && !canUpdate && !canArchive && (
          <p className="t-caption">{t("template.readOnly")}</p>
        )}
      </PanelBody>
      <ListTable
        state={list}
        unit="unit.offerTemplates"
        searchable={false}
        action={
          canCreate ? (
            <CreateAction
              label={t("template.new")}
              invalidate="offer-templates"
              screen="offer-templates"
              create={createTemplate}
              fields={TEMPLATE_FIELDS}
            />
          ) : undefined
        }
        columns={[
          {
            key: "name",
            header: t("template.name"),
            sort: "name",
            cell: (tpl: OfferTemplate) => tpl.name,
            fixed: true,
          },
          {
            key: "locale",
            header: t("template.locale"),
            sort: "locale",
            cell: (tpl: OfferTemplate) => tpl.locale,
          },
          {
            key: "is_default",
            header: t("template.isDefault"),
            sort: "is_default",
            cell: (tpl: OfferTemplate) =>
              tpl.is_default ? (
                <Badge tone="success">{t("template.isDefault")}</Badge>
              ) : null,
          },
          ...(canUpdate || canArchive ? [rowActions] : []),
        ]}
        rowKey={(tpl) => tpl.id}
        chips={[
          {
            key: "locale",
            label: "template.localeFilter",
            allLabel: "template.localeFilterAll",
            options: LOCALE_FILTER_OPTIONS,
          },
        ]}
      />
    </Panel>
  );
}
