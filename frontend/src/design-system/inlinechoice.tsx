import { ChevronDown } from "lucide-react";
import { type ReactNode, useEffect, useId, useRef, useState } from "react";
import { useT } from "../i18n";
import { BusyMark, TextInput } from "./atoms";
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
  // Set right before a close that should return focus to the resting
  // trigger, read once that trigger has actually remounted (see the effect
  // below). Every exit from the editing view sets it — a value picked and a
  // value abandoned both put the reader back where they started — because
  // what the rule is about is leaving the view, not which answer they left
  // with. Neither path can focus the trigger directly: at the moment they
  // run, `editing` is still true, so the resting button — only rendered in
  // the `!editing` branch — does not exist yet and `trigger.current` is null.
  const restoreFocus = useRef(false);
  const fieldId = useId();
  const errorId = useId();

  const close = () => {
    setEditing(false);
    setPending(null);
    setFailure(null);
  };

  const revert = () => {
    // The a11y counterpart of the value snapping back into plain text: a
    // keyboard user who opened this and backed out lands on the same trigger
    // they pressed, not dropped to the document body.
    restoreFocus.current = true;
    close();
  };

  // Runs after the resting trigger has actually mounted, so the focus call
  // lands on a live node instead of the null ref `revert` would have hit.
  useEffect(() => {
    if (!editing && restoreFocus.current) {
      restoreFocus.current = false;
      trigger.current?.focus();
    }
  }, [editing]);

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
      restoreFocus.current = true;
      setEditing(false);
      return;
    }
    setSaving(true);
    setFailure(null);
    try {
      await onSave(next);
      // Picking a value leaves the editing view exactly as backing out of it
      // does, so it lands the reader on the same trigger: the rule is about
      // leaving the view at all, not about which answer they left with.
      restoreFocus.current = true;
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
      className="inlinechoice-edit"
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
        // Disabled AND busy, which are not the same claim. `disabled` is what
        // stops a second choice landing on top of a write that has not answered
        // yet; `aria-busy` is what says the control is working rather than
        // refused, and it is what the stylesheet keys the paint off — a write in
        // flight keeps its full ink and takes the waiting cursor, exactly as
        // Switch has since it was written.
        disabled={saving}
        aria-busy={saving || undefined}
        aria-invalid={failure ? true : undefined}
        aria-describedby={failure ? errorId : undefined}
        // The click that started editing already meant "show me the
        // options" — opening on mount spends that same click rather than
        // asking for a second one.
        openOnMount
        // Closing the popup without picking anything (a press outside, the
        // trigger scrolling away) is the one closed transition that is not
        // also a commit — Select's own `commit` never routes through this,
        // only `cancel`/an outside dismissal do. Tab is deliberately routed
        // to `onLeave`, not here: the reader already moved forward, and
        // refocusing this trigger would drag them back to where they left.
        onCancel={revert}
        onLeave={close}
        onChange={(next) => {
          setPending(next);
          void commit(next);
        }}
      />
      {saving && <BusyMark />}
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
  const field = useRef<HTMLInputElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  // Set by the two exits the reader takes without leaving this field —
  // Escape, and Enter on a save that succeeds — and read once the resting
  // trigger has remounted, exactly as InlineChoice does. A blur-commit
  // deliberately does NOT set it: the reader is already somewhere else, and
  // dragging focus back here would undo the move they just made.
  const restoreFocus = useRef(false);
  const fieldId = useId();
  const errorId = useId();

  // The click that opened this asked to TYPE here, so the caret belongs in the
  // field without a second click. It is also what makes every exit rule below
  // hold at all: Escape, Enter and the blur-commit are all keyboard and focus
  // events on this input, and an input nobody ever focused receives none of
  // them — the box would then sit open until it was clicked into, including
  // after the reader had moved on to another field.
  useEffect(() => {
    if (editing) {
      field.current?.focus();
      return;
    }
    if (restoreFocus.current) {
      restoreFocus.current = false;
      trigger.current?.focus();
    }
  }, [editing]);

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
        ref={trigger}
        type="button"
        className="inline-editable"
        // An empty field is an invitation to fill it and reads as one; a field
        // with a value is a value, and dressing the fact as a link would say
        // it is a place to go.
        data-empty={value ? undefined : "true"}
        aria-label={t("inlineChoice.change", { field: label })}
        title={t("inlineChoice.change", { field: label })}
        onClick={() => {
          setDraft(value);
          setFailure(null);
          // A previous Escape can leave this set if the browser never
          // delivered the unmount's blur to this node's React handler — the
          // one place `onBlur` below clears it. Cleared here too, the one
          // path every new edit session always runs, so a stale flag cannot
          // silently swallow this session's first blur commit.
          cancelling.current = false;
          setEditing(true);
        }}
      >
        {shown}
      </button>
    );
  }

  // `byKeyboard` is what tells the two exits apart: an Enter press ends the
  // edit without moving the reader anywhere, so focus goes back to the value
  // they were on; a blur means they have already moved, and is left alone.
  const commit = async (byKeyboard = false) => {
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
      restoreFocus.current = byKeyboard;
      setEditing(false);
      return;
    }
    setSaving(true);
    setFailure(null);
    try {
      await onSave(next);
      restoreFocus.current = byKeyboard;
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
        ref={field}
        id={fieldId}
        value={draft}
        maxLength={maxLength}
        // `readOnly`, not `disabled`, and this is the one that had to change.
        // A disabled field leaves the tab order, so a reader who pressed Enter
        // and then Tab was thrown to the far side of the form for as long as
        // the write took. Read-only holds the field, holds the caret, and
        // refuses the keystroke — which is the whole of what a write in flight
        // needs. `aria-busy` carries the reason.
        readOnly={saving}
        aria-busy={saving || undefined}
        aria-invalid={failure ? true : undefined}
        aria-describedby={failure ? errorId : undefined}
        onChange={(event) => setDraft(event.target.value)}
        onKeyDown={(event) => {
          if (event.key === "Enter") {
            event.preventDefault();
            void commit(true);
          }
          if (event.key === "Escape") {
            cancelling.current = true;
            restoreFocus.current = true;
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
      {saving && <BusyMark />}
      {failure && (
        <span id={errorId} role="alert" className="form-error">
          {failure}
        </span>
      )}
    </span>
  );
}
