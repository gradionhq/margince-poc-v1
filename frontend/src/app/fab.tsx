import { useQueryClient } from "@tanstack/react-query";
import { Sparkles, X } from "lucide-react";
import { useState } from "react";
import { Button, Textarea } from "../design-system/atoms";
import { useT } from "../i18n";
import { AskSection } from "../screens/company360";
import { openCitation, organizationQueryKey } from "../screens/organizations";
import { NAV, RAIL_LESS_SCREENS } from "./nav";
import type { Route } from "./router";

// Record-aware "Ask about this" FAB + panel (B-EP09.6, AC-shell-8): mounts on
// every core screen EXCEPT the full Ask-AI surface, scoped to the current
// screen/record. The scope copy is load-bearing (03b): the agent reads only
// the RBAC ∩ Passport intersection — the panel must never imply more.

// The account a company route is showing, named. Read from the cache the
// page already filled rather than fetched again, and narrowed rather than
// asserted: a cache holds whatever was last written to it, and this panel
// would otherwise print whatever that was into its own heading.
function cachedAccountName(
  cached: unknown,
  fallback: string | undefined,
): string | undefined {
  if (
    typeof cached === "object" &&
    cached !== null &&
    "display_name" in cached &&
    typeof cached.display_name === "string"
  ) {
    return cached.display_name;
  }
  return fallback;
}

export function AskFab({ route }: Readonly<{ route: Route }>) {
  const t = useT();
  const client = useQueryClient();
  const [open, setOpen] = useState(false);

  if (route.screen === "ai" || RAIL_LESS_SCREENS.has(route.screen)) {
    return null;
  }

  // One account page, one ask surface. The company record used to carry its
  // own Ask card as well, so the same question had two boxes on one screen —
  // and only this one is present on every tab.
  const orgId = route.screen === "companies" ? route.id : undefined;
  const navItem = NAV.find((item) => item.screen === route.screen);
  const context =
    (orgId &&
      cachedAccountName(
        client.getQueryData(organizationQueryKey(orgId)),
        orgId,
      )) ||
    route.id ||
    (navItem ? t(navItem.labelKey) : route.screen);

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
          {orgId ? (
            <AskSection
              orgId={orgId}
              enabled
              onOpenRecord={(entityType, entityId) => {
                setOpen(false);
                openCitation(entityType, entityId);
              }}
            />
          ) : (
            <>
              <Textarea
                aria-label={t("fab.inputAria")}
                placeholder={t("fab.placeholder")}
                rows={3}
              />
              <Button variant="primary" small>
                {t("fab.send")}
              </Button>
            </>
          )}
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
