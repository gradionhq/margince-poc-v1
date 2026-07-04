import {
  BarChart3,
  Building2,
  CheckSquare,
  Home,
  Inbox,
  type LucideIcon,
  Sparkles,
  Target,
  UserPlus,
  Users,
} from "lucide-react";
import type { MessageKey } from "../i18n/en";

// The canonical 9-item rail (§2b / AC-shell-1) — order is normative and
// shell.test.tsx pins it. Badge counts come from live data; entries without
// data render no badge (the "show nothing that isn't true" rule).
export type NavItem = {
  screen: string;
  labelKey: MessageKey;
  icon: LucideIcon;
};

export const NAV: readonly NavItem[] = [
  { screen: "home", labelKey: "nav.home", icon: Home },
  { screen: "contacts", labelKey: "nav.contacts", icon: Users },
  { screen: "companies", labelKey: "nav.companies", icon: Building2 },
  { screen: "leads", labelKey: "nav.leads", icon: UserPlus },
  { screen: "deals", labelKey: "nav.deals", icon: Target },
  { screen: "tasks", labelKey: "nav.tasks", icon: CheckSquare },
  { screen: "inbox", labelKey: "nav.inbox", icon: Inbox },
  { screen: "reports", labelKey: "nav.reports", icon: BarChart3 },
  { screen: "ai", labelKey: "nav.ai", icon: Sparkles },
];

// Documented rail-less exceptions (AC-shell layout exception): onboarding,
// the public booking page, and the extension client surfaces.
export const RAIL_LESS_SCREENS: ReadonlySet<string> = new Set([
  "onboarding",
  "book",
  "client",
]);
