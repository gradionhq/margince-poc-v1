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
// the render edge (user.locale → workspace.locale → de-DE default, A24) and
// never participates in storage or math. Until /v1/me is wired in, the app
// mounts with the A24 default and the switch flips it locally.

export type Locale = "de" | "en";
export const DEFAULT_LOCALE: Locale = "de";

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
  initial = DEFAULT_LOCALE,
  children,
}: {
  initial?: Locale;
  children: ReactNode;
}) {
  const [locale, setLocale] = useState<Locale>(initial);
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
