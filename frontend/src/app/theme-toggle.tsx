// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { Moon, Sun } from "lucide-react";
import { useT } from "../i18n";
import "./theme-toggle.css";
import { toggleTheme, useTheme } from "./theme";

/**
 * The light/dark control, wherever a surface has room for it.
 *
 * The top bar had the only one for a long time, which left the two surfaces a
 * reader meets FIRST — sign-in and onboarding — with no way to change a theme
 * they can already see. This carries its own styling rather than borrowing the
 * top bar's `.iconbtn` rule, because that rule is scoped to `.topbar` and
 * neither of those surfaces renders one.
 *
 * One button, so it offers a theme rather than the three-way choice the account
 * menu carries: it names the appearance the next press lands on, and pressing
 * it is an explicit pick even when the theme on screen came from the operating
 * system. A control with one label cannot say "follow my machine".
 */
export function ThemeToggle({
  className,
}: Readonly<{ className?: string }> = {}) {
  const t = useT();
  const theme = useTheme();

  return (
    <button
      type="button"
      className={className ? `theme-toggle ${className}` : "theme-toggle"}
      aria-label={theme === "light" ? t("theme.toDark") : t("theme.toLight")}
      onClick={toggleTheme}
    >
      {theme === "light" ? <Moon aria-hidden /> : <Sun aria-hidden />}
    </button>
  );
}
