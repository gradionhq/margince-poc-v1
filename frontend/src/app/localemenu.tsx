// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { Check } from "lucide-react";
import { useEffect, useState } from "react";
import { LOCALES, localeNameKey, useLocale, useT } from "../i18n";
import "./localemenu.css";

/**
 * The language control, wherever a surface has room for it.
 *
 * A menu rather than a toggle: with more than two locales a toggle cannot say
 * where the next click lands, which is exactly the reader least able to guess.
 * Carries its own styling rather than borrowing the top bar's `.iconbtn` rule,
 * because that rule is scoped to `.topbar` and the sign-in screen renders none.
 */
export function LocaleMenu({
  className,
}: Readonly<{ className?: string }> = {}) {
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
        className={className ? `localetrigger ${className}` : "localetrigger"}
        aria-label={t("locale.switchLabel")}
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
