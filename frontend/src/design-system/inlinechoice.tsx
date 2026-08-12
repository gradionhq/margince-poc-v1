import { ChevronDown } from "lucide-react";
import { type ReactNode, useId, useRef, useState } from "react";
import { useT } from "../i18n";
import { TextInput } from "./atoms";
import "./inlinechoice.css";
import { Select, type SelectOption } from "./select";

// One value a reader can change without leaving the page they are reading.
//
// It exists because burying a field in an edit modal is not neutral: a value
// nobody can change in place is a value nobody changes. Account lifecycle and
// owner are the two a rep moves during a call, and both were reachable only
// through a form that also asked about legal names and size bands.
//
// The interaction is edit-in-place, not a form: at rest the value reads as
// plain text — no box, no accent, nothing saying "control" — and only a hover
// or a keyboard focus reveals the affordance (an underline, and for a chooser
// a caret) that this can be changed. A click turns the value itself into the
// live control in the same spot; there is no separate Save — a chooser
// commits on picking, a text field commits on Enter or on losing focus.
//
// The rules it keeps, all failure modes rather than polish:
//
//   - A viewer who may NOT change the value sees the VALUE, with no hover
//     affordance at all. A control that looks editable and then refuses is
//     a defect already fixed once; plain text says what is true.
//   - A save that fails leaves the control open on what the user chose,
//     the refusal shown right beside it. Snapping back to the old value on a
//     version conflict would discard their answer and tell them nothing.
//   - Escape reverts and closes, so a reader who opened it to LOOK can get
//     out without changing anything, whether or not a save is failing.
//   - Choosing or retyping the value already stored is not an edit: no
//     audit row for a change that did not happen, however often blur fires.

export function InlineChoice({
  label,
  hideLabel,
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
  // Suppresses the VISIBLE "label: " prefix without touching the accessible
  // name: `label` still drives the change button's aria-label and the edit
  // form's own label, both read to assistive tech, sr-only rather than
  // dropped. For a caller whose surrounding layout already prints the field's
  // name once (FieldGrid's own label column) — printing it a second time here
  // is the field naming itself twice, not a second fact.
  hideLabel?: boolean;
  value: string;
  options: readonly SelectOption[];
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
  const container = useRef<HTMLSpanElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  const fieldId = useId();
  const errorId = useId();

  const revert = () => {
    setEditing(false);
    setPending(null);
    setFailure(null);
    // The a11y counterpart of the value snapping back into plain text: a
    // keyboard user who opened this and backed out lands on the same trigger
    // they pressed, not dropped to the document body.
    trigger.current?.focus();
  };

  if (!canEdit || !editing) {
    return (
      <span>
        {!hideLabel && <>{label}: </>}
        {canEdit ? (
          <button
            ref={trigger}
            type="button"
            className="inline-editable inline-editable-choice"
            // aria-label, not title: the button's content is the VALUE, so
            // without this a screen reader announces "Not assessed, button" —
            // the state, with no hint that pressing it changes anything. title
            // does not override content for the accessible name; aria-label
            // does, and stays as the tooltip for a pointer. Carried
            // regardless of `hideLabel`: the visible prefix is what a sighted
            // reader does not need twice, not the accessible name a screen
            // reader needs at all.
            aria-label={t("inlineChoice.change", { field: label })}
            title={t("inlineChoice.change", { field: label })}
            onClick={() => {
              setPending(value);
              setFailure(null);
              setEditing(true);
            }}
          >
            {render(value)}
            <ChevronDown
              className="inline-editable-caret"
              size={12}
              aria-hidden="true"
            />
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
    // Escape only reaches this handler once the popup itself is closed — an
    // OPEN popup's own keydown claims and stops the Escape press, which is
    // exactly the case (a picker left open on a failed save) that this
    // control has no other way to back out of.
    // biome-ignore lint/a11y/noStaticElementInteractions: keydown here only ever catches an Escape the Select below already declined to claim; the interactive element is that Select's own trigger.
    <span
      ref={container}
      onKeyDown={(event) => {
        if (event.key === "Escape") {
          revert();
        }
      }}
    >
      <label className={hideLabel ? "sr-only" : undefined} htmlFor={fieldId}>
        {label}
        {!hideLabel && ": "}
      </label>
      <Select
        id={fieldId}
        value={chosen}
        options={options}
        disabled={saving}
        aria-invalid={failure ? true : undefined}
        aria-describedby={failure ? errorId : undefined}
        // The click that started editing already meant "show me the
        // options" — opening on mount spends that same click rather than
        // asking for a second one.
        openOnMount
        // Closing the popup without picking anything (a press outside, the
        // trigger scrolling away, Tab) is the one closed transition that is
        // not also a commit — Select's own `commit` never routes through
        // this, only `cancel`/`leave`/an outside dismissal do.
        onCancel={revert}
        onChange={(next) => {
          setPending(next);
          void commit(next);
        }}
      />
      {failure && (
        <span id={errorId} role="alert" className="form-error">
          {failure}
        </span>
      )}
    </span>
  );
}

// InlineText is InlineChoice for a free-text value: the company's one-line
// description, edited where it is read rather than inside a form that also
// asks about legal names and size bands.
//
// It keeps the same rules as InlineChoice above — a viewer who may not edit
// sees the value with no hover affordance, a failed save keeps the typed
// text and shows the refusal beside the field, Escape reverts — and adds the
// two a text field needs that a chooser does not: an explicit MOMENT of
// commit (a chooser commits the instant something is picked; typing has no
// such moment, so Enter or losing focus stands in for it), and something to
// press when the value is empty, since there is no text to click on.
export function InlineText({
  label,
  value,
  placeholder,
  maxLength,
  canEdit,
  readOnlyReason,
  onSave,
}: Readonly<{
  label: string;
  value: string;
  // What the pressable reads as when the value is empty. Without it an unset
  // description is a zero-width button nobody can find.
  placeholder: string;
  maxLength?: number;
  canEdit: boolean;
  readOnlyReason?: string;
  // Returns nothing on success and throws on failure; the thrown message is
  // what the reader is shown.
  onSave: (next: string) => Promise<void>;
}>) {
  const t = useT();
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(value);
  const [saving, setSaving] = useState(false);
  const [failure, setFailure] = useState<string | null>(null);
  // Escape unmounts this input, which the browser reads as focus leaving it —
  // a blur this control did not ask to commit. Set true for exactly the tick
  // between the Escape keydown and that blur, so the blur handler below can
  // tell "the reader cancelled" from "the reader tabbed away" and skip the
  // commit only for the former.
  const cancelling = useRef(false);
  const fieldId = useId();
  const errorId = useId();

  if (!canEdit || !editing) {
    const shown = value || placeholder;
    if (!canEdit) {
      // `placeholder` is written for someone about to press it ("Add legal
      // name") — showing it plain to a viewer who cannot edit reads as an
      // instruction aimed at them. `field.unset` is the neutral fact instead,
      // the same fallback the grid's own read-only rows (owner, domain,
      // address) already use, so an empty field never reads as either an
      // invitation this viewer cannot act on or a blank the row forgot to
      // fill.
      return (
        <span className="inlinetext" title={readOnlyReason}>
          {value || t("field.unset")}
        </span>
      );
    }
    return (
      <button
        type="button"
        className="inline-editable"
        aria-label={t("inlineChoice.change", { field: label })}
        title={t("inlineChoice.change", { field: label })}
        onClick={() => {
          setDraft(value);
          setFailure(null);
          setEditing(true);
        }}
      >
        {shown}
      </button>
    );
  }

  const commit = async () => {
    // A commit already in flight owns the next state transition; a second
    // one racing in behind it (Enter, then the blur disabling the input for
    // `saving` fires synchronously) would double-send the same edit.
    if (saving) {
      return;
    }
    const next = draft.trim();
    // Saving what is already stored writes an audit row for a change that did
    // not happen. Blur fires on every exit now, so this guard is what keeps
    // "clicked in, typed nothing, clicked out" silent.
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
      // The draft survives and the input stays mounted right where the
      // reader left it — pulling focus back after a failed blur-commit would
      // be a second surprise on top of the refusal.
      setFailure(err instanceof Error ? err.message : String(err));
    } finally {
      setSaving(false);
    }
  };

  return (
    <span className="inlinetext-edit">
      <label className="sr-only" htmlFor={fieldId}>
        {label}
      </label>
      <TextInput
        id={fieldId}
        value={draft}
        maxLength={maxLength}
        disabled={saving}
        aria-invalid={failure ? true : undefined}
        aria-describedby={failure ? errorId : undefined}
        onChange={(event) => setDraft(event.target.value)}
        onKeyDown={(event) => {
          if (event.key === "Enter") {
            event.preventDefault();
            void commit();
          }
          if (event.key === "Escape") {
            cancelling.current = true;
            setDraft(value);
            setEditing(false);
          }
        }}
        onBlur={() => {
          if (cancelling.current) {
            cancelling.current = false;
            return;
          }
          void commit();
        }}
      />
      {failure && (
        <span id={errorId} role="alert" className="form-error">
          {failure}
        </span>
      )}
    </span>
  );
}
