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

// The prompted languages are spelled as a map KEYED BY the contract enum, not
// as a list drawn from it, because only the map carries the guarantee that
// matters here. `satisfies Record<OnboardingLocale, true>` rejects a missing
// key as loudly as an unknown one, so the day the prompt library gains a
// language and the enum widens, this file stops compiling until the map
// follows. A `readonly OnboardingLocale[]` list cannot say that — a subset
// satisfies it happily, and the new language would keep falling back to
// English forever with nothing failing.
const PROMPTED = { en: true, de: true } satisfies Record<
  OnboardingLocale,
  true
>;

function isPrompted(locale: Locale): locale is Locale & OnboardingLocale {
  return Object.hasOwn(PROMPTED, locale);
}

export function onboardingLocale(locale: Locale): OnboardingLocale {
  // The contract makes this field REQUIRED, enumerates only the prompted
  // languages, and declares NO default — so it cannot be left off, and the
  // fallback is the UI's own choice rather than something inherited. It
  // chooses "en" because that is DEFAULT_LOCALE: the one language this app
  // already falls back to whenever it cannot serve the reader's own (A100).
  // Choosing separately here would give the product two floors instead of one.
  return isPrompted(locale) ? locale : "en";
}
