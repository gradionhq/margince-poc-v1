// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { X } from "lucide-react";
import { type KeyboardEvent, useState } from "react";
import "./tokeninput.css";

/**
 * A set of short values a reader builds one at a time — the control the `in`
 * operator needs, where a clause's operand is a LIST (`region ∈ {DE, AT, CH}`)
 * rather than a scalar.
 *
 * Why not a multi-select `Select`: `in` is used where the candidate set is not
 * enumerable in advance. A picklist field's `in` could offer its options, but a
 * text field's cannot — nobody can list every city — and shipping two different
 * controls for one operator would put the difference in front of the reader for
 * no reason they care about.
 *
 * The interaction, and why each half exists:
 *
 *  - **Enter commits** the typed text as a token. Comma does too, because a
 *    reader pasting `DE, AT, CH` expects three tokens and would otherwise get
 *    one; the paste path splits on comma for the same reason.
 *  - **Backspace on an empty box removes the last token**, which is the
 *    convention every tag input shares and the only way to correct a typo
 *    without reaching for the mouse.
 *  - **A duplicate is dropped silently.** `in` is a set — admitting `DE` twice
 *    would change nothing about what matches, so refusing it with a message
 *    would be noise about a distinction the engine does not make.
 *  - **Blank is dropped.** Enter on an empty box does nothing rather than adding
 *    an empty token, which would compile to a predicate matching the empty
 *    string.
 *
 * Every token carries a remove control rather than relying on Backspace alone:
 * the keyboard path is for the reader mid-flow, and the button is for the one
 * returning to a filter they built last week.
 */
export type TokenInputProps = Readonly<{
  values: readonly string[];
  onChange: (values: readonly string[]) => void;
  placeholder?: string;
  disabled?: boolean;
  id?: string;
  "aria-label"?: string;
  "aria-labelledby"?: string;
  "aria-describedby"?: string;
  "aria-invalid"?: boolean;
  required?: boolean;
}>;

export function TokenInput({
  values,
  onChange,
  placeholder,
  disabled,
  ...aria
}: TokenInputProps) {
  const [typed, setTyped] = useState("");

  const commit = (raw: string) => {
    // One paste can carry several values; one keystroke carries one. Splitting
    // both ways through here keeps the two paths from disagreeing about what a
    // token is.
    const fresh = raw
      .split(",")
      .map((part) => part.trim())
      .filter((part) => part !== "" && !values.includes(part));
    if (fresh.length > 0) {
      onChange([...values, ...fresh]);
    }
    setTyped("");
  };

  const onKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (event.key === "Enter" || event.key === ",") {
      // Enter must not submit the surrounding form: the reader is adding a
      // value, not finishing the filter.
      event.preventDefault();
      commit(typed);
      return;
    }
    if (event.key === "Backspace" && typed === "" && values.length > 0) {
      onChange(values.slice(0, -1));
    }
  };

  return (
    // No click handler on the frame: the box already flexes to fill whatever the
    // tokens leave (see token-box in the stylesheet), so a click in the empty
    // part of the row lands on the input itself. A handler here would make a
    // static element interactive to buy back only the gaps BETWEEN tokens, which
    // is not worth an a11y exception.
    <span
      className={`token-input input ${disabled ? "is-disabled" : ""}`.trim()}
    >
      {values.map((value) => (
        <span key={value} className="token">
          {value}
          <button
            type="button"
            className="token-remove"
            disabled={disabled}
            aria-label={`Remove ${value}`}
            onClick={() => onChange(values.filter((v) => v !== value))}
          >
            <X size={12} aria-hidden />
          </button>
        </span>
      ))}
      <input
        {...aria}
        className="token-box"
        value={typed}
        disabled={disabled}
        placeholder={values.length === 0 ? placeholder : undefined}
        onChange={(event) => setTyped(event.target.value)}
        onKeyDown={onKeyDown}
        // A value left typed but not committed would be silently dropped when
        // the reader clicks away, so blur commits it.
        onBlur={() => commit(typed)}
      />
    </span>
  );
}
