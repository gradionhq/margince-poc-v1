import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { type RenderResult, render as rtlRender } from "@testing-library/react";
import { Database, ShieldCheck, UserRound } from "lucide-react";
import type { ReactNode } from "react";
import { vi } from "vitest";
import { LocaleProvider } from "../../i18n";
import type { NavSection } from "../nav";

// The doubles the shell's chrome suites share, in one place.
//
// shell.test.tsx and rail.test.tsx are two halves of one surface, so they need
// the same mount (a fresh query client under the locale provider), the same
// phone-width stub and the same section fixture. These are test doubles rather
// than production — a duplicated copy would be legitimate for a pair of ten-line
// helpers — but `fixtureSection` is a fixture of the NavSection CONTRACT, with a
// synthetic third level nothing in production publishes, and two copies of that
// drift the moment the shape changes. So it lives here, and the render harness
// and viewport stub come with it rather than being split across two homes.
//
// It is NOT a *.test.* file, on purpose, for the same reason
// src/testing/appharness.tsx is not: the design-system and lint gates skip test
// files, and a helper the real chrome is mounted through answers to the app's
// rules.

/**
 * A query client with retries off, fresh per mount.
 *
 * Never a shared one: react-query caches by key, so a client carried between
 * tests would serve one case's record to the next.
 */
export const newClient = () =>
  new QueryClient({ defaultOptions: { queries: { retry: false } } });

/** Mount under a caller's own client — for a case that seeds the cache first. */
export const renderWith = (client: QueryClient, ui: ReactNode): RenderResult =>
  rtlRender(
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">{ui}</LocaleProvider>
    </QueryClientProvider>,
  );

/** Mount under a client of its own — what a case that reads no cache wants. */
export const render = (ui: ReactNode): RenderResult =>
  renderWith(newClient(), ui);

/**
 * The shell cannot render without a way to open the palette; the handler belongs
 * to the top bar's search button and is proved there, so cases here pass one
 * that records nothing.
 */
export const ignoreSearch = () => undefined;

/**
 * Phone width, for the chrome that has to KNOW it rather than merely be laid out
 * by it (app/viewport.ts). jsdom's own window answers every media query with
 * false, so the wide arrangement is what every other case renders — which is the
 * honest default and exactly what the desktop assertions rely on.
 *
 * Only the QUERY the app asks for matches: a stub that answered true to
 * everything would also tell the theme the reader prefers a dark one.
 */
export function stubPhoneViewport(): void {
  vi.stubGlobal("matchMedia", (query: string) => ({
    matches: query === "(max-width: 700px)",
    media: query,
    addEventListener: () => undefined,
    removeEventListener: () => undefined,
  }));
}

/**
 * A section with a THIRD level under one of its entries.
 *
 * Settings — the only real section the app ships — is two levels deep, so
 * nothing in production would prove the renderer takes its depth from the data
 * rather than from a hard-coded pair of levels.
 */
export function fixtureSection(activeId?: string): NavSection {
  return {
    screen: "settings",
    titleKey: "nav.settings",
    activeId,
    groups: [
      {
        headingKey: "settings.group.you",
        items: [
          { id: "account", labelKey: "settings.tab.account", icon: UserRound },
        ],
      },
      {
        headingKey: "settings.group.admin",
        items: [
          {
            // The child level is SYNTHETIC: no settings entry publishes children,
            // so nothing in production proves the renderer takes its depth from the
            // data. The parent and child deliberately borrow labels rather than
            // entry IDS — `#/settings/privacy/data-model` would name two real
            // sibling entries as parent and child, which the settings level does
            // not publish and a reader would take for the real shape.
            id: "deep",
            labelKey: "settings.tab.privacy",
            icon: ShieldCheck,
            children: [
              {
                id: "deeper",
                labelKey: "settings.tab.data-model",
                icon: Database,
              },
            ],
          },
        ],
      },
    ],
  };
}
