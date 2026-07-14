import type { ReactNode } from "react";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { Badge } from "./atoms";

// RBAC presentation primitives: how a principal's role and a withheld value
// read on screen. Presentation ONLY — the server's admission gates are the
// authority on what a role may do and what the wire discloses.

// The five seeded system roles. A workspace-defined role key
// outside this set renders as its raw key — honest, never invented copy.
const ROLE_LABEL_KEYS: Record<string, MessageKey> = {
  admin: "role.admin",
  manager: "role.manager",
  rep: "role.rep",
  read_only: "role.readOnly",
  ops: "role.ops",
};

export function RoleBadge({ roleKey }: Readonly<{ roleKey: string }>) {
  const t = useT();
  const labelKey = ROLE_LABEL_KEYS[roleKey];
  return <Badge tone="accent">{labelKey ? t(labelKey) : roleKey}</Badge>;
}

// A withheld value must read as "withheld", not "absent" — omitting the node
// is indistinguishable from there being no data. `masked` renders a visible
// mask token; `visible` passes the value through. Masking is presentation
// over values the wire already withholds (a passport token today; field-level
// masks when B-EP03.4 lands) — it never hides data the client was given as a
// substitute for a server gate.
export function FieldGuard({
  mode,
  children,
}: Readonly<{ mode: "visible" | "masked"; children?: ReactNode }>) {
  const t = useT();
  if (mode === "masked") {
    return (
      // NOSONAR: CSS-only mask glyph, same pattern as AutonomyDot — role=img
      // gives assistive tech the "withheld value" semantics.
      <span
        role="img"
        aria-label={t("rbac.masked")}
        className="t-mono"
        style={{ userSelect: "none", letterSpacing: "0.2em" }}
      >
        ••••
      </span>
    );
  }
  return <>{children}</>;
}
