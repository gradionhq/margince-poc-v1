import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Plus } from "lucide-react";
import { type ReactNode, useEffect, useId, useRef, useState } from "react";
import { navigate, type Route } from "../app/router";
import { Button, Modal, TextInput } from "../design-system/atoms";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { ProblemError, problemExistingId } from "./common";

// The shared create-record form (contacts, companies, leads, deals): each
// list screen declares its fields; the transport (which endpoint, how values
// map onto the request body) stays with the screen that owns the resource.
// Server-side validation is the truth — a 422 renders its RFC 7807 detail
// verbatim under the form, never a swallowed or re-worded error.

export type CreateFieldOption = { value: string; label: string };

// One subfield within a repeatable row (e.g. an emails row's `email` and
// `email_type`) — reuses the same control types as a top-level CreateField,
// minus repeatable-ness itself (rows don't nest).
export type SubField = {
  key: string;
  label: MessageKey;
  type?: "text" | "email" | "number" | "date" | "datetime-local" | "select";
  required?: boolean;
  options?: CreateFieldOption[];
  placeholder?: string;
};

export type CreateField = {
  key: string;
  label: MessageKey;
  type?:
    | "text"
    | "email"
    | "number"
    | "date"
    | "datetime-local"
    | "select"
    | "repeatable";
  required?: boolean;
  options?: CreateFieldOption[];
  placeholder?: string;
  // repeatable-only: the subfields each row renders, the "add row" button's
  // label, and (if set) which subfield key holds the row's primary flag.
  rowFields?: SubField[];
  addLabel?: MessageKey;
  primaryKey?: string;
};

// One repeatable-row field's collected rows, e.g. `{ email: "a@x", email_type:
// "work", is_primary: "true" }`.
export type FormRow = Record<string, string>;
// Repeatable-row values, keyed by the field's key — the SECOND channel: it
// exists alongside `values: Record<string, string>` (never merged into it) so
// every existing scalar-only screen and its single-arg create callback keeps
// working untouched.
export type FormRows = Record<string, FormRow[]>;

function rowsRequirementMet(field: CreateField, rows: FormRow[]): boolean {
  if (!field.required) {
    return true;
  }
  const required = field.rowFields ?? [];
  return rows.some((row) =>
    required.every(
      (sub) => !sub.required || (row[sub.key] ?? "").trim().length > 0,
    ),
  );
}

// The shared post-create choreography: refresh the list, close the modal,
// open the fresh record's 360. Screens supply only their transport.
export function useCreateRecord<Created extends { id: string }>({
  create,
  invalidate,
  screen,
  onDone,
}: Readonly<{
  create: (values: Record<string, string>, rows?: FormRows) => Promise<Created>;
  invalidate: string;
  screen: string;
  onDone: () => void;
}>) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({
      values,
      rows,
    }: {
      values: Record<string, string>;
      rows: FormRows;
    }) => create(values, rows),
    onSuccess: (created) => {
      queryClient.invalidateQueries({ queryKey: [invalidate] });
      onDone();
      navigate({ screen, id: created.id });
    },
  });
}

// The whole per-screen create affordance in one piece: the button, the modal,
// its open state, and the post-create choreography. A list screen supplies
// its label, fields, and transport — nothing else.
export function CreateAction<Created extends { id: string }>({
  label,
  fields,
  create,
  invalidate,
  screen,
  startOpen = false,
  resolveExisting,
}: Readonly<{
  label: string;
  fields: CreateField[];
  create: (values: Record<string, string>, rows?: FormRows) => Promise<Created>;
  invalidate: string;
  screen: string;
  startOpen?: boolean;
  // Duplicate (409) dedupe: given the problem's code + collided record id,
  // builds the route to that record. Absent screens simply never show the
  // "view existing" link.
  resolveExisting?: (code: string, id: string) => Route;
}>) {
  const [creating, setCreating] = useState(startOpen);
  const mutation = useCreateRecord({
    create,
    invalidate,
    screen,
    onDone: () => setCreating(false),
  });
  const existing =
    mutation.error instanceof ProblemError
      ? problemExistingId(mutation.error.problem)
      : null;
  return (
    <>
      <NewRecordButton label={label} onClick={() => setCreating(true)} />
      <CreateRecordModal
        open={creating}
        onClose={() => setCreating(false)}
        title={label}
        fields={fields}
        pending={mutation.isPending}
        error={mutation.isError ? mutation.error.message : null}
        existing={existing}
        resolveExisting={resolveExisting}
        onSubmit={(values, rows) =>
          mutation.mutate({ values, rows: rows ?? {} })
        }
      />
    </>
  );
}

export function NewRecordButton({
  label,
  onClick,
}: Readonly<{ label: string; onClick: () => void }>) {
  return (
    <Button small onClick={onClick} data-testid="new-record">
      <Plus aria-hidden style={{ width: 14, height: 14 }} /> {label}
    </Button>
  );
}

export function fieldControl(
  field: CreateField | SubField,
  fieldId: string,
  value: string,
  setValue: (next: string) => void,
): ReactNode {
  if (field.type === "select") {
    return (
      <select
        id={fieldId}
        className="input"
        value={value}
        required={field.required}
        onChange={(event) => setValue(event.target.value)}
      >
        {!field.required && <option value="" />}
        {(field.options ?? []).map((option) => (
          <option key={option.value} value={option.value}>
            {option.label}
          </option>
        ))}
      </select>
    );
  }
  return (
    <TextInput
      id={fieldId}
      type={field.type ?? "text"}
      value={value}
      required={field.required}
      placeholder={field.placeholder}
      onChange={(event) => setValue(event.target.value)}
    />
  );
}

// A repeatable-row field (emails/phones/domains): each existing row renders
// its subfields via the same fieldControl every scalar field uses, plus an
// optional "primary" radio (selecting one clears it on every other row) and a
// remove button; an "Add" button appends a blank row. Rows live in the
// second `rows` channel — never merged into `values` — so scalar-only
// screens stay untouched.
function RepeatableRowsField({
  field,
  formId,
  rows,
  setRows,
}: Readonly<{
  field: CreateField;
  formId: string;
  rows: FormRow[];
  setRows: (next: FormRow[]) => void;
}>) {
  const t = useT();
  const rowFields = field.rowFields ?? [];
  const primaryKey = field.primaryKey;

  function updateRow(index: number, key: string, value: string) {
    setRows(
      rows.map((row, rowIndex) =>
        rowIndex === index ? { ...row, [key]: value } : row,
      ),
    );
  }

  function markPrimary(index: number) {
    if (!primaryKey) {
      return;
    }
    setRows(
      rows.map((row, rowIndex) => ({
        ...row,
        [primaryKey]: rowIndex === index ? "true" : "",
      })),
    );
  }

  function removeRow(index: number) {
    setRows(rows.filter((_, rowIndex) => rowIndex !== index));
  }

  return (
    <div className="field-repeatable">
      <span className="t-label">
        {t(field.label)}
        {field.required ? " *" : ""}
      </span>
      {rows.map((row, index) => {
        const rowId = `${formId}-${field.key}-${index}`;
        return (
          // Rows have no stable identity until saved — index is the only key
          // available, and reordering never happens (add appends, remove
          // filters), so it's safe here.
          <div
            // biome-ignore lint/suspicious/noArrayIndexKey: rows are unordered-append/remove only
            key={index}
            className="card"
            style={{
              display: "flex",
              flexWrap: "wrap",
              gap: 8,
              alignItems: "center",
            }}
          >
            {rowFields.map((subField) => {
              const subFieldId = `${rowId}-${subField.key}`;
              return (
                <div className="field" key={subField.key}>
                  <label className="t-label" htmlFor={subFieldId}>
                    {t(subField.label)}
                    {subField.required ? " *" : ""}
                  </label>
                  {fieldControl(
                    subField,
                    subFieldId,
                    row[subField.key] ?? "",
                    (next) => updateRow(index, subField.key, next),
                  )}
                </div>
              );
            })}
            {primaryKey && (
              <label
                className="t-label"
                style={{ display: "flex", alignItems: "center", gap: 4 }}
              >
                <input
                  type="radio"
                  name={`${formId}-${field.key}-primary`}
                  checked={row[primaryKey] === "true"}
                  onChange={() => markPrimary(index)}
                />
                {t("field.primary")}
              </label>
            )}
            <Button small type="button" onClick={() => removeRow(index)}>
              {t("field.removeRow")}
            </Button>
          </div>
        );
      })}
      <Button small type="button" onClick={() => setRows([...rows, {}])}>
        {t(field.addLabel ?? field.label)}
      </Button>
    </div>
  );
}

// The shared modal form body: fields → controls, the error paragraph, and
// the Cancel/Save row. Both create and edit render this identically — only
// the values' origin (empty defaults vs. a prefilled record) and the submit
// label differ, and those stay with each modal's owner.
export function RecordFormBody({
  fields,
  values,
  setValues,
  rows,
  setRows,
  pending,
  error,
  existing,
  resolveExisting,
  onSubmit,
  onClose,
  submitLabelKey,
}: Readonly<{
  fields: CreateField[];
  values: Record<string, string>;
  setValues: (next: Record<string, string>) => void;
  rows: FormRows;
  setRows: (next: FormRows) => void;
  pending: boolean;
  error: string | null;
  // The collided record from a duplicate (409) problem, and the screen's
  // mapping from its code + id to a Route — both present renders the "view
  // existing" link right under the error message.
  existing?: { id: string; code: string } | null;
  resolveExisting?: (code: string, id: string) => Route;
  onSubmit: (values: Record<string, string>, rows?: FormRows) => void;
  onClose: () => void;
  submitLabelKey: MessageKey;
}>) {
  const t = useT();
  const formId = useId();

  const requiredMissing = fields.some((field) => {
    if (field.type === "repeatable") {
      return !rowsRequirementMet(field, rows[field.key] ?? []);
    }
    return field.required && !(values[field.key] ?? "").trim();
  });

  return (
    <form
      onSubmit={(event) => {
        event.preventDefault();
        onSubmit(values, rows);
      }}
      className="form-stack"
    >
      {fields.map((field) => {
        const fieldId = `${formId}-${field.key}`;
        if (field.type === "repeatable") {
          return (
            <RepeatableRowsField
              key={field.key}
              field={field}
              formId={formId}
              rows={rows[field.key] ?? []}
              setRows={(next) => setRows({ ...rows, [field.key]: next })}
            />
          );
        }
        return (
          <div className="field" key={field.key}>
            <label className="t-label" htmlFor={fieldId}>
              {t(field.label)}
              {field.required ? " *" : ""}
            </label>
            {fieldControl(field, fieldId, values[field.key] ?? "", (next) =>
              setValues({ ...values, [field.key]: next }),
            )}
          </div>
        );
      })}
      {error && (
        <p className="t-caption" style={{ color: "var(--danger)" }}>
          {error}
        </p>
      )}
      {existing && resolveExisting && (
        <Button
          small
          type="button"
          style={{ alignSelf: "flex-start" }}
          onClick={() => navigate(resolveExisting(existing.code, existing.id))}
        >
          {t("dedupe.viewExisting")}
        </Button>
      )}
      <div className="actions">
        <Button small type="button" onClick={onClose}>
          {t("create.cancel")}
        </Button>
        <Button
          small
          variant="primary"
          type="submit"
          disabled={pending || requiredMissing}
        >
          {pending ? t("create.saving") : t(submitLabelKey)}
        </Button>
      </div>
    </form>
  );
}

export function CreateRecordModal({
  open,
  onClose,
  title,
  fields,
  pending,
  error,
  existing,
  resolveExisting,
  onSubmit,
}: Readonly<{
  open: boolean;
  onClose: () => void;
  title: string;
  fields: CreateField[];
  pending: boolean;
  error: string | null;
  existing?: { id: string; code: string } | null;
  resolveExisting?: (code: string, id: string) => Route;
  onSubmit: (values: Record<string, string>, rows?: FormRows) => void;
}>) {
  const headingId = useId();
  const [values, setValues] = useState<Record<string, string>>({});
  const [rows, setRows] = useState<FormRows>({});
  // Only the closed→open TRANSITION should reset the form — `fields` is a
  // non-primitive prop that a parent re-render (react-query background
  // refetch, locale change) can hand a new reference to while the modal
  // stays open, and re-running the effect on that alone would wipe whatever
  // the user is mid-typing.
  const wasOpen = useRef(false);

  useEffect(() => {
    if (open && !wasOpen.current) {
      // A fresh open starts from the fields' defaults (first select option
      // for required selects), never from a previous attempt's leftovers.
      const defaults: Record<string, string> = {};
      for (const field of fields) {
        if (field.type === "select" && field.required) {
          defaults[field.key] = field.options?.[0]?.value ?? "";
        }
      }
      setValues(defaults);
      setRows({});
    }
    wasOpen.current = open;
  }, [open, fields]);

  return (
    <Modal open={open} onClose={onClose} labelledBy={headingId}>
      <h2 id={headingId} className="t-h2" style={{ marginBottom: 12 }}>
        {title}
      </h2>
      <RecordFormBody
        fields={fields}
        values={values}
        setValues={setValues}
        rows={rows}
        setRows={setRows}
        pending={pending}
        error={error}
        existing={existing}
        resolveExisting={resolveExisting}
        onSubmit={onSubmit}
        onClose={onClose}
        submitLabelKey="create.save"
      />
    </Modal>
  );
}
