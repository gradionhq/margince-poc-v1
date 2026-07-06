import { Sparkles, X } from "lucide-react";
import { useState } from "react";
import { Button } from "../design-system/atoms";
import { useT } from "../i18n";
import { NAV, RAIL_LESS_SCREENS } from "./nav";
import type { Route } from "./router";

// Record-aware "Ask about this" FAB + panel (B-EP09.6, AC-shell-8): mounts on
// every core screen EXCEPT the full Ask-AI surface, scoped to the current
// screen/record. The scope copy is load-bearing (03b): the agent reads only
// the RBAC ∩ Passport intersection — the panel must never imply more.

export function AskFab({ route }: Readonly<{ route: Route }>) {
  const t = useT();
  const [open, setOpen] = useState(false);

  if (route.screen === "ai" || RAIL_LESS_SCREENS.has(route.screen)) {
    return null;
  }

  const navItem = NAV.find((item) => item.screen === route.screen);
  const context = route.id ?? (navItem ? t(navItem.labelKey) : route.screen);

  return (
    <div className="askfab-root">
      {open && (
        <div
          className="askfab-panel card"
          // NOSONAR: inline anchored panel, not a native modal dialog; styling and conditional mount don't map to <dialog>
          role="dialog"
          aria-label={t("fab.panelAria")}
        >
          <div className="askfab-head">
            <span className="t-label">{t("fab.context", { context })}</span>
            <button
              type="button"
              className="iconbtn"
              aria-label={t("fab.close")}
              onClick={() => setOpen(false)}
            >
              <X aria-hidden />
            </button>
          </div>
          <p className="t-caption askfab-scope">{t("fab.scope")}</p>
          <textarea
            className="textarea askfab-input"
            aria-label={t("fab.inputAria")}
            placeholder={t("fab.placeholder")}
            rows={3}
          />
          <Button variant="primary" small>
            {t("fab.send")}
          </Button>
        </div>
      )}
      <button
        type="button"
        className="askfab"
        aria-label={t("fab.open")}
        onClick={() => setOpen((value) => !value)}
      >
        <Sparkles aria-hidden />
      </button>
    </div>
  );
}
