import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Plus } from "lucide-react";
import { type ReactNode, useEffect, useId, useState } from "react";
import { navigate } from "../app/router";
import { Button, Modal, TextInput } from "../design-system/atoms";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";

// The shared create-record form (contacts, companies, leads, deals): each
// list screen declares its fields; the transport (which endpoint, how values
// map onto the request body) stays with the screen that owns the resource.
// Server-side validation is the truth — a 422 renders its RFC 7807 detail
// verbatim under the form, never a swallowed or re-worded error.

export type CreateFieldOption = { value: string; label: string };

export type CreateField = {
  key: string;
  label: MessageKey;
  type?: "text" | "email" | "number" | "date" | "datetime-local" | "select";
  required?: boolean;
  options?: CreateFieldOption[];
  placeholder?: string;
};

// The shared post-create choreography: refresh the list, close the modal,
// open the fresh record's 360. Screens supply only their transport.
export function useCreateRecord<Created extends { id: string }>({
  create,
  invalidate,
  screen,
  onDone,
}: Readonly<{
  create: (values: Record<string, string>) => Promise<Created>;
  invalidate: string;
  screen: string;
  onDone: () => void;
}>) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: create,
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
}: Readonly<{
  label: string;
  fields: CreateField[];
  create: (values: Record<string, string>) => Promise<Created>;
  invalidate: string;
  screen: string;
  startOpen?: boolean;
}>) {
  const [creating, setCreating] = useState(startOpen);
  const mutation = useCreateRecord({
    create,
    invalidate,
    screen,
    onDone: () => setCreating(false),
  });
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
        onSubmit={(values) => mutation.mutate(values)}
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

function fieldControl(
  field: CreateField,
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

export function CreateRecordModal({
  open,
  onClose,
  title,
  fields,
  pending,
  error,
  onSubmit,
}: Readonly<{
  open: boolean;
  onClose: () => void;
  title: string;
  fields: CreateField[];
  pending: boolean;
  error: string | null;
  onSubmit: (values: Record<string, string>) => void;
}>) {
  const t = useT();
  const headingId = useId();
  const [values, setValues] = useState<Record<string, string>>({});

  useEffect(() => {
    if (open) {
      // A fresh open starts from the fields' defaults (first select option
      // for required selects), never from a previous attempt's leftovers.
      const defaults: Record<string, string> = {};
      for (const field of fields) {
        if (field.type === "select" && field.required) {
          defaults[field.key] = field.options?.[0]?.value ?? "";
        }
      }
      setValues(defaults);
    }
  }, [open, fields]);

  const requiredMissing = fields.some(
    (field) => field.required && !(values[field.key] ?? "").trim(),
  );

  return (
    <Modal open={open} onClose={onClose} labelledBy={headingId}>
      <h2 id={headingId} className="t-h2" style={{ marginBottom: 12 }}>
        {title}
      </h2>
      <form
        onSubmit={(event) => {
          event.preventDefault();
          onSubmit(values);
        }}
        style={{ display: "flex", flexDirection: "column", gap: 10 }}
      >
        {fields.map((field) => {
          const fieldId = `${headingId}-${field.key}`;
          return (
            <div className="field" key={field.key}>
              <label className="t-label" htmlFor={fieldId}>
                {t(field.label)}
                {field.required ? " *" : ""}
              </label>
              {fieldControl(field, fieldId, values[field.key] ?? "", (next) =>
                setValues((current) => ({ ...current, [field.key]: next })),
              )}
            </div>
          );
        })}
        {error && (
          <p className="t-caption" style={{ color: "var(--danger)" }}>
            {error}
          </p>
        )}
        <div style={{ display: "flex", gap: 8, justifyContent: "flex-end" }}>
          <Button small type="button" onClick={onClose}>
            {t("create.cancel")}
          </Button>
          <Button
            small
            variant="primary"
            type="submit"
            disabled={pending || requiredMissing}
          >
            {pending ? t("create.saving") : t("create.save")}
          </Button>
        </div>
      </form>
    </Modal>
  );
}
