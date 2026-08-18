import type { Route } from "../app/router";

// The contact record's tabs, and the ONE way to address one.
//
// They live apart from the page that draws them because three other surfaces
// send a reader to a tab — the header's verbs and the rail's "view all" — and
// `navigate` takes `id2` as a bare string. When the tab set last changed, two
// of those call sites kept pointing at ids that no longer existed: the router
// fell back to Overview and nothing anywhere said so. `personTabRoute` is
// typed on `PersonTab`, so the next rename fails to compile instead.

/**
 * The six tabs, in the concept's order (§5.4). URL-addressable, so a tab
 * survives a reload and can be linked to.
 *
 * Activity and History are ONE tab, as they are on the account page: what was
 * said to a contact and what was changed about them are one chronology to the
 * person reading them, and the filter above the list is what separates them.
 */
export const PERSON_TABS = [
  "overview",
  "timeline",
  "deals",
  "meetings",
  "research",
  "documents",
] as const;

export type PersonTab = (typeof PERSON_TABS)[number];

/** isPersonTab narrows a URL segment, which is any string a reader can type. */
export function isPersonTab(value: string | undefined): value is PersonTab {
  return PERSON_TABS.some((tab) => tab === value);
}

/** personTabRoute addresses one tab of one contact. */
export function personTabRoute(id: string, tab: PersonTab): Route {
  return { screen: "contacts", id, id2: tab };
}
