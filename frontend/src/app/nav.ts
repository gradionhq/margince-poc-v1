import {
  BarChart3,
  Building2,
  CheckSquare,
  Home,
  type LucideIcon,
  ShieldCheck,
  Sparkles,
  Target,
  UserPlus,
  Users,
  Zap,
} from "lucide-react";
import type { MessageKey } from "../i18n/en";

// The canonical 10-item nav (00-design-language.md §nav, A72/ADR-0035 Am.1
// promoted Automations to primary nav). Order is normative and shell.test.tsx
// pins it. Home stands alone above three labeled groups; the groups are the
// expanded sidebar's own structure and collapse to hairline rules at 64px, so
// the collapsed rail is the flat 10-item list WDS-NAV-1 describes.
//
// `screen` is the route id and never changes with a label: `deals` presents as
// Pipeline (it routes to the pipeline surface) and `inbox` presents as
// Approvals (it is a governance surface, not a mailbox).
export type NavItem = {
  screen: string;
  labelKey: MessageKey;
  icon: LucideIcon;
};

export type NavGroup = {
  headingKey?: MessageKey;
  items: readonly NavItem[];
};

export const NAV_GROUPS: readonly NavGroup[] = [
  { items: [{ screen: "home", labelKey: "nav.home", icon: Home }] },
  {
    headingKey: "nav.group.records",
    items: [
      { screen: "contacts", labelKey: "nav.contacts", icon: Users },
      { screen: "companies", labelKey: "nav.companies", icon: Building2 },
      { screen: "leads", labelKey: "nav.leads", icon: UserPlus },
    ],
  },
  {
    headingKey: "nav.group.work",
    items: [
      { screen: "deals", labelKey: "nav.deals", icon: Target },
      { screen: "tasks", labelKey: "nav.tasks", icon: CheckSquare },
      // ShieldCheck, not another check-in-square: Approvals sits directly under
      // Tasks and a near-identical glyph makes the pair unreadable at 20px.
      { screen: "inbox", labelKey: "nav.inbox", icon: ShieldCheck },
    ],
  },
  {
    headingKey: "nav.group.intelligence",
    items: [
      { screen: "reports", labelKey: "nav.reports", icon: BarChart3 },
      { screen: "automations", labelKey: "nav.automations", icon: Zap },
      { screen: "ai", labelKey: "nav.ai", icon: Sparkles },
    ],
  },
];

export const NAV: readonly NavItem[] = NAV_GROUPS.flatMap(
  (group) => group.items,
);

// A badge counts only what wants a human's attention (Tasks due, Approvals
// waiting). Ambient totals are deliberately absent: the list endpoints are
// keyset-paginated and are not known to return one, and a decorative count
// contradicts the badge rule.
export const BADGE_SCREENS: ReadonlySet<string> = new Set(["tasks", "inbox"]);

// At phone width the sidebar becomes a bottom bar, which fits four thumb-sized
// destinations plus More — ten would need horizontal scrolling, and a nav you
// have to scroll is a nav you cannot see. Approvals is non-negotiable here: the
// 390px approval path is required for V1.
export const MOBILE_PRIMARY: ReadonlySet<string> = new Set([
  "home",
  "contacts",
  "deals",
  "inbox",
]);

// Documented rail-less exceptions (AC-shell layout exception): onboarding,
// the public booking page, the extension client surfaces, and the OAuth
// consent screen — a human lending an agent their authority reads it apart
// from the rest of the app, not framed inside it.
export const RAIL_LESS_SCREENS: ReadonlySet<string> = new Set([
  "onboarding",
  "book",
  "client",
  "preferences",
  "oauth-consent",
]);
