// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { type DragEvent, useState } from "react";
import { Field, type FieldControl } from "./atoms";
import "./filedropzone.css";

// FileDropzone: choosing a file, by drop AND by click, from one control.
//
// Both, not one. Dropping is what a reader reaches for with a document already
// in front of them; clicking is what works from a keyboard, from a phone, and
// from a screen reader. A drop zone with no real input behind it is a box that
// silently excludes everyone not holding a mouse, which is the usual way this
// pattern ships.
//
// So there is exactly ONE control here: a transparent file input stretched
// across the whole area (see filedropzone.css). A click anywhere lands on the
// input itself, with no handler forwarding it and nothing to keep in sync; the
// drag handlers sit on that same input rather than on the box around it, so
// nothing in this component is interactive except the control that owns the
// value. Everything else — the border, the state text — is inert chrome.

/**
 * A labelled control for picking one file — the ordinary way in.
 *
 * `onPick` fires only with a file — an empty selection (the picker opened and
 * cancelled, a drop carrying no files) leaves the current choice alone, because
 * cancelling a picker is not the same act as clearing a field, and a caller
 * that could not tell them apart would discard a file the reader already chose.
 *
 * A caller that needs its OWN `Field` — because it renders more inside it than
 * the zone, a list of what is already filed, say — takes `FileDropzoneControl`
 * instead and passes the control props down. Nesting this inside another `Field`
 * would label the same input twice.
 */
export function FileDropzone({
  label,
  hint,
  emptyLabel,
  file,
  onPick,
}: Readonly<{
  label: string;
  hint?: string;
  // What the zone says before anything is chosen. The caller owns it because
  // only the caller knows what kind of file it is asking for.
  emptyLabel: string;
  file?: File;
  onPick: (file: File) => void;
}>) {
  return (
    <Field label={label} hint={hint}>
      {(control) => (
        <FileDropzoneControl
          control={control}
          emptyLabel={emptyLabel}
          file={file}
          onPick={onPick}
        />
      )}
    </Field>
  );
}

/**
 * The zone alone, for a caller that owns the `Field` around it.
 *
 * `control` is whatever that `Field` handed its child — the id, the required
 * flag and the hint association. Passing it through is what keeps ONE label on
 * ONE input when the caller renders other things beside the zone.
 */
export function FileDropzoneControl({
  control,
  emptyLabel,
  file,
  onPick,
}: Readonly<{
  control: FieldControl;
  emptyLabel: string;
  file?: File;
  onPick: (file: File) => void;
}>) {
  const [over, setOver] = useState(false);

  const take = (chosen: FileList | null) => {
    const first = chosen?.[0];
    if (first) {
      onPick(first);
    }
  };

  return (
    // An inert div. The zone is not a second label and not a widget: the
    // input stretched across it is the only control here, and `Field` has
    // already labelled that input by id. A <label> wrapper would name the
    // input a SECOND time and fold the chosen filename into its accessible
    // name, so the control would announce as "File order_form.txt" — the
    // value baked into the name, changing every time a file is picked.
    <div className={over ? "fdz dragover" : "fdz"}>
      <input
        {...control}
        type="file"
        className="fdz-input"
        // Cleared after every pick. A browser fires no change event when
        // the SAME path is chosen again, and choosing it again is the
        // natural next move after a caller clears the field — which is
        // exactly what the add-document dialog does when an upload half
        // fails. Without this the second pick is silently inert.
        onChange={(event) => {
          const chosen = event.target.files;
          take(chosen);
          event.target.value = "";
        }}
        // The drag handlers live on the INPUT, which covers the whole zone,
        // so they need no role invented for them and the drop lands on the
        // control that owns the value.
        onDragOver={(event: DragEvent<HTMLInputElement>) => {
          // Without this the browser navigates to the dropped file, which
          // loses both the file and the form the reader had filled in.
          event.preventDefault();
          setOver(true);
        }}
        onDragLeave={() => setOver(false)}
        onDrop={(event: DragEvent<HTMLInputElement>) => {
          event.preventDefault();
          setOver(false);
          take(event.dataTransfer.files);
        }}
      />
      {/* A live region, and it has to be: the input's value is cleared
              after every pick (see above), so the control itself announces "no
              file chosen" whatever is actually held. This text is the only
              place the choice is stated, so it must reach a screen reader — and
              as a status rather than as part of the control's name, which would
              rename the control on every pick. `polite` because a file the
              reader just chose is a confirmation, not an interruption. */}
      <span
        aria-live="polite"
        className={file ? "fdz-label chosen" : "fdz-label"}
      >
        {file ? file.name : emptyLabel}
      </span>
    </div>
  );
}
