import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useEffect, useId, useState } from "react";
import { Button, Modal } from "../design-system/atoms";
import { type CreateField, type FormRows, RecordFormBody } from "./create";

// The shared post-update choreography: run the screen-supplied PATCH, then
// refresh both the list and the specific record so the 360 reflects the new
// version. A 409 version_skew surfaces as mutation.error (rendered by the form),
// never a silent overwrite.
export function useUpdateRecord<Updated extends { id: string }>({
  update,
  invalidate,
  recordKey,
  onDone,
}: Readonly<{
  update: (
    values: Record<string, unknown>,
    rows?: FormRows,
  ) => Promise<Updated>;
  invalidate: string;
  recordKey: string;
  onDone: () => void;
}>) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({
      values,
      rows,
    }: {
      values: Record<string, unknown>;
      rows: FormRows;
    }) => update(values, rows),
    onSuccess: (updated) => {
      queryClient.invalidateQueries({ queryKey: [invalidate] });
      queryClient.invalidateQueries({ queryKey: [recordKey, updated.id] });
      onDone();
    },
  });
}

// The edit modal: prefilled from the record's current field values (each
// field's key projected off the record, coerced to a string; a field the
// record doesn't carry starts blank rather than throwing). The screen's
// `update` callback — not this form — builds the PATCH body and attaches
// `ifMatch(record.version)`, so this stays resource-agnostic.
export function EditRecordModal({
  open,
  onClose,
  title,
  fields,
  record,
  pending,
  error,
  onSubmit,
}: Readonly<{
  open: boolean;
  onClose: () => void;
  title: string;
  fields: CreateField[];
  record: Record<string, unknown> & { id: string; version?: number };
  pending: boolean;
  error: string | null;
  onSubmit: (values: Record<string, string>, rows?: FormRows) => void;
}>) {
  const headingId = useId();
  const [values, setValues] = useState<Record<string, string>>({});
  // Repeatable-row fields aren't populated from the record yet (no edit
  // screen uses them) — the state exists so the modal's shape matches
  // create's, ready for a future screen to prefill.
  const [rows, setRows] = useState<FormRows>({});

  useEffect(() => {
    if (open) {
      // A fresh open starts from the record's current values, never a
      // previous attempt's leftovers.
      const prefilled: Record<string, string> = {};
      for (const field of fields) {
        const current = record[field.key];
        prefilled[field.key] = current == null ? "" : String(current);
      }
      setValues(prefilled);
      setRows({});
    }
  }, [open, fields, record]);

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
        onSubmit={onSubmit}
        onClose={onClose}
        submitLabelKey="record.save"
      />
    </Modal>
  );
}

// The whole per-screen edit affordance in one piece: the trigger button, the
// prefilled modal, its open state, and the If-Match update choreography
// (useUpdateRecord above). A screen supplies its label, fields, the record to
// prefill from, and its transport — nothing else.
export function EditAction<Updated extends { id: string }>({
  label,
  fields,
  record,
  update,
  invalidate,
  recordKey,
}: Readonly<{
  label: string;
  fields: CreateField[];
  record: Record<string, unknown> & { id: string; version?: number };
  update: (
    values: Record<string, unknown>,
    rows?: FormRows,
  ) => Promise<Updated>;
  invalidate: string;
  recordKey: string;
}>) {
  const [editing, setEditing] = useState(false);
  const mutation = useUpdateRecord({
    update,
    invalidate,
    recordKey,
    onDone: () => setEditing(false),
  });
  return (
    <>
      <Button small onClick={() => setEditing(true)} data-testid="edit-record">
        {label}
      </Button>
      <EditRecordModal
        open={editing}
        onClose={() => setEditing(false)}
        title={label}
        fields={fields}
        record={record}
        pending={mutation.isPending}
        error={mutation.isError ? mutation.error.message : null}
        onSubmit={(values, rows) =>
          mutation.mutate({ values, rows: rows ?? {} })
        }
      />
    </>
  );
}
