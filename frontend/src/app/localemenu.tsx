// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { Check } from "lucide-react";
import { useEffect, useState } from "react";
import { LOCALES, localeNameKey, useLocale, useT } from "../i18n";

/**
 * The language control in the top bar.
 *
 * A menu rather than a toggle: with more than two locales a toggle cannot say
 * where the next click lands, which is exactly the reader least able to guess.
 * It ships behaviour, not chrome: the top bar dresses both of its menus in one
 * rule (`shell.css`), so this file has no stylesheet of its own and `className`
 * is required — the trigger's chrome is the host's to name. The sign-in screen
 * shares no stylesheet with the top bar and offers its languages as a footer
 * row of its own instead.
 */
export function LocaleMenu({ className }: Readonly<{ className: string }>) {
  const t = useT();
  const { locale, setLocale } = useLocale();
  const [open, setOpen] = useState(false);

  // Dismissal lives on the document so Escape works from anywhere in the menu
  // and any outside click closes it — the opening click is deferred past so it
  // does not close what it just opened.
  useEffect(() => {
    if (!open) {
      return;
    }
    const onKey = (event: globalThis.KeyboardEvent) => {
      if (event.key === "Escape") {
        setOpen(false);
      }
    };
    const onClick = () => setOpen(false);
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
  }, [open]);

  return (
    <div className="localemenu">
      <button
        type="button"
        className={className}
        // The visible face is a two-letter code, which a screen reader spells
        // out and a voice-control user cannot say. Naming the current language
        // in full keeps the control's purpose AND its present state audible —
        // the toggle it replaced announced the state for free (WCAG 2.5.3).
        aria-label={`${t("locale.switchLabel")}: ${t(localeNameKey(locale))}`}
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={() => setOpen((current) => !current)}
      >
        <span className="t-mono">{locale.toUpperCase()}</span>
      </button>
      {open && (
        <div className="localelist" role="menu">
          {LOCALES.map((option) => (
            <button
              key={option}
              type="button"
              role="menuitemradio"
              aria-checked={option === locale}
              onClick={() => setLocale(option)}
            >
              {t(localeNameKey(option))}
              {option === locale && <Check size={14} aria-hidden />}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}
