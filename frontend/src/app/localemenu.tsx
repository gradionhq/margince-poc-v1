// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { Check, Languages } from "lucide-react";
import {
  type KeyboardEvent,
  useCallback,
  useEffect,
  useRef,
  useState,
} from "react";
import { LOCALES, type Locale, localeNameKey, useLocale, useT } from "../i18n";

// The movement `role="menu"` obliges: Up/Down step and wrap, Home/End jump to
// the ends. A Map rather than an object literal so a keystroke can never reach
// something inherited from Object.prototype.
const NAVIGATION = new Map<string, (from: number) => number>([
  ["ArrowDown", (from) => from + 1],
  ["ArrowUp", (from) => from - 1],
  ["Home", () => 0],
  ["End", () => LOCALES.length - 1],
]);

/**
 * The open list of languages.
 *
 * Split from the trigger because focus is the whole job here: mounting IS
 * opening, so one effect can put focus on the language already in force and
 * then follow the arrows, and unmounting cannot leave a stale position behind.
 */
function LocaleList({
  current,
  onSelect,
  onDismiss,
}: Readonly<{
  current: Locale;
  onSelect: (locale: Locale) => void;
  onDismiss: () => void;
}>) {
  const t = useT();
  const items = useRef<(HTMLButtonElement | null)[]>([]);
  // The list opens on the language already in force, so the reader is told
  // where they are before they are asked to move.
  const [active, setActive] = useState(() => LOCALES.indexOf(current));

  useEffect(() => {
    items.current[active]?.focus();
  }, [active]);

  const onKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    // Tab means the reader is leaving. The menu closes behind them rather than
    // staying open, and expanded, over a page they have moved on from.
    if (event.key === "Tab") {
      onDismiss();
      return;
    }
    const move = NAVIGATION.get(event.key);
    if (!move) {
      return;
    }
    // These keys scroll the document by default; inside the menu they ARE the
    // navigation, so the page must not move underneath it.
    event.preventDefault();
    setActive((from) => (move(from) + LOCALES.length) % LOCALES.length);
  };

  return (
    <div
      className="localelist"
      role="menu"
      // Without a name a screen reader announces this second layer as "menu"
      // and nothing else — the reader is told a list opened but not of what.
      aria-label={t("locale.switchLabel")}
      onKeyDown={onKeyDown}
      // Choosing a language must not dismiss the menu this list is nested in:
      // the account menu closes on any click that reaches the document, and the
      // row it opened from is where the chosen language is then reported. This
      // list closes itself through `onSelect` instead, so nothing is lost.
      onClick={(event) => event.stopPropagation()}
    >
      {LOCALES.map((option, index) => (
        <button
          key={option}
          type="button"
          role="menuitemradio"
          aria-checked={option === current}
          // One tabstop for the whole list, riding the focused row: Tab leaves
          // the menu and the arrows move within it. A tabstop per language
          // would turn a three-item choice into a three-stop wall.
          tabIndex={index === active ? 0 : -1}
          ref={(node) => {
            items.current[index] = node;
          }}
          onClick={() => onSelect(option)}
        >
          {/* Three languages inside a document declared to be in one. Without
              its own `lang` a screen reader reads "Tiếng Việt" with the
              phonemes of whichever locale is currently on — the same WCAG 3.1.1
              rule LocaleProvider keeps for the document, applied to the control
              that changes it. Our locale codes are BCP 47 language subtags, so
              the code IS the value `lang` wants. */}
          <span lang={option}>{t(localeNameKey(option))}</span>
          {option === current && <Check size={14} aria-hidden />}
        </button>
      ))}
    </div>
  );
}

/**
 * The language control, a row in the account menu.
 *
 * A menu rather than a toggle: with more than two locales a toggle cannot say
 * where the next click lands, which is exactly the reader least able to guess.
 * It ships behaviour, not chrome: the top bar dresses its menus in one rule
 * (`shell.css`), so this file has no stylesheet of its own and `className` is
 * required — the trigger's chrome is the host's to name. The sign-in screen
 * shares no stylesheet with the top bar and offers its languages as a footer
 * row of its own instead.
 *
 * It sits INSIDE the account menu, next to the theme row, because a language is
 * a preference of this person rather than an action on the screen. Two things
 * follow from being nested in another popover, and both are load-bearing: the
 * trigger stops its own click, or the host's document-level dismissal would
 * close the account menu — and this list with it — on the way to opening; and
 * the list opens BESIDE the account menu rather than under its own row, which
 * would cover the rows below it (`shell.css`).
 */
export function LocaleMenu({ className }: Readonly<{ className: string }>) {
  const t = useT();
  const { locale, setLocale } = useLocale();
  const [open, setOpen] = useState(false);
  const wrap = useRef<HTMLDivElement | null>(null);
  const trigger = useRef<HTMLButtonElement | null>(null);

  // Closing must not strand the reader at the top of the document — this is
  // the only in-app control for changing language, so the person most likely
  // to need it is the one least able to recover from losing focus.
  //
  // Focus goes back to the trigger unless the closing gesture has already
  // handed it to another control: a click into a text field has said where
  // focus belongs and pulling it back would fight the reader. Focus on <body>
  // is the opposite case — the menu that held it has just gone away and left
  // nothing behind, which is exactly the stranding this repairs.
  const close = useCallback(() => {
    const focused = document.activeElement;
    const claimed =
      focused !== null &&
      focused !== document.body &&
      !(wrap.current?.contains(focused) ?? false);
    setOpen(false);
    if (!claimed) {
      trigger.current?.focus();
    }
  }, []);

  // Dismissal lives on the document so Escape works from anywhere in the menu
  // and any outside click closes it — the opening click is deferred past so it
  // does not close what it just opened.
  useEffect(() => {
    if (!open) {
      return;
    }
    const onKey = (event: globalThis.KeyboardEvent) => {
      if (event.key === "Escape") {
        close();
      }
    };
    const onClick = () => close();
    document.addEventListener("keydown", onKey);
    const timer = window.setTimeout(
      () => document.addEventListener("click", onClick),
      0,
    );
    return () => {
      document.removeEventListener("keydown", onKey);
      window.clearTimeout(timer);
      document.removeEventListener("click", onClick);
    };
  }, [open, close]);

  return (
    <div className="localemenu" ref={wrap}>
      <button
        type="button"
        ref={trigger}
        className={className}
        // The row reads "Language — Deutsch", and the name says the same thing
        // rather than only what the control does: a reader who arrives on it is
        // told BOTH its purpose and the language currently in force, and a
        // voice-control user can say the word they can see (WCAG 2.5.3).
        aria-label={`${t("locale.switchLabel")}: ${t(localeNameKey(locale))}`}
        aria-haspopup="menu"
        aria-expanded={open}
        // Nested in the account menu, whose dismissal listens on the document:
        // without this the click that opens the list also closes the popover the
        // list lives in. Harmless when the trigger is not nested.
        onClick={(event) => {
          event.stopPropagation();
          if (open) {
            close();
          } else {
            setOpen(true);
          }
        }}
      >
        <Languages size={15} aria-hidden />
        {t("locale.switchLabel")}
        {/* The endonym carries its own `lang`, for the same reason the rows in
            the list below do: read with the phonemes of the current locale,
            "Tiếng Việt" is not the name of anything. */}
        <span className="menuvalue" lang={locale}>
          {t(localeNameKey(locale))}
        </span>
      </button>
      {open && (
        <LocaleList
          current={locale}
          // A choice closes the menu the same way a dismissal does — through
          // `close`, so the reader lands back on the trigger, which now names
          // the language they just picked.
          onSelect={(option) => {
            setLocale(option);
            close();
          }}
          onDismiss={close}
        />
      )}
    </div>
  );
}
