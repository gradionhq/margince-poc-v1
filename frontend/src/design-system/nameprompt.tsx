// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useState } from "react";
import { Button, Field, TextInput } from "./atoms";
import { ConfirmModal } from "./confirmmodal";

// "Give this a name, then save it" — the one dialog for a write whose only input
// is a name the reader chooses.
//
// It owns three things and delegates the rest: the name, the rule that an empty
// name cannot be saved, and clearing the box on success. The chrome — heading,
// Cancel/Confirm pair, the in-flight and refused readings, the error's live
// region — is ConfirmModal's, which is what keeps this from becoming a second
// dialog. It exists because that composition was hand-rolled with a raw `Modal`
// in the saved-view action, and a second surface needing the same question would
// have hand-rolled it again slightly differently.
//
// The name is NOT held for the caller between openings: a dialog that reopens
// carrying the last name typed offers to save something under a name the reader
// has already used, which on a create surface is how a duplicate gets made.

export function NamePrompt({
  trigger,
  title,
  label,
  confirmLabel,
  pending = false,
  problem,
  onSave,
}: Readonly<{
  /** The button that opens it, already translated. */
  trigger: string;
  title: string;
  /** The field's label — "Name" is the usual one, but a caller may be specific. */
  label: string;
  confirmLabel: string;
  pending?: boolean;
  /** The failure, already read into the reader's language. */
  problem?: string;
  /**
   * Called with the trimmed name and a `done` callback to run once the write has
   * landed. The caller closes the dialog by calling it, rather than this
   * component guessing from a promise: a mutation that fails must leave the
   * dialog open with what was typed still in it.
   */
  onSave: (name: string, done: () => void) => void;
}>) {
  const [open, setOpen] = useState(false);
  const [name, setName] = useState("");

  const close = () => {
    setOpen(false);
    setName("");
  };

  const trimmed = name.trim();

  return (
    <>
      <Button small onClick={() => setOpen(true)}>
        {trigger}
      </Button>
      <ConfirmModal
        open={open}
        onClose={close}
        title={title}
        confirmLabel={confirmLabel}
        // The precondition, not the in-flight state — ConfirmModal draws those
        // two differently on purpose, and folding them would tell a reader who
        // has lost focus that their write is going.
        confirmDisabled={trimmed === ""}
        pending={pending}
        error={problem ?? null}
        onConfirm={() => {
          if (trimmed === "") {
            return;
          }
          onSave(trimmed, close);
        }}
      >
        <Field label={label}>
          {(control) => (
            <TextInput
              {...control}
              value={name}
              onChange={(event) => setName(event.target.value)}
            />
          )}
        </Field>
      </ConfirmModal>
    </>
  );
}
