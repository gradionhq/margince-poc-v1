import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Calendar,
  Euro,
  Hash,
  List,
  type LucideIcon,
  ToggleRight,
  Type,
} from "lucide-react";
import { useId, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import {
  Badge,
  Button,
  DataTable,
  EmptyState,
  Modal,
  SectionHeader,
  TextInput,
} from "../design-system/atoms";
import { useT } from "../i18n";
import {
  canManageCustomFields,
  problemMessage,
  QueryGate,
  useMe,
} from "./common";
import {
  apiKey,
  CF_OBJECTS,
  CF_TYPES,
  type CfObject,
  type CfType,
  columnName,
  ddlPreview,
  looksStructural,
  slug,
} from "./customfields.logic";
import "./customfields.css";

// The add-field builder (AC-custom-fields-3..5/8): a governed form that turns a
// human's plain label into one typed scalar column on an existing object. The
// immutable cf_-prefixed API key and the pending DDL are shown before Confirm so
// the schema change is legible, a structural-sounding label is refused up front,
// and the 🟡 gate states that Confirm writes a live column + an audit row. This
// is NOT the ApprovalGate (Accept/Edit/Dismiss triad) — it is a local .cf-gate
// preview block owned by this screen.

// One glyph per scalar type, mirrored from the mockup's field icons and type
// chips so a field's shape reads at a glance. Decorative only — every use is
// aria-hidden, so the accessible name stays the translated type word.
const TYPE_ICON: Record<CfType, LucideIcon> = {
  text: Type,
  number: Hash,
  date: Calendar,
  currency: Euro,
  picklist: List,
  boolean: ToggleRight,
};

export type NewFieldDraft = {
  object: CfObject;
  label: string;
  type: CfType;
  currency: string;
  options: string[];
};

export function FieldBuilder({
  object,
  pending,
  onSubmit,
  onToast,
}: Readonly<{
  object: CfObject;
  pending: boolean;
  onSubmit: (draft: NewFieldDraft) => void;
  onToast: (msg: string) => void;
}>) {
  const t = useT();
  const ids = { label: useId(), key: useId(), currency: useId() };
  const [label, setLabel] = useState("");
  const [type, setType] = useState<CfType>("text");
  const [currency, setCurrency] = useState("EUR");
  const [options, setOptions] = useState<string[]>([""]);
  const structural = looksStructural(label);
  const canConfirm = !pending && label.trim().length > 0 && !structural;

  const setOptionAt = (idx: number, value: string) => {
    setOptions((current) => current.map((opt, i) => (i === idx ? value : opt)));
  };

  const removeOption = (idx: number) => {
    // A picklist without an option is not a picklist — the last row is a floor,
    // not a delete target, so the intent is surfaced as a toast, not swallowed.
    if (options.length <= 1) {
      onToast(t("cf.lastOptionBlocked"));
      return;
    }
    setOptions((current) => current.filter((_, i) => i !== idx));
  };

  const confirm = () => {
    if (!canConfirm) {
      return;
    }
    onSubmit({ object, label: label.trim(), type, currency, options });
  };

  return (
    <section
      className="card cf-builder"
      aria-label={t("cf.builder.addTo", { object })}
    >
      <header className="cf-builder-head">
        <h3 className="section-header">{t("cf.builder.addTo", { object })}</h3>
        <span className="badge">{t("cf.builder.noCode")}</span>
      </header>
      <p className="cf-hint">{t("cf.builder.intro")}</p>

      <div className="cf-grid">
        <div className="field">
          <label className="t-label" htmlFor={ids.label}>
            {t("cf.label")}
          </label>
          <TextInput
            id={ids.label}
            value={label}
            onChange={(event) => setLabel(event.target.value)}
          />
        </div>
        <div className="field">
          <label className="t-label" htmlFor={ids.key}>
            {t("cf.apiKey")}
          </label>
          <TextInput
            id={ids.key}
            className="t-mono"
            value={apiKey(object, label)}
            disabled
            readOnly
          />
          <span className="cf-hint">{t("cf.apiKeyHint")}</span>
        </div>
      </div>

      <div className="field">
        <span className="t-label">{t("cf.typeLabel")}</span>
        <div className="cf-typegrid">
          {CF_TYPES.map((candidate) => {
            const Icon = TYPE_ICON[candidate];
            return (
              <button
                key={candidate}
                type="button"
                aria-pressed={candidate === type}
                className={
                  candidate === type ? "cf-typebtn active" : "cf-typebtn"
                }
                onClick={() => setType(candidate)}
              >
                <Icon aria-hidden />
                <span>{t(`cf.type.${candidate}`)}</span>
              </button>
            );
          })}
        </div>
      </div>

      {type === "currency" && (
        <div className="field">
          <label className="t-label" htmlFor={ids.currency}>
            {t("cf.currencyCode")}
          </label>
          <TextInput
            id={ids.currency}
            className="t-mono"
            value={currency}
            maxLength={3}
            onChange={(event) => setCurrency(event.target.value.toUpperCase())}
          />
          <span className="cf-hint">{t("cf.currencyHint")}</span>
        </div>
      )}

      {type === "picklist" && (
        <div className="field">
          <span className="t-label">{t("cf.options")}</span>
          <div className="cf-options">
            {options.map((option, idx) => (
              // Option rows have no stable id (they are user-typed values that
              // may repeat), so the row index is the only honest key here.
              // biome-ignore lint/suspicious/noArrayIndexKey: option rows are positional, not identity-keyed
              <div className="cf-option-row" key={idx}>
                <TextInput
                  aria-label={t("cf.optionPlaceholder")}
                  placeholder={t("cf.optionPlaceholder")}
                  value={option}
                  onChange={(event) => setOptionAt(idx, event.target.value)}
                />
                <Button
                  small
                  aria-label={t("cf.removeOption")}
                  onClick={() => removeOption(idx)}
                >
                  {"×"}
                </Button>
              </div>
            ))}
          </div>
          <Button
            small
            onClick={() => setOptions((current) => [...current, ""])}
          >
            {t("cf.addOption")}
          </Button>
        </div>
      )}

      {structural && (
        <div className="cf-refuse" role="alert">
          <strong>{t("cf.refuse.title")}</strong>
          <p>{t("cf.refuse.body")}</p>
          <p>{t("cf.refuse.route")}</p>
        </div>
      )}

      <div className="cf-gate">
        <strong>{t("cf.gate.title")}</strong>
        <p>{t("cf.gate.body", { object })}</p>
        <code className="cf-ddl">
          {ddlPreview(object, label, type, currency)}
        </code>
      </div>

      <div className="cf-actions">
        <Button variant="primary" disabled={!canConfirm} onClick={confirm}>
          {t("cf.confirm")}
        </Button>
        <Button
          onClick={() => {
            setLabel("");
            setType("text");
            setCurrency("EUR");
            setOptions([""]);
          }}
        >
          {t("cf.reset")}
        </Button>
      </div>
    </section>
  );
}

type CustomField = components["schemas"]["CustomField"];
type CustomFieldList = components["schemas"]["CustomFieldListResponse"];
type AuditLogEntry = components["schemas"]["AuditLogEntry"];

// The sentinel id for the optimistic "writing…" row the create mutation stages
// into the list cache before the server commits — a real field id is a UUID, so
// this never collides with one, and the table gives it the pending treatment.
const STAGED_ID = "staged";

// The custom-fields listing for one object (AC-custom-fields-1): every field's
// immutable cf_ API key, its typed chip, and who added it, plus the rename /
// archive affordances — rendered only for a manager whose call the server would
// honour. A retired field is not removed (retire is a reversible status flip,
// CUSTOM-FIELDS-AC-13): it stays in the list, struck through and badged, so the
// history the audit trail retains is legible at a glance. DataTable owns no
// per-row class hook, so the retired treatment lives inside the field cell.
export function FieldTable({
  object,
  fields,
  canManage,
  meUserId,
  onRename,
  onArchive,
}: Readonly<{
  object: CfObject;
  fields: CustomField[];
  canManage: boolean;
  meUserId?: string;
  onRename: (field: CustomField) => void;
  onArchive: (field: CustomField) => void;
}>) {
  const t = useT();

  if (fields.length === 0) {
    return <EmptyState>{t(`cf.empty.${object}`)}</EmptyState>;
  }

  const typeChip = (field: CustomField): string => {
    const base = t(`cf.type.${field.type}`);
    if (field.type === "picklist") {
      return `${base} · ${field.options?.length ?? 0}`;
    }
    if (field.type === "currency") {
      return `${base} · ${field.currency ?? ""}`;
    }
    return base;
  };

  const columns: {
    key: string;
    header: string;
    render: (field: CustomField) => React.ReactNode;
  }[] = [
    {
      key: "field",
      header: t("cf.col.field"),
      render: (field) => {
        const staged = field.id === STAGED_ID;
        let cellClass: string | undefined;
        if (staged) {
          cellClass = "cf-cell-staged";
        } else if (field.status === "retired") {
          cellClass = "cf-cell-retired";
        }
        const Icon = TYPE_ICON[field.type];
        return (
          <div className="cf-fieldcell">
            <span className="cf-fieldicon" aria-hidden>
              <Icon />
            </span>
            <div className="cf-fieldmeta">
              <span className="cf-fieldname">
                <span className={cellClass}>{field.label}</span>
                {field.status === "retired" && (
                  <Badge tone="warn">{t("cf.retired")}</Badge>
                )}
              </span>
              <span className="cf-key t-mono">
                {`${field.object}.${field.column_name}`}
              </span>
            </div>
          </div>
        );
      },
    },
    {
      key: "type",
      header: t("cf.col.type"),
      render: (field) => {
        const Icon = TYPE_ICON[field.type];
        return (
          <span className="cf-typechip">
            <Icon aria-hidden />
            {typeChip(field)}
          </span>
        );
      },
    },
    {
      key: "addedBy",
      header: t("cf.col.addedBy"),
      render: (field) =>
        meUserId === field.created_by
          ? t("cf.addedByYou")
          : t("cf.addedByAdmin"),
    },
  ];

  if (canManage) {
    columns.push({
      key: "actions",
      header: "",
      // The optimistic staged row is not yet a real field: it has no id the
      // server would honour, so it wears the "writing…" note instead of the
      // rename/archive affordances until the create commits and replaces it.
      render: (field) =>
        field.id === STAGED_ID ? (
          <span className="cf-cell-staged">{t("cf.writing")}</span>
        ) : (
          <div className="cf-rowactions">
            <Button
              small
              aria-label={t("cf.edit")}
              onClick={() => onRename(field)}
            >
              {t("cf.edit")}
            </Button>
            <Button
              small
              variant="danger"
              aria-label={t("cf.archive")}
              onClick={() => onArchive(field)}
            >
              {t("cf.archive")}
            </Button>
          </div>
        ),
    });
  }

  return (
    <DataTable columns={columns} rows={fields} rowKey={(field) => field.id} />
  );
}

// The custom-field audit rail (AC-custom-fields-6/7): a most-recent-first,
// read-only projection of the audit_log rows this screen's changes emit. It
// renders only the fields the AuditLogEntry contract actually carries — the
// action, the entity it touched, the actor, and when — never an invented
// display name. Empty is an honest state, not a bug.
export function AuditRail({ entries }: Readonly<{ entries: AuditLogEntry[] }>) {
  const t = useT();

  if (entries.length === 0) {
    return <p className="cf-audit-empty">{t("cf.audit.empty")}</p>;
  }

  const recentFirst = [...entries].sort((a, b) =>
    b.occurred_at.localeCompare(a.occurred_at),
  );

  return (
    <ul className="cf-audit">
      {recentFirst.map((entry) => (
        <li className="cf-audit-row" key={entry.id}>
          <span className="cf-audit-action">{entry.action}</span>
          <span className="cf-audit-entity">{entry.entity_type}</span>
          <span className="cf-audit-actor">{entry.actor_id}</span>
          <time className="cf-audit-when" dateTime={entry.occurred_at}>
            {new Date(entry.occurred_at).toLocaleString()}
          </time>
        </li>
      ))}
    </ul>
  );
}

// The add-field create body (CUSTOM-FIELDS-WIRE-2): a plain manual field carries
// `source:"manual"` (the FE convention across deals/leads/organizations), and the
// two conditional shapes ride only on their own type — currency on a currency
// field, options on a picklist — never on the others.
function createBody(
  draft: NewFieldDraft,
): components["schemas"]["CreateCustomFieldRequest"] {
  return {
    object: draft.object,
    label: draft.label,
    type: draft.type,
    source: "manual",
    ...(draft.type === "currency" ? { currency: draft.currency } : {}),
    ...(draft.type === "picklist" ? { options: draft.options } : {}),
  };
}

// The optimistic row shown while the create is in flight — a full CustomField so
// the table renders it, tagged with STAGED_ID so it gets the pending treatment
// (no rename/archive affordance) and is rolled back on error.
function stagedField(draft: NewFieldDraft, createdBy: string): CustomField {
  const now = new Date().toISOString();
  return {
    id: STAGED_ID,
    workspace_id: "",
    object: draft.object,
    label: draft.label,
    slug: slug(draft.label),
    type: draft.type,
    status: "active",
    column_name: columnName(draft.label),
    currency: draft.type === "currency" ? draft.currency : null,
    options: draft.type === "picklist" ? draft.options : null,
    created_by: createdBy,
    created_at: now,
    updated_at: now,
  };
}

// The custom-fields admin screen (AC-custom-fields-1..8): pick an object, read
// its fields with the audit rail, and — for an admin/ops role — add one via the
// governed create (optimistic "writing…" row → commit) or rename/archive an
// existing one. Every mutation is server-authorized; the UI mirror only keeps
// affordances that a call could actually honour. Copy is i18n throughout.
export function CustomFieldsScreen() {
  const t = useT();
  const queryClient = useQueryClient();
  const me = useMe();
  const canManage = canManageCustomFields(me.data?.roles);
  const meUserId = me.data?.user?.id;

  const [object, setObject] = useState<CfObject>("deal");
  const [toast, setToast] = useState<string | null>(null);
  const [renaming, setRenaming] = useState<CustomField | null>(null);
  const [renameLabel, setRenameLabel] = useState("");
  const renameId = useId();

  const list = useQuery({
    queryKey: ["custom-fields", object],
    queryFn: async () => {
      const { data, error } = await api.GET("/custom-fields", {
        params: { query: { object } },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });

  const audit = useQuery({
    queryKey: ["cf-audit"],
    queryFn: async () => {
      const { data, error } = await api.GET("/audit-log", {
        params: { query: { entity_type: "custom_field" } },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });

  const invalidate = () => {
    queryClient.invalidateQueries({ queryKey: ["custom-fields", object] });
    queryClient.invalidateQueries({ queryKey: ["cf-audit"] });
  };

  const create = useMutation({
    mutationFn: async (draft: NewFieldDraft) => {
      const { data, error } = await api.POST("/custom-fields", {
        body: createBody(draft),
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
    onMutate: (draft: NewFieldDraft) => {
      const key = ["custom-fields", draft.object];
      const previous = queryClient.getQueryData<CustomFieldList>(key);
      queryClient.setQueryData<CustomFieldList>(key, (old) =>
        old
          ? { ...old, data: [...old.data, stagedField(draft, meUserId ?? "")] }
          : old,
      );
      return { previous, key };
    },
    onError: (error, _draft, context) => {
      if (context) {
        queryClient.setQueryData(context.key, context.previous);
      }
      setToast(error instanceof Error ? error.message : problemMessage(error));
    },
    onSuccess: (_data, draft) => {
      invalidate();
      setToast(t("cf.added", { label: draft.label }));
    },
  });

  const rename = useMutation({
    mutationFn: async (input: { field: CustomField; label: string }) => {
      const { data, error } = await api.PATCH("/custom-fields/{id}", {
        params: {
          path: { id: input.field.id },
          header: input.field.version
            ? { "If-Match": String(input.field.version) }
            : undefined,
        },
        body: { label: input.label },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
    onSuccess: (_data, input) => {
      invalidate();
      setToast(t("cf.renamed", { label: input.label }));
      setRenaming(null);
    },
    onError: (error) => {
      setToast(error instanceof Error ? error.message : problemMessage(error));
    },
  });

  const archive = useMutation({
    mutationFn: async (field: CustomField) => {
      const { data, error } = await api.POST("/custom-fields/{id}/retire", {
        params: { path: { id: field.id } },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
    onSuccess: (_data, field) => {
      invalidate();
      setToast(t("cf.archived", { label: field.label }));
    },
    onError: (error) => {
      setToast(error instanceof Error ? error.message : problemMessage(error));
    },
  });

  const startRename = (field: CustomField) => {
    setRenaming(field);
    setRenameLabel(field.label);
  };

  return (
    <div className="wrap">
      <SectionHeader title={t("cf.title")} sub={t("cf.subtitle")} />

      <fieldset className="cf-objbar" aria-label={t("cf.object")}>
        {CF_OBJECTS.map((candidate) => {
          const active = candidate === object;
          return (
            <button
              key={candidate}
              type="button"
              aria-pressed={active}
              className="cf-objchip"
              onClick={() => setObject(candidate)}
            >
              {t(`cf.obj.${candidate}`)}
              {active && list.isSuccess && (
                <span className="cf-count">{list.data.data.length}</span>
              )}
            </button>
          );
        })}
      </fieldset>

      <section className="card">
        <SectionHeader title={t("cf.onObject", { object })} />
        <QueryGate query={list}>
          {(page) => (
            <FieldTable
              object={object}
              fields={page.data}
              canManage={canManage}
              meUserId={meUserId}
              onRename={startRename}
              onArchive={(field) => archive.mutate(field)}
            />
          )}
        </QueryGate>
      </section>

      {canManage ? (
        <FieldBuilder
          key={object}
          object={object}
          pending={create.isPending}
          onSubmit={(draft) => create.mutate(draft)}
          onToast={setToast}
        />
      ) : (
        <p className="t-caption">{t("cf.noPermission")}</p>
      )}

      <section className="card">
        <SectionHeader title={t("cf.audit.title")} />
        <AuditRail entries={audit.data?.data ?? []} />
        <p className="t-caption">{t("cf.audit.footer")}</p>
      </section>

      {toast && (
        <div className="toast-region">
          <output className="toast">
            <span className="dot dot-auto" />
            {toast}
          </output>
        </div>
      )}

      <Modal
        open={renaming !== null}
        onClose={() => setRenaming(null)}
        labelledBy={renameId}
      >
        <h3 id={renameId} className="section-header">
          {t("cf.edit")}
        </h3>
        <div className="field">
          <label className="t-label" htmlFor={`${renameId}-input`}>
            {t("cf.renamePrompt")}
          </label>
          <TextInput
            id={`${renameId}-input`}
            value={renameLabel}
            onChange={(event) => setRenameLabel(event.target.value)}
          />
        </div>
        <div className="cf-actions">
          <Button
            variant="primary"
            disabled={rename.isPending || renameLabel.trim().length === 0}
            onClick={() => {
              if (renaming && renameLabel.trim().length > 0) {
                rename.mutate({ field: renaming, label: renameLabel.trim() });
              }
            }}
          >
            {t("trust.save")}
          </Button>
          <Button onClick={() => setRenaming(null)}>{t("deals.cancel")}</Button>
        </div>
      </Modal>
    </div>
  );
}
