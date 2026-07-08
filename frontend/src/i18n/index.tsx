import {
  createContext,
  type ReactNode,
  useContext,
  useMemo,
  useState,
} from "react";
import { de } from "./de";
import { en, type MessageKey } from "./en";

// Locale is a presentation concern only (architecture/10 §3): it resolves at
// the render edge and never participates in storage or math. The resolution
// order is user.locale → workspace.locale → the browser's Accept-Language →
// en-GB (A100). Until /v1/me carries a locale, the browser guess is the best
// signal we have, and A100 stays the floor when the browser asks for a
// language we don't ship. An explicit `initial` (later fed from /v1/me)
// always wins; the switch flips it locally after mount.

export type Locale = "de" | "en";
export const DEFAULT_LOCALE: Locale = "en";

// detectLocale reads the visitor's own language preference and maps it to a
// locale we ship, falling back to the A100 default when neither German nor
// English is asked for. It never throws off-browser (SSR, tests): an absent
// navigator yields the default.
export function detectLocale(
  languages: readonly string[] = globalThis.navigator?.languages ??
    (globalThis.navigator?.language ? [globalThis.navigator.language] : []),
): Locale {
  for (const tag of languages) {
    const base = tag.toLowerCase().split("-")[0];
    if (base === "de" || base === "en") {
      return base;
    }
  }
  return DEFAULT_LOCALE;
}

const catalogs: Record<Locale, Record<MessageKey, string>> = { de, en };

export function translate(
  locale: Locale,
  key: MessageKey,
  params?: Record<string, string | number>,
): string {
  const message = catalogs[locale][key];
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
