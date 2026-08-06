// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { Moon, Sun } from "lucide-react";
import { useT } from "../i18n";
import "./theme-toggle.css";
import { useTheme } from "./theme";

/**
 * The light/dark control, wherever a surface has room for it.
 *
 * The top bar had the only one for a long time, which left the two surfaces a
 * reader meets FIRST — sign-in and onboarding — with no way to change a theme
 * they can already see. This carries its own styling rather than borrowing the
 * top bar's `.iconbtn` rule, because that rule is scoped to `.topbar` and
 * neither of those surfaces renders one.
 */
export function ThemeToggle({
  className,
}: Readonly<{ className?: string }> = {}) {
  const t = useT();
  const [theme, toggle] = useTheme();

  return (
    <button
      type="button"
      className={className ? `theme-toggle ${className}` : "theme-toggle"}
      aria-label={theme === "light" ? t("theme.toDark") : t("theme.toLight")}
      onClick={toggle}
    >
      {theme === "light" ? <Moon aria-hidden /> : <Sun aria-hidden />}
    </button>
  );
}
