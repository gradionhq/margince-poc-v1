// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { type DragEvent, useState } from "react";
import { Field } from "./atoms";
import "./filedropzone.css";

// FileDropzone: choosing a file, by drop AND by click, from one control.
//
// Both, not one. Dropping is what a reader reaches for with a document already
// in front of them; clicking is what works from a keyboard, from a phone, and
// from a screen reader. A drop zone with no real input behind it is a box that
// silently excludes everyone not holding a mouse, which is the usual way this
// pattern ships.
//
// So the input is the control and the zone is chrome around it: a transparent
// file input stretched across the whole area (see filedropzone.css) means a
// click anywhere lands on the input itself, with no handler forwarding it and
// nothing to keep in sync. The drag handlers are the only behaviour this
// component adds, and they add an affordance rather than replacing one.

/**
 * A labelled control for picking one file.
 *
 * `onPick` fires only with a file — an empty selection (the picker opened and
 * cancelled, a drop carrying no files) leaves the current choice alone, because
 * cancelling a picker is not the same act as clearing a field, and a caller
 * that could not tell them apart would discard a file the reader already chose.
 */
export function FileDropzone({
  label,
  hint,
  emptyLabel,
  file,
  accept,
  onPick,
}: Readonly<{
  label: string;
  hint?: string;
  // What the zone says before anything is chosen. The caller owns it because
  // only the caller knows what kind of file it is asking for.
  emptyLabel: string;
  file?: File;
  accept?: string;
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
    <Field label={label} hint={hint}>
      {(control) => (
        // A LABEL, not a div: it owns the file input it wraps, so the zone is
        // part of a control rather than an interactive box of unknown purpose,
        // and the drag handlers decorate something a screen reader can already
        // announce. A div here would need a role invented for it.
        <label
          className={over ? "fdz dragover" : "fdz"}
          onDragOver={(event: DragEvent<HTMLLabelElement>) => {
            // Without this the browser navigates to the dropped file, which
            // loses both the file and the form the reader had filled in.
            event.preventDefault();
            setOver(true);
          }}
          onDragLeave={() => setOver(false)}
          onDrop={(event: DragEvent<HTMLLabelElement>) => {
            event.preventDefault();
            setOver(false);
            take(event.dataTransfer.files);
          }}
        >
          <input
            {...control}
            type="file"
            accept={accept}
            className="fdz-input"
            onChange={(event) => take(event.target.files)}
          />
          <span className={file ? "fdz-label chosen" : "fdz-label"}>
            {file ? file.name : emptyLabel}
          </span>
        </label>
      )}
    </Field>
  );
}
