import { type ReactNode, useEffect, useId, useRef, useState } from "react";
import { useT } from "../i18n";

// One value a reader can change without leaving the page they are reading.
//
// It exists because burying a field in an edit modal is not neutral: a value
// nobody can change in place is a value nobody changes. Account lifecycle and
// owner are the two a rep moves during a call, and both were reachable only
// through a form that also asked about legal names and size bands.
//
// The rules it keeps, all four of which are failure modes rather than polish:
//
//   - A viewer who may NOT change the value sees the VALUE, not a greyed-out
//     control. A disabled select says "you could do this" and then refuses;
//     plain text says what is true.
//   - A save that fails leaves the picker open on what the user chose. Snapping
//     back to the old value on a version conflict discards their answer and
//     tells them nothing.
//   - The refusal is shown next to the control that caused it, not as a toast
//     somewhere else on the page.
//   - Escape reverts and closes, so a reader who opened it to LOOK at the
//     options can get out without changing anything.

export type InlineChoiceOption = { value: string; label: string };

export function InlineChoice({
  label,
  value,
  options,
  canEdit,
  readOnlyReason,
  render,
  onSave,
}: Readonly<{
  // Names the field, for the reader and for assistive tech. A bare value in a
  // header row reads as one more fact among many.
  label: string;
  value: string;
  options: readonly InlineChoiceOption[];
  canEdit: boolean;
  // Why this is not editable, when there is a reason worth saying — an archived
  // record, an overlay-mirrored one. Absent means "you simply may not", which
  // needs no sentence.
  readOnlyReason?: string;
  // How the current value reads when the control is closed. A raw value is
  // rarely what a human should see: a lifecycle is a badge, an owner is a name
  // the caller has to resolve.
  render: (value: string) => ReactNode;
  // Returns nothing on success and throws on failure; the thrown message is
  // what the reader is shown. Version conflicts, validation and permission
  // refusals all arrive here as the server's own sentence.
  onSave: (next: string) => Promise<void>;
}>) {
  const t = useT();
  const [editing, setEditing] = useState(false);
  const [pending, setPending] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [failure, setFailure] = useState<string | null>(null);
  const selectRef = useRef<HTMLSelectElement>(null);
  const fieldId = useId();
  const errorId = useId();

  // Focus follows the control the click opened, so the keyboard lands where the
  // mouse did and a screen reader announces the field rather than the page.
  useEffect(() => {
    if (editing) {
      selectRef.current?.focus();
    }
  }, [editing]);

  if (!canEdit || !editing) {
    return (
      <span>
        {label}:{" "}
        {canEdit ? (
          <button
            type="button"
            className="link-button"
            // aria-label, not title: the button's content is the VALUE, so
            // without this a screen reader announces "Not assessed, button" —
            // the state, with no hint that pressing it changes anything. title
            // does not override content for the accessible name; aria-label
            // does, and stays as the tooltip for a pointer.
            aria-label={t("inlineChoice.change", { field: label })}
            title={t("inlineChoice.change", { field: label })}
            onClick={() => {
              setPending(value);
              setFailure(null);
              setEditing(true);
            }}
          >
            {render(value)}
          </button>
        ) : (
          <span title={readOnlyReason}>{render(value)}</span>
        )}
      </span>
    );
  }

  const chosen = pending ?? value;
  const commit = async (next: string) => {
    // Choosing what is already set is not an edit. Sending it would write an
    // audit row for a change that did not happen.
    if (next === value) {
      setEditing(false);
      return;
    }
    setSaving(true);
    setFailure(null);
    try {
      await onSave(next);
      setEditing(false);
    } catch (err) {
      // The draft survives: `pending` still holds what they chose, and the
      // control stays open on it. A save that fails must not also lose the
      // answer the user gave.
      setFailure(err instanceof Error ? err.message : String(err));
    } finally {
      setSaving(false);
    }
  };

  return (
    <span>
      <label htmlFor={fieldId}>{label}: </label>
      <select
        className="input"
        id={fieldId}
        ref={selectRef}
        value={chosen}
        disabled={saving}
        aria-busy={saving}
        aria-invalid={failure ? true : undefined}
        aria-describedby={failure ? errorId : undefined}
        onChange={(event) => {
          setPending(event.target.value);
          void commit(event.target.value);
        }}
        onKeyDown={(event) => {
          if (event.key === "Escape") {
            setPending(null);
            setFailure(null);
            setEditing(false);
          }
        }}
        onBlur={() => {
          // A failed save keeps the control open: closing on blur would take
          // the refusal off screen along with the value that caused it.
          if (!saving && !failure) {
            setEditing(false);
          }
        }}
      >
        {options.map((option) => (
          <option key={option.value} value={option.value}>
            {option.label}
          </option>
        ))}
      </select>
      {failure && (
        <span id={errorId} role="alert" className="form-error">
          {failure}
        </span>
      )}
    </span>
  );
}
