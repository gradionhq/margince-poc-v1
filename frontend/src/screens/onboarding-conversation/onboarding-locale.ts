import type { components } from "../../api/schema";
import type { Locale } from "../../i18n";

// The onboarding conversation asks the server for copy in the reader's own
// language, and the contract enumerates the languages its prompt library
// covers — a narrower set than the UI catalogs, which grow a locale as soon
// as their strings are translated. Sending a locale the enum does not carry
// would earn a 422 mid-onboarding, so a reader outside the prompted set gets
// the contract's own default instead. This is the ONE place the two sets are
// reconciled; every call that puts a locale on this wire goes through it.
export type OnboardingLocale =
  components["schemas"]["OnboardingCompanyMessageRequest"]["locale"];

// Widening the enum upstream fails to compile here until this list follows,
// which is how the reconciliation stays honest rather than drifting silently.
const PROMPTED = ["en", "de"] as const satisfies readonly OnboardingLocale[];

function isPrompted(locale: Locale): locale is Locale & OnboardingLocale {
  return PROMPTED.some((prompted) => prompted === locale);
}

export function onboardingLocale(locale: Locale): OnboardingLocale {
  // "en" is the contract's declared default for this field, not a second
  // opinion about the UI's default locale.
  return isPrompted(locale) ? locale : "en";
}
