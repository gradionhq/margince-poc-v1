import { extensionCopy } from "@composition/copy";
import {
  createContext,
  type ReactNode,
  useContext,
  useEffect,
  useMemo,
  useState,
} from "react";
import { de } from "./de";
import { en, type MessageKey } from "./en";
import { vi } from "./vi";

// Locale is a presentation concern only (architecture/10 §3): it resolves at
// the render edge and never participates in storage or math. The resolution
// order is user.locale → workspace.locale → the browser's Accept-Language →
// en-GB (A100). Until /v1/me carries a locale, the browser guess is the best
// signal we have, and A100 stays the floor when the browser asks for a
// language we don't ship. An explicit `initial` (later fed from /v1/me)
// always wins; the switch flips it locally after mount.

// The catalog registry is what we ship. `Locale` derives from it, so the type
// needs no edit when a locale arrives. `LOCALES` below does NOT derive: it is
// hand-ordered because it also fixes the order the switcher shows, and both
// the switcher and browser detection read that written list. `satisfies
// readonly Locale[]` proves each entry is a real locale — it does not prove
// the list is COMPLETE. Completeness is enforced by i18n.test.ts.
export const catalogs = { en, de, vi } satisfies Record<
  string,
  Record<MessageKey, string>
>;

export type Locale = keyof typeof catalogs;

/**
 * A key an extension unit's own copy supplies, namespaced to the unit the way
 * its tables and RBAC objects are (`extCrmDemo.title`).
 *
 * A template-literal type rather than a union, for the reason
 * ExtensionRbacObject is one: this file cannot enumerate what an installation
 * enabled, and a union would have to be generated and would then make the
 * vanilla and composed lanes different programs. The GENERATOR checks the real
 * rule — that every key a unit ships begins with that unit's own prefix.
 */
export type ExtensionMessageKey = `ext${string}`;

/**
 * The copy an enabled unit contributed, per locale, merged by gen-composition.
 * Empty on a vanilla tree.
 */
const unitCopy: Partial<Record<Locale, Record<string, string>>> = extensionCopy;

// Display order for the switcher. `satisfies` proves each entry is a real
// locale; i18n.test.ts proves the list is exhaustive.
export const LOCALES = ["en", "de", "vi"] as const satisfies readonly Locale[];

export const DEFAULT_LOCALE: Locale = "en";

function isLocale(value: string): value is Locale {
  return LOCALES.some((locale) => locale === value);
}

// The endonym key for a locale. The template literal is checked against
// MessageKey, so adding a locale without adding its `locale.name.<code>` key
// fails the build rather than rendering a raw key at runtime.
export function localeNameKey(locale: Locale): MessageKey {
  return `locale.name.${locale}`;
}

// detectLocale reads the visitor's own language preference and maps it to a
// locale we ship, falling back to the A100 default when none of the shipped
// locales is asked for. It never throws off-browser (SSR, tests): an absent
// navigator yields the default.
export function detectLocale(
  languages: readonly string[] = globalThis.navigator?.languages ??
    (globalThis.navigator?.language ? [globalThis.navigator.language] : []),
): Locale {
  for (const tag of languages) {
    const base = tag.toLowerCase().split("-")[0];
    if (isLocale(base)) {
      return base;
    }
  }
  return DEFAULT_LOCALE;
}

export function translate(
  locale: Locale,
  key: MessageKey | ExtensionMessageKey,
  params?: Record<string, string | number>,
): string {
  // CORE FIRST, and a unit second. The generator already refuses a key outside
  // the unit's own namespace, so the two sets cannot overlap today — this
  // ordering is what keeps that true if the namespace rule were ever loosened:
  // a unit must not be able to change what a core string says.
  //
  // A key neither side carries falls back to the key itself, which is what an
  // untranslated string has always done here and reads as an obvious defect on
  // the page rather than as an empty element.
  const message =
    (catalogs[locale] as Record<string, string>)[key] ??
    unitCopy[locale]?.[key] ??
    key;
  if (!params) {
    return message;
  }
  return message.replace(/\{(\w+)\}/g, (whole, name: string) =>
    name in params ? String(params[name]) : whole,
  );
}

type LocaleContextValue = {
  locale: Locale;
  setLocale: (locale: Locale) => void;
};

const LocaleContext = createContext<LocaleContextValue>({
  locale: DEFAULT_LOCALE,
  setLocale: () => {},
});

export function LocaleProvider({
  initial,
  children,
}: Readonly<{
  initial?: Locale;
  children: ReactNode;
}>) {
  // An explicit initial (a server-provided locale, once /v1/me carries one)
  // is authoritative; otherwise fall to the browser's own preference.
  const [locale, setLocale] = useState<Locale>(() => initial ?? detectLocale());
  /*
   * The document's own language follows the catalog. index.html can only ship a
   * static `lang`, so without this every German reader gets German text inside a
   * document declared English — a screen reader then applies English phonemes to
   * German words, which is the difference between a page that can be listened to
   * and one that cannot. WCAG 3.1.1.
   *
   * It fails on FIRST LOAD, not only after the switcher is used, because
   * `detectLocale` reads the browser's own preference — so the reader most likely
   * to need it is the one who never touches the switch.
   */
  useEffect(() => {
    document.documentElement.lang = locale;
  }, [locale]);
  const value = useMemo(() => ({ locale, setLocale }), [locale]);
  return (
    <LocaleContext.Provider value={value}>{children}</LocaleContext.Provider>
  );
}

export function useLocale(): LocaleContextValue {
  return useContext(LocaleContext);
}

export function useT() {
  const { locale } = useContext(LocaleContext);
  return (key: MessageKey, params?: Record<string, string | number>) =>
    translate(locale, key, params);
}
