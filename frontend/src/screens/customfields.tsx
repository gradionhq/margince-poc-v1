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
import { useCanWrite, useHoldsAdminRole } from "../app/capability";
import {
  Badge,
  Button,
  DataTable,
  Disclosure,
  EmptyState,
  Field,
  Modal,
  SegmentedControl,
  TextInput,
} from "../design-system/atoms";
import { Callout } from "../design-system/callout";
import { Panel, PanelBody } from "../design-system/panel";
import { SettingList, SettingRow } from "../design-system/settingrow";
import { type SectionState, SurfaceState } from "../design-system/surfacestate";
import { ToastRegion, useToast } from "../design-system/toast";
import { AutonomyDot } from "../design-system/trust";
import { useT } from "../i18n";
import { AuditEntryLine } from "./audit";
import { problemMessageOf, QueryGate, throwProblem, useMe } from "./common";
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
// is NOT the ApprovalGate (Accept/Edit/Dismiss triad) — it is a `warn` Callout,
// which is what the surface saying something about itself already looks like
// everywhere else.

// One glyph per scalar type, so a field's shape reads at a glance in the table.
// Decorative only — every use is aria-hidden, so the accessible name stays the
// translated type word.
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
  onCancel,
  onToast,
}: Readonly<{
  object: CfObject;
  pending: boolean;
  onSubmit: (draft: NewFieldDraft) => void;
  // Leaving the form without adding a field. The builder lives in a dialog, so
  // its secondary verb is what closes that dialog rather than what empties the
  // inputs: a form that is discarded on close has nothing to reset to.
  onCancel: () => void;
  onToast: (msg: string) => void;
}>) {
  const t = useT();
  const [label, setLabel] = useState("");
  const [type, setType] = useState<CfType>("text");
  const [currency, setCurrency] = useState("EUR");
  const [options, setOptions] = useState<string[]>([""]);
  const structural = looksStructural(label);
  // A picklist with no non-blank option is not a picklist, and a currency field
  // needs a well-formed 3-letter ISO-4217 code — Confirm stays disabled until
  // the type-specific shape is valid, not just the label.
  const typeShapeValid =
    (type !== "picklist" || options.some((opt) => opt.trim().length > 0)) &&
    (type !== "currency" || /^[A-Za-z]{3}$/.test(currency.trim()));
  const canConfirm =
    !pending && label.trim().length > 0 && !structural && typeShapeValid;

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
    <div className="cf-builder">
      <div className="cf-builder-head">
        <p className="cf-hint">{t("cf.builder.intro")}</p>
        <Badge>{t("cf.builder.noCode")}</Badge>
      </div>

      <div className="cf-grid">
        <Field label={t("cf.label")}>
          {(control) => (
            <TextInput
              {...control}
              value={label}
              onChange={(event) => setLabel(event.target.value)}
            />
          )}
        </Field>
        <Field
          label={t("cf.apiKey")}
          className="cf-field"
          hint={t("cf.apiKeyHint")}
        >
          {(control) => (
            <TextInput
              {...control}
              className="t-mono"
              value={apiKey(object, label)}
              disabled
              readOnly
            />
          )}
        </Field>
      </div>

      {/* One closed set of options, all visible at once — the definition of
          SegmentedControl. It replaced a six-tile icon grid that was the same
          `aria-pressed` button in a third chrome, and whose glyphs were
          aria-hidden decoration the accessible name never carried. Losing them
          costs the reader nothing; the type still reads on every row of the
          table above, where it names a field rather than a choice. */}
      <div className="field">
        <span className="t-label">{t("cf.typeLabel")}</span>
        <SegmentedControl
          label={t("cf.typeLabel")}
          options={CF_TYPES}
          value={type}
          onChange={setType}
          labels={typeLabels(t)}
        />
      </div>

      {type === "currency" && (
        <Field
          label={t("cf.currencyCode")}
          className="cf-field"
          hint={t("cf.currencyHint")}
        >
          {(control) => (
            <TextInput
              {...control}
              className="t-mono"
              value={currency}
              maxLength={3}
              onChange={(event) =>
                setCurrency(event.target.value.toUpperCase())
              }
            />
          )}
        </Field>
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
        <Callout tone="danger" live="alert" title={t("cf.refuse.title")}>
          <p>{t("cf.refuse.body")}</p>
          <p>{t("cf.refuse.route")}</p>
        </Callout>
      )}

      {/* `warn` because nothing is wrong yet and something will be if the
          reader confirms without reading: the column goes live on every record
          of this object. The autonomy dot rides in the title so the confirm
          tier and the sentence it qualifies are one line, not two. */}
      <Callout
        tone="warn"
        title={
          <>
            <AutonomyDot tier="confirm" /> {t("cf.gate.title")}
          </>
        }
      >
        <p>{t("cf.gate.body", { object: t(`cf.obj.${object}`) })}</p>
        <code className="cf-ddl">
          {ddlPreview(object, label, type, currency)}
        </code>
      </Callout>

      {/* Cancel first, then the verb that writes — the order every dialog in
          this tree uses, so the destructive-looking half is never where the
          reader's hand expects the safe one. Reset went with the disclosure
          this form used to live in: closing the dialog discards the draft, so
          a control that empties the inputs in place has nothing left to do. */}
      <div className="cf-actions">
        <Button small variant="ghost" onClick={onCancel}>
          {t("deals.cancel")}
        </Button>
        <Button
          small
          variant="primary"
          disabled={!canConfirm}
          onClick={confirm}
        >
          {t("cf.confirm")}
        </Button>
      </div>
    </div>
  );
}

// The two option sets this screen switches on, each as the flat label map
// SegmentedControl takes. Spelled out rather than derived from the tuple so
// every key is checked against the catalog at compile time — a mapped
// `Object.fromEntries` would need a cast to get back to Record<Option, string>,
// and a cast is exactly what stops a missing translation being a build error.
function typeLabels(t: ReturnType<typeof useT>): Record<CfType, string> {
  return {
    text: t("cf.type.text"),
    number: t("cf.type.number"),
    date: t("cf.type.date"),
    currency: t("cf.type.currency"),
    picklist: t("cf.type.picklist"),
    boolean: t("cf.type.boolean"),
  };
}

function objectLabels(t: ReturnType<typeof useT>): Record<CfObject, string> {
  return {
    deal: t("cf.obj.deal"),
    organization: t("cf.obj.organization"),
    person: t("cf.obj.person"),
    lead: t("cf.obj.lead"),
  };
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
  canEdit,
  meUserId,
  onRename,
  onArchive,
}: Readonly<{
  object: CfObject;
  fields: CustomField[];
  // Both affordances this gates are updates: renaming relabels a live field,
  // and retiring one is a lifecycle transition that keeps the column and its
  // history. Neither is custom_field:delete, which no surface offers.
  canEdit: boolean;
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

  if (canEdit) {
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
            {/* Two ghost verbs. Archiving a field hides it from new records and
                keeps every value already captured — the toast says so, and the
                act is reversible — so `danger` overstated it: three solid red
                buttons per table were the loudest thing on the tab, which is
                the shout a reader learns to ignore. An `aria-label` repeating
                the button's own words is not a name either; the text is the
                name. */}
            <Button small onClick={() => onRename(field)}>
              {t("cf.edit")}
            </Button>
            <Button small onClick={() => onArchive(field)}>
              {t("cf.archive")}
            </Button>
          </div>
        ),
    });
  }

  return (
    <DataTable
      label={t("cf.listLabel", { object: objectLabels(t)[object] })}
      columns={columns}
      rows={fields}
      rowKey={(field) => field.id}
    />
  );
}

// The custom-field audit rail (AC-custom-fields-6/7): a most-recent-first,
// read-only projection of the audit_log rows this screen's changes emit. It
// renders only the fields the AuditLogEntry contract actually carries — the
// action, the entity it touched, the actor, and when — never an invented
// display name.
//
// Loading, failed, withheld and empty are FOUR different sentences and the rail
// used to hand-roll three of them into one muted paragraph, with no retry on
// the failure — which is `unavailable` wearing the word "error". SurfaceState
// owns that vocabulary, so the caller classifies and this renders.
export function AuditRail({
  entries,
  state,
  meUserId,
  onRetry,
}: Readonly<{
  entries: AuditLogEntry[];
  state: SectionState;
  meUserId?: string;
  onRetry: () => void;
}>) {
  const t = useT();
  const recentFirst = [...entries].sort((a, b) =>
    b.occurred_at.localeCompare(a.occurred_at),
  );

  return (
    <SurfaceState
      state={state}
      emptyLabel={t("cf.audit.empty")}
      detail={{ onRetry }}
    >
      <ul className="cf-audit">
        {recentFirst.map((entry) => (
          <li key={entry.id}>
            <AuditEntryLine entry={entry} meUserId={meUserId} />
          </li>
        ))}
      </ul>
    </SurfaceState>
  );
}

// Which of the four the rail is in. Withheld comes FIRST because a disabled
// query never leaves its pending state: classified in read-query order, a
// reader whose role cannot see the trail would be shown a skeleton forever
// instead of being told the answer is already settled.
function auditState(
  isAdmin: boolean,
  isPending: boolean,
  isError: boolean,
  count: number,
): SectionState {
  if (!isAdmin) {
    return "withheld";
  }
  if (isError) {
    return "failed";
  }
  if (isPending) {
    return "loading";
  }
  return count === 0 ? "empty" : "ready";
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
    ...(draft.type === "picklist"
      ? { options: cleanOptions(draft.options) }
      : {}),
  };
}

// A picklist ships only its real choices: blank / whitespace-only rows (the
// editor's floor row and any half-typed entries) are dropped and exact
// duplicates collapsed, so the stored enum matches what the admin sees.
function cleanOptions(options: string[]): string[] {
  const seen = new Set<string>();
  const cleaned: string[] = [];
  for (const raw of options) {
    const trimmed = raw.trim();
    if (trimmed.length === 0 || seen.has(trimmed)) {
      continue;
    }
    seen.add(trimmed);
    cleaned.push(trimmed);
  }
  return cleaned;
}

// The optimistic row shown while the create is in flight — a full CustomField so
// the table renders it, tagged with STAGED_ID so it gets the pending treatment
// (no rename/archive affordance) and is rolled back on error.
function stagedField(draft: NewFieldDraft, createdBy: string): CustomField {
  const now = new Date().toISOString();
  return {
    id: STAGED_ID,
    object: draft.object,
    label: draft.label,
    slug: slug(draft.label),
    type: draft.type,
    status: "active",
    column_name: columnName(draft.label),
    currency: draft.type === "currency" ? draft.currency : null,
    options: draft.type === "picklist" ? cleanOptions(draft.options) : null,
    created_by: createdBy,
    created_at: now,
    updated_at: now,
  };
}

// The custom-fields admin surface (AC-custom-fields-1..8): pick an object, read
// its fields with the audit rail, and — for an admin/ops role — add one via the
// governed create (optimistic "writing…" row → commit) or rename/archive an
// existing one. Every mutation is server-authorized; the UI mirror only keeps
// affordances that a call could actually honour. Copy is i18n throughout.
//
// This is CONTENT inside a settings page, not a route of its own. The page owns
// the .wrap reading column (a second one nested inside it would double the page
// padding) and the h1 in the shell's page head, so this returns ONE Panel whose
// own h2 sits under that h1 — the document never carries two page titles.
//
// One panel, not four surfaces. It used to be a bare heading, a chip bar and
// three sibling Cards, which is four things on a tab that already carries three
// more subjects — and the heading pair said "Custom fields" twice in 40px, once
// as the section name and again as the card title. The object is now named by
// the segmented control alone, and the two surfaces most visits do not want —
// the builder and the change trail — are Disclosures. What is left open is the
// answer to the question people actually arrive with: which fields exist.
export function CustomFieldsAdmin() {
  const t = useT();
  const queryClient = useQueryClient();
  const me = useMe();
  // Two grants, two affordances: the builder adds a column to a live table,
  // while rename and retire change one that already exists.
  const canCreate = useCanWrite("custom_field", "create");
  const canEdit = useCanWrite("custom_field", "update");
  // The trail is the ADMIN's, not this screen's: /audit-log is gated
  // server-side on the role (privacy.ListAuditLog), while this page opens for
  // anyone holding custom_field:read. So the rail below is gated on the role
  // too, exactly as the settings audit card is.
  const isAdmin = useHoldsAdminRole();
  const meUserId = me.data?.user?.id;

  const [object, setObject] = useState<CfObject>("deal");
  const toast = useToast();
  const [renaming, setRenaming] = useState<CustomField | null>(null);
  const [renameLabel, setRenameLabel] = useState("");
  // The builder is mounted only while its dialog is open, which is what stops a
  // second Confirm resubmitting the same, now-committed, draft (m6): a
  // successful create closes the dialog and the form's state goes with it.
  const [adding, setAdding] = useState(false);
  const renameId = useId();
  const addId = useId();

  const list = useQuery({
    queryKey: ["custom-fields", object],
    queryFn: async () => {
      const { data, error } = await api.GET("/custom-fields", {
        params: { query: { object } },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });

  const audit = useQuery({
    queryKey: ["cf-audit"],
    // A denial that is already known is not worth a request. Without this a
    // non-admin on this tab fired a call that could only 403 and got the
    // rail's red role="alert" back — a failure with a retry that can never
    // succeed, over a refusal they cannot act on.
    enabled: isAdmin,
    queryFn: async () => {
      const { data, error } = await api.GET("/audit-log", {
        params: { query: { entity_type: "custom_field" } },
      });
      if (error) {
        throwProblem(error);
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
        throwProblem(error);
      }
      return data;
    },
    onMutate: async (draft: NewFieldDraft) => {
      // Key the optimistic write to the DRAFT's object, not the current-render
      // one, so switching objects mid-create still stages, rolls back, and
      // invalidates the right list (m2).
      const key = ["custom-fields", draft.object];
      await queryClient.cancelQueries({ queryKey: key });
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
      toast.show(problemMessageOf(error, t), { mark: false });
    },
    onSuccess: (_data, draft) => {
      queryClient.invalidateQueries({
        queryKey: ["custom-fields", draft.object],
      });
      queryClient.invalidateQueries({ queryKey: ["cf-audit"] });
      toast.show(t("cf.added", { label: draft.label }));
      // The dialog goes, and the toast is what reports the outcome on the card
      // behind it — a reader who has just added a column has no second one to
      // type into, and unmounting the form is what clears the committed draft.
      setAdding(false);
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
        throwProblem(error);
      }
      return data;
    },
    onSuccess: (_data, input) => {
      invalidate();
      toast.show(t("cf.renamed", { label: input.label }));
      setRenaming(null);
    },
    onError: (error) => {
      toast.show(problemMessageOf(error, t), { mark: false });
    },
  });

  const archive = useMutation({
    mutationFn: async (field: CustomField) => {
      const { data, error } = await api.POST("/custom-fields/{id}/retire", {
        params: { path: { id: field.id } },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    onSuccess: (_data, field) => {
      invalidate();
      toast.show(t("cf.archived", { label: field.label }));
    },
    onError: (error) => {
      toast.show(problemMessageOf(error, t), { mark: false });
    },
  });

  const startRename = (field: CustomField) => {
    setRenaming(field);
    setRenameLabel(field.label);
  };

  const objectName = t(`cf.obj.${object}`);

  return (
    <Panel
      className="cf-screen"
      title={t("cf.title")}
      // The create verb is the card's, so it stands in the header band. As a
      // trailing row its label ("Add a field to Deal") said the same thing as
      // the button beside it, which is one act named twice on one line; the
      // object it applies to is chosen in the row below and named again by the
      // dialog. Absent without the create grant, as the row was: a
      // surface that is only an action makes no claim about the data by not
      // being there.
      titleAction={
        canCreate && (
          <Button small onClick={() => setAdding(true)}>
            {t("cf.builder.open")}
          </Button>
        )
      }
    >
      {/* No `form-stack` on the body: the description already pays for its own
          interval to the rows (`.settings-panel-sub`), and a stack's gap on top
          of that margin — margins do not collapse in a flex container — put 28px
          under a line every other settings card sets 16px below. The posture
          line below the rows takes its interval from `.cf-posture`. */}
      <PanelBody>
        <p className="settings-panel-sub">{t("cf.subtitle")}</p>
        <SettingList>
          {/* Which object the rows below belong to. One closed set of four, all
              visible at once, so it answers its own row from the right column —
              the same shape every other single-choice setting on this page
              takes. The count that used to ride the active pill is gone: it
              only ever described the object already selected, whose whole list
              is immediately below. */}
          <SettingRow
            label={t("cf.object")}
            control={
              <SegmentedControl
                label={t("cf.object")}
                options={CF_OBJECTS}
                value={object}
                onChange={setObject}
                labels={objectLabels(t)}
              />
            }
          />
          {/* The fields are the SUBJECT of this card rather than an answer to a
              question beside them, so they take the row's full width. The table
              is read per object, so the row above is a tab strip: the table
              that lands is a fresh element and arrives. */}
          <SettingRow
            label={t("cf.listLabel", { object: objectName })}
            layout="stack"
            control={
              <div className="arrive-stack">
                <QueryGate query={list}>
                  {(page) => (
                    <FieldTable
                      object={object}
                      fields={page.data}
                      canEdit={canEdit}
                      meUserId={meUserId}
                      onRename={startRename}
                      onArchive={(field) => archive.mutate(field)}
                    />
                  )}
                </QueryGate>
              </div>
            }
          />
          {/* Withheld, not absent: the trail keeps its place for every reader,
              because a section that simply were not there would read as "nobody
              has changed a field" — a claim about the data in place of one about
              who may read it. Closed by default because it is a secondary read;
              the state inside it is settled before it is ever opened. */}
          <Disclosure summary={t("cf.audit.title")}>
            <AuditRail
              entries={audit.data?.data ?? []}
              state={auditState(
                isAdmin,
                audit.isPending,
                audit.isError,
                audit.data?.data.length ?? 0,
              )}
              meUserId={meUserId}
              onRetry={() => void audit.refetch()}
            />
            {/* True for every reader: the recording happens whether or not this
                one may read it back. */}
            <p className="t-caption">{t("cf.audit.footer")}</p>
          </Disclosure>
        </SettingList>
        {/* The posture speaks for BOTH grants, so it is bound to both. The
            server splits them — create.go admits `custom_field:create`, the
            lifecycle handlers admit `update` — and a principal holding update
            without create was reading "you have read-only access" above rows
            whose Edit and Archive buttons worked. A sentence about a boundary
            has to be true of the boundary it names. */}
        {!canCreate && !canEdit && (
          <p className="cf-posture">{t("cf.noPermission")}</p>
        )}
      </PanelBody>

      <ToastRegion toast={toast} />

      {/* Mounted only while it is open, so a half-typed label is gone the next
          time the dialog opens rather than waiting there under an object
          nobody re-chose.

          `wide` is the variant's stated case: the builder carries the pending
          DDL, and a 440px dialog wraps
          `ALTER organization ADD COLUMN cf_contract_end_date (date)` into an
          unreadable stack — the one line a reader is meant to check before
          confirming a live schema change. It also keeps the label and the API
          key derived from it side by side. */}
      {adding && (
        <Modal
          open
          size="wide"
          onClose={() => setAdding(false)}
          labelledBy={addId}
        >
          <h2 id={addId} className="t-h2 modal-title">
            {t("cf.builder.addTo", { object: objectName })}
          </h2>
          <FieldBuilder
            object={object}
            pending={create.isPending}
            onSubmit={(draft) => create.mutate(draft)}
            onCancel={() => setAdding(false)}
            onToast={toast.show}
          />
        </Modal>
      )}

      <Modal
        open={renaming !== null}
        onClose={() => setRenaming(null)}
        labelledBy={renameId}
      >
        {/* A dialog is its own region, so its title starts the outline at
            level 2, matching ConfirmModal. `.section-header` went with the h3:
            it is the flex row that carries a card section's title, subtitle and
            actions, and a dialog title is none of those — inside the modal it
            only contributed a page-flow top margin that pushed the title off
            the modal's own padding. `.modal-title` is the catalog's own name for
            the interval under a dialog title, so the twelve pixels are declared
            once for every dialog rather than typed in here. */}
        <h2 id={renameId} className="t-h2 modal-title">
          {t("cf.edit")}
        </h2>
        <Field label={t("cf.renamePrompt")}>
          {(control) => (
            <TextInput
              {...control}
              value={renameLabel}
              onChange={(event) => setRenameLabel(event.target.value)}
            />
          )}
        </Field>
        <div className="cf-actions">
          <Button small variant="ghost" onClick={() => setRenaming(null)}>
            {t("deals.cancel")}
          </Button>
          <Button
            small
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
        </div>
      </Modal>
    </Panel>
  );
}
