// The scale between an amount as a person types it and the integer this
// product stores.
//
// It is NOT presentation, and that is why it is its own module beside the
// formatters rather than inside them: the same table governs the write
// direction, where locale must never reach, and format.ts's own header says
// locale never flows back into storage. These functions take no locale
// deliberately — a currency's minor-unit count is a property of the currency,
// not of who is reading.
//
// It exists because the write direction had no owner at all. Eleven call sites
// spelled `Math.round(Number(amount) * 100)` and `valueMinor / 100`, which is
// right for the euro and wrong for every currency that is not two-decimal: VND,
// JPY and KRW have no minor unit, so a dong price typed as 18,000,000 was
// stored as 1,800,000,000, and a Kuwaiti dinar — three digits — was stored at a
// tenth. The display side had already got this right through Intl; only the
// writers had not, and the two disagreeing is why a wrong figure could survive
// a round trip and look correct on the way back.

// minorUnitDigits reports how many minor-unit digits a currency carries.
//
// Read from Intl rather than from a table of our own, because the runtime
// already ships ISO-4217's answer and maintains it. An unknown or malformed
// code answers 2 — which is ISO's own default and what Intl itself falls back
// to — rather than throwing: a currency we cannot place is still a currency
// somebody typed an amount into, and refusing to scale it would silently store
// the raw digits as minor units.
export function minorUnitDigits(currency: string): number {
  try {
    return (
      new Intl.NumberFormat("en", {
        style: "currency",
        currency,
      }).resolvedOptions().maximumFractionDigits ?? 2
    );
  } catch {
    // Intl throws on a code that is not three letters. The amount still has to
    // go somewhere, and two digits is what the schema's own default assumes.
    return 2;
  }
}

// toMinorUnits converts a major-unit amount — what the person typed — into the
// integer the API stores. Rounded, because a person may type more decimals than
// the currency has and the alternative is a truncated cent.
export function toMinorUnits(major: number, currency: string): number {
  return Math.round(major * 10 ** minorUnitDigits(currency));
}

// toMajorUnits is the inverse, for seeding an input from a stored amount.
//
// It returns a number and not a string on purpose: the caller decides how many
// digits to show, and a currency with no minor unit must not be handed a
// ".00" that says it has one.
export function toMajorUnits(amountMinor: number, currency: string): number {
  return amountMinor / 10 ** minorUnitDigits(currency);
}
